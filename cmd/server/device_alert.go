package main

import (
	"context"
	"database/sql"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"cups-web/internal/devicealert"
	"cups-web/internal/ipp"
	"cups-web/internal/store"
)

// 设备异常巡检。
//
// ── 它解决的问题 ──
//
// 打印机卡纸不会有人告诉你。真正的故障链是：半夜卡纸 → 之后每一单都送印失败
// → 系统按规则自动退款 → 第二天早上才发现「昨晚 40 单全退了，一分钱没收」。
//
// 而缺纸、卡纸、门没关、耗材耗尽这些信号【早就在手里】—— printer-state-reasons
// 与 marker-levels，只是从来没人看。见 printer_device.go 顶部：cupsd 报的那些
// 值有不少是缓存的旧印象，但打印机【自己报的】是真的，直连问一次就有。
//
// 每分钟：取信号 → 判定（internal/devicealert）→ 落库（device_events）→
// 必要时发通知（alert_notify.go）。

const (
	// deviceAlertInterval 巡检间隔。
	//
	// 60 秒：一轮要为每台打印机直连问一次（单次 2 秒超时封顶），十台机器最坏
	// 二十秒。再密只是替打印机做无用功，再疏则「卡纸 → 通知」之间多出一段
	// 没人处理的窗口。
	deviceAlertInterval = 60 * time.Second

	// deviceAlertDebounce 是「异常必须连续存在多久才开事件并通知」。
	//
	// 2 分钟 = 连续两轮都看到。去掉「一次网络抖动就报一次」的假阳性 ——
	// 而假阳性正是让人把告警整个关掉的原因：第一次是「打印机又抽风了」，
	// 第三次之后连真告警也没人点开了。
	//
	// ⚠️ 它只影响【开事件】。恢复是立即的：已经在响的告警一看到好转就关，
	// 不必再等一轮 —— 拖一轮等于让已经解决的事继续挂在横幅上。
	deviceAlertDebounce = 2 * time.Minute
)

// alertPending 记「这个毛病是从什么时候开始连续出现的」。
//
// ⚠️ 只在内存里，进程重启即丢。这是刻意的取舍：丢了最多让一次抖动重新计时
// （多等一分钟），而落库要多一张表、多一轮写。为去抖动付这个代价不划算。
var alertPending = struct {
	mu sync.Mutex
	m  map[string]time.Time
}{m: map[string]time.Time{}}

// alertPendingKey 是「哪台机器的哪个毛病」。
//
// 用 \x00 连接而不是 "|" 之类：URI 里可能出现任何可见字符（队列名允许），
// 用可见分隔符就有拼出同一个键的可能，而那时两台不同机器的同类异常会互相
// 顶掉计时。
func alertPendingKey(printerURI, kind string) string {
	return printerURI + "\x00" + kind
}

// confirmIssue 判断一个毛病是否已经连续出现够久（去抖动后可以开事件）。
//
// 第一次看到只记下时刻并返回 false；此后每轮巡检都会再看到，到点返回 true。
// 同时返回【第一次看到它的时刻】—— 那才是「这个问题从什么时候开始」的答案，
// 用它当事件的 started_at，界面上的「已持续多久」才不会从「确认的那一刻」起算。
func confirmIssue(key string, now time.Time) (bool, time.Time) {
	alertPending.mu.Lock()
	defer alertPending.mu.Unlock()
	first, ok := alertPending.m[key]
	if !ok {
		alertPending.m[key] = now
		return false, now
	}
	return now.Sub(first) >= deviceAlertDebounce, first
}

// forgetIssues 清掉这台打印机上【已经不再出现】的计时。
//
// 不清的话，「卡纸一分钟 → 好了 → 十分钟后真的卡住」会在第二次出现的那一瞬间
// 就被当成「已经持续十分钟」，直接开事件并通知 —— 去抖动等于没做。
func forgetIssues(printerURI string, stillPresent map[string]bool) {
	prefix := printerURI + "\x00"
	alertPending.mu.Lock()
	defer alertPending.mu.Unlock()
	for k := range alertPending.m {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		if !stillPresent[strings.TrimPrefix(k, prefix)] {
			delete(alertPending.m, k)
		}
	}
}

// pruneAlertPending 丢掉已经不在队列列表里的打印机的计时，防止这张表随
// 「删掉又新建的队列」无限增长。与 deviceProbeCache 的清理是同一个理由。
func pruneAlertPending(seen map[string]bool) {
	alertPending.mu.Lock()
	defer alertPending.mu.Unlock()
	for k := range alertPending.m {
		uri, _, ok := strings.Cut(k, "\x00")
		if !ok || !seen[uri] {
			delete(alertPending.m, k)
		}
	}
}

func startDeviceAlertWorker(s *store.Store) {
	go func() {
		for {
			deviceAlertTick(context.Background(), s)
			time.Sleep(deviceAlertInterval)
		}
	}()
}

// printerSignals 是一台打印机这一轮的判定结果。
type printerSignals struct {
	URI  string
	Name string
	// Evaluated 为假表示这一轮【根本没取到信号】（cupsd 没答上来）。
	//
	// ⚠️ 必须与「取到了、一切正常」区分开：把问不到当成正常，会让正在响的
	// 告警在 cupsd 抽风的那一分钟里被判定成「已恢复」，然后下一轮又「新出现」
	// ——一条假恢复 + 一条假告警，而打印机什么都没发生。
	Evaluated bool
	Issues    []devicealert.Issue
	// FirstSeen 是每个类别第一次被看到的时刻，用于落库时的 started_at。
	FirstSeen map[string]time.Time
}

// deviceAlertTick 跑一轮巡检。所有错误都只记日志：一次巡检失败不该让进程
// 有任何反应，下一分钟重来就是了。
func deviceAlertTick(ctx context.Context, s *store.Store) {
	cfg, err := readAlertConfig(ctx, s)
	if err != nil {
		log.Printf("[device-alert] 读取告警配置失败，跳过本轮: %v", err)
		return
	}
	// 开关关掉 = 这件事整个不做：不巡检、不记事件、不通知。
	// 🚫 不要理解成「只关通知」—— 那样管理员关掉之后还会看到事件在涨，
	// 而没有任何东西告诉他为什么。
	if !cfg.Enabled {
		return
	}

	cupsHost := os.Getenv("CUPS_HOST")
	if cupsHost == "" {
		cupsHost = "localhost"
	}
	printers, err := ipp.ListPrinters(cupsHost)
	if err != nil {
		// 列不到队列就【整轮跳过】。
		//
		// 🚫 特别注意：不能把「cupsd 问不到」当成「所有打印机都不可达」。
		// cupsd 重启的那一分钟里，那会一次性给每台机器记一条「设备不可达」
		// 并发一遍通知，而一台打印机都没坏。
		log.Printf("[device-alert] 列队列失败，跳过本轮: %v", err)
		return
	}

	now := time.Now()

	// ── 第一步：取信号 + 判定 + 去抖动。全是网络与纯计算，不碰数据库。 ──
	signals := make([]printerSignals, 0, len(printers))
	seen := make(map[string]bool, len(printers))
	for _, p := range printers {
		if p.URI == "" {
			continue
		}
		seen[p.URI] = true

		issues, ok := evaluatePrinter(p.URI, cfg.SupplyLowPct)
		if !ok {
			// 这一轮没取到信号：原样传下去，让落库那一步【整台跳过】。
			signals = append(signals, printerSignals{URI: p.URI, Name: p.Name})
			continue
		}

		stillPresent := make(map[string]bool, len(issues))
		for _, is := range issues {
			stillPresent[is.Kind] = true
		}
		forgetIssues(p.URI, stillPresent)

		// 去抖动：还没连续出现够久的不进入后续处理。
		confirmed := make([]devicealert.Issue, 0, len(issues))
		firstSeen := make(map[string]time.Time, len(issues))
		for _, is := range issues {
			c, since := confirmIssue(alertPendingKey(p.URI, is.Kind), now)
			firstSeen[is.Kind] = since
			if c {
				confirmed = append(confirmed, is)
			}
		}
		signals = append(signals, printerSignals{
			URI: p.URI, Name: p.Name, Evaluated: true,
			Issues: confirmed, FirstSeen: firstSeen,
		})
	}
	pruneAlertPending(seen)

	// ── 第二步：落库。一轮巡检的所有写操作在同一个事务里。 ──
	//
	// 分开写会留下「一半机器的新状态 + 一半旧状态」，下一轮巡检就会把已经
	// 恢复的异常当成新出现的再通知一遍。
	nowStr := now.UTC().Format(time.RFC3339)

	type pendingNotice struct {
		notice alertNotice
		// eventID 是这条通知对应的库里的行，仅【问题】通知需要回写 notified_at。
		eventID int64
	}
	var toNotify []pendingNotice

	err = s.WithTx(ctx, false, func(tx *sql.Tx) error {
		mutes, err := store.ListAllDeviceMutes(ctx, tx)
		if err != nil {
			return err
		}

		for _, sig := range signals {
			// 这一轮没取到信号 → 整台跳过：不开关事件、不动静音。
			// 保持上一轮的状态是这里唯一诚实的做法（见 printerSignals.Evaluated）。
			if !sig.Evaluated {
				continue
			}
			muted := mutes[sig.URI]

			// 静音：管理员已经说过「这台的这个毛病我知道」。
			active := make([]devicealert.Issue, 0, len(sig.Issues))
			for _, is := range sig.Issues {
				if muted[is.Kind] {
					continue
				}
				active = append(active, is)
			}

			keepKinds := make([]string, 0, len(active))
			for _, is := range active {
				keepKinds = append(keepKinds, is.Kind)

				startedAt := nowStr
				if t := sig.FirstSeen[is.Kind]; !t.IsZero() {
					startedAt = t.UTC().Format(time.RFC3339)
				}
				ev, created, err := store.OpenDeviceEvent(ctx, tx, store.DeviceEvent{
					PrinterURI: sig.URI,
					Kind:       is.Kind,
					Severity:   is.Severity,
					Detail:     is.Detail,
					StartedAt:  startedAt,
					UpdatedAt:  nowStr,
				})
				if err != nil {
					return err
				}
				if created || shouldRepeatAlert(ev, cfg, now) {
					toNotify = append(toNotify, pendingNotice{
						notice:  noticeFromEvent(ev, sig.Name, false),
						eventID: ev.ID,
					})
				}
			}

			// 本轮不再出现的类别 → 关掉事件，并解除它们的静音。
			resolvedEvents, err := store.ResolveDeviceEvents(ctx, tx, sig.URI, keepKinds, nowStr)
			if err != nil {
				return err
			}
			for _, ev := range resolvedEvents {
				// 只对「确实通知过的问题」发恢复消息。从没通知过的（比如
				// 首个问题通知发失败了）再说一句「已恢复」只会让人困惑。
				if ev.NotifiedAt == "" {
					continue
				}
				toNotify = append(toNotify, pendingNotice{
					notice: noticeFromEvent(ev, sig.Name, true),
				})
			}

			// 静音的解除条件：该类别【本轮完全没有出现】。
			//
			// 用 sig.Issues（过滤前的原始观测）判断，不能用 active ——
			// 静音中的类别本来就进不了 active，用 active 判断的话静音
			// 永远不会被解除，等于「这台机器以后再也不告警了」。
			var staleMutes []string
			observed := make(map[string]bool, len(sig.Issues))
			for _, is := range sig.Issues {
				observed[is.Kind] = true
			}
			for kind := range muted {
				if !observed[kind] {
					staleMutes = append(staleMutes, kind)
				}
			}
			if err := store.ClearDeviceKindMutes(ctx, tx, sig.URI, staleMutes); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		log.Printf("[device-alert] 本轮落库失败: %v", err)
		return
	}

	// ── 第三步：发通知。在事务【之外】—— 网络调用不该占着数据库事务。 ──
	for _, p := range toNotify {
		if !cfg.hasWebhook() {
			// 没配地址：只记事件不发消息。管理端页面上仍然看得到。
			continue
		}
		text := formatAlertNotice(p.notice)
		if err := postAlert(ctx, cfg, text); err != nil {
			// 🚫 失败时【不】写 notified_at：下一轮会重试。这条是
			// 「webhook 地址少打一个字符」不至于静默吃掉整轮告警的全部依据。
			log.Printf("[device-alert] 通知发送失败（下一轮会重试）: %v", err)
			continue
		}
		if p.eventID == 0 {
			continue
		}
		if err := markAlertNotified(ctx, s, p.eventID, nowStr); err != nil {
			log.Printf("[device-alert] 记录通知时间失败（可能导致重发）: %v", err)
		}
	}
}

// shouldRepeatAlert 判断一条【已存在】的事件要不要再发一次提醒。
//
// 两种要重发的情形：
//   - notified_at 为空：上一次没发出去（webhook 配错、网络不通），立刻补发。
//   - 距上次通知已超过设置的间隔：毛病一直没好，隔一段时间再提醒一次，
//     否则它只在第一条消息里出现过，而那条消息早就被刷到聊天记录下面了。
//
// repeat 设为 0 表示「只提醒一次」。
func shouldRepeatAlert(ev store.DeviceEvent, cfg alertConfig, now time.Time) bool {
	if ev.NotifiedAt == "" {
		return true
	}
	interval := cfg.repeatInterval()
	if interval <= 0 {
		return false
	}
	last, err := time.Parse(time.RFC3339, ev.NotifiedAt)
	if err != nil {
		return true
	}
	return now.Sub(last) >= interval
}

// hasWebhook 判断是否配了通知地址。
func (c alertConfig) hasWebhook() bool {
	return strings.TrimSpace(c.WebhookURL) != ""
}

func readAlertConfig(ctx context.Context, s *store.Store) (alertConfig, error) {
	var cfg alertConfig
	err := s.WithTx(ctx, true, func(tx *sql.Tx) error {
		var e error
		cfg, e = loadAlertConfig(ctx, tx)
		return e
	})
	return cfg, err
}

func markAlertNotified(ctx context.Context, s *store.Store, id int64, at string) error {
	return s.WithTx(ctx, false, func(tx *sql.Tx) error {
		return store.MarkDeviceEventNotified(ctx, tx, id, at)
	})
}

// evaluatePrinter 取一台打印机的原始信号并判定。
//
// 第二个返回值表示【这一轮到底取到信号没有】。为假时调用方必须整台跳过，
// 不能当成「一切正常」—— 理由见 printerSignals.Evaluated。
func evaluatePrinter(printerURI string, supplyLowPct int) ([]devicealert.Issue, bool) {
	info, err := ipp.GetPrinterAttributes(printerURI)
	if err != nil || info == nil {
		// 连 cupsd 都问不到这台队列：什么都判断不了。
		//
		// 🚫 不能因此报「设备不可达」—— 这是 cupsd 侧的问题（队列刚被删、
		// cupsd 忙），而「设备不可达」的措辞会把管理员引到打印机上去。
		return nil, false
	}

	var snap devicealert.Snapshot
	if dev := deviceURIOf(info); dev != "" {
		// 直连设备问一次：只有它知道打印机【现在】的状态与墨量。
		// probeDevice 自带 2 秒超时与 5 秒缓存，与打印页共用同一份结果。
		if st := probeDevice(dev); st.Probed {
			snap = devicealert.FromDevice(st)
		}
	}
	if !snap.Probed {
		// 退路：设备地址探不了（usb:// 之类），只能用 cupsd 报的状态原因。
		// 墨量不参与（cupsd 报的是缓存值，见 devicealert 包注释）。
		snap = devicealert.FromQueue(info)
	}

	return devicealert.Evaluate(snap, devicealert.Options{SupplyLowPercent: supplyLowPct}), true
}
