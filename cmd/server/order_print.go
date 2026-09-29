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
	f, err := os.OpenInRoot(uploadDir, filepath.FromSlash(order.PrintPath))
	if err != nil {
		return "failed", failOrderAndRefund(ctx, orderID, "待打印文件缺失")
	}
	defer f.Close()

	mime := order.PrintMime
	if mime == "" {
		mime = detectOrderMime(f)
	}

	opts := ipp.PrintJobOptions{
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

	job, err := ipp.SendPrintJob(order.PrinterURI, f, mime, order.Username, order.Filename, opts)
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

		total, err := store.SumPendingAmountByBatch(ctx, tx, batchID)
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
			log.Printf("[order] 余额支付失败 batch=%s: %v", batchID, err)
			writeJSONError(w, http.StatusInternalServerError, "failed to pay")
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
