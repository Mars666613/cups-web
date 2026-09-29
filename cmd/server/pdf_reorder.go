package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/phpdave11/gofpdf"
)

// buildEvenReversePages returns even page numbers in reverse order.
// When totalPages is odd, a 0 is prepended to represent a blank page
// (so the sheet count matches the odd-pages pass).
//
// Examples:
//
//	totalPages=6 → [6, 4, 2]
//	totalPages=5 → [0, 4, 2]
func buildEvenReversePages(totalPages int) []int {
	var evens []int
	for i := 2; i <= totalPages; i += 2 {
		evens = append(evens, i)
	}
	// reverse
	for l, r := 0, len(evens)-1; l < r; l, r = l+1, r-1 {
		evens[l], evens[r] = evens[r], evens[l]
	}
	if totalPages%2 != 0 {
		evens = append([]int{0}, evens...)
	}
	return evens
}

// reorderPDFForManualDuplex produces a new PDF with even pages in reverse
// order, optionally prepending a blank page when the total page count is odd.
// The caller must invoke the returned cleanup function to remove temp files.
func reorderPDFForManualDuplex(inputPath string, totalPages int, paperSize string) (string, func(), error) {
	pages := buildEvenReversePages(totalPages)
	if len(pages) == 0 {
		return "", nil, fmt.Errorf("no even pages to extract (totalPages=%d)", totalPages)
	}

	needBlank := pages[0] == 0
	var realPages []int
	if needBlank {
		realPages = pages[1:]
	} else {
		realPages = pages
	}

	tmpDir, err := os.MkdirTemp("", "cups-web-reorder-*")
	if err != nil {
		return "", nil, fmt.Errorf("create temp dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(tmpDir) }

	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationRelaxed

	// Collect even pages in reverse order from the input PDF.
	collectPath := filepath.Join(tmpDir, "collected.pdf")
	pageStrs := make([]string, len(realPages))
	for i, p := range realPages {
		pageStrs[i] = strconv.Itoa(p)
	}
	if err := api.CollectFile(inputPath, collectPath, pageStrs, conf); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("pdfcpu collect: %w", err)
	}

	if !needBlank {
		return collectPath, cleanup, nil
	}

	// Generate a single blank page PDF matching the paper size.
	blankPath := filepath.Join(tmpDir, "blank.pdf")
	if err := generateBlankPDF(blankPath, paperSize); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("generate blank page: %w", err)
	}

	// Merge: blank + collected (even reversed)
	outPath := filepath.Join(tmpDir, "final.pdf")
	if err := api.MergeCreateFile([]string{blankPath, collectPath}, outPath, false, conf); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("pdfcpu merge: %w", err)
	}

	return outPath, cleanup, nil
}

// generateBlankPDF creates a single-page blank PDF at the given path.
func generateBlankPDF(outPath string, paperSize string) error {
	w, h := paperDimensionsMM(paperSize)
	pdf := gofpdf.NewCustom(&gofpdf.InitType{
		UnitStr: "mm",
		Size:    gofpdf.SizeType{Wd: w, Ht: h},
	})
	pdf.AddPage()
	return pdf.OutputFileAndClose(outPath)
}

// paperDimensionsMM returns width and height in millimeters for common paper sizes.
func paperDimensionsMM(size string) (float64, float64) {
	switch size {
	case "A3":
		return 297, 420
	case "5inch":
		return 127, 178
	case "6inch":
		return 152, 203
	case "7inch":
		return 178, 229
	case "8inch":
		return 203, 254
	case "10inch":
		return 254, 381
	default: // A4
		return 210, 297
	}
}
