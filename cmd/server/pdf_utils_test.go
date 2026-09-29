package main

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// 用唯一颜色填充四个角，便于断言旋转后像素去了哪里。
// 坐标约定：x 向右、y 向下。w=2,h=2。
//
//	(0,0)=红 (1,0)=绿
//	(0,1)=蓝 (1,1)=白
func new2x2() *image.RGBA {
	m := image.NewRGBA(image.Rect(0, 0, 2, 2))
	set := func(x, y int, r, g, b uint8) {
		i := (y*m.Stride + x*4)
		m.Pix[i], m.Pix[i+1], m.Pix[i+2], m.Pix[i+3] = r, g, b, 255
	}
	set(0, 0, 255, 0, 0)     // 左上 红
	set(1, 0, 0, 255, 0)     // 右上 绿
	set(0, 1, 0, 0, 255)     // 左下 蓝
	set(1, 1, 255, 255, 255) // 右下 白
	return m
}

func px(m *image.RGBA, x, y int) (uint8, uint8, uint8) {
	i := y*m.Stride + x*4
	return m.Pix[i], m.Pix[i+1], m.Pix[i+2]
}

// 90° 顺时针：左下→左上，左上→右上，右上→右下，右下→左下。
func TestRotateRGBA90CW(t *testing.T) {
	src := new2x2()
	dst := rotateRGBA(src, 1) // 90° CW

	if got := dst.Bounds().Dx(); got != 2 {
		t.Fatalf("90° width = %d, want 2", got)
	}
	cases := []struct {
		x, y    int
		r, g, b uint8
		name    string
	}{
		{0, 0, 0, 0, 255, "左上=原左下(蓝)"},     // src(0,1)
		{1, 0, 255, 0, 0, "右上=原左上(红)"},     // src(0,0)
		{1, 1, 0, 255, 0, "右下=原右上(绿)"},     // src(1,0)
		{0, 1, 255, 255, 255, "左下=原右下(白)"}, // src(1,1)
	}
	for _, c := range cases {
		r, g, b := px(dst, c.x, c.y)
		if r != c.r || g != c.g || b != c.b {
			t.Errorf("dst(%d,%d)=%d,%d,%d, want %d,%d,%d (%s)", c.x, c.y, r, g, b, c.r, c.g, c.b, c.name)
		}
	}
}

// 旋转 4 次 90° 必须回到原图（无损像素搬移）。
func TestRotateRGBA360Identity(t *testing.T) {
	src := new2x2()
	cur := src
	for i := 0; i < 4; i++ {
		cur = rotateRGBA(cur, 1)
	}
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			r1, g1, b1 := px(src, x, y)
			r2, g2, b2 := px(cur, x, y)
			if r1 != r2 || g1 != g2 || b1 != b2 {
				t.Errorf("after 4×90 at (%d,%d): %d,%d,%d != %d,%d,%d", x, y, r2, g2, b2, r1, g1, b1)
			}
		}
	}
}

// 180° = 两次 90°，且左上↔右下、右上↔左下互换。
func TestRotateRGBA180(t *testing.T) {
	src := new2x2()
	dst := rotateRGBA(src, 2)
	cases := []struct {
		x, y    int
		r, g, b uint8
	}{
		{0, 0, 255, 255, 255}, // 左上=原右下(白)
		{1, 1, 255, 0, 0},     // 右下=原左上(红)
		{1, 0, 0, 0, 255},     // 右上=原左下(蓝)
		{0, 1, 0, 255, 0},     // 左下=原右上(绿)
	}
	for _, c := range cases {
		r, g, b := px(dst, c.x, c.y)
		if r != c.r || g != c.g || b != c.b {
			t.Errorf("180° dst(%d,%d)=%d,%d,%d, want %d,%d,%d", c.x, c.y, r, g, b, c.r, c.g, c.b)
		}
	}
}

// 反色：RGB 三通道 255-c，alpha 不变。
func TestInvertRGBA(t *testing.T) {
	m := image.NewRGBA(image.Rect(0, 0, 1, 1))
	m.Pix[0], m.Pix[1], m.Pix[2], m.Pix[3] = 10, 20, 30, 255
	invertRGBA(m)
	if m.Pix[0] != 245 || m.Pix[1] != 235 || m.Pix[2] != 225 {
		t.Errorf("invert = %d,%d,%d, want 245,235,225", m.Pix[0], m.Pix[1], m.Pix[2])
	}
	if m.Pix[3] != 255 {
		t.Errorf("alpha changed to %d, want 255", m.Pix[3])
	}
}

// 端到端串联：造一张 3x2 PNG，验证 applyImageTransforms 的 decode/encode/旋转尺寸。
func TestApplyImageTransforms(t *testing.T) {
	tmpDir := t.TempDir()
	// 3 宽 x 2 高 PNG
	src := image.NewRGBA(image.Rect(0, 0, 3, 2))
	src.Pix[0] = 255 // 标记像素
	inPath := filepath.Join(tmpDir, "src.png")
	f, err := os.Create(inPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, src); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	// 无变换：返回原路径，尺寸不变
	out, cfg, err := applyImageTransforms(inPath, tmpDir, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if out != inPath {
		t.Errorf("no-op should return original path, got %s", out)
	}
	if cfg.Width != 3 || cfg.Height != 2 {
		t.Errorf("no-op cfg = %dx%d, want 3x2", cfg.Width, cfg.Height)
	}

	// 90° 旋转：尺寸互换 3x2 -> 2x3
	out, cfg, err = applyImageTransforms(inPath, tmpDir, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if out == inPath {
		t.Error("rotation should produce a new file")
	}
	if cfg.Width != 2 || cfg.Height != 3 {
		t.Errorf("90° cfg = %dx%d, want 2x3", cfg.Width, cfg.Height)
	}

	// 仅反色：尺寸不变，输出新文件
	out, cfg, err = applyImageTransforms(inPath, tmpDir, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if out == inPath {
		t.Error("invert should produce a new file")
	}
	if cfg.Width != 3 || cfg.Height != 2 {
		t.Errorf("invert cfg = %dx%d, want 3x2", cfg.Width, cfg.Height)
	}
}

// E2E：convertImageToPDF(invert) 端到端产出合法 PDF（issue #87）。
func TestConvertImageToPDFInvert(t *testing.T) {
	tmpDir := t.TempDir()
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := 0; i < len(src.Pix); i += 4 {
		src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = 0, 0, 0, 255 // 全黑
	}
	inPath := filepath.Join(tmpDir, "black.png")
	f, err := os.Create(inPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, src); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	out, cleanup, err := convertImageToPDF(inPath, "portrait", "A4", true)
	if err != nil {
		t.Fatalf("convertImageToPDF(invert) failed: %v", err)
	}
	defer cleanup()
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("output PDF missing: %v", err)
	}
	pages, err := countPDFPages(out)
	if err != nil {
		t.Fatalf("countPDFPages failed: %v", err)
	}
	if pages != 1 {
		t.Errorf("pages = %d, want 1", pages)
	}
}
