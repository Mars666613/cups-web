package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// 刷卡取件（拉式打印）的数据层测试。
// 需要 PostgreSQL，未设 TEST_DATABASE_URL 时跳过（同 billing_test.go）。

// createHeldOrder 建一条【待取件】订单，返回 id。
func createHeldOrder(t *testing.T, s *Store, userID int64, printerURI, storedPath string) int64 {
	t.Helper()
	var id int64
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		id, err = InsertPrintOrder(ctx, tx, &PrintOrder{
			UserID:          userID,
			Source:          OrderSourcePull,
			PrinterURI:      printerURI,
			Filename:        "t.pdf",
			StoredPath:      storedPath,
			PrintPath:       storedPath,
			PrintMime:       "application/pdf",
			Copies:          1,
			Status:          OrderStatusHeld,
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

func createTestTerminal(t *testing.T, s *Store, token, printerURI string) {
	t.Helper()
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		return CreateKioskTerminal(ctx, tx, KioskTerminal{
			Token:      token,
			Name:       "测试终端",
			PrinterURI: printerURI,
			Enabled:    true,
		})
	})
}

// ─── 迁移 ──────────────────────────────────────────────────────────────

func TestMigrationCreatesPullSchema(t *testing.T) {
	s := openTestStore(t)

	if n := countRows(t, s, "information_schema.tables",
		"table_schema = current_schema() AND table_name = 'kiosk_terminals'"); n != 1 {
		t.Errorf("kiosk_terminals 表没有被创建（找到 %d 次）", n)
	}
	if n := countRows(t, s, "information_schema.columns",
		"table_schema = current_schema() AND table_name = 'users' AND column_name = 'card_no'"); n != 1 {
		t.Errorf("users.card_no 不存在（找到 %d 次）", n)
	}
	if n := countRows(t, s, "pg_indexes",
		"schemaname = current_schema() AND tablename = 'users' AND indexname = 'idx_users_card_no'"); n != 1 {
		t.Errorf("idx_users_card_no 不存在（找到 %d 次）", n)
	}
}

// ─── 工牌号 ────────────────────────────────────────────────────────────

func TestSetUserCardNoUniqueness(t *testing.T) {
	s := openTestStore(t)
	a := createTestUser(t, s, "alice")
	b := createTestUser(t, s, "bob")

	set := func(userID int64, card string) error {
		return s.WithTx(context.Background(), false, func(tx *sql.Tx) error {
			return SetUserCardNo(context.Background(), tx, userID, card)
		})
	}

	if err := set(a, "1234567890"); err != nil {
		t.Fatalf("第一次绑卡应当成功: %v", err)
	}
	// 同一张卡绑给别人 → 必须被拒，且是【可识别】的哨兵错误。
	// 上游要靠它去查出占用者是谁，提示「这张卡已绑定给 XXX」——
	// 把 ErrCardTaken 降级成普通错误，管理员就只能看到一句「绑定失败」。
	if err := set(b, "1234567890"); !errors.Is(err, ErrCardTaken) {
		t.Errorf("同一张卡绑给第二个人应当返回 ErrCardTaken，实际: %v", err)
	}

	// 解绑之后那张卡可以被别人绑走 —— 换卡就是「先解绑、再绑新的」。
	if err := set(a, ""); err != nil {
		t.Fatalf("解绑应当成功: %v", err)
	}
	if err := set(b, "1234567890"); err != nil {
		t.Errorf("解绑后这张卡应当能绑给别人: %v", err)
	}
}

// 空串是「没绑卡」的哨兵，不参与那条部分唯一索引 ——
// 否则第二个没绑卡的用户就建不出来了。这与 alipay_open_id 是同一个模式。
func TestEmptyCardNoDoesNotCollide(t *testing.T) {
	s := openTestStore(t)
	for i, name := range []string{"u1", "u2", "u3"} {
		id := createTestUser(t, s, name)
		if err := s.WithTx(context.Background(), false, func(tx *sql.Tx) error {
			return SetUserCardNo(context.Background(), tx, id, "")
		}); err != nil {
			t.Fatalf("第 %d 个用户解绑（置空）不该冲突: %v", i+1, err)
		}
	}
}

// 归一规则本身。PC/SC 读卡器（ACR122U 等）吐十六进制 UID，而那个串
// 会被管理员从终端上抄进管理界面 —— 大小写与分隔符的差异是必然的。
func TestNormalizeCardNo(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"04A23B1C", "04A23B1C"},
		{"04a23b1c", "04A23B1C"},     // 手抄小写
		{"04:A2:3B:1C", "04A23B1C"},  // 冒号分隔（ACR122U 文档常见写法）
		{"04-a2-3b-1c", "04A23B1C"},  // 连字符 + 小写
		{" 04A2 3B1C\n", "04A23B1C"}, // 空白
		{"0011223344", "0011223344"}, // 🚫 前导零必须保留：去掉就换了张卡
		{"", ""},
	}
	for _, c := range cases {
		if got := NormalizeCardNo(c.in); got != c.want {
			t.Errorf("NormalizeCardNo(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// 绑定与刷卡只要过同一个归一，写法不同也能对上 —— 这正是加它的理由。
func TestCardNoMatchingIsFormatInsensitive(t *testing.T) {
	s := openTestStore(t)
	alice := createTestUser(t, s, "alice")

	// 管理员手抄小写 + 冒号分隔。
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		return SetUserCardNo(ctx, tx, alice, "04:a2:3b:1c")
	})

	// 读卡器吐出来的是大写无分隔。
	var found User
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		found, err = GetUserByCardNo(ctx, tx, "04A23B1C")
		return err
	})
	if found.ID != alice {
		t.Errorf("写法不同但同一张卡应当命中同一个人：拿到 id=%d，期望 %d", found.ID, alice)
	}
}

func TestGetUserByCardNoRejectsEmpty(t *testing.T) {
	s := openTestStore(t)
	createTestUser(t, s, "alice")

	err := s.WithTx(context.Background(), true, func(tx *sql.Tx) error {
		_, err := GetUserByCardNo(context.Background(), tx, "")
		return err
	})
	// 空串必须返回 ErrNoRows，而不是「匹配到某个没绑卡的人」。
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("空卡号应当返回 sql.ErrNoRows，实际: %v", err)
	}
}

// ─── 取件终端 ──────────────────────────────────────────────────────────

func TestPrinterURIsWithKioskIgnoresDisabled(t *testing.T) {
	s := openTestStore(t)
	createTestTerminal(t, s, "tok-a", "ipp://p/a")
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		return CreateKioskTerminal(ctx, tx, KioskTerminal{
			Token: "tok-b", Name: "停用的", PrinterURI: "ipp://p/b", Enabled: false,
		})
	})

	var set map[string]bool
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		set, err = PrinterURIsWithKiosk(ctx, tx)
		return err
	})

	if !set["ipp://p/a"] {
		t.Error("启用的终端所在打印机应当出现在集合里")
	}
	// ⚠️ 停用的终端不该让打印机在提交页继续露面 —— 那样员工会选到一台
	// 实际取不出件的机器。
	if set["ipp://p/b"] {
		t.Error("停用的终端不该让打印机算作「可取件」")
	}
}

// ─── 领取 ──────────────────────────────────────────────────────────────

// 刷卡的核心保证：「刷一次只打一次」。
// 第二次刷卡必须匹配 0 行 —— 否则员工刷两下就打两份。
func TestClaimHeldOrdersForPrinterOnceOnly(t *testing.T) {
	s := openTestStore(t)
	user := createTestUser(t, s, "alice")
	const printer = "ipp://p/a"
	createHeldOrder(t, s, user, printer, "20260921/a.pdf")
	createHeldOrder(t, s, user, printer, "20260921/b.pdf")

	claim := func() []int64 {
		var ids []int64
		mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
			var err error
			ids, err = ClaimHeldOrdersForPrinter(ctx, tx, user, printer)
			return err
		})
		return ids
	}

	first := claim()
	if len(first) != 2 {
		t.Fatalf("第一次领取应当拿到 2 条，实际 %d 条", len(first))
	}
	// 顺序按 id 升序 —— 出纸顺序要跟用户提交的先后一致。
	if first[0] > first[1] {
		t.Errorf("领取结果应当按 id 升序，实际 %v", first)
	}

	second := claim()
	if len(second) != 0 {
		t.Errorf("第二次领取必须什么都不拿到（否则会重复出纸），实际 %v", second)
	}
}

// 领取的边界：只领【这个人 + 这台打印机】的。
// 少了任一维度就会串单 —— 别人来刷卡，打出来的是你的文件。
func TestClaimHeldOrdersForPrinterIsScoped(t *testing.T) {
	s := openTestStore(t)
	alice := createTestUser(t, s, "alice")
	bob := createTestUser(t, s, "bob")

	createHeldOrder(t, s, alice, "ipp://p/a", "20260921/mine.pdf")
	createHeldOrder(t, s, alice, "ipp://p/b", "20260921/other-printer.pdf")
	createHeldOrder(t, s, bob, "ipp://p/a", "20260921/other-user.pdf")

	var ids []int64
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		ids, err = ClaimHeldOrdersForPrinter(ctx, tx, alice, "ipp://p/a")
		return err
	})

	if len(ids) != 1 {
		t.Fatalf("只应当领到「alice 在 a 机」的那 1 条，实际 %d 条", len(ids))
	}
	// 比对文件路径而不是 filename：createHeldOrder 给所有单子都用同一个文件名，
	// 拿它做判据的话这条测试永远不会失败 —— 也就是测了个寂寞。
	var storedPath string
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT stored_path FROM print_orders WHERE id = $1`, ids[0]).Scan(&storedPath)
	})
	if storedPath != "20260921/mine.pdf" {
		t.Errorf("领错了单：拿到的是 %q", storedPath)
	}
}

// ─── 回退 ──────────────────────────────────────────────────────────────

func TestRevertOrderToHeld(t *testing.T) {
	s := openTestStore(t)
	user := createTestUser(t, s, "alice")
	const printer = "ipp://p/a"
	createHeldOrder(t, s, user, printer, "20260921/a.pdf")

	var id int64
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		ids, err := ClaimHeldOrdersForPrinter(ctx, tx, user, printer)
		if err != nil {
			return err
		}
		id = ids[0]
		return nil
	})

	revert := func() bool {
		var ok bool
		mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
			var err error
			ok, err = RevertOrderToHeld(ctx, tx, id, "打印机离线")
			return err
		})
		return ok
	}

	if !revert() {
		t.Fatal("第一次回退应当成功")
	}
	// 幂等：同一单回退两次，第二次什么都不做。
	if revert() {
		t.Error("重复回退不该再次生效")
	}

	// 回退之后应当可以被再次领取 —— 这就是「员工原地重刷」。
	var again []int64
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		again, err = ClaimHeldOrdersForPrinter(ctx, tx, user, printer)
		return err
	})
	if len(again) != 1 {
		t.Errorf("回退后应当能被重新领取，实际领到 %d 条", len(again))
	}
}

// 🚨 这条是【重复出纸】的保险。
//
// 任务一旦真的进了 CUPS（job_id 有值），纸有没有出来就存疑了。
// 这时再退回 held，员工下次刷卡就会【再打一份】。
// 所以回退必须带 `job_id = ”` 的条件。
func TestRevertOrderToHeldRefusesAfterSubmit(t *testing.T) {
	s := openTestStore(t)
	user := createTestUser(t, s, "alice")
	const printer = "ipp://p/a"
	id := createHeldOrder(t, s, user, printer, "20260921/a.pdf")

	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		_, err := ClaimHeldOrdersForPrinter(ctx, tx, user, printer)
		return err
	})
	// 模拟送印成功：回写了 job_id。
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		return SetOrderSubmitted(ctx, tx, id, "ipp://localhost:631/jobs/7", 0)
	})

	var reverted bool
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		reverted, err = RevertOrderToHeld(ctx, tx, id, "不该生效")
		return err
	})
	if reverted {
		t.Error("任务已经提交到 CUPS，绝不能再退回待取件（会让员工下次刷卡重复出纸）")
	}
}

// 提交成功时要把 error 列清空 —— 包括索赔取的「已领取」标记。
// 留着的话 ListOrphanPullOrders 会一直把它当成孤儿。
func TestSetOrderSubmittedClearsError(t *testing.T) {
	s := openTestStore(t)
	user := createTestUser(t, s, "alice")
	const printer = "ipp://p/a"
	id := createHeldOrder(t, s, user, printer, "20260921/a.pdf")

	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		_, err := ClaimHeldOrdersForPrinter(ctx, tx, user, printer)
		return err
	})
	var marked string
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT error FROM print_orders WHERE id = $1`, id).Scan(&marked)
	})
	if marked != PullClaimMarker {
		t.Fatalf("领取后 error 应当是领取标记，实际 %q", marked)
	}

	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		return SetOrderSubmitted(ctx, tx, id, "ipp://localhost:631/jobs/7", 0)
	})
	var after string
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT error FROM print_orders WHERE id = $1`, id).Scan(&after)
	})
	if after != "" {
		t.Errorf("提交成功后 error 应当被清空，实际 %q", after)
	}
}

// ─── 过期 ──────────────────────────────────────────────────────────────

func TestMarkHeldOrdersExpiredIdempotent(t *testing.T) {
	s := openTestStore(t)
	user := createTestUser(t, s, "alice")
	id := createHeldOrder(t, s, user, "ipp://p/a", "20260921/a.pdf")

	expire := func() bool {
		var ok bool
		mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
			var err error
			ok, err = MarkHeldOrdersExpired(ctx, tx, id, "超过取件时限")
			return err
		})
		return ok
	}

	if !expire() {
		t.Fatal("第一次过期应当成功")
	}
	if expire() {
		t.Error("重复过期不该再次生效")
	}
}

// 已经领走的任务不该再被过期任务扫到 —— 否则会去删一份正在打印的文件。
func TestListExpiredHeldOrdersSkipsClaimed(t *testing.T) {
	s := openTestStore(t)
	user := createTestUser(t, s, "alice")
	const printer = "ipp://p/a"
	createHeldOrder(t, s, user, printer, "20260921/a.pdf")
	createHeldOrder(t, s, user, printer, "20260921/b.pdf")

	// 领走一条。
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		_, err := ClaimHeldOrdersForPrinter(ctx, tx, user, printer)
		return err
	})

	var ids []int64
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		ids, err = ListExpiredHeldOrders(ctx, tx, "2099-01-01T00:00:00Z", 10)
		return err
	})
	if len(ids) != 0 {
		t.Errorf("两条都已被领取，过期扫描不该再捞到任何一条，实际 %d 条", len(ids))
	}
}

// ─── 孤儿恢复 ──────────────────────────────────────────────────────────

// 领取之后进程挂掉（还没送印）的订单要有回收路径。
//
// 判据必须是【那个标记】而不是「job_id 为空」—— 后者会把
// 「送印成功但任务号没回写」的订单也捞进来，回退后就会重复出纸。
func TestListOrphanPullOrdersMatchesOnlyClaimMarker(t *testing.T) {
	s := openTestStore(t)
	user := createTestUser(t, s, "alice")
	const printer = "ipp://p/a"

	// ① 领了但没送印 —— 这才是真孤儿，应当被捞到。
	orphan := createHeldOrder(t, s, user, printer, "20260921/orphan.pdf")
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		_, err := ClaimHeldOrdersForPrinter(ctx, tx, user, printer)
		return err
	})

	// ② 另一个用户：领了、送印成功了，只差任务号没回写（job_id 仍为空）。
	//    形状与 ① 一样，必须【不】被捞到。
	user2 := createTestUser(t, s, "bob")
	createHeldOrder(t, s, user2, printer, "20260921/submitted.pdf")
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		ids, err := ClaimHeldOrdersForPrinter(ctx, tx, user2, printer)
		if err != nil {
			return err
		}
		// 模拟「已送出、但回写任务号失败」—— error 换成送印失败的措辞，
		// 于是它不再带着领取标记，形状却与真孤儿一样（printing + job_id 为空）。
		_, err = tx.ExecContext(ctx, `UPDATE print_orders SET error = $1 WHERE id = $2`,
			"打印任务号记录失败", ids[0])
		return err
	})

	var ids []int64
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		ids, err = ListOrphanPullOrders(ctx, tx, "2099-01-01T00:00:00Z", 10)
		return err
	})

	if len(ids) != 1 || ids[0] != orphan {
		t.Errorf("只应当捞到真孤儿 #%d，实际 %v", orphan, ids)
	}
}

// ─── 文件保护（本方案最危险的一处）───────────────────────────────────

// 🚨🚨 这条测试是【整个取件模式最容易出人命的地方】。
//
// maintenance 每小时按 print_jobs 的保留期删文件，它完全不知道订单的存在，
// 只认 ListActiveOrderFiles 给的这份保护名单。名单里少了 held 的话，
// 表现是：员工刷卡时提示「待打印文件缺失」—— 而根因是一小时前的一次清理。
//
// 🚫 不要因为「OrderStatusesActive 里加了 held」就以为这里也覆盖到了 ——
// 那个变量是【死代码】，全仓没有任何地方读它。
func TestListActiveOrderFilesIncludesHeld(t *testing.T) {
	s := openTestStore(t)
	user := createTestUser(t, s, "alice")

	createHeldOrder(t, s, user, "ipp://p/a", "20260921/held.pdf")
	// 再放一条终态订单：它【不】该出现在保护名单里（否则文件永不回收）。
	terminalID := createHeldOrder(t, s, user, "ipp://p/a", "20260921/done.pdf")
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		_, err := MarkHeldOrdersExpired(ctx, tx, terminalID, "已作废")
		return err
	})

	var refs []OrderFileRef
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		refs, err = ListActiveOrderFiles(ctx, tx)
		return err
	})

	paths := map[string]bool{}
	for _, r := range refs {
		paths[r.StoredPath] = true
	}
	if !paths["20260921/held.pdf"] {
		t.Errorf("❌ held 订单的文件【没有】被保护。员工刷卡时会提示文件缺失 —— "+
			"检查 ListActiveOrderFiles 的 status IN 列表里有没有 OrderStatusHeld。"+
			"（当前保护名单：%v）", refs)
	}
	if paths["20260921/done.pdf"] {
		t.Error("终态订单的文件不该在保护名单里，否则永远不会被回收")
	}
}
