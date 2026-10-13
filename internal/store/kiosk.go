package store

import (
	"context"
	"database/sql"
)

// KioskTerminal 是打印机旁那台取件终端（树莓派）。
//
// Token 是这台终端的凭据，由管理员生成、写进 Pi 的配置文件；
// 取件端没有浏览器也没有会话，所以它是本系统唯一的无会话写入口。
type KioskTerminal struct {
	Token      string
	Name       string
	PrinterURI string
	Enabled    bool
	CreatedAt  string
	LastSeenAt string // '' = 从未上线
}

const kioskTerminalColumns = `token, name, printer_uri, enabled, created_at, last_seen_at`

func scanKioskTerminal(s scanner) (KioskTerminal, error) {
	var t KioskTerminal
	err := s.Scan(&t.Token, &t.Name, &t.PrinterURI, &t.Enabled, &t.CreatedAt, &t.LastSeenAt)
	return t, err
}

func ListKioskTerminals(ctx context.Context, tx *sql.Tx) ([]KioskTerminal, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT `+kioskTerminalColumns+` FROM kiosk_terminals ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []KioskTerminal{}
	for rows.Next() {
		t, err := scanKioskTerminal(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, t)
	}
	return list, rows.Err()
}

// GetKioskTerminal 按 token 认终端。查不到返回 sql.ErrNoRows。
//
// ⚠️ 调用方必须把「token 不存在」与「终端已停用」分开报：
// 前者是配置漏了/抄错了，后者是管理员主动关的。合成一句话会让运维两边都查。
func GetKioskTerminal(ctx context.Context, tx *sql.Tx, token string) (KioskTerminal, error) {
	if token == "" {
		return KioskTerminal{}, sql.ErrNoRows
	}
	row := tx.QueryRowContext(ctx,
		`SELECT `+kioskTerminalColumns+` FROM kiosk_terminals WHERE token = $1`, token)
	return scanKioskTerminal(row)
}

func CreateKioskTerminal(ctx context.Context, tx *sql.Tx, t KioskTerminal) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO kiosk_terminals (token, name, printer_uri, enabled, created_at, last_seen_at)
		 VALUES ($1, $2, $3, $4, $5, '')`,
		t.Token, t.Name, t.PrinterURI, t.Enabled, nowUTC())
	return err
}

// UpdateKioskTerminal 改名字 / 换绑打印机 / 启用停用。token 本身不可改
// （它是 Pi 上配好的值，改了现场那台就失效了；要换 token 就重建一个）。
func UpdateKioskTerminal(ctx context.Context, tx *sql.Tx, token, name, printerURI string, enabled bool) error {
	res, err := tx.ExecContext(ctx,
		`UPDATE kiosk_terminals SET name = $1, printer_uri = $2, enabled = $3 WHERE token = $4`,
		name, printerURI, enabled, token)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err == nil && affected == 0 {
		return sql.ErrNoRows
	}
	return err
}

func DeleteKioskTerminal(ctx context.Context, tx *sql.Tx, token string) error {
	res, err := tx.ExecContext(ctx, `DELETE FROM kiosk_terminals WHERE token = $1`, token)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err == nil && affected == 0 {
		return sql.ErrNoRows
	}
	return err
}

// TouchKioskTerminal 记一笔「这台终端刚动过」。
//
// 失败不影响取件本身，调用方只记日志 —— 它纯粹是给管理员看「哪台在线」的。
func TouchKioskTerminal(ctx context.Context, tx *sql.Tx, token string) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE kiosk_terminals SET last_seen_at = $1 WHERE token = $2`, nowUTC(), token)
	return err
}

// PrinterURIsWithKiosk 返回所有【启用了取件终端】的打印机 URI。
//
// 用途是 /api/printers 的 hasKiosk 标记：取件模式下提交页只列这些打印机。
// 少了它，员工可以选一台没装终端的机器，任务就永远取不出来 —— 那是个
// 没有报错、也没有出口的黑洞。
//
// ⚠️ 只算 enabled 的：停用的终端不该让打印机在提交页继续露面。
func PrinterURIsWithKiosk(ctx context.Context, tx *sql.Tx) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT DISTINCT printer_uri FROM kiosk_terminals WHERE enabled = TRUE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	set := map[string]bool{}
	for rows.Next() {
		var uri string
		if err := rows.Scan(&uri); err != nil {
			return nil, err
		}
		set[uri] = true
	}
	return set, rows.Err()
}
