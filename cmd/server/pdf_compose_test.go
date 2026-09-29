package main

import (
	"fmt"
	"image"
	"testing"
)

func TestFilterInvoiceSparsePages(t *testing.T) {
	// stub pdfPageTextLenFn：按 (path,pageNo) -> (n, err) 表返回
	type key struct {
		path string
		p    int
	}
	type ret struct {
		n   int
		err error
	}
	table := map[key]ret{}
	original := pdfPageTextLenFn
	pdfPageTextLenFn = func(path string, pageNo int) (int, error) {
		if v, ok := table[key{path, pageNo}]; ok {
			return v.n, v.err
		}
		return -1, fmt.Errorf("unmapped %s p%d", path, pageNo)
	}
	t.Cleanup(func() { pdfPageTextLenFn = original })

	tests := []struct {
		name    string
		pages   []composePage
		texts   map[key]ret
		want    []composePage
		wantLen int
	}{
		{
			name: "single-page PDF is always kept even if sparse (first-page rule)",
			pages: []composePage{
				{pdfPath: "/a.pdf", pageNo: 1},
			},
			texts:   map[key]ret{{"/a.pdf", 1}: {n: 5}},
			wantLen: 1,
		},
		{
			name: "multi-page PDF: first page kept, sparse non-first dropped",
			pages: []composePage{
				{pdfPath: "/didi.pdf", pageNo: 1},
				{pdfPath: "/didi.pdf", pageNo: 2},
			},
			texts: map[key]ret{
				{"/didi.pdf", 1}: {n: 500},
				{"/didi.pdf", 2}: {n: 4}, // "didi"
			},
			wantLen: 1,
		},
		{
			name: "multi-page PDF: rich non-first kept",
			pages: []composePage{
				{pdfPath: "/multi.pdf", pageNo: 1},
				{pdfPath: "/multi.pdf", pageNo: 2},
			},
			texts: map[key]ret{
				{"/multi.pdf", 1}: {n: 400},
				{"/multi.pdf", 2}: {n: 400},
			},
			wantLen: 2,
		},
		{
			name: "image page never filtered",
			pages: []composePage{
				{imgPath: "/scan.jpg", imgCfg: image.Config{Width: 100, Height: 100}},
				{imgPath: "/scan2.jpg", imgCfg: image.Config{Width: 100, Height: 100}},
			},
			wantLen: 2,
		},
		{
			name: "text extraction error keeps page",
			pages: []composePage{
				{pdfPath: "/broken.pdf", pageNo: 1},
				{pdfPath: "/broken.pdf", pageNo: 2},
			},
			texts: map[key]ret{
				{"/broken.pdf", 1}: {n: 300},
				{"/broken.pdf", 2}: {n: -1, err: fmt.Errorf("boom")},
			},
			wantLen: 2,
		},
		{
			name: "two separate PDFs: each keeps its own first page",
			pages: []composePage{
				{pdfPath: "/a.pdf", pageNo: 1},
				{pdfPath: "/b.pdf", pageNo: 1},
				{pdfPath: "/b.pdf", pageNo: 2},
			},
			texts: map[key]ret{
				{"/a.pdf", 1}: {n: 3}, // sparse but first
				{"/b.pdf", 1}: {n: 3}, // sparse but first
				{"/b.pdf", 2}: {n: 3}, // sparse, non-first → drop
			},
			wantLen: 2,
		},
		{
			name: "threshold boundary: exactly threshold kept",
			pages: []composePage{
				{pdfPath: "/x.pdf", pageNo: 1},
				{pdfPath: "/x.pdf", pageNo: 2},
			},
			texts: map[key]ret{
				{"/x.pdf", 1}: {n: 100},
				{"/x.pdf", 2}: {n: 30}, // == threshold, not < → keep
			},
			wantLen: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			table = tc.texts
			got := filterInvoiceSparsePages(tc.pages, 30)
			if len(got) != tc.wantLen {
				t.Fatalf("got %d pages, want %d: %+v", len(got), tc.wantLen, got)
			}
		})
	}
}
