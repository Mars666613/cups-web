package store

import (
	"context"
	"database/sql"
	"errors"
)

// ErrBindSessionActive 已经有一个绑定窗口开着了。
//
// 全局单飞是刻意的：两个窗口同时开着的话，一次刷卡绑给谁是**不确定**的 ——
// 而绑错的后果是「A 的卡能取到 B 的打印件」，属于隐私事故。
var ErrBindSessionActive = errors.New("store: a card bind session is already active")

// CardBindSession 是一次「刷卡绑定」的窗口。
//
// 管理员点「绑卡」→ 建一行 → 到打印机旁的终端刷卡 → 那次刷卡把卡绑给
// UserID 并把这行标成 Done。见 schema.sql 里这张表的说明。
type CardBindSession struct {
	ID        int64
	UserID    int64
	Username  string // JOIN users 带出的展示字段
	CreatedBy string
	CreatedAt string
	ExpiresAt string
	CardNo    string
	Done      bool
	Error     string
}

const cardBindSessionColumns = `s.id, s.user_id, u.username, s.created_by,
	s.created_at, s.expires_at, s.card_no, s.done, s.error`

func scanCardBindSession(s scanner) (CardBindSession, error) {
	var b CardBindSession
	err := s.Scan(&b.ID, &b.UserID, &b.Username, &b.CreatedBy,
		&b.CreatedAt, &b.ExpiresAt, &b.CardNo, &b.Done, &b.Error)
	return b, err
}

// CreateCardBindSession 开一个绑定窗口，返回它的 id。
//
// 同一时刻只允许一个（见 ErrBindSessionActive）。开之前先把已经过期但
// 没收尾的那些标成 done —— 否则那条部分唯一索引会把新窗口永远挡在门外，
// 而表现是「点绑卡没反应」，且重启才能恢复。
func CreateCardBindSession(ctx context.Context, tx *sql.Tx, userID int64, createdBy, expiresAt string) (int64, error) {
	if _, err := tx.ExecContext(ctx,
		`UPDATE card_bind_sessions SET done = TRUE WHERE NOT done AND expires_at < $1`,
		nowUTC()); err != nil {
		return 0, err
	}

	var id int64
	err := tx.QueryRowContext(ctx, `INSERT INTO card_bind_sessions
		(user_id, created_by, created_at, expires_at) VALUES ($1, $2, $3, $4)
		RETURNING id`,
		userID, createdBy, nowUTC(), expiresAt).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			return 0, ErrBindSessionActive
		}
		return 0, err
	}
	return id, nil
}

// GetCardBindSession 按 id 取。查不到返回 sql.ErrNoRows。
//
// ⚠️ 没过期的窗口直接当成「已过期」返回（Done 置真）：把判定收在这一处，
// 调用方就不用各自比对时间 —— 少一处比对就少一处「有的地方判了、
// 有的地方没判」的分叉。
func GetCardBindSession(ctx context.Context, tx *sql.Tx, id int64) (CardBindSession, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+cardBindSessionColumns+`
		FROM card_bind_sessions s JOIN users u ON u.id = s.user_id
		WHERE s.id = $1`, id)
	b, err := scanCardBindSession(row)
	if err != nil {
		return CardBindSession{}, err
	}
	if !b.Done && b.ExpiresAt < nowUTC() {
		b.Done = true
	}
	return b, nil
}

// ActiveCardBindSession 取当前那个未完成的绑定窗口。
// 没有活跃窗口时返回零值（ID == 0）而不是错误 —— 「没有人在绑卡」是常态。
func ActiveCardBindSession(ctx context.Context, tx *sql.Tx) (CardBindSession, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+cardBindSessionColumns+`
		FROM card_bind_sessions s JOIN users u ON u.id = s.user_id
		WHERE NOT s.done AND s.expires_at >= $1
		ORDER BY s.id LIMIT 1`, nowUTC())
	b, err := scanCardBindSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return CardBindSession{}, nil
	}
	return b, err
}

// CompleteCardBindSession 收尾一个绑定窗口，返回「本次是不是由它收的尾」。
//
// 这个布尔就是幂等的基石：两次刷卡撞在一起时，只有一个能拿到 true，
// 也就只有一个有资格去改 users.card_no。
func CompleteCardBindSession(ctx context.Context, tx *sql.Tx, id int64, cardNo string) (bool, error) {
	res, err := tx.ExecContext(ctx, `UPDATE card_bind_sessions
		SET done = TRUE, card_no = $1, error = '' WHERE id = $2 AND NOT done`,
		cardNo, id)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// RecordCardBindError 记下「这次刷卡为什么没绑上」，但【不】关闭窗口。
//
// 典型情形是那张卡已经绑给别人了 —— 管理员可以直接换一张卡再刷，
// 不必回到电脑前重新点一次。错误内容会经轮询显示在他的浏览器上。
func RecordCardBindError(ctx context.Context, tx *sql.Tx, id int64, reason string) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE card_bind_sessions SET error = $1 WHERE id = $2 AND NOT done`,
		reason, id)
	return err
}

// CancelCardBindSession 管理员主动取消。返回是否真的改动了。
func CancelCardBindSession(ctx context.Context, tx *sql.Tx, id int64) (bool, error) {
	res, err := tx.ExecContext(ctx,
		`UPDATE card_bind_sessions SET done = TRUE WHERE id = $1 AND NOT done`, id)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}
