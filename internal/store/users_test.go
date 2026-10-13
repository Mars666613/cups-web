package store

import (
	"context"
	"database/sql"
	"testing"
)

// 支付宝授权登录加的那一列（alipay_open_id）与它的部分唯一索引。
// 需要 PostgreSQL，未设 TEST_DATABASE_URL 时会跳过（同 billing_test.go）。

func TestCreateUserWithAlipayOpenID(t *testing.T) {
	s := openTestStore(t)
	const openID = "074a1CcTG1LelxKe4xQC0zgNdId0nxi95b5lsNpazWYoCo5"

	var created User
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		u, err := CreateUser(ctx, tx, CreateUserInput{
			Username:     "alipay:" + openID,
			PasswordHash: "x",
			Role:         RoleUser,
			AlipayOpenID: openID,
		})
		created = u
		return err
	})

	if created.AlipayOpenID != openID {
		t.Fatalf("新建用户没有带回 alipay_open_id: %q", created.AlipayOpenID)
	}

	// 再按 open_id 查一次：证明它真的落库了，而不只是结构体字段被填过。
	var found User
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		u, err := GetUserByAlipayOpenID(ctx, tx, openID)
		found = u
		return err
	})
	if found.ID != created.ID {
		t.Errorf("按 open_id 查回来的不是同一个人: id %d vs %d", found.ID, created.ID)
	}
	if found.Username != "alipay:"+openID {
		t.Errorf("username 不对: %q", found.Username)
	}
}

// 同一个支付宝号不能建出两个账号 —— 靠的就是那条部分唯一索引。
func TestAlipayOpenIDIsUnique(t *testing.T) {
	s := openTestStore(t)
	const openID = "openid-dup"

	create := func(username string) error {
		return s.WithTx(context.Background(), false, func(tx *sql.Tx) error {
			_, err := CreateUser(context.Background(), tx, CreateUserInput{
				Username: username, PasswordHash: "x", Role: RoleUser, AlipayOpenID: openID,
			})
			return err
		})
	}

	if err := create("alipay:" + openID); err != nil {
		t.Fatalf("第一个应当建成功: %v", err)
	}
	if err := create("alipay:" + openID + "-2"); err == nil {
		t.Error("同一个 open_id 建第二个账号应当被唯一索引拒绝")
	}
}

// 空串不参与唯一性：普通账号（alipay_open_id 为 ”）可以有任意多个。
//
// 这是【部分】唯一索引与普通唯一索引的关键区别 —— 若写成普通唯一索引，
// 第二个普通账号就建不出来了，而且现象是"新用户建不了"，
// 与支付宝八竿子打不着，极难查。
func TestEmptyAlipayOpenIDDoesNotCollide(t *testing.T) {
	s := openTestStore(t)

	for _, name := range []string{"u1", "u2", "u3"} {
		err := s.WithTx(context.Background(), false, func(tx *sql.Tx) error {
			_, err := CreateUser(context.Background(), tx, CreateUserInput{
				Username: name, PasswordHash: "x", Role: RoleUser,
			})
			return err
		})
		if err != nil {
			t.Fatalf("普通账号 %s 不该被 open_id 的索引挡住: %v", name, err)
		}
	}
}

// ListUsers 的用户名筛选：子串、不区分大小写、空条件不过滤。
//
// 这条链路服务的场景很具体：顾客报出一个用户名，管理员粘进搜索框要把人找出来。
func TestListUsersUsernameFilter(t *testing.T) {
	s := openTestStore(t)

	for _, n := range []string{"alice", "alipay:8F3K2Q", "bob", "ALIPAY:ZZZZZZ"} {
		mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
			_, err := CreateUser(ctx, tx, CreateUserInput{
				Username: n, PasswordHash: "x", Role: RoleUser,
			})
			return err
		})
	}

	cases := []struct {
		name   string
		filter string
		want   int
	}{
		{"空条件不过滤", "", 4},
		{"完整用户名命中一个", "alipay:8F3K2Q", 1},
		{"只给一半也能命中", "8F3K2Q", 1},
		{"前缀命中两条", "alipay:", 2},
		{"大小写无关", "ALIPAY:", 2},
		{"查不到就是零条", "nobody", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got int
			mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
				users, err := ListUsers(ctx, tx, UserFilter{Username: c.filter})
				got = len(users)
				return err
			})
			if got != c.want {
				t.Errorf("筛选 %q：期望 %d 条，实际 %d 条", c.filter, c.want, got)
			}
		})
	}
}

// 搜索框里打 % 必须按【字面】处理，不能变成"匹配所有人"。
// 这正是实现里用 position() 而不是 LIKE 的理由 —— LIKE 里 % 和 _ 是通配符，
// 反斜杠转义又特别容易被漏掉。
func TestListUsersFilterTreatsPercentLiterally(t *testing.T) {
	s := openTestStore(t)
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		_, err := CreateUser(ctx, tx, CreateUserInput{
			Username: "alice", PasswordHash: "x", Role: RoleUser,
		})
		return err
	})

	var got int
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		users, err := ListUsers(ctx, tx, UserFilter{Username: "%"})
		got = len(users)
		return err
	})
	if got != 0 {
		t.Errorf(`"%%" 应当按字面处理（没有用户名含它），却匹配到 %d 条`, got)
	}
}
