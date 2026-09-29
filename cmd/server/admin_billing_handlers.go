package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"cups-web/internal/pricing"
	"cups-web/internal/store"
)

// maxPriceRules 是价目表的行数上限。
// 维度只有 (是否彩色 × 纸张尺寸 × 纸张类型)，前端实际提供的选项组合是几十种；
// 给 512 是留足自定义余量，同时挡住误贴入的巨量数据。
const maxPriceRules = 512

type priceRulePayload struct {
	IsColor   bool   `json:"isColor"`
	PaperSize string `json:"paperSize"`
	PaperType string `json:"paperType"`
	UnitCents int64  `json:"unitCents"`
}

// pricesPayload 是 PUT /api/admin/prices 的请求体。
//
// 三个字段都是指针，与 settingsPayload 同一套「省略即不改」的语义：
// 只想改开关时不该把整张价目表清空，只想改单价时也不该动开关。
// 传 "rules": [] （空数组）才是明确要求清空价目表。
type pricesPayload struct {
	Enabled           *bool               `json:"enabled"`
	FallbackUnitCents *int64              `json:"fallbackUnitCents"`
	Rules             *[]priceRulePayload `json:"rules"`
}

// loadPriceView 读出价目视图。管理端与用户端共用 —— 两边看到的必须是同一份数据，
// 否则用户按 A 价预期、系统按 B 价扣款，是必然的投诉来源。
func loadPriceView(ctx context.Context) (map[string]interface{}, error) {
	var enabled, fallback int64
	var rows []store.PriceRule

	err := appStore.WithTx(ctx, true, func(tx *sql.Tx) error {
		var err error
		if enabled, err = store.GetSettingInt(ctx, tx, store.SettingBillingEnabled, 0); err != nil {
			return err
		}
		if fallback, err = loadFallbackUnitCents(ctx, tx); err != nil {
			return err
		}
		rows, err = store.ListPriceRules(ctx, tx)
		return err
	})
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"enabled":           enabled != 0,
		"currency":          CurrencyCNY,
		"fallbackUnitCents": fallback,
		"rules":             mapPriceRulePayloads(rows),
	}, nil
}

func adminGetPricesHandler(w http.ResponseWriter, r *http.Request) {
	view, err := loadPriceView(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load prices")
		return
	}
	writeJSON(w, view)
}

// adminAdjustBalanceHandler 手工调整某个用户的余额（可正可负）。
//
// 这是「线下收款后入账」与「纠正误扣」的唯一入口。每一笔都会写进
// wallet_transactions 并记下操作人，所以它同时也是一个审计动作 ——
// 🚫 不要为它开一条「直接改 users.balance_cents」的捷径。
func adminAdjustBalanceHandler(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	var payload struct {
		AmountCents int64  `json:"amountCents"`
		Note        string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	if payload.AmountCents == 0 {
		writeJSONError(w, http.StatusBadRequest, "调整金额不能为 0")
		return
	}

	operator := sessionUsername(r)
	var balanceAfter int64
	err = appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
		ctx := r.Context()
		user, err := store.GetUserByID(ctx, tx, id)
		if err != nil {
			return err
		}
		note := strings.TrimSpace(payload.Note)
		if note == "" {
			note = "管理员手工调整"
		}
		balanceAfter, err = store.ApplyWalletDelta(ctx, tx, store.WalletDeltaInput{
			UserID:         user.ID,
			Kind:           store.WalletKindAdminAdjust,
			AmountCents:    payload.AmountCents,
			IdempotencyKey: store.WalletKeyAdjust(),
			Operator:       operator,
			Note:           note,
		})
		return err
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrInsufficientBalance):
			// 扣到负数会被 ApplyWalletDelta 拒掉（余额不允许为负）。
			writeJSONError(w, http.StatusUnprocessableEntity, "扣减后余额会变成负数，请先核对")
		case errors.Is(err, sql.ErrNoRows):
			writeJSONError(w, http.StatusNotFound, "user not found")
		default:
			writeJSONError(w, http.StatusInternalServerError, "failed to adjust balance")
		}
		return
	}

	log.Printf("[billing] %s 调整用户 #%d 余额 %+d 分，调整后 %d 分",
		operator, id, payload.AmountCents, balanceAfter)
	writeJSON(w, map[string]interface{}{
		"ok":           true,
		"balanceCents": balanceAfter,
	})
}

func adminUpdatePricesHandler(w http.ResponseWriter, r *http.Request) {
	var payload pricesPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid payload")
		return
	}

	err := appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
		ctx := r.Context()

		// 先把本次要写入的价目解析校验出来（nil 指针 = 不改价目表）。
		var newRules []pricing.Rule
		if payload.Rules != nil {
			parsed, err := parsePriceRules(*payload.Rules)
			if err != nil {
				return err
			}
			newRules = parsed
		}

		// ⚠️ 跨字段校验必须基于「这次修改【之后】」的状态。
		// 只看 payload 会把「开计费 + 库里已有价目」误判成「开计费但没价目」，
		// 于是管理员只切一下开关就被拒。所以没在本次请求里出现的字段，
		// 一律从库里读现值补上。
		effectiveEnabled, err := effectiveBoolSetting(ctx, tx, store.SettingBillingEnabled, payload.Enabled)
		if err != nil {
			return err
		}
		effectiveFallback, err := effectiveIntSetting(ctx, tx, store.SettingPriceFallback, payload.FallbackUnitCents)
		if err != nil {
			return err
		}

		effectiveRuleCount := 0
		if payload.Rules != nil {
			effectiveRuleCount = len(newRules)
		} else {
			existing, err := store.ListPriceRules(ctx, tx)
			if err != nil {
				return err
			}
			effectiveRuleCount = len(existing)
		}

		// 拦住「开了计费但什么都算 0 元」——那等于收款功能被静默关掉，
		// 而管理员以为自己已经开启收费了。这是最需要防的一种配置错误。
		if effectiveEnabled && effectiveRuleCount == 0 && effectiveFallback <= 0 {
			return errors.New("开启计费前请先配置价目，或设置一个大于 0 的兜底单价")
		}

		// 访客模式与计费互斥：guest 是所有未登录用户共用的一个账号，
		// 它的余额就是「公摊钱包」，任何人都能花光且无法追责、无法对账。
		if effectiveEnabled {
			guestMode, err := store.GetSettingInt(ctx, tx, store.SettingGuestMode, 0)
			if err != nil {
				return err
			}
			if guestMode != 0 {
				return errors.New("开启计费前请先关闭访客模式：访客是所有人共用的账号，余额无法追责")
			}
		}

		if payload.Enabled != nil {
			if err := store.SetSettingInt(ctx, tx, store.SettingBillingEnabled, boolToInt(*payload.Enabled)); err != nil {
				return err
			}
		}
		if payload.FallbackUnitCents != nil {
			if err := store.SetSettingInt(ctx, tx, store.SettingPriceFallback, *payload.FallbackUnitCents); err != nil {
				return err
			}
		}
		if payload.Rules != nil {
			rows := make([]store.PriceRule, 0, len(newRules))
			for _, rule := range newRules {
				rows = append(rows, store.PriceRule{
					IsColor:        rule.IsColor,
					PaperSize:      rule.PaperSize,
					PaperType:      rule.PaperType,
					UnitPriceCents: rule.UnitCents,
				})
			}
			if err := store.ReplacePriceRules(ctx, tx, rows); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// parsePriceRules 校验并归一化前端提交的价目表。
//
// 重复检测是【大小写不敏感】的（"A4"/"a4" 判为同一条）。
// 数据库唯一索引是大小写敏感的，所以必须在这里挡住 ——
// 否则库里会同时存在 "A4" 与 "a4" 两行，而 pricing.LookupUnitCents
// 也是大小写不敏感匹配，命中哪一条就变成了看插入顺序，无从解释。
//
// 存储值只做首尾去空格，不强制大小写：管理端要原样看到自己填的
// "5inch" / "plain"，全大写成 "5INCH" / "PLAIN" 太难读。
func parsePriceRules(in []priceRulePayload) ([]pricing.Rule, error) {
	if len(in) > maxPriceRules {
		return nil, fmt.Errorf("价目条数不能超过 %d 条", maxPriceRules)
	}

	rules := make([]pricing.Rule, 0, len(in))
	seen := make(map[string]bool, len(in))

	for _, r := range in {
		size := strings.TrimSpace(r.PaperSize)
		ptype := strings.TrimSpace(r.PaperType)
		if size == "" || ptype == "" {
			return nil, errors.New("每条价目都必须填写纸张尺寸与纸张类型")
		}
		if r.UnitCents < 0 {
			return nil, fmt.Errorf("单价不能为负数：%s / %s", size, ptype)
		}

		key := priceRuleKey(r.IsColor, size, ptype)
		if seen[key] {
			return nil, fmt.Errorf("价目重复：%s 的 %s 已经有一条了", size, paperTypeLabel(r.IsColor, ptype))
		}
		seen[key] = true

		rules = append(rules, pricing.Rule{
			IsColor:   r.IsColor,
			PaperSize: size,
			PaperType: ptype,
			UnitCents: r.UnitCents,
		})
	}
	return rules, nil
}

// priceRuleKey 是价目维度的比较键，大小写不敏感。
// 与数据库唯一索引 idx_price_rules_dim 的语义保持一致（见 parsePriceRules）。
func priceRuleKey(isColor bool, paperSize, paperType string) string {
	return fmt.Sprintf("%v|%s|%s",
		isColor,
		strings.ToUpper(strings.TrimSpace(paperSize)),
		strings.ToUpper(strings.TrimSpace(paperType)))
}

func paperTypeLabel(isColor bool, paperType string) string {
	if isColor {
		return "彩色 " + paperType
	}
	return "黑白 " + paperType
}

func mapPriceRulePayloads(rows []store.PriceRule) []priceRulePayload {
	out := make([]priceRulePayload, 0, len(rows))
	for _, r := range rows {
		out = append(out, priceRulePayload{
			IsColor:   r.IsColor,
			PaperSize: r.PaperSize,
			PaperType: r.PaperType,
			UnitCents: r.UnitPriceCents,
		})
	}
	return out
}

// effectiveBoolSetting 求「本次修改之后」的布尔设置值：override 非 nil 用它，
// 否则读库里的现值。
func effectiveBoolSetting(ctx context.Context, tx *sql.Tx, key string, override *bool) (bool, error) {
	if override != nil {
		return *override, nil
	}
	v, err := store.GetSettingInt(ctx, tx, key, 0)
	if err != nil {
		return false, err
	}
	return v != 0, nil
}

// effectiveIntSetting 求「本次修改之后」的整数设置值。
func effectiveIntSetting(ctx context.Context, tx *sql.Tx, key string, override *int64) (int64, error) {
	if override != nil {
		return *override, nil
	}
	return store.GetSettingInt(ctx, tx, key, 0)
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
