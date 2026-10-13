package main

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"cups-web/internal/auth"
	"cups-web/internal/payment"
	"cups-web/internal/store"

	"github.com/gorilla/mux"
)

// errPaymentAmountMismatch 回调金额与应付金额不符。
// 单独成一个哨兵是因为这是安全事件，不是普通业务失败。
var errPaymentAmountMismatch = errors.New("payment: amount mismatch")

// paymentCallbackBodyLimit 限制回调请求体大小。
// 回调是【公开】接口，任何人都能打 —— 不设上限等于给了一个免费的
// 内存放大入口。
const paymentCallbackBodyLimit = 1 << 20 // 1 MiB

const (
	// paymentQRTTL 是支付二维码的有效期。
	//
	// 到期后前端会自动再发起一次支付换一张新二维码（见 createPayment 的注释），
	// 所以这个值只影响「用户有多久去扫码」，不影响订单本身的有效期
	// —— 后者由 order_ttl_minutes 决定。
	//
	// 取 3 分钟：够用户掏出手机、打开沙箱版支付宝扫一下；
	// 再长则二维码在屏幕上挂太久，用户可能已经走开了。
	paymentQRTTL = 3 * time.Minute

	// paymentChannelTimeout 是对渠道的【出站】调用超时（下单/关单）。
	//
	// 🚫 这类调用绝不挂 HTTP 请求的 ctx：浏览器侧一次中止（重复点击、
	// 页面切换、HMR 重载）就会当场掐断对渠道的调用，表现为
	// 「第一次点报错、第二次成功」。与 IPP 提交、查单同一条约定。
	paymentChannelTimeout = 20 * time.Second
)

// settlePayment 是「钱已到账」的【唯一】入口。
//
// mock 支付、真实渠道回调、查单轮询三条路都汇到这里 —— 渠道差异在抵达
// 之前就已被消除，所以账本与订单状态的维护逻辑只有这一份。
//
// 返回的 orderIDsToPrint 非空时，调用方【必须在事务提交之后】逐个
// go startPrintForOrder：IPP 是网络调用，绝不能放进事务里。
func settlePayment(ctx context.Context, res payment.Result, provider string, raw string) ([]int64, error) {
	// ── 第一阶段（只读）：定位支付单并校验金额 ──
	var paymentID int64
	var expectedCents int64

	err := appStore.WithTx(ctx, true, func(tx *sql.Tx) error {
		// 回调必须以【支付单】为锚，不是订单：回调只带 out_trade_no，
		// 要先靠它查出这笔钱是充值还是打印、应付多少。
		// 查不到就拒绝 —— 那是伪造的订单号，绝不能凭空建单入账。
		p, err := store.GetPaymentByOutTradeNo(ctx, tx, res.OutTradeNo)
		if err != nil {
			return err
		}
		paymentID = p.ID
		expectedCents = p.AmountCents
		return nil
	})
	if err != nil {
		return nil, err
	}

	// 金额不符：不入账、不打印，只留证 + 告警。
	// 这是防篡改的最后一道 —— 回调内容对攻击者是可构造的（mock 渠道尤其如此），
	// 只有 payments 表里我们【自己写下】的金额才算数。
	if res.AmountCents != expectedCents {
		log.Printf("[payment] ⚠️ 金额不匹配 outTradeNo=%s 回调=%d 应付=%d 渠道=%s",
			res.OutTradeNo, res.AmountCents, expectedCents, provider)
		// 用独立事务写证据：主事务会被回滚，写在里面等于没写。
		_ = appStore.WithTx(ctx, false, func(tx *sql.Tx) error {
			return store.SetPaymentRawCallback(ctx, tx, paymentID, raw)
		})
		return nil, errPaymentAmountMismatch
	}

	// ── 第二阶段（写）：结算 ──
	var orderIDsToPrint []int64
	err = appStore.WithTx(ctx, false, func(tx *sql.Tx) error {
		// 幂等第一层：渠道重复投递回调是常态（支付宝 25 小时内最多重试 8 次）。
		// 已经不是 created 状态就直接返回，不再动账本与订单。
		migrated, err := store.MarkPaymentPaid(ctx, tx, paymentID, res.TransactionID)
		if err != nil {
			return err
		}
		if !migrated {
			log.Printf("[payment] outTradeNo=%s 重复回调，已忽略", res.OutTradeNo)
			return nil
		}

		p, err := store.GetPaymentByID(ctx, tx, paymentID)
		if err != nil {
			return err
		}

		switch p.Purpose {
		case store.PaymentPurposeTopup:
			_, err := store.ApplyWalletDelta(ctx, tx, store.WalletDeltaInput{
				UserID:         p.UserID,
				Kind:           store.WalletKindTopup,
				AmountCents:    p.AmountCents,
				IdempotencyKey: store.WalletKeyPay(p.OutTradeNo),
				PaymentID:      p.ID,
				Note:           "在线充值",
			})
			return err

		case store.PaymentPurposePrint:
			ids, err := store.MarkOrdersPaidByPayment(ctx, tx, p.ID, provider)
			if err != nil {
				return err
			}
			orderIDsToPrint = ids
			return nil
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	log.Printf("[payment] %s 到账 outTradeNo=%s 金额=%d 分 订单=%v",
		provider, res.OutTradeNo, res.AmountCents, orderIDsToPrint)
	return orderIDsToPrint, nil
}

// launchPrintForOrders 在事务提交后逐个送印。
//
// 放在 goroutine 里是因为：回调要求渠道侧 5 秒内收到应答，而送印要上传
// 整个文档；轮询/同步接口那边用户也在等。进程若在 goroutine 跑完前重启，
// 订单会停在 paid —— 那正是 P5 的补偿扫描要兜的场景。
func launchPrintForOrders(orderIDs []int64) {
	for _, id := range orderIDs {
		go func(orderID int64) {
			if _, err := startPrintForOrder(context.Background(), orderID); err != nil {
				log.Printf("[payment] 订单 #%d 送印出错: %v", orderID, err)
			}
		}(id)
	}
}

// ─── 渠道异步通知 ─────────────────────────────────────────────────────

// paymentCallbackHandler 处理渠道的异步通知。
//
// ⚠️ 它挂在【公开】路由上：无会话、无 CSRF。验签是唯一的身份证明，
// 所以绝不能因为「看起来像自家回调」就跳过 VerifyCallback。
func paymentCallbackHandler(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["provider"]
	provider, err := buildProvider(name)
	if err != nil {
		http.Error(w, "unknown provider", http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, paymentCallbackBodyLimit))
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}

	res, err := provider.VerifyCallback(r, body)
	if err != nil {
		log.Printf("[payment] %s 回调验签失败: %v", name, err)
		http.Error(w, "bad signature", http.StatusBadRequest)
		return
	}

	// ⚠️ 验签通过 ≠ 已收款。渠道在「交易关闭」「等待付款」这些状态下也会投递
	// 通知（支付宝的 TRADE_CLOSED 就是一条正常通知，签名完全合法）。
	// settlePayment 只认「钱到了」这件事，它【不看】res.Paid —— 所以这道判断
	// 必须在调用它之前。少了它，一条交易关闭的通知会把订单标成已支付并发货。
	//
	// 仍然回成功应答：这不是投递错误，让渠道别再重试。
	if !res.Paid {
		log.Printf("[payment] %s 回调非成功状态 outTradeNo=%s，仅应答不结算", name, res.OutTradeNo)
		status, ack := provider.CallbackAck()
		w.WriteHeader(status)
		_, _ = w.Write(ack)
		return
	}

	orderIDs, err := settlePayment(r.Context(), res, name, string(body))
	if err != nil {
		// ⚠️ 结算失败【必须】回非 2xx，让渠道继续重试。
		// 回 200 等于告诉渠道「收到了」，这笔钱就永久掉单了。
		log.Printf("[payment] %s 结算失败 outTradeNo=%s: %v", name, res.OutTradeNo, err)
		http.Error(w, "settle failed", http.StatusInternalServerError)
		return
	}

	status, ack := provider.CallbackAck()
	w.WriteHeader(status)
	_, _ = w.Write(ack)

	launchPrintForOrders(orderIDs)
}

// ─── 主动查单（"我已支付"） ───────────────────────────────────────────

// paymentSyncHandler 让用户手动触发一次查单。
//
// 内网部署下渠道回调到不了本机，用户付完钱界面却停在「待支付」，
// 这个按钮就是他们的出口。它做的是与回调完全相同的结算。
func paymentSyncHandler(w http.ResponseWriter, r *http.Request) {
	sess, err := auth.GetSession(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	outTradeNo := mux.Vars(r)["outTradeNo"]

	var providerName, purpose string
	err = appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
		p, err := store.GetPaymentByOutTradeNo(r.Context(), tx, outTradeNo)
		if err != nil {
			return err
		}
		// 只能查自己的单，避免拿别人的订单号探测支付状态。
		if p.UserID != sess.UserID {
			return sql.ErrNoRows
		}
		providerName = p.Provider
		purpose = p.Purpose
		return nil
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "payment not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to load payment")
		return
	}

	provider, err := buildProvider(providerName)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "该支付渠道不可用")
		return
	}

	res, err := provider.Query(r.Context(), outTradeNo)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "查询支付状态失败，请稍后重试")
		return
	}
	if !res.Paid {
		writeJSON(w, map[string]interface{}{
			"paid": false, "purpose": purpose,
			"message": "尚未收到支付结果，请稍后再试",
		})
		return
	}

	orderIDs, err := settlePayment(r.Context(), res, providerName, res.Raw)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "结算失败，请联系管理员")
		return
	}
	// 把 purpose 一并带出去：手机网站支付付完，支付宝会把浏览器送回
	// return_url，前端是【重新加载】的，内存里什么都不知道了，只能靠这个
	// 判断该说「充值成功」还是「正在打印」。
	resp := map[string]interface{}{"paid": true, "purpose": purpose}

	// 再带上批次号，给前端当锚点。
	//
	// 付完款回跳后要展示「刚才那一笔打的是什么」，而页面已经重新加载过、
	// 内存里没有批次号了。没有它就只能去猜「最近一条订单 / 最近一条打印
	// 记录」—— 多设备或短时间内打印多次时必然猜错，用户会看到别人的单子。
	if len(orderIDs) > 0 {
		_ = appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
			o, err := store.GetOrderByID(r.Context(), tx, orderIDs[0])
			if err != nil {
				return err
			}
			resp["batchId"] = o.BatchID
			return nil
		})
	}
	writeJSON(w, resp)
	launchPrintForOrders(orderIDs)
}

// ─── mock 渠道的显式支付 ──────────────────────────────────────────────

// mockPayHandler 模拟「渠道通知我们已收款」。
//
// 它与真实回调走的是【完全相同】的 settlePayment —— 本地因此能验证到
// 账本、订单状态、退款这些真正容易写错的部分，只有「跟渠道通信」是假的。
func mockPayHandler(w http.ResponseWriter, r *http.Request) {
	if !paymentMockEnabled() {
		writeJSONError(w, http.StatusNotFound, "mock payment disabled")
		return
	}
	sess, err := auth.GetSession(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	outTradeNo := mux.Vars(r)["outTradeNo"]

	var amountCents int64
	err = appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
		p, err := store.GetPaymentByOutTradeNo(r.Context(), tx, outTradeNo)
		if err != nil {
			return err
		}
		if p.UserID != sess.UserID {
			return sql.ErrNoRows
		}
		amountCents = p.AmountCents
		return nil
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "payment not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to load payment")
		return
	}

	// 金额从库里取，【绝不】用客户端传来的 —— 否则「我付 1 分钱买 100 页」
	// 这条路径就成立了。settlePayment 内部还会再校验一次，两道防线。
	res := payment.Result{
		OutTradeNo:    outTradeNo,
		TransactionID: "MOCK" + randomToken()[:8],
		AmountCents:   amountCents,
		Paid:          true,
		Raw:           "mock-explicit",
	}

	orderIDs, err := settlePayment(r.Context(), res, store.PaymentProviderMock, res.Raw)
	if err != nil {
		log.Printf("[payment] mock 结算失败 outTradeNo=%s: %v", outTradeNo, err)
		writeJSONError(w, http.StatusInternalServerError, "结算失败")
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "paid": true})
	launchPrintForOrders(orderIDs)
}

// ─── 发起在线支付 ─────────────────────────────────────────────────────

// onlinePayOptions 是发起在线支付时前端带上的「用户当下拿的是什么设备、在哪一页」。
//
// 这两件事服务端都判断不了：同一个用户换个设备答案就变了，不能靠 UA 猜；
// 而「付完回哪一页」取决于他当时在打印页还是钱包页。
type onlinePayOptions struct {
	// Channel 见 payment.ChannelWap / payment.ChannelQRCode，空值按二维码处理。
	Channel string
	// ReturnPath 付完回到前端的哪一页（如 "/wallet"）。会被夹成本站内的绝对路径。
	ReturnPath string
}

// startOnlinePayment 为订单批次发起在线支付。
//
// 调用方已确认批次属于该用户。这里只负责「把订单挂到一个支付单上并拿到凭据」，
// 【不】送印：真正的送印发生在渠道到账之后，由 settlePayment 触发。
func startOnlinePayment(ctx context.Context, r *http.Request, userID int64, batchID, method string, opts onlinePayOptions) (map[string]interface{}, error) {
	var total int64
	err := appStore.WithTx(ctx, true, func(tx *sql.Tx) error {
		var err error
		total, err = store.SumPendingAmountByBatch(ctx, tx, batchID)
		return err
	})
	if err != nil {
		return nil, err
	}
	if total <= 0 {
		return nil, errNoPendingOrders
	}

	return createPayment(ctx, paymentCreateInput{
		Purpose:     store.PaymentPurposePrint,
		UserID:      userID,
		AmountCents: total,
		Method:      method,
		Subject:     "打印费用",
		// 订单先挂到支付单上：回调只带 out_trade_no，靠这个外键才知道
		// 该把哪些订单推进到 paid。
		attachBatchID: batchID,
		Channel:       opts.Channel,
		ReturnURL:     paymentReturnURL(ctx, r, opts.ReturnPath),
	})
}

// startTopupPayment 发起一笔余额充值。
func startTopupPayment(ctx context.Context, r *http.Request, userID, amountCents int64, method string, opts onlinePayOptions) (map[string]interface{}, error) {
	return createPayment(ctx, paymentCreateInput{
		Purpose:     store.PaymentPurposeTopup,
		UserID:      userID,
		AmountCents: amountCents,
		Method:      method,
		Subject:     "余额充值",
		Channel:     opts.Channel,
		ReturnURL:   paymentReturnURL(ctx, r, opts.ReturnPath),
	})
}

type paymentCreateInput struct {
	Purpose       string
	UserID        int64
	AmountCents   int64
	Method        string
	Subject       string
	attachBatchID string // 非空时把该批次的订单挂到新支付单上
	Channel       string // payment.ChannelWap / ChannelQRCode
	ReturnURL     string // 已拼好的绝对回跳地址；空 = 拿不到，走二维码
}

// closePreviousBatchPayment 关闭该批次上仍挂着的、未支付的在线支付单。
//
// 二维码到期自动刷新时会重新建单，旧单必须先关掉：用户若扫了【旧】二维码
// 付款，钱会到账，但那笔支付单下面已经没有订单了（订单被重新挂到新单上）——
// 结果是「付了钱什么都没打出来」，而钱在渠道侧，我们无法自动原路退回。
//
// ⚠️ 关单失败【不阻断】刷新：用户此刻要的是一个能扫的新二维码，
// 让他卡在一个关不掉的旧单上体验更差。失败只记日志。
func closePreviousBatchPayment(ctx context.Context, batchID string) {
	var pending store.UnsettledPayment
	found := false
	err := appStore.WithTx(ctx, true, func(tx *sql.Tx) error {
		p, err := store.GetUnsettledPaymentByBatch(ctx, tx, batchID)
		if err != nil {
			return nil // 没有未结算的支付单是常态，不是错误
		}
		pending, found = p, true
		return nil
	})
	if err != nil || !found {
		return
	}

	if provider, perr := buildProvider(pending.Provider); perr == nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), paymentChannelTimeout)
		if cerr := provider.Close(closeCtx, pending.OutTradeNo); cerr != nil {
			log.Printf("[payment] 关闭旧支付单失败 outTradeNo=%s: %v", pending.OutTradeNo, cerr)
		}
		cancel()
	}

	if merr := appStore.WithTx(ctx, false, func(tx *sql.Tx) error {
		return store.MarkPaymentClosed(ctx, tx, pending.ID)
	}); merr != nil {
		log.Printf("[payment] 标记旧支付单已关闭失败 id=%d: %v", pending.ID, merr)
	}
}

// createPayment 是「建支付单 → 向渠道发起 → 回写凭据」的共用实现。
// 订单支付与余额充值除了 purpose 和是否挂订单之外完全一样，不该写两遍。
func createPayment(ctx context.Context, in paymentCreateInput) (map[string]interface{}, error) {
	provider, err := buildProvider(in.Method)
	if err != nil {
		return nil, err
	}
	if in.AmountCents <= 0 {
		return nil, errNoPendingOrders
	}

	// 二维码到期后前端会自动再调一次这个接口 —— 等价于「刷新二维码」。
	// 旧单必须先关掉，否则会留下一个「能付款但没有订单挂在下面」的坑。
	if in.attachBatchID != "" {
		closePreviousBatchPayment(ctx, in.attachBatchID)
	}

	// 设备形态决定收款方式，【先定下来】—— 下面算有效期要用它。
	//
	// ⚠️ 手机网站支付必须有 return_url：没有它，用户付完会停在支付宝自己的
	// 结果页上，还得自己找回我们的页面 —— 那正是改用官方方案要解决的问题。
	// 拿不到就降级成二维码，而不是发一个用户回不来的链接出去。
	channel := in.Channel
	if channel == "" {
		channel = payment.ChannelQRCode
	}
	// ⚠️ 下面这个降级判断只认支付宝的产品词汇，且【不知道渠道的存在】。
	// 新增渠道前先确认它是否适用：比如某种收款方式根本没有「回跳」这个概念
	// （页面内二维码就是），那这条判断套上去就是错的。同理见 credTTLFor。
	if channel == payment.ChannelWap && in.ReturnURL == "" {
		log.Printf("[payment] 拿不到回跳地址，手机网站支付降级为二维码")
		channel = payment.ChannelQRCode
	}

	credTTL := credTTLFor(channel, orderTTL(ctx))

	// 前缀区分用途，对账时肉眼就能分类。
	prefix := "CW"
	if in.Purpose == store.PaymentPurposeTopup {
		prefix = "TP"
	}
	outTradeNo := newOutTradeNo(prefix)
	expiresAt := time.Now().UTC().Add(credTTL).Format(time.RFC3339)

	var paymentID int64
	err = appStore.WithTx(ctx, false, func(tx *sql.Tx) error {
		p := store.Payment{
			UserID:      in.UserID,
			Purpose:     in.Purpose,
			Provider:    in.Method,
			OutTradeNo:  outTradeNo,
			AmountCents: in.AmountCents,
			Status:      store.PaymentStatusCreated,
		}
		id, err := store.InsertPayment(ctx, tx, &p)
		if err != nil {
			return err
		}
		paymentID = id
		if in.attachBatchID != "" {
			return store.AttachOrdersToPayment(ctx, tx, in.attachBatchID, id)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// ⚠️ 渠道调用用【独立 context】，绝不挂 HTTP 请求的 ctx。
	//
	// 这是实测踩出来的：挂在请求 ctx 上时，浏览器侧任何一次中止
	// （重复点击触发的 fetch 取消、页面切换、HMR 重载）都会当场掐断
	// 对渠道的调用，用户看到的就是「第一次点报错、第二次成功」。
	// 与 IPP 提交、查单同一条约定：出站调用自己管超时。
	channelCtx, cancel := context.WithTimeout(context.Background(), paymentChannelTimeout)
	defer cancel()

	// channel 在函数上半段就定好了（算有效期要用它），这里直接用。
	// 前端不必特殊处理降级：看返回里有没有 qrCodeText 就知道走的是哪条路。
	cred, err := provider.Create(channelCtx, payment.Request{
		OutTradeNo:  outTradeNo,
		AmountCents: in.AmountCents,
		Subject:     in.Subject,
		UserID:      in.UserID,
		NotifyURL:   paymentNotifyURL(ctx, in.Method),
		ReturnURL:   in.ReturnURL,
		ExpiresAt:   time.Now().UTC().Add(credTTL),
		Channel:     channel,
	})
	if err != nil {
		// 渠道发起失败：把支付单置为失败，否则它会永远挂在「等你付款」状态，
		// 而用户已经看到报错了。
		_ = appStore.WithTx(ctx, false, func(tx *sql.Tx) error {
			return store.MarkPaymentFailed(ctx, tx, paymentID, err.Error())
		})
		return nil, err
	}

	_ = appStore.WithTx(ctx, false, func(tx *sql.Tx) error {
		return store.SetPaymentPrepay(ctx, tx, paymentID, cred.Raw)
	})

	return map[string]interface{}{
		"paymentId":   paymentID,
		"provider":    in.Method,
		"outTradeNo":  outTradeNo,
		"amountCents": in.AmountCents,
		"qrCodeText":  cred.QRCodeText,
		"payUrl":      cred.PayURL,
		"expiresAt":   expiresAt,
		// mock 专用：前端据此显示「模拟支付成功」按钮。
		// 真实渠道下为 false，前端不渲染那个按钮。
		"mock": in.Method == store.PaymentProviderMock,
	}, nil
}

// credTTLFor 算一笔支付凭据的有效期。这个值最终会变成发给支付宝的
// timeout_express（见 payment.Request.ExpiresAt），直接决定用户在收银台上
// 有多少时间把钱付掉。
//
// ⚠️ 必须【按渠道分开算】，不能共用一个值 —— 两条路的"用户在哪里"完全不同：
//
//	二维码：用户【还在我们页面上】，盯着这个码。到期由前端自动换一张，
//	        所以短一点（3 分钟）反而好：避免他对着一个失效的码反复扫。
//	收银台：用户【已经离开我们的页面】去了支付宝。这条路没有"自动刷新"
//	        可言；而 3 分钟对收银台太短 —— 首次付款要登录、可能要输密码，
//	        很容易超。超了这笔交易就被渠道关闭，收银台报
//	        「操作已超时，请重新发起」。实测踩过。
func credTTLFor(channel string, orderTTLValue time.Duration) time.Duration {
	// 两条【收银台】路（手机网站支付 / 电脑网站支付）都跟订单走。
	// 注意也不能活得比订单长：否则会出现「还能付，但订单已经过期回收了」的错位。
	//
	// ⚠️ 这里同样只认支付宝的 channel 词汇，且不知道调用方是哪个渠道。
	// 新增渠道前先确认：这个渠道的收款方式里，哪些属于「用户已经离开我们的
	// 页面」（要跟订单走），哪些属于「码在我们页面上、能自动刷新」（3 分钟）。
	// 分错的后果实测过 —— 3 分钟对收银台太短，渠道会报「操作已超时，请重新发起」。
	// 同理见上面 createPayment 里那处 wap 降级。
	if channel == payment.ChannelWap || channel == payment.ChannelPage {
		return orderTTLValue
	}
	if orderTTLValue < paymentQRTTL {
		return orderTTLValue
	}
	return paymentQRTTL
}

// paymentNotifyURL 拼渠道的异步通知地址。
// 未配置 site_base_url（内网部署的常态）时返回空串 —— 渠道不会回调，
// 结算就靠用户点「我已支付」触发查单。
func paymentNotifyURL(ctx context.Context, method string) string {
	var base string
	_ = appStore.WithTx(ctx, true, func(tx *sql.Tx) error {
		v, err := store.GetSettingString(ctx, tx, store.SettingSiteBaseURL, "")
		if err != nil {
			return err
		}
		base = v
		return nil
	})
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/api/payments/callback/" + method
}

// defaultReturnPath 是回跳路径缺失或不可用时的落脚点。
//
// 选钱包页：付完款的用户最需要看到的就是「这笔成了没有、订单什么状态」，
// 而这两样都在钱包页上（余额 + 打印订单列表）。
const defaultReturnPath = "/wallet"

// paymentReturnURL 拼手机网站支付的前台回跳地址。
//
// 用户付完款，支付宝会把浏览器【送回这个地址】，我们就在那儿把结果补回来。
// 这一步解决的是手机场景最烦人的一环：付完不必自己去任务切换器里翻
// 我们的页面在哪个标签，支付宝负责送他回来。
//
// 地址优先取 settings 的 site_base_url；没配就从本次请求推。
//
// ⚠️ 内网地址完全可用 —— 回跳是【手机浏览器】去访问的，支付宝的服务器
// 不需要能连到它。所以 http://192.168.1.22:1180/#/wallet 这种地址没有任何
// 问题，既不用公网域名也不用备案。（真正需要公网可达的只有 notify_url，
// 而本项目不靠回调结算，那个一直是空的。）
//
// 拿不到地址时返回空串，调用方据此降级成二维码方案。
func paymentReturnURL(ctx context.Context, r *http.Request, returnPath string) string {
	var base string
	_ = appStore.WithTx(ctx, true, func(tx *sql.Tx) error {
		v, err := store.GetSettingString(ctx, tx, store.SettingSiteBaseURL, "")
		if err != nil {
			return err
		}
		base = v
		return nil
	})
	if base == "" {
		base = requestOrigin(r)
	}
	if base == "" {
		return ""
	}
	// 拼成 <origin>/#<path>，于是支付宝附加的查询参数会落在 hash 里面：
	//   http://host/#/wallet?out_trade_no=CW...&trade_no=...
	// 前端是 hash 路由，解析出来正好是「/wallet 页 + query.out_trade_no」。
	return strings.TrimRight(base, "/") + "/#" + sanitizeReturnPath(returnPath)
}

// requestOrigin 从本次请求推断对外的站点根地址。
//
// X-Forwarded-* 的处理与项目其它地方一致：反代（以及前端的 vite dev proxy）
// 会把【对外】的主机名放在 X-Forwarded-Host 里，而 r.Host 是上游地址。
// 协议同理 —— 直连看 r.TLS，TLS 卸载在反代上时看 X-Forwarded-Proto
// （与 auth.CookieSecure 同一套判据）。
func requestOrigin(r *http.Request) string {
	if r == nil {
		return ""
	}
	host := firstForwardedValue(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = r.Host
	}
	if host == "" {
		return ""
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := firstForwardedValue(r.Header.Get("X-Forwarded-Proto")); p == "http" || p == "https" {
		scheme = p
	}
	return scheme + "://" + host
}

// firstForwardedValue 取 X-Forwarded-* 的第一跳。多级代理下它们是逗号分隔的，
// 最左边那跳才是客户端直连的那一层。
func firstForwardedValue(v string) string {
	if v == "" {
		return ""
	}
	return strings.TrimSpace(strings.Split(v, ",")[0])
}

// sanitizeReturnPath 把前端传来的回跳路径夹成「本站内的绝对路径」。
//
// 🚫 不校验的话，一个 returnPath="//evil.com" 就能把刚付完款的用户送到
// 别人的站上去（开放重定向），而那个页面顶着的正是「支付成功」的语境 ——
// 钓鱼的完美入口。只接受单个前导 "/" 的普通路径，其余一律退回默认页。
func sanitizeReturnPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
		return defaultReturnPath
	}
	// 部分浏览器把反斜杠当 / 处理，"\/evil.com" 同样能跳出去。
	if strings.Contains(p, "\\") {
		return defaultReturnPath
	}
	// ? 与 # 会让拼出来的地址坏掉：渠道还会再往后面接一个 ?out_trade_no=...，
	// 多一个 ? 就变成 "…?x=1?out_trade_no=Y"，单号被吞进上一个参数的值里，
	// 回跳处理再也读不到它。前端传的是 route.path（不含查询串），
	// 这条只是拦住将来有人误传 route.fullPath。
	if strings.ContainsAny(p, "?#") {
		return defaultReturnPath
	}
	return p
}

// writePaymentStartError 把发起支付时的错误映射成合适的 HTTP 响应。
func writePaymentStartError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, payment.ErrUnavailable):
		writeJSONError(w, http.StatusBadRequest, "该支付方式当前不可用")
	case errors.Is(err, errNoPendingOrders):
		writeJSONError(w, http.StatusConflict, "没有待支付的订单，请刷新查看")
	case errors.Is(err, sql.ErrNoRows):
		writeJSONError(w, http.StatusNotFound, "order batch not found")
	default:
		log.Printf("[payment] 发起支付失败: %v", err)
		// ⚠️ 这里曾经一律回「failed to start payment」，把渠道的原话吞进日志 ——
		// 结果是一次真实的失败在浏览器里只留下一句无信息量的英文，
		// 排查时既不知道是网络、签名还是参数问题。
		//
		// 现在把渠道侧的原文带出去。这些报文来自支付宝/微信，本身不含
		// 密钥或用户隐私（只有错误码与说明），但会含我们的 out_trade_no
		// 与金额 —— 那本来就是用户自己这笔单的信息，给回他自己没有问题。
		writeJSONError(w, http.StatusBadGateway, "发起支付失败："+err.Error())
	}
}
