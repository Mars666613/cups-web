package main

import (
	"context"
	"database/sql"
	"log"

	"cups-web/internal/pricing"
	"cups-web/internal/store"
)

// CurrencyCNY 是计费使用的币种。金额一律以「分」为整数存储与传输，
// 前端用 formatCents 换算展示。
const CurrencyCNY = "CNY"

// billingEnabled 读取计费总开关。
//
// 🚫 刻意【不加进程内缓存】。SQLite 按主键读一行是微秒级的，而缓存会引入
// 「管理员刚打开计费、服务端还在按旧的免费状态放行」这类时序问题 ——
// 那种问题极难排查，而省下的那点开销在打印请求（要做文件 IO 和文档转换）
// 面前完全是噪音。管理员改完立刻生效，这个确定性比微优化值钱。
// 注意这与计划文档里写的「30s 缓存」不同，是有意的取舍。
//
// 降级方向是 fail-closed，与 getPrintLimits 的 fail-open 正好相反：
// 读库失败时按「已开启」处理，让用户看到「计费已开启，请通过打印订单提交」，
// 而不是在数据库故障期间免费打印。这里的降级方向直接对应收入损失。
func billingEnabled(ctx context.Context) bool {
	return modeFlag(ctx, store.SettingBillingEnabled, "billing")
}

// pullModeEnabled 读刷卡取件总开关。默认关闭。
//
// 与计费【互斥】（互斥在 adminUpdateSettingsHandler 里强制）：两者描述的都是
// 「这个部署的打印入口是谁」，同时开启会得到两个都不对的入口 ——
// /api/print 既该 403 又该暂存。
//
// 降级方向与计费相同，也是 fail-closed：读库失败时宁可让用户看到一条明确的
// 拒绝，也不能放行一次「本该暂存却直接打出来」的打印。
func pullModeEnabled(ctx context.Context) bool {
	return modeFlag(ctx, store.SettingPullModeEnabled, "pull")
}

// modeFlag 是模式开关的统一读取。两个开关（计费 / 取件）共用。
//
// 降级方向 fail-closed 与 getPrintLimits 的 fail-open 正好相反：
// 读库失败时按「已开启」处理。理由是这个方向直接对应「打错东西」或
// 「白送钱」，而另一个方向的代价只是用户多点一次。
func modeFlag(ctx context.Context, key, tag string) bool {
	var enabled int64
	err := appStore.WithTx(ctx, true, func(tx *sql.Tx) error {
		v, err := store.GetSettingInt(ctx, tx, key, 0)
		if err != nil {
			return err
		}
		enabled = v
		return nil
	})
	if err != nil {
		log.Printf("[%s] 读取开关 %s 失败，按已开启处理（fail-closed）: %v", tag, key, err)
		return true
	}
	return enabled != 0
}

// loadPricingRules 读出全部单价规则，转成计价内核使用的值类型。
//
// 传 *sql.Tx 而不是自己开事务：调用方通常已经在事务里（下单要保证
// 「读价目 → 算价 → 落单」是一个一致快照）。
func loadPricingRules(ctx context.Context, tx *sql.Tx) ([]pricing.Rule, error) {
	rows, err := store.ListPriceRules(ctx, tx)
	if err != nil {
		return nil, err
	}
	return toPricingRules(rows), nil
}

// toPricingRules 把数据层的行转成计价内核的值类型。
//
// internal/store 不认识 internal/pricing —— 数据层不该依赖领域计算包。
// 转换就落在这个边界上。
func toPricingRules(rows []store.PriceRule) []pricing.Rule {
	rules := make([]pricing.Rule, 0, len(rows))
	for _, r := range rows {
		rules = append(rules, pricing.Rule{
			IsColor:   r.IsColor,
			PaperSize: r.PaperSize,
			PaperType: r.PaperType,
			UnitCents: r.UnitPriceCents,
		})
	}
	return rules
}

// loadFallbackUnitCents 读「价目未命中时的兜底单价」。0 表示未配置。
func loadFallbackUnitCents(ctx context.Context, tx *sql.Tx) (int64, error) {
	return store.GetSettingInt(ctx, tx, store.SettingPriceFallback, 0)
}
