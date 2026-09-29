package main

import (
	"strings"
	"testing"
)

func TestParsePriceRulesValid(t *testing.T) {
	got, err := parsePriceRules([]priceRulePayload{
		{IsColor: false, PaperSize: "A4", PaperType: "plain", UnitCents: 20},
		{IsColor: true, PaperSize: "A4", PaperType: "plain", UnitCents: 100},
		{IsColor: false, PaperSize: "A3", PaperType: "photo", UnitCents: 300},
	})
	if err != nil {
		t.Fatalf("意外报错: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("规则条数 = %d，期望 3", len(got))
	}
	if got[0].UnitCents != 20 || got[1].UnitCents != 100 || got[2].PaperSize != "A3" {
		t.Errorf("解析结果不对: %+v", got)
	}
}

// 首尾空格要抹掉：管理端从别处粘贴过来的值经常带空格，
// 不处理的话库里会存下 "A4 " 这种永远命中不了的维度。
func TestParsePriceRulesTrimsWhitespace(t *testing.T) {
	got, err := parsePriceRules([]priceRulePayload{
		{IsColor: false, PaperSize: "  A4 ", PaperType: " plain\t", UnitCents: 20},
	})
	if err != nil {
		t.Fatalf("意外报错: %v", err)
	}
	if got[0].PaperSize != "A4" || got[0].PaperType != "plain" {
		t.Errorf("未去除首尾空格: %+v", got[0])
	}
}

// 空数组是合法输入（表示「清空价目表」），不是错误。
func TestParsePriceRulesEmpty(t *testing.T) {
	got, err := parsePriceRules(nil)
	if err != nil {
		t.Fatalf("意外报错: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("期望空结果，实际 %+v", got)
	}
}

func TestParsePriceRulesRejects(t *testing.T) {
	cases := []struct {
		name string
		in   []priceRulePayload
		// 错误信息里应包含的片段，用来确认报的是【哪一个】问题
		wantContains string
	}{
		{
			name: "单价为负",
			in: []priceRulePayload{
				{PaperSize: "A4", PaperType: "plain", UnitCents: -1},
			},
			wantContains: "单价不能为负数",
		},
		{
			name: "纸张尺寸为空",
			in: []priceRulePayload{
				{PaperSize: "  ", PaperType: "plain", UnitCents: 20},
			},
			wantContains: "必须填写纸张尺寸与纸张类型",
		},
		{
			name: "纸张类型为空",
			in: []priceRulePayload{
				{PaperSize: "A4", PaperType: "", UnitCents: 20},
			},
			wantContains: "必须填写纸张尺寸与纸张类型",
		},
		{
			// 数据库唯一索引是大小写敏感的，所以这里必须挡住 ——
			// 否则库里会同时存在 "A4" 与 "a4"，而 pricing.LookupUnitCents
			// 也是大小写不敏感匹配，命中哪条就变成看插入顺序了。
			name: "维度大小写不同也算重复",
			in: []priceRulePayload{
				{PaperSize: "A4", PaperType: "plain", UnitCents: 20},
				{PaperSize: "a4", PaperType: "PLAIN", UnitCents: 30},
			},
			wantContains: "价目重复",
		},
		{
			name: "维度带空格也算重复",
			in: []priceRulePayload{
				{PaperSize: "A4", PaperType: "plain", UnitCents: 20},
				{PaperSize: " A4 ", PaperType: " plain ", UnitCents: 30},
			},
			wantContains: "价目重复",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parsePriceRules(c.in)
			if err == nil {
				t.Fatal("期望报错，实际通过")
			}
			if !strings.Contains(err.Error(), c.wantContains) {
				t.Errorf("错误信息 = %q，期望包含 %q", err.Error(), c.wantContains)
			}
		})
	}
}

// 彩色与黑白是不同维度，同一个纸型/纸张类型应当能各存一条。
func TestParsePriceRulesColorIsSeparateDimension(t *testing.T) {
	got, err := parsePriceRules([]priceRulePayload{
		{IsColor: false, PaperSize: "A4", PaperType: "plain", UnitCents: 20},
		{IsColor: true, PaperSize: "A4", PaperType: "plain", UnitCents: 100},
	})
	if err != nil {
		t.Fatalf("彩色与黑白不应被判为重复: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("规则条数 = %d，期望 2", len(got))
	}
}

func TestParsePriceRulesRejectsTooMany(t *testing.T) {
	in := make([]priceRulePayload, maxPriceRules+1)
	for i := range in {
		in[i] = priceRulePayload{PaperSize: "A4", PaperType: "plain", UnitCents: 1}
	}
	_, err := parsePriceRules(in)
	if err == nil {
		t.Fatal("超过上限应当报错")
	}
	if !strings.Contains(err.Error(), "不能超过") {
		t.Errorf("错误信息 = %q", err.Error())
	}
}

// 边界：正好等于上限应当放行。
func TestParsePriceRulesAtLimit(t *testing.T) {
	in := make([]priceRulePayload, maxPriceRules)
	for i := range in {
		// 维度必须互不相同，否则先撞重复检测
		in[i] = priceRulePayload{
			PaperSize: "A4",
			PaperType: "type-" + strings.Repeat("x", i%8) + string(rune('a'+i%26)) + strings.Repeat("y", i/26),
			UnitCents: 1,
		}
	}
	if _, err := parsePriceRules(in); err != nil {
		t.Fatalf("正好 %d 条不应报错: %v", maxPriceRules, err)
	}
}
