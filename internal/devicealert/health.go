// Package devicealert 把「设备探测看到的原始信号」翻译成「现在有哪些异常」。
//
// 这一层是【纯函数】：输入一次探测结果，输出一组异常。不碰数据库、不发通知、
// 不读设置 —— 于是「半夜卡纸」这类判断能被单测钉死，而不需要一台真打印机。
//
// ── 为什么单独一个包 ──
//
// 判定规则是这个功能里唯一会随打印机型号增长的东西（各家报的
// printer-state-reasons 五花八门，同一个「缺纸」有四五种写法）。把它从巡检
// 循环里拆出来，新规则就能单独测；混在 tick 里的话，想验一条新规则得先起库。
//
// ── 输入的两种来源，以及它们的可信度差别 ──
//
//   - FromDevice：直连打印机问出来的（ipp.ProbeDevice）。状态与墨量都是实时的。
//   - FromQueue：cupsd 报的队列属性。cupsd 【不会】主动探打印机（见
//     printer_device.go 顶部），它报的墨量是上一次连上时的缓存值。
//
// 因此墨量只在 FromDevice 那条路上参与判定。走队列退路时用缓存墨量会得到
// 「永远 5% 的墨盒」——一条永不消失的假告警，比不告警更糟。
package devicealert

import (
	"fmt"
	"sort"
	"strings"

	"cups-web/internal/ipp"
)

// 异常类别。英文键，进数据库、进 URL、进日志。
//
// 中文文案只在 KindLabel 一处，与 store 的 Audit* / cmd/server 的
// auditActionLabel 同一套做法：两处清单就会漂。
const (
	// KindUnreachable 直连探测不通（关机 / 断网 / 端口不对）。
	KindUnreachable = "unreachable"
	// KindOutOfPaper 缺纸：media-empty / media-needed / 缺进纸盒。
	KindOutOfPaper = "out_of_paper"
	// KindPaperJam 卡纸。
	KindPaperJam = "paper_jam"
	// KindDoorOpen 门 / 盖 / 安全联锁打开。
	KindDoorOpen = "door_open"
	// KindSupplyEmpty 墨粉 / 墨水 / 耗材耗尽。
	KindSupplyEmpty = "supply_empty"
	// KindSupplyLow 耗材不足（还能打，但该换了）。
	KindSupplyLow = "supply_low"
	// KindPaperLow 纸快没了。
	KindPaperLow = "paper_low"
	// KindWasteFull 废粉盒 / 废墨盒已满或将满。
	KindWasteFull = "waste_full"
	// KindOutputFull 出纸盘已满（打印机会因此停下来）。
	KindOutputFull = "output_full"
	// KindPaused 队列被暂停 —— 此时所有任务都会排队但不打印。
	KindPaused = "paused"
	// KindOther 认得出严重级别、但认不出类别的原因。
	KindOther = "other"
)

// 严重级别。与 schema.sql 里 device_events.severity 的取值一一对应。
//
//   - error   现在就有印不出来的风险，通知一律发。
//   - warning 还能打，但该去处理了。与 error 分开是因为两类消息的紧急程度
//     差得远：混在一起发，几次「耗材不足」之后，真出事的那条就没人看了。
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// AllKinds 按固定顺序返回所有类别。
//
// 供管理端的筛选下拉与「标签是否齐全」的自检用：从这一处取就保证了「新增一个
// 类别时不会漏掉某张清单」——而漏掉的表现是筛选下拉里没有那一项，或者界面上
// 冒出一行英文。
func AllKinds() []string {
	return []string{
		KindUnreachable, KindOutOfPaper, KindPaperJam, KindDoorOpen,
		KindSupplyEmpty, KindSupplyLow, KindPaperLow, KindWasteFull,
		KindOutputFull, KindPaused, KindOther,
	}
}

// KindLabel 是异常类别的中文，供通知文案与管理端显示。
func KindLabel(kind string) string {
	switch kind {
	case KindUnreachable:
		return "设备不可达"
	case KindOutOfPaper:
		return "缺纸"
	case KindPaperJam:
		return "卡纸"
	case KindDoorOpen:
		return "门/盖未关"
	case KindSupplyEmpty:
		return "耗材耗尽"
	case KindSupplyLow:
		return "耗材不足"
	case KindPaperLow:
		return "纸量不足"
	case KindWasteFull:
		return "废粉盒将满/已满"
	case KindOutputFull:
		return "出纸盘已满"
	case KindPaused:
		return "队列已暂停"
	case KindOther:
		return "其它异常"
	default:
		return kind
	}
}

// SeverityLabel 把严重级别翻成中文。未知值原样返回。
func SeverityLabel(sev string) string {
	switch sev {
	case SeverityError:
		return "故障"
	case SeverityWarning:
		return "提醒"
	default:
		return sev
	}
}

// SeverityRank 用于排序与「取更严重的一个」：数字小的更严重。
func SeverityRank(sev string) int {
	if sev == SeverityError {
		return 0
	}
	return 1
}

// worst 返回两个级别里更严重的一个。
func worst(a, b string) string {
	if SeverityRank(a) <= SeverityRank(b) {
		return a
	}
	return b
}

// Snapshot 是一次巡检看到的原始信号。
//
// 刻意与 ipp 解耦（两处构造器负责转换）：这样单测里拼一个「缺纸 + 墨量 3%」
// 的场景只要写四行字面量，不需要构造 IPP 报文。
type Snapshot struct {
	// Probed 表示这次是不是真的直连问了设备。
	//
	// false 有两个来源：设备地址根本没法用网络探（usb:// 等），或者只问了
	// cupsd。两种情况都【不能】判定「设备不可达」—— 那会把 USB 打印机的
	// 用户全变成假告警。
	Probed bool
	// Reachable 只在 Probed 为真时有意义。
	Reachable bool
	// ProbeMessage 不通时的原因原文（超时 / 拒绝连接 / 域名解析失败）。
	ProbeMessage string
	// StateReasons 是 printer-state-reasons 的原始串。
	StateReasons []string
	// MarkerNames / MarkerLevels 一一对应，单位是百分比。
	// 负数是 IPP 定义的「未知 / 不限量」，不参与判定。
	MarkerNames  []string
	MarkerLevels []int
}

// FromDevice 用【直连探测】的结果构造 Snapshot。
func FromDevice(st ipp.DeviceStatus) Snapshot {
	return Snapshot{
		Probed:       st.Probed,
		Reachable:    st.Reachable,
		ProbeMessage: st.Message,
		StateReasons: st.StateReasons,
		MarkerNames:  st.MarkerNames,
		MarkerLevels: st.MarkerLevels,
	}
}

// FromQueue 用 cupsd 报的队列属性构造 Snapshot。
//
// ⚠️ 只搬 StateReasons，【不搬墨量】——cupsd 的墨量是缓存值，拿它判定会得到
// 一条永不消失的假告警（见包注释）。
func FromQueue(info *ipp.PrinterInfo) Snapshot {
	if info == nil {
		return Snapshot{}
	}
	return Snapshot{StateReasons: info.StateReasons}
}

// Options 是判定用的可调阈值。
type Options struct {
	// SupplyLowPercent 低于这个百分比算「耗材不足」。<=0 时取默认值 10。
	//
	// 默认 10 而不是 20：耗材不足只是提醒，报得太早会变成背景噪声。
	// 真要紧的是「耗尽」，那一档不看阈值（level == 0）。
	SupplyLowPercent int
}

const defaultSupplyLowPercent = 10

// Issue 是一条判定结果。
type Issue struct {
	Kind     string
	Severity string
	// Detail 是【当时看到的原文】：触发它的 reason 串、或墨盒名 + 读数。
	// 「media-empty-error」比「缺纸」有用得多 —— 前者能拿去搜，后者不能。
	Detail string
}

// reasonRule 把 printer-state-reasons 的前缀映射到异常类别。
//
// 用前缀匹配而不是全等，因为同一个原因 IPP 允许带三级后缀：
//
//	media-empty          严重级别未指定（按下面 sev 字段）
//	media-empty-error    明确是故障
//	media-empty-warning  明确只是提醒
//	media-empty-report   明确只是「知悉一下」
//
// 实测各家打印机的写法也确实不一致（有的只报裸名，有的只报 -error）。
type reasonRule struct {
	prefix string
	kind   string
	sev    string // 裸名时的级别
}

// 顺序即匹配优先级：一个 reason 只会命中第一条前缀匹配它的规则，
// 所以更长、更具体的写在前面（media-empty 要排在 media-low 前面）。
var reasonRules = []reasonRule{
	{"media-empty", KindOutOfPaper, SeverityError},
	{"media-needed", KindOutOfPaper, SeverityError},
	{"input-tray-missing", KindOutOfPaper, SeverityError},
	{"media-jam", KindPaperJam, SeverityError},
	{"door-open", KindDoorOpen, SeverityError},
	{"cover-open", KindDoorOpen, SeverityError},
	{"interlock-open", KindDoorOpen, SeverityError},
	{"toner-empty", KindSupplyEmpty, SeverityError},
	{"ink-empty", KindSupplyEmpty, SeverityError},
	{"marker-supply-empty", KindSupplyEmpty, SeverityError},
	{"supply-empty", KindSupplyEmpty, SeverityError},
	{"media-low", KindPaperLow, SeverityWarning},
	{"toner-low", KindSupplyLow, SeverityWarning},
	{"ink-low", KindSupplyLow, SeverityWarning},
	{"marker-supply-low", KindSupplyLow, SeverityWarning},
	{"supply-low", KindSupplyLow, SeverityWarning},
	{"marker-waste-full", KindWasteFull, SeverityWarning},
	{"marker-waste-almost-full", KindWasteFull, SeverityWarning},
	{"output-area-full", KindOutputFull, SeverityWarning},
	{"output-tray-full", KindOutputFull, SeverityWarning},
	{"paused", KindPaused, SeverityWarning},
	{"moving-to-paused", KindPaused, SeverityWarning},
}

// Evaluate 从一次快照推导出当前有哪些异常。
//
// 返回值按「先故障、后提醒，同级按类别名排序」排列：顺序稳定，单测和通知
// 文案都不会因为 map 遍历顺序而抖动。
func Evaluate(s Snapshot, opt Options) []Issue {
	if opt.SupplyLowPercent <= 0 {
		opt.SupplyLowPercent = defaultSupplyLowPercent
	}

	// 直连探不通：这是最强的一个结论，此时 StateReasons / 墨量要么是空的、
	// 要么是过期的，一律不看 —— 说「没纸」而其实只是连不上的话，
	// 会把人指到完全错误的方向去。
	if s.Probed && !s.Reachable {
		msg := strings.TrimSpace(s.ProbeMessage)
		if msg == "" {
			msg = "无法连接到打印机"
		}
		return []Issue{{Kind: KindUnreachable, Severity: SeverityError, Detail: msg}}
	}

	byKind := map[string]*Issue{}
	add := func(kind, sev, detail string) {
		cur, ok := byKind[kind]
		if !ok {
			byKind[kind] = &Issue{Kind: kind, Severity: sev, Detail: detail}
			return
		}
		cur.Severity = worst(cur.Severity, sev)
		// 同一类别的多条原因都留着（"media-empty-error；input-tray-missing"），
		// 排查时「两件事同时成立」和「只有一件」是两个结论。
		if detail != "" && !strings.Contains(cur.Detail, detail) {
			if cur.Detail == "" {
				cur.Detail = detail
			} else {
				cur.Detail += "；" + detail
			}
		}
	}

	for _, raw := range s.StateReasons {
		reason := strings.TrimSpace(raw)
		if reason == "" || reason == "none" {
			continue
		}
		base, suffix := splitReasonSuffix(reason)
		// "none" 是 IPP 用来表示「这一类没有问题」的唯一取值。带后缀的
		// "none-error" 这种怪写法一并按「没问题」处理 —— 把它当告警的话，
		// 界面会多出一条谁也看不懂的 other。
		if base == "none" {
			continue
		}
		switch suffix {
		case "none":
			// 明确表示「这一项没有问题」。
			continue
		case "report":
			// IPP 定义「仅供知悉」。我们按 warning 处理（"该去处理了，但还能打"）：
			// media-low / marker-waste-almost-full 恰恰常以 -report 形式出现，
			// 而那两条正是「纸快没了、废粉盒快满了」这类最该提前知道的事。
			addFromReason(add, base, reason, SeverityWarning)
		case "warning":
			addFromReason(add, base, reason, SeverityWarning)
		case "error":
			addFromReason(add, base, reason, SeverityError)
		default:
			// 裸名：级别按规则表。认不出的裸名【忽略】——各家打印机的私有
			// 字符串太多，全当告警的话，界面会被一串看不懂的英文占满，
			// 而真正的那条就淹在里面了。
			addFromReason(add, base, reason, "")
		}
	}

	// 墨量：只在直连探测过、且够得着时看（见包注释）。
	if s.Probed && s.Reachable {
		for i, level := range s.MarkerLevels {
			name := ""
			if i < len(s.MarkerNames) {
				name = strings.TrimSpace(s.MarkerNames[i])
			}
			if name == "" {
				name = fmt.Sprintf("耗材 %d", i+1)
			}
			// level < 0 是 IPP 的「未知 / 不限量」（-1 未知、-2 有剩余但未知、
			// -3 不限量），0 才是真的空了。
			if level < 0 {
				continue
			}
			switch {
			case level == 0:
				add(KindSupplyEmpty, SeverityError, fmt.Sprintf("%s 余量 0%%", name))
			case level <= opt.SupplyLowPercent:
				add(KindSupplyLow, SeverityWarning, fmt.Sprintf("%s 余量 %d%%", name, level))
			}
		}
	}

	out := make([]Issue, 0, len(byKind))
	for _, is := range byKind {
		out = append(out, *is)
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := SeverityRank(out[i].Severity), SeverityRank(out[j].Severity)
		if ri != rj {
			return ri < rj
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// addFromReason 把一条 reason 记进结果。
//
// defaultSev 为空表示「裸名，级别看规则表」；非空表示后缀已经说明了级别，
// 以它为准 —— 打印机自己说「这只是 -warning」时，不该被规则表的 error 顶掉。
func addFromReason(add func(kind, sev, detail string), base, raw, defaultSev string) {
	for _, r := range reasonRules {
		if !strings.HasPrefix(base, r.prefix) {
			continue
		}
		sev := defaultSev
		if sev == "" {
			sev = r.sev
		}
		add(r.kind, sev, raw)
		return
	}
	// 认不出类别，但后缀说了级别（-warning / -error）—— 那是有信息量的，
	// 归到 other，别丢。
	if defaultSev != "" {
		add(KindOther, defaultSev, raw)
	}
}

// splitReasonSuffix 拆掉 printer-state-reasons 的级别后缀，返回主体与后缀。
// 没有后缀时后缀为空串。
func splitReasonSuffix(reason string) (base, suffix string) {
	for _, s := range []string{"error", "warning", "report", "none"} {
		if strings.HasSuffix(reason, "-"+s) {
			return strings.TrimSuffix(reason, "-"+s), s
		}
	}
	return reason, ""
}
