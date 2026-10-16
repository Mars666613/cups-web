package main

// 扫描件的「空白判定」：没放原件时，设备不会报错。
//
// 为什么需要它（RICOH Pro C5200S 真机实测，2026-10-14）：
//   - 进稿器里没纸时下发 ADF 任务，设备照样回 HTTP 201 建任务，随后交回
//     一张【1 页、A4 横向、内容全白】的 PDF —— 既不是 Aborted，也不是空
//     响应。于是「扫描成功 → 落单 / 送印」整条链路照常走完：用户付了钱、
//     机器吐一张白纸（支付模式下先付款后出纸，更冤）。
//   - 玻璃板没有任何纸传感器，空玻璃板扫出来同样是一张白纸。
//
// 所以「到底有没有原件」这件事，设备只在【放稿方式 = 进稿器】且【它愿意
// 上报 AdfState】时才可能提前知道（见 adfPreflight），其余情况只能看扫描
// 结果 —— 本文件就是那个看结果的判据。
//
// ── 判据为什么不是「墨点占比」 ──────────────────────────────────────
// 直觉上「暗像素占比极低就是空白」最直接，实测却不可用：空进稿器扫出的
// 空白页（3508×2480）暗像素(<200) 占 0.34%，而真实原件的整页占比是
// 0.84%-1.74% —— 只差 2-5 倍，其中 0.34% 里大半还是扫描头底噪与 JPEG
// 块效应。一张「只有一小枚图章」的原件，墨点占比约 0.2%，会被误判成空白；
// 而把阈值调高到能区分噪声，就会开始吞掉真件。
//
// 有效的度量是【分块均值的覆盖率】：
//   - 页面按 32×32 像素（300dpi 约 2.7mm）切块，块内隔点取样求灰度均值；
//   - 均值 < 240 记为一个「有内容」块；
//   - 整页「有内容」块数 < blankMinContentBlocks 才算这一页是空白。
//
// 散粒噪声在 32×32 的块里被平均掉，成片的文字 / 图章 / 线条才压得动块均值。
// 同一批样本上的实测值：
//
//	空进稿器（无原件）:   1 /  7416 块
//	真实 A4 原件      : 601 /  7416 块
//	真实 A3 幅面原件  : 596 / 15347 块
//
// 量级差足够大，阈值取 3 有大量余量，不需要精调，也不依赖单台设备的噪声水平。
//
// ⚠️ 边缘 3% 必须裁掉：实测空白页图像的第一行有一条 1-2 像素的深色线
// （走纸通道的边界），它横贯整页，会一次点亮上百个块，把空白页说成
// 「有内容」。裁掉 3%（A4 短边约 6mm）只损失最外圈信息，而「只有最外圈
// 6mm 有内容」的原件本来也等同于空白。
//
// ── 判定失败的方向是「放行」 ────────────────────────────────────────
// 解不开的编码（CCITT / JBIG2 / JPX）、pdfcpu 解析报错、超时 —— 一律返回
// known=false，当作「不知道」，继续走原流程。这道判据是【保护用户的护栏】
// （别让他为一张白纸付钱），不是【收钱的闸门】：拦错的代价是用户明明放好
// 了原件却被拒绝，比偶尔漏掉一次空白件严重得多。用户也永远有一条明确的
// 出口（copyRequest.ForceBlank）。

import (
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

const (
	// blankBlockPx 是判定的块边长（像素）。32px 在 300dpi 下约 2.7mm：
	// 比一个汉字的字身略大，散粒噪声平得掉，正文/图章一定压得动。
	blankBlockPx = 32
	// blankBlockMeanMax 是「这一块有内容」的灰度均值上限（0-255）。
	blankBlockMeanMax = 240
	// blankBlockSampleStep 是块内取样的步长。覆盖率是统计量，隔点取样
	// 既够准又能把整图遍历的成本降到 1/4（3508×2480 约 30ms）。
	blankBlockSampleStep = 2
	// blankEdgeIgnorePct 是裁掉的边缘比例（见上文说明）。
	blankEdgeIgnorePct = 3
	// blankMinContentBlocks 是「这一页不是空白」所需的块数。
	blankMinContentBlocks = 3
	// blankCheckMaxPages 是最多看几页。复印的判据是「整份都空白」，
	// 只要发现一页有内容就立刻收工，所以正常文件在第 1 页就结束了；
	// 这个上限只影响「整份都空白」这种极端情况下的耗时。
	blankCheckMaxPages = 12
	// blankCheckTimeout 是整次判定的墙钟上限。pdfcpu 不接受 context，
	// 只能用看门狗兜住请求时长 —— 判定超时按「不知道」放行。
	blankCheckTimeout = 10 * time.Second
)

// errContentFound 是回调里的提前收工信号（找到有内容的页就不必再看后面）。
var errContentFound = errors.New("pdf: content found")

// pageContentBlocks 返回一页里「有内容」的块数。
//
// 只统计裁掉边缘后的区域，理由见文件头。
func pageContentBlocks(img image.Image) int {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return 0
	}
	ix := w * blankEdgeIgnorePct / 100
	iy := h * blankEdgeIgnorePct / 100
	x0, x1 := b.Min.X+ix, b.Max.X-ix
	y0, y1 := b.Min.Y+iy, b.Max.Y-iy

	blocks := 0
	for by := y0; by+blankBlockPx <= y1; by += blankBlockPx {
		for bx := x0; bx+blankBlockPx <= x1; bx += blankBlockPx {
			sum, n := 0, 0
			for y := by; y < by+blankBlockPx; y += blankBlockSampleStep {
				for x := bx; x < bx+blankBlockPx; x += blankBlockSampleStep {
					r, g, bl, _ := img.At(x, y).RGBA()
					// ITU-R BT.601 亮度：扫描件是灰阶或彩色，统一折成亮度比。
					sum += (299*int(r>>8) + 587*int(g>>8) + 114*int(bl>>8)) / 1000
					n++
				}
			}
			if n > 0 && sum/n < blankBlockMeanMax {
				blocks++
			}
		}
	}
	return blocks
}

// pdfLooksBlank 判断一份扫描件是不是「每一页都空白」。
//
//	blank 为真 且 known 为真 → 整份都空白（可以据此拦下这一单）
//	known 为假               → 判不了（不是扫描件、编码不认识、解析失败、超时），
//	                           调用方应当继续原流程
//
// rs 由本次调用独占，函数会把它交给后台 goroutine 处理。
func pdfLooksBlank(rs io.ReadSeeker) (blank, known bool) {
	type verdict struct{ blank, known bool }
	done := make(chan verdict, 1)
	go func() {
		b, k := inspectBlank(rs)
		done <- verdict{b, k}
	}()
	select {
	case v := <-done:
		return v.blank, v.known
	case <-time.After(blankCheckTimeout):
		log.Printf("[copy] 空白判定超时（%s），按「不知道」放行", blankCheckTimeout)
		return false, false
	}
}

// inspectBlank 是 pdfLooksBlank 的同步实现。
func inspectBlank(rs io.ReadSeeker) (blank, known bool) {
	// pdfcpu 内部对畸形 PDF 会 panic 转 error，但图片解码路径不在它的
	// 保护范围内，这里再兜一层：判不了就放行，绝不让复印流程挂在这里。
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[copy] 空白判定异常（按「不知道」放行）: %v", r)
			blank, known = false, false
		}
	}()

	sawImage, foundContent := false, false
	// 只取前 blankCheckMaxPages 页：pdfcpu 的页选择会把超出总页数的上界
	// 自行夹到实际页数（1-12 作用在单页文件上同样成立）。
	sel := fmt.Sprintf("1-%d", blankCheckMaxPages)
	err := api.ExtractImages(rs, []string{sel}, func(im model.Image, _ bool, _ int) error {
		// 图像蒙版与缩略图不是页面内容，混进来会把噪声当墨迹。
		if im.IsImgMask || im.Thumb {
			return nil
		}
		img, _, derr := image.Decode(im)
		if derr != nil {
			// 不认识的编码（CCITT/JBIG2/JPX…）：本页跳过。
			// 最终能不能判，由 sawImage 决定。
			return nil
		}
		sawImage = true
		if pageContentBlocks(img) >= blankMinContentBlocks {
			foundContent = true
			return errContentFound
		}
		return nil
	}, nil)

	if foundContent {
		return false, true
	}
	if err != nil {
		log.Printf("[copy] 空白判定未能完成（按「不知道」放行）: %v", err)
		return false, false
	}
	if !sawImage {
		// 一页可解码的位图都没有：这不是扫描件（或整份都是矢量内容），
		// 本判据对它不成立，不做判断。
		return false, false
	}
	return true, true
}
