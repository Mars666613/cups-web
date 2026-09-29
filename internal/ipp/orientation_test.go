package ipp

import "testing"

// TestOrientationRequested 锁死「纵向也必须下发」这条契约。
//
// 早先的实现只在横向时下发 orientation-requested，理由是「纵向是 CUPS
// 默认值、显式下发 3 会让 pdftopdf 重新处理文档」。但那样一来，用户对一份
// 【本身就是横向】的 PDF 选「纵向」就完全无效 —— 选择被静默忽略，
// 用户只能看到「我选了纵向，却打成横向」，无从判断是哪里出了问题。
//
// 这个 bug 直到有人在真实打印机上试才暴露：单元测试覆盖不到，
// 因为它不在转换链里，而在 IPP 属性构造这一步。
func TestOrientationRequested(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"纵向下发 3", "portrait", 3},
		{"横向下发 4", "landscape", 4},
		{"空值不下发", "", 0},
		{"未知值不下发", "sideways", 0},
		{"大小写敏感：不认识的写法一律不下发", "Portrait", 0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := orientationRequested(c.in); got != c.want {
				t.Errorf("orientationRequested(%q) = %d，期望 %d", c.in, got, c.want)
			}
		})
	}
}

// TestOrientationIsMutuallyExclusive 确认两个朝向不会同时下发。
// IPP 的 orientation-requested 是单值属性，同时发两个会让 CUPS 报错或取其一。
func TestOrientationIsMutuallyExclusive(t *testing.T) {
	portrait := orientationRequested("portrait")
	landscape := orientationRequested("landscape")
	if portrait == landscape {
		t.Fatal("纵向与横向映射到了同一个值")
	}
	if portrait == 0 || landscape == 0 {
		t.Fatal("两个朝向都应当下发，不能有 0（不下发）")
	}
}
