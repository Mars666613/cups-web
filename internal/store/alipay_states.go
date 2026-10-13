package store

import (
	"context"
	"database/sql"
)

// ─── 支付宝授权登录的短命状态 ──────────────────────────────────────────
//
// 为什么要有这张表（而不是把 state 放在签名 cookie 里）：
// 授权与兑换【必然发生在支付宝的 webview】—— 官方要求手机端 H5 必须在支付宝
// 客户端内打开。而发起可能发生在普通浏览器的（顾客先用相机扫了打印机的码）。
// 浏览器和 webview 是【两个 cookie 罐】，cookie 过不去，所以状态只能放服务端。
//
// 流程：
//   start    → 插一行（user_id = 0）
//   兑换成功 → 把 user_id 写进这一行
//   浏览器轮询到 user_id 非 0 → 给自己签发会话，并删掉这一行
//
// from_browser 记的是"发起方是普通浏览器"，也就是"会有人来轮询"。
// 用支付宝扫一扫直接进来的那种没有轮询方，它在 webview 里就地登录。

// AlipayLoginState 是一行待领取的登录结果。UserID 为 0 表示还没完成。
type AlipayLoginState struct {
	State       string
	UserID      int64
	FromBrowser bool
	CreatedAt   string
}

func CreateAlipayLoginState(ctx context.Context, tx *sql.Tx, state string, fromBrowser bool) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO alipay_login_states (state, user_id, from_browser, created_at)
		 VALUES ($1, 0, $2, $3)`,
		state, fromBrowser, nowUTC())
	return err
}

func GetAlipayLoginState(ctx context.Context, tx *sql.Tx, state string) (AlipayLoginState, error) {
	var s AlipayLoginState
	err := tx.QueryRowContext(ctx,
		`SELECT state, user_id, from_browser, created_at
		 FROM alipay_login_states WHERE state = $1`, state).
		Scan(&s.State, &s.UserID, &s.FromBrowser, &s.CreatedAt)
	return s, err
}

// MarkAlipayLoginStateDone 把兑换出来的用户写进这一行，等浏览器来领。
func MarkAlipayLoginStateDone(ctx context.Context, tx *sql.Tx, state string, userID int64) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE alipay_login_states SET user_id = $1 WHERE state = $2`, userID, state)
	return err
}

func DeleteAlipayLoginState(ctx context.Context, tx *sql.Tx, state string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM alipay_login_states WHERE state = $1`, state)
	return err
}

// DeleteExpiredAlipayLoginStates 清掉过期的行，返回删了几条。
//
// 刻意不塞进 maintenance 的小时任务：这张表只有"正在登录的人"在用，
// 量极小，而过期行留在那里是有害的（一个 state 理论上能被重放）。
// 在每次发起授权时顺手清一次，比等一小时更及时。
func DeleteExpiredAlipayLoginStates(ctx context.Context, tx *sql.Tx, cutoff string) (int64, error) {
	res, err := tx.ExecContext(ctx,
		`DELETE FROM alipay_login_states WHERE created_at < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
