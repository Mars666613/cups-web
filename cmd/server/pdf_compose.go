package main

import (
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"log"
	"math"
	"mime/multipart"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"unicode"

	"github.com/phpdave11/gofpdf"
	"github.com/phpdave11/gofpdf/contrib/gofpdi"
	"golang.org/x/image/draw"
	rscpdf "rsc.io/pdf"
)

const (
	composeMarginMM   = 10.0
	dashLineLenMM     = 3.0
	dashLineGapMM     = 2.0
	dashLineGrayLevel = 180
	// invoiceSparsePageThreshold 是发票 2-up 合并时判定"稀疏附加页"（如滴滴 PDF
	// 尾部的品牌页/水印页）的阈值：同一 PDF 的非首页若可提取文本 rune 数少于该
	// 阈值，则视为附加页跳过。30 字符（≈15 汉字）远低于任何正常发票单行文本量，
	// 又足以过滤"didi"/"滴滴出行"等极简附加页（issue #93）。
	invoiceSparsePageThreshold = 30
)

type composePage struct {
	pdfPath string
	pageNo  int
	imgPath string
	imgCfg  image.Config
}

func composeInvoice2Up(ctx context.Context, fileHeaders []*multipart.FileHeader) (string, func(), error) {
	if len(fileHeaders) == 0 {
		return "", nil, errors.New("no files provided")
	}

	tmpDir, err := os.MkdirTemp("", "compose-invoice-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }

	pages, err := collectPages(ctx, fileHeaders, tmpDir)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	pages = filterInvoiceSparsePages(pages, invoiceSparsePageThreshold)
	if len(pages) == 0 {
		cleanup()
		return "", nil, errors.New("no pages to compose")
	}

	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(0, 0, 0)
	pdf.SetAutoPageBreak(false, 0)

	imp := gofpdi.NewImporter()

	pageW, pageH := 210.0, 297.0
	halfH := pageH / 2
	slotW := pageW - 2*composeMarginMM
	slotH := halfH - 2*composeMarginMM

	for i := 0; i < len(pages); i += 2 {
		pdf.AddPage()

		if err := placePageInSlot(pdf, imp, &pages[i], composeMarginMM, composeMarginMM, slotW, slotH); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("page %d: %w", i+1, err)
		}

		drawDashLine(pdf, composeMarginMM, halfH, pageW-composeMarginMM, halfH)

		if i+1 < len(pages) {
			if err := placePageInSlot(pdf, imp, &pages[i+1], composeMarginMM, halfH+composeMarginMM, slotW, slotH); err != nil {
				cleanup()
				return "", nil, fmt.Errorf("page %d: %w", i+2, err)
			}
		}
	}

	outPath := filepath.Join(tmpDir, "invoice_composed.pdf")
	if err := pdf.OutputFileAndClose(outPath); err != nil {
		cleanup()
		return "", nil, err
	}
	return outPath, cleanup, nil
}

const (
	idCardWidthMM  = 85.6
	idCardHeightMM = 54.0
	idCardGapMM    = 30.0
)

func composeIdCard(ctx context.Context, fileHeaders []*multipart.FileHeader, paper string) (string, func(), error) {
	if len(fileHeaders) != 2 {
		return "", nil, errors.New("id card mode requires exactly 2 files (front and back)")
	}

	tmpDir, err := os.MkdirTemp("", "compose-idcard-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }

	pages, err := collectPages(ctx, fileHeaders, tmpDir)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	if len(pages) < 2 {
		cleanup()
		return "", nil, errors.New("failed to process id card files")
	}

	for i := range pages {
		if pages[i].imgPath != "" {
			cropped, cfg, cerr := autoCropCard(pages[i].imgPath, tmpDir)
			if cerr == nil {
				pages[i].imgPath = cropped
				pages[i].imgCfg = cfg
			}
		}
	}

	var pageW, pageH float64
	if paper == "A5" {
		pageW, pageH = 148.0, 210.0
		pdf := gofpdf.NewCustom(&gofpdf.InitType{
			OrientationStr: "P",
			UnitStr:        "mm",
			Size:           gofpdf.SizeType{Wd: pageW, Ht: pageH},
		})
		pdf.SetMargins(0, 0, 0)
		pdf.SetAutoPageBreak(false, 0)
		pdf.AddPage()

		imp := gofpdi.NewImporter()

		halfH := pageH / 2
		cardX := (pageW - idCardWidthMM) / 2
		card1Y := (halfH - idCardHeightMM) / 2
		card2Y := halfH + (halfH-idCardHeightMM)/2

		if err := placePageInSlot(pdf, imp, &pages[0], cardX, card1Y, idCardWidthMM, idCardHeightMM); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("front: %w", err)
		}
		if err := placePageInSlot(pdf, imp, &pages[1], cardX, card2Y, idCardWidthMM, idCardHeightMM); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("back: %w", err)
		}

		outPath := filepath.Join(tmpDir, "idcard_composed.pdf")
		if err := pdf.OutputFileAndClose(outPath); err != nil {
			cleanup()
			return "", nil, err
		}
		return outPath, cleanup, nil
	}

	pageW, pageH = 210.0, 297.0
	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(0, 0, 0)
	pdf.SetAutoPageBreak(false, 0)
	pdf.AddPage()

	imp := gofpdi.NewImporter()

	halfH := pageH / 2
	cardX := (pageW - idCardWidthMM) / 2
	card1Y := (halfH - idCardHeightMM) / 2
	card2Y := halfH + (halfH-idCardHeightMM)/2

	if err := placePageInSlot(pdf, imp, &pages[0], cardX, card1Y, idCardWidthMM, idCardHeightMM); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("front: %w", err)
	}
	if err := placePageInSlot(pdf, imp, &pages[1], cardX, card2Y, idCardWidthMM, idCardHeightMM); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("back: %w", err)
	}

	outPath := filepath.Join(tmpDir, "idcard_composed.pdf")
	if err := pdf.OutputFileAndClose(outPath); err != nil {
		cleanup()
		return "", nil, err
	}
	return outPath, cleanup, nil
}

func autoCropCard(inputPath string, tmpDir string) (string, image.Config, error) {
	f, err := os.Open(inputPath)
	if err != nil {
		return inputPath, image.Config{}, err
	}
	img, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return inputPath, image.Config{}, err
	}

	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w < 100 || h < 100 {
		return inputPath, image.Config{Width: w, Height: h}, nil
	}

	lum := make([][]uint8, h)
	for y := 0; y < h; y++ {
		lum[y] = make([]uint8, w)
		for x := 0; x < w; x++ {
			r, g, b, _ := img.At(x+bounds.Min.X, y+bounds.Min.Y).RGBA()
			lum[y][x] = uint8((r*299 + g*587 + b*114) / 1000 >> 8)
		}
	}

	rowSum := make([]float64, h)
	colSum := make([]float64, w)
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			gx := int(lum[y][x+1]) - int(lum[y][x-1])
			gy := int(lum[y+1][x]) - int(lum[y-1][x])
			mag := math.Sqrt(float64(gx*gx + gy*gy))
			rowSum[y] += mag
			colSum[x] += mag
		}
	}

	smoothK := maxInt(w, h) / 40
	if smoothK < 3 {
		smoothK = 3
	}
	rowSmooth := smoothSlice(rowSum, smoothK)
	colSmooth := smoothSlice(colSum, smoothK)

	rowMax := sliceMax(rowSmooth)
	colMax := sliceMax(colSmooth)
	rowThresh := rowMax * 0.15
	colThresh := colMax * 0.15

	top, bottom := 0, h-1
	left, right := 0, w-1
	for top < h && rowSmooth[top] < rowThresh {
		top++
	}
	for bottom > 0 && rowSmooth[bottom] < rowThresh {
		bottom--
	}
	for left < w && colSmooth[left] < colThresh {
		left++
	}
	for right > 0 && colSmooth[right] < colThresh {
		right--
	}

	cropW := right - left
	cropH := bottom - top
	if cropW < w*3/10 || cropH < h*3/10 || cropW <= 0 || cropH <= 0 {
		return inputPath, image.Config{Width: w, Height: h}, nil
	}

	cropped := image.NewRGBA(image.Rect(0, 0, cropW, cropH))
	draw.Draw(cropped, cropped.Bounds(), img, image.Pt(left+bounds.Min.X, top+bounds.Min.Y), draw.Src)

	seq := atomic.AddUint64(&downscaleSeq, 1)
	outPath := filepath.Join(tmpDir, "cropped_"+itoa(int(seq))+".jpg")
	outFile, err := os.Create(outPath)
	if err != nil {
		return inputPath, image.Config{Width: w, Height: h}, err
	}
	if err := jpeg.Encode(outFile, cropped, &jpeg.Options{Quality: 95}); err != nil {
		outFile.Close()
		os.Remove(outPath)
		return inputPath, image.Config{Width: w, Height: h}, err
	}
	outFile.Close()

	return outPath, image.Config{Width: cropW, Height: cropH}, nil
}

func smoothSlice(data []float64, k int) []float64 {
	n := len(data)
	out := make([]float64, n)
	half := k / 2
	for i := 0; i < n; i++ {
		sum := 0.0
		count := 0
		for j := i - half; j <= i+half; j++ {
			if j >= 0 && j < n {
				sum += data[j]
				count++
			}
		}
		out[i] = sum / float64(count)
	}
	return out
}

func sliceMax(data []float64) float64 {
	m := 0.0
	for _, v := range data {
		if v > m {
			m = v
		}
	}
	return m
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func collectPages(ctx context.Context, fileHeaders []*multipart.FileHeader, tmpDir string) ([]composePage, error) {
	var pages []composePage

	for idx, fh := range fileHeaders {
		src, err := fh.Open()
		if err != nil {
			return nil, err
		}
		ext := strings.ToLower(filepath.Ext(fh.Filename))
		if ext == "" {
			ext = ".bin"
		}
		localPath := filepath.Join(tmpDir, "input_"+itoa(idx)+ext)
		dst, err := os.Create(localPath)
		if err != nil {
			src.Close()
			return nil, err
		}
		if _, err := dst.ReadFrom(src); err != nil {
			dst.Close()
			src.Close()
			return nil, err
		}
		dst.Close()
		src.Close()

		kind := detectFileKind(localPath, fh.Filename)
		switch kind {
		case fileKindImage:
			finalPath, cfg, err := downscaleImageIfNeeded(localPath, tmpDir)
			if err != nil {
				return nil, err
			}
			pages = append(pages, composePage{imgPath: finalPath, imgCfg: cfg})

		case fileKindPDF:
			numPages, err := countPDFPages(localPath)
			if err != nil {
				numPages = 1
			}
			if canGofpdiParse(localPath) {
				for p := 1; p <= numPages; p++ {
					pages = append(pages, composePage{pdfPath: localPath, pageNo: p})
				}
			} else {
				imgPages, err := renderPDFToImages(ctx, localPath, numPages, tmpDir)
				if err != nil {
					return nil, fmt.Errorf("cannot process PDF %s: %w", fh.Filename, err)
				}
				pages = append(pages, imgPages...)
			}

		case fileKindOFD:
			outPath, cleanupConv, err := convertOFDToPDF(ctx, localPath)
			if err != nil {
				return nil, err
			}
			permPath := filepath.Join(tmpDir, "ofd_converted_"+itoa(idx)+".pdf")
			if copyErr := copyFile(outPath, permPath); copyErr != nil {
				cleanupConv()
				return nil, copyErr
			}
			cleanupConv()
			numPages, _ := countPDFPages(permPath)
			if numPages < 1 {
				numPages = 1
			}
			for p := 1; p <= numPages; p++ {
				pages = append(pages, composePage{pdfPath: permPath, pageNo: p})
			}

		case fileKindOffice:
			outPath, cleanupConv, err := convertOfficeToPDF(ctx, localPath)
			if err != nil {
				return nil, err
			}
			permPath := filepath.Join(tmpDir, "office_converted_"+itoa(idx)+".pdf")
			if copyErr := copyFile(outPath, permPath); copyErr != nil {
				cleanupConv()
				return nil, copyErr
			}
			cleanupConv()
			numPages, _ := countPDFPages(permPath)
			if numPages < 1 {
				numPages = 1
			}
			for p := 1; p <= numPages; p++ {
				pages = append(pages, composePage{pdfPath: permPath, pageNo: p})
			}

		default:
			return nil, errors.New("unsupported file type: " + fh.Filename)
		}
	}

	return pages, nil
}

func placePageInSlot(pdf *gofpdf.Fpdf, imp *gofpdi.Importer, page *composePage, slotX, slotY, slotW, slotH float64) (err error) {
	if page.imgPath != "" {
		imgW := float64(page.imgCfg.Width)
		imgH := float64(page.imgCfg.Height)
		scale := math.Min(slotW/imgW, slotH/imgH)
		if scale <= 0 {
			scale = 1
		}
		w := imgW * scale
		h := imgH * scale
		x := slotX + (slotW-w)/2
		y := slotY + (slotH-h)/2
		opts := gofpdf.ImageOptions{ImageType: "", ReadDpi: true}
		pdf.ImageOptions(page.imgPath, x, y, w, h, false, opts, 0, "")
		return nil
	}

	if page.pdfPath != "" {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("failed to import PDF %s: %v", filepath.Base(page.pdfPath), r)
			}
		}()

		tplId := imp.ImportPage(pdf, page.pdfPath, page.pageNo, "/MediaBox")
		sizes := imp.GetPageSizes()
		srcW, srcH := 210.0, 297.0
		if ps, ok := sizes[page.pageNo]; ok {
			if mb, ok := ps["/MediaBox"]; ok {
				if w, ok := mb["w"]; ok {
					srcW = w
				}
				if h, ok := mb["h"]; ok {
					srcH = h
				}
			}
		}
		scale := math.Min(slotW/srcW, slotH/srcH)
		if scale <= 0 {
			scale = 1
		}
		w := srcW * scale
		h := srcH * scale
		x := slotX + (slotW-w)/2
		y := slotY + (slotH-h)/2
		imp.UseImportedTemplate(pdf, tplId, x, y, w, h)
	}
	return nil
}

func drawDashLine(pdf *gofpdf.Fpdf, x1, y1, x2, y2 float64) {
	pdf.SetDrawColor(dashLineGrayLevel, dashLineGrayLevel, dashLineGrayLevel)
	pdf.SetLineWidth(0.3)
	pdf.SetDashPattern([]float64{dashLineLenMM, dashLineGapMM}, 0)
	pdf.Line(x1, y1, x2, y2)
	pdf.SetDashPattern([]float64{}, 0)
	pdf.SetDrawColor(0, 0, 0)
}

func canGofpdiParse(pdfPath string) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			ok = false
		}
	}()
	imp := gofpdi.NewImporter()
	pdf := gofpdf.New("P", "mm", "A4", "")
	imp.ImportPage(pdf, pdfPath, 1, "/MediaBox")
	return true
}

func renderPDFToImages(ctx context.Context, pdfPath string, numPages int, tmpDir string) ([]composePage, error) {
	gsBin, err := exec.LookPath("gs")
	if err != nil {
		return nil, fmt.Errorf("ghostscript not found, cannot render PDF to images")
	}

	log.Printf("[compose] gofpdi cannot parse %s, falling back to gs rendering (%d pages)", filepath.Base(pdfPath), numPages)

	var pages []composePage
	for p := 1; p <= numPages; p++ {
		seq := atomic.AddUint64(&downscaleSeq, 1)
		outPath := filepath.Join(tmpDir, fmt.Sprintf("pdfrender_%d.jpg", seq))

		args := []string{
			"-dNOPAUSE", "-dBATCH", "-dSAFER", "-dQUIET",
			"-sDEVICE=jpeg", "-dJPEGQ=95", "-r300",
			fmt.Sprintf("-dFirstPage=%d", p),
			fmt.Sprintf("-dLastPage=%d", p),
			"-sOutputFile=" + outPath,
			pdfPath,
		}
		cmd := exec.CommandContext(ctx, gsBin, args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("gs render page %d failed: %w - %s", p, err, string(out))
		}

		f, err := os.Open(outPath)
		if err != nil {
			return nil, err
		}
		cfg, _, err := image.DecodeConfig(f)
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read rendered image: %w", err)
		}
		pages = append(pages, composePage{imgPath: outPath, imgCfg: cfg})
	}
	return pages, nil
}

// pdfPageTextLenFn 返回 PDF 指定页可提取文本的 rune 数。抽成变量便于测试注入。
// rsc.io/pdf 对某些畸形 PDF 会 panic，包装层用 recover 兜底，失败视为"未知长度"
// 返回 -1，调用方按"保留该页"处理。
var pdfPageTextLenFn = pdfPageTextRuneCount

func pdfPageTextRuneCount(path string, pageNo int) (n int, err error) {
	defer func() {
		if r := recover(); r != nil {
			n = -1
			err = fmt.Errorf("pdf text panic: %v", r)
		}
	}()
	doc, err := rscpdf.Open(path)
	if err != nil {
		return -1, err
	}
	if pageNo < 1 || pageNo > doc.NumPage() {
		return -1, fmt.Errorf("page %d out of range", pageNo)
	}
	page := doc.Page(pageNo)
	if page.V.IsNull() {
		return -1, fmt.Errorf("page %d is null", pageNo)
	}
	content := page.Content()
	count := 0
	for _, t := range content.Text {
		for _, r := range t.S {
			if unicode.IsSpace(r) {
				continue
			}
			count++
		}
	}
	return count, nil
}

// filterInvoiceSparsePages 过滤发票 2-up 合并中的"稀疏附加页"。同一 PDF 的首页
// 无条件保留；从第 2 页起，若可提取文本的非空白 rune 数少于 threshold，视为附
// 加页（如滴滴 PDF 尾部只有"didi"字样的品牌页）并跳过。文本提取失败/未知时保
// 留该页，避免误杀（issue #93）。
//
// 图片页（imgPath != ""）不参与过滤——扫描图像发票天然无文本流。
func filterInvoiceSparsePages(pages []composePage, threshold int) []composePage {
	if len(pages) == 0 {
		return pages
	}
	// firstPageSeen 记录每个源 PDF 是否已经至少保留了一页（一般是第 1 页；用 map
	// 而非依赖 pageNo==1，是因为 pageNo 语义是"源 PDF 中的页码"，且我们只想对
	// "同一 PDF 内的后续页"启用启发式，而不是全局第 1 页）。
	firstPageSeen := make(map[string]bool)
	filtered := make([]composePage, 0, len(pages))
	for _, p := range pages {
		if p.imgPath != "" || p.pdfPath == "" {
			filtered = append(filtered, p)
			continue
		}
		if !firstPageSeen[p.pdfPath] {
			firstPageSeen[p.pdfPath] = true
			filtered = append(filtered, p)
			continue
		}
		n, err := pdfPageTextLenFn(p.pdfPath, p.pageNo)
		if err != nil || n < 0 {
			// 无法判定：保留该页
			filtered = append(filtered, p)
			continue
		}
		if n < threshold {
			log.Printf("[compose] skip sparse page %s p%d (text len=%d < %d)",
				filepath.Base(p.pdfPath), p.pageNo, n, threshold)
			continue
		}
		filtered = append(filtered, p)
	}
	return filtered
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = out.ReadFrom(in)
	return err
}
