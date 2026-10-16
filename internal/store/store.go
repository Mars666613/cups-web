package store

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
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

	// 刷卡取件（拉式打印）。与企业内网场景配套：不收费，员工刷卡才出纸。
	//
	// ⚠️ 与 SettingBillingEnabled 【互斥】：两者都描述「谁是这个部署的打印
	// 入口」，同时开启会得到两个都不对的入口。互斥在
	// admin_handlers.go 的 adminUpdateSettingsHandler 里强制。
	SettingPullModeEnabled = "pull_mode_enabled" // "0" 关闭时 /api/print 行为不变
	SettingPullTTLHours    = "pull_ttl_hours"    // "72" 待取件任务保留多久（小时），过期即删文件

	// 设备异常告警。见 schema.sql 的 device_events 与 cmd/server/device_alert.go。
	//
	// 默认【关闭】：开关打开而 webhook 没配，就是「明明配了告警却什么都没收到」，
	// 比不告警更危险 —— 后者至少知道要靠自己盯。
	SettingAlertEnabled = "alert_enabled" // "0"
	// SettingAlertWebhookURL 为空表示只记事件、不外发（管理端页面上仍然看得到）。
	SettingAlertWebhookURL = "alert_webhook_url"
	// SettingAlertWebhookKind "auto" 时按 URL 主机名推断渠道消息格式
	// （qyapi.weixin.qq.com → 企业微信，oapi.dingtalk.com → 钉钉，
	// open.feishu.cn → 飞书），识别不出按通用 {"text": ...} 发。
	SettingAlertWebhookKind = "alert_webhook_kind"
	// SettingAlertRepeatMinutes 同一异常持续存在时多久重发一次提醒；0 = 只发首次。
	SettingAlertRepeatMinutes = "alert_repeat_minutes"
	// SettingAlertSupplyLowPercent 墨量/粉量低于这个百分比算「耗材不足」。
	SettingAlertSupplyLowPercent = "alert_supply_low_percent"
)

type Store struct {
	DB *sql.DB
}

//go:embed schema.sql
var schemaSQL string

// Open 连接 PostgreSQL 并保证表结构就绪。
//
// dsn 形如 postgres://user:pass@host:5432/dbname?sslmode=require
//
// 用的是 pgx 的 database/sql 兼容层（`stdlib`）而不是原生 pgx 接口 ——
// 这样 `*sql.Tx`、`sql.ErrNoRows`、`RowsAffected` 的语义全部不变，
// 整个数据层只需要改 SQL 方言，不用重写调用风格。
//
// ⚠️ 数据库【必须】是 LC_COLLATE='C' 建的（见 schema.sql 顶部）。
// PG 默认若是 ICU 非确定性 collation，username 的唯一约束会把 Admin 和
// admin 判为冲突，而且 `WHERE username = ?` 会变成大小写不敏感 ——
// 也就是能用 ADMIN 登录进来。C 是字节序，与原先 SQLite 的 BINARY 一致。
func Open(ctx context.Context, dsn string, maxConns int) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	if maxConns <= 0 {
		maxConns = 10
	}
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns / 2)

	// sql.Open 是惰性的，不真连。这里主动 ping 一次，让「连不上库」
	// 在启动时就报出来，而不是等第一个用户点打印才炸。
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect postgres: %w", err)
	}

	s := &Store{DB: db}
	if err := s.applySchema(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.DB.Close()
}

// applySchema 执行 schema.sql。里面全是 IF NOT EXISTS，可重复执行。
//
// ⚠️ 必须整份交给 PG 解析，不能拆成一条条走扩展协议：pgx 默认用扩展协议，
// 一次 Exec 多语句会报「cannot insert multiple commands into a prepared
// statement」。而按 `;` 字符串切分 schema.sql 又很脆（行内注释、多行
// VALUES 都会踩到）。`Raw` 拿到底层 pgx 连接后，无参数的 Exec 走的是
// 简单查询协议，天然支持多语句。
//
// 包在一个事务里：PG 支持事务性 DDL，建到一半失败会整体回滚，
// 不会留下半套表。
func (s *Store) applySchema(ctx context.Context) error {
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	defer conn.Close()

	return conn.Raw(func(driverConn any) error {
		pgxConn, ok := driverConn.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("apply schema: 底层连接不是 pgx（%T）", driverConn)
		}
		tx, err := pgxConn.Conn().Begin(ctx)
		if err != nil {
			return fmt.Errorf("apply schema: %w", err)
		}
		defer func() { _ = tx.Rollback(ctx) }() // 提交后返回 ErrTxClosed，忽略
		if _, err := tx.Exec(ctx, schemaSQL); err != nil {
			return fmt.Errorf("apply schema: %w", err)
		}
		return tx.Commit(ctx)
	})
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

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}
