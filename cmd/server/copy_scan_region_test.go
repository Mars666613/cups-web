package main

import (
	"testing"

	"cups-web/internal/escl"
)

// realCaps 是现场机器的能力（RICOH Pro C5200S，实测值）：
// 玻璃板与进稿器都报 3507×5102（297×432mm）。
var realCaps = &escl.Capabilities{
	HasPlaten: true, HasAdf: true, AdfDuplex: true,
	MaxWidth: 3507, MaxHeight: 5102,
	AdfMaxWidth: 3507, AdfMaxHeight: 5102,
}

func TestScanRegion(t *testing.T) {
	cases := []struct {
		name   string
		paper  string
		source string
		caps   *escl.Capabilities
		wantW  int
		wantH  int
	}{
		{
			// 核心用例：A4 走进稿器时是「长边横跨、短边走纸」，区域必须是
			// 297×210mm —— 给大了（设备最大幅面）会采成 A3 页面，打印时
			// 被转 90° 缩到 0.687 倍，出来是横着的复印件。
			// 297mm 换算过来是 3508，但设备上限报的是 3507，收敛后取 3507
			// （差 0.08mm，无损）。
			name:  "A4 走 ADF：长边横跨、短边走纸",
			paper: "A4", source: escl.SourceADF, caps: realCaps,
			wantW: 3507, wantH: 2480,
		},
		{
			name:  "A4 走玻璃板：纵向",
			paper: "A4", source: escl.SourcePlaten, caps: realCaps,
			wantW: 2480, wantH: 3508,
		},
		{
			// A3 的长边（420mm）比进稿器（297mm）还宽，只能短边横跨。
			name:  "A3 走 ADF：短边横跨、长边走纸",
			paper: "A3", source: escl.SourceADF, caps: realCaps,
			wantW: 3507, wantH: 4961,
		},
		{
			name:  "A3 走玻璃板",
			paper: "A3", source: escl.SourcePlaten, caps: realCaps,
			wantW: 3507, wantH: 4961,
		},
		{
			name:  "A5 走 ADF",
			paper: "A5", source: escl.SourceADF, caps: realCaps,
			wantW: 2480, wantH: 1748,
		},
		{
			name:  "小写纸型名也认",
			paper: "a4", source: escl.SourcePlaten, caps: realCaps,
			wantW: 2480, wantH: 3508,
		},
		{
			// 纸型不认识：退回设备最大幅面。多采一圈白边由打印端缩放，
			// 总好过按猜测裁掉原件内容。
			name:  "纸型不认识退回设备最大幅面",
			paper: "B5", source: escl.SourceADF, caps: realCaps,
			wantW: 3507, wantH: 5102,
		},
		{
			name:  "纸型为空退回设备最大幅面",
			paper: "", source: escl.SourcePlaten, caps: realCaps,
			wantW: 3507, wantH: 5102,
		},
		{
			// 设备没上报进稿器幅面：按「长边横跨」处理，这是常见装法。
			name:  "进稿器幅面未知仍按长边横跨",
			paper: "A4", source: escl.SourceADF,
			caps:  &escl.Capabilities{HasPlaten: true, HasAdf: true, MaxWidth: 3507, MaxHeight: 5102},
			wantW: 3507, wantH: 2480,
		},
		{
			// 窄进稿器（Letter 幅面，横跨上限 216mm）：A4 的长边放不下。
			name:  "窄进稿器上 A4 只能短边横跨",
			paper: "A4", source: escl.SourceADF,
			caps:  &escl.Capabilities{HasAdf: true, MaxWidth: 2550, MaxHeight: 4200, AdfMaxWidth: 2550, AdfMaxHeight: 4200},
			wantW: 2550, wantH: 3508,
		},
		{
			// 设备幅面上限比纸张还小：必须收敛到设备能力内，否则可能被拒单。
			name:  "区域收敛到设备上限",
			paper: "A3", source: escl.SourcePlaten,
			caps:  &escl.Capabilities{HasPlaten: true, MaxWidth: 2480, MaxHeight: 3508},
			wantW: 2480, wantH: 3508,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, h := scanRegion(c.paper, c.source, c.caps)
			if w != c.wantW || h != c.wantH {
				t.Fatalf("区域 = %d×%d, want %d×%d", w, h, c.wantW, c.wantH)
			}
		})
	}
}

// 纸张认识时，区域的长宽比必须和纸张一致 —— 页面与纸张同形是打印不旋转、
// 不缩放的前提，这条比具体数值更要紧。允许几个单位的误差：设备上报的上限
// 取整过（297mm 报成 3507），收敛后差个零点几毫米无所谓。
func TestScanRegionAspectMatchesPaper(t *testing.T) {
	for _, paper := range []string{"A3", "A4", "A5"} {
		wantLong, wantShort := mm300(paperSizeMM[paper][1]), mm300(paperSizeMM[paper][0])
		if wantLong > realCaps.AdfMaxWidth+regionTolerance {
			continue // 长边横跨不了（A3：短边横跨、长边走纸），规则不同，另有用例覆盖
		}
		w, h := scanRegion(paper, escl.SourceADF, realCaps)
		if abs(w-wantLong) > regionTolerance || abs(h-wantShort) > regionTolerance {
			t.Errorf("%s: 区域 %d×%d，应约等于纸张长边×短边 %d×%d", paper, w, h, wantLong, wantShort)
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
