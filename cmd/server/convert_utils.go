package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// errBinaryNotInstalled 是一个跨模块共享的哨兵错误，表示某个**外部命令行工具**
// （gs / libreoffice 等）在本机 PATH 中找不到。
//
// 该错误**仅用于日志措辞**：PDF 标准化管线拿到它可以打友好的"已跳过"日志，
// 而不是把一大串 `exec: "xxx": executable file not found in $PATH` 噪声丢给运维/开发看。
// 降级控制流不受影响——不论是"未安装"还是"执行失败"，上层都会继续尝试下一级 fallback。
//
// 注意：返回这个错误时，error message 的前缀（"libreoffice" / "ghostscript"）由各
// 包装函数自行决定，errors.Is 只用于识别"工具未安装"这一类型。
var errBinaryNotInstalled = errors.New("not installed")

// runLibreOfficeConvert 调用 libreoffice --headless --convert-to <filter> 做通用文档转换，
// 返回生成的 PDF 绝对路径、清理函数与错误。filter 为空时默认使用 "pdf"。
// infilter 不为空时，会在命令行中追加 --infilter=<infilter> 参数，用于指定输入格式
// （例如 PDF→PDF 场景传入 "writer_pdf_import" 让 LibreOffice 用 Writer 的 PDF 导入器
// 解析原 PDF，而不是走 Draw 默认路径——后者对中文字体映射更差）。
//
// libreoffice 不在 PATH 中时，返回的错误可被 errors.Is(err, errBinaryNotInstalled) 识别，
// 供 PDF 标准化管线做"友好跳过"日志处理；Office 文档转换场景则正常向上报错即可。
func runLibreOfficeConvert(ctx context.Context, inputPath string, filter string, infilter string) (string, func(), error) {
	if _, err := exec.LookPath("libreoffice"); err != nil {
		return "", nil, fmt.Errorf("libreoffice %w", errBinaryNotInstalled)
	}

	tmpDir, err := os.MkdirTemp("", "convert-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }

	convertTo := filter
	if convertTo == "" {
		convertTo = "pdf"
	}
	args := []string{"--headless", "--convert-to", convertTo}
	if infilter != "" {
		args = append(args, "--infilter="+infilter)
	}
	args = append(args, "--outdir", tmpDir, inputPath)
	cmd := exec.CommandContext(ctx, "libreoffice", args...)
	cmd.Env = append(os.Environ(), "LANG=zh_CN.UTF-8", "LC_ALL=zh_CN.UTF-8")
	if out, err := cmd.CombinedOutput(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("conversion failed: %w - %s", err, string(out))
	}

	base := filepath.Base(inputPath)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	outPath := filepath.Join(tmpDir, name+".pdf")
	if _, err := os.Stat(outPath); os.IsNotExist(err) {
		matches, _ := filepath.Glob(filepath.Join(tmpDir, "*.pdf"))
		if len(matches) == 0 {
			cleanup()
			return "", nil, fmt.Errorf("conversion produced no PDF")
		}
		outPath = matches[0]
	}

	return outPath, cleanup, nil
}

// convertOfficeToPDF 将 Office 文档（.doc/.docx/.xls/.xlsx/.ppt/.pptx）转成 PDF。
func convertOfficeToPDF(ctx context.Context, inputPath string) (string, func(), error) {
	return runLibreOfficeConvert(ctx, inputPath, "pdf", "")
}

// convertPDFViaLibreOffice 通过 LibreOffice 重新导出 PDF，用作 Ghostscript 不可用时的兜底。
// 使用 --infilter=writer_pdf_import 让 LibreOffice 通过 Writer 的 PDF 导入器解析原 PDF，
// 而不是走 Draw 默认路径——Writer 导入器对中文字体的映射更准确，能正确处理 GBK 编码
// CID 字体的 PDF，避免 Ghostscript 10.x pdfwrite 破坏文本编码导致的中文乱码问题。
func convertPDFViaLibreOffice(ctx context.Context, inputPath string) (string, func(), error) {
	return runLibreOfficeConvert(ctx, inputPath, "pdf", "writer_pdf_import")
}

func convertOFDToPDF(ctx context.Context, inputPath string) (string, func(), error) {
	tmpDir, err := os.MkdirTemp("", "convert-ofd-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }

	outPath := filepath.Join(tmpDir, "output.pdf")

	jarPath := os.Getenv("OFD_CONVERTER_JAR")
	if jarPath == "" {
		jarPath = "/ofd-converter.jar"
	}

	cmd := exec.CommandContext(ctx, "java", "-Xmx512m", "-jar", jarPath, inputPath, outPath)
	cmd.Env = append(os.Environ(), "LANG=zh_CN.UTF-8", "LC_ALL=zh_CN.UTF-8")
	if out, err := cmd.CombinedOutput(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("OFD to PDF conversion failed: %w - %s", err, string(out))
	}

	if _, err := os.Stat(outPath); os.IsNotExist(err) {
		cleanup()
		return "", nil, fmt.Errorf("OFD to PDF conversion produced no output")
	}

	return outPath, cleanup, nil
}

func convertTimeoutContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 60*time.Second)
}
