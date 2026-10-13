package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// 工牌绑定窗口（「点绑卡 → 到终端刷卡」）的数据层测试。
// 需要 PostgreSQL，未设 TEST_DATABASE_URL 时跳过。

func openBind(t *testing.T, s *Store, userID int64, expiresAt string) int64 {
	t.Helper()
	var id int64
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		id, err = CreateCardBindSession(ctx, tx, userID, "admin", expiresAt)
		return err
	})
	return id
}

func future() string { return time.Now().UTC().Add(2 * time.Minute).Format(time.RFC3339) }
func past() string   { return time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339) }

// 🚨 全局单飞。两个窗口同时开着的话，一次刷卡绑给谁是**不确定**的 ——
// 而绑错的后果是「A 的卡能取到 B 的打印件」，属于隐私事故。
func TestCardBindSessionIsSingleFlight(t *testing.T) {
	s := openTestStore(t)
	a := createTestUser(t, s, "alice")
	b := createTestUser(t, s, "bob")

	openBind(t, s, a, future())

	err := s.WithTx(context.Background(), false, func(tx *sql.Tx) error {
		_, err := CreateCardBindSession(context.Background(), tx, b, "admin", future())
		return err
	})
	if !errors.Is(err, ErrBindSessionActive) {
		t.Errorf("已经有一个窗口开着时应当返回 ErrBindSessionActive，实际: %v", err)
	}
}

// 开新窗口前要先把过期没收尾的那些清掉，否则那条部分唯一索引会把新窗口
// 永远挡在门外 —— 表现是「点绑卡没反应」，且要重启才能恢复。
func TestCardBindSessionExpiredDoesNotBlock(t *testing.T) {
	s := openTestStore(t)
	a := createTestUser(t, s, "alice")
	b := createTestUser(t, s, "bob")

	openBind(t, s, a, past())

	if err := s.WithTx(context.Background(), false, func(tx *sql.Tx) error {
		_, err := CreateCardBindSession(context.Background(), tx, b, "admin", future())
		return err
	}); err != nil {
		t.Errorf("过期窗口不该挡住新窗口，实际: %v", err)
	}
}

// 收尾是幂等的：两次刷卡撞在一起时，只有一个能拿到 true，
// 也就只有一个有资格去改 users.card_no。
func TestCompleteCardBindSessionOnceOnly(t *testing.T) {
	s := openTestStore(t)
	a := createTestUser(t, s, "alice")
	id := openBind(t, s, a, future())

	complete := func(card string) bool {
		var ok bool
		mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
			var err error
			ok, err = CompleteCardBindSession(ctx, tx, id, card)
			return err
		})
		return ok
	}

	if !complete("04A23B1C") {
		t.Fatal("第一次收尾应当成功")
	}
	if complete("04A23B1C") {
		t.Error("重复收尾不该再次成功（那会让一次刷卡改两次 users.card_no）")
	}
}

func TestActiveCardBindSessionIgnoresFinishedAndExpired(t *testing.T) {
	s := openTestStore(t)
	a := createTestUser(t, s, "alice")

	// 过期的
	openBind(t, s, a, past())
	var got CardBindSession
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		got, err = ActiveCardBindSession(ctx, tx)
		return err
	})
	if got.ID != 0 {
		t.Errorf("过期窗口不该算活跃，实际拿到 id=%d", got.ID)
	}

	// 已收尾的
	id := openBind(t, s, a, future())
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		_, err := CompleteCardBindSession(ctx, tx, id, "04A23B1C")
		return err
	})
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		got, err = ActiveCardBindSession(ctx, tx)
		return err
	})
	if got.ID != 0 {
		t.Errorf("已收尾的窗口不该算活跃，实际拿到 id=%d", got.ID)
	}
}

// 「没有人在绑卡」是常态，不是一个错误。
func TestActiveCardBindSessionEmptyIsNotError(t *testing.T) {
	s := openTestStore(t)
	var got CardBindSession
	err := s.WithTx(context.Background(), true, func(tx *sql.Tx) error {
		var e error
		got, e = ActiveCardBindSession(context.Background(), tx)
		return e
	})
	if err != nil {
		t.Fatalf("没有活跃窗口时不该报错: %v", err)
	}
	if got.ID != 0 {
		t.Errorf("应当返回零值，实际 id=%d", got.ID)
	}
}

// 记错误【不】关窗口：管理员换一张卡就能接着刷，不必回电脑前重新点一次。
func TestRecordCardBindErrorKeepsWindowOpen(t *testing.T) {
	s := openTestStore(t)
	a := createTestUser(t, s, "alice")
	id := openBind(t, s, a, future())

	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		return RecordCardBindError(ctx, tx, id, "这张卡已经绑定给「bob」了")
	})

	var got CardBindSession
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		got, err = GetCardBindSession(ctx, tx, id)
		return err
	})
	if got.Done {
		t.Error("记一次失败不该把窗口关掉（那样管理员得回电脑前重新点一次）")
	}
	if got.Error == "" {
		t.Error("失败原因应当被记下来，让管理员的浏览器能显示")
	}

	// 换一张卡仍然能绑定。
	var ok bool
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		ok, err = CompleteCardBindSession(ctx, tx, id, "OTHER")
		return err
	})
	if !ok {
		t.Error("窗口还开着，换一张卡应当能绑上")
	}
}

// GetCardBindSession 把「已过期」直接当成「已结束」返回 ——
// 判定收在这一处，调用方就不用各自比对时间。
func TestGetCardBindSessionMarksExpiredDone(t *testing.T) {
	s := openTestStore(t)
	a := createTestUser(t, s, "alice")
	id := openBind(t, s, a, past())

	var got CardBindSession
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		got, err = GetCardBindSession(ctx, tx, id)
		return err
	})
	if !got.Done {
		t.Error("过期的窗口应当被当作已结束")
	}
}

func TestCancelCardBindSession(t *testing.T) {
	s := openTestStore(t)
	a := createTestUser(t, s, "alice")
	id := openBind(t, s, a, future())

	cancel := func() bool {
		var ok bool
		mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
			var err error
			ok, err = CancelCardBindSession(ctx, tx, id)
			return err
		})
		return ok
	}

	if !cancel() {
		t.Fatal("第一次取消应当成功")
	}
	if cancel() {
		t.Error("重复取消不该再次生效")
	}

	// 取消之后应当能开新窗口（全局单飞被释放了）。
	b := createTestUser(t, s, "bob")
	if err := s.WithTx(context.Background(), false, func(tx *sql.Tx) error {
		_, err := CreateCardBindSession(context.Background(), tx, b, "admin", future())
		return err
	}); err != nil {
		t.Errorf("取消之后应当能开新窗口，实际: %v", err)
	}
}
