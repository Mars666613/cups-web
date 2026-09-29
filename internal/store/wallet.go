package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// 余额变动类型。
const (
	WalletKindTopup       = "topup"
	WalletKindSpend       = "spend"
	WalletKindRefund      = "refund"
	WalletKindAdminAdjust = "admin_adjust"
	// WalletKindWithdraw 是余额【原路退回】到支付渠道。
	//
	// 与 WalletKindRefund 的区别：refund 是打印失败后把钱退回【余额】，
	// 钱没离开系统；withdraw 是把余额里的钱退回【用户的支付宝】，
	// 钱真的出去了。对账时这两者必须分得清。
	WalletKindWithdraw = "withdraw"
	// WalletKindWithdrawRollback 是原路退款失败时把已扣的余额补回来。
	WalletKindWithdrawRollback = "withdraw_rollback"
)

// ─── 幂等键 ────────────────────────────────────────────────────────────
//
// 全项目的幂等键只在下面这几个构造函数里产生，调用方一律走它们，
// 🚫 不要手写字符串字面量 —— 拼错一个字符就绕过了
// wallet_transactions.idempotency_key 的唯一索引，后果是重复入账。
//
//	spend:<ref>        余额支付扣款。单订单用 order_id 的十进制串，
//	                   批量下单用 batch_id（一次支付只扣一次）
//	refund:<order_id>  订单退款（打印失败自动退 / 管理员手工退共用）
//	pay:<out_trade_no> 在线充值到账
//	adjust:<随机串>    管理员手工调整余额（天然不重复）

// WalletKeySpend 构造扣款幂等键。
// ref 对单订单是 order_id 的十进制串，对批量是 batch_id —— 语义是
// 「这一次支付」，同一笔支付覆盖的多个订单只应扣一次钱。
func WalletKeySpend(ref string) string { return "spend:" + ref }

// WalletKeySpendOrder 是 WalletKeySpend 针对单订单的便捷包装。
func WalletKeySpendOrder(orderID int64) string {
	return WalletKeySpend(strconv.FormatInt(orderID, 10))
}

// WalletKeyRefund 构造退款幂等键。键里带 order_id 而不是支付单 id，
// 因为批量支付下退款是逐单发生的：一个支付单覆盖 3 个订单、其中 1 个打印失败，
// 那次退款只该退那一个订单的金额。
func WalletKeyRefund(orderID int64) string {
	return "refund:" + strconv.FormatInt(orderID, 10)
}

// WalletKeyPay 构造在线充值到账的幂等键。
func WalletKeyPay(outTradeNo string) string { return "pay:" + outTradeNo }

// WalletKeyAdjust 构造管理员手工调额的幂等键。
func WalletKeyAdjust() string { return "adjust:" + rand.Text() }

// WalletKeyWithdraw 构造余额原路退款的扣款幂等键。
// ref 是一次退款操作的批次号（同一次操作里的多笔渠道退款共用它）。
func WalletKeyWithdraw(ref string) string { return "withdraw:" + ref }

// WalletKeyWithdrawRollback 构造「原路退款失败、余额补回」的幂等键。
func WalletKeyWithdrawRollback(ref string) string { return "withdraw_rollback:" + ref }

// ─── 错误 ──────────────────────────────────────────────────────────────

var (
	// ErrInsufficientBalance 余额不足。出账时条件 UPDATE 命中 0 行。
	ErrInsufficientBalance = errors.New("store: insufficient balance")
	// ErrDuplicateTransaction 幂等键撞车，说明这笔变动已经记过了。
	// 调用方应当【当作成功处理】，而不是报错。
	ErrDuplicateTransaction = errors.New("store: duplicate wallet transaction")
)

// ─── 余额变动 ──────────────────────────────────────────────────────────

type WalletDeltaInput struct {
	UserID         int64
	Kind           string
	AmountCents    int64 // 正 = 入账，负 = 出账，不得为 0
	IdempotencyKey string
	OrderID        int64 // 0 = 无
	PaymentID      int64 // 0 = 无
	Operator       string
	Note           string
}

// ApplyWalletDelta 是全项目【唯一】允许修改 users.balance_cents 的地方。
//
// 它在同一个事务内完成「改余额」与「记流水」两件事，并保证：
//   - 出账用条件 UPDATE 一步完成「够不够」与「扣多少」，不会双花
//   - 流水插入放最后，幂等键撞车时整事务回滚，上面的余额改动一并撤销
//
// 返回变更后的余额。
//
// 🚫 不要在其他任何地方写 `UPDATE users SET balance_cents = ...`。
// 余额与账本一旦分叉，对账就再也做不回来了。有一条 _test.go 在扫这个。
func ApplyWalletDelta(ctx context.Context, tx *sql.Tx, in WalletDeltaInput) (int64, error) {
	if in.IdempotencyKey == "" {
		return 0, errors.New("store: wallet delta requires an idempotency key")
	}
	if in.AmountCents == 0 {
		return 0, errors.New("store: wallet delta amount must not be zero")
	}

	// 出账必须带条件：把「余额是否足够」和「扣款」放进同一条语句，
	// 避免「先 SELECT 再 UPDATE」在并发下双花。
	// 入账不需要条件（余额上不封顶）。
	var res sql.Result
	var err error
	if in.AmountCents < 0 {
		res, err = tx.ExecContext(ctx, `UPDATE users SET balance_cents = balance_cents + ?
			WHERE id = ? AND balance_cents + ? >= 0`,
			in.AmountCents, in.UserID, in.AmountCents)
	} else {
		res, err = tx.ExecContext(ctx, `UPDATE users SET balance_cents = balance_cents + ?
			WHERE id = ?`, in.AmountCents, in.UserID)
	}
	if err != nil {
		return 0, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if affected == 0 {
		if in.AmountCents < 0 {
			return 0, ErrInsufficientBalance
		}
		// 入账命中 0 行只可能是用户不存在。
		return 0, sql.ErrNoRows
	}

	var balanceAfter int64
	if err := tx.QueryRowContext(ctx,
		`SELECT balance_cents FROM users WHERE id = ?`, in.UserID).Scan(&balanceAfter); err != nil {
		return 0, err
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO wallet_transactions (
		user_id, kind, amount_cents, balance_after_cents, order_id, payment_id,
		idempotency_key, operator, note, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.UserID, in.Kind, in.AmountCents, balanceAfter,
		nullID(in.OrderID), nullID(in.PaymentID),
		in.IdempotencyKey, in.Operator, in.Note, nowUTC(),
	); err != nil {
		if isUniqueViolation(err) {
			return 0, ErrDuplicateTransaction
		}
		return 0, err
	}
	return balanceAfter, nil
}

// GetBalance 读账户余额（单位：分）。
func GetBalance(ctx context.Context, tx *sql.Tx, userID int64) (int64, error) {
	var balance int64
	err := tx.QueryRowContext(ctx,
		`SELECT balance_cents FROM users WHERE id = ?`, userID).Scan(&balance)
	return balance, err
}

// HasWalletTransactions 判断该用户是否有任何余额流水。
// 管理员删除用户前用它拦截 —— 删用户会级联删掉账本，
// 有流水的用户被删 = 财务记录消失。
func HasWalletTransactions(ctx context.Context, tx *sql.Tx, userID int64) (bool, error) {
	var count int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM wallet_transactions WHERE user_id = ?`, userID).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// ─── 流水查询 ──────────────────────────────────────────────────────────

type WalletTransaction struct {
	ID                int64
	UserID            int64
	Username          string
	Kind              string
	AmountCents       int64
	BalanceAfterCents int64
	OrderID           sql.NullInt64
	PaymentID         sql.NullInt64
	IdempotencyKey    string
	Operator          string
	Note              string
	CreatedAt         string
}

// walletTransactionColumns 与 scanWalletTransaction 的字段顺序严格对应。
const walletTransactionColumns = `w.id, w.user_id, u.username, w.kind, w.amount_cents,
	w.balance_after_cents, w.order_id, w.payment_id, w.idempotency_key, w.operator, w.note, w.created_at`

func scanWalletTransaction(s scanner) (WalletTransaction, error) {
	var t WalletTransaction
	err := s.Scan(
		&t.ID, &t.UserID, &t.Username, &t.Kind, &t.AmountCents,
		&t.BalanceAfterCents, &t.OrderID, &t.PaymentID, &t.IdempotencyKey, &t.Operator, &t.Note, &t.CreatedAt,
	)
	return t, err
}

type WalletFilter struct {
	Username string
	Kind     string
	StartAt  string
	EndAt    string
	Limit    int
}

func ListWalletTransactions(ctx context.Context, tx *sql.Tx, filter WalletFilter) ([]WalletTransaction, error) {
	args := []interface{}{}
	conds := []string{"1=1"}
	if filter.Username != "" {
		conds = append(conds, "u.username = ?")
		args = append(args, filter.Username)
	}
	if filter.Kind != "" {
		conds = append(conds, "w.kind = ?")
		args = append(args, filter.Kind)
	}
	if filter.StartAt != "" {
		conds = append(conds, "w.created_at >= ?")
		args = append(args, filter.StartAt)
	}
	if filter.EndAt != "" {
		conds = append(conds, "w.created_at <= ?")
		args = append(args, filter.EndAt)
	}
	query := fmt.Sprintf(`SELECT `+walletTransactionColumns+`
		FROM wallet_transactions w
		JOIN users u ON u.id = w.user_id
		WHERE %s
		ORDER BY w.created_at DESC, w.id DESC`, strings.Join(conds, " AND "))
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// 用 []T{} 而不是 var []T —— 空结果要序列化成 [] 而不是 null，
	// 前端对空数组更友好（users.go 的写法是对的，prints.go 是反例）。
	transactions := []WalletTransaction{}
	for rows.Next() {
		t, err := scanWalletTransaction(rows)
		if err != nil {
			return nil, err
		}
		transactions = append(transactions, t)
	}
	return transactions, rows.Err()
}

// ─── 工具 ──────────────────────────────────────────────────────────────

// nullID 把 0 转成 SQL NULL。
// order_id / payment_id 用 0 表示「无」，但那两列是可空外键 —— 写 NULL 比写 0
// 更诚实（0 不指向任何一行，会让 ON DELETE SET NULL 语义变得含糊）。
func nullID(id int64) interface{} {
	if id == 0 {
		return nil
	}
	return id
}

// isUniqueViolation 判断错误是否来自 UNIQUE 索引冲突。
// modernc.org/sqlite 的错误文本形如：
//
//	constraint failed: UNIQUE constraint failed: wallet_transactions.idempotency_key (2067)
//
// 这里用字符串匹配而不是错误码，与 migrate() 里 addColumnIfMissing 识别
// "duplicate column name" 的做法一致。
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
