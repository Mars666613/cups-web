package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// 支付单状态。
const (
	PaymentStatusCreated = "created"
	PaymentStatusPaid    = "paid"
	PaymentStatusFailed  = "failed"
	PaymentStatusClosed  = "closed"
)

// 支付用途。
const (
	PaymentPurposePrint = "print"
	PaymentPurposeTopup = "topup"
)

// 支付渠道。
//
// balance 是一个特殊渠道：它在余额支付时生成，创建即已支付。
// 把它也写成 payments 行，是为了让「一笔支付覆盖 N 个订单」在余额支付与
// 在线支付两种路径下形状完全统一 —— print_orders.payment_id 永远指向
// 一个有意义的支付单，退款与对账有统一锚点。
const (
	PaymentProviderBalance = "balance"
	PaymentProviderMock    = "mock"
	PaymentProviderAlipay  = "alipay"
	PaymentProviderWechat  = "wechat"
)

// Payment 是一次「发起支付」。
//
// 一个支付单可以覆盖同一批次的多个订单（批量下单合并支付），
// 靠 print_orders.payment_id 反向引用，不建中间表。
type Payment struct {
	ID            int64
	UserID        int64
	Username      string // JOIN users 带出的展示字段
	Purpose       string
	Provider      string
	OutTradeNo    string
	AmountCents   int64
	RefundedCents int64
	Status        string
	TransactionID string
	PrepayPayload string
	RawCallback   string
	PaidAt        string
	CreatedAt     string
	UpdatedAt     string
}

// paymentColumns 与 scanPayment 的字段顺序严格对应。
const paymentColumns = `p.id, p.user_id, u.username, p.purpose, p.provider, p.out_trade_no,
	p.amount_cents, p.refunded_cents, p.status, p.transaction_id, p.prepay_payload,
	p.raw_callback, p.paid_at, p.created_at, p.updated_at`

func scanPayment(s scanner) (Payment, error) {
	var p Payment
	err := s.Scan(
		&p.ID, &p.UserID, &p.Username, &p.Purpose, &p.Provider, &p.OutTradeNo,
		&p.AmountCents, &p.RefundedCents, &p.Status, &p.TransactionID, &p.PrepayPayload,
		&p.RawCallback, &p.PaidAt, &p.CreatedAt, &p.UpdatedAt,
	)
	return p, err
}

func InsertPayment(ctx context.Context, tx *sql.Tx, p *Payment) (int64, error) {
	now := nowUTC()
	if p.CreatedAt == "" {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	var id int64
	err := tx.QueryRowContext(ctx, `INSERT INTO payments (
		user_id, purpose, provider, out_trade_no, amount_cents, refunded_cents,
		status, transaction_id, prepay_payload, raw_callback, paid_at, created_at, updated_at
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	RETURNING id`,
		p.UserID, p.Purpose, p.Provider, p.OutTradeNo, p.AmountCents, p.RefundedCents,
		p.Status, p.TransactionID, p.PrepayPayload, p.RawCallback, p.PaidAt, p.CreatedAt, p.UpdatedAt,
	).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func GetPaymentByID(ctx context.Context, tx *sql.Tx, id int64) (Payment, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+paymentColumns+`
		FROM payments p JOIN users u ON u.id = p.user_id
		WHERE p.id = $1`, id)
	return scanPayment(row)
}

// GetPaymentByOutTradeNo 按商户订单号取支付单。
//
// 支付回调必须以「支付单」为锚而不是订单：回调只带 out_trade_no，
// 要先靠它查出 purpose 与 amount_cents，才知道这笔钱是充值还是打印、
// 金额对不对，再由 payment_id 找出受影响的全部订单。
//
// 查不到时【必须拒绝】—— 那是伪造的 out_trade_no，不能凭空建单入账。
func GetPaymentByOutTradeNo(ctx context.Context, tx *sql.Tx, outTradeNo string) (Payment, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+paymentColumns+`
		FROM payments p JOIN users u ON u.id = p.user_id
		WHERE p.out_trade_no = $1`, outTradeNo)
	return scanPayment(row)
}

// MarkPaymentPaid 把支付单置为已支付，返回是否真的迁移了。
//
// WHERE status <> 'paid' 是幂等的第一层：支付渠道重复投递回调是常态
// （支付宝 25 小时内会重试 8 次），第二次起命中 0 行，调用方据此
// 跳过后续的入账与送印。
func MarkPaymentPaid(ctx context.Context, tx *sql.Tx, id int64, transactionID string) (bool, error) {
	now := nowUTC()
	res, err := tx.ExecContext(ctx, `UPDATE payments
		SET status = $1, transaction_id = $2, paid_at = $3, updated_at = $4
		WHERE id = $5 AND status <> $6`,
		PaymentStatusPaid, transactionID, now, now, id, PaymentStatusPaid)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// MarkPaymentFailed 把支付单置为失败（渠道明确返回失败，或用户放弃）。
func MarkPaymentFailed(ctx context.Context, tx *sql.Tx, id int64, raw string) error {
	_, err := tx.ExecContext(ctx, `UPDATE payments
		SET status = $1, raw_callback = $2, updated_at = $3
		WHERE id = $4 AND status = $5`,
		PaymentStatusFailed, raw, nowUTC(), id, PaymentStatusCreated)
	return err
}

// MarkPaymentClosed 把支付单置为已关闭。
//
// 用于二维码到期自动刷新：旧单在渠道侧已被关掉，本地也要跟上，
// 否则它会一直躺在「等待付款」里被后台反复查单（直到订单 TTL 到期）。
//
// WHERE status = 'created' 保证只关掉还没付款的：万一用户在关单的同时
// 刚好付款成功，这里命中 0 行，结算流程不受影响。
func MarkPaymentClosed(ctx context.Context, tx *sql.Tx, id int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE payments
		SET status = $1, updated_at = $2
		WHERE id = $3 AND status = $4`,
		PaymentStatusClosed, nowUTC(), id, PaymentStatusCreated)
	return err
}

// SetPaymentPrepay 回写渠道返回的支付凭据（二维码内容 / 跳转链接）。
func SetPaymentPrepay(ctx context.Context, tx *sql.Tx, id int64, payload string) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE payments SET prepay_payload = $1, updated_at = $2 WHERE id = $3`,
		payload, nowUTC(), id)
	return err
}

// SetPaymentRawCallback 记录最近一次回调原文（排障用，尤其是金额不匹配时）。
func SetPaymentRawCallback(ctx context.Context, tx *sql.Tx, id int64, raw string) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE payments SET raw_callback = $1, updated_at = $2 WHERE id = $3`,
		raw, nowUTC(), id)
	return err
}

// AddPaymentRefunded 累加一个支付单已退回的金额。
//
// 批量支付下退款是逐单发生的，所以这里累加而不是覆盖：
// 一个 3 订单的支付单里 1 个打印失败，只退那一个的金额。
func AddPaymentRefunded(ctx context.Context, tx *sql.Tx, id int64, amountCents int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE payments
		SET refunded_cents = refunded_cents + $1, updated_at = $2 WHERE id = $3`,
		amountCents, nowUTC(), id)
	return err
}

// RefundableTopup 是一笔还能原路退回的充值。
type RefundableTopup struct {
	ID          int64
	Provider    string
	OutTradeNo  string
	AmountCents int64
	Refunded    int64
}

// Remaining 返回这笔充值还剩多少可退。
func (r RefundableTopup) Remaining() int64 { return r.AmountCents - r.Refunded }

// ListRefundableTopups 列出该用户【还能原路退回】的充值单，按时间从早到晚。
//
// 判据是三条，缺一不可：
//   - purpose = 'topup'  钱是充进来的。打印订单的支付单不在其列 ——
//     那笔钱已经变成纸了，退它等于白送。
//   - status  = 'paid'   钱确实到过账。
//   - refunded_cents < amount_cents  还没退完（支持多次部分退款）。
//
// ⚠️ 余额支付（provider='balance'）被排除：它没有对应的渠道交易，
// 钱从没离开过系统，也就无从「原路」退回。
//
// 🚫 这也意味着【管理员手工调额送的钱退不了】—— 它压根没有渠道交易。
// 调用方必须用这个列表的和来限制退款额度，不能只看余额。
func ListRefundableTopups(ctx context.Context, tx *sql.Tx, userID int64) ([]RefundableTopup, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, provider, out_trade_no, amount_cents, refunded_cents
		FROM payments
		WHERE user_id = $1 AND purpose = $2 AND status = $3 AND provider <> $4
		  AND refunded_cents < amount_cents
		ORDER BY id`, userID, PaymentPurposeTopup, PaymentStatusPaid, PaymentProviderBalance)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []RefundableTopup{}
	for rows.Next() {
		var r RefundableTopup
		if err := rows.Scan(&r.ID, &r.Provider, &r.OutTradeNo, &r.AmountCents, &r.Refunded); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type PaymentFilter struct {
	Username string
	Status   string
	Purpose  string
	StartAt  string
	EndAt    string
	Limit    int
}

func ListPayments(ctx context.Context, tx *sql.Tx, filter PaymentFilter) ([]Payment, error) {
	args := []interface{}{}
	conds := []string{"1=1"}
	// 同 ListPrintRecords：追加实参与取占位符绑成一步。
	arg := func(v interface{}) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if filter.Username != "" {
		conds = append(conds, "u.username = "+arg(filter.Username))
	}
	if filter.Status != "" {
		conds = append(conds, "p.status = "+arg(filter.Status))
	}
	if filter.Purpose != "" {
		conds = append(conds, "p.purpose = "+arg(filter.Purpose))
	}
	if filter.StartAt != "" {
		conds = append(conds, "p.created_at >= "+arg(filter.StartAt))
	}
	if filter.EndAt != "" {
		conds = append(conds, "p.created_at <= "+arg(filter.EndAt))
	}
	query := fmt.Sprintf(`SELECT `+paymentColumns+`
		FROM payments p JOIN users u ON u.id = p.user_id
		WHERE %s
		ORDER BY p.created_at DESC, p.id DESC`, strings.Join(conds, " AND "))
	if filter.Limit > 0 {
		query += " LIMIT " + arg(filter.Limit)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	payments := []Payment{}
	for rows.Next() {
		p, err := scanPayment(rows)
		if err != nil {
			return nil, err
		}
		payments = append(payments, p)
	}
	return payments, rows.Err()
}

// UnsettledPayment 是后台查单轮询需要的最小信息。
type UnsettledPayment struct {
	ID         int64
	Provider   string
	OutTradeNo string
}

// ListUnsettledOnlinePayments 列出仍在等待到账的在线支付单。
//
// 这是「主动查单」的输入：内网 AIO 部署通常没有公网回调地址，
// 渠道的异步通知到不了我们这里，只能自己定时去问。
// 余额支付不在其列（创建即终态）。
func ListUnsettledOnlinePayments(ctx context.Context, tx *sql.Tx,
	createdBefore string, limit int) ([]UnsettledPayment, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, provider, out_trade_no FROM payments
		WHERE status = $1 AND provider <> $2 AND created_at < $3
		ORDER BY id LIMIT $4`,
		PaymentStatusCreated, PaymentProviderBalance, createdBefore, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	payments := []UnsettledPayment{}
	for rows.Next() {
		var p UnsettledPayment
		if err := rows.Scan(&p.ID, &p.Provider, &p.OutTradeNo); err != nil {
			return nil, err
		}
		payments = append(payments, p)
	}
	return payments, rows.Err()
}

// GetUnsettledPaymentByBatch 找出该批次挂着的、仍在等到账的在线支付单。
//
// 支付弹窗每 2 秒轮询批次状态，靠它【顺带】触发一次按需查单 ——
// 用户扫完码几秒内界面就会变，不必去点「我已支付」。
//
// 找不到返回 sql.ErrNoRows：批次还没发起在线支付、已经付过了、或者用的是
// 余额支付（余额支付创建即终态，不该出现在这里）。调用方据此静默跳过 ——
// 「没查到」是常态，不是错误。
func GetUnsettledPaymentByBatch(ctx context.Context, tx *sql.Tx, batchID string) (UnsettledPayment, error) {
	var p UnsettledPayment
	err := tx.QueryRowContext(ctx, `SELECT p.id, p.provider, p.out_trade_no
		FROM payments p
		JOIN print_orders o ON o.payment_id = p.id
		WHERE o.batch_id = $1 AND p.status = $2 AND p.provider <> $3
		LIMIT 1`,
		batchID, PaymentStatusCreated, PaymentProviderBalance).
		Scan(&p.ID, &p.Provider, &p.OutTradeNo)
	return p, err
}
