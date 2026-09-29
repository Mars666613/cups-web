package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"cups-web/internal/store"
)

// adminListLimit 是管理端列表接口的默认返回条数。
// 打印店/内网的量级下够用；真要长时段对账应当加时间范围筛选。
const adminListLimit = 200

// 退款相关的哨兵错误。
var (
	errNothingToRefund = errors.New("order has nothing to refund")
	errAlreadyRefunded = errors.New("order already fully refunded")
)

// adminListOrdersHandler 列出全站订单。
func adminListOrdersHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := store.OrderFilter{
		Username: strings.TrimSpace(q.Get("username")),
		Status:   strings.TrimSpace(q.Get("status")),
		StartAt:  strings.TrimSpace(q.Get("start")),
		EndAt:    strings.TrimSpace(q.Get("end")),
		Limit:    adminListLimit,
	}

	var orders []store.PrintOrder
	err := appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
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
		// matched 只用于提示「价目未命中」，管理端列的是历史记录，
		// 当时命没命中已经记在金额里了，这里恒为 true。
		out = append(out, mapOrder(o, true))
	}
	writeJSON(w, out)
}

// adminListWalletTransactionsHandler 列出余额流水（对账用）。
func adminListWalletTransactionsHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := store.WalletFilter{
		Username: strings.TrimSpace(q.Get("username")),
		Kind:     strings.TrimSpace(q.Get("kind")),
		StartAt:  strings.TrimSpace(q.Get("start")),
		EndAt:    strings.TrimSpace(q.Get("end")),
		Limit:    adminListLimit,
	}

	var rows []store.WalletTransaction
	err := appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
		var err error
		rows, err = store.ListWalletTransactions(r.Context(), tx, filter)
		return err
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list transactions")
		return
	}
	writeJSON(w, mapWalletTransactionsWithUser(rows))
}

// adminRefundOrderHandler 手工把一笔订单全额退款到余额。
//
// 用途：打印结果有争议时（比如卡在 printing 被转人工的单子）由管理员定夺。
//
// 幂等键 refund:<order_id> 与「打印失败自动退款」【共用】—— 两者撞车时
// 只会退一次。这正是共用幂等键的意义：不管谁先退的、走了哪条路，
// 同一笔订单都不可能退两次。
func adminRefundOrderHandler(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid order id")
		return
	}

	var payload struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		// 允许空 body（前端可能不带原因）
		payload.Reason = ""
	}

	operator := sessionUsername(r)
	reason := strings.TrimSpace(payload.Reason)
	if reason == "" {
		reason = "管理员手工退款"
	}

	var refunded int64
	var userID int64
	err = appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
		ctx := r.Context()

		order, err := store.GetOrderByID(ctx, tx, id)
		if err != nil {
			return err
		}
		// 没付过钱就没什么可退的。pay_method 只在订单真正被支付时才写入，
		// 用它而不是状态判断，是因为 expired/cancelled 这类状态也可能
		// 出现在「从未付款」的订单上，能给出更准确的提示。
		if order.AmountCents <= 0 || order.PayMethod == "" {
			return errNothingToRefund
		}
		if order.RefundedCents >= order.AmountCents {
			return errAlreadyRefunded
		}

		// 状态迁移与幂等键共同保证只退一次：
		// MarkOrderRefunded 只在 recorded 状态是 printed/failed 且退款额为 0 时生效，
		// 而 wallet_transactions 的唯一键是第二道闸。
		ok, err := store.MarkOrderRefunded(ctx, tx, id, order.AmountCents)
		if err != nil {
			return err
		}
		if !ok {
			return errAlreadyRefunded
		}

		if _, err := store.ApplyWalletDelta(ctx, tx, store.WalletDeltaInput{
			UserID:         order.UserID,
			Kind:           store.WalletKindRefund,
			AmountCents:    order.AmountCents,
			IdempotencyKey: store.WalletKeyRefund(id),
			OrderID:        id,
			PaymentID:      order.PaymentID,
			Operator:       operator,
			Note:           reason,
		}); err != nil {
			if errors.Is(err, store.ErrDuplicateTransaction) {
				return errAlreadyRefunded
			}
			return err
		}

		if order.PaymentID != 0 {
			if err := store.AddPaymentRefunded(ctx, tx, order.PaymentID, order.AmountCents); err != nil {
				return err
			}
		}

		refunded = order.AmountCents
		userID = order.UserID
		return nil
	})

	if err != nil {
		switch {
		case errors.Is(err, errNothingToRefund):
			writeJSONError(w, http.StatusBadRequest, "该订单金额为 0，无需退款")
		case errors.Is(err, errAlreadyRefunded):
			writeJSONError(w, http.StatusConflict, "该订单已经全额退款过了")
		case errors.Is(err, sql.ErrNoRows):
			writeJSONError(w, http.StatusNotFound, "order not found")
		default:
			log.Printf("[order] 手工退款失败 order=#%d: %v", id, err)
			writeJSONError(w, http.StatusInternalServerError, "failed to refund")
		}
		return
	}

	log.Printf("[order] %s 手工退款 订单 #%d 金额 %d 分（用户 #%d）原因：%s",
		operator, id, refunded, userID, reason)
	writeJSON(w, map[string]interface{}{"ok": true, "refundedCents": refunded})
}
