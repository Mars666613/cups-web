package main

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"

	"cups-web/internal/auth"
	"cups-web/internal/store"
)

// walletRecentLimit 是钱包页返回的最近流水条数。
// 单人钱包不需要分页：一个用户短期内能产生的流水条数很有限，
// 真要查完整账可以走管理端的流水查询。
const walletRecentLimit = 50

// priceViewHandler 返回给登录用户的价目表。
//
// 与 /api/admin/prices 是同一份数据、同一个构造函数 —— 价格必须对用户透明。
// 藏起价目只会让用户在下单那一刻才发现要付钱，那是最差的体验。
func priceViewHandler(w http.ResponseWriter, r *http.Request) {
	view, err := loadPriceView(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load prices")
		return
	}
	writeJSON(w, view)
}

// walletHandler 返回当前用户的余额与最近流水。
func walletHandler(w http.ResponseWriter, r *http.Request) {
	sess, err := auth.GetSession(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var balance int64
	var transactions []store.WalletTransaction
	err = appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
		ctx := r.Context()
		var err error
		if balance, err = store.GetBalance(ctx, tx, sess.UserID); err != nil {
			return err
		}
		transactions, err = store.ListWalletTransactions(ctx, tx, store.WalletFilter{
			Username: sess.Username,
			Limit:    walletRecentLimit,
		})
		return err
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load wallet")
		return
	}

	// 可原路退回的额度。前端据此决定「退款」按钮是否可用、以及最多能填多少。
	//
	// 读失败时回 0 而不是让整个钱包接口挂掉：退款按钮暂时不可用，
	// 但余额、流水这些用户真正天天要看的东西照常显示。
	refundable, rerr := refundableCents(r.Context(), sess.UserID)
	if rerr != nil {
		log.Printf("[refund] 计算可退额度失败 user=%s: %v", sess.Username, rerr)
		refundable = 0
	}

	writeJSON(w, map[string]interface{}{
		"balanceCents":    balance,
		"refundableCents": refundable,
		"currency":        CurrencyCNY,
		"paymentMethods":  availableProviders(),
		"transactions":    mapWalletTransactions(transactions),
	})
}

// walletTopupHandler 发起一笔余额充值。
//
// 返回的是渠道凭据；钱到账后由 settlePayment 入账（充值走 topup 分支）。
// 也就是说充值接口本身【不】改余额 —— 只有「钱确实到了」才会改。
func walletTopupHandler(w http.ResponseWriter, r *http.Request) {
	sess, err := auth.GetSession(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var payload struct {
		AmountCents int64  `json:"amountCents"`
		Provider    string `json:"provider"`
		// Channel / ReturnPath 由前端按【当前设备与当前页面】给出，见 onlinePayOptions。
		Channel    string `json:"channel"`
		ReturnPath string `json:"returnPath"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	if payload.AmountCents <= 0 {
		writeJSONError(w, http.StatusBadRequest, "充值金额必须大于 0")
		return
	}
	// 上限防手滑（也防有人用充值接口构造一个天文数字的支付单）。
	const maxTopupCents = 100000000 // 100 万元
	if payload.AmountCents > maxTopupCents {
		writeJSONError(w, http.StatusBadRequest, "单次充值金额过大")
		return
	}
	if payload.Provider == "" {
		methods := availableProviders()
		if len(methods) == 0 {
			writeJSONError(w, http.StatusBadRequest, "当前没有可用的在线支付渠道")
			return
		}
		payload.Provider = methods[0]
	}

	resp, err := startTopupPayment(r.Context(), r, sess.UserID, payload.AmountCents, payload.Provider,
		onlinePayOptions{Channel: payload.Channel, ReturnPath: payload.ReturnPath})
	if err != nil {
		writePaymentStartError(w, err)
		return
	}
	writeJSON(w, resp)
}

type walletTransactionResponse struct {
	ID int64 `json:"id"`
	// 只有管理端列表会填这一列：用户看自己的钱包时它纯属冗余，
	// 而管理端是跨用户的流水，没有它就没法看。
	Username          string `json:"username,omitempty"`
	Kind              string `json:"kind"`
	AmountCents       int64  `json:"amountCents"`
	BalanceAfterCents int64  `json:"balanceAfterCents"`
	OrderID           int64  `json:"orderId,omitempty"`
	Operator          string `json:"operator,omitempty"`
	Note              string `json:"note,omitempty"`
	CreatedAt         string `json:"createdAt"`
}

func mapWalletTransactions(rows []store.WalletTransaction) []walletTransactionResponse {
	out := make([]walletTransactionResponse, 0, len(rows))
	for _, t := range rows {
		out = append(out, walletTransactionResponse{
			ID:                t.ID,
			Kind:              t.Kind,
			AmountCents:       t.AmountCents,
			BalanceAfterCents: t.BalanceAfterCents,
			OrderID:           t.OrderID.Int64,
			Operator:          t.Operator,
			Note:              t.Note,
			CreatedAt:         t.CreatedAt,
		})
	}
	return out
}

// mapWalletTransactionsWithUser 与 mapWalletTransactions 相同，但补上用户名。
func mapWalletTransactionsWithUser(rows []store.WalletTransaction) []walletTransactionResponse {
	out := mapWalletTransactions(rows)
	for i := range out {
		out[i].Username = rows[i].Username
	}
	return out
}
