package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"cups-web/internal/auth"
	"cups-web/internal/pricing"
	"cups-web/internal/store"

	"github.com/gorilla/mux"
)

// orderTTL 读待支付订单的有效期。
//
// 订单过期后其持有的上传文件会被后台回收（见 order_worker.go），
// 所以这个值同时决定了「用户可以在支付页停留多久」。
func orderTTL(ctx context.Context) time.Duration {
	minutes := int64(15)
	_ = appStore.WithTx(ctx, true, func(tx *sql.Tx) error {
		v, err := store.GetSettingInt(ctx, tx, store.SettingOrderTTLMinutes, 15)
		if err != nil {
			return err
		}
		minutes = v
		return nil
	})
	if minutes < 1 {
		minutes = 1
	}
	return time.Duration(minutes) * time.Minute
}

// newBatchID 生成批次号。
//
// 时间前缀让运维在日志/库里一眼看出批次的新旧，随机后缀防同一秒内碰撞。
// 单文件下单也分配批次号 —— 支付始终以「批次」为单位，
// 这样一次支付覆盖 N 个订单的模型不需要为 N=1 特判。
func newBatchID() string {
	return "B" + time.Now().UTC().Format("20060102150405") + randomToken()[:6]
}

// ─── 下单 ─────────────────────────────────────────────────────────────

type orderResponse struct {
	OrderID  int64  `json:"orderId"`
	BatchID  string `json:"batchId"`
	Username string `json:"username,omitempty"`
	Filename string `json:"filename"`
	// PrinterURI 是这台打印机的队列地址。刷卡取件的「待取件」提示页要靠它
	// 告诉员工「去哪台取」—— 少了它，那个页面只能说「去打印机那儿」，
	// 而现场通常不止一台。
	PrinterURI     string `json:"printerUri"`
	Status         string `json:"status"`
	Pages          int    `json:"pages"`
	BilledPages    int    `json:"billedPages"`
	Copies         int    `json:"copies"`
	UnitPriceCents int64  `json:"unitPriceCents"`
	AmountCents    int64  `json:"amountCents"`
	RefundedCents  int64  `json:"refundedCents"`
	MatchedPrice   bool   `json:"matchedPrice"`
	PayMethod      string `json:"payMethod,omitempty"`
	JobID          string `json:"jobId,omitempty"`
	Error          string `json:"error,omitempty"`
	ExpiresAt      string `json:"expiresAt"`
	CreatedAt      string `json:"createdAt"`
}

func mapOrder(o store.PrintOrder, matched bool) orderResponse {
	return orderResponse{
		OrderID:        o.ID,
		BatchID:        o.BatchID,
		Username:       o.Username,
		Filename:       o.Filename,
		PrinterURI:     o.PrinterURI,
		Status:         o.Status,
		Pages:          o.Pages,
		BilledPages:    o.BilledPages,
		Copies:         o.Copies,
		UnitPriceCents: o.UnitPriceCents,
		AmountCents:    o.AmountCents,
		RefundedCents:  o.RefundedCents,
		MatchedPrice:   matched,
		PayMethod:      o.PayMethod,
		JobID:          o.JobID,
		Error:          o.Error,
		ExpiresAt:      o.ExpiresAt,
		CreatedAt:      o.CreatedAt,
	}
}

// orderBatchResponse 是下单的返回体。
//
// ⚠️ 单文件与多文件【共用同一个形状】：两种情况下支付、轮询、取消都是同一套流程，
// 前端不需要为「单文件」写一条分支。orders 里只有一条时即单文件下单。
type orderBatchResponse struct {
	BatchID          string          `json:"batchId"`
	OrderIDs         []int64         `json:"orderIds"`
	Orders           []orderResponse `json:"orders"`
	TotalAmountCents int64           `json:"totalAmountCents"`
	BalanceCents     int64           `json:"balanceCents"`
	ExpiresAt        string          `json:"expiresAt"`
	// Failed 列出转换失败的条目。部分失败不影响其余订单 —— 用户不该因为
	// 一个坏文件把整批重传一遍。
	Failed []orderFailedItem `json:"failed"`
	// Held 为真表示这是一批【刷卡取件】的任务：已暂存，等员工刷卡才出纸。
	//
	// 前端靠它把「已提交打印」换成「已暂存，请到打印机刷卡取件」。
	// 重打那条路也共用这个响应体（见 order_reprint.go），所以它才能
	// 「不用为取件模式写一套独立响应」—— 与 handleReprint 现有的
	// 「靠响应形状分流」约定一致。
	Held bool `json:"held,omitempty"`
}

type orderFailedItem struct {
	Filename string `json:"filename"`
	Error    string `json:"error"`
}

// orderCreateHandler 创建订单（单文件 file / 多文件 files）。
//
// 计费关闭时直接 404 —— 计费没开就不该有订单，订单页签也不会出现。
func orderCreateHandler(w http.ResponseWriter, r *http.Request) {
	if !billingEnabled(r.Context()) {
		writeJSONError(w, http.StatusNotFound, "计费未开启")
		return
	}

	sess, err := auth.GetSession(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	// 运行时兜底：访客账号是所有人共用的，它的余额是「公摊钱包」。
	// 管理端已经禁止计费与访客模式同时开启，但存量配置或直接改库都可能绕过，
	// 所以这里再挡一道。
	if sess.Username == guestUsername {
		writeJSONError(w, http.StatusForbidden, "访客模式不支持计费打印")
		return
	}

	maxPages, _ := getPrintLimits(r.Context())
	applyUploadLimit(w, r)
	if err := r.ParseMultipartForm(512 << 20); err != nil {
		if isMaxBytesError(err) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "文件超出管理员设置的大小上限")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "invalid multipart form")
		return
	}

	form, err := parseOrderForm(r)
	if err != nil {
		writeClientError(w, err)
		return
	}

	// ── 打印机不可用就不接单 ──
	//
	// 这是「打印机离线」的【主要】防线，而不是靠送印失败后退款。
	// 顺序上很关键：先拦住，用户根本不会付钱；等到送印才发现，钱已经收了，
	// 剩下的只有退款 —— 那是最差的体验，用户要的是纸不是退款。
	//
	// 检查放在这里（而不是转换完再查）：转换可能要几十秒，
	// 让用户在等待之前就知道打印机不可用，别白等一场。
	//
	// ⚠️ 送印前还会再查一次：下单到送印之间打印机完全可能掉线。
	if reason := printerUnavailableReason(form.Printer); reason != "" {
		log.Printf("[order] 下单被拒：打印机不可用 %s（%s）", form.Printer, reason)
		writeJSONError(w, http.StatusUnprocessableEntity, "打印机当前不可用："+reason)
		return
	}

	// 收集待下单的文件：单文件走 file 字段，多文件走 files（与 /api/convert 一致）。
	type uploadItem struct {
		file     multipartFile
		filename string
	}
	var items []uploadItem

	if f, fh, ferr := r.FormFile("file"); ferr == nil {
		defer f.Close()
		items = append(items, uploadItem{file: f, filename: fh.Filename})
	}
	if r.MultipartForm != nil {
		for _, fh := range r.MultipartForm.File["files"] {
			f, ferr := fh.Open()
			if ferr != nil {
				continue
			}
			defer f.Close()
			items = append(items, uploadItem{file: f, filename: fh.Filename})
		}
	}

	if len(items) == 0 {
		writeJSONError(w, http.StatusBadRequest, "missing file field")
		return
	}

	rules, fallback, err := loadPricingSnapshot(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load prices")
		return
	}

	expires := time.Now().UTC().Add(orderTTL(r.Context())).Format(time.RFC3339)
	batchID := newBatchID()

	resp := orderBatchResponse{
		BatchID:   batchID,
		OrderIDs:  []int64{},
		Orders:    []orderResponse{},
		ExpiresAt: expires,
		Failed:    []orderFailedItem{},
	}

	for _, item := range items {
		order, amount, matched, perr := createOneOrder(r.Context(), sess.UserID, batchID,
			orderOrigin{Source: store.OrderSourceWeb}, form,
			item.file, item.filename, maxPages, rules, fallback, expires)
		if perr != nil {
			log.Printf("[order] 下单失败 file=%q: %v", item.filename, perr)
			resp.Failed = append(resp.Failed, orderFailedItem{
				Filename: item.filename,
				Error:    clientMessage(perr),
			})
			continue
		}
		resp.Orders = append(resp.Orders, mapOrder(order, matched))
		resp.OrderIDs = append(resp.OrderIDs, order.ID)
		resp.TotalAmountCents += amount
	}

	// 整批都没成功就不算「部分失败」，直接按第一个错误返回，
	// 否则前端会收到一个没有任何订单、也没有明确错误的 200。
	if len(resp.Orders) == 0 {
		writeJSONError(w, http.StatusBadRequest, resp.Failed[0].Error)
		return
	}

	if balance, berr := readUserBalance(r.Context(), sess.UserID); berr == nil {
		resp.BalanceCents = balance
	}
	writeJSON(w, resp)
}

// multipartFile 是 io.Reader 的别名，仅为让 uploadItem 更可读。
type multipartFile = interface {
	Read(p []byte) (int, error)
	Close() error
}

// orderOrigin 标识订单的来源。
//
// 下单内核（createOneOrder）对三种来源一视同仁 —— 转换、计价、落库完全相同，
// 只有 source / reprint_of / scheduled_id 这三个纯审计字段不一样。
// 把它单独拎出来而不是给 createOneOrder 加三个散参数：那三个字段永远一起出现，
// 而且新增来源（比如将来的「邮件打印」）时只改这里。
type orderOrigin struct {
	Source      string // store.OrderSourceWeb / Reprint / Scheduled / Pull
	ReprintOf   int64  // 重打来源 print_jobs.id，0 表示非重打
	ScheduledID int64  // 定时任务 id，0 表示非定时
	// Hold 表示这是一条【刷卡取件】的订单：不收费，落库时进 held 状态
	// （待取件）而不是 pending_payment，刷卡才送印。
	//
	// 它是「计费」与「取件」两种模式的唯一分叉点，所以刻意做成显式字段
	// 而不是靠 amount==0 之类的隐式判据 —— 后者在将来加免费单时会静默走错。
	Hold bool
}

// createOneOrder 走完整流水线并把订单落库。
func createOneOrder(ctx context.Context, userID int64, batchID string, origin orderOrigin, form orderForm,
	file multipartFile, filename string, maxPages int64,
	rules []pricing.Rule, fallback int64, expiresAt string) (store.PrintOrder, int64, bool, error) {

	prepared, err := prepareOrderPrint(ctx, form, file, filename, maxPages)
	if err != nil {
		return store.PrintOrder{}, 0, false, err
	}

	// 刷卡取件不收费。做法是【用空价目 + 零兜底价走同一个 Compute】，
	// 而不是跳过它 —— Compute 顺带做了两件与钱无关、但必须做的事：
	// 把 copies 兜底成 >= 1，以及校验页码范围（「范围内没有可打印的页面」
	// 在取件模式下同样是错误，只是不涉及钱）。
	//
	// 🚫 不要用「加一条 0 元价目规则」来实现免费：那会把「不收费」表达成
	// 一份可以改的配置，管理员哪天整理价目表把它删了，取件任务就开始收钱。
	if origin.Hold {
		rules, fallback = nil, 0
	}

	// ⚠️ 计费用【重排前】的页数（prepared.DocPages）。
	// even-reverse 会补一张空白页，用 PrintPages 会多收钱。
	quote, err := pricing.Compute(
		prepared.DocPages, form.Copies, form.IsColor, form.PaperSize, form.PaperType,
		form.PageRange, form.PageSet, rules, fallback)
	if err != nil {
		discardPreparedFiles(prepared)
		return store.PrintOrder{}, 0, false, orderPricingError(err)
	}

	// 计费口径的两列在取件模式下强制归零：它们进的是营业统计，
	// 而取件模式压根没有营业额这回事，留着非零值只会污染报表。
	billedPages, unitCents := quote.BilledPages, quote.UnitCents
	status := store.OrderStatusPendingPayment
	if origin.Hold {
		billedPages, unitCents = 0, 0
		status = store.OrderStatusHeld
	}

	order := store.PrintOrder{
		UserID:      userID,
		Source:      origin.Source,
		ReprintOf:   origin.ReprintOf,
		ScheduledID: origin.ScheduledID,
		BatchID:     batchID,
		PrinterURI:  form.Printer,
		Filename:    filename,
		StoredPath:  prepared.StoredRel,
		PrintPath:   prepared.PrintRel,
		PrintMime:   prepared.PrintMime,
		Pages:       prepared.PrintPages,
		BilledPages: billedPages,

		IsDuplex:        prepared.EffectiveDuplex,
		IsColor:         form.IsColor,
		Copies:          quote.Copies,
		Orientation:     form.Orientation,
		PaperSize:       form.PaperSize,
		PaperType:       form.PaperType,
		MediaSource:     form.MediaSource,
		PrintScaling:    prepared.EffectiveScaling,
		ReqPrintScaling: form.PrintScaling,
		PageRange:       form.PageRange,
		PageSet:         prepared.EffectivePageSet,
		ReqPageSet:      form.PageSet,
		Mirror:          form.Mirror,
		WatermarkText:   form.Watermark,
		NumberUp:        form.NumberUp,
		NumberUpLayout:  form.NumberUpLayout,
		PageBorder:      form.PageBorder,

		UnitPriceCents: unitCents,
		AmountCents:    quote.AmountCents,

		Status:    status,
		ExpiresAt: expiresAt,
	}

	var orderID int64
	err = appStore.WithTx(ctx, false, func(tx *sql.Tx) error {
		id, err := store.InsertPrintOrder(ctx, tx, &order)
		if err != nil {
			return err
		}
		orderID = id
		return nil
	})
	if err != nil {
		discardPreparedFiles(prepared)
		return store.PrintOrder{}, 0, false, err
	}

	order.ID = orderID
	order.ExpiresAt = expiresAt
	order.CreatedAt = nowRFC3339()
	return order, quote.AmountCents, quote.Matched, nil
}

// orderPricingError 把计价错误转成面向用户的文案。
func orderPricingError(err error) error {
	switch {
	case errors.Is(err, pricing.ErrInvalidPageRange):
		return unprocessableErr(msgInvalidPageRange)
	case errors.Is(err, pricing.ErrNoPagesSelected):
		return unprocessableErr("选择的页面范围内没有可打印的页面")
	case errors.Is(err, pricing.ErrUnsupportedCombination):
		return unprocessableErr("偶数页倒序不能与页面范围同时使用")
	default:
		return err
	}
}

// clientMessage 取错误里适合展示给用户的那部分。
func clientMessage(err error) string {
	var ce *clientError
	if errors.As(err, &ce) {
		return ce.Msg
	}
	return "下单失败"
}

// discardPreparedFiles 丢弃一次【失败下单】留下的文件，避免 uploads 里堆孤儿。
// 与 maintenance.go 的 cleanupOrderArtifacts（回收终态订单的文件）不是一回事。
func discardPreparedFiles(prepared orderPrepared) {
	if prepared.StoredAbs != "" {
		_ = os.Remove(prepared.StoredAbs)
	}
	// print_path 与 stored_path 相同时（PDF 分支）上面已经删过了，重复删是 no-op。
	if prepared.PrintAbs != "" && prepared.PrintAbs != prepared.StoredAbs {
		_ = os.Remove(prepared.PrintAbs)
	}
}

// loadPricingSnapshot 一次性读出价目表与兜底价，供整批下单复用。
// 一批文件必须按同一份价目计价，逐个读会出现「同一批次单价不同」的怪事。
func loadPricingSnapshot(ctx context.Context) ([]pricing.Rule, int64, error) {
	var rules []pricing.Rule
	var fallback int64
	err := appStore.WithTx(ctx, true, func(tx *sql.Tx) error {
		var err error
		if rules, err = loadPricingRules(ctx, tx); err != nil {
			return err
		}
		fallback, err = loadFallbackUnitCents(ctx, tx)
		return err
	})
	return rules, fallback, err
}

func readUserBalance(ctx context.Context, userID int64) (int64, error) {
	var balance int64
	err := appStore.WithTx(ctx, true, func(tx *sql.Tx) error {
		var err error
		balance, err = store.GetBalance(ctx, tx, userID)
		return err
	})
	return balance, err
}

// ─── 查询 ─────────────────────────────────────────────────────────────

// orderListHandler 返回当前用户的订单（不含别人的）。
func orderListHandler(w http.ResponseWriter, r *http.Request) {
	sess, err := auth.GetSession(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	q := r.URL.Query()
	filter := store.OrderFilter{
		Username: sess.Username,
		Status:   q.Get("status"),
		StartAt:  q.Get("start"),
		EndAt:    q.Get("end"),
		Limit:    100,
	}

	var orders []store.PrintOrder
	err = appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
		var err error
		orders, err = store.ListOrders(r.Context(), tx, filter)
		return err
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list orders")
		return
	}

	out := make([]orderResponse, 0, len(orders))
	for _, o := range orders {
		out = append(out, mapOrder(o, true))
	}
	writeJSON(w, out)
}

// orderBatchStatusHandler 返回一个批次的状态，供支付弹窗轮询。
//
// 返回的是【整批】的聚合状态 + 每个订单的明细：批量下单下用户需要看到
// 「3 个成功、1 个失败已退款」这种部分成功的结果。
func orderBatchStatusHandler(w http.ResponseWriter, r *http.Request) {
	sess, err := auth.GetSession(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	batchID := mux.Vars(r)["batchId"]

	// 借这次轮询顺带问一次渠道（内部有 5 秒节流），并在事务外完成结算。
	// 用户扫码付完几秒内订单就会推进 —— 不必去点「我已支付」。
	// 绝大多数轮询会因为「没有未结算的支付单」而立即返回。
	syncUnsettledPaymentForBatch(r.Context(), batchID)

	// 同理，顺带核对一下这批订单在 CUPS 侧的真实打印结果。
	// 少了它，用户付完钱要盯着「打印中」等后台巡检那 30 秒 ——
	// 其中大半时间只是没人在问 CUPS 而已。
	confirmPrintResultsForBatch(r.Context(), batchID)

	var orders []store.PrintOrder
	var balance int64
	err = appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
		owns, err := store.OwnsOrderBatch(r.Context(), tx, batchID, sess.UserID)
		if err != nil {
			return err
		}
		// 不区分「不存在」与「不属于你」，避免拿别人的 batchId 探测订单是否存在。
		if !owns {
			return sql.ErrNoRows
		}
		if orders, err = store.ListOrdersByBatch(r.Context(), tx, batchID); err != nil {
			return err
		}
		balance, err = store.GetBalance(r.Context(), tx, sess.UserID)
		return err
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "order batch not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to load order batch")
		return
	}

	writeJSON(w, buildBatchStatus(batchID, orders, balance))
}

type orderBatchStatus struct {
	BatchID          string          `json:"batchId"`
	Status           string          `json:"status"`
	TotalAmountCents int64           `json:"totalAmountCents"`
	RefundedCents    int64           `json:"refundedCents"`
	BalanceCents     int64           `json:"balanceCents"`
	Orders           []orderResponse `json:"orders"`
}

func buildBatchStatus(batchID string, orders []store.PrintOrder, balance int64) orderBatchStatus {
	out := orderBatchStatus{
		BatchID:      batchID,
		BalanceCents: balance,
		Orders:       make([]orderResponse, 0, len(orders)),
	}
	// ⚠️ expired 与 cancelled 必须单独计数。早先把它们和 failed 归成一桶，
	// 结果是一个「全部已过期」的批次在界面上显示成「失败」—— 用户会以为
	// 打印机出了问题，实际上只是他没付款、订单自然到期了。
	pending, processing, printed := 0, 0, 0
	failed, expired, cancelled := 0, 0, 0
	// ⚠️ held（刷卡取件的待取件）必须单独计数。少了它，这些订单会掉进
	// 下面的 default 被当成 failed —— 中间页会显示「打印失败」，
	// 而实际上任务好好地躺在待取件里等着刷卡。
	held := 0
	for _, o := range orders {
		out.Orders = append(out.Orders, mapOrder(o, true))
		out.TotalAmountCents += o.AmountCents
		out.RefundedCents += o.RefundedCents

		switch o.Status {
		case store.OrderStatusPendingPayment:
			pending++
		case store.OrderStatusHeld:
			held++
		case store.OrderStatusPaid, store.OrderStatusPrinting:
			processing++
		case store.OrderStatusPrinted:
			printed++
		case store.OrderStatusExpired:
			expired++
		case store.OrderStatusCancelled:
			cancelled++
		default:
			// failed / refunded
			failed++
		}
	}

	notPrinted := failed + expired + cancelled
	switch {
	case held > 0:
		// 还有没取走的 —— 这比其它任何状态都更该让用户看到。
		out.Status = store.OrderStatusHeld
	case pending > 0:
		out.Status = store.OrderStatusPendingPayment
	case processing > 0:
		out.Status = "processing"
	case printed > 0 && notPrinted == 0:
		out.Status = store.OrderStatusPrinted
	case printed > 0:
		// 部分成功：批量下单里有的打成、有的没成。
		out.Status = "partial"
	case len(orders) > 0 && expired == len(orders):
		out.Status = store.OrderStatusExpired
	case len(orders) > 0 && cancelled == len(orders):
		out.Status = store.OrderStatusCancelled
	default:
		out.Status = store.OrderStatusFailed
	}
	return out
}

// ─── 取消 ─────────────────────────────────────────────────────────────

// orderCancelHandler 取消一个批次里仍待支付的订单，并回收其文件。
func orderCancelHandler(w http.ResponseWriter, r *http.Request) {
	sess, err := auth.GetSession(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	batchID := mux.Vars(r)["batchId"]

	var cancelled int
	err = appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
		ctx := r.Context()
		owns, err := store.OwnsOrderBatch(ctx, tx, batchID, sess.UserID)
		if err != nil {
			return err
		}
		if !owns {
			return sql.ErrNoRows
		}
		orders, err := store.GetPendingOrdersByBatch(ctx, tx, batchID)
		if err != nil {
			return err
		}
		for _, o := range orders {
			ok, err := store.MarkOrderCancelled(ctx, tx, o.ID)
			if err != nil {
				return err
			}
			if ok {
				cancelled++
				removeOrderFiles(o)
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "order batch not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to cancel order")
		return
	}

	writeJSON(w, map[string]interface{}{"ok": true, "cancelled": cancelled})
}

// removeOrderFiles 删除订单持有的文件。
//
// 🚫 定时任务（source='scheduled'）的 stored_path 指向 uploads/scheduled/ 下的
// 共享源文件，多份记录都指向它 —— 删掉会连带毁掉别的订单与任务本身。
func removeOrderFiles(o store.PrintOrder) {
	if o.Source == store.OrderSourceScheduled || isScheduledRel(o.StoredPath) {
		return
	}
	// 走 removeFileLogged 而不是裸 os.Remove：它会重试并在最终失败时记日志。
	// 裸删在 Windows 上遇到瞬时占用会静默失败，磁盘慢慢涨满却毫无痕迹。
	removeFileLogged(uploadDir, o.StoredPath)
	if o.PrintPath != o.StoredPath {
		removeFileLogged(uploadDir, o.PrintPath)
	}
}
