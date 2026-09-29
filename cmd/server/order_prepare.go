package main

// 订单专用的「上传 → 转换 → 数页 → 后处理 → 持久化」流水线。
//
// ⚠️ 这段逻辑与 print_handlers.go 的流水线是【孪生】的：两者都做
//    「落盘 → 按类型转换 → 数页数 → 页数上限 → 水印 → 自定义缩放 → even-reverse 重排」，
//    唯一区别是最后一步 —— printHandler 直接送 IPP，这里则把成品落到
//    uploads 下的稳定位置，等支付完成后由 startPrintForOrder 再送印。
//
// 之所以是复制而不是抽公共函数：printHandler 那条路径目前没有自动化覆盖
// （5 种文件类型 × 水印 × 缩放 × 重排的组合），在没有测试网的情况下重构
// 四百行安全攸关的代码属于纯粹的回归风险。等给管线补上表驱动测试之后，
// 应当把两者合并成同一个函数。
//
// 🚫 改动这里时【必须】同步检查 print_handlers.go / print_records_handlers.go /
//    scheduler.go 里的同类分支 —— 这四处目前是四胞胎。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"cups-web/internal/pricing"
)

// clientError 是「应当以 4xx/5xx 原样返回给用户」的错误。
//
// 流水线有好几种失败（转换失败 400、页数超限 422、落盘失败 500），
// 每层都往上透传一个状态码会让签名变得很难看。用这个类型把
// 「给用户的文案 + HTTP 状态」打包带出去。
type clientError struct {
	Status int
	Msg    string
}

func (e *clientError) Error() string { return e.Msg }

func badRequestErr(msg string) error { return &clientError{Status: http.StatusBadRequest, Msg: msg} }

func unprocessableErr(msg string) error {
	return &clientError{Status: http.StatusUnprocessableEntity, Msg: msg}
}

// writeClientError 把 clientError 按其自带状态码写出；其他错误一律 500。
func writeClientError(w http.ResponseWriter, err error) {
	var ce *clientError
	if errors.As(err, &ce) {
		writeJSONError(w, ce.Status, ce.Msg)
		return
	}
	log.Printf("[order] 未预期的错误: %v", err)
	writeJSONError(w, http.StatusInternalServerError, "internal error")
}

// orderForm 是下单表单解析结果，字段语义与 /api/print 完全一致。
type orderForm struct {
	Printer string

	Invert   bool
	IsColor  bool
	IsDuplex bool
	Mirror   bool

	Copies   int
	NumberUp int

	Orientation    string
	PaperSize      string
	PaperType      string
	MediaSource    string
	PrintScaling   string // 原始值（含 custom 百分比）
	PageRange      string // 【已归一化】的规范形式
	PageSet        string // 原始值
	Watermark      string
	NumberUpLayout string
	PageBorder     string
}

// parseOrderForm 解析 multipart 表单。调用方需先做 applyUploadLimit 与 ParseMultipartForm。
//
// 与 printHandler 的表单读取保持一致；🚫 默认值/空值不下发的规则也一样
// （见 AGENTS.md 的 /api/print 字段表）。
func parseOrderForm(r *http.Request) (orderForm, error) {
	var form orderForm

	form.Printer = r.FormValue("printer")
	if form.Printer == "" {
		return form, badRequestErr("missing printer field")
	}

	form.IsDuplex = r.FormValue("duplex") == "true"
	form.IsColor = r.FormValue("color") == "true"
	form.Invert = r.FormValue("invert") == "true"
	form.Mirror = r.FormValue("mirror") == "true"

	form.Copies = 1
	if n, err := strconv.Atoi(r.FormValue("copies")); err == nil && n > 0 {
		form.Copies = n
	}

	// N-up：只接受管线支持的档位，非法值退回 1（不发送该属性）。
	form.NumberUp = 1
	if n, err := strconv.Atoi(r.FormValue("number_up")); err == nil {
		switch n {
		case 1, 2, 4, 6, 9, 16:
			form.NumberUp = n
		}
	}

	form.Orientation = r.FormValue("orientation")
	form.PaperSize = r.FormValue("paper_size")
	form.PaperType = r.FormValue("paper_type")
	form.MediaSource = r.FormValue("media_source")
	form.PageSet = r.FormValue("page_set")
	form.NumberUpLayout = r.FormValue("number_up_layout")
	form.PageBorder = r.FormValue("page_border")
	form.Watermark = strings.TrimSpace(r.FormValue("watermark_text"))

	// 页码必须在这里就校验并归一化 —— 非法的 range 会被后续 IPP 层拒绝，
	// 但那时文件已经转换完（可能几十秒），用户白等一场才看到报错。
	normalized, err := pricing.NormalizePageRange(r.FormValue("page_range"))
	if err != nil {
		return form, unprocessableErr(msgInvalidPageRange)
	}
	form.PageRange = normalized

	form.PrintScaling = r.FormValue("print_scaling")

	return form, nil
}

// orderPrepared 是流水线的产物。
type orderPrepared struct {
	StoredRel string
	StoredAbs string
	PrintRel  string // 待打印文件的稳定相对路径
	PrintAbs  string
	PrintMime string

	// DocPages 是【重排前】的文档总页数 —— 计费必须用它。
	DocPages int
	// PrintPages 是重排后的页数，仅用于订单展示。
	// even-reverse 会补一张空白页，所以两者可能差 1。
	PrintPages int

	// 下面三个是链路改写过的【有效值】，订单要存它们，重放时不再转换。
	EffectiveScaling string
	EffectivePageSet string
	EffectiveDuplex  bool
}

// prepareOrderPrint 执行完整流水线，产物已落到 uploads 下的稳定位置。
// 失败时自行清理已落盘的文件，调用方不需要再管。
func prepareOrderPrint(ctx context.Context, form orderForm, file io.Reader, filename string, maxPages int64) (orderPrepared, error) {
	var prepared orderPrepared

	storedRel, storedAbs, err := saveUploadedFile(file, filename, uploadDir)
	if err != nil {
		return prepared, fmt.Errorf("保存上传文件失败: %w", err)
	}
	prepared.StoredRel = storedRel
	prepared.StoredAbs = storedAbs

	// 失败路径统一删掉原文件，避免 uploads 里堆孤儿。
	fail := func(err error) (orderPrepared, error) {
		_ = os.Remove(storedAbs)
		return orderPrepared{}, err
	}

	// 所有中间临时文件都在本函数返回前清理：成品已经由 persistPrintArtifact
	// 落到稳定位置，临时文件没有跨请求存在的必要。
	var cleanups []func()
	defer func() {
		for _, fn := range cleanups {
			fn()
		}
	}()

	countCtx, cancel := convertTimeoutContext(ctx)
	defer cancel()

	printPath := storedAbs
	printMime := ""
	var pages int
	kind := detectFileKind(storedAbs, filename)

	switch kind {
	case fileKindPDF:
		// 与 printHandler 一致：默认不对上传 PDF 走 gs 规范化，直接打印原始字节。
		var cerr error
		pages, cerr = countPDFPages(storedAbs)
		if cerr != nil {
			log.Printf("[order] countPDFPages failed: %v", cerr)
			pages = 1
			printMime = "application/octet-stream"
		} else {
			printMime = "application/pdf"
		}
		printPath = storedAbs

	case fileKindOffice, fileKindOFD, fileKindImage, fileKindText:
		outPath, cleanup, convertedPages, err := convertForOrder(countCtx, kind, storedAbs, form)
		if cleanup != nil {
			cleanups = append(cleanups, cleanup)
		}
		if err != nil {
			return fail(err)
		}
		pages = convertedPages
		// 转换产物先落一份到 uploads（与 printHandler 相同的位置），
		// 后面若被水印/缩放/重排改写，persistPrintArtifact 会覆盖它。
		_, convertedAbs, err := saveConvertedPDFToUploads(outPath, storedRel, uploadDir)
		if err != nil {
			return fail(fmt.Errorf("保存转换产物失败: %w", err))
		}
		printPath = convertedAbs
		printMime = "application/pdf"

	default:
		p, _, err := countPages(countCtx, storedAbs, filename)
		if err != nil {
			return fail(badRequestErr("failed to read pages"))
		}
		pages = p
	}

	if pages < 1 {
		pages = 1
	}
	// 记下重排前的页数：even-reverse 之后 pages 会被改写，而计费必须用原始值。
	prepared.DocPages = pages

	// 页数硬上限（与 /api/print 一致，422 表示「内容合法但被业务规则拒绝」）。
	if maxPages > 0 && int64(pages) > maxPages {
		return fail(unprocessableErr("文件页数超出管理员设置的上限"))
	}

	if form.Watermark != "" && printMime == "application/pdf" {
		wmPath, wmCleanup, wmErr := applyWatermarkToPDF(printPath, form.Watermark)
		if wmErr != nil {
			log.Printf("[order] watermark failed: %v", wmErr)
		} else {
			cleanups = append(cleanups, wmCleanup)
			printPath = wmPath
		}
	}

	// 自定义百分比缩放：先用 gs 预缩放内容，再把 print-scaling 换成合法 keyword。
	scaledPath, scaleCleanup, effectiveScaling := resolveCustomScaling(printPath, form.PrintScaling, printMime, "order")
	printPath = scaledPath
	if scaleCleanup != nil {
		cleanups = append(cleanups, scaleCleanup)
	}

	effectivePageSet := form.PageSet
	if form.PageSet == "even-reverse" && printMime == "application/pdf" && pages > 1 {
		reorderedPath, reorderCleanup, rerr := reorderPDFForManualDuplex(printPath, pages, form.PaperSize)
		if rerr != nil {
			log.Printf("[order] even-reverse reorder failed: %v, falling back to normal even", rerr)
			effectivePageSet = "even"
		} else {
			cleanups = append(cleanups, reorderCleanup)
			printPath = reorderedPath
			if reorderedPages, _ := countPDFPages(reorderedPath); reorderedPages > 0 {
				pages = reorderedPages
			}
			effectivePageSet = ""
		}
	}

	// 手动双面（奇/偶 或 偶数倒序）语义上必然单面输出（issue #109）：
	// 用户没在前端切回单面时在这里强制回落，避免 pdftopdf 用空白页填补。
	effectiveDuplex := form.IsDuplex
	if form.PageSet == "odd" || form.PageSet == "even" || form.PageSet == "even-reverse" {
		effectiveDuplex = false
	}

	// 把成品落到稳定位置（订单要跨 HTTP 请求持有它）。
	printRel, printAbs, err := persistPrintArtifact(printPath, storedRel, storedAbs)
	if err != nil {
		return fail(fmt.Errorf("持久化待打印文件失败: %w", err))
	}

	prepared.PrintRel = printRel
	prepared.PrintAbs = printAbs
	prepared.PrintMime = printMime
	prepared.PrintPages = pages
	prepared.EffectiveScaling = effectiveScaling
	prepared.EffectivePageSet = effectivePageSet
	prepared.EffectiveDuplex = effectiveDuplex
	return prepared, nil
}

// convertForOrder 把非 PDF 文件转成 PDF，返回 (临时产物路径, 清理函数, 页数, error)。
//
// 四个分支与 printHandler 的同名分支逐一对应，只是把「写响应并返回」
// 换成了「返回错误」，好让上层统一处置。
func convertForOrder(ctx context.Context, kind fileKind, storedAbs string, form orderForm) (string, func(), int, error) {
	switch kind {
	case fileKindOffice:
		outPath, cleanup, err := convertOfficeToPDF(ctx, storedAbs)
		if err != nil {
			return "", nil, 0, badRequestErr(convertErrMsg(err))
		}
		pages, err := countPDFPages(outPath)
		if err != nil {
			cleanup()
			return "", nil, 0, badRequestErr("failed to read pages")
		}
		return outPath, cleanup, pages, nil

	case fileKindOFD:
		outPath, cleanup, err := convertOFDToPDF(ctx, storedAbs)
		if err != nil {
			return "", nil, 0, badRequestErr(convertErrMsg(err))
		}
		pages, err := countPDFPages(outPath)
		if err != nil {
			cleanup()
			return "", nil, 0, badRequestErr("failed to read pages")
		}
		return outPath, cleanup, pages, nil

	case fileKindImage:
		outPath, cleanup, err := convertImageToPDF(storedAbs, form.Orientation, form.PaperSize, form.Invert)
		if err != nil {
			return "", nil, 0, badRequestErr("conversion failed")
		}
		// 图片转出来的 PDF 恒为一页（与 printHandler 的 pages = 1 一致）。
		return outPath, cleanup, 1, nil

	case fileKindText:
		pages, err := estimateTextPages(storedAbs)
		if err != nil {
			return "", nil, 0, badRequestErr("failed to read pages")
		}
		outPath, cleanup, err := convertTextToPDF(storedAbs, form.Orientation, form.PaperSize)
		if err != nil {
			return "", nil, 0, badRequestErr("conversion failed")
		}
		return outPath, cleanup, pages, nil
	}
	return "", nil, 0, fmt.Errorf("convertForOrder: 不支持的类型 %v", kind)
}

// persistPrintArtifact 把最终待打印文件落到 uploads 下的稳定位置。
//
// 现状分叉：PDF 分支的 printPath 就是原上传文件（从不生成 .print.pdf），
// 而水印 / 自定义缩放 / even-reverse 会把 printPath 换成临时文件（函数返回即删）。
// 订单要跨 HTTP 请求持有文件，必须落到稳定位置。
//
//	finalAbs == storedAbs  → 原文件本身就是待打印文件，不复制、不占双份空间
//	否则                   → <storedRel>.print.pdf
//
// 选 convertedRelPath（= <storedRel>.print.pdf）而不是另建目录，是因为
// maintenance.go 清理时删的正是 storedRel 与 convertedRelPath(storedRel) 这两个路径，
// 订单的保护逻辑因此可以精确到文件（见 protectedRels）。
func persistPrintArtifact(finalAbs, storedRel, storedAbs string) (string, string, error) {
	if finalAbs == storedAbs {
		return storedRel, storedAbs, nil
	}

	targetRel := convertedRelPath(storedRel)
	targetAbs := filepath.Join(uploadDir, filepath.FromSlash(targetRel))
	if finalAbs == targetAbs {
		// 转换链已经写在目标位置（Office/OFD/图片/文本，且没经过水印/缩放/重排）。
		return targetRel, targetAbs, nil
	}

	// 水印 / 自定义缩放 / even-reverse 把产物换成了临时文件，复制到稳定位置。
	// 复用 saveConvertedPDFToUploads：它按 storedRel 推导目标、建父目录、
	// 失败时回滚已写出的半截文件。
	return saveConvertedPDFToUploads(finalAbs, storedRel, uploadDir)
}
