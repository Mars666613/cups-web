package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// 设备异常事件的数据层。
//
// 表结构与「为什么落库」见 schema.sql 里 device_events 上面的说明。这里
// 只补充两条数据层自己的约定：
//
//  1. 所有写入都收 *sql.Tx。巡检一轮会更新多台机器、多个类别，必须整体成功
//     或整体失败 —— 中途失败留下「一半机器的新状态 + 一半旧状态」，会让
//     下一轮巡检把已经恢复的异常又当成新出现的再通知一遍。
//  2. time 一律由调用方传字符串（RFC3339 UTC）。本包不调 time.Now()，
//     这样每条路径都能被单测固定在某个时刻上。
//
// kind / severity 在本包是【不透明字符串】，取值清单在 internal/devicealert。
//
// 刻意不在这里再定义一份常量：本包只负责「存下来、按条件取出来」，它不需要
// 认识 unreachable / paper_jam 这些词。两处清单就会漂 —— 而漂的表现是
// 「刚加的那类异常，前端认不出来」。中文文案同理，在 cmd/server 里。

// DeviceEvent 是一条设备异常事件。
type DeviceEvent struct {
	ID         int64  `json:"id"`
	PrinterURI string `json:"printerUri"`
	Kind       string `json:"kind"`
	Severity   string `json:"severity"`
	Detail     string `json:"detail"`
	StartedAt  string `json:"startedAt"`
	UpdatedAt  string `json:"updatedAt"`
	// ResolvedAt 为空表示这条异常【现在还在】。
	ResolvedAt string `json:"resolvedAt"`
	// NotifiedAt 是最后一次成功发出通知的时刻，用于重发节流。
	NotifiedAt string `json:"notifiedAt"`
}

const deviceEventColumns = `id, printer_uri, kind, severity, detail,
	started_at, updated_at, resolved_at, notified_at`

// rowScanner 是 *sql.Row 与 *sql.Rows 的公共部分。
type rowScanner interface {
	Scan(dest ...any) error
}

func scanDeviceEvent(s rowScanner) (DeviceEvent, error) {
	var e DeviceEvent
	err := s.Scan(&e.ID, &e.PrinterURI, &e.Kind, &e.Severity, &e.Detail,
		&e.StartedAt, &e.UpdatedAt, &e.ResolvedAt, &e.NotifiedAt)
	return e, err
}

// OpenDeviceEvent 记录「这台打印机仍然有这一类异常」。
//
// 返回该事件，以及它是不是【这一轮才新建的】—— 调用方据此决定要不要发通知。
//
// 幂等，靠 idx_device_events_open 这个部分唯一索引（见 schema.sql）：
// 已经存在未恢复的同名事件时只刷新 severity / detail / updated_at。
//
// ⚠️ 刷新时【不碰 started_at】：那一列是「这个问题从什么时候开始」的唯一答案。
// 每轮巡检都把它更新成当前时刻的话，界面上「已持续 6 小时」会永远显示「刚刚」，
// 而「已经卡了多久」正是决定要不要现在跑去机房的那个数字。
func OpenDeviceEvent(ctx context.Context, tx *sql.Tx, e DeviceEvent) (DeviceEvent, bool, error) {
	if e.StartedAt == "" {
		e.StartedAt = nowUTC()
	}
	if e.UpdatedAt == "" {
		e.UpdatedAt = e.StartedAt
	}

	// 一条语句搞定「插入或已存在」：撞上部分唯一索引时 ON CONFLICT DO NOTHING
	// 不返回任何行，QueryRow 给出 sql.ErrNoRows —— 那就是「已存在」的信号。
	row := tx.QueryRowContext(ctx, `INSERT INTO device_events
		(printer_uri, kind, severity, detail, started_at, updated_at, resolved_at, notified_at)
		VALUES ($1, $2, $3, $4, $5, $6, '', '')
		ON CONFLICT (printer_uri, kind) WHERE resolved_at = '' DO NOTHING
		RETURNING `+deviceEventColumns,
		e.PrinterURI, e.Kind, e.Severity, e.Detail, e.StartedAt, e.UpdatedAt)
	out, err := scanDeviceEvent(row)
	if err == nil {
		return out, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return DeviceEvent{}, false, err
	}

	// 已存在：刷新「仍在持续」的那三列。
	existing, err := openDeviceEvent(ctx, tx, e.PrinterURI, e.Kind)
	if err != nil {
		// 走到这里说明刚被别的事务关掉了（人工点「已处理」）—— 极少见，
		// 但此时应当按「不存在」处理让调用方下一轮重新开，不要报错。
		if errors.Is(err, sql.ErrNoRows) {
			return DeviceEvent{}, false, nil
		}
		return DeviceEvent{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE device_events
		SET severity = $1, detail = $2, updated_at = $3
		WHERE id = $4 AND resolved_at = ''`,
		e.Severity, e.Detail, e.UpdatedAt, existing.ID); err != nil {
		return DeviceEvent{}, false, err
	}
	existing.Severity = e.Severity
	existing.Detail = e.Detail
	existing.UpdatedAt = e.UpdatedAt
	return existing, false, nil
}

// openDeviceEvent 取一台打印机某类异常【未恢复】的那一行。
// 没有时返回 sql.ErrNoRows。
func openDeviceEvent(ctx context.Context, tx *sql.Tx, printerURI, kind string) (DeviceEvent, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+deviceEventColumns+`
		FROM device_events
		WHERE printer_uri = $1 AND kind = $2 AND resolved_at = ''`,
		printerURI, kind)
	return scanDeviceEvent(row)
}

// ResolveDeviceEvents 关闭这台打印机上【本轮不再出现】的未恢复事件。
//
// keepKinds 是这一轮仍然存在的异常类别（见 devicealert.Evaluate 的返回值）。
// 不在里面的，就认为机器已经恢复了。返回刚被关闭的行，供调用方发「已恢复」。
//
// 单条关闭都带上「还没恢复」这个条件（resolved_at 仍为空串）：并发的人工
// 「已处理」会抢在前面把 resolved_at 填上，此时 RowsAffected 为 0，我们就不该
// 把它算作「本轮自动恢复」再通知一次。
func ResolveDeviceEvents(ctx context.Context, tx *sql.Tx, printerURI string, keepKinds []string, now string) ([]DeviceEvent, error) {
	if now == "" {
		now = nowUTC()
	}
	keep := make(map[string]bool, len(keepKinds))
	for _, k := range keepKinds {
		keep[k] = true
	}

	rows, err := tx.QueryContext(ctx, `SELECT `+deviceEventColumns+`
		FROM device_events
		WHERE printer_uri = $1 AND resolved_at = ''`, printerURI)
	if err != nil {
		return nil, err
	}
	var open []DeviceEvent
	for rows.Next() {
		e, err := scanDeviceEvent(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		open = append(open, e)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	resolved := []DeviceEvent{}
	for _, e := range open {
		if keep[e.Kind] {
			continue
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE device_events SET resolved_at = $1 WHERE id = $2 AND resolved_at = ''`,
			now, e.ID)
		if err != nil {
			return nil, err
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			continue
		}
		e.ResolvedAt = now
		resolved = append(resolved, e)
	}
	return resolved, nil
}

// ResolveDeviceEventByID 是管理端「手工标记已处理」。已恢复或不存在时返回 false。
//
// ⚠️ 它单独用是【不够的】：下一轮巡检（60 秒后）会发现原因还在，重新开一条
// 并重新通知。所以管理端那一步在同事务里还会调 MuteDeviceKind —— 理由见
// schema.sql 里 device_alert_mutes 上面的说明。
func ResolveDeviceEventByID(ctx context.Context, tx *sql.Tx, id int64, now string) (bool, error) {
	if now == "" {
		now = nowUTC()
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE device_events SET resolved_at = $1 WHERE id = $2 AND resolved_at = ''`, now, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// GetDeviceEvent 按 id 取一条（管理端标记已处理前要先知道是哪台、哪一类）。
// 不存在时返回 sql.ErrNoRows。
func GetDeviceEvent(ctx context.Context, tx *sql.Tx, id int64) (DeviceEvent, error) {
	row := tx.QueryRowContext(ctx, "SELECT "+deviceEventColumns+" FROM device_events WHERE id = $1", id)
	return scanDeviceEvent(row)
}

// MuteDeviceKind 静音「这台打印机的这一类异常」。
//
// 重复静音同一对 (打印机, 类别) 是幂等的：唯一索引 + DO UPDATE，刷新时间和
// 操作人，不会报错也不会攒出多行。
func MuteDeviceKind(ctx context.Context, tx *sql.Tx, printerURI, kind, by, now string) error {
	if now == "" {
		now = nowUTC()
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO device_alert_mutes
		(printer_uri, kind, muted_at, muted_by) VALUES ($1, $2, $3, $4)
		ON CONFLICT (printer_uri, kind) DO UPDATE
		SET muted_at = EXCLUDED.muted_at, muted_by = EXCLUDED.muted_by`,
		printerURI, kind, now, by)
	return err
}

// ListMutedKinds 取一台打印机当前被静音的异常类别，供巡检跳过。
func ListMutedKinds(ctx context.Context, tx *sql.Tx, printerURI string) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT kind FROM device_alert_mutes WHERE printer_uri = $1 ORDER BY kind`, printerURI)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// ListAllDeviceMutes 一次取回所有静音，键是打印机 URI，值是它被静音的类别集合。
//
// 巡检里用这一份而不是每台机器查一次：一轮巡检本来就要为每台打印机各发一次
// 网络探测，能省的数据库往返就省掉 —— 而且「同一轮里看到的静音状态」是同一个
// 瞬间的快照，比逐台查出来的一致。
func ListAllDeviceMutes(ctx context.Context, tx *sql.Tx) (map[string]map[string]bool, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT printer_uri, kind FROM device_alert_mutes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]map[string]bool{}
	for rows.Next() {
		var uri, kind string
		if err := rows.Scan(&uri, &kind); err != nil {
			return nil, err
		}
		if out[uri] == nil {
			out[uri] = map[string]bool{}
		}
		out[uri][kind] = true
	}
	return out, rows.Err()
}

// ClearDeviceKindMutes 解除这台打印机上若干类别的静音。
//
// 巡检在「该类别已经不在异常列表里」时调用它 —— 见 schema.sql 里
// device_alert_mutes 上的说明：静音只对当前这一次持续存在的毛病有效。
func ClearDeviceKindMutes(ctx context.Context, tx *sql.Tx, printerURI string, kinds []string) error {
	for _, k := range kinds {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM device_alert_mutes WHERE printer_uri = $1 AND kind = $2`,
			printerURI, k); err != nil {
			return err
		}
	}
	return nil
}

// MarkDeviceEventNotified 记下「这一条已经通知过了」。
//
// 只在 webhook 【发送成功】之后调用：发送失败时不记，下一轮巡检会重试，
// 这样「配置写错了一个字符」不会静默地吃掉整轮告警。
func MarkDeviceEventNotified(ctx context.Context, tx *sql.Tx, id int64, at string) error {
	if at == "" {
		at = nowUTC()
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE device_events SET notified_at = $1 WHERE id = $2`, at, id)
	return err
}

// deviceEventWhere 拼设备事件的筛选条件，并返回对应的参数。
//
// status 取 open / resolved / 空串（全部）。占位符序号跟着 args 长度走，
// 与 ListAudit 同一个理由：条件可选时写死 $1/$2 就得把整条查询重写一遍，
// 那种分支最容易只改一半。
func deviceEventWhere(status string) (string, []any) {
	switch status {
	case "open":
		return " WHERE resolved_at = ''", nil
	case "resolved":
		return " WHERE resolved_at <> ''", nil
	default:
		return "", nil
	}
}

// ListDeviceEvents 按「未恢复优先、其次时间倒序」取一页。
func ListDeviceEvents(ctx context.Context, tx *sql.Tx, status string, limit, offset int) ([]DeviceEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	where, args := deviceEventWhere(status)

	// resolved_at = '' 在 PG 里是 boolean，DESC 排序后 TRUE 在前 ——
	// 也就是「还在出问题的排在最上面」，那正是打开这一页想看到的。
	query := "SELECT " + deviceEventColumns + " FROM device_events" + where +
		fmt.Sprintf(" ORDER BY (resolved_at = '') DESC, started_at DESC, id DESC LIMIT $%d OFFSET $%d",
			len(args)+1, len(args)+2)
	args = append(args, limit, offset)

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]DeviceEvent, 0, limit)
	for rows.Next() {
		e, err := scanDeviceEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountDeviceEvents 数事件条数（分页用）。
func CountDeviceEvents(ctx context.Context, tx *sql.Tx, status string) (int, error) {
	where, args := deviceEventWhere(status)
	var n int
	err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM device_events"+where, args...).Scan(&n)
	return n, err
}

// CountOpenDeviceEvents 数「现在还没恢复」的事件条数，供管理端横幅用。
func CountOpenDeviceEvents(ctx context.Context, tx *sql.Tx) (int, error) {
	var n int
	err := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM device_events WHERE resolved_at = ''").Scan(&n)
	return n, err
}

// ListOpenDeviceEvents 取所有未恢复的事件，供管理端横幅列出具体是哪几台。
//
// 不分页：未恢复的事件本来就该是「一眼能看完的几条」。真到了几十条，
// 问题已经不在展示上，而在机房那台机器上。
func ListOpenDeviceEvents(ctx context.Context, tx *sql.Tx) ([]DeviceEvent, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+deviceEventColumns+`
		FROM device_events WHERE resolved_at = ''
		ORDER BY severity = 'error' DESC, started_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []DeviceEvent{}
	for rows.Next() {
		e, err := scanDeviceEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
