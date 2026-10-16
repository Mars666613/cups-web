package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
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
	var view map[string]interface{}
	err := appStore.WithTx(ctx, true, func(tx *sql.Tx) error {
		var err error
		view, err = loadPriceViewTx(ctx, tx)
		return err
	})
	return view, err
}

// loadPriceViewTx 是 loadPriceView 的事务内版本。
//
// 单独抽出来是为了给审计用：PUT 里要读「改之前」和「改之后」两次，而
// loadPriceView 自己开事务 —— 在已有事务里调它会另起一个连接，读到的
// 不是本事务的一致快照。
func loadPriceViewTx(ctx context.Context, tx *sql.Tx) (map[string]interface{}, error) {
	var enabled, fallback int64
	var rows []store.PriceRule
	var err error

	if enabled, err = store.GetSettingInt(ctx, tx, store.SettingBillingEnabled, 0); err != nil {
		return nil, err
	}
	if fallback, err = loadFallbackUnitCents(ctx, tx); err != nil {
		return nil, err
	}
	if rows, err = store.ListPriceRules(ctx, tx); err != nil {
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
		if err != nil {
			return err
		}
		// 手工调额本来就会写一条 admin_adjust 流水，但流水表回答的是
		// 「钱怎么动的」，审计表回答的是「谁动了哪个人」。管理员查「谁把
		// 张三的余额加了 100」时只会去一个地方找 —— 把这件事放在同一个
		// 列表里，比让他记住「余额看流水、开关看审计」强。
		return recordAudit(ctx, tx, operator, store.AuditBalanceAdjust,
			fmt.Sprintf("user#%d %s", user.ID, user.Username),
			fmt.Sprintf("余额 %+d 分（%s → %s）：%s",
				payload.AmountCents, formatYuan(user.BalanceCents), formatYuan(balanceAfter), note),
			auditChange{
				Field: "amountCents", Text: fmt.Sprintf("%+d 分", payload.AmountCents),
				Before: user.BalanceCents, After: balanceAfter,
			})
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

		// 「改之前」取本事务内的一致快照（理由同 adminUpdateSettingsHandler）。
		beforeView, err := loadPriceViewTx(ctx, tx)
		if err != nil {
			return err
		}

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

			// 取件模式与计费互斥（反方向在 adminUpdateSettingsHandler 里校验）。
			// 两者都在描述「谁是这个部署的打印入口」：一个要求先付钱、
			// 一个压根不收费，同时开启会让 /api/print 既该 403 又该暂存。
			pullMode, err := store.GetSettingInt(ctx, tx, store.SettingPullModeEnabled, 0)
			if err != nil {
				return err
			}
			if pullMode != 0 {
				return errors.New("开启计费前请先关闭刷卡取件模式")
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

		afterView, err := loadPriceViewTx(ctx, tx)
		if err != nil {
			return err
		}
		return recordAudit(ctx, tx, sessionUsername(r), store.AuditPriceUpdate,
			"全局", "", priceAuditChanges(beforeView, afterView)...)
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

// priceAuditChanges 比较前后两份价目视图，返回变更项。
//
// 价目表是【整表替换】（ReplacePriceRules 先删后插），所以「第 2 条单价从
// 20 分改成 25 分」在这里只能表现为「整张表变了」。刻意不去猜是哪一条被改：
// 猜错的审计记录比不猜更糟 —— 它看起来是确定的。两份完整价目都进 JSON，
// 列表页展开就能逐条比对；摘要只给条数，让人一眼看出「这次动过价目」。
func priceAuditChanges(before, after map[string]interface{}) []auditChange {
	var out []auditChange

	bEnabled, _ := before["enabled"].(bool)
	aEnabled, _ := after["enabled"].(bool)
	if bEnabled != aEnabled {
		out = append(out, auditChange{
			Field: "enabled", Text: fmt.Sprintf("计费开关 %s → %s", onOff(bEnabled), onOff(aEnabled)),
			Before: bEnabled, After: aEnabled,
		})
	}

	bFallback := toInt64(before["fallbackUnitCents"])
	aFallback := toInt64(after["fallbackUnitCents"])
	if bFallback != aFallback {
		out = append(out, auditChange{
			Field:  "fallbackUnitCents",
			Text:   fmt.Sprintf("兜底单价 %s → %s", formatYuan(bFallback), formatYuan(aFallback)),
			Before: bFallback, After: aFallback,
		})
	}

	bRules := toPriceRulePayloads(before["rules"])
	aRules := toPriceRulePayloads(after["rules"])
	if priceRuleSetKey(bRules) != priceRuleSetKey(aRules) {
		out = append(out, auditChange{
			Field:  "rules",
			Text:   fmt.Sprintf("价目条目 %d 条 → %d 条", len(bRules), len(aRules)),
			Before: bRules, After: aRules,
		})
	}
	return out
}

func toInt64(v interface{}) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	default:
		return 0
	}
}

func toPriceRulePayloads(v interface{}) []priceRulePayload {
	switch rows := v.(type) {
	case []priceRulePayload:
		return rows
	case []store.PriceRule:
		return mapPriceRulePayloads(rows)
	default:
		return nil
	}
}

// priceRuleSetKey 把一份价目表归一成与顺序无关的字符串，用来判断「真的变了」。
//
// 不直接比切片：ReplacePriceRules 是删了重插，两次提交同样内容也可能因为
// 查询顺序不同而列出不同次序 —— 那样审计里会留下一条「2 条 → 2 条」的
// 假变更记录，比没有记录更让人困惑。
func priceRuleSetKey(rows []priceRulePayload) string {
	keys := make([]string, 0, len(rows))
	for _, r := range rows {
		keys = append(keys, fmt.Sprintf("%s=%d", priceRuleKey(r.IsColor, r.PaperSize, r.PaperType), r.UnitCents))
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}
