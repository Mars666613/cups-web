package pricing

import (
	"errors"
	"testing"
)

// ─── 页码范围解析 ──────────────────────────────────────────────────────

func TestParsePageRanges(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    [][2]int
		wantErr bool
	}{
		{"空串", "", nil, false},
		{"单页", "5", [][2]int{{5, 5}}, false},
		{"单区间", "1-3", [][2]int{{1, 3}}, false},
		{"多个空格分隔", "1-5 8 10-12", [][2]int{{1, 5}, {8, 8}, {10, 12}}, false},
		{"逗号分隔", "1,3,5", [][2]int{{1, 1}, {3, 3}, {5, 5}}, false},
		{"全角逗号", "1，3，5", [][2]int{{1, 1}, {3, 3}, {5, 5}}, false},
		{"顿号", "1、3、5", [][2]int{{1, 1}, {3, 3}, {5, 5}}, false},
		{"分号", "1;3;5", [][2]int{{1, 1}, {3, 3}, {5, 5}}, false},
		{"混合分隔符", "1-3, 5 7、9", [][2]int{{1, 3}, {5, 5}, {7, 7}, {9, 9}}, false},
		{"全角连字符", "1－3", [][2]int{{1, 3}}, false},
		{"破折号", "1—3", [][2]int{{1, 3}}, false},
		{"连字符两侧有空格", "1 - 3", [][2]int{{1, 3}}, false},
		{"起止相同", "4-4", [][2]int{{4, 4}}, false},

		// 下面这些非法写法在 IPP 侧会被静默丢弃（fmt.Sscanf + hi>=lo 判断），
		// 结果是整个文档全打 —— 计费必须在这里拦住，不能放行。
		{"起点大于终点", "5-3", nil, true},
		{"页码为 0", "0-5", nil, true},
		{"单页为 0", "0", nil, true},
		{"负数", "-5", nil, true},
		{"多连字符", "1-2-3", nil, true},
		{"非数字", "abc", nil, true},
		{"数字混字母", "3abc", nil, true},
		{"缺少终点", "5-", nil, true},
		{"缺少起点", "-5x", nil, true},
		{"全角数字（不支持，与前端一致）", "１-３", nil, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParsePageRanges(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("ParsePageRanges(%q) 期望报错，实际得到 %v", c.in, got)
				}
				if !errors.Is(err, ErrInvalidPageRange) {
					t.Errorf("错误类型应为 ErrInvalidPageRange，实际 %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePageRanges(%q) 意外报错: %v", c.in, err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("ParsePageRanges(%q) = %v，期望 %v", c.in, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("ParsePageRanges(%q)[%d] = %v，期望 %v", c.in, i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestNormalizePageRange(t *testing.T) {
	// 输出格式必须是空格分隔的 `n` 或 `lo-hi`（且 lo>=1、hi>=lo）——
	// internal/ipp 的 parsePageRange 靠 strings.Fields 切开再逐段解析，
	// 只有这个格式能保证它读到的区间与这里算出的完全一致。
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"空串", "", ""},
		{"单页", "5", "5"},
		{"单区间", "1-3", "1-3"},
		{"乱序排好", "5 3 1", "1 3 5"},
		{"相邻区间合并", "1-3 4-5", "1-5"},
		{"重叠区间合并", "1-3 2-5", "1-5"},
		{"跨区间排序后合并", "10-12 1-3 4-6", "1-6 10-12"},
		{"重复单页去重", "8 8 8", "8"},
		{"保留不连续的区间", "1-2 5-6", "1-2 5-6"},
		{"混合", "3,1-2,7", "1-3 7"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NormalizePageRange(c.in)
			if err != nil {
				t.Fatalf("NormalizePageRange(%q) 报错: %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("NormalizePageRange(%q) = %q，期望 %q", c.in, got, c.want)
			}
		})
	}
}

func TestNormalizePageRangeRejectsInvalid(t *testing.T) {
	if _, err := NormalizePageRange("5-3"); !errors.Is(err, ErrInvalidPageRange) {
		t.Errorf("期望 ErrInvalidPageRange，实际 %v", err)
	}
}

// ─── 计费印数 ──────────────────────────────────────────────────────────

func TestCountBilledPages(t *testing.T) {
	cases := []struct {
		name      string
		pages     int
		pageRange string
		pageSet   string
		want      int
		wantErr   error
	}{
		// ── 无过滤 ──
		{"全部页", 10, "", "", 10, nil},
		{"全部页显式 all", 10, "", "all", 10, nil},
		{"未知 pageSet 按不过滤", 10, "", "bogus", 10, nil},
		{"单页文档", 1, "", "", 1, nil},
		{"页数兜底为 1", 0, "", "", 1, nil},

		// ── 页码范围 ──
		{"区间", 10, "3-5", "", 3, nil},
		{"单页", 10, "7", "", 1, nil},
		{"多段", 10, "1-3 6 9-10", "", 6, nil},
		{"越界裁剪：4 页文档填 3-5", 4, "3-5", "", 2, nil},
		{"全部越界", 4, "10-20", "", 0, ErrNoPagesSelected},
		{"去重：1-3 与 2-5 重叠只算 5 页", 10, "1-3 2-5", "", 5, nil},
		{"重复单页只算一次", 10, "8 8 8", "", 1, nil},

		// ── 奇偶过滤 ──
		{"奇数页", 5, "", "odd", 3, nil},
		{"偶数页", 5, "", "even", 2, nil},
		{"偶数页 + 偶数总页数", 6, "", "even", 3, nil},
		{"奇数页 + 偶数总页数", 6, "", "odd", 3, nil},
		{"单页文档取偶数 → 无页可打", 1, "", "even", 0, ErrNoPagesSelected},
		{"单页文档取奇数", 1, "", "odd", 1, nil},

		// ── 页码范围 + 奇偶过滤的叠加（IPP 侧顺序：先 range 再 page-set）──
		{"范围 3-5 后取偶数", 10, "3-5", "even", 1, nil}, // {3,4,5} ∩ 偶 = {4}
		{"范围 3-5 后取奇数", 10, "3-5", "odd", 2, nil},  // {3,4,5} ∩ 奇 = {3,5}
		{"范围裁剪后取偶数", 4, "3-5", "even", 1, nil},     // {3,4} ∩ 偶 = {4}
		{"范围全越界后取奇数", 4, "10-20", "odd", 0, ErrNoPagesSelected},

		// ── even-reverse（手动双面）──
		// 只打偶数页；奇数总页数时重排会补一张空白页对齐，补白不耗墨不计费。
		{"偶数页倒序：奇数总页数", 5, "", "even-reverse", 2, nil},
		{"偶数页倒序：偶数总页数", 6, "", "even-reverse", 3, nil},
		{"偶数页倒序：与 even 同价", 9, "", "even-reverse", 4, nil},
		{"偶数页倒序：10 页", 10, "", "even-reverse", 5, nil},
		{"偶数页倒序：单页退化为原样打印", 1, "", "even-reverse", 1, nil},
		{"偶数页倒序不能配页码范围", 10, "1-5", "even-reverse", 0, ErrUnsupportedCombination},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := CountBilledPages(c.pages, c.pageRange, c.pageSet)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("期望 %v，实际 err=%v got=%d", c.wantErr, err, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("意外报错: %v", err)
			}
			if got != c.want {
				t.Errorf("CountBilledPages(%d, %q, %q) = %d，期望 %d",
					c.pages, c.pageRange, c.pageSet, got, c.want)
			}
		})
	}
}

// TestCountBilledPagesOddEvenNeverDoubleCounts 锁死「减半只减一次」。
//
// even 与 even-reverse 走的是两条不同代码路径（前者靠 IPP page-set 过滤，
// 后者靠 PDF 重排），如果实现里既按偶数页减半、又在重排后重算一次，
// 用户会被收两次「减半」—— 这条测试就是防这个。
func TestCountBilledPagesOddEvenNeverDoubleCounts(t *testing.T) {
	const pages = 10
	even, err := CountBilledPages(pages, "", "even")
	if err != nil {
		t.Fatalf("even 报错: %v", err)
	}
	evenReverse, err := CountBilledPages(pages, "", "even-reverse")
	if err != nil {
		t.Fatalf("even-reverse 报错: %v", err)
	}

	if even != 5 || evenReverse != 5 {
		t.Fatalf("10 页文档：even=%d, even-reverse=%d，都应恰好是 5（减半一次）", even, evenReverse)
	}

	odd, err := CountBilledPages(pages, "", "odd")
	if err != nil {
		t.Fatalf("odd 报错: %v", err)
	}
	// 手动双面分两趟提交：一趟奇数、一趟偶数，合计必须正好等于整份文档。
	// 多收（重复计费）或少收（漏计）都会在这里露出来。
	if odd+even != pages {
		t.Errorf("手动双面两趟合计 = %d + %d = %d，期望正好 %d 页",
			odd, even, odd+even, pages)
	}
}

// ─── 单价查询 ──────────────────────────────────────────────────────────

func TestLookupUnitCents(t *testing.T) {
	rules := []Rule{
		{IsColor: false, PaperSize: "A4", PaperType: "plain", UnitCents: 20},
		{IsColor: true, PaperSize: "A4", PaperType: "plain", UnitCents: 100},
		{IsColor: false, PaperSize: "A3", PaperType: "plain", UnitCents: 50},
		{IsColor: false, PaperSize: "A4", PaperType: "photo", UnitCents: 300},
	}

	cases := []struct {
		name      string
		isColor   bool
		paperSize string
		paperType string
		fallback  int64
		wantUnit  int64
		wantMatch bool
	}{
		{"黑白 A4 普通纸", false, "A4", "plain", 0, 20, true},
		{"彩色 A4 普通纸", true, "A4", "plain", 0, 100, true},
		{"黑白 A3 普通纸", false, "A3", "plain", 0, 50, true},
		{"黑白 A4 照片纸", false, "A4", "photo", 0, 300, true},
		{"大小写不敏感", false, "a4", "PLAIN", 0, 20, true},
		{"首尾空格不敏感", false, " A4 ", " plain ", 0, 20, true},
		{"彩色 A3 未配置 → 兜底", true, "A3", "plain", 7, 7, false},
		{"未知纸张类型 → 兜底", false, "A4", "envelope", 7, 7, false},
		{"兜底价为负时按 0", false, "A4", "envelope", -5, 0, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			unit, matched := LookupUnitCents(rules, c.isColor, c.paperSize, c.paperType, c.fallback)
			if unit != c.wantUnit || matched != c.wantMatch {
				t.Errorf("得到 (%d, %v)，期望 (%d, %v)", unit, matched, c.wantUnit, c.wantMatch)
			}
		})
	}
}

func TestLookupUnitCentsEmptyRules(t *testing.T) {
	unit, matched := LookupUnitCents(nil, false, "A4", "plain", 99)
	if matched {
		t.Error("空价目表不应命中")
	}
	if unit != 99 {
		t.Errorf("应返回兜底价 99，实际 %d", unit)
	}
}

// ─── 报价 ──────────────────────────────────────────────────────────────

func TestCompute(t *testing.T) {
	rules := []Rule{
		{IsColor: false, PaperSize: "A4", PaperType: "plain", UnitCents: 20},
		{IsColor: true, PaperSize: "A4", PaperType: "plain", UnitCents: 100},
	}

	cases := []struct {
		name        string
		pages       int
		copies      int
		isColor     bool
		paperType   string // 留空按 plain
		pageRange   string
		pageSet     string
		wantBilled  int
		wantCopies  int
		wantAmount  int64
		wantMatched bool
	}{
		{
			name: "黑白 10 页 1 份", pages: 10, copies: 1,
			wantBilled: 10, wantCopies: 1, wantAmount: 200, wantMatched: true,
		},
		{
			name: "黑白 10 页 3 份", pages: 10, copies: 3,
			wantBilled: 10, wantCopies: 3, wantAmount: 600, wantMatched: true,
		},
		{
			name: "彩色单价更高", pages: 10, copies: 1, isColor: true,
			wantBilled: 10, wantCopies: 1, wantAmount: 1000, wantMatched: true,
		},
		{
			name: "页码范围只收实际印数", pages: 10, copies: 1, pageRange: "3-5",
			wantBilled: 3, wantCopies: 1, wantAmount: 60, wantMatched: true,
		},
		{
			name: "越界裁剪后计费", pages: 4, copies: 1, pageRange: "3-5",
			wantBilled: 2, wantCopies: 1, wantAmount: 40, wantMatched: true,
		},
		{
			name: "奇数页", pages: 10, copies: 1, pageSet: "odd",
			wantBilled: 5, wantCopies: 1, wantAmount: 100, wantMatched: true,
		},
		{
			name: "偶数页倒序与偶数同价", pages: 10, copies: 1, pageSet: "even-reverse",
			wantBilled: 5, wantCopies: 1, wantAmount: 100, wantMatched: true,
		},
		{
			name: "范围 + 份数 + 奇偶三重叠加", pages: 20, copies: 2,
			pageRange: "1-10", pageSet: "even",
			wantBilled: 5, wantCopies: 2, wantAmount: 200, wantMatched: true,
		},
		{
			name: "份数兜底为 1", pages: 5, copies: 0,
			wantBilled: 5, wantCopies: 1, wantAmount: 100, wantMatched: true,
		},
		{
			name: "份数为负也兜底为 1", pages: 5, copies: -3,
			wantBilled: 5, wantCopies: 1, wantAmount: 100, wantMatched: true,
		},
		{
			// 价目表里没有 (黑白, A4, glossy) 这个组合，兜底价传 0，
			// 于是金额是 0 且 Matched=false —— 上层据此提示管理员去配价目。
			name: "未配置的组合走兜底价", pages: 5, copies: 1, paperType: "glossy",
			wantBilled: 5, wantCopies: 1, wantAmount: 0, wantMatched: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			paperType := c.paperType
			if paperType == "" {
				paperType = "plain"
			}
			q, err := Compute(c.pages, c.copies, c.isColor, "A4", paperType,
				c.pageRange, c.pageSet, rules, 0)
			if err != nil {
				t.Fatalf("Compute 报错: %v", err)
			}
			if q.BilledPages != c.wantBilled {
				t.Errorf("BilledPages = %d，期望 %d", q.BilledPages, c.wantBilled)
			}
			if q.Copies != c.wantCopies {
				t.Errorf("Copies = %d，期望 %d", q.Copies, c.wantCopies)
			}
			if q.AmountCents != c.wantAmount {
				t.Errorf("AmountCents = %d，期望 %d", q.AmountCents, c.wantAmount)
			}
			if q.Matched != c.wantMatched {
				t.Errorf("Matched = %v，期望 %v", q.Matched, c.wantMatched)
			}
		})
	}
}

// TestComputeUnmatchedUsesFallback 未命中价目时用兜底价，
// 且 Matched=false 让上层能提示管理员去配价目。
func TestComputeUnmatchedUsesFallback(t *testing.T) {
	q, err := Compute(4, 2, false, "A4", "glossy", "", "", nil, 33)
	if err != nil {
		t.Fatalf("Compute 报错: %v", err)
	}
	if q.Matched {
		t.Error("空价目表不应命中")
	}
	if q.UnitCents != 33 {
		t.Errorf("UnitCents = %d，期望兜底 33", q.UnitCents)
	}
	// 33 × 4 页 × 2 份
	if q.AmountCents != 264 {
		t.Errorf("AmountCents = %d，期望 264", q.AmountCents)
	}
}

// TestComputePropagatesErrors 计价失败必须整体失败，绝不能退化成 0 元单。
func TestComputePropagatesErrors(t *testing.T) {
	cases := []struct {
		name      string
		pageRange string
		pageSet   string
		wantErr   error
	}{
		{"非法页码", "5-3", "", ErrInvalidPageRange},
		{"页码全越界", "90-99", "", ErrNoPagesSelected},
		{"even-reverse 配页码范围", "1-5", "even-reverse", ErrUnsupportedCombination},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Compute(10, 1, false, "A4", "plain", c.pageRange, c.pageSet, nil, 0)
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("期望 %v，实际 %v", c.wantErr, err)
			}
		})
	}
}

// TestComputeIgnoresNumberUp By design：N-up 不是 Compute 的参数。
//
// 产品选择是「按页计费」而非「按张计费」，4 合 1 只是省纸，印的还是那 4 页。
// 这条测试用「同参数多次调用结果一致」来表达这个不变量 ——
// 真正的保证来自函数签名里根本没有 number_up 这个入参。
func TestComputeIgnoresNumberUp(t *testing.T) {
	rules := []Rule{{IsColor: false, PaperSize: "A4", PaperType: "plain", UnitCents: 20}}

	first, err := Compute(4, 1, false, "A4", "plain", "", "", rules, 0)
	if err != nil {
		t.Fatalf("Compute 报错: %v", err)
	}
	for i := 0; i < 3; i++ {
		again, err := Compute(4, 1, false, "A4", "plain", "", "", rules, 0)
		if err != nil {
			t.Fatalf("Compute 报错: %v", err)
		}
		if again != first {
			t.Fatalf("同样输入得到不同报价: %+v vs %+v", again, first)
		}
	}
	if first.AmountCents != 80 {
		t.Errorf("4 页 × 0.20 元 = 80 分，实际 %d", first.AmountCents)
	}
}
