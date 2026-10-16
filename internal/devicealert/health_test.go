package devicealert

import (
	"strings"
	"testing"

	"cups-web/internal/ipp"
)

// kinds 把结果压成 "kind/severity" 的列表，方便断言顺序与级别。
func kinds(issues []Issue) []string {
	out := make([]string, 0, len(issues))
	for _, is := range issues {
		out = append(out, is.Kind+"/"+is.Severity)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// 探不通是【唯一】的结论：此时 printer-state-reasons 要么是空的、要么是上次
// 连上时的旧值，照着它报「缺纸」会把管理员指到完全错误的方向。
func TestEvaluateUnreachableIgnoresReasons(t *testing.T) {
	got := Evaluate(Snapshot{
		Probed:       true,
		Reachable:    false,
		ProbeMessage: "连接超时，打印机可能已关机或不在网络上",
		StateReasons: []string{"media-empty-error"}, // 陈旧信号，必须被忽略
		MarkerLevels: []int{0},
		MarkerNames:  []string{"Black"},
	}, Options{})

	if len(got) != 1 {
		t.Fatalf("探不通时应当只报一条，得到 %v", kinds(got))
	}
	if got[0].Kind != KindUnreachable || got[0].Severity != SeverityError {
		t.Fatalf("应当是 unreachable/error，得到 %v", kinds(got))
	}
	if !strings.Contains(got[0].Detail, "关机") {
		t.Fatalf("detail 应当保留探测的原始原因，得到 %q", got[0].Detail)
	}
}

// 走队列退路（探不了设备，如 usb://）时【绝不能】报不可达 ——
// 那会把所有 USB 打印机的用户变成假告警。
func TestEvaluateQueueFallbackNeverUnreachable(t *testing.T) {
	got := Evaluate(FromQueue(&ipp.PrinterInfo{
		StateReasons: []string{"media-empty-error"},
		MarkerLevels: []int{0}, // cupsd 的缓存墨量，必须被忽略
		MarkerNames:  []string{"Black"},
	}), Options{})

	if len(got) != 1 || got[0].Kind != KindOutOfPaper {
		t.Fatalf("队列退路应当只报缺纸、不看墨量，得到 %v", kinds(got))
	}
	if got[0].Severity != SeverityError {
		t.Fatalf("裸的 media-empty 默认是 error，得到 %s", got[0].Severity)
	}
}

// 未探测（Probed=false）时连 reasons 也不该有来源 —— 空快照必须安静。
func TestEvaluateEmptySnapshotIsQuiet(t *testing.T) {
	for _, snap := range []Snapshot{
		{},
		{StateReasons: []string{"", "none", "none-error"}},
		{Probed: true, Reachable: true},
	} {
		if got := Evaluate(snap, Options{}); len(got) != 0 {
			t.Fatalf("快照 %+v 不该产生告警，得到 %v", snap, kinds(got))
		}
	}
}

// 后缀说明的级别优先于规则表：打印机自己说 -warning / -report 时，
// 不该被规则表里的 error 顶上去。
func TestEvaluateReasonSuffixOverridesDefaultSeverity(t *testing.T) {
	got := Evaluate(Snapshot{
		Probed: true, Reachable: true,
		StateReasons: []string{"media-empty-report", "media-needed-warning"},
	}, Options{})

	// 两条归到同一类别 out_of_paper，级别取更严重的 warning。
	if len(got) != 1 || got[0].Kind != KindOutOfPaper {
		t.Fatalf("应当合并成一条 out_of_paper，得到 %v", kinds(got))
	}
	if got[0].Severity != SeverityWarning {
		t.Fatalf("两边都是非 error 后缀，合并后应当是 warning，得到 %s", got[0].Severity)
	}
	// 原文都要留着 —— 排查时「两条原因同时成立」是有信息的。
	for _, want := range []string{"media-empty-report", "media-needed-warning"} {
		if !strings.Contains(got[0].Detail, want) {
			t.Fatalf("detail 应当含 %q，得到 %q", want, got[0].Detail)
		}
	}
}

// -report 的已知类别按 warning 处理。media-low / marker-waste-almost-full
// 恰恰常以 -report 出现，而那两条正是「纸快没了」这类最该提前知道的事。
func TestEvaluateReportBecomesWarning(t *testing.T) {
	got := Evaluate(Snapshot{
		Probed: true, Reachable: true,
		StateReasons: []string{"media-low-report", "marker-waste-almost-full-report"},
	}, Options{})

	if !equalStrings(kinds(got), []string{KindPaperLow + "/warning", KindWasteFull + "/warning"}) {
		t.Fatalf("两条都应当是 warning，得到 %v", kinds(got))
	}
}

// 认不出的裸名忽略；带了 -error/-warning 的认不出也要留下来（归到 other）——
// 那至少说明打印机明确报了一个故障，丢掉等于静默。
func TestEvaluateUnknownReasons(t *testing.T) {
	got := Evaluate(Snapshot{
		Probed: true, Reachable: true,
		StateReasons: []string{"vendor-private-thing", "cups-something-error"},
	}, Options{})

	if !equalStrings(kinds(got), []string{KindOther + "/error"}) {
		t.Fatalf("裸的私有原因应忽略、带 -error 的应留下，得到 %v", kinds(got))
	}
	if got[0].Detail != "cups-something-error" {
		t.Fatalf("detail 应当是原文，得到 %q", got[0].Detail)
	}
}

func TestEvaluateMarkerLevels(t *testing.T) {
	got := Evaluate(Snapshot{
		Probed: true, Reachable: true,
		MarkerNames:  []string{"Black", "Cyan", "Magenta", "Yellow", "Waste"},
		MarkerLevels: []int{0, 3, 40, -3, -1},
	}, Options{})

	if !equalStrings(kinds(got), []string{
		KindSupplyEmpty + "/error",
		KindSupplyLow + "/warning",
	}) {
		t.Fatalf("应当只有 Black 耗尽 + Cyan 不足，得到 %v", kinds(got))
	}
	if !strings.Contains(got[0].Detail, "Black") || !strings.Contains(got[0].Detail, "0%") {
		t.Fatalf("detail 应当带名字与读数，得到 %q", got[0].Detail)
	}
}

// 阈值可调；负数（IPP 的未知 / 不限量）任何阈值下都不能报。
func TestEvaluateMarkerThreshold(t *testing.T) {
	snap := Snapshot{
		Probed: true, Reachable: true,
		MarkerNames:  []string{"A", "B"},
		MarkerLevels: []int{25, -2},
	}
	if got := Evaluate(snap, Options{}); len(got) != 0 {
		t.Fatalf("默认阈值 10 时 25%% 不该报，得到 %v", kinds(got))
	}
	got := Evaluate(snap, Options{SupplyLowPercent: 30})
	if !equalStrings(kinds(got), []string{KindSupplyLow + "/warning"}) {
		t.Fatalf("阈值 30 时 25%% 应当报 supply_low，得到 %v", kinds(got))
	}
}

// 墨量名字缺失时不能 panic，也不能空白一片：给一个可读的占位。
func TestEvaluateMarkerWithoutName(t *testing.T) {
	got := Evaluate(Snapshot{
		Probed: true, Reachable: true,
		MarkerLevels: []int{0},
	}, Options{})
	if len(got) != 1 || got[0].Detail == "" {
		t.Fatalf("缺名字也应当有可读 detail，得到 %v", got)
	}
}

// 同一类别多条原因合并；故障排在提醒前面；顺序稳定（不随 map 遍历抖动）。
func TestEvaluateOrderAndDedup(t *testing.T) {
	snap := Snapshot{
		Probed: true, Reachable: true,
		StateReasons: []string{"media-low-report", "media-empty-error", "media-jam-error"},
		MarkerNames:  []string{"Black"},
		MarkerLevels: []int{0},
	}
	want := []string{
		KindOutOfPaper + "/error",
		KindPaperJam + "/error",
		KindSupplyEmpty + "/error",
		KindPaperLow + "/warning",
	}
	for i := 0; i < 5; i++ {
		if got := Evaluate(snap, Options{}); !equalStrings(kinds(got), want) {
			t.Fatalf("第 %d 次顺序不一致：得到 %v，期望 %v", i+1, kinds(got), want)
		}
	}
}

// 类别中文必须齐 —— 漏一条的表现是界面上冒出一行英文的 paper_jam。
func TestKindLabelsCoverAllKinds(t *testing.T) {
	for _, k := range AllKinds() {
		if KindLabel(k) == k {
			t.Fatalf("%s 没有中文标签", k)
		}
	}
	// 也不能有重复：重复意味着某一条判定的结果会覆盖另一条。
	seen := map[string]bool{}
	for _, k := range AllKinds() {
		if seen[k] {
			t.Fatalf("AllKinds 里 %s 出现了两次", k)
		}
		seen[k] = true
	}
	if KindLabel("nonsense") != "nonsense" {
		t.Fatal("未知类别应当原样返回")
	}
	if SeverityLabel(SeverityError) == SeverityError || SeverityLabel(SeverityWarning) == SeverityWarning {
		t.Fatal("严重级别没有中文标签")
	}
}

// 规则表指向的类别必须在 AllKinds 里 —— 否则管理端的筛选下拉会漏掉
// 一个真实会出现的类别。
func TestReasonRulesKindsAreListed(t *testing.T) {
	known := map[string]bool{}
	for _, k := range AllKinds() {
		known[k] = true
	}
	for _, r := range reasonRules {
		if !known[r.kind] {
			t.Fatalf("规则 %q 指向的类别 %q 不在 AllKinds 里", r.prefix, r.kind)
		}
	}
}

// 规则表本身的自洽性：前缀不能是另一个前缀的前缀（否则后面的规则永远不生效），
// 且 kind 必须都有中文标签。
func TestReasonRulesSelfConsistent(t *testing.T) {
	for i, r := range reasonRules {
		if strings.TrimSpace(r.prefix) == "" {
			t.Fatalf("第 %d 条规则前缀为空", i)
		}
		if KindLabel(r.kind) == r.kind {
			t.Fatalf("规则 %q 指向的类别 %q 没有中文标签", r.prefix, r.kind)
		}
		if r.sev != SeverityError && r.sev != SeverityWarning {
			t.Fatalf("规则 %q 的级别 %q 不合法", r.prefix, r.sev)
		}
		// 前面的规则若已经吃掉了这条前缀，本条就是死代码。
		for j := 0; j < i; j++ {
			if strings.HasPrefix(r.prefix, reasonRules[j].prefix) {
				t.Fatalf("规则 %q 被前面的 %q 遮住了（永远不会生效）", r.prefix, reasonRules[j].prefix)
			}
		}
	}
}

func TestSplitReasonSuffix(t *testing.T) {
	cases := map[string]struct{ base, suffix string }{
		"media-empty-error":        {"media-empty", "error"},
		"media-empty-warning":      {"media-empty", "warning"},
		"media-empty-report":       {"media-empty", "report"},
		"media-empty-none":         {"media-empty", "none"},
		"media-empty":              {"media-empty", ""},
		"marker-waste-almost-full": {"marker-waste-almost-full", ""},
		"":                         {"", ""},
	}
	for in, want := range cases {
		base, suffix := splitReasonSuffix(in)
		if base != want.base || suffix != want.suffix {
			t.Fatalf("splitReasonSuffix(%q) = (%q,%q)，期望 (%q,%q)",
				in, base, suffix, want.base, want.suffix)
		}
	}
}
