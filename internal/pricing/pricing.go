// Package pricing 是打印计费的纯计算内核：只有算术，不碰数据库、不发 HTTP。
//
// 单独成包的理由是这里的规则最容易错，而且错了直接等于收错钱 ——
// 必须能脱离服务器跑表驱动测试（见 pricing_test.go）。
//
// 计费口径是「实际印数」，即本次真正会着墨的文档页数：
//
//	billed_pages = | pageRange ∩ [1..N] | 再按 pageSet 过滤奇数/偶数
//	amount       = 单价(彩色?,纸型,纸张类型) × billed_pages × copies
//
// N-up（number_up）不影响金额：产品选择是「按页计费」而非「按张计费」，
// 4 合 1 只是省纸，印的还是那 4 页。orientation / mirror / watermark /
// print_scaling 同理都不改变页数。
package pricing

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	// ErrInvalidPageRange 页码范围写法非法。与 internal/ipp 的宽松解析不同，
	// 这里必须严格：非法片段被静默忽略会导致「以为只打 3-5 页、实际打了全文」，
	// 对计费不可接受。
	ErrInvalidPageRange = errors.New("pricing: invalid page range")

	// ErrNoPagesSelected 页码范围 + 奇偶过滤之后没有任何页可打。
	ErrNoPagesSelected = errors.New("pricing: no pages selected")

	// ErrUnsupportedCombination 选项组合语义不可预期，拒绝计价而不是猜。
	ErrUnsupportedCombination = errors.New("pricing: unsupported option combination")
)

// ─── 页码范围解析 ──────────────────────────────────────────────────────

// normalizeRangeSeparators 把各种全角/异体标点归一化成 ASCII，
// 与前端 PrintOptions.vue 的 validatePageRange 保持同一套规则。
var rangeSeparatorReplacer = strings.NewReplacer(
	"－", "-", "—", "-", "–", "-", "―", "-", // 全角/破折号 → 半角连字符
	"，", ",", "、", ",", // 全角逗号、顿号 → 半角逗号
	"；", ";", // 全角分号
	"　", " ", // 全角空格
)

// hyphenSpaceRe 匹配连字符两侧的空白，用于在切分之前把 "1 - 3" 粘成 "1-3"。
// 不先粘的话 FieldsFunc 会把它切成 "1"、"-"、"3" 三段，中间那个孤立的
// 连字符会被判成非法写法 —— 而用户写 "1 - 3" 的意图再清楚不过。
// 前端 validatePageRange 里对应的就是 replace(/\s*-\s*/g, '-')。
var hyphenSpaceRe = regexp.MustCompile(`[ \t]*-[ \t]*`)

// ParsePageRanges 严格解析 "1-3 5 8-10" 形式的页码范围。
//
// 接受空格、逗号、分号混合分隔（前端会把逗号归一化成空格，API 调用方可能直接用逗号）。
// 逐段严格校验：必须是纯数字或 `数字-数字`，且 lo >= 1、hi >= lo。
// 任何一段不合法都返回 ErrInvalidPageRange，不做静默丢弃。
//
// 注意返回值【不做越界裁剪】—— 文档只有 4 页时输入 "3-5" 会原样返回 [3,5]，
// 由 CountBilledPages 负责与实际页数取交集。这样才能把「用户填的范围」
// 与「实际能打的页」分开：前者用于落库与发给 IPP，后者用于计费。
func ParsePageRanges(s string) ([][2]int, error) {
	s = strings.TrimSpace(rangeSeparatorReplacer.Replace(s))
	if s == "" {
		return nil, nil
	}
	// 必须在按分隔符切分之前把 "1 - 3" 粘成 "1-3"，否则那个孤立连字符会被判非法。
	s = hyphenSpaceRe.ReplaceAllString(s, "-")

	// 空格 / 逗号 / 分号都是分隔符
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == ',' || r == ';'
	})

	ranges := make([][2]int, 0, len(fields))
	for _, f := range fields {
		if f == "" {
			continue
		}
		lo, hi, err := parseRangeToken(f)
		if err != nil {
			return nil, err
		}
		ranges = append(ranges, [2]int{lo, hi})
	}
	return ranges, nil
}

func parseRangeToken(tok string) (int, int, error) {
	if idx := strings.Index(tok, "-"); idx >= 0 {
		loStr, hiStr := tok[:idx], tok[idx+1:]
		// 不允许 "1-2-3" 或 "-5" 这类多连字符写法：
		// 前者 hiStr 里还有 '-'，后者 loStr 为空。
		if loStr == "" || hiStr == "" || strings.Contains(hiStr, "-") {
			return 0, 0, fmt.Errorf("%w: %q", ErrInvalidPageRange, tok)
		}
		lo, err := parsePositiveInt(loStr)
		if err != nil {
			return 0, 0, fmt.Errorf("%w: %q", ErrInvalidPageRange, tok)
		}
		hi, err := parsePositiveInt(hiStr)
		if err != nil {
			return 0, 0, fmt.Errorf("%w: %q", ErrInvalidPageRange, tok)
		}
		if hi < lo {
			// "5-3" 在 IPP 侧会被静默丢弃（parsePageRange 要求 hi >= lo），
			// 结果是整个文档全打 —— 必须在这里拦住。
			return 0, 0, fmt.Errorf("%w: 区间起点大于终点 (%q)", ErrInvalidPageRange, tok)
		}
		return lo, hi, nil
	}

	n, err := parsePositiveInt(tok)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: %q", ErrInvalidPageRange, tok)
	}
	return n, n, nil
}

func parsePositiveInt(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	if n < 1 {
		return 0, fmt.Errorf("页码必须 >= 1")
	}
	return n, nil
}

// NormalizePageRange 输出去重、排序、合并相邻/重叠区间后的规范形式。
//
// 结果会同时写进 print_orders.page_range 并发给 IPP —— internal/ipp 用自己那套
// 宽松解析读这个规范串，得到的结果与这里完全一致，所以不需要改动 ipp 包。
//
// 空输入返回空串（表示「全部页」）。
func NormalizePageRange(s string) (string, error) {
	ranges, err := ParsePageRanges(s)
	if err != nil {
		return "", err
	}
	if len(ranges) == 0 {
		return "", nil
	}

	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i][0] != ranges[j][0] {
			return ranges[i][0] < ranges[j][0]
		}
		return ranges[i][1] < ranges[j][1]
	})

	merged := [][2]int{ranges[0]}
	for _, r := range ranges[1:] {
		last := &merged[len(merged)-1]
		// 相邻（1-3 与 4-5）也合并成 1-5 —— 两者之间没有空洞，分开写没有意义。
		if r[0] <= last[1]+1 {
			if r[1] > last[1] {
				last[1] = r[1]
			}
			continue
		}
		merged = append(merged, r)
	}

	parts := make([]string, 0, len(merged))
	for _, r := range merged {
		if r[0] == r[1] {
			parts = append(parts, strconv.Itoa(r[0]))
		} else {
			parts = append(parts, strconv.Itoa(r[0])+"-"+strconv.Itoa(r[1]))
		}
	}
	return strings.Join(parts, " "), nil
}

// ─── 计费印数 ──────────────────────────────────────────────────────────

// CountBilledPages 计算本次实际会着墨的文档页数（不含 copies，不含 N-up 折算）。
//
// 过滤次序与 IPP 侧一致（见 internal/ipp/client.go 的注释：range 先应用，
// odd/even 后过滤）：
//
//  1. S = [1..N]，pageRange 非空时取它与 [1..N] 的交集
//     —— 越界部分静默裁剪（文档 4 页填 "3-5" → S={3,4}），
//     但裁剪后为空则报 ErrNoPagesSelected
//  2. "odd" 取奇数页、"even" 取偶数页
//  3. "even-reverse" 按 "even" 计（见下）
//
// even-reverse 的两个特例：
//
//   - 与 pageRange 同时出现 → ErrUnsupportedCombination。代码里 page-ranges 是
//     原样透传的（print_handlers.go），而文档此时已被重排成「偶数页倒序」，
//     于是用户填的 "1-2" 指的是重排后文档的第 1、2 页 —— 既不可预期也无法向用户解释。
//   - N <= 1 时退化为普通打印：reorderPDFForManualDuplex 的调用条件是 pages > 1，
//     不满足时既不重排也不下发 page-set，实际会原样打印整份文档，所以按 N 计费。
//
// 其余情况下 even-reverse 与 even 同价：都只打偶数页，重排只改变出纸顺序，
// 奇数总页数时补的那张空白页不耗墨、不计费。
func CountBilledPages(pages int, pageRange, pageSet string) (int, error) {
	n := pages
	if n < 1 {
		// 与打印管线一致：页数解析失败时按 1 页兜底（print_handlers.go 的 pages < 1 分支）。
		n = 1
	}

	set := strings.ToLower(strings.TrimSpace(pageSet))
	rangeStr := strings.TrimSpace(pageRange)

	if set == "even-reverse" {
		if rangeStr != "" {
			return 0, fmt.Errorf("%w: 偶数页倒序不能与页码范围同时使用", ErrUnsupportedCombination)
		}
		if n <= 1 {
			return n, nil
		}
		return n / 2, nil
	}

	selected, err := selectPages(n, rangeStr)
	if err != nil {
		return 0, err
	}

	switch set {
	case "odd":
		selected = filterParity(selected, 1)
	case "even":
		selected = filterParity(selected, 0)
		// 其余值（"all" / "" / 未知）按不过滤处理，与 ipp.normalizePageSet
		// 对未知值返回空（即不下发 page-set）的行为一致。
	}

	if len(selected) == 0 {
		return 0, ErrNoPagesSelected
	}
	return len(selected), nil
}

// selectPages 求 pageRange 与 [1..n] 的交集。pageRange 为空表示全部页。
func selectPages(n int, pageRange string) ([]int, error) {
	if pageRange == "" {
		all := make([]int, n)
		for i := range all {
			all[i] = i + 1
		}
		return all, nil
	}

	ranges, err := ParsePageRanges(pageRange)
	if err != nil {
		return nil, err
	}
	if len(ranges) == 0 {
		all := make([]int, n)
		for i := range all {
			all[i] = i + 1
		}
		return all, nil
	}

	// 用集合去重：用户写 "1-3 2-5" 时真正的页数是 5 而不是 7。
	seen := make(map[int]bool, n)
	var out []int
	for _, r := range ranges {
		lo, hi := r[0], r[1]
		if lo < 1 {
			lo = 1
		}
		if hi > n {
			hi = n // 越界裁剪
		}
		for p := lo; p <= hi; p++ {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	sort.Ints(out)
	return out, nil
}

func filterParity(pages []int, want int) []int {
	out := pages[:0:0] // 新底层数组，别改到入参
	for _, p := range pages {
		if p%2 == want {
			out = append(out, p)
		}
	}
	return out
}

// ─── 单价与报价 ────────────────────────────────────────────────────────

// Rule 是一条单价规则：单价(是否彩色, 纸张尺寸, 纸张类型)，单位「分」。
//
// JSON 标签是必须的：testdata/pricing_cases.json 里的规则直接反序列化成本类型，
// 管理端的价格配置接口（/api/admin/prices）也会直接收发它。
type Rule struct {
	IsColor   bool   `json:"isColor"`
	PaperSize string `json:"paperSize"`
	PaperType string `json:"paperType"`
	UnitCents int64  `json:"unitCents"`
}

// LookupUnitCents 按 (是否彩色, 纸型, 纸张类型) 查单价。
//
// 未命中时返回 fallbackCents 与 false —— 调用方据此提示管理员「该组合未配置价目」，
// 而不是悄悄按 0 元打印。
//
// 比较是大小写不敏感 + 去首尾空格的：价目表由管理员手工配置，
// "A4" 与 "a4" 被判成两种纸会让排查成本高得离谱。
func LookupUnitCents(rules []Rule, isColor bool, paperSize, paperType string,
	fallbackCents int64) (int64, bool) {
	if fallbackCents < 0 {
		fallbackCents = 0
	}
	size := normalizeDim(paperSize)
	ptype := normalizeDim(paperType)

	for _, r := range rules {
		if r.IsColor == isColor &&
			normalizeDim(r.PaperSize) == size &&
			normalizeDim(r.PaperType) == ptype {
			return r.UnitCents, true
		}
	}
	return fallbackCents, false
}

func normalizeDim(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

// Quote 是下单时的计费快照，逐列落进 print_orders。
type Quote struct {
	BilledPages int   // 计费印数（未乘份数）
	Copies      int   // 份数，已兜底为 >= 1
	UnitCents   int64 // 命中或兜底的单价
	AmountCents int64 // 应付 = UnitCents × BilledPages × Copies
	Matched     bool  // false = 走了兜底价，价目表里没有这个组合
}

// Compute 是下单计费的唯一入口。
func Compute(pages, copies int, isColor bool, paperSize, paperType,
	pageRange, pageSet string, rules []Rule, fallbackCents int64) (Quote, error) {
	if copies < 1 {
		copies = 1
	}

	billed, err := CountBilledPages(pages, pageRange, pageSet)
	if err != nil {
		return Quote{}, err
	}

	unit, matched := LookupUnitCents(rules, isColor, paperSize, paperType, fallbackCents)

	return Quote{
		BilledPages: billed,
		Copies:      copies,
		UnitCents:   unit,
		AmountCents: unit * int64(billed) * int64(copies),
		Matched:     matched,
	}, nil
}
