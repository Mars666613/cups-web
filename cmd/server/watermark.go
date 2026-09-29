package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
	"github.com/phpdave11/gofpdf"
)

func generateWatermarkPDF(text string) (string, func(), error) {
	tmpDir, err := os.MkdirTemp("", "cups-web-watermark-*")
	if err != nil {
		return "", nil, fmt.Errorf("create temp dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(tmpDir) }

	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(0, 0, 0)
	pdf.SetAutoPageBreak(false, 0)
	pdf.AddPage()

	if err := setPdfTextFont(pdf, 28); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("load font: %w", err)
	}

	pdf.SetTextColor(180, 180, 180)
	pdf.SetAlpha(0.15, "")

	pageW, pageH := pdf.GetPageSize()
	textW := pdf.GetStringWidth(text)
	if textW < 10 {
		textW = 10
	}

	stepX := textW + 30
	stepY := 45.0
	diag := math.Sqrt(pageW*pageW + pageH*pageH)

	for y := -diag / 2; y < pageH+diag/2; y += stepY {
		for x := -diag / 2; x < pageW+diag/2; x += stepX {
			pdf.TransformBegin()
			pdf.TransformRotate(45, x, y)
			pdf.Text(x, y, text)
			pdf.TransformEnd()
		}
	}

	outPath := filepath.Join(tmpDir, "watermark.pdf")
	if err := pdf.OutputFileAndClose(outPath); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("write watermark pdf: %w", err)
	}
	return outPath, cleanup, nil
}

func applyWatermarkToPDF(inputPath, watermarkText string) (string, func(), error) {
	wmPath, wmCleanup, err := generateWatermarkPDF(watermarkText)
	if err != nil {
		return "", nil, fmt.Errorf("generate watermark: %w", err)
	}
	defer wmCleanup()

	wmFile, err := os.Open(wmPath)
	if err != nil {
		return "", nil, fmt.Errorf("open watermark pdf: %w", err)
	}
	defer wmFile.Close()

	wm, err := api.PDFWatermarkForReadSeeker(wmFile, 1, "scalefactor:1 rel, opacity:1", true, false, types.POINTS)
	if err != nil {
		return "", nil, fmt.Errorf("create watermark config: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "cups-web-wm-apply-*")
	if err != nil {
		return "", nil, fmt.Errorf("create output temp dir: %w", err)
	}
	outCleanup := func() { os.RemoveAll(tmpDir) }

	outPath := filepath.Join(tmpDir, "watermarked.pdf")

	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationRelaxed

	if err := api.AddWatermarksFile(inputPath, outPath, nil, wm, conf); err != nil {
		outCleanup()
		return "", nil, fmt.Errorf("apply watermark: %w", err)
	}

	return outPath, outCleanup, nil
}
