package main

// 打印机设备的直连探测。
//
// ── 为什么不看 cupsd 的状态就够 ──
//
// cupsd 报的很多东西【不是】打印机的状态：
//
//	printer-state            队列没活儿干就是 idle。cupsd 不会主动探打印机，
//	                         只有送任务时才发现连不上 —— 所以打印机拔了电，
//	                         队列照样显示「空闲」。
//	marker-levels            上一次成功连上时缓存的旧值。离线后照端不误。
//	media-ready / media-source-supported
//	                         来自 PPD（驱动的能力声明），静态，与打印机通不通电无关。
//
// 实测：一台离线的打印机，cupsd 仍然报 idle、四个墨盒 70/60/50/70、五个纸盒。
// 全是它脑子里的印象。
//
// device-uri 是 cupsd 唯一给出的真实线索，拿它自己问一次才是真的。

import (
	"sync"
	"time"

	"cups-web/internal/ipp"
)

// deviceProbeTTL 是探测结果的缓存时长。
//
// 探测在不可达时要等满超时（2 秒），而这个结果会被两条路径用到：
// 打印页展示状态、下单前的闸门。不缓存的话，用户在打印页每点一次刷新
// 就要等一次超时。
//
// 取 5 秒：短到「刚把打印机打开」的用户不用等太久，长到同一屏操作能复用。
const deviceProbeTTL = 5 * time.Second

// deviceProbeTimeout 是单次探测的超时。
// 局域网内正常在毫秒级，2 秒足够；再长就是在让用户干等。
const deviceProbeTimeout = 2 * time.Second

type cachedProbe struct {
	status ipp.DeviceStatus
	at     time.Time
}

var deviceProbeCache = struct {
	mu sync.Mutex
	m  map[string]cachedProbe
}{m: map[string]cachedProbe{}}

// probeDevice 探测设备地址，带短时缓存。
func probeDevice(deviceURI string) ipp.DeviceStatus {
	if deviceURI == "" {
		return ipp.DeviceStatus{}
	}

	now := time.Now()
	deviceProbeCache.mu.Lock()
	if c, ok := deviceProbeCache.m[deviceURI]; ok && now.Sub(c.at) < deviceProbeTTL {
		deviceProbeCache.mu.Unlock()
		return c.status
	}
	deviceProbeCache.mu.Unlock()

	// 探测本身不持锁：2 秒的超时没必要把别的打印机一起堵住。
	status := ipp.ProbeDevice(deviceURI, deviceProbeTimeout)

	deviceProbeCache.mu.Lock()
	// 顺手清理，这张表与进程同寿命。
	if len(deviceProbeCache.m) > 256 {
		for k, c := range deviceProbeCache.m {
			if now.Sub(c.at) > time.Hour {
				delete(deviceProbeCache.m, k)
			}
		}
	}
	deviceProbeCache.m[deviceURI] = cachedProbe{status: status, at: now}
	deviceProbeCache.mu.Unlock()

	return status
}

// deviceURIOf 取一个队列的设备地址（device-uri）。
//
// 拿不到时返回空串：那说明 cupsd 没告诉我们它在跟谁说话，
// 调用方应当按「未知」处理，而不是当成「不可达」。
func deviceURIOf(info *ipp.PrinterInfo) string {
	if info == nil || info.Attributes == nil {
		return ""
	}
	return info.Attributes["device-uri"]
}

// printerDeviceStatus 拿到某台队列背后设备的真实状态。
//
// 先问 cupsd 要 device-uri，再直连那台设备。任何一步失败都返回
// Probed=false —— 「没探成」和「探到不可达」是两件事，不能混为一谈。
func printerDeviceStatus(printerURI string) ipp.DeviceStatus {
	info, err := ipp.GetPrinterAttributes(printerURI)
	if err != nil || info == nil {
		return ipp.DeviceStatus{}
	}
	return probeDevice(deviceURIOf(info))
}

// deviceUnavailableReason 返回设备不可用的原因；可用或未知时返回空串。
//
// 🚫 探测不到（Probed=false，比如 usb:// 队列、或拿不到 device-uri）
// 一律返回空串放行 —— 把「探不了」当成「不可达」，会把 USB 打印机的
// 用户全部挡在门外。那种情况交给送印后的印数核对兜底。
func deviceUnavailableReason(st ipp.DeviceStatus) string {
	if !st.Probed || st.Reachable {
		return ""
	}
	if st.Message != "" {
		return st.Message
	}
	return "无法连接到打印机"
}
