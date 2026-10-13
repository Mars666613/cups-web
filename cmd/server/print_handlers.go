package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cups-web/internal/auth"
	"cups-web/internal/ipp"
	"cups-web/internal/pricing"
	"cups-web/internal/store"
)

// msgInvalidPageRange 是页码写法非法时返回给用户的提示。
// 用 422（内容合法、但被业务规则拒绝）而不是 400，与页数超限的处理保持一致。
const msgInvalidPageRange = "页面范围写法无效，请检查（示例：1-5 8 10-12）"

type printResp struct {
	JobID    string `json:"jobId,omitempty"`
	OK       bool   `json:"ok"`
	Pages    int    `json:"pages"`
	IsDuplex bool   `json:"isDuplex"`
	IsColor  bool   `json:"isColor"`
	Copies   int    `json:"copies"`
}

// printHandler 是【未计费】部署的打印入口。
//
// 计费开启时这条路径整体关闭 —— 订单是唯一的打印通道。
func printHandler(w http.ResponseWriter, r *http.Request) {
	// 计费闸门。前端的打印按钮在计费开启时已经改走 /api/orders，
	// 这道判断是防【直连】的兜底：少了它，任何人对 /api/print 发一次 curl
	// 就能完全绕过下单与扣款，前端做得再对也没用。
	//
	// 用 403 而不是 400/422（那两个是「这个请求本身有问题」）：
	// 请求完全合法，只是这个入口在当前配置下不开放，换个入口就能成。
	if billingEnabled(r.Context()) {
		writeJSONError(w, http.StatusForbidden, "计费已开启，请通过打印订单提交")
		return
	}

	// 刷卡取件模式下这个入口同样关闭 —— 任务必须先暂存、刷卡才出纸。
	// 少了这道判断，一次 curl 就能把文件直接打出来，完全绕开取件流程：
	// 纸在员工不在场的时候出来了，而「刷卡才出纸」正是这个模式存在的理由。
	// （前端此时本来就走 /api/pull-orders，这道判断是防直连的兜底。）
	if pullModeEnabled(r.Context()) {
		writeJSONError(w, http.StatusForbidden, "刷卡取件已开启，请通过打印订单提交")
		return
	}

	maxPages, _ := getPrintLimits(r.Context())
	applyUploadLimit(w, r)
	// Expect multipart form
	if err := r.ParseMultipartForm(512 << 20); err != nil {
		if isMaxBytesError(err) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "文件超出管理员设置的大小上限")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "invalid multipart form")
		return
	}
	file, fh, err := r.FormFile("file")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "missing file field")
		return
	}
	defer file.Close()

	printer := r.FormValue("printer")
	if printer == "" {
		writeJSONError(w, http.StatusBadRequest, "missing printer field")
		return
	}

	isDuplex := r.FormValue("duplex") == "true"
	isColor := r.FormValue("color") == "true"
	// 黑白反转（issue #87）：仅图片转 PDF 生效，不落库（reprint 不保留）
	invert := r.FormValue("invert") == "true"

	// Extended print options
	copiesStr := r.FormValue("copies")
	copies := 1
	if copiesStr != "" {
		if n, err := strconv.Atoi(copiesStr); err == nil && n > 0 {
			copies = n
		}
	}
	orientation := r.FormValue("orientation")
	paperSize := r.FormValue("paper_size")
	paperType := r.FormValue("paper_type")
	printScaling := r.FormValue("print_scaling")
	mediaSource := r.FormValue("media_source")
	pageRange := r.FormValue("page_range")
	pageSet := r.FormValue("page_set")
	// even-reverse / custom-scale 分支下方会改写 pageSet/printScaling，落库要保留用户的原始选择。
	origPageSet := pageSet
	origPrintScaling := printScaling
	mirror := r.FormValue("mirror") == "true"
	watermarkText := strings.TrimSpace(r.FormValue("watermark_text"))

	// N-up (multiple pages per sheet). Default numberUp=1 means off; invalid or
	// unsupported values fall back to 1 (Issue #78).
	numberUp := 1
	if n, err := strconv.Atoi(r.FormValue("number_up")); err == nil {
		switch n {
		case 1, 2, 4, 6, 9, 16:
			numberUp = n
		}
	}
	numberUpLayout := r.FormValue("number_up_layout")
	pageBorder := r.FormValue("page_border")

	// 页码范围必须在这里校验并归一化，不能只靠 IPP 层兜底：
	//  1. 时机 —— 下面这段转换可能要几十秒，等转换完才报错是白等一场；
	//  2. 正确性 —— 非法写法（"5-3"、"0-5"）以前会被 IPP 的宽松解析器静默丢弃，
	//     于是 page-ranges 属性压根不下发，CUPS 打印【整个文档】；
	//  3. 一致性 —— 归一化后的串同时用于落库与下发，库里存的永远是规范形式，
	//     重打预填时不会把 "1-3 2-5" 这种冗余写法再搬出来。
	pageRange, err = pricing.NormalizePageRange(pageRange)
	if err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, msgInvalidPageRange)
		return
	}

	var saveHistory bool
	if err := appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
		v, err := store.GetSettingInt(r.Context(), tx, store.SettingSaveHistory, 1)
		if err != nil {
			return err
		}
		saveHistory = v != 0
		return nil
	}); err != nil {
		saveHistory = true
	}

	storedRel, storedAbs, err := saveUploadedFile(file, fh.Filename, uploadDir)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, saveErrMsg(err))
		return
	}

	countCtx, cancel := convertTimeoutContext(r.Context())
	defer cancel()
	printPath := storedAbs
	var printCleanup func()
	printMime := ""
	var pages int
	kind := detectFileKind(storedAbs, fh.Filename)
	switch kind {
	case fileKindPDF:
		// 默认不再对上传 PDF 走 gs 规范化，直接打印原始字节。
		// 如客户端有需要（例如 CJK 字体乱码），可先调用 /api/convert?normalize=true
		// 拿到规范化后的字节再回传到本接口。
		var cerr error
		pages, cerr = countPDFPages(storedAbs)
		if cerr != nil {
			log.Printf("[print] countPDFPages failed: %v", cerr)
			pages = 1
		}
		printPath = storedAbs
		printMime = "application/pdf"
		// 解析失败时降级 MIME，让 CUPS/IPP 自行识别
		if cerr != nil {
			printMime = "application/octet-stream"
		}
	case fileKindOffice:
		outPath, cleanup, err := convertOfficeToPDF(countCtx, storedAbs)
		if err != nil {
			_ = os.Remove(storedAbs)
			writeJSONError(w, http.StatusBadRequest, convertErrMsg(err))
			return
		}
		pages, err = countPDFPages(outPath)
		if err != nil {
			cleanup()
			_ = os.Remove(storedAbs)
			writeJSONError(w, http.StatusBadRequest, countPagesErrMsg(err))
			return
		}
		_, convertedAbs, err := saveConvertedPDFToUploads(outPath, storedRel, uploadDir)
		if err != nil {
			cleanup()
			_ = os.Remove(storedAbs)
			writeJSONError(w, http.StatusInternalServerError, saveErrMsg(err))
			return
		}
		printPath = convertedAbs
		printCleanup = cleanup
		printMime = "application/pdf"
	case fileKindOFD:
		outPath, cleanup, err := convertOFDToPDF(countCtx, storedAbs)
		if err != nil {
			_ = os.Remove(storedAbs)
			writeJSONError(w, http.StatusBadRequest, convertErrMsg(err))
			return
		}
		pages, err = countPDFPages(outPath)
		if err != nil {
			cleanup()
			_ = os.Remove(storedAbs)
			writeJSONError(w, http.StatusBadRequest, countPagesErrMsg(err))
			return
		}
		_, convertedAbs, err := saveConvertedPDFToUploads(outPath, storedRel, uploadDir)
		if err != nil {
			cleanup()
			_ = os.Remove(storedAbs)
			writeJSONError(w, http.StatusInternalServerError, saveErrMsg(err))
			return
		}
		printPath = convertedAbs
		printCleanup = cleanup
		printMime = "application/pdf"
	case fileKindImage:
		outPath, cleanup, err := convertImageToPDF(storedAbs, orientation, paperSize, invert)
		if err != nil {
			_ = os.Remove(storedAbs)
			writeJSONError(w, http.StatusBadRequest, convertErrMsg(err))
			return
		}
		_, convertedAbs, err := saveConvertedPDFToUploads(outPath, storedRel, uploadDir)
		if err != nil {
			cleanup()
			_ = os.Remove(storedAbs)
			writeJSONError(w, http.StatusInternalServerError, saveErrMsg(err))
			return
		}
		printPath = convertedAbs
		printCleanup = cleanup
		printMime = "application/pdf"
		pages = 1
	case fileKindText:
		var err error
		pages, err = estimateTextPages(storedAbs)
		if err != nil {
			_ = os.Remove(storedAbs)
			writeJSONError(w, http.StatusBadRequest, countPagesErrMsg(err))
			return
		}
		outPath, cleanup, err := convertTextToPDF(storedAbs, orientation, paperSize)
		if err != nil {
			_ = os.Remove(storedAbs)
			writeJSONError(w, http.StatusBadRequest, convertErrMsg(err))
			return
		}
		_, convertedAbs, err := saveConvertedPDFToUploads(outPath, storedRel, uploadDir)
		if err != nil {
			cleanup()
			_ = os.Remove(storedAbs)
			writeJSONError(w, http.StatusInternalServerError, saveErrMsg(err))
			return
		}
		printPath = convertedAbs
		printCleanup = cleanup
		printMime = "application/pdf"
	default:
		var err error
		pages, _, err = countPages(countCtx, storedAbs, fh.Filename)
		if err != nil {
			_ = os.Remove(storedAbs)
			writeJSONError(w, http.StatusBadRequest, countPagesErrMsg(err))
			return
		}
	}
	if pages < 1 {
		pages = 1
	}
	if printCleanup != nil {
		defer printCleanup()
	}

	// 页数硬上限（Issue #72）：0=不限；超过则拒绝，422 表示"内容合法但被业务规则拒绝"。
	if maxPages > 0 && int64(pages) > maxPages {
		_ = os.Remove(storedAbs)
		writeJSONError(w, http.StatusUnprocessableEntity,
			"文件页数超出管理员设置的上限")
		return
	}

	if watermarkText != "" && printMime == "application/pdf" {
		wmPath, wmCleanup, wmErr := applyWatermarkToPDF(printPath, watermarkText)
		if wmErr != nil {
			log.Printf("[print] watermark failed: %v", wmErr)
		} else {
			defer wmCleanup()
			printPath = wmPath
		}
	}

	// 自定义百分比缩放：先用 gs 把 PDF 内容预缩放，再把 print-scaling 换成合法 keyword
	scaledPath, scaleCleanup, effectiveScaling := resolveCustomScaling(printPath, printScaling, printMime, "print")
	printPath = scaledPath
	printScaling = effectiveScaling
	if scaleCleanup != nil {
		defer scaleCleanup()
	}

	// Handle even-reverse: reorder PDF pages for manual duplex (even pages
	// in reverse order, with a blank page prepended when total is odd).
	if pageSet == "even-reverse" && printMime == "application/pdf" && pages > 1 {
		reorderedPath, reorderCleanup, err := reorderPDFForManualDuplex(printPath, pages, paperSize)
		if err != nil {
			log.Printf("[print] even-reverse reorder failed: %v, falling back to normal even", err)
			pageSet = "even"
		} else {
			defer reorderCleanup()
			printPath = reorderedPath
			reorderedPages, _ := countPDFPages(reorderedPath)
			if reorderedPages > 0 {
				pages = reorderedPages
			}
			pageSet = ""
		}
	}

	// 手动双面（奇/偶 或 偶数倒序）语义上必然单面输出：如果用户没在前端切回单面，
	// 这里强制回落，避免 CUPS pdftopdf 用空白页填补被过滤掉的另一面（issue #109）。
	if origPageSet == "odd" || origPageSet == "even" || origPageSet == "even-reverse" {
		isDuplex = false
	}

	sess, _ := auth.GetSession(r)
	var recordID int64

	if saveHistory {
		err = appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
			user, err := store.GetUserByID(r.Context(), tx, sess.UserID)
			if err != nil {
				return err
			}

			rec := store.PrintRecord{
				UserID:     user.ID,
				PrinterURI: printer,
				Filename:   fh.Filename,
				StoredPath: storedRel,
				Pages:      pages,
				Status:     "queued",
				IsDuplex:   isDuplex,
				IsColor:    isColor,

				Copies:         copies,
				Orientation:    orientation,
				PaperSize:      paperSize,
				PaperType:      paperType,
				MediaSource:    mediaSource,
				PrintScaling:   origPrintScaling,
				PageRange:      pageRange,
				PageSet:        origPageSet,
				Mirror:         mirror,
				WatermarkText:  watermarkText,
				NumberUp:       numberUp,
				NumberUpLayout: numberUpLayout,
				PageBorder:     pageBorder,

				CreatedAt: time.Now().UTC().Format(time.RFC3339),
			}
			id, err := store.InsertPrintRecord(r.Context(), tx, &rec)
			if err != nil {
				return err
			}
			recordID = id
			return nil
		})
		if err != nil {
			_ = os.Remove(storedAbs)
			writeJSONError(w, http.StatusInternalServerError, "failed to create print record")
			return
		}
	}

	f, err := os.Open(printPath)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to open file")
		return
	}
	defer f.Close()

	mime := printMime
	if mime == "" {
		mime = fh.Header.Get("Content-Type")
	}
	if mime == "" {
		buf := make([]byte, 512)
		if n, _ := f.Read(buf); n > 0 {
			mime = http.DetectContentType(buf[:n])
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				writeJSONError(w, http.StatusInternalServerError, "failed to read file")
				return
			}
		}
	}

	printOpts := ipp.PrintJobOptions{
		IsDuplex:     isDuplex,
		IsColor:      isColor,
		Copies:       copies,
		Orientation:  orientation,
		PaperSize:    paperSize,
		PaperType:    paperType,
		PrintScaling: printScaling,
		MediaSource:  mediaSource,
		PageRange:    pageRange,
		PageSet:      pageSet,
		Mirror:       mirror,

		NumberUp:       numberUp,
		NumberUpLayout: numberUpLayout,
		PageBorder:     pageBorder,
	}

	job, err := ipp.SendPrintJob(printer, f, mime, sess.Username, fh.Filename, printOpts)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "print error: "+err.Error())
		return
	}

	if saveHistory && recordID > 0 {
		// ⚠️ 回写失败必须留痕。
		//
		// 纸已经出来了，所以不把这次打印判成失败；但记录会永久停在 queued，
		// 用户在「打印记录」里看到的是「排队中」—— 他会以为没打出去而重复点
		// 重打。这里不出错响应，但日志里得有，否则事后完全查不到。
		if err := appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
			return store.UpdatePrintStatus(r.Context(), tx, recordID, "printed", job)
		}); err != nil {
			log.Printf("[print] 回写打印记录状态失败（记录会停在 queued）: record=%d err=%v", recordID, err)
		}
	}
	if !saveHistory {
		_ = os.Remove(storedAbs)
		cRel := convertedRelPath(storedRel)
		if cRel != "" {
			_ = os.Remove(filepath.Join(uploadDir, filepath.FromSlash(cRel)))
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(printResp{
		JobID:    job,
		OK:       true,
		Pages:    pages,
		IsDuplex: isDuplex,
		IsColor:  isColor,
		Copies:   copies,
	})
}
