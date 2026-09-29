package main

import (
	"context"
	"database/sql"
	"log"
	"sync"
	"time"

	"cups-web/internal/ipp"
	"cups-web/internal/store"
)

const (
	// orderTickInterval 是订单巡检的心跳间隔。
	orderTickInterval = 30 * time.Second

	// orderTickBatch 限制每轮处理的条数，避免积压时一次扫出上千条把
	// SQLite 与打印机都压垮。
	orderTickBatch = 20

	// orderStalePaidAfter 是「已支付但迟迟没进入打印」的宽限期。
	// 支付事务提交后才 go 出去送印，正常几百毫秒内就会迁移到 printing；
	// 给 30 秒是为了容忍慢盘与瞬时负载。
	orderStalePaidAfter = 30 * time.Second

	// orderStuckPrintingAfter 是「卡在 printing」的判定阈值。
	// 超过它说明进程很可能在送印中途挂了，结果未知。
	orderStuckPrintingAfter = 30 * time.Minute

	// paymentPollAfter 是「在线支付单创建后多久才值得去查」的宽限期。
	//
	// 太短会白查：用户刚点开支付弹窗、二维码还没扫，此时查必然是「未支付」，
	// 白白占用渠道的接口配额（支付宝对查单有频率限制）。
	// 30 秒足够让"掏出手机扫码"这个动作发生。
	paymentPollAfter = 30 * time.Second

	// paymentPollBatch 限制每轮查单的笔数。
	// 查单是【出站网络请求】，与前面几步的纯本地查询不同 ——
	// 一轮几十个请求串行出去会明显拖长 tick，所以要夹紧。
	paymentPollBatch = 5
)

func startOrderWorker(s *store.Store, uploads string) {
	go func() {
		for {
			orderTick(context.Background(), s, uploads)
			time.Sleep(orderTickInterval)
		}
	}()
}

// orderTick 处理四种「订单卡住了」的情形。
//
// 全部操作都是幂等的：状态迁移一律带 WHERE status = ... 条件，
// 因此与支付回调、用户手动重试并发时也只会有一个生效。
func orderTick(ctx context.Context, s *store.Store, uploads string) {
	now := time.Now().UTC()

	pollUnsettledPayments(ctx, now)
	compensateStalePaidOrders(ctx, s, now)
	confirmPrintResults(ctx, s, now)
	expireUnpaidBatches(ctx, s, uploads, now)
}

// ─── 按需查单：把「付完到界面变样」从 30 秒压到几秒 ───────────────────
//
// 后台巡检每 30 秒才问一次渠道，用户扫完码要盯着二维码等半分钟 ——
// 而这个项目的部署形态（内网、无公网回调）决定了主动查单是唯一的结算途径，
// 等待时间就等于用户面对界面的时间。
//
// 前端本来就在轮询：支付弹窗每 2 秒拉一次批次状态，钱包充值页可以每 5 秒
// 拉一次支付状态。让这些【已有的】轮询顺带触发一次查单，延迟就降到几秒，
// 而代价只是把已经发生的请求乘以一个常数。
//
// 节流是必须的：2 秒一次不限流的话，一笔支付单每分钟会被查 30 次，
// 支付宝对查单接口有频率限制，白查既浪费配额也可能被限流。

const (
	// paymentQueryMinInterval 是同一个支付单两次查单之间的最小间隔。
	// 取 5 秒：前端 2 秒轮询时大约每三轮真正查一次，用户感知仍是「立刻」。
	paymentQueryMinInterval = 5 * time.Second

	// paymentQueryTimeout 单次查单的超时。与后台巡检用同一个值。
	paymentQueryTimeout = 20 * time.Second
)

// queryGate 是支付单查单的节流器。
//
// 后台巡检与按需查单【共用】它，否则同一笔单会被两个来源同时查 ——
// 那正是节流想避免的事。
type queryGate struct {
	mu   sync.Mutex
	last map[string]time.Time
	min  time.Duration
}

func newQueryGate(min time.Duration) *queryGate {
	return &queryGate{last: make(map[string]time.Time), min: min}
}

// allow 放行则返回 true 并记录时间；在间隔内重复调用返回 false。
func (g *queryGate) allow(key string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if t, ok := g.last[key]; ok && now.Sub(t) < g.min {
		return false
	}
	g.last[key] = now
	// 这个 map 的生命周期是整个进程，不清会随支付单数量无限增长。
	// 阈值取得很高，清理本身不是热路径。
	if len(g.last) > 4096 {
		for k, t := range g.last {
			if now.Sub(t) > time.Hour {
				delete(g.last, k)
			}
		}
	}
	return true
}

var paymentQueryGate = newQueryGate(paymentQueryMinInterval)

// syncPaymentByOutTradeNo 对一个支付单查一次渠道，到账则结算。
//
// 后台巡检与按需查单共用这一份 —— 结算路径只有一条，与回调也汇到同一个
// settlePayment。返回值表示这次是否真的去查了（被节流挡下或渠道不可用时为 false）。
func syncPaymentByOutTradeNo(ctx context.Context, providerName, outTradeNo string) bool {
	if !paymentQueryGate.allow(outTradeNo, time.Now()) {
		return false
	}
	provider, err := buildProvider(providerName)
	if err != nil {
		// 渠道不可用（比如管理员撤了环境变量）。不判失败：万一只是临时缺配置，
		// 恢复后还能查到；直接判失败反而会误伤已付的款。
		return false
	}

	// 自己起超时，不挂调用方的 ctx —— 一个渠道卡住不该拖垮整轮巡检，
	// 也不该让一次 HTTP 请求挂在一个慢查询上。
	queryCtx, cancel := context.WithTimeout(context.Background(), paymentQueryTimeout)
	defer cancel()

	res, err := provider.Query(queryCtx, outTradeNo)
	if err != nil {
		log.Printf("[payment] 查单失败 outTradeNo=%s: %v", outTradeNo, err)
		return true
	}
	if !res.Paid {
		return true
	}

	orderIDs, err := settlePayment(ctx, res, providerName, res.Raw)
	if err != nil {
		log.Printf("[payment] 查单结算失败 outTradeNo=%s: %v", outTradeNo, err)
		return true
	}
	log.Printf("[payment] 查单发现到账 outTradeNo=%s 金额=%d 分", res.OutTradeNo, res.AmountCents)
	launchPrintForOrders(orderIDs)
	return true
}

// syncUnsettledPaymentForBatch 对该批次挂着的未结算在线支付单查一次。
//
// 挂在批次状态接口上：支付弹窗每 2 秒轮询，用户扫完码几秒内界面就会变。
// 绝大多数轮询会因为「没有未结算的支付单」或「被节流」而直接返回。
func syncUnsettledPaymentForBatch(ctx context.Context, batchID string) {
	var pending store.UnsettledPayment
	found := false
	err := appStore.WithTx(ctx, true, func(tx *sql.Tx) error {
		p, err := store.GetUnsettledPaymentByBatch(ctx, tx, batchID)
		if err != nil {
			// 没有未结算的支付单是常态，不是错误。
			return nil
		}
		pending, found = p, true
		return nil
	})
	if err != nil || !found {
		return
	}
	// ⚠️ 必须在事务外查：查单是出站网络请求，放进 WithTx 会占着 SQLite 的
	// 读事务等网络往返。
	syncPaymentByOutTradeNo(ctx, pending.Provider, pending.OutTradeNo)
}

// pollUnsettledPayments 主动向渠道查单，把到账的钱结算掉。
//
// 这是内网部署下【最主要】的结算途径，不是兜底：本项目的典型形态是
// 单容器跑在内网，没有公网 HTTPS 域名，渠道的异步通知根本到不了。
// 用户不点「我已支付」时，就是靠这一轮轮询发现钱到了。
//
// ⚠️ 查单是出站网络请求，必须【在事务外】逐个做：把 HTTP 调用放进
// WithTx 里会占着 SQLite 的写锁等网络往返，把整个库卡住。
// 所以这里的形状是「先读一批 → 逐个查 → 各自settle」。
func pollUnsettledPayments(ctx context.Context, now time.Time) {
	before := now.Add(-paymentPollAfter).Format(time.RFC3339)

	var pending []store.UnsettledPayment
	err := appStore.WithTx(ctx, true, func(tx *sql.Tx) error {
		var err error
		pending, err = store.ListUnsettledOnlinePayments(ctx, tx, before, paymentPollBatch)
		return err
	})
	if err != nil {
		log.Printf("[payment] 列出待查支付单失败: %v", err)
		return
	}

	for _, p := range pending {
		// 与按需查单走【同一个】内核，账目行为因此完全一致。
		// 共用节流器：刚被弹窗查过的单，这一轮会直接跳过。
		syncPaymentByOutTradeNo(ctx, p.Provider, p.OutTradeNo)
	}
}

// compensateStalePaidOrders 补偿送印。
//
// 覆盖两种情形：回调里 go 出去的 goroutine 随进程重启丢了；
// 或者回调压根没来、用户也没点「我已支付」。
// 送印本身由 startPrintForOrder 内的原子领取保证不会重复。
func compensateStalePaidOrders(ctx context.Context, s *store.Store, now time.Time) {
	before := now.Add(-orderStalePaidAfter).Format(time.RFC3339)

	var ids []int64
	if err := s.WithTx(ctx, true, func(tx *sql.Tx) error {
		var err error
		ids, err = store.ListStalePaidOrders(ctx, tx, before, orderTickBatch)
		return err
	}); err != nil {
		log.Printf("[order] 扫描待补偿订单失败: %v", err)
		return
	}
	if len(ids) == 0 {
		return
	}

	log.Printf("[order] 发现 %d 笔已支付未送印的订单，开始补偿", len(ids))
	for _, id := range ids {
		if _, err := startPrintForOrder(ctx, id); err != nil {
			log.Printf("[order] #%d 补偿送印失败: %v", id, err)
		}
	}
}

// ─── 送印结果核对 ─────────────────────────────────────────────────────
//
// ⚠️ 这里是「钱该不该收」的最终裁决处。
//
// IPP 的 Print-Job 是异步的：CUPS 回 successful-ok 只表示任务入了队列。
// 打印机离线时 CUPS 会把任务挂着反复重试（界面上是 "Connecting to printer"），
// 而任务状态会一直停在 pending/stopped —— 纸一张都不会出来。
//
// 所以订单提交成功后【停在 printing】，由这一轮轮询去问 CUPS 要真实结果：
//
//	completed(9)                       → 真打完了，转 printed，钱不退
//	canceled(7) / aborted(8)           → 确定没打出来，失败 + 全额退款
//	processing 且印数长时间不增长       → 后端在空转重试，先取消任务再退款
//	processing 且印数在增长             → 正在出纸，等（哪怕打一小时）
//	pending（排队中）                   → 前面有任务，等多久都正常
//	查不到该任务                        → 存疑，转人工（不自动退款）
//
// 🚫 这一层是【兜底】，不是主要手段。主要手段是在【下单时】就拦住：
// 打印机不可用时根本不接单，用户压根不会付钱（见 orderCreateHandler）。
// 指望「先收钱再退款」是错的方向 —— 用户要的是纸，不是退款。
//
// 🚫 「先取消再退款」的顺序不能反：直接退款而任务还在队列里，等打印机
// 恢复就会把纸打出来 —— 钱退了、纸也出了。

const (
	// printConfirmGrace 是提交后多久才开始核对。刚提交的订单还没被 CUPS
	// 处理完，立刻去查只会得到「还在排队」。
	printConfirmGrace = 20 * time.Second

	// printStuckAfter 是【印数停止增长】多久才判定任务卡死。
	//
	// ⚠️ 计时的是「印数有多少久没变」，不是「任务存在了多久」。
	// 用户打印几百页要 6 分钟、或者前面排了别的任务，都完全正常 ——
	// 按存在时长一刀切会把正常打印的任务取消掉。
	printStuckAfter = 3 * time.Minute

	// printConfirmBatch 限制每轮核对的任务数。每次核对都是一次 IPP 往返。
	printConfirmBatch = 10
)

// CUPS 的 job-state 取值（IPP 标准）。
const (
	cupsJobPending        = 3
	cupsJobPendingHeld    = 4
	cupsJobProcessing     = 5
	cupsJobProcessingStop = 6
	cupsJobCanceled       = 7
	cupsJobAborted        = 8
	cupsJobCompleted      = 9
)

// printCheckGate 节流打印结果核对。
//
// 与支付查单同一个道理：前端轮询批次状态（每 2 秒）会顺带触发核对，
// 而核对是一次 IPP 往返。不节流的话，一次打印会被问几十遍。
// 2 秒的间隔让「打完到界面变样」几乎无感，同时把往返次数压到与
// 前端的轮询同量级。
var printCheckGate = newQueryGate(2 * time.Second)

// confirmPrintResultsForBatch 借前端的批次轮询顺带核对这一批的打印结果。
//
// 没有它的话，订单提交后要等后台巡检（每 30 秒）才被核对一次 ——
// 用户付完钱盯着「打印中」等半分钟，其中大半时间只是没人在问。
func confirmPrintResultsForBatch(ctx context.Context, batchID string) {
	before := time.Now().Add(-printConfirmGrace).Format(time.RFC3339)

	var orders []store.PrintOrder
	if err := appStore.WithTx(ctx, true, func(tx *sql.Tx) error {
		var err error
		orders, err = store.ListPrintingOrdersByBatch(ctx, tx, batchID, before)
		return err
	}); err != nil || len(orders) == 0 {
		return
	}
	for _, o := range orders {
		reconcilePrintingOrder(ctx, appStore, o, time.Now().UTC())
	}
}

// confirmPrintResults 后台巡检：核对所有 printing 订单在 CUPS 侧的真实结果。
func confirmPrintResults(ctx context.Context, s *store.Store, now time.Time) {
	before := now.Add(-printConfirmGrace).Format(time.RFC3339)

	var orders []store.PrintOrder
	if err := s.WithTx(ctx, true, func(tx *sql.Tx) error {
		var err error
		orders, err = store.ListPrintingOrders(ctx, tx, before, printConfirmBatch)
		return err
	}); err != nil {
		log.Printf("[order] 扫描待核对订单失败: %v", err)
		return
	}

	for _, o := range orders {
		reconcilePrintingOrder(ctx, s, o, now)
	}
}

// printAction 是核对一个打印任务后要采取的动作。
type printAction int

const (
	// printActionWait 继续等：任务在正常推进。
	printActionWait printAction = iota
	// printActionDone 确认打完，落定并收钱。
	printActionDone
	// printActionRefunded 确定没打出来，退款。
	printActionRefunded
	// printActionCancelRefund 卡住了：先取消任务再退款（顺序不能反）。
	printActionCancelRefund
	// printActionUnconfirmed 结果无法确认：转人工，【不退款】。
	printActionUnconfirmed
	// printActionUnknownState 未知状态，只记日志。
	printActionUnknownState
)

// progressTracker 记住每个任务上次观测到的完成印数，用来回答
// 「这个任务到底有没有在出纸」。
//
// ⚠️ 为什么不能只看 job-state / job-state-reasons：
// 实测（CUPS 2.4 + 网络打印机）打印机连不上时，CUPS 报的是
//
//	job-state              = 5  (processing)   ← 一派「正在打印」的样子
//	job-state-reasons      = [job-printing]    ← 同上，没有任何异常词
//	job-printer-state-message = "The printer is unreachable at this time."
//	job-impressions-completed = 1              ← 然后就再也不动了
//
// 状态词和原因词全都看不出问题，唯一可信的事实是【印数停了】。
// 而且数字不依赖语言、不依赖 CUPS 的措辞变化。
type progressTracker struct {
	mu   sync.Mutex
	last map[string]progressMark
}

type progressMark struct {
	impressions int
	changedAt   time.Time
}

func newProgressTracker() *progressTracker {
	return &progressTracker{last: make(map[string]progressMark)}
}

// observe 记录一次观测，返回「距离上次印数增长过了多久」。
//
// 首次见到这个任务时返回 0（还不知道它会不会动，先给一个周期）。
func (p *progressTracker) observe(key string, impressions int, now time.Time) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()

	prev, ok := p.last[key]
	if !ok || impressions > prev.impressions {
		p.last[key] = progressMark{impressions: impressions, changedAt: now}
		return 0
	}
	// 印数没涨（也可能因为重新排队而变小），保持原来的计时起点。
	if impressions < prev.impressions {
		p.last[key] = progressMark{impressions: impressions, changedAt: now}
		return 0
	}

	// 顺手清理：这张表与进程同寿命，不清会随任务数无限增长。
	if len(p.last) > 4096 {
		for k, m := range p.last {
			if now.Sub(m.changedAt) > 24*time.Hour {
				delete(p.last, k)
			}
		}
	}
	return now.Sub(prev.changedAt)
}

var printProgress = newProgressTracker()

// decidePrintAction 是「这笔钱该不该收」的【唯一】判断处。
//
// 抽成纯函数是为了能脱离 CUPS 与数据库测它 —— 这段逻辑一旦错了，
// 要么白送钱（该收的没收），要么让用户付了钱拿不到东西。两种都不能接受，
// 所以它的每个分支都要有测试盯着。
//
//	state         CUPS 的 job-state（3..9）
//	found         任务是否还存在（cupsd 会清理旧任务的记录）
//	stuckFor      订单进入 printing 至今的时长
//	noProgressFor 印数停着不动的时长（见 progressTracker）
//
// ⚠️ 「等了很久」【本身不是】取消任务的理由：
//   - processing 且印数在涨 → 正在出纸，哪怕打一小时也要等
//   - pending（排队中）        → 前面有任务，等多久都正常
//
// 只有【processing 但印数长时间不动】才说明后端在空转重试 ——
// 打印机连不上时的真实样子。这时才取消并退款。
func decidePrintAction(state int, found bool, stuckFor, noProgressFor time.Duration) printAction {
	if !found {
		// 任务不见了。可能已打完被 cupsd 清了记录，也可能从没提交成功 ——
		// 判断不了纸有没有出来，所以【不自动退款】。够久了转人工。
		if stuckFor > orderStuckPrintingAfter {
			return printActionUnconfirmed
		}
		return printActionWait
	}

	switch state {
	case cupsJobCompleted:
		return printActionDone

	case cupsJobCanceled, cupsJobAborted:
		// 用户或 CUPS 明确终止了任务，纸一定没出来，可以放心退。
		return printActionRefunded

	case cupsJobProcessing:
		// 正在处理。是「在出纸」还是「在空转重试」，看印数动不动。
		if noProgressFor >= printStuckAfter {
			return printActionCancelRefund
		}
		return printActionWait

	case cupsJobPending, cupsJobPendingHeld, cupsJobProcessingStop:
		// 还在队列里等着。前面有任务排着，等多久都是正常的，
		// 不能因为「等得久」就把用户的任务取消掉 —— 那样他付了钱、
		// 纸没出来、还被告知「已退款」，而他本来只是需要多等一会儿。
		return printActionWait

	default:
		return printActionUnknownState
	}
}

// reconcilePrintingOrder 核对【一个】订单的打印结果并据此定状态。
func reconcilePrintingOrder(ctx context.Context, s *store.Store, o store.PrintOrder, now time.Time) {
	// 节流：前端的轮询比核对本身快得多（见 printCheckGate）。
	if !printCheckGate.allow(o.JobID, now) {
		return
	}

	// IPP 往返放事务外。
	st, found, err := ipp.GetJobState(o.PrinterURI, ipp.JobIDFromURI(o.JobID))
	if err != nil {
		// 查不动（cupsd 挂了/网络抖动）——下一轮再说，不改变订单状态。
		log.Printf("[order] #%d 查询任务状态失败（job=%s）: %v", o.ID, o.JobID, err)
		return
	}
	state := st.State

	stuckFor := now.Sub(parseRFC3339(o.UpdatedAt))
	// 印数停着不动多久了。这是判断「在出纸」还是「在空转重试」的唯一依据。
	noProgressFor := printProgress.observe(o.JobID, st.Impressions, now)

	switch decidePrintAction(state, found, stuckFor, noProgressFor) {
	case printActionWait:
		// 什么都不做。绝大多数轮次落在这里。
		if !found {
			return
		}
		if state == cupsJobProcessing && stuckFor > orderStuckPrintingAfter {
			log.Printf("[order] #%d 任务处理超过 %s 仍未完成（job=%s）", o.ID, orderStuckPrintingAfter, o.JobID)
		}

	case printActionDone:
		finalizeOrderPrinted(ctx, s, o)

	case printActionRefunded:
		log.Printf("[order] #%d 任务被取消/中止（state=%d），全额退款", o.ID, state)
		if _, rerr := startRefundForOrder(ctx, o, "打印任务被取消或中止"); rerr != nil {
			log.Printf("[order] #%d 退款失败: %v", o.ID, rerr)
		}

	case printActionCancelRefund:
		// 先取消，确认它以后也打不出来，再退款。
		//
		// 顺序不能反 —— 直接退款而任务还在队列里，等打印机恢复就会把纸
		// 打出来，变成「钱退了、纸也出了」。
		if cerr := ipp.CancelJob(o.PrinterURI, ipp.JobIDFromURI(o.JobID), o.Username); cerr != nil {
			log.Printf("[order] #%d 取消卡住的任务失败（job=%s）: %v", o.ID, o.JobID, cerr)
			// 取消失败就不退：任务还在队列里，打印机恢复后会打出来。
			return
		}
		log.Printf("[order] #%d 任务卡在 state=%d 超过 %s，已取消并退款", o.ID, state, printStuckAfter)
		if _, rerr := startRefundForOrder(ctx, o, "打印机长时间无响应，任务已取消"); rerr != nil {
			log.Printf("[order] #%d 退款失败: %v", o.ID, rerr)
		}

	case printActionUnconfirmed:
		markPrintUnconfirmed(ctx, s, o, "打印结果无法确认（CUPS 中已无该任务），请核对后决定是否退款")

	default:
		log.Printf("[order] #%d 未知任务状态 %d（job=%s）", o.ID, state, o.JobID)
	}
}

// finalizeOrderPrinted 在 CUPS 确认打完之后把订单落定为已打印。
func finalizeOrderPrinted(ctx context.Context, s *store.Store, o store.PrintOrder) {
	var migrated bool
	if err := s.WithTx(ctx, false, func(tx *sql.Tx) error {
		// 打印记录是【提交时】就建好的（状态 queued），这里只把它改成
		// printed。分开做的好处：用户点完重打在记录页立刻看得到任务，
		// 而不必等纸真的出来 —— 同时状态始终如实。
		if o.PrintJobRecordID != 0 {
			if err := store.UpdatePrintStatus(ctx, tx, o.PrintJobRecordID, "printed", o.JobID); err != nil {
				log.Printf("[order] #%d 更新打印记录状态失败（record=%d）: %v", o.ID, o.PrintJobRecordID, err)
			}
		}
		var err error
		migrated, err = store.MarkOrderPrinted(ctx, tx, o.ID, o.JobID, o.PrintJobRecordID)
		return err
	}); err != nil {
		log.Printf("[order] #%d 标记已打印失败: %v", o.ID, err)
		return
	}
	if migrated {
		log.Printf("[order] #%d 打印完成（job=%s）", o.ID, o.JobID)
	}
}

// startRefundForOrder 是 failOrderAndRefund 在后台巡检里的入口。
func startRefundForOrder(ctx context.Context, o store.PrintOrder, reason string) (string, error) {
	if err := failOrderAndRefund(ctx, o.ID, reason); err != nil {
		return "failed", err
	}
	return "failed", nil
}

// markPrintUnconfirmed 把无法确认结果的订单转人工，【不退款】。
func markPrintUnconfirmed(ctx context.Context, s *store.Store, o store.PrintOrder, reason string) {
	var migrated bool
	if err := s.WithTx(ctx, false, func(tx *sql.Tx) error {
		var err error
		migrated, err = store.MarkOrderPrintUnconfirmed(ctx, tx, o.ID, reason)
		return err
	}); err != nil {
		log.Printf("[order] #%d 转人工失败: %v", o.ID, err)
		return
	}
	if migrated {
		log.Printf("[order] ⚠️ #%d 打印结果无法确认，已转人工（未自动退款）", o.ID)
	}
}

// parseRFC3339 解析时间戳，失败返回零值（调用方用 now.Sub 得到很大的时长，
// 从而走「超时」分支 —— 对无法解析的时间戳保守处理是对的）。
func parseRFC3339(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// expireUnpaidBatches 释放过期未支付的批次，并回收其文件。
//
// 按【批次】而不是按订单处理：同一批必须一起过期，否则会出现
// 「同一批里一半过期、一半还能付」的脏状态。
//
// ⚠️ 顺序是「先迁移状态、后删文件」，这是刻意的：
// 若支付恰在此刻到账，MarkOrdersPaidByPayment 会因为状态已变成 expired
// 而把它推进到 paid（P4 里允许这条迁移），此时文件若还没删就照常打印。
// 真赶上「已过期且文件已删但钱到了」的窄窗口，startPrintForOrder 会发现
// 文件缺失并自动退款 —— 用户拿回钱、我们不打纸，双方都不亏。
func expireUnpaidBatches(ctx context.Context, s *store.Store, uploads string, now time.Time) {
	var batches []string
	if err := s.WithTx(ctx, true, func(tx *sql.Tx) error {
		var err error
		batches, err = store.ListExpiredPendingBatches(ctx, tx, now.Format(time.RFC3339), orderTickBatch)
		return err
	}); err != nil {
		log.Printf("[order] 扫描过期批次失败: %v", err)
		return
	}
	if len(batches) == 0 {
		return
	}

	for _, batchID := range batches {
		var expired []store.PrintOrder
		if err := s.WithTx(ctx, false, func(tx *sql.Tx) error {
			var err error
			expired, err = store.MarkPendingOrdersExpired(ctx, tx, batchID)
			return err
		}); err != nil {
			log.Printf("[order] 批次 %s 过期处理失败: %v", batchID, err)
			continue
		}

		for _, o := range expired {
			removeOrderFiles(o)
		}
		if len(expired) > 0 {
			log.Printf("[order] 批次 %s 超时未支付，已释放 %d 笔订单的文件", batchID, len(expired))
		}
	}
}
