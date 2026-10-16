package main

// 空白页判据的用例。度量本身的构造说明见 pdf_blank.go 的文件头 ——
// 这里的图像都是合成的（真机样本没法进仓库），但每个用例都对应一个
// 从真机上观察到的现象：
//
//	TestPageContentBlocks        分块度量对散粒噪声免疫、对成片内容敏感
//	TestPdfLooksBlank_BlankScan  空白件（空进稿器/空玻璃板）判为空白
//	TestPdfLooksBlank_Content    有内容的件不会被误判
//	TestPdfLooksBlank_EdgeLine   走纸通道那条深色边线不能把空白说成有内容
//	TestPdfLooksBlank_NoImages   没有位图（非扫描件）时不作判断（放行）

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/phpdave11/gofpdf"
)

// testGrayImage 造一张灰阶图：底色 254（扫描件里的"白"普遍不到 255），
// 再按需叠内容与噪声。
type grayCanvas struct {
	img *image.Gray
}

func newGrayCanvas(w, h int) *grayCanvas {
	m := image.NewGray(image.Rect(0, 0, w, h))
	for i := range m.Pix {
		m.Pix[i] = 254
	}
	return &grayCanvas{img: m}
}

func (c *grayCanvas) fill(r image.Rectangle, v uint8) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if (image.Point{X: x, Y: y}).In(c.img.Rect) {
				c.img.SetGray(x, y, color.Gray{Y: v})
			}
		}
	}
}

// sprinkleNoise 撒散粒噪声：模拟扫描头底噪 + JPEG 块效应。
// 真机空白页上的暗像素约占 0.34%，且是孤立点 —— 这正是分块均值的
// 存在理由：它对孤立点不敏感，对成片内容敏感。
func (c *grayCanvas) sprinkleNoise(ratio float64, seed int64) {
	rnd := rand.New(rand.NewSource(seed))
	total := c.img.Rect.Dx() * c.img.Rect.Dy()
	for i := 0; i < int(float64(total)*ratio); i++ {
		x := rnd.Intn(c.img.Rect.Dx())
		y := rnd.Intn(c.img.Rect.Dy())
		c.img.SetGray(x, y, color.Gray{Y: uint8(rnd.Intn(120))})
	}
}

func (c *grayCanvas) writeJPEG(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, c.img, &jpeg.Options{Quality: 75}); err != nil {
		t.Fatalf("编码 JPEG 失败: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
	return path
}

// pdfFromJPEGs 用 gofpdf 把若干张 JPEG 组成一份 PDF（每页一张）。
// gofpdf 遇到 .jpg 会原样嵌入（DCTDecode），与扫描仪交回的 PDF 同构。
func pdfFromJPEGs(t *testing.T, dir string, jpegs ...string) string {
	t.Helper()
	pdf := gofpdf.New("P", "mm", "A4", "")
	for _, j := range jpegs {
		pdf.AddPage()
		pdf.Image(j, 0, 0, 210, 297, false, "", 0, "")
	}
	out := filepath.Join(dir, "scan.pdf")
	if err := pdf.OutputFileAndClose(out); err != nil {
		t.Fatalf("生成 PDF 失败: %v", err)
	}
	return out
}

func TestPageContentBlocks(t *testing.T) {
	// 空白 + 散粒噪声：真机空白页的噪声水平下，不该点亮任何一个块。
	blank := newGrayCanvas(1000, 700)
	blank.sprinkleNoise(0.0034, 1)
	if n := pageContentBlocks(blank.img); n != 0 {
		t.Errorf("空白页 + 0.34%% 散粒噪声：有内容块 = %d，想要 0", n)
	}

	// 一小片图章（约 60×60px，300dpi 下 5mm 见方）：必须被认出来。
	stamp := newGrayCanvas(1000, 700)
	stamp.sprinkleNoise(0.0034, 1)
	stamp.fill(image.Rect(470, 320, 530, 380), 40)
	if n := pageContentBlocks(stamp.img); n < blankMinContentBlocks {
		t.Errorf("含 5mm 图章的页：有内容块 = %d，想要 >= %d", n, blankMinContentBlocks)
	}

	// 一行文字（约 600×40px）：复印场景里最小的常见内容。
	line := newGrayCanvas(1000, 700)
	line.sprinkleNoise(0.0034, 1)
	line.fill(image.Rect(200, 300, 800, 340), 30)
	if n := pageContentBlocks(line.img); n < blankMinContentBlocks {
		t.Errorf("含一行文字的页：有内容块 = %d，想要 >= %d", n, blankMinContentBlocks)
	}

	// 边缘线（真机空白页第一行的那条深色线）：裁边之后不该再算内容。
	edge := newGrayCanvas(1000, 700)
	edge.sprinkleNoise(0.0034, 1)
	edge.fill(image.Rect(0, 0, 1000, 2), 0)
	if n := pageContentBlocks(edge.img); n >= blankMinContentBlocks {
		t.Errorf("只有边缘线的页：有内容块 = %d，想要 < %d（边缘应被裁掉）", n, blankMinContentBlocks)
	}
}

func TestPdfLooksBlank_BlankScan(t *testing.T) {
	dir := t.TempDir()
	// 空进稿器真机交回的正是这一张：全白 + 底噪 + 边缘线。
	c := newGrayCanvas(1200, 850)
	c.sprinkleNoise(0.0034, 7)
	c.fill(image.Rect(0, 0, 1200, 2), 0)
	pdf := pdfFromJPEGs(t, dir, c.writeJPEG(t, dir, "blank.jpg"))

	f, err := os.Open(pdf)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	blank, known := pdfLooksBlank(f)
	if !known {
		t.Fatal("known = false，想要 true")
	}
	if !blank {
		t.Error("blank = false，想要 true（这是空白件）")
	}
}

func TestPdfLooksBlank_Content(t *testing.T) {
	dir := t.TempDir()
	// 双页：第一页有内容，第二页空白 —— 只要有一页有内容就不算空白件。
	content := newGrayCanvas(1200, 850)
	content.sprinkleNoise(0.0034, 9)
	content.fill(image.Rect(150, 200, 1050, 260), 20)
	content.fill(image.Rect(150, 400, 900, 460), 20)

	blank := newGrayCanvas(1200, 850)
	blank.sprinkleNoise(0.0034, 11)

	pdf := pdfFromJPEGs(t, dir,
		content.writeJPEG(t, dir, "p1.jpg"),
		blank.writeJPEG(t, dir, "p2.jpg"))

	f, err := os.Open(pdf)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	blankVerdict, known := pdfLooksBlank(f)
	if !known {
		t.Fatal("known = false，想要 true")
	}
	if blankVerdict {
		t.Error("blank = true，想要 false（其中一页有内容）")
	}
}

func TestPdfLooksBlank_EdgeLine(t *testing.T) {
	dir := t.TempDir()
	c := newGrayCanvas(1200, 850)
	c.sprinkleNoise(0.0034, 13)
	c.fill(image.Rect(0, 0, 1200, 3), 0) // 走纸通道边界线，横贯全页
	pdf := pdfFromJPEGs(t, dir, c.writeJPEG(t, dir, "blank_edge.jpg"))

	f, err := os.Open(pdf)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	blank, known := pdfLooksBlank(f)
	if !known {
		t.Fatal("known = false，想要 true")
	}
	if !blank {
		t.Error("blank = false，想要 true（边缘线不该把空白页说成有内容）")
	}
}

// TestPdfLooksBlank_NoImages：没有位图的对象（例如上传的矢量 PDF）不归本
// 判据管，必须返回 known=false 让调用方照常放行 —— 否则 /api/copy 之外的
// 任何误用都会静默拦件。
func TestPdfLooksBlank_NoImages(t *testing.T) {
	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "vector.pdf")
	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.AddPage()
	pdf.SetFont("Helvetica", "", 12)
	pdf.Cell(40, 10, "hello")
	if err := pdf.OutputFileAndClose(pdfPath); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(pdfPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	blank, known := pdfLooksBlank(f)
	if known {
		t.Errorf("known = true（blank=%v），想要 false：没有位图可判", blank)
	}
}
