package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
)

// 审计表是「谁在什么时候把什么改成了什么」的唯一凭证，而这组用例钉的是
// 它的两条硬约束：actor 不能为空、筛选与计数必须与列表一致。

func TestInsertAndListAudit(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	entries := []AuditEntry{
		{Actor: "admin", Action: AuditPriceUpdate, Target: "全局",
			Summary: "计费开关 关闭 → 开启", BeforeJSON: `{"enabled":false}`, AfterJSON: `{"enabled":true}`},
		// actor 留空：必须落成 'unknown'，不能存空串。
		{Actor: "", Action: AuditCleanupRun, Target: "全局", Summary: "清空打印历史，删除 3 个文件"},
		{Actor: "ops", Action: AuditPriceUpdate, Target: "全局", Summary: "兜底单价 ¥0.00 → ¥0.20"},
	}
	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		for _, e := range entries {
			if err := InsertAudit(ctx, tx, e); err != nil {
				return err
			}
		}
		return nil
	})

	var list []AuditEntry
	err := s.WithTx(ctx, true, func(tx *sql.Tx) error {
		var err error
		list, err = ListAudit(ctx, tx, "", 100, 0)
		return err
	})
	if err != nil {
		t.Fatalf("读取审计列表失败: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("应当有 3 条，实际 %d 条", len(list))
	}
	// 倒序：最后写的排最前。
	if list[0].Summary != "兜底单价 ¥0.00 → ¥0.20" {
		t.Errorf("列表应当按时间倒序，首条实际是 %q", list[0].Summary)
	}
	// actor 归一化。
	var unknown *AuditEntry
	for i := range list {
		if list[i].Action == AuditCleanupRun {
			unknown = &list[i]
		}
	}
	if unknown == nil {
		t.Fatal("没找到 cleanup.run 那条")
	}
	if unknown.Actor != "unknown" {
		t.Errorf("空 actor 应当落成 unknown，实际 %q", unknown.Actor)
	}
	// 时间戳由 InsertAudit 补，不能是空串。
	for _, e := range list {
		if e.CreatedAt == "" {
			t.Errorf("第 %d 条缺 created_at", e.ID)
		}
	}
}

// TestListAuditFilterAndCountConsistency 钉住「筛选后的条数」与「筛选后的列表」
// 是同一个条件。分页靠这两个数配合，一旦它们用了不同的条件，表现是
// 「说有 5 页，翻到第 3 页就空了」——而那种 bug 只在数据多时才露头。
func TestListAuditFilterAndCountConsistency(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		for i := 0; i < 3; i++ {
			if err := InsertAudit(ctx, tx, AuditEntry{
				Actor: "admin", Action: AuditPriceUpdate, Target: "全局", Summary: "改价",
			}); err != nil {
				return err
			}
		}
		return InsertAudit(ctx, tx, AuditEntry{
			Actor: "admin", Action: AuditUserDelete, Target: "user#7", Summary: "删除用户 bob",
		})
	})

	var (
		all, filtered []AuditEntry
		totalAll      int
		totalFiltered int
		actions       []string
	)
	err := s.WithTx(ctx, true, func(tx *sql.Tx) error {
		var err error
		if all, err = ListAudit(ctx, tx, "", 100, 0); err != nil {
			return err
		}
		if totalAll, err = CountAudit(ctx, tx, ""); err != nil {
			return err
		}
		if filtered, err = ListAudit(ctx, tx, AuditPriceUpdate, 100, 0); err != nil {
			return err
		}
		if totalFiltered, err = CountAudit(ctx, tx, AuditPriceUpdate); err != nil {
			return err
		}
		actions, err = ListAuditActions(ctx, tx)
		return err
	})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}

	if len(all) != totalAll || totalAll != 4 {
		t.Errorf("全量：列表 %d 条、计数 %d，期望都是 4", len(all), totalAll)
	}
	if len(filtered) != totalFiltered || totalFiltered != 3 {
		t.Errorf("筛选 price.update：列表 %d 条、计数 %d，期望都是 3", len(filtered), totalFiltered)
	}
	for _, e := range filtered {
		if e.Action != AuditPriceUpdate {
			t.Errorf("筛选结果里混进了 %q", e.Action)
		}
	}
	// 动作键去重且有序：前端筛选下拉直接用它。
	want := []string{AuditPriceUpdate, AuditUserDelete}
	if len(actions) != len(want) {
		t.Fatalf("动作键应当去重成 %d 个，实际 %v", len(want), actions)
	}
	for i := range want {
		if actions[i] != want[i] {
			t.Errorf("动作键[%d] = %q，期望 %q", i, actions[i], want[i])
		}
	}
}

// TestListAuditPaging 确认 offset 生效 —— 审计页是唯一的翻页入口，
// 翻页错了就等于历史看不到。
func TestListAuditPaging(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	mustExec(t, s, func(ctx context.Context, tx *sql.Tx) error {
		for i := 1; i <= 5; i++ {
			if err := InsertAudit(ctx, tx, AuditEntry{
				Actor: "admin", Action: AuditSettingsUpdate, Target: "全局",
				Summary: fmt.Sprintf("第 %d 次修改", i),
			}); err != nil {
				return err
			}
		}
		return nil
	})

	var first, second []AuditEntry
	err := s.WithTx(ctx, true, func(tx *sql.Tx) error {
		var err error
		if first, err = ListAudit(ctx, tx, "", 2, 0); err != nil {
			return err
		}
		second, err = ListAudit(ctx, tx, "", 2, 2)
		return err
	})
	if err != nil {
		t.Fatalf("分页查询失败: %v", err)
	}
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("每页应当是 2 条，实际 %d / %d", len(first), len(second))
	}
	if first[1].ID <= second[0].ID {
		t.Errorf("第二页应当比第一页更旧：第一页末条 id=%d，第二页首条 id=%d",
			first[1].ID, second[0].ID)
	}
}
