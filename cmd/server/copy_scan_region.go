package main

// 扫描区域的选择：让扫描件页面与所选纸张同形。
//
// 早期实现一律用设备上报的最大幅面做区域，看着「最保险（不会裁掉东西）」，
// 实际代价很大：设备照单全收地把区域做成 PDF 页面，一张 A4 原件于是被采成
// 297×432mm 的 **A3 页面**。这份 A3 页面交给 CUPS 打 A4 时，pdftopdf 为了
// 「最适应纸张」会把它转 90° 再缩到 0.687 倍 —— 用户拿到一张横过来、还变
// 小了一圈的复印件。
//
// 页面与纸张同形，打印才能 1:1、才不需要旋转。区域因此要按【扫描几何】下
// 原件的大小来给，而两种通道的几何不一样：
//
//   - 玻璃板：平铺，原件放左上角，区域 = 纸张纵向尺寸（宽 × 高）。
//   - 进稿器：纸是走过去的。横跨走纸方向的那条边受进稿器宽度限制，例如
//     本机（RICOH Pro C5200S，A3 幅面进稿器）装 A4 时是「长边横跨、短边
//     走纸」：实测扫描件里 A4 内容的足迹正好是 297mm（横向占满）×210mm
//     （纵向，从 Y=0 起），机器侧的原稿标尺也把 A4 标在 8½ 英寸（210mm）
//     处，两处证据互相印证。
//
// 一般规则：能横跨就走长边横跨（即宽 = 长边、高 = 短边）；长边放不下
// （A3 这类比进稿器还宽的原件）才让短边横跨、长边走纸。

import (
	"math"
	"strings"

	"cups-web/internal/escl"
)

// paperSizeMM 是支持的纸张尺寸（毫米，纵向：宽 × 高）。
var paperSizeMM = map[string][2]int{
	"A3":     {297, 420},
	"A4":     {210, 297},
	"A5":     {148, 210},
	"LETTER": {216, 279},
	"LEGAL":  {216, 356},
}

const mmTo300 = 300.0 / 25.4 // 1/300 英寸 与 毫米的换算

// mm300 把毫米换成 1/300 英寸（eSCL 区域的单位）。
func mm300(mm int) int { return int(math.Round(float64(mm) * mmTo300)) }

// regionTolerance 是尺寸比较的容差（1/300 英寸，约 0.85mm）。设备上报的
// 上限是取整过的，297mm 会被报成 3507 而不是 3508，不留容差会把「长边正好
// 横跨」误判成「长边放不下」。
const regionTolerance = 10

// scanRegion 按纸张与放稿方式算出扫描区域（1/300 英寸）。
//
// 纸张不认识时退回设备最大幅面：宁可多采一圈白边（打印端会缩放到纸张），
// 也不要凭猜测把原件内容裁掉。
func scanRegion(paperSize, source string, caps *escl.Capabilities) (int, int) {
	if caps == nil {
		return 0, 0
	}
	dims, ok := paperSizeMM[strings.ToUpper(strings.TrimSpace(paperSize))]
	if !ok {
		return caps.MaxWidth, caps.MaxHeight
	}
	short, long := mm300(dims[0]), mm300(dims[1])

	var w, h int
	if source == escl.SourceADF {
		switch {
		case caps.AdfMaxWidth <= 0 || long <= caps.AdfMaxWidth+regionTolerance:
			// 长边能横跨走纸方向（A4/A5/Letter 的常见装法）。
			w, h = long, short
		default:
			// 长边比进稿器还宽：只能短边横跨，长边走纸。走纸边的上限由
			// AdfMaxHeight 兜底，不再按纸张猜。
			w, h = caps.AdfMaxWidth, long
		}
		if caps.AdfMaxHeight > 0 && h > caps.AdfMaxHeight {
			h = caps.AdfMaxHeight
		}
	} else {
		w, h = short, long
	}

	// 再按设备上限兜一次底：区域超出能力时设备可能直接拒单。
	if caps.MaxWidth > 0 && w > caps.MaxWidth {
		w = caps.MaxWidth
	}
	if caps.MaxHeight > 0 && h > caps.MaxHeight {
		h = caps.MaxHeight
	}
	return w, h
}
