package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// withSearchPath 在 DSN 上追加 search_path 选项。
//
// ⚠️ 走 DSN 而不是执行 `SET search_path`：连接池里每条连接都要生效，
// 而 SET 只影响当前那一条 —— 表现是「测试偶尔查不到表」，极难查。
func withSearchPath(dsn, schema string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	// ⚠️ 空格必须编成 %20，不能留 url.QueryEscape 默认的 '+'：
	// pgx/libpq 会把 '+' 当成参数名的一部分，报
	// 「unrecognized configuration parameter "+search_path"」。
	opt := strings.ReplaceAll(url.QueryEscape("-c search_path="+schema), "+", "%20")
	return dsn + sep + "options=" + opt
}

// testSchema 建一个随机 schema 并把 search_path 指过去，返回可直接用的 DSN。
//
// ⚠️ 需要环境变量 TEST_DATABASE_URL。未设置时【跳过】而不是失败 ——
// 没装 PG 的机器上 `go test ./...` 不该是红的。
//
//	TEST_DATABASE_URL='postgres://postgres@127.0.0.1:5432/cupsweb?sslmode=disable' go test ./...
//
// 隔离：随机 schema + search_path，结束后 DROP ... CASCADE。
// 不共用一个 schema，是为了测试之间互不污染、且可以并发跑。
//
// 同一测试内多次 Open 请复用同一个返回值 —— 那样才共享同一个 schema。
func testSchema(t *testing.T) string {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL 未设置，跳过需要 PostgreSQL 的测试")
	}
	schema := fmt.Sprintf("t%d_%d", time.Now().UnixNano()%1e9, rand.Intn(10000))

	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		_ = admin.Close()
		t.Fatalf("建 schema 失败: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE")
		_ = admin.Close()
	})
	return withSearchPath(base, schema)
}

// openTestStore 开一个隔离 schema 的测试库。
func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), testSchema(t), 4)
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// mustExec 在事务里跑一条写语句。
func mustExec(t *testing.T, s *Store, fn func(context.Context, *sql.Tx) error) {
	t.Helper()
	err := s.WithTx(context.Background(), false, func(tx *sql.Tx) error {
		return fn(context.Background(), tx)
	})
	if err != nil {
		t.Fatalf("事务失败: %v", err)
	}
}

// balanceOf 读账户余额（分）。
func balanceOf(t *testing.T, s *Store, userID int64) int64 {
	t.Helper()
	var balance int64
	err := s.WithTx(context.Background(), true, func(tx *sql.Tx) error {
		var err error
		balance, err = GetBalance(context.Background(), tx, userID)
		return err
	})
	if err != nil {
		t.Fatalf("读余额失败: %v", err)
	}
	return balance
}

// countRows 数一张表的行数。
func countRows(t *testing.T, s *Store, table string, where string, args ...interface{}) int {
	t.Helper()
	query := "SELECT COUNT(1) FROM " + table
	if where != "" {
		query += " WHERE " + where
	}
	var n int
	err := s.WithTx(context.Background(), true, func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), query, args...).Scan(&n)
	})
	if err != nil {
		t.Fatalf("计数 %s 失败: %v", table, err)
	}
	return n
}

// createTestUser 建一个测试用户并返回 id。
// 直接写 SQL 而不是走 CreateUser —— 计费测试不关心密码哈希，
// 少一个 bcrypt 依赖，测试意图也更清楚。
func createTestUser(t *testing.T, s *Store, username string) int64 {
	t.Helper()
	var id int64
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `INSERT INTO users
			(username, password_hash, role, protected, contact_name, phone, email,
			 balance_cents, created_at, updated_at)
			VALUES ($1, 'x', 'user', false, '', '', '', 0, $2, $3) RETURNING id`,
			username, nowUTC(), nowUTC()).Scan(&id)
	})
	return id
}

// createTestOrder 建一条待支付订单，返回 id。
func createTestOrder(t *testing.T, s *Store, userID int64, batchID string, amount int64) int64 {
	t.Helper()
	var id int64
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		id, err = InsertPrintOrder(ctx, tx, &PrintOrder{
			UserID:          userID,
			Source:          OrderSourceWeb,
			BatchID:         batchID,
			PrinterURI:      "ipp://localhost:631/printers/test",
			Filename:        "t.pdf",
			StoredPath:      "20260921/a.pdf",
			PrintPath:       "20260921/a.pdf",
			BilledPages:     2,
			Copies:          1,
			IsColor:         false,
			AmountCents:     amount,
			Status:          OrderStatusPendingPayment,
			ExpiresAt:       "2099-01-01T00:00:00Z",
			PaperSize:       "A4",
			PaperType:       "plain",
			PrintScaling:    "fit",
			ReqPrintScaling: "fit",
			PageSet:         "all",
			ReqPageSet:      "all",
			NumberUpLayout:  "lrtb",
			PageBorder:      "none",
			MediaSource:     "auto",
			Orientation:     "portrait",
		})
		return err
	})
	return id
}

// ─── 迁移 ──────────────────────────────────────────────────────────────

// TestMigrationCreatesBillingSchema 确认 4 张新表与 users.balance_cents 都建出来了。
func TestMigrationCreatesBillingSchema(t *testing.T) {
	s := openTestStore(t)

	tables := []string{"price_rules", "print_orders", "payments", "wallet_transactions"}
	for _, table := range tables {
		n := countRows(t, s, "information_schema.tables",
			"table_schema = current_schema() AND table_name = $1", table)
		if n != 1 {
			t.Errorf("表 %s 没有被创建", table)
		}
	}

	// users 的新列。
	//   balance_cents —— 计费加的
	//   alipay_open_id —— 支付宝授权登录加的。⚠️ 它走的是 ALTER TABLE，
	//     是本仓库第一条 ALTER，所以尤其要验：新库靠 CREATE、存量库靠 ALTER，
	//     两条路径都得有这一列。
	for _, col := range []string{"balance_cents", "alipay_open_id"} {
		n := countRows(t, s, "information_schema.columns",
			"table_schema = current_schema() AND table_name = 'users' AND column_name = $1", col)
		if n != 1 {
			t.Errorf("users.%s 不存在（找到 %d 次）", col, n)
		}
	}

	// 支付宝 open_id 的【部分】唯一索引。少了它，同一个支付宝号能建出多个账号；
	// 写成普通唯一索引则会误伤大量 alipay_open_id = '' 的普通账号。
	if n := countRows(t, s, "pg_indexes",
		"schemaname = current_schema() AND tablename = 'users' AND indexname = 'idx_users_alipay_open_id'"); n != 1 {
		t.Errorf("idx_users_alipay_open_id 不存在（找到 %d 次）", n)
	}
}

// TestMigrationIsIdempotent 在同一个 schema 上反复 Open。
// 建表全是 IF NOT EXISTS，没有 schema 版本表 —— 老部署重复启动时
// 这条路径必须能走，否则用户升级一次就再也起不来。
func TestMigrationIsIdempotent(t *testing.T) {
	dsn := testSchema(t) // 三次 Open 复用同一个 schema
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		s, err := Open(ctx, dsn, 4)
		if err != nil {
			t.Fatalf("第 %d 次 Open 失败: %v", i, err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("第 %d 次 Close 失败: %v", i, err)
		}
	}
}

// TestMigrationSeedsBillingSettings 确认计费相关设置项被 seed，且默认关闭。
// 默认关闭是升级安全的基石：既有部署升级后三个打印入口的行为必须不变。
func TestMigrationSeedsBillingSettings(t *testing.T) {
	s := openTestStore(t)

	cases := []struct {
		key  string
		want string
	}{
		{SettingBillingEnabled, "0"},
		{SettingOrderTTLMinutes, "15"},
		{SettingPriceFallback, "0"},
		{SettingPaymentMethods, ""},
		{SettingSiteBaseURL, ""},
	}
	for _, c := range cases {
		var got string
		err := s.WithTx(context.Background(), true, func(tx *sql.Tx) error {
			var err error
			got, err = GetSettingString(context.Background(), tx, c.key, "<缺失>")
			return err
		})
		if err != nil {
			t.Fatalf("读设置 %s 失败: %v", c.key, err)
		}
		if got != c.want {
			t.Errorf("设置 %s = %q，期望 %q", c.key, got, c.want)
		}
	}
}

// ─── 账本 ──────────────────────────────────────────────────────────────

func TestApplyWalletDeltaTopupThenSpend(t *testing.T) {
	s := openTestStore(t)
	uid := createTestUser(t, s, "alice")

	var afterTopup int64
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		afterTopup, err = ApplyWalletDelta(ctx, tx, WalletDeltaInput{
			UserID: uid, Kind: WalletKindTopup, AmountCents: 1000,
			IdempotencyKey: WalletKeyPay("CW001"),
		})
		return err
	})
	if afterTopup != 1000 {
		t.Fatalf("充值后余额 = %d，期望 1000", afterTopup)
	}

	var afterSpend int64
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		afterSpend, err = ApplyWalletDelta(ctx, tx, WalletDeltaInput{
			UserID: uid, Kind: WalletKindSpend, AmountCents: -250,
			IdempotencyKey: WalletKeySpendOrder(7),
		})
		return err
	})
	if afterSpend != 750 {
		t.Fatalf("扣款后余额 = %d，期望 750", afterSpend)
	}
	if got := balanceOf(t, s, uid); got != 750 {
		t.Errorf("库里余额 = %d，期望 750", got)
	}
	if n := countRows(t, s, "wallet_transactions", "user_id = $1", uid); n != 2 {
		t.Errorf("流水条数 = %d，期望 2", n)
	}
}

// TestApplyWalletDeltaInsufficientBalance 余额不足时必须整体拒绝：
// 既不扣款、也不留流水。出账靠条件 UPDATE 一步完成「够不够」与「扣多少」，
// 这条测试锁的就是那个 WHERE 条件。
func TestApplyWalletDeltaInsufficientBalance(t *testing.T) {
	s := openTestStore(t)
	uid := createTestUser(t, s, "bob")

	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		_, err := ApplyWalletDelta(ctx, tx, WalletDeltaInput{
			UserID: uid, Kind: WalletKindTopup, AmountCents: 100,
			IdempotencyKey: WalletKeyPay("CW002"),
		})
		return err
	})

	err := s.WithTx(context.Background(), false, func(tx *sql.Tx) error {
		_, err := ApplyWalletDelta(context.Background(), tx, WalletDeltaInput{
			UserID: uid, Kind: WalletKindSpend, AmountCents: -101,
			IdempotencyKey: WalletKeySpendOrder(8),
		})
		return err
	})
	if !errors.Is(err, ErrInsufficientBalance) {
		t.Fatalf("期望 ErrInsufficientBalance，实际 %v", err)
	}

	if got := balanceOf(t, s, uid); got != 100 {
		t.Errorf("失败后余额 = %d，期望仍为 100（不能扣掉）", got)
	}
	if n := countRows(t, s, "wallet_transactions", "kind = $1", WalletKindSpend); n != 0 {
		t.Errorf("失败后残留了 %d 条扣款流水，期望 0", n)
	}
}

// TestApplyWalletDeltaAllowsSpendingToExactlyZero 余额正好花光是合法的边界。
func TestApplyWalletDeltaAllowsSpendingToExactlyZero(t *testing.T) {
	s := openTestStore(t)
	uid := createTestUser(t, s, "carol")

	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := ApplyWalletDelta(ctx, tx, WalletDeltaInput{
			UserID: uid, Kind: WalletKindTopup, AmountCents: 100,
			IdempotencyKey: WalletKeyPay("CW003"),
		}); err != nil {
			return err
		}
		got, err := ApplyWalletDelta(ctx, tx, WalletDeltaInput{
			UserID: uid, Kind: WalletKindSpend, AmountCents: -100,
			IdempotencyKey: WalletKeySpendOrder(9),
		})
		if err != nil {
			return err
		}
		if got != 0 {
			t.Errorf("花光后余额 = %d，期望 0", got)
		}
		return nil
	})
}

// TestApplyWalletDeltaIsIdempotent 同一个幂等键重复入账只应生效一次。
//
// 这是支付回调重复投递（支付宝 25 小时内重试 8 次）时的最后一道闸：
// 第二次撞唯一索引 → 整事务回滚 → 余额不会被加两次，
// 且返回 ErrDuplicateTransaction 让调用方当成功处理。
func TestApplyWalletDeltaIsIdempotent(t *testing.T) {
	s := openTestStore(t)
	uid := createTestUser(t, s, "dave")
	key := WalletKeyPay("CW004")

	for i := 1; i <= 5; i++ {
		err := s.WithTx(context.Background(), false, func(tx *sql.Tx) error {
			_, err := ApplyWalletDelta(context.Background(), tx, WalletDeltaInput{
				UserID: uid, Kind: WalletKindTopup, AmountCents: 500,
				IdempotencyKey: key,
			})
			return err
		})
		if i == 1 {
			if err != nil {
				t.Fatalf("首次入账失败: %v", err)
			}
			continue
		}
		if !errors.Is(err, ErrDuplicateTransaction) {
			t.Fatalf("第 %d 次期望 ErrDuplicateTransaction，实际 %v", i, err)
		}
	}

	if got := balanceOf(t, s, uid); got != 500 {
		t.Errorf("余额 = %d，期望 500（重复入账只能生效一次）", got)
	}
	if n := countRows(t, s, "wallet_transactions", "idempotency_key = $1", key); n != 1 {
		t.Errorf("同键流水条数 = %d，期望 1", n)
	}
}

// TestApplyWalletDeltaRejectsBadInput 幂等键缺失与金额为 0 都必须被拒。
// 没有幂等键的入账在重试时无法自证，等于给对账埋雷。
func TestApplyWalletDeltaRejectsBadInput(t *testing.T) {
	s := openTestStore(t)
	uid := createTestUser(t, s, "erin")

	cases := []struct {
		name string
		in   WalletDeltaInput
	}{
		{"缺幂等键", WalletDeltaInput{UserID: uid, Kind: WalletKindTopup, AmountCents: 100}},
		{"金额为 0", WalletDeltaInput{UserID: uid, Kind: WalletKindTopup, IdempotencyKey: "x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := s.WithTx(context.Background(), false, func(tx *sql.Tx) error {
				_, err := ApplyWalletDelta(context.Background(), tx, c.in)
				return err
			})
			if err == nil {
				t.Fatal("期望报错，实际通过")
			}
		})
	}
}

// TestHasWalletTransactions 是删除用户守卫的依据。
func TestHasWalletTransactions(t *testing.T) {
	s := openTestStore(t)
	uid := createTestUser(t, s, "frank")

	var before bool
	err := s.WithTx(context.Background(), true, func(tx *sql.Tx) error {
		var err error
		before, err = HasWalletTransactions(context.Background(), tx, uid)
		return err
	})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if before {
		t.Error("新用户不该有流水")
	}

	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		_, err := ApplyWalletDelta(ctx, tx, WalletDeltaInput{
			UserID: uid, Kind: WalletKindTopup, AmountCents: 10,
			IdempotencyKey: WalletKeyPay("CW005"),
		})
		return err
	})

	var after bool
	err = s.WithTx(context.Background(), true, func(tx *sql.Tx) error {
		var err error
		after, err = HasWalletTransactions(context.Background(), tx, uid)
		return err
	})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if !after {
		t.Error("入账后应当检测到流水")
	}
}

// ─── 订单状态机 ────────────────────────────────────────────────────────

// TestClaimOrderForPrintOnceOnly 是「付一次只印一次」的核心保证。
//
// ClaimOrderForPrint 把 paid 原子迁移到 printing 并返回是否成功。
// 支付回调、补偿扫描、前端重试三条路可能同时到达同一条订单，
// 只有拿到 true 的那个才有资格发起 IPP 提交。
func TestClaimOrderForPrintOnceOnly(t *testing.T) {
	s := openTestStore(t)
	uid := createTestUser(t, s, "grace")
	orderID := createTestOrder(t, s, uid, "B1", 80)

	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		ok, err := MarkOrderPaid(ctx, tx, orderID, PaymentProviderBalance, 1)
		if err != nil {
			return err
		}
		if !ok {
			t.Error("pending_payment → paid 应当成功")
		}
		return nil
	})

	// 并发模拟：顺序调用两次，第二次必须拿到 false
	for i := 1; i <= 3; i++ {
		var claimed bool
		err := s.WithTx(context.Background(), false, func(tx *sql.Tx) error {
			var err error
			claimed, err = ClaimOrderForPrint(context.Background(), tx, orderID)
			return err
		})
		if err != nil {
			t.Fatalf("第 %d 次 claim 失败: %v", i, err)
		}
		if want := i == 1; claimed != want {
			t.Fatalf("第 %d 次 claim = %v，期望 %v", i, claimed, want)
		}
	}
}

// TestMarkOrderPaidFromExpired 用户扫码后订单过期、钱才到账：
// 必须能从 expired 推进到 paid（付了钱就该拿到东西）。
func TestMarkOrderPaidFromExpired(t *testing.T) {
	s := openTestStore(t)
	uid := createTestUser(t, s, "heidi")
	orderID := createTestOrder(t, s, uid, "B2", 80)

	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		_, err := MarkPendingOrdersExpired(ctx, tx, "B2")
		return err
	})

	var ok bool
	err := s.WithTx(context.Background(), false, func(tx *sql.Tx) error {
		var err error
		ok, err = MarkOrderPaid(context.Background(), tx, orderID, PaymentProviderAlipay, 3)
		return err
	})
	if err != nil {
		t.Fatalf("过期订单转 paid 失败: %v", err)
	}
	if !ok {
		t.Error("expired → paid 应当被允许")
	}

	order := getOrder(t, s, orderID)
	if order.Status != OrderStatusPaid {
		t.Errorf("状态 = %s，期望 paid", order.Status)
	}
}

// TestMarkPendingOrdersExpiredIsAtomic 批量过期必须整批一起，
// 不能出现「同一批里一半过期一半还能付」。
func TestMarkPendingOrdersExpiredIsAtomic(t *testing.T) {
	s := openTestStore(t)
	uid := createTestUser(t, s, "ivan")
	for i := 0; i < 3; i++ {
		createTestOrder(t, s, uid, "BATCH-X", 80)
	}

	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		_, err := MarkPendingOrdersExpired(ctx, tx, "BATCH-X")
		return err
	})

	if n := countRows(t, s, "print_orders", "batch_id = $1 AND status = $2", "BATCH-X", OrderStatusExpired); n != 3 {
		t.Errorf("过期订单数 = %d，期望 3", n)
	}
	if n := countRows(t, s, "print_orders", "batch_id = $1 AND status = $2", "BATCH-X", OrderStatusPendingPayment); n != 0 {
		t.Errorf("残留待支付订单 %d 条，期望 0", n)
	}
}

// TestMarkOrderRefundedAccumulates 退款累加 refunded_cents 并把订单置为 refunded。
func TestMarkOrderRefundedAccumulates(t *testing.T) {
	s := openTestStore(t)
	uid := createTestUser(t, s, "judy")
	orderID := createTestOrder(t, s, uid, "B3", 120)

	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := MarkOrderPaid(ctx, tx, orderID, PaymentProviderBalance, 1); err != nil {
			return err
		}
		if _, err := ClaimOrderForPrint(ctx, tx, orderID); err != nil {
			return err
		}
		if _, err := MarkOrderPrinted(ctx, tx, orderID, "job-1", 42); err != nil {
			return err
		}
		ok, err := MarkOrderRefunded(ctx, tx, orderID, 120)
		if err != nil {
			return err
		}
		if !ok {
			t.Error("printed → refunded 应当成功")
		}
		return nil
	})

	order := getOrder(t, s, orderID)
	if order.Status != OrderStatusRefunded {
		t.Errorf("状态 = %s，期望 refunded", order.Status)
	}
	if order.RefundedCents != 120 {
		t.Errorf("refunded_cents = %d，期望 120", order.RefundedCents)
	}
}

// ─── 文件保护 ──────────────────────────────────────────────────────────

// TestListActiveOrderFilesOnlyUnfinished 只有未终结订单才占用文件保护名额。
// maintenance 按 print_jobs 的保留期删文件，完全不知道订单存在 ——
// 这份名单就是它唯一的依据。
func TestListActiveOrderFilesOnlyUnfinished(t *testing.T) {
	s := openTestStore(t)
	uid := createTestUser(t, s, "kate")

	done := createTestOrder(t, s, uid, "B4", 80)
	createTestOrder(t, s, uid, "B5", 80)

	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := MarkOrderPaid(ctx, tx, done, PaymentProviderBalance, 1); err != nil {
			return err
		}
		if _, err := ClaimOrderForPrint(ctx, tx, done); err != nil {
			return err
		}
		_, err := MarkOrderPrinted(ctx, tx, done, "job-2", 0)
		return err
	})

	var refs []OrderFileRef
	err := s.WithTx(context.Background(), true, func(tx *sql.Tx) error {
		var err error
		refs, err = ListActiveOrderFiles(context.Background(), tx)
		return err
	})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("受保护文件数 = %d，期望 1（只有那条 pending_payment 的）", len(refs))
	}
	if refs[0].StoredPath != "20260921/a.pdf" {
		t.Errorf("受保护路径 = %q", refs[0].StoredPath)
	}
}

// ─── 价目表 ────────────────────────────────────────────────────────────

// TestReplacePriceRulesReplacesWholesale 全量替换语义：
// 第二次调用必须把第一次的规则完全覆盖掉，不能有残留。
func TestReplacePriceRulesReplacesWholesale(t *testing.T) {
	s := openTestStore(t)

	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		return ReplacePriceRules(ctx, tx, []PriceRule{
			{IsColor: false, PaperSize: "A4", PaperType: "plain", UnitPriceCents: 20},
			{IsColor: true, PaperSize: "A4", PaperType: "plain", UnitPriceCents: 100},
		})
	})
	if n := countRows(t, s, "price_rules", ""); n != 2 {
		t.Fatalf("规则数 = %d，期望 2", n)
	}

	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		return ReplacePriceRules(ctx, tx, []PriceRule{
			{IsColor: false, PaperSize: "A3", PaperType: "photo", UnitPriceCents: 500},
		})
	})

	var rules []PriceRule
	err := s.WithTx(context.Background(), true, func(tx *sql.Tx) error {
		var err error
		rules, err = ListPriceRules(context.Background(), tx)
		return err
	})
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("替换后规则数 = %d，期望 1", len(rules))
	}
	if rules[0].PaperSize != "A3" || rules[0].UnitPriceCents != 500 || rules[0].IsColor {
		t.Errorf("替换后规则内容不对: %+v", rules[0])
	}
}

// TestReplacePriceRulesRejectsDuplicateDimension 维度重复必须被唯一索引挡住。
// handler 层会先做友好校验，这里是最后一道防线。
func TestReplacePriceRulesRejectsDuplicateDimension(t *testing.T) {
	s := openTestStore(t)

	err := s.WithTx(context.Background(), false, func(tx *sql.Tx) error {
		return ReplacePriceRules(context.Background(), tx, []PriceRule{
			{IsColor: false, PaperSize: "A4", PaperType: "plain", UnitPriceCents: 20},
			{IsColor: false, PaperSize: "A4", PaperType: "plain", UnitPriceCents: 30},
		})
	})
	if err == nil {
		t.Fatal("重复维度应当报错")
	}

	// 整事务回滚，不能留下半张表
	if n := countRows(t, s, "price_rules", ""); n != 0 {
		t.Errorf("失败后残留 %d 条规则，期望 0（应当整体回滚）", n)
	}
}

// ─── 辅助 ──────────────────────────────────────────────────────────────

func getOrder(t *testing.T, s *Store, id int64) PrintOrder {
	t.Helper()
	var order PrintOrder
	err := s.WithTx(context.Background(), true, func(tx *sql.Tx) error {
		var err error
		order, err = GetOrderByID(context.Background(), tx, id)
		return err
	})
	if err != nil {
		t.Fatalf("读订单失败: %v", err)
	}
	return order
}
