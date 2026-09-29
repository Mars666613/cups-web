package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// 订单状态。
//
// 终态：printed / failed / refunded / expired / cancelled。
// 只有非终态的订单才会保护其持有的文件不被 maintenance 回收。
const (
	OrderStatusPendingPayment = "pending_payment"
	OrderStatusPaid           = "paid"
	OrderStatusPrinting       = "printing"
	OrderStatusPrinted        = "printed"
	OrderStatusFailed         = "failed"
	OrderStatusRefunded       = "refunded"
	OrderStatusExpired        = "expired"
	OrderStatusCancelled      = "cancelled"
)

// OrderStatusesActive 是「仍在占用文件」的状态集合。
// maintenance 清理文件时必须跳过持有这些状态订单的路径。
var OrderStatusesActive = []string{
	OrderStatusPendingPayment,
	OrderStatusPaid,
	OrderStatusPrinting,
}

// 订单来源。
const (
	OrderSourceWeb       = "web"
	OrderSourceReprint   = "reprint"
	OrderSourceScheduled = "scheduled"
)

// PrintOrder 是一条打印订单。
//
// 它自带完整参数快照，支付后由 startPrintForOrder 直接重放 IPP 请求。
// ⚠️ 列里存的是「最终发给 IPP 的值」：缩放已解析成 keyword、even-reverse 已
// 消化、手动双面已强制单面。带 req_ 前缀的才是用户原始选择（审计与重打预填用）。
type PrintOrder struct {
	ID          int64
	UserID      int64
	Username    string // JOIN users 带出的展示字段，表里没有这列
	Source      string
	ReprintOf   int64
	ScheduledID int64
	BatchID     string

	PrinterURI string
	Filename   string
	StoredPath string
	PrintPath  string
	PrintMime  string

	Pages       int
	BilledPages int

	IsDuplex        bool
	IsColor         bool
	Copies          int
	Orientation     string
	PaperSize       string
	PaperType       string
	MediaSource     string
	PrintScaling    string
	ReqPrintScaling string
	PageRange       string
	PageSet         string
	ReqPageSet      string
	Mirror          bool
	WatermarkText   string
	NumberUp        int
	NumberUpLayout  string
	PageBorder      string

	UnitPriceCents int64
	AmountCents    int64
	RefundedCents  int64
	PayMethod      string

	Status           string
	PaymentID        int64
	JobID            string
	PrintJobRecordID int64
	Error            string
	ExpiresAt        string
	CreatedAt        string
	UpdatedAt        string
}

// printOrderColumns 与 scanPrintOrder 的字段顺序严格对应。
const printOrderColumns = `o.id, o.user_id, u.username, o.source, o.reprint_of, o.scheduled_id, o.batch_id,
	o.printer_uri, o.filename, o.stored_path, o.print_path, o.print_mime,
	o.pages, o.billed_pages,
	o.is_duplex, o.is_color, o.copies, o.orientation, o.paper_size, o.paper_type, o.media_source,
	o.print_scaling, o.req_print_scaling, o.page_range, o.page_set, o.req_page_set,
	o.mirror, o.watermark_text, o.number_up, o.number_up_layout, o.page_border,
	o.unit_price_cents, o.amount_cents, o.refunded_cents, o.pay_method,
	o.status, o.payment_id, o.job_id, o.print_job_record_id, o.error, o.expires_at,
	o.created_at, o.updated_at`

func scanPrintOrder(s scanner) (PrintOrder, error) {
	var o PrintOrder
	err := s.Scan(
		&o.ID, &o.UserID, &o.Username, &o.Source, &o.ReprintOf, &o.ScheduledID, &o.BatchID,
		&o.PrinterURI, &o.Filename, &o.StoredPath, &o.PrintPath, &o.PrintMime,
		&o.Pages, &o.BilledPages,
		&o.IsDuplex, &o.IsColor, &o.Copies, &o.Orientation, &o.PaperSize, &o.PaperType, &o.MediaSource,
		&o.PrintScaling, &o.ReqPrintScaling, &o.PageRange, &o.PageSet, &o.ReqPageSet,
		&o.Mirror, &o.WatermarkText, &o.NumberUp, &o.NumberUpLayout, &o.PageBorder,
		&o.UnitPriceCents, &o.AmountCents, &o.RefundedCents, &o.PayMethod,
		&o.Status, &o.PaymentID, &o.JobID, &o.PrintJobRecordID, &o.Error, &o.ExpiresAt,
		&o.CreatedAt, &o.UpdatedAt,
	)
	return o, err
}

// InsertPrintOrder 插入一条待支付订单，返回订单 id。
// 调用方负责在同一个事务里完成计价，金额列必须已填好。
func InsertPrintOrder(ctx context.Context, tx *sql.Tx, o *PrintOrder) (int64, error) {
	now := nowUTC()
	if o.CreatedAt == "" {
		o.CreatedAt = now
	}
	o.UpdatedAt = now
	res, err := tx.ExecContext(ctx, `INSERT INTO print_orders (
		user_id, source, reprint_of, scheduled_id, batch_id,
		printer_uri, filename, stored_path, print_path, print_mime,
		pages, billed_pages,
		is_duplex, is_color, copies, orientation, paper_size, paper_type, media_source,
		print_scaling, req_print_scaling, page_range, page_set, req_page_set,
		mirror, watermark_text, number_up, number_up_layout, page_border,
		unit_price_cents, amount_cents, refunded_cents, pay_method,
		status, payment_id, job_id, print_job_record_id, error, expires_at,
		created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.UserID, o.Source, o.ReprintOf, o.ScheduledID, o.BatchID,
		o.PrinterURI, o.Filename, o.StoredPath, o.PrintPath, o.PrintMime,
		o.Pages, o.BilledPages,
		o.IsDuplex, o.IsColor, o.Copies, o.Orientation, o.PaperSize, o.PaperType, o.MediaSource,
		o.PrintScaling, o.ReqPrintScaling, o.PageRange, o.PageSet, o.ReqPageSet,
		o.Mirror, o.WatermarkText, o.NumberUp, o.NumberUpLayout, o.PageBorder,
		o.UnitPriceCents, o.AmountCents, o.RefundedCents, o.PayMethod,
		o.Status, o.PaymentID, o.JobID, o.PrintJobRecordID, o.Error, o.ExpiresAt,
		o.CreatedAt, o.UpdatedAt,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func GetOrderByID(ctx context.Context, tx *sql.Tx, id int64) (PrintOrder, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+printOrderColumns+`
		FROM print_orders o
		JOIN users u ON u.id = o.user_id
		WHERE o.id = ?`, id)
	return scanPrintOrder(row)
}

type OrderFilter struct {
	Username string
	Status   string
	StartAt  string
	EndAt    string
	Limit    int
}

func ListOrders(ctx context.Context, tx *sql.Tx, filter OrderFilter) ([]PrintOrder, error) {
	args := []interface{}{}
	conds := []string{"1=1"}
	if filter.Username != "" {
		conds = append(conds, "u.username = ?")
		args = append(args, filter.Username)
	}
	if filter.Status != "" {
		conds = append(conds, "o.status = ?")
		args = append(args, filter.Status)
	}
	if filter.StartAt != "" {
		conds = append(conds, "o.created_at >= ?")
		args = append(args, filter.StartAt)
	}
	if filter.EndAt != "" {
		conds = append(conds, "o.created_at <= ?")
		args = append(args, filter.EndAt)
	}
	query := fmt.Sprintf(`SELECT `+printOrderColumns+`
		FROM print_orders o
		JOIN users u ON u.id = o.user_id
		WHERE %s
		ORDER BY o.created_at DESC, o.id DESC`, strings.Join(conds, " AND "))
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	orders := []PrintOrder{}
	for rows.Next() {
		o, err := scanPrintOrder(rows)
		if err != nil {
			return nil, err
		}
		orders = append(orders, o)
	}
	return orders, rows.Err()
}

// ListOrdersByBatch 取一个批次下的全部订单，按 id 升序（与用户选择的文件顺序一致）。
func ListOrdersByBatch(ctx context.Context, tx *sql.Tx, batchID string) ([]PrintOrder, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+printOrderColumns+`
		FROM print_orders o
		JOIN users u ON u.id = o.user_id
		WHERE o.batch_id = ?
		ORDER BY o.id`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	orders := []PrintOrder{}
	for rows.Next() {
		o, err := scanPrintOrder(rows)
		if err != nil {
			return nil, err
		}
		orders = append(orders, o)
	}
	return orders, rows.Err()
}

// OwnsOrderBatch 判断 batchID 是否属于该用户 —— 下单接口的批量支付必须校验
// 调用者拥有这个批次，否则任何人都能拿别人的 batchId 去发起支付。
func OwnsOrderBatch(ctx context.Context, tx *sql.Tx, batchID string, userID int64) (bool, error) {
	var count int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM print_orders WHERE batch_id = ? AND user_id = ?`,
		batchID, userID).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// SumPendingAmountByBatch 求一个批次中「待支付」订单的金额合计。
func SumPendingAmountByBatch(ctx context.Context, tx *sql.Tx, batchID string) (int64, error) {
	var total sql.NullInt64
	err := tx.QueryRowContext(ctx,
		`SELECT SUM(amount_cents) FROM print_orders WHERE batch_id = ? AND status = ?`,
		batchID, OrderStatusPendingPayment).Scan(&total)
	if err != nil {
		return 0, err
	}
	return total.Int64, nil
}

// ─── 状态迁移 ──────────────────────────────────────────────────────────
//
// 全部经由 orderTransition 实现，它带 WHERE status IN (...) 条件并返回
// 「是否真的迁移了」。这个布尔值就是幂等的基石：两个并发的支付回调、
// 或者回调与补偿扫描撞在一起时，只有一个能拿到 true。

// orderTransition 是状态迁移的统一实现。
//
// setSQL 可以带占位符（实参放 setArgs），函数会自动补上 updated_at。
// 只有当前状态在 from 列表里才会迁移。
func orderTransition(ctx context.Context, tx *sql.Tx, id int64, from []string,
	setSQL string, setArgs ...interface{}) (bool, error) {
	if len(from) == 0 {
		return false, errors.New("store: order transition requires at least one source status")
	}
	placeholders := make([]string, len(from))
	args := make([]interface{}, 0, len(setArgs)+len(from)+2)
	args = append(args, setArgs...)
	args = append(args, nowUTC()) // updated_at
	args = append(args, id)
	for i, s := range from {
		placeholders[i] = "?"
		args = append(args, s)
	}
	query := fmt.Sprintf(
		`UPDATE print_orders SET %s, updated_at = ? WHERE id = ? AND status IN (%s)`,
		setSQL, strings.Join(placeholders, ","))
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// ClaimOrderForPrint 把订单从 paid 原子迁移到 printing。
//
// 这是「付一次只印一次」的最内层闸门：只有返回 true 的调用方才有资格
// 发起 IPP 提交。回调、补偿扫描、前端重试三条路并发时，只有一个能拿到 true。
func ClaimOrderForPrint(ctx context.Context, tx *sql.Tx, id int64) (bool, error) {
	return orderTransition(ctx, tx, id, []string{OrderStatusPaid},
		`status = ?`, OrderStatusPrinting)
}

// MarkOrderPaid 把订单推进到已支付。
//
// 允许从 expired 迁移过来：用户扫了码、订单过期后才到账的场景真实存在，
// 付了钱就该拿到东西。文件若已被过期清理删掉，startPrintForOrder 会走退款兜底。
func MarkOrderPaid(ctx context.Context, tx *sql.Tx, id int64, payMethod string, paymentID int64) (bool, error) {
	return orderTransition(ctx, tx, id,
		[]string{OrderStatusPendingPayment, OrderStatusExpired},
		`status = ?, pay_method = ?, payment_id = ?, error = ''`,
		OrderStatusPaid, payMethod, paymentID)
}

func MarkOrderPrinted(ctx context.Context, tx *sql.Tx, id int64, jobID string, printJobRecordID int64) (bool, error) {
	return orderTransition(ctx, tx, id, []string{OrderStatusPrinting},
		`status = ?, job_id = ?, print_job_record_id = ?`,
		OrderStatusPrinted, jobID, printJobRecordID)
}

// SetOrderSubmitted 记下 CUPS 分配的任务号与对应的打印记录 id。
//
// 订单提交成功后停在 printing，靠 job_id 去 CUPS 查任务到底有没有打完。
// 没有它，「打印完成」就只能靠 Print-Job 的 successful-ok 去猜 ——
// 而那只是「已入队」，打印机连不上时任务会一直挂着，钱却已经收了。
//
// printRecordID 同时记下：打印记录在【提交时】就创建（状态 queued），
// 让用户在「打印记录」里立刻看得到自己刚发起的任务，而不是打完才出现。
func SetOrderSubmitted(ctx context.Context, tx *sql.Tx, id int64, jobID string, printRecordID int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE print_orders
		SET job_id = ?, print_job_record_id = ?, updated_at = ? WHERE id = ? AND status = ?`,
		jobID, printRecordID, nowUTC(), id, OrderStatusPrinting)
	return err
}

// ListPrintingOrders 列出仍在打印中、且早已提交的订单，供后台核对真实结果。
//
// updatedBefore 是宽限期：刚提交的订单还没来得及被 CUPS 处理，
// 立刻去查只会得到「还在排队」，白费一次往返。
func ListPrintingOrders(ctx context.Context, tx *sql.Tx, updatedBefore string, limit int) ([]PrintOrder, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+printOrderColumns+`
		FROM print_orders o
		JOIN users u ON u.id = o.user_id
		WHERE o.status = ? AND o.job_id <> '' AND o.updated_at < ?
		ORDER BY o.id LIMIT ?`, OrderStatusPrinting, updatedBefore, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	orders := []PrintOrder{}
	for rows.Next() {
		o, err := scanPrintOrder(rows)
		if err != nil {
			return nil, err
		}
		orders = append(orders, o)
	}
	return orders, rows.Err()
}

// ListPrintingOrdersByBatch 列出某个批次里仍在打印中、已过宽限期的订单。
//
// 供前端的批次轮询顺带核对用：让「打完到界面变样」不必等后台巡检的 30 秒。
func ListPrintingOrdersByBatch(ctx context.Context, tx *sql.Tx, batchID, updatedBefore string) ([]PrintOrder, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+printOrderColumns+`
		FROM print_orders o
		JOIN users u ON u.id = o.user_id
		WHERE o.batch_id = ? AND o.status = ? AND o.job_id <> '' AND o.updated_at < ?
		ORDER BY o.id`, batchID, OrderStatusPrinting, updatedBefore)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	orders := []PrintOrder{}
	for rows.Next() {
		o, err := scanPrintOrder(rows)
		if err != nil {
			return nil, err
		}
		orders = append(orders, o)
	}
	return orders, rows.Err()
}

// MarkOrderPrintUnconfirmed 把「送印了但结果查不到」的订单转人工。
//
// 【不退款】：任务可能已经打出来了，只是 cupsd 把记录清掉了。
// 自动退款会变成「纸出来了、钱也退了」。交管理员看纸箱判断。
func MarkOrderPrintUnconfirmed(ctx context.Context, tx *sql.Tx, id int64, reason string) (bool, error) {
	return orderTransition(ctx, tx, id, []string{OrderStatusPrinting},
		`status = ?, error = ?`, OrderStatusFailed, reason)
}

// MarkOrderFailed 把订单置为失败。from 由调用方给：IPP 失败是 printing → failed，
// 卡死回收也是 printing → failed，但语义（是否退款）由调用方决定。
func MarkOrderFailed(ctx context.Context, tx *sql.Tx, id int64, errText string, from ...string) (bool, error) {
	return orderTransition(ctx, tx, id, from, `status = ?, error = ?`, OrderStatusFailed, errText)
}

// MarkOrderRefunded 记录一次【管理员手工】退款：累加 refunded_cents 并置为 refunded。
//
// 只在 refunded_cents 还是 0 时生效 —— 这是防重复退款的第二道闸
// （第一道是 wallet_transactions 的 refund:<order_id> 唯一幂等键）。
func MarkOrderRefunded(ctx context.Context, tx *sql.Tx, id int64, amountCents int64) (bool, error) {
	return orderTransition(ctx, tx, id,
		[]string{OrderStatusPrinted, OrderStatusFailed},
		`status = ?, refunded_cents = refunded_cents + ?`, OrderStatusRefunded, amountCents)
}

// AddOrderRefunded 累加已退款金额，【不改状态】。
//
// 用于「打印失败自动退款」：那种情况状态保持 failed（表示这单没打成），
// 退款金额记在 refunded_cents 上。与管理员对已打印订单手工退款后的
// refunded 状态区分开 —— 两者的业务含义不同，混成一个状态会让
// 「有多少单是印坏了的」这个统计再也算不出来。
func AddOrderRefunded(ctx context.Context, tx *sql.Tx, id int64, amountCents int64) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE print_orders SET refunded_cents = refunded_cents + ?, updated_at = ? WHERE id = ?`,
		amountCents, nowUTC(), id)
	return err
}

// MarkOrderCancelled 用户主动取消一条待支付订单。
func MarkOrderCancelled(ctx context.Context, tx *sql.Tx, id int64) (bool, error) {
	return orderTransition(ctx, tx, id, []string{OrderStatusPendingPayment},
		`status = ?, error = ''`, OrderStatusCancelled)
}

// ─── 批次级迁移（批量下单） ────────────────────────────────────────────

// MarkPendingOrdersPaid 把整批待支付订单一次性推进到 paid，返回受影响的订单 id。
//
// 批量支付必须整批成功或整批失败 —— 不允许出现「同一笔支付里一半 paid
// 一半还是 pending_payment」的脏状态，那会导致结算金额与订单对不上。
func MarkPendingOrdersPaid(ctx context.Context, tx *sql.Tx, batchID string,
	payMethod string, paymentID int64) ([]int64, error) {
	ids, err := listOrderIDsByStatus(ctx, tx, batchID, OrderStatusPendingPayment)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	now := nowUTC()
	if _, err := tx.ExecContext(ctx, `UPDATE print_orders
		SET status = ?, pay_method = ?, payment_id = ?, error = '', updated_at = ?
		WHERE batch_id = ? AND status = ?`,
		OrderStatusPaid, payMethod, paymentID, now, batchID, OrderStatusPendingPayment); err != nil {
		return nil, err
	}
	return ids, nil
}

// AttachOrdersToPayment 把整批订单挂到同一个支付单上（在线支付发起时调用）。
func AttachOrdersToPayment(ctx context.Context, tx *sql.Tx, batchID string, paymentID int64) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE print_orders SET payment_id = ?, updated_at = ? WHERE batch_id = ?`,
		paymentID, nowUTC(), batchID)
	return err
}

// ListOrderIDsByPayment 返回一个支付单覆盖的全部订单 id。
// 结算成功后据此逐个发起打印。
func ListOrderIDsByPayment(ctx context.Context, tx *sql.Tx, paymentID int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM print_orders WHERE payment_id = ? ORDER BY id`, paymentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// MarkOrdersPaidByPayment 把某个支付单覆盖的订单推进到 paid，返回受影响的订单 id。
//
// 允许从 expired 迁移过来：用户扫码后订单过期、钱才到账的场景真实存在
// （支付宝的回调重试最长 25 小时），付了钱就该拿到东西。
// 文件若已被过期回收删掉，startPrintForOrder 会走退款兜底，
// 那种情况下用户拿回钱、我们不打纸，双方都不亏。
//
// 按 payment_id 而不是 batch_id 定位：一个支付单就是一次付款，
// 它覆盖哪些订单以 print_orders.payment_id 这个外键为准，
// 这样即使将来出现「一笔支付覆盖多个批次」也不会算错。
func MarkOrdersPaidByPayment(ctx context.Context, tx *sql.Tx, paymentID int64, payMethod string) ([]int64, error) {
	ids, err := queryOrderIDs(ctx, tx,
		`SELECT id FROM print_orders WHERE payment_id = ? AND status IN (?, ?) ORDER BY id`,
		paymentID, OrderStatusPendingPayment, OrderStatusExpired)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	if _, err := tx.ExecContext(ctx, `UPDATE print_orders
		SET status = ?, pay_method = ?, error = '', updated_at = ?
		WHERE payment_id = ? AND status IN (?, ?)`,
		OrderStatusPaid, payMethod, nowUTC(),
		paymentID, OrderStatusPendingPayment, OrderStatusExpired); err != nil {
		return nil, err
	}
	return ids, nil
}

// GetPendingOrdersByBatch 取批次中仍待支付的订单（含文件路径）。
// 取消/过期时据此删除文件。
func GetPendingOrdersByBatch(ctx context.Context, tx *sql.Tx, batchID string) ([]PrintOrder, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+printOrderColumns+`
		FROM print_orders o
		JOIN users u ON u.id = o.user_id
		WHERE o.batch_id = ? AND o.status = ?
		ORDER BY o.id`, batchID, OrderStatusPendingPayment)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	orders := []PrintOrder{}
	for rows.Next() {
		o, err := scanPrintOrder(rows)
		if err != nil {
			return nil, err
		}
		orders = append(orders, o)
	}
	return orders, rows.Err()
}

// MarkPendingOrdersExpired 把整批待支付订单置为 expired，返回被影响的订单。
func MarkPendingOrdersExpired(ctx context.Context, tx *sql.Tx, batchID string) ([]PrintOrder, error) {
	orders, err := GetPendingOrdersByBatch(ctx, tx, batchID)
	if err != nil {
		return nil, err
	}
	if len(orders) == 0 {
		return nil, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE print_orders
		SET status = ?, updated_at = ? WHERE batch_id = ? AND status = ?`,
		OrderStatusExpired, nowUTC(), batchID, OrderStatusPendingPayment); err != nil {
		return nil, err
	}
	return orders, nil
}

// ─── 后台扫描 ──────────────────────────────────────────────────────────

// ListStalePaidOrders 捞出已支付但迟迟没进入打印的订单。
// 覆盖两种情形：回调里 go 出去的 goroutine 随进程重启丢了；回调根本没来。
func ListStalePaidOrders(ctx context.Context, tx *sql.Tx, before string, limit int) ([]int64, error) {
	return queryOrderIDs(ctx, tx,
		`SELECT id FROM print_orders WHERE status = ? AND updated_at < ? ORDER BY id LIMIT ?`,
		OrderStatusPaid, before, limit)
}

// ListStalePrintingOrders 捞出卡在 printing 超过阈值的订单。
// 这类订单结果未知（钱可能已经印出来了），只标记失败并转人工，不自动退款。
func ListStalePrintingOrders(ctx context.Context, tx *sql.Tx, before string, limit int) ([]int64, error) {
	return queryOrderIDs(ctx, tx,
		`SELECT id FROM print_orders WHERE status = ? AND updated_at < ? ORDER BY id LIMIT ?`,
		OrderStatusPrinting, before, limit)
}

// ListExpiredPendingBatches 返回整批都已过期的 batch_id。
//
// 按 batch 而不是按 order 扫描：同一批必须一起过期，否则会出现
// 「同一批里一半过期、一半还能付」的脏状态。
func ListExpiredPendingBatches(ctx context.Context, tx *sql.Tx, now string, limit int) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT batch_id FROM print_orders
		WHERE status = ? AND expires_at <> '' AND expires_at < ?
		GROUP BY batch_id
		ORDER BY MIN(id)
		LIMIT ?`, OrderStatusPendingPayment, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	batches := []string{}
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		batches = append(batches, b)
	}
	return batches, rows.Err()
}

// ─── 文件生命周期 ──────────────────────────────────────────────────────

// OrderFileRef 是订单持有的两个文件路径。
// print_path 可能与 stored_path 相同（PDF 分支不生成转换产物）。
type OrderFileRef struct {
	StoredPath string
	PrintPath  string
	Source     string
}

// ListActiveOrderFiles 返回所有未终结订单持有的文件路径。
// maintenance 清理时必须跳过这些路径 —— 它按 print_jobs 的保留期删文件，
// 完全不知道订单的存在，会把已支付待打印的文件一起删掉。
func ListActiveOrderFiles(ctx context.Context, tx *sql.Tx) ([]OrderFileRef, error) {
	rows, err := tx.QueryContext(ctx, `SELECT stored_path, print_path, source
		FROM print_orders WHERE status IN (?, ?, ?)`,
		OrderStatusPendingPayment, OrderStatusPaid, OrderStatusPrinting)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	refs := []OrderFileRef{}
	for rows.Next() {
		var r OrderFileRef
		if err := rows.Scan(&r.StoredPath, &r.PrintPath, &r.Source); err != nil {
			return nil, err
		}
		refs = append(refs, r)
	}
	return refs, rows.Err()
}

// ListTerminalOrderFiles 返回终态订单持有的文件路径，供 maintenance 回收。
// cutoff 为空表示「全部终态订单」。订单行本身是财务记录，永不删除，只回收文件。
func ListTerminalOrderFiles(ctx context.Context, tx *sql.Tx, cutoff string) ([]OrderFileRef, error) {
	query := `SELECT stored_path, print_path, source FROM print_orders
		WHERE status IN (?, ?, ?, ?, ?)`
	args := []interface{}{
		OrderStatusPrinted, OrderStatusFailed, OrderStatusRefunded,
		OrderStatusExpired, OrderStatusCancelled,
	}
	if cutoff != "" {
		query += " AND updated_at < ?"
		args = append(args, cutoff)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	refs := []OrderFileRef{}
	for rows.Next() {
		var r OrderFileRef
		if err := rows.Scan(&r.StoredPath, &r.PrintPath, &r.Source); err != nil {
			return nil, err
		}
		refs = append(refs, r)
	}
	return refs, rows.Err()
}

// ─── 工具 ──────────────────────────────────────────────────────────────

func listOrderIDsByStatus(ctx context.Context, tx *sql.Tx, batchID, status string) ([]int64, error) {
	return queryOrderIDs(ctx, tx,
		`SELECT id FROM print_orders WHERE batch_id = ? AND status = ? ORDER BY id`,
		batchID, status)
}

func queryOrderIDs(ctx context.Context, tx *sql.Tx, query string, args ...interface{}) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
