package pricing

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

// sharedCasesPath 指向前后端共用的用例文件。
//
// 计价有两份实现：这里的 Go 版（真正扣钱的那个）和
// frontend/src/utils/pricing.js（提交前给用户看价格的那个）。
// 两边算法一旦漂移，用户会看到「显示 ¥0.80、实际扣 ¥1.20」，
// 而且他无从判断哪个是对的 —— 所以规则不靠人工同步，靠这个文件锁住。
//
// 前端的对应检查：frontend/scripts/check-pricing.js（node 直接跑，无需测试框架）。
const sharedCasesPath = "../../testdata/pricing_cases.json"

// sentinelByName 把 JSON 里的错误名映射回哨兵错误变量。
var sentinelByName = map[string]error{
	"ErrInvalidPageRange":       ErrInvalidPageRange,
	"ErrNoPagesSelected":        ErrNoPagesSelected,
	"ErrUnsupportedCombination": ErrUnsupportedCombination,
}

type sharedFile struct {
	DefaultRules []Rule `json:"defaultRules"`

	ParsePageRanges []struct {
		Name    string   `json:"name"`
		In      string   `json:"in"`
		Want    [][2]int `json:"want"`
		WantErr string   `json:"wantErr"`
	} `json:"parsePageRanges"`

	NormalizePageRange []struct {
		Name    string `json:"name"`
		In      string `json:"in"`
		Want    string `json:"want"`
		WantErr string `json:"wantErr"`
	} `json:"normalizePageRange"`

	CountBilledPages []struct {
		Name      string `json:"name"`
		Pages     int    `json:"pages"`
		PageRange string `json:"pageRange"`
		PageSet   string `json:"pageSet"`
		Want      int    `json:"want"`
		WantErr   string `json:"wantErr"`
	} `json:"countBilledPages"`

	Compute []struct {
		Name          string `json:"name"`
		Pages         int    `json:"pages"`
		Copies        int    `json:"copies"`
		IsColor       bool   `json:"isColor"`
		PaperSize     string `json:"paperSize"`
		PaperType     string `json:"paperType"`
		PageRange     string `json:"pageRange"`
		PageSet       string `json:"pageSet"`
		NoRules       bool   `json:"noRules"`
		FallbackCents int64  `json:"fallbackCents"`
		WantBilled    int    `json:"wantBilled"`
		WantCopies    int    `json:"wantCopies"`
		WantUnit      int64  `json:"wantUnit"`
		WantAmount    int64  `json:"wantAmount"`
		WantMatched   bool   `json:"wantMatched"`
		WantErr       string `json:"wantErr"`
	} `json:"compute"`
}

func loadSharedCases(t *testing.T) sharedFile {
	t.Helper()
	raw, err := os.ReadFile(sharedCasesPath)
	if err != nil {
		t.Fatalf("读不到共享用例 %s: %v", sharedCasesPath, err)
	}
	var f sharedFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("解析共享用例失败: %v", err)
	}
	return f
}

// wantSentinel 把用例里的错误名解析成哨兵错误。空串表示期望成功。
func wantSentinel(t *testing.T, name string) error {
	t.Helper()
	if name == "" {
		return nil
	}
	e, ok := sentinelByName[name]
	if !ok {
		t.Fatalf("用例引用了未知错误名 %q", name)
	}
	return e
}

// TestSharedPricingCases 跑 testdata/pricing_cases.json 里的全部用例。
//
// 这个文件同时被前端 check-pricing.js 读取，两边必须给出完全一致的结果。
func TestSharedPricingCases(t *testing.T) {
	cases := loadSharedCases(t)

	if len(cases.ParsePageRanges) == 0 || len(cases.CountBilledPages) == 0 ||
		len(cases.Compute) == 0 || len(cases.NormalizePageRange) == 0 {
		t.Fatal("共享用例文件不完整或未加载")
	}

	t.Run("ParsePageRanges", func(t *testing.T) {
		for _, c := range cases.ParsePageRanges {
			t.Run(c.Name, func(t *testing.T) {
				wantErr := wantSentinel(t, c.WantErr)
				got, err := ParsePageRanges(c.In)
				if wantErr != nil {
					if !errors.Is(err, wantErr) {
						t.Fatalf("ParsePageRanges(%q) 期望 %v，实际 err=%v got=%v", c.In, wantErr, err, got)
					}
					return
				}
				if err != nil {
					t.Fatalf("ParsePageRanges(%q) 意外报错: %v", c.In, err)
				}
				if len(got) != len(c.Want) {
					t.Fatalf("ParsePageRanges(%q) = %v，期望 %v", c.In, got, c.Want)
				}
				for i := range got {
					if got[i] != c.Want[i] {
						t.Errorf("ParsePageRanges(%q)[%d] = %v，期望 %v", c.In, i, got[i], c.Want[i])
					}
				}
			})
		}
	})

	t.Run("NormalizePageRange", func(t *testing.T) {
		for _, c := range cases.NormalizePageRange {
			t.Run(c.Name, func(t *testing.T) {
				wantErr := wantSentinel(t, c.WantErr)
				got, err := NormalizePageRange(c.In)
				if wantErr != nil {
					if !errors.Is(err, wantErr) {
						t.Fatalf("NormalizePageRange(%q) 期望 %v，实际 %v", c.In, wantErr, err)
					}
					return
				}
				if err != nil {
					t.Fatalf("NormalizePageRange(%q) 意外报错: %v", c.In, err)
				}
				if got != c.Want {
					t.Errorf("NormalizePageRange(%q) = %q，期望 %q", c.In, got, c.Want)
				}
			})
		}
	})

	t.Run("CountBilledPages", func(t *testing.T) {
		for _, c := range cases.CountBilledPages {
			t.Run(c.Name, func(t *testing.T) {
				wantErr := wantSentinel(t, c.WantErr)
				got, err := CountBilledPages(c.Pages, c.PageRange, c.PageSet)
				if wantErr != nil {
					if !errors.Is(err, wantErr) {
						t.Fatalf("期望 %v，实际 err=%v got=%d", wantErr, err, got)
					}
					return
				}
				if err != nil {
					t.Fatalf("意外报错: %v", err)
				}
				if got != c.Want {
					t.Errorf("CountBilledPages(%d, %q, %q) = %d，期望 %d",
						c.Pages, c.PageRange, c.PageSet, got, c.Want)
				}
			})
		}
	})

	t.Run("Compute", func(t *testing.T) {
		for _, c := range cases.Compute {
			t.Run(c.Name, func(t *testing.T) {
				wantErr := wantSentinel(t, c.WantErr)

				rules := cases.DefaultRules
				if c.NoRules {
					rules = nil
				}

				q, err := Compute(c.Pages, c.Copies, c.IsColor, c.PaperSize, c.PaperType,
					c.PageRange, c.PageSet, rules, c.FallbackCents)
				if wantErr != nil {
					if !errors.Is(err, wantErr) {
						t.Fatalf("期望 %v，实际 %v", wantErr, err)
					}
					return
				}
				if err != nil {
					t.Fatalf("意外报错: %v", err)
				}
				if q.BilledPages != c.WantBilled {
					t.Errorf("BilledPages = %d，期望 %d", q.BilledPages, c.WantBilled)
				}
				if q.Copies != c.WantCopies {
					t.Errorf("Copies = %d，期望 %d", q.Copies, c.WantCopies)
				}
				if q.UnitCents != c.WantUnit {
					t.Errorf("UnitCents = %d，期望 %d", q.UnitCents, c.WantUnit)
				}
				if q.AmountCents != c.WantAmount {
					t.Errorf("AmountCents = %d，期望 %d", q.AmountCents, c.WantAmount)
				}
				if q.Matched != c.WantMatched {
					t.Errorf("Matched = %v，期望 %v", q.Matched, c.WantMatched)
				}
			})
		}
	})
}
