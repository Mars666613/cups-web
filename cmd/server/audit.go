package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"cups-web/internal/store"
)

// 管理操作审计的写入侧。
//
// ── 为什么要有这一层，而不是在每个 handler 里直接 InsertAudit ──
//
// 一个 handler 要记的其实是「改了什么」，而不是「怎么序列化」。把
// before/after 的 JSON 拼装、summary 的拼接、空变更的跳过集中在这里，
// 各调用点就只剩一行，也就没有「这次忘了记」的借口。
//
// ── 三条约定 ──
//
//  1. 写审计必须用【业务改动所在的那个事务】（参数就是 *sql.Tx）。
//     分开写会出现「改了但没记」和「记了但没改」——两种都让审计不可信。
//  2. 审计写失败 → 返回错误 → 整个事务回滚。宁可这次没改成，也不要一次
//     没人认账的改动。这是刻意的取舍，不要为了「审计不该影响业务」改成
//     忽略错误：那等于把审计降级成建议。
//  3. 无变更不记。管理员点一次保存但什么都没改（前端每次都发全量表单，
//     这是常态），不该在审计里留一条空记录 —— 几十条噪声会把真正的改动
//     淹掉，而审计页的价值全在「一眼看出不一样的那条」。

// auditChange 是一条审计记录里「变了什么」的最小单位。
type auditChange struct {
	// Field 是 before_json / after_json 里的键。用英文、保持稳定 ——
	// 它可能被日志解析或脚本消费，而中文键在 URL / 日志里会被编码。
	Field string
	// Text 是给人看的一句话（如「兜底单价 0 分 → 20 分」）。单位只有调用点
	// 知道（分？元？条？），所以这句话由调用点写。
	Text string
	// Before / After 是前后值，进 JSON。删除一个配置项时 After 可以为 nil。
	Before interface{}
	After  interface{}
}

// recordAudit 在当前事务里写一条管理操作审计。
//
// summary 留空时由 changes 的 Text 拼出来；两者都为空视为「无事发生」，
// 直接返回（见上面第 3 条约定）。
func recordAudit(ctx context.Context, tx *sql.Tx, actor, action, target, summary string, changes ...auditChange) error {
	texts := make([]string, 0, len(changes))
	before := make(map[string]interface{}, len(changes))
	after := make(map[string]interface{}, len(changes))
	for _, c := range changes {
		if c.Text != "" {
			texts = append(texts, c.Text)
		}
		before[c.Field] = c.Before
		after[c.Field] = c.After
	}
	if summary == "" {
		summary = strings.Join(texts, "；")
	}
	if summary == "" {
		return nil
	}

	entry := store.AuditEntry{
		Actor:   actor,
		Action:  action,
		Target:  target,
		Summary: summary,
	}
	if len(changes) > 0 {
		b, err := json.Marshal(before)
		if err != nil {
			return fmt.Errorf("序列化审计前值失败: %w", err)
		}
		a, err := json.Marshal(after)
		if err != nil {
			return fmt.Errorf("序列化审计后值失败: %w", err)
		}
		entry.BeforeJSON, entry.AfterJSON = string(b), string(a)
	}
	if err := store.InsertAudit(ctx, tx, entry); err != nil {
		return fmt.Errorf("写审计记录失败: %w", err)
	}
	log.Printf("[audit] %s 执行 %s（%s）：%s", entry.Actor, action, target, summary)
	return nil
}

// auditActionLabel 把动作键翻成管理端显示的中文。
//
// 放在服务端而不是前端：这份映射要与 internal/store/audit.go 的常量一起
// 维护，放前端就会有两处清单，而新加的动作键只会在其中一处出现 ——
// 表现是审计页上冒出一行英文的 `device.bind`。
func auditActionLabel(action string) string {
	switch action {
	case store.AuditSettingsUpdate:
		return "修改系统设置"
	case store.AuditPriceUpdate:
		return "修改计费与价目"
	case store.AuditBalanceAdjust:
		return "调整用户余额"
	case store.AuditUserCreate:
		return "新建用户"
	case store.AuditUserUpdate:
		return "修改用户"
	case store.AuditUserDelete:
		return "删除用户"
	case store.AuditOrderRefund:
		return "订单退款"
	case store.AuditCleanupRun:
		return "清空打印历史"
	case store.AuditTerminalCreate:
		return "新增取件终端"
	case store.AuditTerminalUpdate:
		return "修改取件终端"
	case store.AuditTerminalDelete:
		return "删除取件终端"
	case store.AuditDeviceResolve:
		return "处理设备告警"
	default:
		return action
	}
}

// roleText 把角色翻成中文，供审计摘要用（用户能看懂的话）。
func roleText(role string) string {
	switch role {
	case store.RoleAdmin:
		return "管理员"
	case store.RoleUser:
		return "普通用户"
	default:
		return role
	}
}

// terminalTargetText 描述一台取件终端「绑在哪」。
//
// printerUri 为空 = 仅用于绑卡的终端（通常插在管理员电脑上的读卡器），
// 直接显示空串会让审计摘要变成「新增取件终端「X」（）」。
func terminalTargetText(printerURI string) string {
	if strings.TrimSpace(printerURI) == "" {
		return "仅用于绑卡"
	}
	return printerURI
}

// auditEntryResponse 在库里的字段之外补一个中文标签给前端。
type auditEntryResponse struct {
	store.AuditEntry
	ActionLabel string `json:"actionLabel"`
}

func toAuditResponse(rows []store.AuditEntry) []auditEntryResponse {
	out := make([]auditEntryResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, auditEntryResponse{AuditEntry: r, ActionLabel: auditActionLabel(r.Action)})
	}
	return out
}
