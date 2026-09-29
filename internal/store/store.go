package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

const (
	SettingRetentionDays  = "retention_days"
	SettingSaveHistory    = "save_history"
	SettingMaxPagesPerJob = "max_pages_per_job"
	SettingMaxUploadBytes = "max_upload_bytes"
	SettingCustomCSS      = "custom_css"
	SettingGuestMode      = "guest_mode"

	// 计费相关（见 docs 中的收费功能设计）。
	// 金额一律以「分」为单位的整数存储，前端负责换算成元展示，
	// 与 max_upload_bytes 存字节、前端显示 MB 的习惯一致。
	SettingBillingEnabled  = "billing_enabled"      // "0" 关闭时三个打印入口行为与改造前完全一致
	SettingOrderTTLMinutes = "order_ttl_minutes"    // "15" 待支付订单有效期（分钟）
	SettingPriceFallback   = "price_fallback_cents" // "0" 价目未命中时的兜底单价
	SettingPaymentMethods  = "payment_methods"      // "" 启用的在线渠道，逗号分隔，如 "alipay,wechat"
	SettingSiteBaseURL     = "site_base_url"        // "" 回调地址前缀（真实渠道接入时用）
)

type Store struct {
	DB *sql.DB
}

func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)

	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode = WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set journal_mode: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set foreign_keys: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}

	s := &Store{DB: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.DB.Close()
}

func (s *Store) WithTx(ctx context.Context, readOnly bool, fn func(*sql.Tx) error) error {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: readOnly})
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			role TEXT NOT NULL,
			protected INTEGER NOT NULL DEFAULT 0,
			contact_name TEXT,
			phone TEXT,
			email TEXT,
			balance_cents INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS scheduled_prints (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			printer_uri TEXT NOT NULL,
			filename TEXT NOT NULL,
			stored_path TEXT NOT NULL,
			is_duplex INTEGER NOT NULL DEFAULT 0,
			is_color INTEGER NOT NULL DEFAULT 1,
			copies INTEGER NOT NULL DEFAULT 1,
			orientation TEXT NOT NULL DEFAULT 'portrait',
			paper_size TEXT NOT NULL DEFAULT 'A4',
			paper_type TEXT NOT NULL DEFAULT 'plain',
			media_source TEXT NOT NULL DEFAULT 'auto',
			print_scaling TEXT NOT NULL DEFAULT 'fit',
			page_range TEXT NOT NULL DEFAULT '',
			page_set TEXT NOT NULL DEFAULT 'all',
			mirror INTEGER NOT NULL DEFAULT 0,
			watermark_text TEXT NOT NULL DEFAULT '',
			number_up INTEGER NOT NULL DEFAULT 1,
			number_up_layout TEXT NOT NULL DEFAULT 'lrtb',
			page_border TEXT NOT NULL DEFAULT 'none',
			schedule_type TEXT NOT NULL DEFAULT 'once',
			schedule_time TEXT NOT NULL DEFAULT '09:00',
			schedule_weekday INTEGER NOT NULL DEFAULT 0,
			schedule_day INTEGER NOT NULL DEFAULT 1,
			run_at TEXT NOT NULL DEFAULT '',
			next_run_at TEXT NOT NULL,
			last_run_at TEXT NOT NULL DEFAULT '',
			last_status TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_scheduled_prints_next
			ON scheduled_prints(enabled, next_run_at)`,
		`CREATE TABLE IF NOT EXISTS print_jobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			printer_uri TEXT NOT NULL,
			filename TEXT NOT NULL,
			stored_path TEXT NOT NULL,
			pages INTEGER NOT NULL,
			job_id TEXT,
			status TEXT NOT NULL,
			is_duplex INTEGER NOT NULL DEFAULT 0,
			is_color INTEGER NOT NULL DEFAULT 1,
			copies INTEGER NOT NULL DEFAULT 1,
			orientation TEXT NOT NULL DEFAULT 'portrait',
			paper_size TEXT NOT NULL DEFAULT 'A4',
			paper_type TEXT NOT NULL DEFAULT 'plain',
			media_source TEXT NOT NULL DEFAULT 'auto',
			print_scaling TEXT NOT NULL DEFAULT 'fit',
			page_range TEXT NOT NULL DEFAULT '',
			page_set TEXT NOT NULL DEFAULT 'all',
			mirror INTEGER NOT NULL DEFAULT 0,
			watermark_text TEXT NOT NULL DEFAULT '',
			number_up INTEGER NOT NULL DEFAULT 1,
			number_up_layout TEXT NOT NULL DEFAULT 'lrtb',
			page_border TEXT NOT NULL DEFAULT 'none',
			created_at TEXT NOT NULL,
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		// 单价查找表：单价(是否彩色, 纸张尺寸, 纸张类型)，单位「分」。
		// 全量替换式配置（PUT 时 DELETE + INSERT），量级只有几十行，
		// 所以用 UNIQUE 索引保证维度组合唯一，不需要额外的版本号。
		`CREATE TABLE IF NOT EXISTS price_rules (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			is_color INTEGER NOT NULL DEFAULT 0,
			paper_size TEXT NOT NULL,
			paper_type TEXT NOT NULL,
			unit_price_cents INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_price_rules_dim
			ON price_rules(is_color, paper_size, paper_type)`,
		// 打印订单：一次「要打印的东西」= 一条订单。
		//
		// 订单必须自带完整参数快照，不能引用 print_jobs —— 管理员关掉 save_history
		// 时那张表根本不写记录（见 print_handlers.go），而账目不能因此消失。
		// 列里存的是「最终发给 IPP 的值」（缩放已解析、even-reverse 已消化、
		// 手动双面已强制单面），支付后由 startPrintForOrder 直接重放，不做任何转换。
		//
		// batch_id 用于批量下单合并支付：一笔 payments 覆盖同一 batch_id 的多条订单。
		`CREATE TABLE IF NOT EXISTS print_orders (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			source TEXT NOT NULL DEFAULT 'web',
			reprint_of INTEGER NOT NULL DEFAULT 0,
			scheduled_id INTEGER NOT NULL DEFAULT 0,
			batch_id TEXT NOT NULL DEFAULT '',

			printer_uri TEXT NOT NULL,
			filename TEXT NOT NULL,
			stored_path TEXT NOT NULL,
			print_path TEXT NOT NULL,
			print_mime TEXT NOT NULL DEFAULT '',

			pages INTEGER NOT NULL DEFAULT 0,
			billed_pages INTEGER NOT NULL DEFAULT 0,

			is_duplex INTEGER NOT NULL DEFAULT 0,
			is_color INTEGER NOT NULL DEFAULT 1,
			copies INTEGER NOT NULL DEFAULT 1,
			orientation TEXT NOT NULL DEFAULT 'portrait',
			paper_size TEXT NOT NULL DEFAULT 'A4',
			paper_type TEXT NOT NULL DEFAULT 'plain',
			media_source TEXT NOT NULL DEFAULT 'auto',
			print_scaling TEXT NOT NULL DEFAULT 'fit',
			req_print_scaling TEXT NOT NULL DEFAULT 'fit',
			page_range TEXT NOT NULL DEFAULT '',
			page_set TEXT NOT NULL DEFAULT 'all',
			req_page_set TEXT NOT NULL DEFAULT 'all',
			mirror INTEGER NOT NULL DEFAULT 0,
			watermark_text TEXT NOT NULL DEFAULT '',
			number_up INTEGER NOT NULL DEFAULT 1,
			number_up_layout TEXT NOT NULL DEFAULT 'lrtb',
			page_border TEXT NOT NULL DEFAULT 'none',

			unit_price_cents INTEGER NOT NULL DEFAULT 0,
			amount_cents INTEGER NOT NULL DEFAULT 0,
			refunded_cents INTEGER NOT NULL DEFAULT 0,
			pay_method TEXT NOT NULL DEFAULT '',

			status TEXT NOT NULL,
			payment_id INTEGER NOT NULL DEFAULT 0,
			job_id TEXT NOT NULL DEFAULT '',
			print_job_record_id INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT '',
			expires_at TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_print_orders_user_created
			ON print_orders(user_id, created_at)`,
		// 补偿扫描用：捞出卡在 paid/printing 的订单。
		`CREATE INDEX IF NOT EXISTS idx_print_orders_status_updated
			ON print_orders(status, updated_at)`,
		// 过期扫描用。
		`CREATE INDEX IF NOT EXISTS idx_print_orders_status_expires
			ON print_orders(status, expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_print_orders_batch ON print_orders(batch_id)`,
		// 支付单：一次「发起支付」= 一行。一个支付单可以覆盖同一批次的多个订单
		// （靠 print_orders.payment_id 反向引用，不建中间表）。
		//
		// 余额支付也写一行（provider='balance'，创建即 paid），这样「一笔支付
		// 覆盖 N 个订单」在余额与在线两种方式下形状完全统一，退款对账有统一锚点。
		//
		// out_trade_no 唯一是幂等的第一层：渠道重复投递回调时靠它识别。
		`CREATE TABLE IF NOT EXISTS payments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			purpose TEXT NOT NULL DEFAULT 'print',
			provider TEXT NOT NULL,
			out_trade_no TEXT NOT NULL,
			amount_cents INTEGER NOT NULL,
			refunded_cents INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL,
			transaction_id TEXT NOT NULL DEFAULT '',
			prepay_payload TEXT NOT NULL DEFAULT '',
			raw_callback TEXT NOT NULL DEFAULT '',
			paid_at TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_payments_out_trade_no ON payments(out_trade_no)`,
		`CREATE INDEX IF NOT EXISTS idx_payments_user_created ON payments(user_id, created_at)`,
		// 余额账本：本项目第一张审计性质的表（此前只有 stdout 日志）。
		// 命名沿用 print_jobs 的风格。
		//
		// idempotency_key 唯一是「防重复入账」的最后一道闸，键的约定见 wallet.go 顶部。
		`CREATE TABLE IF NOT EXISTS wallet_transactions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			kind TEXT NOT NULL,
			amount_cents INTEGER NOT NULL,
			balance_after_cents INTEGER NOT NULL,
			order_id INTEGER,
			payment_id INTEGER,
			idempotency_key TEXT NOT NULL,
			operator TEXT NOT NULL DEFAULT '',
			note TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
			FOREIGN KEY(order_id) REFERENCES print_orders(id) ON DELETE SET NULL,
			FOREIGN KEY(payment_id) REFERENCES payments(id) ON DELETE SET NULL
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_wallet_transactions_idem
			ON wallet_transactions(idempotency_key)`,
		`CREATE INDEX IF NOT EXISTS idx_wallet_transactions_user_created
			ON wallet_transactions(user_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_wallet_transactions_order
			ON wallet_transactions(order_id)`,
	}

	for _, stmt := range stmts {
		if _, err := s.DB.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	if err := addColumnIfMissing(ctx, s.DB, "users", "protected INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// 账户余额（收费功能）。老库热升级只需这一条：其余改动都在
	// users.go 的 SELECT/SCAN 里，读到的旧行默认 0。
	// ⚠️ 余额只能经 store.ApplyWalletDelta 变动，禁止别处直接 UPDATE 这一列。
	if err := addColumnIfMissing(ctx, s.DB, "users", "balance_cents INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := addColumnIfMissing(ctx, s.DB, "print_jobs", "is_duplex INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := addColumnIfMissing(ctx, s.DB, "print_jobs", "is_color INTEGER NOT NULL DEFAULT 1"); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	// 完整打印参数落库，供「重新打印」精确预填第一次的每一项设置（Issue #68）。
	// 旧库热升级：老记录这些列取默认值，重打时即退化为合理默认。
	printJobOptionCols := []string{
		"copies INTEGER NOT NULL DEFAULT 1",
		"orientation TEXT NOT NULL DEFAULT 'portrait'",
		"paper_size TEXT NOT NULL DEFAULT 'A4'",
		"paper_type TEXT NOT NULL DEFAULT 'plain'",
		"media_source TEXT NOT NULL DEFAULT 'auto'",
		"print_scaling TEXT NOT NULL DEFAULT 'fit'",
		"page_range TEXT NOT NULL DEFAULT ''",
		"page_set TEXT NOT NULL DEFAULT 'all'",
		"mirror INTEGER NOT NULL DEFAULT 0",
		"watermark_text TEXT NOT NULL DEFAULT ''",
		"number_up INTEGER NOT NULL DEFAULT 1",
		"number_up_layout TEXT NOT NULL DEFAULT 'lrtb'",
		"page_border TEXT NOT NULL DEFAULT 'none'",
	}
	for _, col := range printJobOptionCols {
		if err := addColumnIfMissing(ctx, s.DB, "print_jobs", col); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}

	if _, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO settings(key, value) VALUES (?, ?)`,
		SettingRetentionDays, "0",
	); err != nil {
		return fmt.Errorf("seed settings: %w", err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO settings(key, value) VALUES (?, ?)`,
		SettingSaveHistory, "1",
	); err != nil {
		return fmt.Errorf("seed settings: %w", err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO settings(key, value) VALUES (?, ?)`,
		SettingMaxPagesPerJob, "0",
	); err != nil {
		return fmt.Errorf("seed settings: %w", err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO settings(key, value) VALUES (?, ?)`,
		SettingMaxUploadBytes, "0",
	); err != nil {
		return fmt.Errorf("seed settings: %w", err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO settings(key, value) VALUES (?, ?)`,
		SettingGuestMode, "0",
	); err != nil {
		return fmt.Errorf("seed settings: %w", err)
	}
	// 计费默认关闭：升级既有部署时三个打印入口的行为必须与改造前完全一致。
	if _, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO settings(key, value) VALUES (?, ?)`,
		SettingBillingEnabled, "0",
	); err != nil {
		return fmt.Errorf("seed settings: %w", err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO settings(key, value) VALUES (?, ?)`,
		SettingOrderTTLMinutes, "15",
	); err != nil {
		return fmt.Errorf("seed settings: %w", err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO settings(key, value) VALUES (?, ?)`,
		SettingPriceFallback, "0",
	); err != nil {
		return fmt.Errorf("seed settings: %w", err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO settings(key, value) VALUES (?, ?)`,
		SettingPaymentMethods, "",
	); err != nil {
		return fmt.Errorf("seed settings: %w", err)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO settings(key, value) VALUES (?, ?)`,
		SettingSiteBaseURL, "",
	); err != nil {
		return fmt.Errorf("seed settings: %w", err)
	}

	return nil
}

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func addColumnIfMissing(ctx context.Context, db *sql.DB, table string, columnDef string) error {
	_, err := db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", table, columnDef))
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "duplicate column name") {
		return nil
	}
	return err
}
