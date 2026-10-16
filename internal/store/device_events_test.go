package store

import (
	"context"
	"database/sql"
	"testing"
)

// 设备事件表是「这台机器现在有什么毛病、从什么时候开始」的唯一凭证，
// 而这组用例钉的是它的四条硬约束：
//
//  1. 同一台 + 同一类 + 未恢复 只能有一行（巡查每分钟 upsert 一次的依据）
//  2. 重复 upsert 不得刷新 started_at（否则「已持续 6 小时」永远显示「刚刚」）
//  3. 恢复后再次出现必须是【新的一行】（历史要能重复，不能被唯一索引吃掉）
//  4. 静音的解除条件是「该类别先彻底消失一次」

const (
	tPrinter = "ipp://192.168.1.9/ipp/print"
	tNow     = "2026-10-15T01:00:00Z"
	tLater   = "2026-10-15T02:00:00Z"
	tMuch    = "2026-10-15T03:00:00Z"
)

func openEvent(t *testing.T, s *Store, kind, sev, detail, at string) (DeviceEvent, bool) {
	t.Helper()
	var (
		ev      DeviceEvent
		created bool
	)
	err := s.WithTx(context.Background(), false, func(tx *sql.Tx) error {
		var err error
		ev, created, err = OpenDeviceEvent(context.Background(), tx, DeviceEvent{
			PrinterURI: tPrinter, Kind: kind, Severity: sev, Detail: detail,
			StartedAt: at, UpdatedAt: at,
		})
		return err
	})
	if err != nil {
		t.Fatalf("开事件失败: %v", err)
	}
	return ev, created
}

func listOpen(t *testing.T, s *Store) []DeviceEvent {
	t.Helper()
	var out []DeviceEvent
	err := s.WithTx(context.Background(), true, func(tx *sql.Tx) error {
		var err error
		out, err = ListOpenDeviceEvents(context.Background(), tx)
		return err
	})
	if err != nil {
		t.Fatalf("读未恢复事件失败: %v", err)
	}
	return out
}

// 一分钟报告一次、报告十次，库里也只能有一行 —— 否则一次卡纸一天能攒 1440 行。
func TestOpenDeviceEventIsIdempotent(t *testing.T) {
	s := openTestStore(t)

	first, created := openEvent(t, s, "paper_jam", "error", "media-jam-error", tNow)
	if !created {
		t.Fatal("第一次应当是新建")
	}
	for i := 0; i < 9; i++ {
		got, created := openEvent(t, s, "paper_jam", "error", "media-jam-error", tLater)
		if created {
			t.Fatalf("第 %d 次重复报告不该新建", i+2)
		}
		if got.ID != first.ID {
			t.Fatalf("第 %d 次重复报告换了行：%d → %d", i+2, first.ID, got.ID)
		}
		// started_at 是「从什么时候开始」，不能被刷新成「刚刚」。
		if got.StartedAt != tNow {
			t.Errorf("第 %d 次刷新改掉了 started_at：%q", i+2, got.StartedAt)
		}
		if got.UpdatedAt != tLater {
			t.Errorf("第 %d 次没刷新 updated_at：%q", i+2, got.UpdatedAt)
		}
	}
	if open := listOpen(t, s); len(open) != 1 {
		t.Fatalf("未恢复事件应当只有 1 条，实际 %d 条", len(open))
	}
}

// 严重级别与原文会随每轮刷新 —— 一台机器从「纸快没了」恶化到「没纸了」
// 必须是同一行的升级，而不是两条并列的告警。
func TestOpenDeviceEventRefreshesSeverity(t *testing.T) {
	s := openTestStore(t)

	openEvent(t, s, "out_of_paper", "warning", "media-low-report", tNow)
	got, created := openEvent(t, s, "out_of_paper", "error", "media-empty-error", tLater)
	if created {
		t.Fatal("同一类别不该新建")
	}
	if got.Severity != "error" || got.Detail != "media-empty-error" {
		t.Fatalf("应当刷新成最新的级别与原文，实际 %+v", got)
	}
}

// 不同类别并存；恢复其中一类不影响另一类。
func TestResolveDeviceEventsKeepsOthersOpen(t *testing.T) {
	s := openTestStore(t)

	openEvent(t, s, "out_of_paper", "error", "media-empty-error", tNow)
	openEvent(t, s, "supply_low", "warning", "Black 余量 4%", tNow)

	var resolved []DeviceEvent
	err := s.WithTx(context.Background(), false, func(tx *sql.Tx) error {
		var err error
		// 只剩 supply_low 还在。
		resolved, err = ResolveDeviceEvents(context.Background(), tx, tPrinter,
			[]string{"supply_low"}, tLater)
		return err
	})
	if err != nil {
		t.Fatalf("关事件失败: %v", err)
	}
	if len(resolved) != 1 || resolved[0].Kind != "out_of_paper" {
		t.Fatalf("应当只关掉缺纸那一类，实际 %+v", resolved)
	}
	if resolved[0].ResolvedAt != tLater {
		t.Errorf("恢复时刻不对：%q", resolved[0].ResolvedAt)
	}

	open := listOpen(t, s)
	if len(open) != 1 || open[0].Kind != "supply_low" {
		t.Fatalf("耗材不足应当还开着，实际 %+v", open)
	}
}

// 唯一索引只约束【未恢复】的行：上月卡纸、这月又卡一次，必须是两行 ——
// 否则「这台机器这个月卡了几次」永远答不出来。
func TestResolvedEventCanReopenAsNewRow(t *testing.T) {
	s := openTestStore(t)

	first, _ := openEvent(t, s, "paper_jam", "error", "media-jam-error", tNow)
	err := s.WithTx(context.Background(), false, func(tx *sql.Tx) error {
		_, err := ResolveDeviceEvents(context.Background(), tx, tPrinter, nil, tLater)
		return err
	})
	if err != nil {
		t.Fatalf("关事件失败: %v", err)
	}

	second, created := openEvent(t, s, "paper_jam", "error", "media-jam-error", tMuch)
	if !created {
		t.Fatal("恢复之后再次出现应当是新建")
	}
	if second.ID == first.ID {
		t.Fatal("复发了却复用了旧行，历史记不下来")
	}
	if second.StartedAt != tMuch {
		t.Errorf("新事件的开始时间应当是本次复发时刻，实际 %q", second.StartedAt)
	}

	var (
		total  int
		opened int
	)
	err = s.WithTx(context.Background(), true, func(tx *sql.Tx) error {
		var err error
		if total, err = CountDeviceEvents(context.Background(), tx, ""); err != nil {
			return err
		}
		if opened, err = CountOpenDeviceEvents(context.Background(), tx); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("计数失败: %v", err)
	}
	if total != 2 || opened != 1 {
		t.Errorf("全量 %d 条（期望 2）、未恢复 %d 条（期望 1）", total, opened)
	}
}

func TestDeviceEventFilterAndPaging(t *testing.T) {
	s := openTestStore(t)

	openEvent(t, s, "paper_jam", "error", "media-jam-error", tNow)
	openEvent(t, s, "supply_low", "warning", "Black 余量 4%", tNow)
	err := s.WithTx(context.Background(), false, func(tx *sql.Tx) error {
		_, err := ResolveDeviceEvents(context.Background(), tx, tPrinter, []string{"supply_low"}, tLater)
		return err
	})
	if err != nil {
		t.Fatalf("关事件失败: %v", err)
	}

	var (
		openList, resolvedList []DeviceEvent
		nOpen, nResolved       int
	)
	err = s.WithTx(context.Background(), true, func(tx *sql.Tx) error {
		var err error
		if openList, err = ListDeviceEvents(context.Background(), tx, "open", 10, 0); err != nil {
			return err
		}
		if nOpen, err = CountDeviceEvents(context.Background(), tx, "open"); err != nil {
			return err
		}
		if resolvedList, err = ListDeviceEvents(context.Background(), tx, "resolved", 10, 0); err != nil {
			return err
		}
		if nResolved, err = CountDeviceEvents(context.Background(), tx, "resolved"); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("筛选查询失败: %v", err)
	}
	if len(openList) != nOpen || nOpen != 1 {
		t.Errorf("未恢复：列表 %d、计数 %d，期望都是 1", len(openList), nOpen)
	}
	if len(resolvedList) != nResolved || nResolved != 1 {
		t.Errorf("已恢复：列表 %d、计数 %d，期望都是 1", len(resolvedList), nResolved)
	}

	// 未恢复优先排序：列表页第一眼要看到「还在出问题的」。
	var all []DeviceEvent
	err = s.WithTx(context.Background(), true, func(tx *sql.Tx) error {
		var err error
		all, err = ListDeviceEvents(context.Background(), tx, "", 10, 0)
		return err
	})
	if err != nil {
		t.Fatalf("全量查询失败: %v", err)
	}
	if len(all) != 2 || all[0].ResolvedAt != "" {
		t.Fatalf("未恢复的应当排在最前，实际 %+v", all)
	}
}

func TestResolveDeviceEventByID(t *testing.T) {
	s := openTestStore(t)
	ev, _ := openEvent(t, s, "door_open", "error", "cover-open-error", tNow)

	var first, second bool
	err := s.WithTx(context.Background(), false, func(tx *sql.Tx) error {
		var err error
		if first, err = ResolveDeviceEventByID(context.Background(), tx, ev.ID, tLater); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("标记已处理失败: %v", err)
	}
	if !first {
		t.Fatal("第一次标记应当生效")
	}

	// 幂等：重复点不该报错，也不该再算一次「刚恢复」。
	err = s.WithTx(context.Background(), false, func(tx *sql.Tx) error {
		var err error
		second, err = ResolveDeviceEventByID(context.Background(), tx, ev.ID, tMuch)
		return err
	})
	if err != nil {
		t.Fatalf("重复标记失败: %v", err)
	}
	if second {
		t.Error("已经恢复的事件再标记不该返回 true")
	}

	// 不存在的 id 不能报错，只返回 false —— 前端点了一个刚被别人处理掉的行。
	err = s.WithTx(context.Background(), false, func(tx *sql.Tx) error {
		_, err := ResolveDeviceEventByID(context.Background(), tx, 999999, tMuch)
		return err
	})
	if err != nil {
		t.Fatalf("对不存在的 id 应当安静返回: %v", err)
	}
}

func TestDeviceEventNotifiedAt(t *testing.T) {
	s := openTestStore(t)
	ev, _ := openEvent(t, s, "unreachable", "error", "连接超时", tNow)
	if ev.NotifiedAt != "" {
		t.Fatalf("新事件的 notified_at 应当是空的，实际 %q", ev.NotifiedAt)
	}

	err := s.WithTx(context.Background(), false, func(tx *sql.Tx) error {
		return MarkDeviceEventNotified(context.Background(), tx, ev.ID, tLater)
	})
	if err != nil {
		t.Fatalf("记录通知时间失败: %v", err)
	}

	// 再报告一次时，notified_at 必须被带出来 —— 重发节流全靠它。
	again, _ := openEvent(t, s, "unreachable", "error", "连接超时", tMuch)
	if again.NotifiedAt != tLater {
		t.Errorf("notified_at 丢了：%q", again.NotifiedAt)
	}
}

// 静音：重复静音幂等；查得到；清除只影响指定类别。
func TestDeviceMutes(t *testing.T) {
	s := openTestStore(t)

	mute := func(kinds ...string) {
		t.Helper()
		err := s.WithTx(context.Background(), false, func(tx *sql.Tx) error {
			for _, k := range kinds {
				if err := MuteDeviceKind(context.Background(), tx, tPrinter, k, "admin", tNow); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("静音失败: %v", err)
		}
	}
	mute("out_of_paper", "out_of_paper", "paper_jam") // 故意重复一次

	var kinds []string
	var all map[string]map[string]bool
	err := s.WithTx(context.Background(), true, func(tx *sql.Tx) error {
		var err error
		if kinds, err = ListMutedKinds(context.Background(), tx, tPrinter); err != nil {
			return err
		}
		all, err = ListAllDeviceMutes(context.Background(), tx)
		return err
	})
	if err != nil {
		t.Fatalf("读静音失败: %v", err)
	}
	if len(kinds) != 2 {
		t.Fatalf("重复静音应当幂等，实际 %v", kinds)
	}
	if !all[tPrinter]["out_of_paper"] || !all[tPrinter]["paper_jam"] {
		t.Fatalf("ListAllDeviceMutes 结果不对: %+v", all)
	}
	if all["ipp://other/ipp/print"] != nil {
		t.Error("别的打印机不该有静音")
	}

	err = s.WithTx(context.Background(), false, func(tx *sql.Tx) error {
		return ClearDeviceKindMutes(context.Background(), tx, tPrinter, []string{"out_of_paper"})
	})
	if err != nil {
		t.Fatalf("解除静音失败: %v", err)
	}
	err = s.WithTx(context.Background(), true, func(tx *sql.Tx) error {
		var err error
		kinds, err = ListMutedKinds(context.Background(), tx, tPrinter)
		return err
	})
	if err != nil {
		t.Fatalf("读静音失败: %v", err)
	}
	if len(kinds) != 1 || kinds[0] != "paper_jam" {
		t.Fatalf("应当只剩 paper_jam 被静音，实际 %v", kinds)
	}
}
