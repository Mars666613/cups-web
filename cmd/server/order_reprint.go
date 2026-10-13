package main

// 重打接入计费 —— 计费开启时「重新打印」不再直接送印，而是走一遍下单流程。
//
// 为什么必须转订单：/api/print-records/{id}/reprint 是历史记录页上的一个按钮，
// 直接送印的话它与订单系统毫无关系 —— 计费开着也能免费打印，且完全不受余额约束。
// 三个绕过口子里这个最容易被发现：按钮就明晃晃摆在界面上。
//
// 与普通下单的唯一差别是【文件来源】—— 不是本次上传的，而是从 print_jobs
// 记录指向的原文件复制一份出来。转换、计价、落库全部复用 createOneOrder，
// 于是重打与普通下单在金额上不可能算出两个结果。

import (
	"net/http"
	"os"
	"path/filepath"
	"time"

	"cups-web/internal/auth"
	"cups-web/internal/pricing"
	"cups-web/internal/store"
)

// reprintAsOrder 把一次重打请求转成订单，返回与普通下单【完全相同】的响应体。
//
// 复用 orderBatchResponse 而不是另造一个形状：前端的支付弹窗、轮询、取消
// 都只认这一个形状，重打因此不需要在支付侧写任何分支。
//
// 调用方（reprintHandler）必须已经完成：会话校验、记录归属校验、
// 页码范围归一化，以及 Copies / NumberUp 的兜底。
func reprintAsOrder(w http.ResponseWriter, r *http.Request, sess auth.Session, record store.PrintRecord, req reprintRequest) {
	// 重打落到哪条通道，取决于当前是哪个模式 —— 与 /api/print 的闸门同一套判断。
	pull := pullModeEnabled(r.Context())

	// 与 orderCreateHandler 同一道兜底：访客账号是共用的，它的余额是「公摊钱包」，
	// 花光了也无从追责。取件模式下更要挡：那种模式的全部意义是「任务认人」，
	// 共用账号会把所有人的任务混在一起，刷卡时统统打出来。
	if sess.Username == guestUsername {
		if pull {
			writeJSONError(w, http.StatusForbidden, "访客模式不支持刷卡取件")
		} else {
			writeJSONError(w, http.StatusForbidden, "访客模式不支持计费打印")
		}
		return
	}

	// 取件模式下重打同样要落到「配有取件终端」的打印机上，否则任务会进一个
	// 取不出来、也不报错的黑洞（与 pullOrderCreateHandler 同一道闸门）。
	if pull {
		hasKiosk, kerr := printerHasKiosk(r.Context(), req.Printer)
		if kerr != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to check kiosk")
			return
		}
		if !hasKiosk {
			writeJSONError(w, http.StatusUnprocessableEntity,
				"这台打印机没有取件终端，任务提交后无法取件。请换一台。")
			return
		}
	}

	maxPages, _ := getPrintLimits(r.Context())

	// 字段语义与 parseOrderForm 一致。重打没有「反色」这个选项
	// （与 reprintHandler 的图片分支一致，历史上就没往下传）。
	form := orderForm{
		Printer:        req.Printer,
		Invert:         false,
		IsColor:        req.Color,
		IsDuplex:       req.Duplex,
		Mirror:         req.Mirror,
		Copies:         req.Copies,
		NumberUp:       req.NumberUp,
		Orientation:    req.Orientation,
		PaperSize:      req.PaperSize,
		PaperType:      req.PaperType,
		MediaSource:    req.MediaSource,
		PrintScaling:   req.PrintScaling,
		PageRange:      req.PageRange, // 调用方已归一化，不再重复解析
		PageSet:        req.PageSet,
		Watermark:      req.WatermarkText,
		NumberUpLayout: req.NumberUpLayout,
		PageBorder:     req.PageBorder,
	}

	// 取件模式不收费：价目表压根不需要读，空价目 + 零兜底即可
	// （createOneOrder 在 Hold 分支里还会再强制一次）。
	rules, fallback := []pricing.Rule(nil), int64(0)
	if !pull {
		var err error
		rules, fallback, err = loadPricingSnapshot(r.Context())
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to load prices")
			return
		}
	}

	// 打开原文件交给下单流水线。它内部会 saveUploadedFile 复制一份新的 ——
	// 订单必须持有自己的副本，否则原记录被清理（retention_days 到期）
	// 会把一份待打印的订单文件一起带走。
	origFile, err := os.OpenInRoot(uploadDir, filepath.FromSlash(record.StoredPath))
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "original file not found, may have been cleaned up")
		return
	}
	defer origFile.Close()

	batchID := newBatchID()
	// 取件模式用的是另一个时限 —— orderTTL 管的是「多久内付款」，
	// pullTTL 管的是「员工多久来取纸」，两者语义不同，别混用。
	ttl := orderTTL(r.Context())
	if pull {
		ttl = pullTTL(r.Context())
	}
	expires := time.Now().UTC().Add(ttl).Format(time.RFC3339)

	// ⚠️ 取件模式下来源要换成 pull，不是保留 reprint —— 理由见 pullOrderOrigin。
	origin := orderOrigin{Source: store.OrderSourceReprint, ReprintOf: record.ID}
	if pull {
		origin = pullOrderOrigin(record.ID)
	}

	order, amount, matched, perr := createOneOrder(r.Context(), sess.UserID, batchID,
		origin, form, origFile, record.Filename, maxPages, rules, fallback, expires)
	if perr != nil {
		writeClientError(w, perr)
		return
	}

	resp := orderBatchResponse{
		BatchID:          batchID,
		OrderIDs:         []int64{order.ID},
		Orders:           []orderResponse{mapOrder(order, matched)},
		TotalAmountCents: amount,
		ExpiresAt:        expires,
		Failed:           []orderFailedItem{},
		Held:             pull,
	}
	if balance, berr := readUserBalance(r.Context(), sess.UserID); berr == nil {
		resp.BalanceCents = balance
	}
	writeJSON(w, resp)
}
