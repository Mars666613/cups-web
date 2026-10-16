package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"cups-web/internal/auth"
	"cups-web/internal/ipp"
	"cups-web/internal/store"

	"github.com/gorilla/mux"
)

// startPrintForOrder 是【唯一】的订单送印执行者。
//
// 返回值 status ∈ {printed, failed, skipped}；skipped 表示订单不在 paid
// 状态（已被别的调用者领走）。
//
// 之所以只有这一个入口：支付回调、余额支付、后台补偿扫描三条路都可能
// 触发送印，如果各写一份，任何一处漏掉状态判断就是「付一次印多次」。
// 状态迁移本身（ClaimOrderForPrint）是原子闸门，这里只是它唯一的调用者。
func startPrintForOrder(ctx context.Context, orderID int64) (string, error) {
	var order store.PrintOrder
	var claimed bool

	err := appStore.WithTx(ctx, false, func(tx *sql.Tx) error {
		var err error
		claimed, err = store.ClaimOrderForPrint(ctx, tx, orderID)
		if err != nil || !claimed {
			return err
		}
		order, err = store.GetOrderByID(ctx, tx, orderID)
		return err
	})
	if err != nil {
		return "failed", fmt.Errorf("领取订单失败: %w", err)
	}
	if !claimed {
		// 已被别人领走，或者状态不是 paid。不是错误。
		return "skipped", nil
	}

	// os.OpenInRoot 自带目录逃逸防护（Go 1.24+），与项目其它读上传文件的地方一致。
	f, mime, err := openOrderPrintFile(order)
	if err != nil {
		return "failed", failOrderAndRefund(ctx, orderID, "待打印文件缺失")
	}
	defer f.Close()

	// ── 提交前确认打印机可用 ──
	//
	// 打印机离线时 CUPS 照样会【收下】任务并挂在队列里反复重试
	// （界面上就是 "Connecting to printer"），纸一张都不会出来。
	// 钱已经付了，这里唯一能做的就是失败退款 —— 但这依然是下策，
	// 用户要的是纸不是退款。真正的闸门在【下单时】（orderCreateHandler），
	// 那里打印机不可用就直接不接单，用户压根不会付钱。
	//
	// ⚠️ 两次检查都要有：下单到送印之间打印机完全可能掉线。
	if reason := printerUnavailableReason(order.PrinterURI); reason != "" {
		log.Printf("[order] #%d 打印机不可用，不提交：%s", orderID, reason)
		return "failed", failOrderAndRefund(ctx, orderID, "打印机当前不可用："+reason)
	}

	job, err := ipp.SendPrintJob(order.PrinterURI, f, mime, order.Username, order.Filename, buildPrintJobOptions(order))
	if err != nil {
		log.Printf("[order] #%d 送印失败: %v", orderID, err)
		return "failed", failOrderAndRefund(ctx, orderID, "打印失败："+err.Error())
	}

	// ⚠️ 到这一步 CUPS 只是【收下了】任务，纸还没出来。
	//
	// IPP 的 Print-Job 是异步的：successful-ok 的含义是「已入队」。
	// 早先这里直接 MarkOrderPrinted，于是打印机离线时会出现
	// 「记录显示已打印、CUPS 里任务还在排队、钱也不退」—— 订单早早离开
	// printing，连卡死回收都够不着。
	//
	// 现在：打印记录【此刻就建】（状态 queued），订单【停在 printing】，
	// 由后台/前端的轮询去 CUPS 核对真实结果。
	//
	// 记录之所以不等确认后再建：用户在「打印记录」页看不到刚提交的任务，
	// 会以为重打没生效 —— 而它其实已经在队列里了。状态如实标成 queued，
	// 确认后再改成 printed，既不骗人也不让人干等。
	recordID := recordOrderInPrintHistory(ctx, order, job)
	if err := appStore.WithTx(ctx, false, func(tx *sql.Tx) error {
		return store.SetOrderSubmitted(ctx, tx, orderID, job, recordID)
	}); err != nil {
		// 任务号没记上，后台就永远核实不了这单。如实报错并退款 ——
		// 但我们并不知道纸有没有出来，所以措辞上要留余地。
		log.Printf("[order] #%d 已提交到 CUPS 但任务号回写失败（job=%s）: %v", orderID, job, err)
		return "failed", failOrderAndRefund(ctx, orderID, "打印任务号记录失败，请确认打印机是否已出纸")
	}

	log.Printf("[order] #%d 已提交到 CUPS（job=%s），等待打印完成确认", orderID, job)
	return "printing", nil
}

// ─── 两条送印路径共用的零件 ─────────────────────────────────────────
//
// 送印有两条路：计费的 startPrintForOrder（支付后送），和刷卡取件的
// startPullPrintForOrder（刷卡后送）。它们送的【是同一份参数快照】，
// 拼 IPP 与读文件这两段必须共用 —— 各写一份的表现是「某种模式下
// 某个参数被悄悄丢了」，而且只有拿真打印机才看得出来。

// buildPrintJobOptions 从订单的参数快照拼出 IPP 请求选项。
func buildPrintJobOptions(order store.PrintOrder) ipp.PrintJobOptions {
	return ipp.PrintJobOptions{
		IsDuplex:     order.IsDuplex,
		IsColor:      order.IsColor,
		Copies:       order.Copies,
		Orientation:  order.Orientation,
		PaperSize:    order.PaperSize,
		PaperType:    order.PaperType,
		PrintScaling: order.PrintScaling,
		MediaSource:  order.MediaSource,
		PageRange:    order.PageRange,
		PageSet:      order.PageSet,
		Mirror:       order.Mirror,

		NumberUp:       order.NumberUp,
		NumberUpLayout: order.NumberUpLayout,
		PageBorder:     order.PageBorder,
	}
}

// openOrderPrintFile 打开订单的待打印文件并判定它的 mime。
//
// mime 优先用订单里存的；存空时才现探一次。调用方负责 Close。
func openOrderPrintFile(order store.PrintOrder) (*os.File, string, error) {
	// os.OpenInRoot 自带目录逃逸防护（Go 1.24+），与项目其它读上传文件的地方一致。
	f, err := os.OpenInRoot(uploadDir, filepath.FromSlash(order.PrintPath))
	if err != nil {
		return nil, "", err
	}
	mime := order.PrintMime
	if mime == "" {
		mime = detectOrderMime(f)
	}
	return f, mime, nil
}

// printerUnavailableReason 返回打印机不可用的原因；可用时返回空串。
//
// 判据是【直连探测设备本身】，而不是读 cupsd 的状态：
//
//	printer-state         队列没活儿干就是 idle，cupsd 不会主动探打印机
//	printer-state-message 只有送过任务、且正在重试时才有内容
//
// 也就是说打印机离线但队列空闲时，cupsd 那一边【完全看不出问题】——
// 照着它判断，用户照样能把钱付出去。
//
// ⚠️ 探不到（拿不到 device-uri、usb 队列、探测超时）一律放行 ——
// 「探不了」不等于「不可达」，把两者混为一谈会把 USB 打印机的用户
// 全挡在门外。那种情况交给送印后的印数核对兜底。
func printerUnavailableReason(printerURI string) string {
	return deviceUnavailableReason(printerDeviceStatus(printerURI))
}

// detectOrderMime 在订单没有记录 MIME 时按内容嗅探。
// 与 printHandler 的做法一致，失败时退回 octet-stream 让 CUPS 自己判断。
func detectOrderMime(f *os.File) string {
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "application/octet-stream"
	}
	if n == 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(buf[:n])
}

// recordOrderInPrintHistory 在 save_history 打开时补一条打印历史，
// 让用户在「打印记录」页也能看到这笔订单产生的任务。
// 失败不影响送印结果，只记日志。
//
// ⚠️ 记录在【提交到 CUPS 的那一刻】就建，状态是 queued 而不是 printed。
// 等确认打完再建的话，用户点完「重新打印」在记录页什么都看不到，
// 会以为没生效 —— 而任务其实已经在队列里了。
func recordOrderInPrintHistory(ctx context.Context, order store.PrintOrder, jobID string) int64 {
	var saveHistory bool
	if err := appStore.WithTx(ctx, true, func(tx *sql.Tx) error {
		v, err := store.GetSettingInt(ctx, tx, store.SettingSaveHistory, 1)
		if err != nil {
			return err
		}
		saveHistory = v != 0
		return nil
	}); err != nil {
		saveHistory = true
	}
	if !saveHistory {
		return 0
	}

	var recordID int64
	err := appStore.WithTx(ctx, false, func(tx *sql.Tx) error {
		rec := store.PrintRecord{
			UserID:     order.UserID,
			PrinterURI: order.PrinterURI,
			Filename:   order.Filename,
			StoredPath: order.StoredPath,
			Pages:      order.Pages,
			// 刚提交，还没打完。确认后由 finalizeOrderPrinted 改成 printed。
			Status:   "queued",
			IsDuplex: order.IsDuplex,
			IsColor:  order.IsColor,

			Copies:         order.Copies,
			Orientation:    order.Orientation,
			PaperSize:      order.PaperSize,
			PaperType:      order.PaperType,
			MediaSource:    order.MediaSource,
			PrintScaling:   order.ReqPrintScaling,
			PageRange:      order.PageRange,
			PageSet:        order.ReqPageSet,
			Mirror:         order.Mirror,
			WatermarkText:  order.WatermarkText,
			NumberUp:       order.NumberUp,
			NumberUpLayout: order.NumberUpLayout,
			PageBorder:     order.PageBorder,

			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}
		var err error
		recordID, err = store.InsertPrintRecord(ctx, tx, &rec)
		if err != nil {
			return err
		}
		return store.UpdatePrintStatus(ctx, tx, recordID, "queued", jobID)
	})
	if err != nil {
		log.Printf("[order] #%d 写入打印历史失败: %v", order.ID, err)
		return 0
	}
	return recordID
}

// failOrderAndRefund 把订单置为失败，并把已付金额全额退回余额。
//
// 无论当初用什么方式付的，都退到【余额】—— 微信/支付宝的原路退款需要
// 商户后台人工操作，本系统做不到。这一点必须在前端文案里讲清楚，
// 否则用户会去账单里找那笔"没退回来"的钱。
func failOrderAndRefund(ctx context.Context, orderID int64, reason string) error {
	err := appStore.WithTx(ctx, false, func(tx *sql.Tx) error {
		order, err := store.GetOrderByID(ctx, tx, orderID)
		if err != nil {
			return err
		}

		ok, err := store.MarkOrderFailed(ctx, tx, orderID, reason, store.OrderStatusPrinting)
		if err != nil {
			return err
		}
		if !ok {
			// 状态已经不是 printing（比如后台的卡死回收先一步处理了）。
			return nil
		}

		// 打印记录也要跟着标失败，否则它会在「打印记录」页永远挂着
		// 「排队中」—— 用户看到一条永远不动的记录，比看到失败更困惑。
		if order.PrintJobRecordID != 0 {
			if err := store.UpdatePrintStatus(ctx, tx, order.PrintJobRecordID, "failed", order.JobID); err != nil {
				log.Printf("[order] #%d 标记打印记录失败（record=%d）: %v", orderID, order.PrintJobRecordID, err)
			}
		}

		if order.AmountCents <= 0 {
			return nil
		}

		if _, err := store.ApplyWalletDelta(ctx, tx, store.WalletDeltaInput{
			UserID:         order.UserID,
			Kind:           store.WalletKindRefund,
			AmountCents:    order.AmountCents,
			IdempotencyKey: store.WalletKeyRefund(orderID),
			OrderID:        orderID,
			PaymentID:      order.PaymentID,
			Note:           "打印失败自动退款",
		}); err != nil {
			if errors.Is(err, store.ErrDuplicateTransaction) {
				// 幂等键已存在 = 这笔退款连同下面的订单/支付单金额累加
				// 已经在同一个事务里提交过了，当成功处理。
				return nil
			}
			return err
		}

		// 订单行的已退金额也必须更新 —— 只改钱包不改订单的话，
		// 订单会显示「失败但未退款」而钱其实已经回去了，账目对不上。
		if err := store.AddOrderRefunded(ctx, tx, orderID, order.AmountCents); err != nil {
			return err
		}
		if order.PaymentID != 0 {
			return store.AddPaymentRefunded(ctx, tx, order.PaymentID, order.AmountCents)
		}
		return nil
	})
	if err != nil {
		log.Printf("[order] #%d 失败退款未完成: %v", orderID, err)
		return fmt.Errorf("订单失败处理出错: %w", err)
	}
	return nil
}

// ─── 刷卡取件的送印 ───────────────────────────────────────────────────

// startPullPrintForOrder 把一条【已被领取】（status=printing）的持件订单送到
// CUPS。它是 startPrintForOrder 在刷卡取件模式下的孪生兄弟，读文件与拼 IPP
// 完全共用，区别只有一处 —— 但那一处是致命的：
//
//	失败时【退回 held】，绝不走 failOrderAndRefund。
//
// 员工没付钱，任务不该丢。退回 held 可以重刷、可以等打印机恢复、到期还有
// TTL 兜底，失败语义完全可逆。而 failOrderAndRefund 会先把订单标成 failed
// （不管金额是不是 0 —— 它在退款之前那一步就已经改了状态），员工就得重新
// 上传一遍。
//
// 调用方（kiosk_handlers.go）负责先原子领取（store.ClaimHeldOrdersForPrinter）。
// 这里拿到的是已经处于 printing 的订单，所以【不】再 claim 一次。
//
// 返回的 error 非空表示这次没送成，且【订单状态已经处理完毕】，
// 错误文本就是给终端/日志看的原因。
func startPullPrintForOrder(ctx context.Context, order store.PrintOrder) error {
	// 打印机可用性：领取之前 kiosk handler 已经查过一次，这里是第二次。
	// 两次都要有 —— 领取到送印之间打印机完全可能掉线（同 startPrintForOrder）。
	if reason := printerUnavailableReason(order.PrinterURI); reason != "" {
		log.Printf("[pull] #%d 打印机不可用，不提交：%s", order.ID, reason)
		return revertPullOrder(ctx, order.ID, "打印机当前不可用："+reason)
	}

	f, mime, err := openOrderPrintFile(order)
	if err != nil {
		// ⚠️ 文件缺失【不】退回 held：重刷多少次都一样，那是个死循环。
		// 标失败，让员工在记录页看到明确的原因。
		log.Printf("[pull] #%d 待打印文件缺失: %v", order.ID, err)
		return failPullOrder(ctx, order.ID, "待打印文件缺失")
	}
	defer f.Close()

	job, err := ipp.SendPrintJob(order.PrinterURI, f, mime, order.Username, order.Filename, buildPrintJobOptions(order))
	if err != nil {
		log.Printf("[pull] #%d 送印失败: %v", order.ID, err)
		return revertPullOrder(ctx, order.ID, "打印失败："+err.Error())
	}

	// 到这一步 CUPS 只是【收下了】任务（IPP 的 Print-Job 是异步的，见
	// startPrintForOrder 那段长注释）。订单停在 printing，交给现有的核对
	// 链路（confirmPrintResults → decidePrintAction）去确认真实结果 ——
	// 与计费路径完全同一套，这里不另起一套。
	recordID := recordOrderInPrintHistory(ctx, order, job)
	if err := appStore.WithTx(ctx, false, func(tx *sql.Tx) error {
		return store.SetOrderSubmitted(ctx, tx, order.ID, job, recordID)
	}); err != nil {
		// ⚠️ 这里【不能】退回 held：任务真的进了 CUPS，纸可能已经出来了。
		// 退回 held 会让下次刷卡再打一份。标失败并留证给人判断。
		log.Printf("[pull] #%d 已提交到 CUPS 但任务号回写失败（job=%s）: %v", order.ID, job, err)
		return failPullOrder(ctx, order.ID, "打印任务号记录失败，请确认打印机是否已出纸")
	}

	log.Printf("[pull] #%d 已提交到 CUPS（job=%s），等待打印完成确认", order.ID, job)
	return nil
}

// revertPullOrder 送印失败时把订单退回待取件 —— 员工可以原地重刷。
//
// 返回的 error 带上原因，供调用方回报给取件终端（无屏幕时它会进日志）。
func revertPullOrder(ctx context.Context, orderID int64, reason string) error {
	var reverted bool
	if err := appStore.WithTx(ctx, false, func(tx *sql.Tx) error {
		var err error
		reverted, err = store.RevertOrderToHeld(ctx, tx, orderID, reason)
		return err
	}); err != nil {
		return fmt.Errorf("退回待取件失败: %w", err)
	}
	if !reverted {
		// 状态已经不是 printing 且 job_id 为空 —— 可能被后台的孤儿扫描
		// 先一步处理了，或者任务其实已经发出去了。如实记一笔，别静默。
		log.Printf("[pull] #%d 退回待取件未生效（状态已变），按失败上报", orderID)
	}
	return errors.New(reason)
}

// failPullOrder 把持件订单标成失败，【且不退任何款】。
//
// 🚫 走 MarkOrderFailed 而不是 failOrderAndRefund —— 后者带退款语义，
// 而取件订单金额恒为 0、压根没有钱可退。混进去只会让「这条路会不会动钱」
// 这件事变得必须读一遍才能确认，而这是资金安全的关键路径。
func failPullOrder(ctx context.Context, orderID int64, reason string) error {
	if err := appStore.WithTx(ctx, false, func(tx *sql.Tx) error {
		ok, err := store.MarkOrderFailed(ctx, tx, orderID, reason, store.OrderStatusPrinting)
		if err != nil {
			return err
		}
		if !ok {
			log.Printf("[pull] #%d 标失败未生效（状态已变）", orderID)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("标记失败出错: %w", err)
	}
	return errors.New(reason)
}

// ─── 余额支付 ─────────────────────────────────────────────────────────

// orderPayHandler 支付一个批次。
//
// P3 只支持余额支付；在线支付（mock / 支付宝 / 微信）在 P4 接入，
// 届时这里会按 method 分流到 payment provider。
func orderPayHandler(w http.ResponseWriter, r *http.Request) {
	sess, err := auth.GetSession(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	batchID := mux.Vars(r)["batchId"]

	var payload struct {
		Method string `json:"method"`
		// Channel / ReturnPath 由前端按【当前设备与当前页面】给出，见 onlinePayOptions。
		Channel    string `json:"channel"`
		ReturnPath string `json:"returnPath"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	if payload.Method == "" {
		payload.Method = store.PaymentProviderBalance
	}

	opts := onlinePayOptions{Channel: payload.Channel, ReturnPath: payload.ReturnPath}

	switch payload.Method {
	case "", store.PaymentProviderBalance:
		payOrdersByBalance(w, r, sess, batchID)
	default:
		payOrdersOnline(w, r, sess, batchID, payload.Method, opts)
	}
}

// payOrdersOnline 为批次发起在线支付。
//
// 【不】在这里送印：真正的送印发生在渠道到账之后，由 settlePayment →
// launchPrintForOrders 触发。这里只做「挂支付单 + 拿渠道凭据」。
func payOrdersOnline(w http.ResponseWriter, r *http.Request, sess auth.Session, batchID, method string, opts onlinePayOptions) {
	ctx := r.Context()

	// 归属校验：不能拿别人的 batchId 发起支付。
	var owns bool
	if err := appStore.WithTx(ctx, true, func(tx *sql.Tx) error {
		var err error
		owns, err = store.OwnsOrderBatch(ctx, tx, batchID, sess.UserID)
		return err
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load order batch")
		return
	}
	if !owns {
		writeJSONError(w, http.StatusNotFound, "order batch not found")
		return
	}

	resp, err := startOnlinePayment(ctx, r, sess.UserID, batchID, method, opts)
	if err != nil {
		writePaymentStartError(w, err)
		return
	}
	writeJSON(w, resp)
}

func payOrdersByBalance(w http.ResponseWriter, r *http.Request, sess auth.Session, batchID string) {
	ctx := r.Context()

	var paidOrderIDs []int64
	var balanceAfter int64

	err := appStore.WithTx(ctx, false, func(tx *sql.Tx) error {
		owns, err := store.OwnsOrderBatch(ctx, tx, batchID, sess.UserID)
		if err != nil {
			return err
		}
		if !owns {
			return sql.ErrNoRows
		}

		// 加行锁：下面要按这个合计值扣款，中途不能被别人改动这批订单
		// （否则会按取消前的总额扣钱）。这条路径在写事务里，加锁合法。
		total, err := store.SumPendingAmountByBatchLocked(ctx, tx, batchID)
		if err != nil {
			return err
		}
		if total <= 0 {
			return errNoPendingOrders
		}

		// 扣款与「标记订单已支付」必须在同一个事务里。
		// ApplyWalletDelta 的出账用条件 UPDATE 完成「够不够 + 扣多少」，
		// 不会双花；幂等键用 batchID，重复提交只会扣一次。
		balanceAfter, err = store.ApplyWalletDelta(ctx, tx, store.WalletDeltaInput{
			UserID:         sess.UserID,
			Kind:           store.WalletKindSpend,
			AmountCents:    -total,
			IdempotencyKey: store.WalletKeySpend(batchID),
			Note:           "打印订单支付",
		})
		if err != nil {
			return err
		}

		payment := store.Payment{
			UserID:      sess.UserID,
			Purpose:     store.PaymentPurposePrint,
			Provider:    store.PaymentProviderBalance,
			OutTradeNo:  "BAL" + batchID,
			AmountCents: total,
			Status:      store.PaymentStatusPaid,
			PaidAt:      time.Now().UTC().Format(time.RFC3339),
		}
		paymentID, err := store.InsertPayment(ctx, tx, &payment)
		if err != nil {
			return err
		}

		paidOrderIDs, err = store.MarkPendingOrdersPaid(ctx, tx, batchID,
			store.PaymentProviderBalance, paymentID)
		return err
	})

	if err != nil {
		switch {
		case errors.Is(err, store.ErrInsufficientBalance):
			writeJSONError(w, http.StatusUnprocessableEntity, "账户余额不足，请先充值")
		case errors.Is(err, store.ErrDuplicateTransaction):
			// 扣款幂等键已存在 = 这批已经付过了。读出实际状态返回，
			// 而不是报错 —— 用户重复点了按钮而已。
			writeJSONError(w, http.StatusConflict, "该订单已支付，请刷新查看")
		case errors.Is(err, errNoPendingOrders):
			writeJSONError(w, http.StatusConflict, "没有待支付的订单，请刷新查看")
		case errors.Is(err, sql.ErrNoRows):
			writeJSONError(w, http.StatusNotFound, "order batch not found")
		default:
			// 原始错因只进日志。给用户（这里是掏出手机准备付钱的人）的话
			// 必须是中文、可执行、可转述的 —— 早先这里直接把内部错误暴露成
			// "failed to pay"，用户既看不懂，也没法向任何人描述发生了什么。
			// 那次 PG 的「FOR UPDATE不能用于聚合函数中」就是这样被读成
			// 「余额支付功能没做」的，实际只是一个 SQL 写错了。
			//
			// 「尚未扣款」是事实而非安慰：事务已整条回滚，余额与账本都没动。
			log.Printf("[order] 余额支付失败 batch=%s: %v", batchID, err)
			writeJSONError(w, http.StatusInternalServerError, "支付失败，订单尚未扣款，请稍后重试")
		}
		return
	}

	writeJSON(w, map[string]interface{}{
		"ok":           true,
		"batchId":      batchID,
		"paidOrders":   len(paidOrderIDs),
		"balanceCents": balanceAfter,
	})

	// 事务提交【之后】才逐个送印。IPP 是网络调用，绝不能放进事务里。
	for _, id := range paidOrderIDs {
		go func(orderID int64) {
			if _, err := startPrintForOrder(context.Background(), orderID); err != nil {
				log.Printf("[order] #%d 送印出错: %v", orderID, err)
			}
		}(id)
	}
}

// errNoPendingOrders 表示批次里没有待支付的订单（可能已支付或已过期）。
var errNoPendingOrders = errors.New("order: no pending orders in batch")
