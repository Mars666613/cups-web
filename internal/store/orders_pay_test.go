package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// ─── 余额支付的事务体（store 层） ───────────────────────────────────────
//
// 这里覆盖的是 cmd/server/order_print.go payOrdersByBalance 的每一步，
// 顺序也保持一致：加锁求和 → 扣款 → 挂支付单 → 标订单已支付。
//
// 分开测的理由：那一整段跑在 HTTP handler 里，而它失败时对用户只呈现
// 一句 500 failed to pay（真正的错因只在服务端日志里）。把事务体搬到
// 测试里，SQL 层面的错就有一个会指名道姓的失败点。

// TestSumPendingAmountByBatchLocked 覆盖余额支付的那条 SQL。
//
// ⚠️ 回归守卫。曾经的实现是
//
//	SELECT SUM(amount_cents)::bigint FROM print_orders
//	WHERE batch_id = $1 AND status = $2 FOR UPDATE
//
// PostgreSQL 直接拒绝：FOR UPDATE 不能与聚合函数同现
// （实测报「FOR UPDATE不能用于聚合函数中」）。而这条 SQL 是余额支付
// 【唯一】的取额入口，一旦这样写，余额支付就 100% 返回 500
// failed to pay，在线支付却毫发无伤（它走不加锁的 SumPendingAmountByBatch）
// —— 现象极易被误读成「余额支付功能没接」。真机故障，别再写回去。
func TestSumPendingAmountByBatchLocked(t *testing.T) {
	s := openTestStore(t)
	userID := createTestUser(t, s, "locked-sum")

	createTestOrder(t, s, userID, "batch-a", 120)
	createTestOrder(t, s, userID, "batch-a", 80)
	// 别的批次不能被算进来。
	createTestOrder(t, s, userID, "batch-b", 999)

	lockedSum := func(batchID string) (int64, error) {
		var total int64
		err := s.WithTx(context.Background(), false, func(tx *sql.Tx) error {
			var err error
			total, err = SumPendingAmountByBatchLocked(context.Background(), tx, batchID)
			return err
		})
		return total, err
	}

	total, err := lockedSum("batch-a")
	if err != nil {
		t.Fatalf("加锁求和失败（PG 不允许聚合与 FOR UPDATE 同现，这条路径必须避开）: %v", err)
	}
	if total != 200 {
		t.Errorf("batch-a 合计 = %d，期望 200", total)
	}

	// 批次里没有待支付订单时返回 0 而不是报错：上层据此报
	// 「没有待支付的订单，请刷新查看」，不能变成 500。
	total, err = lockedSum("no-such-batch")
	if err != nil {
		t.Fatalf("空批次加锁求和失败: %v", err)
	}
	if total != 0 {
		t.Errorf("空批次合计 = %d，期望 0", total)
	}
}

// TestBalancePayTransaction 完整走一遍余额支付的事务体。
//
// 除了 SQL 能执行，还要验三件容易被忽略的事：
//   - 扣的是【这一批】的合计，不是所有订单
//   - 余额与账本一致（ApplyWalletDelta 是唯一改余额的入口）
//   - 同一批次重复提交不会二次扣款（幂等键 = batchID）
func TestBalancePayTransaction(t *testing.T) {
	s := openTestStore(t)
	userID := createTestUser(t, s, "balance-pay")
	ctx := context.Background()

	createTestOrder(t, s, userID, "batch-pay", 200)
	createTestOrder(t, s, userID, "batch-pay", 100)
	// 另一批的订单不该被动到。
	createTestOrder(t, s, userID, "batch-other", 700)

	// 先充值 500 分。
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		_, err := ApplyWalletDelta(ctx, tx, WalletDeltaInput{
			UserID:         userID,
			Kind:           WalletKindTopup,
			AmountCents:    500,
			IdempotencyKey: "topup:test-1",
			Note:           "测试充值",
		})
		return err
	})

	pay := func() (int64, error) {
		var paidCount int64
		err := s.WithTx(ctx, false, func(tx *sql.Tx) error {
			total, err := SumPendingAmountByBatchLocked(ctx, tx, "batch-pay")
			if err != nil {
				return err
			}
			if total <= 0 {
				return errNoPendingForTest
			}
			if _, err := ApplyWalletDelta(ctx, tx, WalletDeltaInput{
				UserID:         userID,
				Kind:           WalletKindSpend,
				AmountCents:    -total,
				IdempotencyKey: WalletKeySpend("batch-pay"),
				Note:           "打印订单支付",
			}); err != nil {
				return err
			}
			paymentID, err := InsertPayment(ctx, tx, &Payment{
				UserID:      userID,
				Purpose:     PaymentPurposePrint,
				Provider:    PaymentProviderBalance,
				OutTradeNo:  "BALbatch-pay",
				AmountCents: total,
				Status:      PaymentStatusPaid,
				PaidAt:      time.Now().UTC().Format(time.RFC3339),
			})
			if err != nil {
				return err
			}
			ids, err := MarkPendingOrdersPaid(ctx, tx, "batch-pay",
				PaymentProviderBalance, paymentID)
			if err != nil {
				return err
			}
			paidCount = int64(len(ids))
			return nil
		})
		return paidCount, err
	}

	paid, err := pay()
	if err != nil {
		t.Fatalf("余额支付事务失败: %v", err)
	}
	if paid != 2 {
		t.Errorf("标记已支付的订单数 = %d，期望 2", paid)
	}
	if got := balanceOf(t, s, userID); got != 200 {
		t.Errorf("支付后余额 = %d，期望 200（500 - 300）", got)
	}

	// 状态确实推进了，而且只有这一批。
	if n := countOrderStatus(t, s, "batch-pay", OrderStatusPaid); n != 2 {
		t.Errorf("batch-pay 中已支付订单 = %d，期望 2", n)
	}
	if n := countOrderStatus(t, s, "batch-other", OrderStatusPaid); n != 0 {
		t.Errorf("batch-other 不该被改动，却已有 %d 单变成已支付", n)
	}

	// 幂等键：同一个 key 再扣一次必须被账本拦下，而不是静默扣第二遍。
	//
	// ⚠️ 金额用一个余额付得起的数（-100）。幂等的判定在流水插入那一刻，
	// 而它前面还有一道余额条件 —— 金额超过余额时会先撞上「余额不足」，
	// 根本走不到幂等检查，就测不到这里真正想说的事。
	err = s.WithTx(ctx, false, func(tx *sql.Tx) error {
		_, err := ApplyWalletDelta(ctx, tx, WalletDeltaInput{
			UserID:         userID,
			Kind:           WalletKindSpend,
			AmountCents:    -100,
			IdempotencyKey: WalletKeySpend("batch-pay"),
			Note:           "打印订单支付",
		})
		return err
	})
	if !errors.Is(err, ErrDuplicateTransaction) {
		t.Errorf("重复扣款应返回 ErrDuplicateTransaction，实际 err = %v", err)
	}
	if got := balanceOf(t, s, userID); got != 200 {
		t.Errorf("重复扣款后余额 = %d，期望仍是 200（不能二次扣款）", got)
	}

	// 订单都付完之后再走一次「取合计」应当取到 0，上层据此报
	// 「没有待支付的订单」，而不是又扣一遍。
	var leftover int64
	err = s.WithTx(ctx, false, func(tx *sql.Tx) error {
		var err error
		leftover, err = SumPendingAmountByBatchLocked(ctx, tx, "batch-pay")
		return err
	})
	if err != nil {
		t.Fatalf("付完后再取合计失败: %v", err)
	}
	if leftover != 0 {
		t.Errorf("付完后待支付合计 = %d，期望 0", leftover)
	}
}

// countOrderStatus 数某个批次里处于指定状态的订单。
func countOrderStatus(t *testing.T, s *Store, batchID, status string) int {
	t.Helper()
	return countRows(t, s, "print_orders",
		"batch_id = $1 AND status = $2", batchID, status)
}

// errNoPendingForTest 对应 cmd/server 里的 errNoPendingOrders ——
// 那个变量在 handler 包里，store 测试只需要一个等价的错误值。
var errNoPendingForTest = errors.New("order: no pending orders in batch")
