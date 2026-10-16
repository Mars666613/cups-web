package store

import (
	"context"
	"database/sql"
	"fmt"
)

// 管理操作审计的动作键。
//
// 用点分小写（`price.update`）而不是中文：这些值会进数据库、会出现在 URL
// 查询参数里，中文在日志、复制粘贴、命令行过滤时都会被编码成 %XX。
// 给人看的中文标签在 cmd/server/audit.go 的 auditActionLabel 里，两处靠
// 这一组常量对齐。
const (
	AuditSettingsUpdate = "settings.update"
	AuditPriceUpdate    = "price.update"
	AuditBalanceAdjust  = "balance.adjust"
	AuditUserCreate     = "user.create"
	AuditUserUpdate     = "user.update"
	AuditUserDelete     = "user.delete"
	AuditOrderRefund    = "order.refund"
	AuditCleanupRun     = "cleanup.run"
	AuditTerminalCreate = "terminal.create"
	AuditTerminalUpdate = "terminal.update"
	AuditTerminalDelete = "terminal.delete"

	// AuditDeviceResolve 是「把一条设备异常标记为已知悉」。
	//
	// 它值得留痕：这个动作会【静音】某一类告警（见 schema.sql 的
	// device_alert_mutes），也就是「之后这台机器再报这个毛病不会有人被通知」。
	// 过一阵子有人问「为什么卡纸没人收到消息」，审计里必须有答案。
	AuditDeviceResolve = "device.resolve"
)

// AuditEntry 是一条管理操作记录。
type AuditEntry struct {
	ID         int64  `json:"id"`
	Actor      string `json:"actor"`
	Action     string `json:"action"`
	Target     string `json:"target"`
	Summary    string `json:"summary"`
	BeforeJSON string `json:"beforeJson"`
	AfterJSON  string `json:"afterJson"`
	CreatedAt  string `json:"createdAt"`
}

// InsertAudit 写一条审计记录。
//
// ⚠️ 收 *sql.Tx 是刻意的，不是图方便：审计必须与业务改动同事务（见
// schema.sql 里 admin_audit_log 的说明）。传 *sql.DB 会写出「改了但没记」
// 的可能，而那种记录比没有记录更糟 —— 它让管理员以为审计是完整的。
//
// actor 为空一律落成 'unknown'：一条不知道是谁做的记录，至少还能说明
// 「有人在这个时间点动了这个东西」，比一个空字符串有用。
func InsertAudit(ctx context.Context, tx *sql.Tx, e AuditEntry) error {
	if e.Actor == "" {
		e.Actor = "unknown"
	}
	if e.CreatedAt == "" {
		e.CreatedAt = nowUTC()
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO admin_audit_log
		(actor, action, target, summary, before_json, after_json, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		e.Actor, e.Action, e.Target, e.Summary, e.BeforeJSON, e.AfterJSON, e.CreatedAt)
	return err
}

const auditColumns = `id, actor, action, target, summary, before_json, after_json, created_at`

// ListAudit 按时间倒序取一页审计记录。action 为空表示不筛。
func ListAudit(ctx context.Context, tx *sql.Tx, action string, limit, offset int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	// 占位符序号跟着 args 长度走：条件是可选的，写死 $1/$2/$3 就得把
	// 整条查询重写一遍，那种分支最容易只改一半（改了 WHERE 忘了 LIMIT）。
	query := "SELECT " + auditColumns + " FROM admin_audit_log"
	args := []interface{}{}
	if action != "" {
		args = append(args, action)
		query += fmt.Sprintf(" WHERE action = $%d", len(args))
	}
	args = append(args, limit, offset)
	query += fmt.Sprintf(" ORDER BY id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args))

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]AuditEntry, 0, limit)
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.Actor, &e.Action, &e.Target, &e.Summary,
			&e.BeforeJSON, &e.AfterJSON, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountAudit 数审计记录条数（分页用）。action 为空表示不筛。
func CountAudit(ctx context.Context, tx *sql.Tx, action string) (int, error) {
	var n int
	if action != "" {
		err := tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM admin_audit_log WHERE action = $1", action).Scan(&n)
		return n, err
	}
	err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM admin_audit_log").Scan(&n)
	return n, err
}

// ListAuditActions 返回出现过的动作键（供前端筛选下拉）。
//
// 从数据里取而不是写死一份：动作键会随功能增加，写死的下拉会漏掉新的那一类，
// 而管理员恰恰是想「看看最近都动了些什么」。顺带也让空库的前端不必硬编码。
func ListAuditActions(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		"SELECT DISTINCT action FROM admin_audit_log ORDER BY action")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
