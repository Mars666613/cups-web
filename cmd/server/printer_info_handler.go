package main

import (
	"log"
	"net/http"

	"cups-web/internal/ipp"
)

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
		log.Printf("[printer-info] GetPrinterAttributes error: %v", err)
		writeJSONError(w, http.StatusBadGateway, "failed to get printer info")
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
