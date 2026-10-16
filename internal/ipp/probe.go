package ipp

// 直接探测打印机设备（绕过 cupsd）。
//
// ── 为什么必须绕过 cupsd ──
//
// cups-web 平时只跟 cupsd 说话，而 cupsd 报的那些「打印机状态」有很多
// 并不是打印机的状态：
//
//	printer-state        = idle         队列没活儿干就是 idle。cupsd【不会】
//	                                    主动去探打印机，只有送任务时才会发现
//	                                    连不上。所以打印机拔了电它照样说 idle。
//	media-ready          ← PPD（驱动的能力声明），静态，与打印机通不通电无关
//	media-source-supported← 同上
//	marker-levels        ← 上一次成功连上时缓存的旧值，离线后照端不误
//
// 实测：打印机离线时，cupsd 仍然报 state=idle、四个墨盒 70/60/50/70、
// 五个纸盒 —— 全是它脑子里的印象，跟眼前这台机器没有关系。
//
// device-uri 是 cupsd 唯一给出的真实线索（`ipp://192.168.1.100/ipp/print`）。
// 拿它自己去问一次，才是真的。

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	goipp "github.com/OpenPrinting/goipp"
)

// DeviceStatus 是【直接问打印机】得到的结果。
type DeviceStatus struct {
	// Probed 表示这次探测是否真的执行了。
	//
	// usb:// / dnssd:// 这类地址没法用网络方式探，返回 false。
	// 调用方据此走「未知」分支 —— 而不是把「没探」当成「不可达」，
	// 那会把 USB 打印机的用户全挡在门外。
	Probed bool
	// Reachable 设备是否响应。
	Reachable bool
	// State 是【打印机自己报的】状态（idle / processing / stopped），
	// 不是 cupsd 那个队列状态。只有可达时才有意义。
	State string
	// StateReasons 是 printer-state-reasons，如 media-empty、toner-low。
	StateReasons []string
	// Message 不可达时是失败原因（超时/拒绝连接），可达时是 printer-state-message。
	Message string
	// MarkerNames / MarkerLevels 是【实时】墨量。不可达时为空 ——
	// 空比给一个缓存旧值诚实。
	MarkerNames  []string
	MarkerLevels []int
}

// deviceProbeTimeout 是探测超时。
//
// 刻意取得短：这个探测会挂在「用户选择打印机」这种交互路径上，
// 让他等 15 秒才知道打印机没开，比不给信息还糟。
// 局域网内一次 TCP 握手正常在毫秒级，3 秒足够。
const deviceProbeTimeout = 3 * time.Second

// ProbeDevice 直接探一次 device-uri 指向的打印机。
//
// 支持的网络后端：
//
//	ipp://  ipps://          发一次真正的 IPP Get-Printer-Attributes，
//	                         拿到打印机自报的状态与实时墨量（信息最全）
//	socket://  lpd://        TCP 连一下端口（网络打印机的常规后端）
//	http://  https://        TCP 连一下端口
//
// 其余（usb:// 等）返回 Probed=false。
func ProbeDevice(deviceURI string, timeout time.Duration) DeviceStatus {
	if timeout <= 0 {
		timeout = deviceProbeTimeout
	}
	u, err := url.Parse(strings.TrimSpace(deviceURI))
	if err != nil {
		return DeviceStatus{}
	}

	switch strings.ToLower(u.Scheme) {
	case "ipp", "ipps":
		return probeIPPDevice(u, timeout)
	case "socket", "lpd", "http", "https":
		return probeTCPDevice(u, timeout)
	default:
		// usb:// 之类没法用网络方式探。不是「不可达」，是「探不了」。
		return DeviceStatus{}
	}
}

// probeIPPDevice 对设备发一次 IPP Get-Printer-Attributes。
//
// 这是信息最全的一种：打印机自己报的状态和墨量都是实时的。
func probeIPPDevice(u *url.URL, timeout time.Duration) DeviceStatus {
	if u.Host == "" {
		return DeviceStatus{}
	}
	// IPP 走 HTTP 传输：ipps→https，ipp→http。
	scheme := "http"
	if strings.EqualFold(u.Scheme, "ipps") {
		scheme = "https"
	}
	httpURI := (&url.URL{Scheme: scheme, Host: u.Host, Path: u.Path}).String()
	ippURI := (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String()

	// 复用项目的安全客户端：它在拨号层按解析后的真实 IP 校验，
	// 能防住 DNS rebinding —— device-uri 虽然来自 cupsd（相对可信），
	// 但也没必要为此新开一条不受保护的出网路径。
	if err := validatePrinterURI(httpURI); err != nil {
		return DeviceStatus{Probed: true, Reachable: false, Message: "设备地址不被允许"}
	}

	req := goipp.NewRequest(goipp.DefaultVersion, goipp.OpGetPrinterAttributes, 1)
	req.Operation.Add(goipp.MakeAttribute("attributes-charset", goipp.TagCharset, goipp.String("utf-8")))
	req.Operation.Add(goipp.MakeAttribute("attributes-natural-language", goipp.TagLanguage, goipp.String("en-US")))
	req.Operation.Add(goipp.MakeAttribute("printer-uri", goipp.TagURI, goipp.String(ippURI)))
	var wanted goipp.Values
	for _, name := range []string{
		"printer-state", "printer-state-reasons", "printer-state-message",
		"marker-names", "marker-levels",
	} {
		wanted.Add(goipp.TagKeyword, goipp.String(name))
	}
	req.Operation.Add(goipp.Attribute{Name: "requested-attributes", Values: wanted})

	payload, err := req.EncodeBytes()
	if err != nil {
		return DeviceStatus{Probed: true, Reachable: false, Message: "构造探测请求失败"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, httpURI, bytes.NewBuffer(payload))
	if err != nil {
		return DeviceStatus{Probed: true, Reachable: false, Message: err.Error()}
	}
	httpReq.Header.Set("Content-Type", goipp.ContentType)
	httpReq.Header.Set("Accept", goipp.ContentType)

	client := newSafeClient(timeout)
	resp, err := client.Do(httpReq)
	if err != nil {
		return DeviceStatus{Probed: true, Reachable: false, Message: probeErrText(err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return DeviceStatus{Probed: true, Reachable: false,
			Message: fmt.Sprintf("设备返回 HTTP %d", resp.StatusCode)}
	}

	var rsp goipp.Message
	if err := rsp.Decode(limitedBody(resp.Body)); err != nil {
		// 能连上但看不懂 —— 多半是那个端口上蹲着别的服务。
		return DeviceStatus{Probed: true, Reachable: false, Message: "设备响应无法解析为 IPP"}
	}

	st := DeviceStatus{Probed: true, Reachable: true}
	for _, grp := range rsp.Groups {
		if grp.Tag != goipp.TagPrinterGroup {
			continue
		}
		for _, a := range grp.Attrs {
			if len(a.Values) == 0 {
				continue
			}
			switch a.Name {
			case "printer-state":
				var n int
				if _, err := fmt.Sscanf(a.Values[0].V.String(), "%d", &n); err == nil {
					st.State = deviceStateName(n)
				}
			case "printer-state-reasons":
				for _, v := range a.Values {
					if s := v.V.String(); s != "" && s != "none" {
						st.StateReasons = append(st.StateReasons, s)
					}
				}
			case "printer-state-message":
				st.Message = a.Values[0].V.String()
			case "marker-names":
				for _, v := range a.Values {
					st.MarkerNames = append(st.MarkerNames, v.V.String())
				}
			case "marker-levels":
				for _, v := range a.Values {
					var n int
					if _, err := fmt.Sscanf(v.V.String(), "%d", &n); err == nil {
						st.MarkerLevels = append(st.MarkerLevels, n)
					}
				}
			}
		}
	}
	return st
}

// probeTCPDevice 连一下设备的端口。连得上就说明机器在网络上活着。
func probeTCPDevice(u *url.URL, timeout time.Duration) DeviceStatus {
	host := u.Host
	if host == "" {
		return DeviceStatus{}
	}
	// 没写端口时按 scheme 补默认端口。
	if _, _, err := net.SplitHostPort(host); err != nil {
		port := "9100" // socket:// 的默认端口
		switch strings.ToLower(u.Scheme) {
		case "lpd":
			port = "515"
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
		host = net.JoinHostPort(u.Hostname(), port)
	}

	// ⚠️ 这一条不能省：DialContext 之外的裸 Dial 没有 IP 层校验，
	// 会绕过 newSafeClient 的 SSRF 防护。
	ips, err := net.LookupIP(u.Hostname())
	if err != nil {
		return DeviceStatus{Probed: true, Reachable: false, Message: "域名解析失败"}
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return DeviceStatus{Probed: true, Reachable: false, Message: "设备地址不被允许"}
		}
	}

	conn, err := net.DialTimeout("tcp", host, timeout)
	if err != nil {
		return DeviceStatus{Probed: true, Reachable: false, Message: probeErrText(err)}
	}
	_ = conn.Close()
	return DeviceStatus{Probed: true, Reachable: true, Message: "设备在线"}
}

// probeErrText 把网络错误翻成用户看得懂的话。
func probeErrText(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "timeout") || strings.Contains(s, "deadline exceeded"):
		return "连接超时，打印机可能已关机或不在网络上"
	case strings.Contains(s, "refused"):
		return "连接被拒绝，打印机没有开放该端口"
	case strings.Contains(s, "no route to host"), strings.Contains(s, "network is unreachable"):
		return "网络不可达，请检查打印机与服务器是否在同一网段"
	case strings.Contains(s, "no such host"):
		return "无法解析打印机地址"
	default:
		return s
	}
}

// deviceStateName 把打印机自报的 printer-state 整数翻成名字。
func deviceStateName(n int) string {
	switch n {
	case 3:
		return "idle"
	case 4:
		return "processing"
	case 5:
		return "stopped"
	default:
		return ""
	}
}
