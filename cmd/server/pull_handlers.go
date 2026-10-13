package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"strconv"
	"time"

	"cups-web/internal/auth"
	"cups-web/internal/store"

	"github.com/gorilla/mux"
)

// ─── 刷卡取件：用户侧的提交 / 查看 / 撤销 ──────────────────────────────
//
// 这条通道与计费是【互斥】的两个模式，共用同一套订单机器：
//
//	计费：pending_payment →（付款）→ paid →（ClaimOrderForPrint）→ printing
//	取件：held ─────────────（刷卡）→ printing
//
// 转换、参数快照、送印、结果核对全部复用 —— 真正不同的只有「什么时候
// 离开待处理状态」，以及「失败时退不退款」（取件没有钱可退）。
//
// 见 docs/pull-printing.md。

// pullTTL 读待取件任务的保留时长。
//
// 与 orderTTL（待支付有效期，默认 15 分钟）是两回事：那个决定用户能在支付页
// 停留多久，这个决定员工有多久来取纸。默认 72 小时 —— 周五下班前提交、
// 周一早上来取也该还在。
//
// 过期后文件被回收，任务作废（见 order_worker.go 的 expireHeldOrders）。
func pullTTL(ctx context.Context) time.Duration {
	return time.Duration(pullTTLHours(ctx)) * time.Hour
}

// pullTTLHours 是同一个设置的小时数形式，也供 /api/public-settings 下发给
// 前端拼「请在 N 小时内取件」的提示。
//
// 读库失败时用默认值而不是 0：0 会让 expires_at 落在过去，
// 于是刚提交的任务下一次扫描就被判过期删掉 —— 一个读库抖动就能
// 无声地吃掉员工的任务。这里宁可放长不可放短。
func pullTTLHours(ctx context.Context) int64 {
	hours := int64(72)
	_ = appStore.WithTx(ctx, true, func(tx *sql.Tx) error {
		v, err := store.GetSettingInt(ctx, tx, store.SettingPullTTLHours, 72)
		if err != nil {
			return err
		}
		hours = v
		return nil
	})
	if hours < 1 {
		hours = 72
	}
	return hours
}

// printerHasKiosk 判断这台打印机有没有【启用】的取件终端。
//
// 取件模式下提交页只列有终端的打印机，但那是界面层的过滤，挡不住 curl。
// 少了这道后端闸门，可以提交一条「永远取不出来」的任务：它躺在 held 里
// 直到 TTL 过期，员工在打印机旁边刷多少次卡都没反应 —— 而且没有任何报错。
func printerHasKiosk(ctx context.Context, printerURI string) (bool, error) {
	var ok bool
	err := appStore.WithTx(ctx, true, func(tx *sql.Tx) error {
		set, err := store.PrinterURIsWithKiosk(ctx, tx)
		if err != nil {
			return err
		}
		ok = set[printerURI]
		return nil
	})
	return ok, err
}

// pullOrderOrigin 拼取件订单的来源标记。提交与重打两条路都用它。
//
// ⚠️ 取件模式下 source 一律写 'pull'，【即便这是一次重打】。
// 因为 source 是「这条订单失败时要不要退款」的唯一判据
// （order_worker.go 里那句 o.Source == store.OrderSourcePull）。
// 让重打来的取件单带着 source='reprint'，它在核对阶段就会被当成计费订单，
// 走到退款路径上去 —— 而取件订单金额恒为 0，那条路只会把订单标成 failed
// 并把员工的任务丢掉。
//
// 「这是一次重打」的信息保留在 reprint_of 列里，一点没丢。
func pullOrderOrigin(reprintOf int64) orderOrigin {
	return orderOrigin{Source: store.OrderSourcePull, ReprintOf: reprintOf, Hold: true}
}

// pullOrderResponse 是提交取件任务的返回体。
//
// Held 恒为 true，前端据它把「打印成功」换成「已暂存，请到打印机刷卡取件」——
// 这是本次改动里最容易漏、后果也最直接的一处：文案不改，员工会以为纸已经
// 出来了，转身就走。
type pullOrderResponse struct {
	Held      bool              `json:"held"`
	Orders    []orderResponse   `json:"orders"`
	Failed    []orderFailedItem `json:"failed"`
	BatchID   string            `json:"batchId,omitempty"`
	ExpiresAt string            `json:"expiresAt"`
}

// pullOrderCreateHandler 提交打印任务，【暂存】到员工刷卡为止。
//
// 与 orderCreateHandler 的区别：不收费、不建支付单、落 held 状态。
// 其余（转换流水线、参数快照、失败条目、打印机预检）完全一致 —— 两份代码
// 必须一起改，见 order_prepare.go 顶部关于「孪生文件」的说明。
func pullOrderCreateHandler(w http.ResponseWriter, r *http.Request) {
	if !pullModeEnabled(r.Context()) {
		writeJSONError(w, http.StatusNotFound, "取件模式未开启")
		return
	}

	sess, err := auth.GetSession(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	// 访客账号是所有人共用的，而取件模式的全部意义就是「任务认人」。
	// 共用账号下所有人的任务会混在一起，刷卡时统统打出来。
	// 管理端已经禁止计费与访客同时开启，但存量配置或直接改库都可能绕过。
	if sess.Username == guestUsername {
		writeJSONError(w, http.StatusForbidden, "访客模式不支持刷卡取件")
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
	// 与计费路径同一套理由：先拦住，员工不会白跑一趟；等到刷卡才发现，
	// 员工已经站在打印机旁边了，那是最差的体验。
	// ⚠️ 送印前（startPullPrintForOrder）还会再查一次 —— 提交到刷卡之间
	// 打印机完全可能掉线，而中间可能隔着好几个小时。
	if reason := printerUnavailableReason(form.Printer); reason != "" {
		log.Printf("[pull] 提交被拒：打印机不可用 %s（%s）", form.Printer, reason)
		writeJSONError(w, http.StatusUnprocessableEntity, "打印机当前不可用："+reason)
		return
	}

	// ── 目标打印机必须配有取件终端 ──
	// 少了这道，任务会进一个没有出口的黑洞：永远取不出来，也不报错。
	hasKiosk, err := printerHasKiosk(r.Context(), form.Printer)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to check kiosk")
		return
	}
	if !hasKiosk {
		writeJSONError(w, http.StatusUnprocessableEntity,
			"这台打印机没有取件终端，任务提交后无法取件。请换一台。")
		return
	}

	// 收集文件：单文件 file / 多文件 files（与 /api/convert、/api/orders 一致）。
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

	expires := time.Now().UTC().Add(pullTTL(r.Context())).Format(time.RFC3339)
	batchID := newBatchID()

	resp := pullOrderResponse{
		Held:      true,
		Orders:    []orderResponse{},
		Failed:    []orderFailedItem{},
		BatchID:   batchID,
		ExpiresAt: expires,
	}

	for _, item := range items {
		// rules / fallback 传空：createOneOrder 在 Hold 分支里会再强制一次，
		// 这里传空只是让「取件不收费」在这行代码上就看得见。
		order, _, matched, perr := createOneOrder(r.Context(), sess.UserID, batchID,
			pullOrderOrigin(0), form,
			item.file, item.filename, maxPages, nil, 0, expires)
		if perr != nil {
			log.Printf("[pull] 提交失败 file=%q: %v", item.filename, perr)
			resp.Failed = append(resp.Failed, orderFailedItem{
				Filename: item.filename,
				Error:    clientMessage(perr),
			})
			continue
		}
		resp.Orders = append(resp.Orders, mapOrder(order, matched))
	}

	if len(resp.Orders) == 0 {
		writeJSONError(w, http.StatusBadRequest, resp.Failed[0].Error)
		return
	}

	log.Printf("[pull] 用户 %s 提交 %d 个待取件任务（打印机 %s）", sess.Username, len(resp.Orders), form.Printer)
	writeJSON(w, resp)
}

// pullOrderListHandler 列出本人所有待取件任务。
//
// ⚠️ 以 print_orders 为准，不是查 print_jobs：后者受 save_history 开关影响
// （关掉时压根不写记录），依赖它会让员工看不到自己待取的任务 ——
// 而那恰恰是这个功能要解决的问题本身。
func pullOrderListHandler(w http.ResponseWriter, r *http.Request) {
	if !pullModeEnabled(r.Context()) {
		writeJSONError(w, http.StatusNotFound, "取件模式未开启")
		return
	}
	sess, err := auth.GetSession(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	orders := []store.PrintOrder{}
	err = appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
		var err error
		orders, err = store.ListHeldOrdersByUser(r.Context(), tx, sess.UserID)
		return err
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list pull orders")
		return
	}

	out := []orderResponse{}
	for _, o := range orders {
		// matched 传 false：取件订单没有价格，这个字段对它们无意义。
		out = append(out, mapOrder(o, false))
	}
	writeJSON(w, out)
}

// pullOrderCancelHandler 撤销一条还没取件的任务。
//
// 没有它的话，员工提交错了只能干等到 TTL 过期 —— 而 TTL 是 72 小时。
func pullOrderCancelHandler(w http.ResponseWriter, r *http.Request) {
	if !pullModeEnabled(r.Context()) {
		writeJSONError(w, http.StatusNotFound, "取件模式未开启")
		return
	}
	sess, err := auth.GetSession(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid order id")
		return
	}

	// 先把整行读出来：取消成功之后要删它的文件，那时行已经不在 held 了。
	var order store.PrintOrder
	var cancelled bool
	err = appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
		var err error
		order, err = store.GetOrderByID(r.Context(), tx, id)
		if err != nil {
			return err
		}
		// 归属校验在 SQL 里（CancelHeldOrder 的 WHERE user_id）——
		// 不能只靠上面这次读：读与改之间它可能刚被刷卡领走。
		cancelled, err = store.CancelHeldOrder(r.Context(), tx, id, sess.UserID)
		return err
	})
	if err != nil {
		if err == sql.ErrNoRows {
			writeJSONError(w, http.StatusNotFound, "任务不存在")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to cancel")
		return
	}
	if !cancelled {
		// 不是本人的、或已经不在 held（刚被刷卡领走 / 已过期）。
		// 两种情况都不该报 500 —— 如实说「已经不在待取件状态」。
		writeJSONError(w, http.StatusConflict, "这个任务已经不在待取件状态了")
		return
	}

	removeOrderFiles(order)
	log.Printf("[pull] 用户 %s 撤销了待取件任务 #%d（%s）", sess.Username, id, order.Filename)
	writeJSON(w, map[string]any{"ok": true})
}
