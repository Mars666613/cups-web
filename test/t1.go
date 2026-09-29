//go:build ignore

// 手工调试探针：直接对 CUPS 发一个 IPP Get-Printer-Attributes，把返回的全部属性
// 打印出来。排查"打印机支持哪些选项/纸张/进纸盒"时比翻 PPD 快。
//
// 跑法（需要 cupsd 在 localhost:631，按需改下面的 uri）：
//
//	go run test/t1.go
//
// ⚠️ `//go:build ignore` 是必须的：本目录下的每个调试脚本都是独立的 package main +
// 各自的 func main()，不加这个约束，同一目录里两个 main 会让 `go build ./...` 直接
// 报 "main redeclared in this block"。加了之后它们被排除在常规构建/vet/test 之外，
// 仍可用上面的 `go run <文件>` 单独执行。
package main

import (
	"bytes"
	"net/http"
	"os"

	"github.com/OpenPrinting/goipp"
)

const uri = "http://localhost:631/printers/EPSON_L380_Series"

// Build IPP OpGetPrinterAttributes request
func makeRequest() ([]byte, error) {
	m := goipp.NewRequest(goipp.DefaultVersion, goipp.OpGetPrinterAttributes, 1)
	m.Operation.Add(goipp.MakeAttribute("attributes-charset",
		goipp.TagCharset, goipp.String("utf-8")))
	m.Operation.Add(goipp.MakeAttribute("attributes-natural-language",
		goipp.TagLanguage, goipp.String("en-US")))
	m.Operation.Add(goipp.MakeAttribute("printer-uri",
		goipp.TagURI, goipp.String(uri)))
	m.Operation.Add(goipp.MakeAttribute("requested-attributes",
		goipp.TagKeyword, goipp.String("all")))

	return m.EncodeBytes()
}

// Check that there is no error
func check(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	request, err := makeRequest()
	check(err)

	resp, err := http.Post(uri, goipp.ContentType, bytes.NewBuffer(request))
	check(err)

	var respMsg goipp.Message

	err = respMsg.Decode(resp.Body)
	check(err)

	respMsg.Print(os.Stdout, false)
}
