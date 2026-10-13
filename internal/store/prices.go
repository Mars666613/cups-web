package store

import (
	"context"
	"database/sql"
	"fmt"
)

// PriceRule 是一条单价规则：单价(是否彩色, 纸张尺寸, 纸张类型)。
//
// 配置方式是「全量替换」而非逐条增删 —— 价目表只有几十行，一次 PUT 覆盖整表
// 最不容易出现「删了一条却留下半截状态」的问题（见 ReplacePriceRules）。
type PriceRule struct {
	ID             int64
	IsColor        bool
	PaperSize      string
	PaperType      string
	UnitPriceCents int64
	UpdatedAt      string
}

// priceRuleColumns 与 scanPriceRule 的字段顺序严格对应。
const priceRuleColumns = `id, is_color, paper_size, paper_type, unit_price_cents, updated_at`

func scanPriceRule(s scanner) (PriceRule, error) {
	var r PriceRule
	err := s.Scan(&r.ID, &r.IsColor, &r.PaperSize, &r.PaperType, &r.UnitPriceCents, &r.UpdatedAt)
	return r, err
}

// ListPriceRules 返回全部单价规则，按维度排序便于前端稳定展示。
func ListPriceRules(ctx context.Context, tx *sql.Tx) ([]PriceRule, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+priceRuleColumns+`
		FROM price_rules
		ORDER BY is_color, paper_size, paper_type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	rules := []PriceRule{}
	for rows.Next() {
		r, err := scanPriceRule(rows)
		if err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	return rules, rows.Err()
}

// ReplacePriceRules 用给定规则全量替换现有价目表。
//
// 先 DELETE 再 INSERT，两步在同一个事务里（调用方用 WithTx 包住），
// 中途失败会整体回滚，不会留下半张表。
//
// ⚠️ 调用方必须先去重：唯一索引 idx_price_rules_dim 会拒绝重复维度，
// 但那时错误信息对用户不友好，应当在 handler 层用 errors.New 给出明确文案。
func ReplacePriceRules(ctx context.Context, tx *sql.Tx, rules []PriceRule) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM price_rules`); err != nil {
		return fmt.Errorf("clear price rules: %w", err)
	}
	if len(rules) == 0 {
		return nil
	}

	now := nowUTC()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO price_rules (
		is_color, paper_size, paper_type, unit_price_cents, updated_at
	) VALUES ($1, $2, $3, $4, $5)`)
	if err != nil {
		return fmt.Errorf("prepare price rule insert: %w", err)
	}
	defer stmt.Close()

	for _, r := range rules {
		if _, err := stmt.ExecContext(ctx, r.IsColor, r.PaperSize, r.PaperType, r.UnitPriceCents, now); err != nil {
			return fmt.Errorf("insert price rule (%v,%s,%s): %w", r.IsColor, r.PaperSize, r.PaperType, err)
		}
	}
	return nil
}
