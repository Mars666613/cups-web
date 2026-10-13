package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// 订单状态。
//
// 终态：printed / failed / refunded / expired / cancelled。
// 只有非终态的订单才会保护其持有的文件不被 maintenance 回收。
const (
	OrderStatusPendingPayment = "pending_payment"
	OrderStatusPaid           = "paid"
	// OrderStatusHeld 是刷卡取件的待取件态：文件已经转换好并落盘，
	// 但【还没有送 CUPS】。刷卡（或过期）才会离开这个状态。
	//
	// ⚠️ 它【不是】终态，必须出现在 ListActiveOrderFiles 的保护名单里，
	// 否则 maintenance 会把员工还没取件的文件当过期文件删掉。
	OrderStatusHeld      = "held"
	OrderStatusPrinting  = "printing"
	OrderStatusPrinted   = "printed"
	OrderStatusFailed    = "failed"
	OrderStatusRefunded  = "refunded"
	OrderStatusExpired   = "expired"
	OrderStatusCancelled = "cancelled"
)

// ⚠️ 这里原先有一个 OrderStatusesActive 切片，自称是「仍在占用文件的状态
// 集合」，但【全仓没有任何地方读它】—— 真正决定文件保护的是下面
// ListActiveOrderFiles 里硬编码的那条 IN 列表。两处并存只会误导下一个人
// 照着死代码去加状态、以为保护到位了、实际文件照样被删。已删除。
//
// 加新状态时，要改的是 ListActiveOrderFiles。

// 订单来源。
const (
	OrderSourceWeb       = "web"
	OrderSourceReprint   = "reprint"
	OrderSourceScheduled = "scheduled"
	// OrderSourcePull 是刷卡取件：提交时只暂存，刷卡才送印。
	// 金额恒为 0，永不进入任何退款路径（见 startPullPrintForOrder）。
	OrderSourcePull = "pull"
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
	var id int64
	err := tx.QueryRowContext(ctx, `INSERT INTO print_orders (
		user_id, source, reprint_of, scheduled_id, batch_id,
		printer_uri, filename, stored_path, print_path, print_mime,
		pages, billed_pages,
		is_duplex, is_color, copies, orientation, paper_size, paper_type, media_source,
		print_scaling, req_print_scaling, page_range, page_set, req_page_set,
		mirror, watermark_text, number_up, number_up_layout, page_border,
		unit_price_cents, amount_cents, refunded_cents, pay_method,
		status, payment_id, job_id, print_job_record_id, error, expires_at,
		created_at, updated_at
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30, $31, $32, $33, $34, $35, $36, $37, $38, $39, $40, $41)
	RETURNING id`,
		o.UserID, o.Source, o.ReprintOf, o.ScheduledID, o.BatchID,
		o.PrinterURI, o.Filename, o.StoredPath, o.PrintPath, o.PrintMime,
		o.Pages, o.BilledPages,
		o.IsDuplex, o.IsColor, o.Copies, o.Orientation, o.PaperSize, o.PaperType, o.MediaSource,
		o.PrintScaling, o.ReqPrintScaling, o.PageRange, o.PageSet, o.ReqPageSet,
		o.Mirror, o.WatermarkText, o.NumberUp, o.NumberUpLayout, o.PageBorder,
		o.UnitPriceCents, o.AmountCents, o.RefundedCents, o.PayMethod,
		o.Status, o.PaymentID, o.JobID, o.PrintJobRecordID, o.Error, o.ExpiresAt,
		o.CreatedAt, o.UpdatedAt,
	).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func GetOrderByID(ctx context.Context, tx *sql.Tx, id int64) (PrintOrder, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+printOrderColumns+`
		FROM print_orders o
		JOIN users u ON u.id = o.user_id
		WHERE o.id = $1`, id)
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
	// 同 ListPrintRecords：追加实参与取占位符绑成一步。
	arg := func(v interface{}) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if filter.Username != "" {
		conds = append(conds, "u.username = "+arg(filter.Username))
	}
	if filter.Status != "" {
		conds = append(conds, "o.status = "+arg(filter.Status))
	}
	if filter.StartAt != "" {
		conds = append(conds, "o.created_at >= "+arg(filter.StartAt))
	}
	if filter.EndAt != "" {
		conds = append(conds, "o.created_at <= "+arg(filter.EndAt))
	}
	query := fmt.Sprintf(`SELECT `+printOrderColumns+`
		FROM print_orders o
		JOIN users u ON u.id = o.user_id
		WHERE %s
		ORDER BY o.created_at DESC, o.id DESC`, strings.Join(conds, " AND "))
	if filter.Limit > 0 {
		query += " LIMIT " + arg(filter.Limit)
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
		WHERE o.batch_id = $1
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
		`SELECT COUNT(1) FROM print_orders WHERE batch_id = $1 AND user_id = $2`,
		batchID, userID).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// ⚠️ `::bigint` 不能省。PostgreSQL 的 SUM(bigint) 返回的是 numeric 而不是
// bigint，扫进 sql.NullInt64 会失败 —— 而这是【余额支付的主路径】，
// 挂了等于余额支付全不可用。任何 SUM/AVG 都要显式 cast 回 bigint。
const sumPendingAmountSQL = `SELECT SUM(amount_cents)::bigint FROM print_orders
	WHERE batch_id = $1 AND status = $2`

// SumPendingAmountByBatch 求一个批次中「待支付」订单的金额合计。
//
// 只读地看一眼合计值 —— 发起在线支付时用它算「该收多少钱」。
// 这个值只用于展示与建单，真正的扣款另有条件，所以这里不加锁。
func SumPendingAmountByBatch(ctx context.Context, tx *sql.Tx, batchID string) (int64, error) {
	var total sql.NullInt64
	if err := tx.QueryRowContext(ctx, sumPendingAmountSQL,
		batchID, OrderStatusPendingPayment).Scan(&total); err != nil {
		return 0, err
	}
	return total.Int64, nil
}

// SumPendingAmountByBatchLocked 与上面相同，但给这批订单加行锁。
//
// 给「先读合计、再据此扣款」的路径用（余额支付）：不加锁的话，
// 读合计与扣款之间只要有人取消了其中一笔订单，那条 UPDATE 会跳过它，
// 而钱已经按【取消前】的总额扣走了 —— 用户为没打出来的纸付了钱。
//
// 🚫 只能在【写事务】里调用，PG 对只读事务里的 FOR UPDATE 直接报错。
// 所以这个锁不能并进上面那个函数 —— startOnlinePayment 是只读事务里调的。
func SumPendingAmountByBatchLocked(ctx context.Context, tx *sql.Tx, batchID string) (int64, error) {
	var total sql.NullInt64
	if err := tx.QueryRowContext(ctx, sumPendingAmountSQL+" FOR UPDATE",
		batchID, OrderStatusPendingPayment).Scan(&total); err != nil {
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
	args := make([]interface{}, 0, len(setArgs)+len(from)+2)
	// ⚠️ $N 必须按【实参 append 顺序】编号，而 setSQL 是调用方传进来的、
	// 里面已经写着 $1..$k（与 setArgs 一一对应）。所以先把 setArgs 放进去，
	// 计数器就自然从 $k+1 接着走。
	//
	// 🚫 不要另起一个从 1 开始的编号，也不要数 setSQL 里有几个 `?` ——
	// 数错了 setSQL 与 status IN 列表会各自绑到错误的实参上，表现是
	// 【静默迁移到了错误的状态集】，不报任何错。
	arg := func(v interface{}) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	args = append(args, setArgs...)
	updatedPh := arg(nowUTC())
	idPh := arg(id)
	placeholders := make([]string, len(from))
	for i, s := range from {
		placeholders[i] = arg(s)
	}
	query := fmt.Sprintf(
		`UPDATE print_orders SET %s, updated_at = %s WHERE id = %s AND status IN (%s)`,
		setSQL, updatedPh, idPh, strings.Join(placeholders, ","))
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
		`status = $1`, OrderStatusPrinting)
}

// ─── 刷卡取件（拉式打印）──────────────────────────────────────────────

// PullClaimMarker 是「已领取、送印还没开始」时写在 error 列上的标记。
//
// 它存在的唯一理由是让 ListOrphanPullOrders 能【精确地】认出该回退的孤儿。
// 光看「printing 且 job_id 为空」不够：送印成功但 job_id 回写失败时也是这个
// 形状，那时回退会让员工下次刷卡【重复出纸】。带上这个标记后，只有
// 「领了但 IPP 请求还没发出去」才匹配 —— 那正是可以安全重试的那种。
//
// 提交成功后由 SetOrderSubmitted 清空，不会长期挂在订单上。
const PullClaimMarker = "刷卡取件：已领取，等待送印"

// ClaimHeldOrdersForPrinter 把某人在某台打印机上【全部】待取件的订单
// 原子领取到 printing，返回领取到的订单 id。
//
// 这是刷卡取件的「刷一次只印一次」闸门，作用与 ClaimOrderForPrint 相同，
// 区别是匹配【一批】而不是一条 —— 员工刷卡时把他所有待取件的任务一起打出来。
//
// 幂等的根基就是 WHERE 里的 status = 'held'：第二次刷卡匹配 0 行；
// 两台终端并发时行级锁会串行化，每行只有一个 UPDATE 能拿到。
// 多实例部署同样安全 —— 这里不依赖任何进程内的锁
// （对比：wallet_refund.go 的 userRefundLock 是进程内 mutex，那种才有多实例隐患）。
//
// ⚠️ printer_uri 是硬约束：任务在哪台提交就只能在哪台取。这是产品决定
// （见 docs/pull-printing.md），换来的是打印参数绝不会与目标机不匹配。
func ClaimHeldOrdersForPrinter(ctx context.Context, tx *sql.Tx, userID int64, printerURI string) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `UPDATE print_orders
		SET status = $1, error = $2, updated_at = $3
		WHERE user_id = $4 AND printer_uri = $5 AND status = $6
		RETURNING id`,
		OrderStatusPrinting, PullClaimMarker, nowUTC(), userID, printerURI, OrderStatusHeld)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// PG 的 UPDATE 不支持 ORDER BY，出纸顺序只能在 Go 里定死，
	// 让多份文件按用户提交的先后顺序出来。
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

// RevertOrderToHeld 把送印失败的持件订单退回待取件，让员工可以重刷。
//
// 🚫 这条路【绝不能】走 failOrderAndRefund：那个会把订单标成 failed 并尝试
// 退款。员工压根没付钱，任务也不该丢 —— 退回 held 可以重刷、可以等打印机
// 恢复、到期还有 TTL 兜底，失败语义是完全可逆的。
// （注意：不能拿「金额为 0 所以 failOrderAndRefund 不会退款」当理由偷懒 ——
// 它在退款之前就已经把订单标成 failed 了，任务照样丢。）
//
// ⚠️ job_id = ” 是硬条件，不是保险：任务一旦真的进了 CUPS，纸有没有出来
// 就存疑了，再退回 held 会让下次刷卡【重复出纸】。那种情况必须标失败并留证
// 给人判断（见 order_worker.go 里 reconcilePrintingOrder 的 pull 分支）。
func RevertOrderToHeld(ctx context.Context, tx *sql.Tx, id int64, reason string) (bool, error) {
	res, err := tx.ExecContext(ctx, `UPDATE print_orders
		SET status = $1, error = $2, updated_at = $3
		WHERE id = $4 AND status = $5 AND job_id = ''`,
		OrderStatusHeld, reason, nowUTC(), id, OrderStatusPrinting)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// MarkHeldOrdersExpired 把超过取件时限的持件订单标为过期。幂等
// （WHERE 里带 held，重复调用第二次返回 false）。
// 文件由调用方回收，回收时机见 order_worker.go 的 expireHeldOrders。
func MarkHeldOrdersExpired(ctx context.Context, tx *sql.Tx, id int64, reason string) (bool, error) {
	return orderTransition(ctx, tx, id, []string{OrderStatusHeld},
		`status = $1, error = $2`, OrderStatusExpired, reason)
}

// ListHeldOrdersByUser 列某人所有待取件的订单，按提交顺序。
//
// 员工侧的「待取件」列表用它，【不是】查 print_jobs：后者受 save_history
// 开关影响（关掉时压根不写记录），而那会让员工看不到自己待取的任务 ——
// 恰恰是本功能的核心诉求。
func ListHeldOrdersByUser(ctx context.Context, tx *sql.Tx, userID int64) ([]PrintOrder, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+printOrderColumns+`
		FROM print_orders o
		JOIN users u ON u.id = o.user_id
		WHERE o.user_id = $1 AND o.status = $2
		ORDER BY o.id`, userID, OrderStatusHeld)
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

// CancelHeldOrder 撤销一条还没取件的任务（仅限本人、仅限 held）。
//
// user_id 写在 WHERE 里是做归属校验 —— 少了它，任何一个登录用户都能凭
// 订单 id 撤销别人的任务。返回 false 表示「不是你的、或者已经不在 held」
// （可能刚被刷卡领走了），两种情况都不该被当成错误。
func CancelHeldOrder(ctx context.Context, tx *sql.Tx, id, userID int64) (bool, error) {
	res, err := tx.ExecContext(ctx, `UPDATE print_orders
		SET status = $1, updated_at = $2
		WHERE id = $3 AND user_id = $4 AND status = $5`,
		OrderStatusCancelled, nowUTC(), id, userID, OrderStatusHeld)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// MarkOrderPaid 把订单推进到已支付。
//
// 允许从 expired 迁移过来：用户扫了码、订单过期后才到账的场景真实存在，
// 付了钱就该拿到东西。文件若已被过期清理删掉，startPrintForOrder 会走退款兜底。
func MarkOrderPaid(ctx context.Context, tx *sql.Tx, id int64, payMethod string, paymentID int64) (bool, error) {
	return orderTransition(ctx, tx, id,
		[]string{OrderStatusPendingPayment, OrderStatusExpired},
		`status = $1, pay_method = $2, payment_id = $3, error = ''`,
		OrderStatusPaid, payMethod, paymentID)
}

func MarkOrderPrinted(ctx context.Context, tx *sql.Tx, id int64, jobID string, printJobRecordID int64) (bool, error) {
	return orderTransition(ctx, tx, id, []string{OrderStatusPrinting},
		`status = $1, job_id = $2, print_job_record_id = $3`,
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
	// error 一并清空：既然成功提交到 CUPS 了，之前那条错误（含刷卡取件的
	// 「已领取待送印」标记，见 PullClaimMarker）就不该再挂在订单上。
	_, err := tx.ExecContext(ctx, `UPDATE print_orders
		SET job_id = $1, print_job_record_id = $2, error = '', updated_at = $3
		WHERE id = $4 AND status = $5`,
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
		WHERE o.status = $1 AND o.job_id <> '' AND o.updated_at < $2
		ORDER BY o.id LIMIT $3`, OrderStatusPrinting, updatedBefore, limit)
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
		WHERE o.batch_id = $1 AND o.status = $2 AND o.job_id <> '' AND o.updated_at < $3
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
		`status = $1, error = $2`, OrderStatusFailed, reason)
}

// MarkOrderFailed 把订单置为失败。from 由调用方给：IPP 失败是 printing → failed，
// 卡死回收也是 printing → failed，但语义（是否退款）由调用方决定。
func MarkOrderFailed(ctx context.Context, tx *sql.Tx, id int64, errText string, from ...string) (bool, error) {
	return orderTransition(ctx, tx, id, from, `status = $1, error = $2`, OrderStatusFailed, errText)
}

// MarkOrderRefunded 记录一次【管理员手工】退款：累加 refunded_cents 并置为 refunded。
//
// 只在 refunded_cents 还是 0 时生效 —— 这是防重复退款的第二道闸
// （第一道是 wallet_transactions 的 refund:<order_id> 唯一幂等键）。
func MarkOrderRefunded(ctx context.Context, tx *sql.Tx, id int64, amountCents int64) (bool, error) {
	return orderTransition(ctx, tx, id,
		[]string{OrderStatusPrinted, OrderStatusFailed},
		`status = $1, refunded_cents = refunded_cents + $2`, OrderStatusRefunded, amountCents)
}

// AddOrderRefunded 累加已退款金额，【不改状态】。
//
// 用于「打印失败自动退款」：那种情况状态保持 failed（表示这单没打成），
// 退款金额记在 refunded_cents 上。与管理员对已打印订单手工退款后的
// refunded 状态区分开 —— 两者的业务含义不同，混成一个状态会让
// 「有多少单是印坏了的」这个统计再也算不出来。
func AddOrderRefunded(ctx context.Context, tx *sql.Tx, id int64, amountCents int64) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE print_orders SET refunded_cents = refunded_cents + $1, updated_at = $2 WHERE id = $3`,
		amountCents, nowUTC(), id)
	return err
}

// MarkOrderCancelled 用户主动取消一条待支付订单。
func MarkOrderCancelled(ctx context.Context, tx *sql.Tx, id int64) (bool, error) {
	return orderTransition(ctx, tx, id, []string{OrderStatusPendingPayment},
		`status = $1, error = ''`, OrderStatusCancelled)
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
		SET status = $1, pay_method = $2, payment_id = $3, error = '', updated_at = $4
		WHERE batch_id = $5 AND status = $6`,
		OrderStatusPaid, payMethod, paymentID, now, batchID, OrderStatusPendingPayment); err != nil {
		return nil, err
	}
	return ids, nil
}

// AttachOrdersToPayment 把整批订单挂到同一个支付单上（在线支付发起时调用）。
func AttachOrdersToPayment(ctx context.Context, tx *sql.Tx, batchID string, paymentID int64) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE print_orders SET payment_id = $1, updated_at = $2 WHERE batch_id = $3`,
		paymentID, nowUTC(), batchID)
	return err
}

// ListOrderIDsByPayment 返回一个支付单覆盖的全部订单 id。
// 结算成功后据此逐个发起打印。
func ListOrderIDsByPayment(ctx context.Context, tx *sql.Tx, paymentID int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM print_orders WHERE payment_id = $1 ORDER BY id`, paymentID)
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
		`SELECT id FROM print_orders WHERE payment_id = $1 AND status IN ($2, $3) ORDER BY id FOR UPDATE`,
		paymentID, OrderStatusPendingPayment, OrderStatusExpired)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	if _, err := tx.ExecContext(ctx, `UPDATE print_orders
		SET status = $1, pay_method = $2, error = '', updated_at = $3
		WHERE payment_id = $4 AND status IN ($5, $6)`,
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
		WHERE o.batch_id = $1 AND o.status = $2
		ORDER BY o.id FOR UPDATE OF o`, batchID, OrderStatusPendingPayment)
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
		SET status = $1, updated_at = $2 WHERE batch_id = $3 AND status = $4`,
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
		`SELECT id FROM print_orders WHERE status = $1 AND updated_at < $2 ORDER BY id LIMIT $3`,
		OrderStatusPaid, before, limit)
}

// ListExpiredHeldOrders 捞出过了取件时限还没被取走的持件订单。
//
// 与 ListStalePaidOrders 的区别：那个是「该送印却没送」的异常，
// 这个是「员工一直没来取」的正常结局 —— 处理方式也不同（过期删文件，
// 不是失败退款）。
func ListExpiredHeldOrders(ctx context.Context, tx *sql.Tx, now string, limit int) ([]int64, error) {
	return queryOrderIDs(ctx, tx,
		`SELECT id FROM print_orders
		 WHERE status = $1 AND expires_at <> '' AND expires_at < $2
		 ORDER BY id LIMIT $3`,
		OrderStatusHeld, now, limit)
}

// ListOrphanPullOrders 捞出「已领取、但 job_id 还没回写」的持件订单。
//
// 这是取件模式【独有】的一道兜底。释放流程是「先原子领取（held→printing），
// 再在事务外送印」——若进程恰好在两步之间挂掉，订单会永远停在 printing
// 且没有 job_id。而现有的核对链路只捞 job_id <> ” 的订单
// （见 ListPrintingOrders），这类孤儿【没有任何回收路径】，表现是
// 「任务卡死：不打、不回退、界面上永远显示打印中」。
//
// ⚠️ 判据里的 error = PullClaimMarker 是关键，不能退化成「job_id 为空」：
// 送印成功、但 job_id 回写失败时也是「printing 且 job_id 为空」的形状，
// 那时回退会让员工下次刷卡重复出纸。带上标记后只有「领了但没发出去」匹配。
//
// source='pull' 同样必须：计费订单在同一窗口挂掉也会变孤儿，但那是另一条
// 链路的历史遗留问题，不在这次改动范围内，不能顺手改。
//
// 调用方拿到 id 后走 RevertOrderToHeld（它自己也带 job_id 为空的条件）。
func ListOrphanPullOrders(ctx context.Context, tx *sql.Tx, before string, limit int) ([]int64, error) {
	return queryOrderIDs(ctx, tx,
		`SELECT id FROM print_orders
		 WHERE status = $1 AND job_id = '' AND source = $2 AND error = $3 AND updated_at < $4
		 ORDER BY id LIMIT $5`,
		OrderStatusPrinting, OrderSourcePull, PullClaimMarker, before, limit)
}

// ListExpiredPendingBatches 返回整批都已过期的 batch_id。
//
// 按 batch 而不是按 order 扫描：同一批必须一起过期，否则会出现
// 「同一批里一半过期、一半还能付」的脏状态。
func ListExpiredPendingBatches(ctx context.Context, tx *sql.Tx, now string, limit int) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT batch_id FROM print_orders
		WHERE status = $1 AND expires_at <> '' AND expires_at < $2
		GROUP BY batch_id
		ORDER BY MIN(id)
		LIMIT $3`, OrderStatusPendingPayment, now, limit)
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
//
// 🚨 【新增订单状态时，这里是必须同步的地方】——不是别处。
// 它和 ListTerminalOrderFiles 是一对互补的名单：一个列「还在用的」，
// 一个列「可以回收的」。状态落在两者的缝隙里，文件就会被静默删掉。
//
// held 尤其要注意：它的文件是【员工还没来取的打印件】。漏了它，
// 表现是「员工刷卡时提示文件不存在」——而根因是一小时前的一次清理。
func ListActiveOrderFiles(ctx context.Context, tx *sql.Tx) ([]OrderFileRef, error) {
	rows, err := tx.QueryContext(ctx, `SELECT stored_path, print_path, source
		FROM print_orders WHERE status IN ($1, $2, $3, $4)`,
		OrderStatusPendingPayment, OrderStatusPaid, OrderStatusHeld, OrderStatusPrinting)
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
		WHERE status IN ($1, $2, $3, $4, $5)`
	args := []interface{}{
		OrderStatusPrinted, OrderStatusFailed, OrderStatusRefunded,
		OrderStatusExpired, OrderStatusCancelled,
	}
	if cutoff != "" {
		query += fmt.Sprintf(" AND updated_at < $%d", len(args)+1)
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

// listOrderIDsByStatus 只被 MarkPendingOrdersPaid 调用（写事务）。
//
// FOR UPDATE 是必须的：调用方拿到这批 id 之后就要按它们批量改状态。
// 不加锁的话，读到 id 之后若有并发取消改了其中一单，UPDATE 会跳过它，
// 而返回给上层的 id 集合里还有 —— 上层据此去送印，就送了一份已取消的订单。
func listOrderIDsByStatus(ctx context.Context, tx *sql.Tx, batchID, status string) ([]int64, error) {
	return queryOrderIDs(ctx, tx,
		`SELECT id FROM print_orders WHERE batch_id = $1 AND status = $2 ORDER BY id FOR UPDATE`,
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
