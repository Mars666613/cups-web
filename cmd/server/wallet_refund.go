package main

// 余额原路退款（提现）。
//
// ── 与「打印失败退款」的区别 ──
//
// 打印失败时钱是退回【余额】的，钱没离开系统，所以那件事纯在数据库里完成。
// 这里要把钱真的退回用户的支付宝，涉及出站调用与资金流出，因此多了一堆
// 必须小心的地方。
//
// ── 退款额度由什么决定 ──
//
// 🚫【不是】看余额。余额里有三种钱，只有一种能原路退回：
//
//	充值进来的（payments.purpose='topup' 且 status='paid'）  → 能退
//	打印花掉的                                              → 已经变成纸了
//	管理员手工调额送的（wallet_transactions.kind='admin_adjust'）→ 退不了
//
// 最后一条最容易被忽略：管理员调额没有对应的渠道交易，支付宝那边根本
// 不存在这笔钱，无从「原路」。所以可退额度取
// 【min(余额, 未退完的充值单之和)】，见 refundableCents。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"cups-web/internal/auth"
	"cups-web/internal/payment"
	"cups-web/internal/store"
)

// formatYuan 把「分」格式化成给用户看的元。
// 与 payment 包里给渠道的定长格式分开写：那个面向机器，这个面向人。
func formatYuan(cents int64) string {
	return fmt.Sprintf("¥%d.%02d", cents/100, cents%100)
}

// errNothingRefundable 没有可原路退回的充值。
var errNothingRefundable = errors.New("没有可原路退回的充值")

// refundableCents 返回该用户当前能原路退回的额度。
//
// 取两者中较小的：
//   - 余额：已经花掉的钱不能退，否则就是超发
//   - 未退完的充值单之和：没有渠道交易的钱（管理员调额）退不了
func refundableCents(ctx context.Context, userID int64) (int64, error) {
	var balance int64
	var tops []store.RefundableTopup

	err := appStore.WithTx(ctx, true, func(tx *sql.Tx) error {
		var err error
		if balance, err = store.GetBalance(ctx, tx, userID); err != nil {
			return err
		}
		tops, err = store.ListRefundableTopups(ctx, tx, userID)
		return err
	})
	if err != nil {
		return 0, err
	}

	var sum int64
	for _, t := range tops {
		sum += t.Remaining()
	}
	if sum > balance {
		return balance, nil
	}
	return sum, nil
}

// walletRefundHandler 处理 POST /api/wallet/refund。
//
// 请求体 {"amountCents": 5000}。
//
// ── 为什么先扣余额、再去渠道退 ──
//
// 两个失败方向不对称：
//   - 先扣款后退款失败 → 用户余额少了一笔，钱还在我们账上。用户看得见，
//     可以重试，管理员也能查出来补。**对用户可逆**
//   - 先退款后扣款失败 → 用户既拿到了钱、余额又还在。**对我们不可逆**，
//     而且这种差错没有任何自动手段能发现
//
// 所以顺序是先扣。渠道退款失败时把没退成的部分补回余额（走
// withdraw_rollback 幂等键），用户不会真的损失。
func walletRefundHandler(w http.ResponseWriter, r *http.Request) {
	sess, err := auth.GetSession(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var payload struct {
		AmountCents int64 `json:"amountCents"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if payload.AmountCents <= 0 {
		writeJSONError(w, http.StatusBadRequest, "退款金额必须大于 0")
		return
	}

	// 单飞：同一用户的两次退款请求串行处理。
	//
	// 退款是「读余额 → 扣余额 → 出站退款」三步，并发下两个请求会各自
	// 读到同一份余额、各自通过校验，最后把同一个余额扣两次。
	// 余额扣减本身有 balance >= amount 的守卫，撑不住的是「可退额度」
	// 那一侧 —— 它依赖一个非原子的求和。
	lock := userRefundLock(sess.UserID)
	lock.Lock()
	defer lock.Unlock()

	tops, balance, err := loadRefundContext(r.Context(), sess.UserID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load wallet")
		return
	}

	var refundable int64
	for _, t := range tops {
		refundable += t.Remaining()
	}
	if refundable > balance {
		refundable = balance
	}

	if refundable <= 0 {
		writeJSONError(w, http.StatusUnprocessableEntity,
			"当前没有可原路退回的充值。只有通过支付宝充值且尚未花掉的金额才能退回。")
		return
	}
	if payload.AmountCents > refundable {
		writeJSONError(w, http.StatusUnprocessableEntity,
			"超出可退款额度，最多可退 "+formatYuan(refundable))
		return
	}

	// 一次退款操作一个批次号：扣款幂等键、各笔渠道退款的单号都挂在它上面。
	batchNo := "RF" + time.Now().UTC().Format("20060102150405") + randomToken()[:6]

	// ── 第一步：先扣余额 ──
	err = appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
		_, aerr := store.ApplyWalletDelta(r.Context(), tx, store.WalletDeltaInput{
			UserID:         sess.UserID,
			Kind:           store.WalletKindWithdraw,
			AmountCents:    -payload.AmountCents,
			IdempotencyKey: store.WalletKeyWithdraw(batchNo),
			Operator:       sess.Username,
			Note:           "余额原路退回",
		})
		return aerr
	})
	if err != nil {
		if errors.Is(err, store.ErrInsufficientBalance) {
			writeJSONError(w, http.StatusUnprocessableEntity, "账户余额不足")
			return
		}
		log.Printf("[refund] 扣减余额失败 user=%s: %v", sess.Username, err)
		writeJSONError(w, http.StatusInternalServerError, "退款失败，请稍后重试")
		return
	}

	// ── 第二步：逐笔向渠道退款 ──
	// 从最早的充值单开始退，直到凑够金额。
	done, refunded, failErr := refundFromTopups(r.Context(), sess.UserID, tops, payload.AmountCents, batchNo)

	// ── 第三步：把没退成的部分补回余额 ──
	if failErr != nil && refunded < payload.AmountCents {
		back := payload.AmountCents - refunded
		rollbackErr := appStore.WithTx(context.Background(), false, func(tx *sql.Tx) error {
			_, aerr := store.ApplyWalletDelta(context.Background(), tx, store.WalletDeltaInput{
				UserID:         sess.UserID,
				Kind:           store.WalletKindWithdrawRollback,
				AmountCents:    back,
				IdempotencyKey: store.WalletKeyWithdrawRollback(batchNo),
				Operator:       sess.Username,
				Note:           "原路退款失败，金额退回余额",
			})
			return aerr
		})
		if rollbackErr != nil {
			// 这一条如果出现，用户余额会短这笔钱，必须人工介入。
			log.Printf("[refund] ⚠️ 退款失败且余额回滚也失败 user=%s batch=%s 未退金额=%d 分 回滚错误=%v 原始错误=%v",
				sess.Username, batchNo, back, rollbackErr, failErr)
		}
		log.Printf("[refund] user=%s 退款部分失败，已退 %d 分，回滚 %d 分: %v",
			sess.Username, refunded, back, failErr)
		writeJSONError(w, http.StatusBadGateway,
			"退款失败："+failErr.Error()+"（未退成的部分已留在余额里）")
		return
	}

	log.Printf("[refund] user=%s 原路退款 %d 分，涉及 %d 笔充值单，批次 %s",
		sess.Username, refunded, done, batchNo)

	// 回读最终余额与流水，让前端展示的服务端事实。
	var finalBalance int64
	var txs []store.WalletTransaction
	_ = appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
		var lerr error
		if finalBalance, lerr = store.GetBalance(r.Context(), tx, sess.UserID); lerr != nil {
			return lerr
		}
		txs, lerr = store.ListWalletTransactions(r.Context(), tx, store.WalletFilter{
			Username: sess.Username, Limit: walletRecentLimit,
		})
		return lerr
	})

	writeJSON(w, map[string]interface{}{
		"refundedCents": refunded,
		"balanceCents":  finalBalance,
		"transactions":  mapWalletTransactions(txs),
	})
}

// loadRefundContext 一次性读出余额与可退款充值单。
func loadRefundContext(ctx context.Context, userID int64) ([]store.RefundableTopup, int64, error) {
	var balance int64
	var tops []store.RefundableTopup
	err := appStore.WithTx(ctx, true, func(tx *sql.Tx) error {
		var err error
		if balance, err = store.GetBalance(ctx, tx, userID); err != nil {
			return err
		}
		tops, err = store.ListRefundableTopups(ctx, tx, userID)
		return err
	})
	return tops, balance, err
}

// refundFromTopups 从最早的充值单开始逐笔退款，直到凑够 amount。
//
// 返回 (退成的笔数, 实际退成的金额, 错误)。错误非 nil 时前两个值仍然有效 ——
// 调用方要据此把没退成的部分补回余额。
//
// ⚠️ 出站调用刻意放【事务外】：退款是网络请求，塞进 WithTx 会占着
// SQLite 的写事务等往返。每一笔退成功后立刻单独提交本地记录，
// 这样中途失败时「已经退出去的」是被记账的。
func refundFromTopups(ctx context.Context, userID int64, tops []store.RefundableTopup,
	amount int64, batchNo string) (int, int64, error) {

	remaining := amount
	count := 0
	var refunded int64

	for i, t := range tops {
		if remaining <= 0 {
			break
		}
		slice := t.Remaining()
		if slice > remaining {
			slice = remaining
		}

		provider, err := buildProvider(t.Provider)
		if err != nil {
			return count, refunded, errors.New("该充值使用的支付渠道已不可用，请联系管理员")
		}

		// 退款单号：批次号 + 序号。同一批里每笔交易的单号互不相同，
		// 而整批重试时会沿用同一个批次号 —— 渠道据此判重。
		refundNo := batchNo + "-" + itoa(i+1)

		refundCtx, cancel := context.WithTimeout(context.Background(), paymentChannelTimeout)
		res, rerr := provider.Refund(refundCtx, payment.RefundRequest{
			OutTradeNo:  t.OutTradeNo,
			RefundNo:    refundNo,
			AmountCents: slice,
			Reason:      "余额退款",
		})
		cancel()
		if rerr != nil {
			return count, refunded, rerr
		}

		// 记账：payment 的已退金额 + 余额流水的对手记录。
		// 这两条与上面余额扣减不在同一个事务里 —— 扣减已经提交了，
		// 这里失败只影响对账明细，不影响用户的钱。
		werr := appStore.WithTx(ctx, false, func(tx *sql.Tx) error {
			if err := store.AddPaymentRefunded(ctx, tx, t.ID, res.AmountCents); err != nil {
				return err
			}
			return nil
		})
		if werr != nil {
			// 钱已经退给用户了，本地明细没记上。必须留痕，人工能补。
			log.Printf("[refund] ⚠️ 渠道已退款但本地记账失败 payment=%d outTradeNo=%s 金额=%d 分 退款单=%s: %v",
				t.ID, t.OutTradeNo, res.AmountCents, refundNo, werr)
		}

		refunded += res.AmountCents
		remaining -= res.AmountCents
		count++
	}

	if remaining > 0 {
		return count, refunded, errors.New("可退款的充值单不足以覆盖该金额")
	}
	return count, refunded, nil
}

// ─── 单飞锁 ──────────────────────────────────────────────────────────
//
// 同一用户的退款请求串行化。理由见 walletRefundHandler 里的说明：
// 「可退额度」依赖一次非原子的求和，并发下两个请求会各自通过校验。

var refundLocks = struct {
	mu sync.Mutex
	m  map[int64]*sync.Mutex
}{m: map[int64]*sync.Mutex{}}

func userRefundLock(userID int64) *sync.Mutex {
	refundLocks.mu.Lock()
	defer refundLocks.mu.Unlock()
	l, ok := refundLocks.m[userID]
	if !ok {
		l = &sync.Mutex{}
		refundLocks.m[userID] = l
	}
	return l
}
