package main

// 扫描目标的解析：前端选打印机时给的是【CUPS 队列地址】，而不是打印机
// 设备地址，两者不要混为一谈。
//
//	队列地址（前端 /api/printers 下发的 p.uri）：http://localhost:631/printers/printer-192-168-1-100
//	设备地址（CUPS 里该队列的 device-uri）：        ipp://192.168.1.100/ipp/print
//
// 打印走队列地址是对的 —— 打印任务本来就该交给 cupsd；但【扫描】必须直接找
// 打印机本体，因为 eSCL 是打印机自带的 Web 服务，cupsd 不管扫描。
// 所以这里要把队列地址解析成它背后的设备地址再推导 eSCL，否则会得到
// http://localhost/eSCL 这种打向 CUPS 主机的地址，次次连接失败，
// 界面上表现为「这台打印机不支持远程复印」——一个极具误导性的错误。

import (
	"context"
	"log"
	"strings"

	"cups-web/internal/escl"
	"cups-web/internal/ipp"
)

// cupsQueueName 从 CUPS 队列地址里取出队列名。
// 形如 http://<cups>/printers/<name> 返回 <name>；不是队列地址则返回空串。
func cupsQueueName(printerURI string) string {
	name := strings.TrimSpace(printerURI)
	// 前缀判断大小写无关，交给 url 解析做其余校验。
	idx := strings.Index(strings.ToLower(name), "/printers/")
	if idx < 0 {
		return ""
	}
	if !hasNetworkScheme(name) {
		return ""
	}
	rest := name[idx+len("/printers/"):]
	// 队列名后面不允许再带路径/查询，否则不是我们认识的队列地址。
	if rest == "" || strings.ContainsAny(rest, "/?#") {
		return ""
	}
	return rest
}

// hasNetworkScheme 判断是不是 http(s):// 或 ipp(s):// 开头的网络地址。
func hasNetworkScheme(uri string) bool {
	lower := strings.ToLower(uri)
	for _, p := range []string{"http://", "https://", "ipp://", "ipps://"} {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// toHTTPURL 把 ipp(s):// 换成 http(s)://，供 IPP over HTTP 的请求使用。
// CUPS 的 IPP 接口本身跑在 HTTP 上，只是 URI 里写的是 ipp://。
func toHTTPURL(uri string) string {
	lower := strings.ToLower(uri)
	switch {
	case strings.HasPrefix(lower, "ipp://"):
		return "http://" + uri[len("ipp://"):]
	case strings.HasPrefix(lower, "ipps://"):
		return "https://" + uri[len("ipps://"):]
	}
	return uri
}

// resolveScanTarget 把前端传来的打印机地址换算成「扫描仪所在设备」的地址。
//
//   - 传进来就是设备地址（ipp://192.168.1.100/ipp/print）→ 原样返回；
//   - 传进来是 CUPS 队列地址 → 查 CUPS 要 device-uri 后返回；
//   - 查不到（队列已删、CUPS 抽风、队列挂在 usb:// 上）→ 退回原值，
//     让 eSCL 客户端去失败并把错误如实报出来，不在这里编造结论。
func resolveScanTarget(ctx context.Context, printerURI string) string {
	name := cupsQueueName(printerURI)
	if name == "" {
		return printerURI
	}

	info, err := ipp.GetPrinterAttributes(toHTTPURL(printerURI))
	if err != nil {
		log.Printf("[copy] 查询队列 %s 的设备地址失败: %v", name, err)
		return printerURI
	}
	device := strings.TrimSpace(info.Attributes["device-uri"])
	if device == "" {
		log.Printf("[copy] 队列 %s 未上报 device-uri，按原地址探测", name)
		return printerURI
	}
	// device-uri 可能是 usb:// 这类本地接口，那种情况 eSCL 无从探测，
	// 由 escl.NewClient 判空后给出「仅支持网络一体机」的提示。
	if device != printerURI {
		log.Printf("[copy] 扫描目标解析: %s → %s", printerURI, device)
	}
	return device
}

// newScannerClient 解析扫描目标并建 eSCL 客户端，同时回传实际探测地址
// （给日志与前端排障用）。无法使用网络扫描时 client 为 nil。
func newScannerClient(ctx context.Context, printerURI string) (*escl.Client, string) {
	target := resolveScanTarget(ctx, printerURI)
	return escl.NewClient(target), target
}
