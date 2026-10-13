package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type PrintRecord struct {
	ID         int64
	UserID     int64
	Username   string
	PrinterURI string
	Filename   string
	StoredPath string
	Pages      int
	JobID      sql.NullString
	Status     string
	IsDuplex   bool
	IsColor    bool

	// 完整打印参数快照，供「重新打印」精确预填（Issue #68）。
	Copies         int
	Orientation    string
	PaperSize      string
	PaperType      string
	MediaSource    string
	PrintScaling   string
	PageRange      string
	PageSet        string
	Mirror         bool
	WatermarkText  string
	NumberUp       int
	NumberUpLayout string
	PageBorder     string

	CreatedAt string
}

// printRecordColumns 是 SELECT 时的统一列清单（带 p./u. 前缀），
// 与 scanPrintRecord 的字段顺序严格对应。
const printRecordColumns = `p.id, p.user_id, u.username, p.printer_uri, p.filename, p.stored_path, p.pages,
	p.job_id, p.status, p.is_duplex, p.is_color,
	p.copies, p.orientation, p.paper_size, p.paper_type, p.media_source, p.print_scaling,
	p.page_range, p.page_set, p.mirror, p.watermark_text, p.number_up, p.number_up_layout, p.page_border,
	p.created_at`

// scanPrintRecord 与 printRecordColumns 的列顺序严格对应。
// 复用 users.go 中定义的 scanner 接口（*sql.Row / *sql.Rows 通用）。
func scanPrintRecord(s scanner) (PrintRecord, error) {
	var rec PrintRecord
	err := s.Scan(
		&rec.ID, &rec.UserID, &rec.Username, &rec.PrinterURI, &rec.Filename, &rec.StoredPath,
		&rec.Pages, &rec.JobID, &rec.Status, &rec.IsDuplex, &rec.IsColor,
		&rec.Copies, &rec.Orientation, &rec.PaperSize, &rec.PaperType, &rec.MediaSource, &rec.PrintScaling,
		&rec.PageRange, &rec.PageSet, &rec.Mirror, &rec.WatermarkText, &rec.NumberUp, &rec.NumberUpLayout, &rec.PageBorder,
		&rec.CreatedAt,
	)
	return rec, err
}

type PrintFilter struct {
	Username string
	StartAt  string
	EndAt    string
	Limit    int
}

func InsertPrintRecord(ctx context.Context, tx *sql.Tx, rec *PrintRecord) (int64, error) {
	// RETURNING id 取代 LastInsertId（PG 没有后者）。
	var id int64
	err := tx.QueryRowContext(ctx, `INSERT INTO print_jobs (
		user_id, printer_uri, filename, stored_path, pages,
		job_id, status, is_duplex, is_color,
		copies, orientation, paper_size, paper_type, media_source, print_scaling,
		page_range, page_set, mirror, watermark_text, number_up, number_up_layout, page_border,
		created_at
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23)
	RETURNING id`,
		rec.UserID, rec.PrinterURI, rec.Filename, rec.StoredPath, rec.Pages,
		rec.JobID, rec.Status, rec.IsDuplex, rec.IsColor,
		rec.Copies, rec.Orientation, rec.PaperSize, rec.PaperType, rec.MediaSource, rec.PrintScaling,
		rec.PageRange, rec.PageSet, rec.Mirror, rec.WatermarkText, rec.NumberUp, rec.NumberUpLayout, rec.PageBorder,
		rec.CreatedAt,
	).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func UpdatePrintStatus(ctx context.Context, tx *sql.Tx, id int64, status string, jobID string) error {
	_, err := tx.ExecContext(ctx, "UPDATE print_jobs SET status = $1, job_id = $2 WHERE id = $3", status, jobID, id)
	return err
}

func GetPrintRecordByID(ctx context.Context, tx *sql.Tx, id int64) (PrintRecord, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+printRecordColumns+`
		FROM print_jobs p
		JOIN users u ON u.id = p.user_id
		WHERE p.id = $1`, id)
	return scanPrintRecord(row)
}

func ListPrintRecords(ctx context.Context, tx *sql.Tx, filter PrintFilter) ([]PrintRecord, error) {
	args := []interface{}{}
	conds := []string{"1=1"}
	// $N 必须按【实参 append 顺序】编号，而条件是可选拼上去的 ——
	// 所以用 arg() 把「追加实参」和「拿到占位符」绑成一步，
	// 编号就不可能和实参错位。末尾那个 LIMIT 也走同一个计数器。
	//
	// ⚠️ 不要退回成手写 $1/$2 再靠人肉对齐：条件一多，漏一个就静默查错数据。
	arg := func(v interface{}) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if filter.Username != "" {
		conds = append(conds, "u.username = "+arg(filter.Username))
	}
	if filter.StartAt != "" {
		conds = append(conds, "p.created_at >= "+arg(filter.StartAt))
	}
	if filter.EndAt != "" {
		conds = append(conds, "p.created_at <= "+arg(filter.EndAt))
	}
	query := fmt.Sprintf(`SELECT `+printRecordColumns+`
		FROM print_jobs p
		JOIN users u ON u.id = p.user_id
		WHERE %s
		ORDER BY p.created_at DESC`, strings.Join(conds, " AND "))
	if filter.Limit > 0 {
		query += " LIMIT " + arg(filter.Limit)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []PrintRecord
	for rows.Next() {
		rec, err := scanPrintRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, rows.Err()
}
