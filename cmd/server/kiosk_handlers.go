package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"cups-web/internal/store"
)

// ─── 刷卡取件：终端侧 ─────────────────────────────────────────────────
//
// 这个文件是【本系统唯一的无会话写入口】。取件端是打印机旁一台没有屏幕的
// 树莓派，它没有浏览器、没有登录、没有 cookie，拿的只是一张卡。所以这里有
// 三件事必须做全，缺任何一件都会以「现场没人知道为什么」的形式暴露：
//
//	① 认终端（token）—— 决定任务打到哪台打印机
//	② 限流 + 审计日志 —— 卡片号可枚举，不限流等于开了个爆破口；
//	   而没有屏幕时，日志是唯一的排障线索
//	③ 状态只在确认能打之后才动 —— 打印机离线时一个字都不改库
//
// 见 docs/pull-printing.md。

// 取件结果码。取件端按它决定响几声、亮什么灯 —— 这是无屏幕方案里
// 唯一的反馈通道，所以每加一个码都要同时想清楚它对应几声（见 docs）。
const (
	kioskStatusReleased           = "released"            // 全部任务已送去打印
	kioskStatusPartial            = "partial"             // 一部分失败
	kioskStatusFailed             = "failed"              // 全部失败
	kioskStatusNoJobs             = "no_jobs"             // 这台打印机上没有他的任务
	kioskStatusCardUnbound        = "card_unbound"        // 卡没登记（或读出来的串对不上）
	kioskStatusPrinterUnavailable = "printer_unavailable" // 打印机离线
	kioskStatusUnknownTerminal    = "unknown_terminal"    // token 不认识
	kioskStatusTerminalDisabled   = "terminal_disabled"   // 终端被管理员停用
	kioskStatusRateLimited        = "rate_limited"        // 刷得太频繁
	kioskStatusBadSwipe           = "bad_swipe"           // 空卡号（误触）

	// 下面三个只在「绑卡窗口开着」时出现（见 admin_kiosk_handlers.go）。
	// 那时刷卡的含义是绑定而不是取件。
	kioskStatusBound      = "bound"       // 绑定成功
	kioskStatusBindTaken  = "bind_taken"  // 这张卡已绑给别人
	kioskStatusBindFailed = "bind_failed" // 其它绑定失败（看 reason）
	// 刷卡的是「仅用于绑卡」的终端（printer_uri = ''），但此刻没有
	// 等待中的绑定窗口 —— 管理员忘了先在界面上点「开始等待刷卡」。
	kioskStatusBindIdle = "bind_idle"
)

type kioskReleaseRequest struct {
	Token  string `json:"token"`
	CardNo string `json:"cardNo"`
}

type kioskJobResult struct {
	Filename string `json:"filename"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
}

type kioskReleaseResponse struct {
	Status string           `json:"status"`
	User   string           `json:"user,omitempty"`
	Jobs   []kioskJobResult `json:"jobs,omitempty"`
	Reason string           `json:"reason,omitempty"`
}

// ─── 限流 ─────────────────────────────────────────────────────────────
//
// 与 login_limiter.go 的「失败次数」模型不同：那边防的是猜密码（失败才有成本），
// 这边防的是【枚举卡号】——而枚举时的响应是正常的 card_unbound，不是失败。
// 所以这里限的是【总次数】而不是失败次数。
//
// 计数仅存进程内存：单二进制部署足够，多实例会各自计数（缓解而非根除，
// 但配合「卡片必须物理接触读卡器才能拿到完整串」这个门槛已经够用）。

const (
	kioskRateLimit     = 30              // 每个窗口允许的刷卡次数
	kioskRateWindow    = time.Minute     // 计数窗口
	kioskRateSweepEach = 5 * time.Minute // 多久清一次过期条目
)

type kioskRateEntry struct {
	count     int
	windowEnd time.Time
}

var (
	kioskRateMu        sync.Mutex
	kioskRateEntries   = make(map[string]*kioskRateEntry)
	kioskRateLastSweep time.Time
)

// kioskRateAllow 报告这个键这次还能不能刷。超限返回 false。
func kioskRateAllow(key string) bool {
	now := time.Now()
	kioskRateMu.Lock()
	defer kioskRateMu.Unlock()

	if now.Sub(kioskRateLastSweep) > kioskRateSweepEach {
		kioskRateLastSweep = now
		for k, e := range kioskRateEntries {
			if now.After(e.windowEnd) {
				delete(kioskRateEntries, k)
			}
		}
	}

	e := kioskRateEntries[key]
	if e == nil || now.After(e.windowEnd) {
		kioskRateEntries[key] = &kioskRateEntry{count: 1, windowEnd: now.Add(kioskRateWindow)}
		return true
	}
	e.count++
	return e.count <= kioskRateLimit
}

// shortToken 给日志用的 token 前缀。
//
// 🚫 不要把完整 token 写进日志：它是【等价于密码的凭据】，拿到它就能冒充
// 那台终端。日志经常被整份贴进工单里，前缀足够定位是哪台就行。
func shortToken(token string) string {
	if len(token) <= 8 {
		return token
	}
	return token[:8] + "…"
}

// writeKioskResult 一律用 200 返回，状态放在 body 里。
//
// ⚠️ 这不是偷懒。取件端是一台没有屏幕的树莓派，它按 status 字段响几声；
// 走 4xx 的话它（以及任何通用 HTTP 客户端）只能笼统报「失败」，
// 分不清「卡没登记」和「这台没有你的任务」—— 而那两种的处理方式完全不同
// （一个叫管理员绑卡，一个直接走人）。
//
// 只有请求本身畸形（JSON 解析不了）才用 400，那是编程错误不是业务结果。
func writeKioskResult(w http.ResponseWriter, resp kioskReleaseResponse) {
	writeJSON(w, resp)
}

// tryBindCard 处理「绑定窗口开着时的刷卡」：把这张卡绑给窗口指定的那个用户。
//
// 返回 handled=true 表示这次刷卡已经被当作「绑定」处理掉了，调用方不要再走取件。
//
// 这条路存在的理由：读卡器挂在打印机旁的终端上，而管理界面在管理员的电脑上 ——
// 「点按钮」和「刷卡」是两个地方。窗口（card_bind_sessions）就是把它们连起来的
// 那根线：管理员点绑卡开窗，这里的这一次刷卡替他把卡绑上。
//
// ⚠️ 它带来一个小概率的错绑风险：窗口开着的这一两分钟里，若**另一个拿着
// 未绑过卡**的人正好来刷，他的卡会被绑给管理员选的那个人。所以：
//   - 窗口只开 120 秒，且全局只有一个；
//   - 已经绑过卡的人不受影响（会被 ErrCardTaken 挡下，走 bind_taken）；
//   - 管理员的浏览器上会实时显示出绑的卡号，可以立刻解绑重来。
//
// 想完全不冒这个险，就用 `kiosk.py --read` 抄号那条路（批量发卡时它本来也更快）。
func tryBindCard(ctx context.Context, term store.KioskTerminal, cardNo string) (bool, kioskReleaseResponse) {
	var bind store.CardBindSession
	if err := appStore.WithTx(ctx, true, func(tx *sql.Tx) error {
		var e error
		bind, e = store.ActiveCardBindSession(ctx, tx)
		return e
	}); err != nil {
		// 查不动就当没有窗口、走正常取件。绑卡是低频操作，
		// 不该因为它出错而把取件也拖下水。
		log.Printf("[kiosk] 查绑定窗口失败（按无窗口处理）: %v", err)
		return false, kioskReleaseResponse{}
	}
	if bind.ID == 0 {
		return false, kioskReleaseResponse{}
	}

	var taken bool
	var closed bool
	err := appStore.WithTx(ctx, false, func(tx *sql.Tx) error {
		// 先收尾窗口 —— 拿到 true 的人才有资格去改 users.card_no。
		// 这是幂等的基石：两次刷卡撞在一起时只有一个能成功。
		claimed, err := store.CompleteCardBindSession(ctx, tx, bind.ID, cardNo)
		if err != nil {
			return err
		}
		if !claimed {
			closed = true
			return nil
		}
		if err := store.SetUserCardNo(ctx, tx, bind.UserID, cardNo); err != nil {
			if errors.Is(err, store.ErrCardTaken) {
				taken = true
			}
			return err
		}
		return nil
	})

	switch {
	case closed:
		// 另一个刷卡先一步把窗口用掉了。如实说，别假装成功。
		log.Printf("[kiosk] 绑定窗口已被用掉（并发刷卡？）")
		return true, kioskReleaseResponse{Status: kioskStatusBindFailed, Reason: "绑定窗口已经结束了"}

	case taken:
		// 事务已回滚，窗口还开着 —— 管理员换一张卡就能接着刷，不必回电脑前。
		owner := ""
		_ = appStore.WithTx(ctx, true, func(tx *sql.Tx) error {
			u, e := store.GetUserByCardNo(ctx, tx, cardNo)
			if e == nil {
				owner = u.Username
			}
			return nil
		})
		reason := "这张卡已经绑定给「" + owner + "」了，换一张卡再刷"
		if owner == "" {
			reason = "这张卡已经绑定给别的账号了，换一张卡再刷"
		}
		_ = appStore.WithTx(ctx, false, func(tx *sql.Tx) error {
			return store.RecordCardBindError(ctx, tx, bind.ID, reason)
		})
		log.Printf("[kiosk] 为「%s」绑卡失败：卡 %s 已被占用", bind.Username, cardNo)
		return true, kioskReleaseResponse{Status: kioskStatusBindTaken, User: bind.Username, Reason: reason}

	case err != nil:
		log.Printf("[kiosk] 为「%s」绑卡出错: %v", bind.Username, err)
		return true, kioskReleaseResponse{Status: kioskStatusBindFailed, User: bind.Username, Reason: "绑定失败，请到管理界面重试"}
	}

	log.Printf("[kiosk] ✅ 已把卡 %s 绑定给「%s」（终端「%s」，操作人 %s）",
		cardNo, bind.Username, term.Name, bind.CreatedBy)
	return true, kioskReleaseResponse{Status: kioskStatusBound, User: bind.Username}
}

// kioskPingHandler 是终端的自检口：只验 token，不碰任何状态。
//
// 它存在的理由很具体：终端启动时最想知道的是「服务器地址对不对、token 对不对、
// 服务端是不是新版本」，而这三件事在「刷卡没反应」的场景里长得一模一样。
// 但自检**不能**用一个假卡号去打 release —— 万一那一刻正好有绑定窗口开着，
// 那张假卡就会被绑给一个真人（见 tryBindCard）。所以单独开一个无副作用的口。
//
// 顺带更新 last_seen_at：管理界面「最近上线」那一列就是靠它亮的。
func kioskPingHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		writeKioskResult(w, kioskReleaseResponse{Status: kioskStatusUnknownTerminal})
		return
	}
	// 与 release 同一道限流，但用【独立的键】：自检在启动时打，
	// 与取件共用一个计数器的话，一台终端反复重启会把取件的额度挤掉。
	if !kioskRateAllow("ping|" + token + "|" + clientIP(r)) {
		writeKioskResult(w, kioskReleaseResponse{Status: kioskStatusRateLimited})
		return
	}

	var term store.KioskTerminal
	err := appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
		var err error
		term, err = store.GetKioskTerminal(r.Context(), tx, token)
		return err
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeKioskResult(w, kioskReleaseResponse{Status: kioskStatusUnknownTerminal})
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to load terminal")
		return
	}
	if !term.Enabled {
		writeKioskResult(w, kioskReleaseResponse{Status: kioskStatusTerminalDisabled, Reason: term.Name})
		return
	}

	if err := appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
		return store.TouchKioskTerminal(r.Context(), tx, term.Token)
	}); err != nil {
		log.Printf("[kiosk] 更新终端在线时间失败（不影响自检）: %v", err)
	}

	// 这里用 released 复用「一切正常」的语义不合适（它会引起声光提示），
	// 单独给一个 ok —— 自检不该响。
	writeKioskResult(w, kioskReleaseResponse{Status: "ok", User: term.Name})
}

// kioskReleaseHandler 是取件终端唯一调用的接口：刷一张卡，释放一批任务。
//
// 公开路由（无会话、无 CSRF），认证靠 token —— 注册位置见 main.go，
// 必须在 protected 子路由【之前】。
func kioskReleaseHandler(w http.ResponseWriter, r *http.Request) {
	var req kioskReleaseRequest
	// 限长：这个 body 只有两个短字段，任何更大的都是异常。
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}

	token := strings.TrimSpace(req.Token)
	// 用与 store 完全相同的归一（转大写、去分隔符），这样读卡器吐的
	// `04A23B1C` 与管理员手抄的 `04:a2:3b:1c` 是同一张卡。
	// 归一规则与理由见 store.NormalizeCardNo。
	cardNo := store.NormalizeCardNo(req.CardNo)

	if token == "" || cardNo == "" {
		// 读卡器误触（空刷/只按了回车）是常态，不是错误，静默忽略。
		writeKioskResult(w, kioskReleaseResponse{Status: kioskStatusBadSwipe})
		return
	}

	if !kioskRateAllow(token + "|" + clientIP(r)) {
		log.Printf("[kiosk] 刷卡过于频繁，已限流（token=%s ip=%s）", shortToken(token), clientIP(r))
		writeKioskResult(w, kioskReleaseResponse{Status: kioskStatusRateLimited})
		return
	}

	// ── ① 认终端 ──
	var term store.KioskTerminal
	err := appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
		var err error
		term, err = store.GetKioskTerminal(r.Context(), tx, token)
		return err
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// 与「已停用」分开报：前者是配置漏了/抄错了，后者是管理员主动关的。
			// 合成一句话会让运维两边都查一遍。
			log.Printf("[kiosk] ⚠️ 未注册的终端（token=%s ip=%s）", shortToken(token), clientIP(r))
			writeKioskResult(w, kioskReleaseResponse{Status: kioskStatusUnknownTerminal})
			return
		}
		log.Printf("[kiosk] 查终端失败: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to load terminal")
		return
	}
	if !term.Enabled {
		log.Printf("[kiosk] ⚠️ 终端「%s」已停用，拒绝取件", term.Name)
		writeKioskResult(w, kioskReleaseResponse{Status: kioskStatusTerminalDisabled})
		return
	}

	// 在线时间戳纯粹是给管理员看的，失败不影响取件本身。
	if err := appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
		return store.TouchKioskTerminal(r.Context(), tx, term.Token)
	}); err != nil {
		log.Printf("[kiosk] 更新终端在线时间失败（不影响取件）: %v", err)
	}

	// ── ② 有人在等绑卡吗？──
	//
	// 绑定窗口开着时，这次刷卡的含义是「把这张卡绑给某某」，而不是取件。
	// ⚠️ 必须在【认卡之前】判断：要绑的那张卡此刻还没绑过人，按取件那条路走
	// 只会得到「工牌未登记」，管理员会以为刷卡没生效。
	if handled, resp := tryBindCard(r.Context(), term, cardNo); handled {
		writeKioskResult(w, resp)
		return
	}

	// 走到这里 = 没有等待中的绑定窗口。若这本来就是一台「仅用于绑卡」的终端
	// （管理员把读卡器插在自己电脑上），那它此刻唯一能做的事就是绑卡，
	// 该说清楚「先去点按钮」，而不是含糊地回一句「没有待取件任务」——
	// 后者会让人以为是任务提交错了地方。
	if term.PrinterURI == "" {
		log.Printf("[kiosk] 绑卡专用终端「%s」刷卡，但没有等待中的绑定窗口", term.Name)
		writeKioskResult(w, kioskReleaseResponse{
			Status: kioskStatusBindIdle,
			Reason: "这是绑卡专用终端：请先在管理界面点「开始等待刷卡」，再刷卡",
		})
		return
	}

	// ── ③ 认卡 ──
	var user store.User
	err = appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
		var err error
		user, err = store.GetUserByCardNo(r.Context(), tx, cardNo)
		return err
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// ⚠️ 这里刻意打出【完整的卡号】。无屏幕方案下，管理员唯一的
			// 排障入口就是这行日志 —— 「读出来的串到底长什么样」是他判断
			// 「是卡没绑」还是「读卡器格式对不上」的唯一依据。
			// 卡号本身不是密码（它是存取控制里的标识符，不是认证因子），
			// 而拿到它的人还得先有这台终端的 token 才能做任何事。
			log.Printf("[kiosk] 卡未登记（终端「%s」读到 %q）", term.Name, cardNo)
			writeKioskResult(w, kioskReleaseResponse{Status: kioskStatusCardUnbound})
			return
		}
		log.Printf("[kiosk] 按卡号查用户失败: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to load user")
		return
	}

	// ── ③ 打印机不可用 → 一个字都不改库 ──
	//
	// 这比「领取后失败再回退」干净得多：零状态变更，任务原样留在 held，
	// 员工等打印机恢复后再刷一次就行。回退虽然也正确，但会白白写两次库，
	// 而且中间那一瞬间的状态是「打印中」—— 员工若正好在看记录页会以为在打。
	if reason := printerUnavailableReason(term.PrinterURI); reason != "" {
		log.Printf("[kiosk] 打印机不可用，未释放任何任务：%s（%s）", term.PrinterURI, reason)
		writeKioskResult(w, kioskReleaseResponse{
			Status: kioskStatusPrinterUnavailable,
			User:   user.Username,
			Reason: reason,
		})
		return
	}

	// ── 从这一步起，任务不再挂在这条 HTTP 请求上 ──
	//
	// ⚠️ 用 WithoutCancel 而不是 r.Context()：取件端会超时（它等 60 秒），
	// 而员工就站在打印机旁边。请求一断，ctx 被取消，正在发的那次 IPP 会在
	// 半路被掐掉 —— 后面几条任务则连领都没领。结果是「任务卡在 printing」，
	// 要等 30 秒后的孤儿扫描才退回待取件，员工当场反复刷卡也没用。
	//
	// 与 payment.go 里「对渠道的出站调用绝不挂请求 ctx」是同一条规则：
	// 浏览器/客户端一次中断不该改变服务端的业务状态。
	// （WithoutCancel 保留 ctx 上的值，只去掉取消信号。）
	ctx := context.WithoutCancel(r.Context())

	// ── ④ 原子领取「他 + 这台打印机」的全部待取件任务 ──
	//
	// 这一步天然防重复：第二次刷卡时 WHERE status='held' 匹配 0 行。
	// 同一事务里把整行读出来 —— 拼 IPP 需要参数快照，而出了事务行可能就变了。
	var orders []store.PrintOrder
	err = appStore.WithTx(ctx, false, func(tx *sql.Tx) error {
		ids, err := store.ClaimHeldOrdersForPrinter(ctx, tx, user.ID, term.PrinterURI)
		if err != nil {
			return err
		}
		for _, id := range ids {
			o, err := store.GetOrderByID(ctx, tx, id)
			if err != nil {
				return err
			}
			orders = append(orders, o)
		}
		return nil
	})
	if err != nil {
		log.Printf("[kiosk] 领取任务失败（user=%s）: %v", user.Username, err)
		writeJSONError(w, http.StatusInternalServerError, "failed to claim orders")
		return
	}

	if len(orders) == 0 {
		log.Printf("[kiosk] %s 在「%s」上没有待取件任务", user.Username, term.Name)
		writeKioskResult(w, kioskReleaseResponse{Status: kioskStatusNoJobs, User: user.Username})
		return
	}

	// ── ⑤ 逐个送印（事务外 —— IPP 是网络调用，绝不能挂在事务里）──
	//
	// 同步做完再返回，而不是「只领取、后台送印 + 轮询」。理由是这个终端
	// 没有屏幕：它只有一次机会把结果告诉员工，异步就得多一个状态表和轮询
	// 端点，复杂度显著上升而省下的那几秒在实体打印机面前毫无意义。
	resp := kioskReleaseResponse{Status: kioskStatusReleased, User: user.Username}
	okCount := 0
	for _, o := range orders {
		item := kioskJobResult{Filename: o.Filename, OK: true}
		if err := startPullPrintForOrder(ctx, o); err != nil {
			item.OK = false
			item.Error = err.Error()
		} else {
			okCount++
		}
		resp.Jobs = append(resp.Jobs, item)
	}
	if okCount == 0 {
		resp.Status = kioskStatusFailed
	} else if okCount < len(resp.Jobs) {
		resp.Status = kioskStatusPartial
	}

	// 审计：每一次刷卡都留痕，成功与失败都留。
	// 无屏幕方案下这是唯一的排障线索 —— 员工说「刷了没反应」时，
	// 只有这行日志能回答「他到底刷没刷上、服务端当时说了什么」。
	log.Printf("[kiosk] %s 在「%s」刷卡：%d 个任务，成功 %d 个（%s）",
		user.Username, term.Name, len(resp.Jobs), okCount, resp.Status)

	writeKioskResult(w, resp)
}
