package main

import (
	"log"
	"net/http"
	"strings"

	"cups-web/internal/ipp"
)

// msgPrinterInfoFailed 是读取打印机信息失败时给用户看的话。
//
// 🚫 不要退回成英文的 "failed to get printer info"：这句话原样弹在界面上，
// 用户既看不懂也无法转述，只能截图问人。而它本该回答的是「谁出问题了、
// 我现在该做什么」—— 出问题的不是打印机，是我们到 CUPS 的这一跳。
const msgPrinterInfoFailed = "读取打印机信息失败：打印服务没有及时响应，请稍后重试"

// ippFailureHint 把底层错误归一句类，只写进服务端日志。
//
// 归类刻意做得粗：它的用途是让下一次故障的排查从「读一整串 error 字符串」
// 变成「一眼看出是哪一段断了」。真正的错误原文紧随其后，不会丢。
func ippFailureHint(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "awaiting response headers"),
		strings.Contains(s, "deadline exceeded"),
		strings.Contains(s, "timeout"):
		return "对端没有在超时内回话：cupsd 或它前面的链路卡住"
	case strings.Contains(s, "connection refused"):
		return "对端拒连：该端口上没有服务在监听（CUPS_HOST 写错了？cupsd 没起来？）"
	case strings.Contains(s, "no such host"):
		return "主机名解析失败（CUPS_HOST 写错了？）"
	case strings.Contains(s, "connection reset"), strings.Contains(s, "EOF"):
		return "连接被对端中断"
	case strings.Contains(s, "ssrf"), strings.Contains(s, "blocked"):
		return "目标地址被出站策略拒绝"
	case strings.Contains(s, "http status"), strings.Contains(s, "ipp error"):
		return "对端有回应，但拒绝了这个请求"
	default:
		return "未分类"
	}
}

// printerInfoHandler handles GET /api/printer-info?uri=<printer_uri>
// It queries the printer via IPP Get-Printer-Attributes and returns structured info.
func printerInfoHandler(w http.ResponseWriter, r *http.Request) {
	uri := r.URL.Query().Get("uri")
	log.Printf("[printer-info] request received, uri=%q", uri)

	if uri == "" {
		log.Printf("[printer-info] error: missing uri parameter")
		writeJSONError(w, http.StatusBadRequest, "missing uri parameter")
		return
	}

	log.Printf("[printer-info] calling GetPrinterAttributes for uri=%q", uri)
	info, err := ipp.GetPrinterAttributes(uri)
	if err != nil {
		// 不把底层错误（含 SSRF 策略细节 / 内网探测信息）回显给客户端，
		// 仅记服务端日志，对外返回泛化提示。
		//
		// ⚠️ 日志里补一句【失败分类】。实测这个接口偶发 502，而原始错误
		// （`http post: Post "...": net/http: timeout awaiting response headers`）
		// 需要读进去才知道方向 —— 「对端不回话」和「cupsd 没在监听」是两条
		// 完全不同的排查路线，而它们都会走到这一行。分类只进日志，不进响应。
		log.Printf("[printer-info] 读取失败 uri=%q: %v（%s）", uri, err, ippFailureHint(err))
		writeJSONError(w, http.StatusBadGateway, msgPrinterInfoFailed)
		return
	}

	// 直连探一次设备。
	//
	// ⚠️ cupsd 报的 state / 墨量 / 纸张都是「它脑子里的印象」，不是实时的：
	// 队列没活儿干就说 idle（哪怕打印机拔了电），墨量是上次连上时的缓存，
	// 纸张信息来自 PPD。要让用户看到真实情况，只能绕过 cupsd 直接问设备。
	device := probeDevice(deviceURIOf(info))

	log.Printf("[printer-info] success: name=%q state=%q jobs=%d device.probed=%v device.reachable=%v",
		info.Name, info.State, info.QueuedJobs, device.Probed, device.Reachable)

	writeJSON(w, printerInfoResponse{PrinterInfo: info, Device: toDeviceResponse(device)})
}

// printerInfoResponse 在 cupsd 的属性之外补上【直连探测】的结果。
//
// 两者刻意分开而不是合并：它们回答的是两个不同的问题 ——
// 「队列怎么想」与「打印机实际在不在」。合并会让人以为 idle 就等于可用。
type printerInfoResponse struct {
	*ipp.PrinterInfo
	Device deviceStatusResponse `json:"device"`
}

// deviceStatusResponse 是设备探测结果对外的形状。
//
// Probed 与 Reachable 必须分开：前端要能区分
// 「探了，不可达」（要警告）和「没探，比如 usb 队列」（要闭嘴）。
// 只有 reachable=false 而不带 probed 的话，这两种情况在前端长得一模一样。
type deviceStatusResponse struct {
	Probed       bool     `json:"probed"`
	Reachable    bool     `json:"reachable"`
	State        string   `json:"state,omitempty"`
	StateReasons []string `json:"stateReasons,omitempty"`
	Message      string   `json:"message,omitempty"`
	MarkerNames  []string `json:"markerNames,omitempty"`
	MarkerLevels []int    `json:"markerLevels,omitempty"`
}

func toDeviceResponse(d ipp.DeviceStatus) deviceStatusResponse {
	return deviceStatusResponse{
		Probed:       d.Probed,
		Reachable:    d.Reachable,
		State:        d.State,
		StateReasons: d.StateReasons,
		Message:      d.Message,
		MarkerNames:  d.MarkerNames,
		MarkerLevels: d.MarkerLevels,
	}
}
