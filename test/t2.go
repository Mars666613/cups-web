//go:build ignore

// 手工调试探针：直接对 CUPS 发一个 IPP Print-Job，把 page.pdf 提交成一个打印作业。
// 用来验证"绕过 cups-web 后端，纯 IPP 链路是否通"——排查是后端问题还是 CUPS/驱动问题。
//
// 跑法（TestPage 是相对路径，所以必须在本目录下执行；按需改下面的 PrinterURL）：
//
//	cd test && go run t2.go
//
// ⚠️ `//go:build ignore` 的理由同 t1.go：同目录下多个 package main 会让
// `go build ./...` 报 "main redeclared in this block"。
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/OpenPrinting/goipp"
)

const (
	PrinterURL = "http://localhost:631/printers/EPSON_L380_Series"
	TestPage   = "page.pdf"
)

// checkErr checks for an error. If err != nil, it prints error
// message and exits
func checkErr(err error, format string, args ...any) {
	if err != nil {
		msg := fmt.Sprintf(format, args...)
		fmt.Fprintf(os.Stderr, "%s: %s\n", msg, err)
		os.Exit(1)
	}
}

// ExamplePrintPDF demo
func main() {
	// Build and encode IPP request
	req := goipp.NewRequest(goipp.DefaultVersion, goipp.OpPrintJob, 1)
	req.Operation.Add(goipp.MakeAttribute("attributes-charset",
		goipp.TagCharset, goipp.String("utf-8")))
	req.Operation.Add(goipp.MakeAttribute("attributes-natural-language",
		goipp.TagLanguage, goipp.String("en-US")))
	req.Operation.Add(goipp.MakeAttribute("printer-uri",
		goipp.TagURI, goipp.String(PrinterURL)))
	req.Operation.Add(goipp.MakeAttribute("requesting-user-name",
		goipp.TagName, goipp.String("John Doe")))
	req.Operation.Add(goipp.MakeAttribute("job-name",
		goipp.TagName, goipp.String("job name")))
	req.Operation.Add(goipp.MakeAttribute("document-format",
		goipp.TagMimeType, goipp.String("application/pdf")))

	payload, err := req.EncodeBytes()
	checkErr(err, "IPP encode")

	// Open document file
	file, err := os.Open(TestPage)
	checkErr(err, "Open document file")

	defer file.Close()

	// Build HTTP request
	body := io.MultiReader(bytes.NewBuffer(payload), file)

	httpReq, err := http.NewRequest(http.MethodPost, PrinterURL, body)
	checkErr(err, "HTTP")

	httpReq.Header.Set("content-type", goipp.ContentType)
	httpReq.Header.Set("accept", goipp.ContentType)

	// Execute HTTP request
	httpRsp, err := http.DefaultClient.Do(httpReq)
	if httpRsp != nil {
		defer httpRsp.Body.Close()
	}

	checkErr(err, "HTTP")

	if httpRsp.StatusCode/100 != 2 {
		checkErr(errors.New(httpRsp.Status), "HTTP")
	}

	// Decode IPP response
	rsp := &goipp.Message{}
	err = rsp.Decode(httpRsp.Body)
	checkErr(err, "IPP decode")

	if goipp.Status(rsp.Code) != goipp.StatusOk {
		err = errors.New(goipp.Status(rsp.Code).String())
		checkErr(err, "IPP")
	}
}
