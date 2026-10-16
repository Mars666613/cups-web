package main

// 设备可达性判断的测试。
//
// deviceUnavailableReason 决定「要不要因为打印机连不上而不接单」。
// 它两个方向都会出错，代价都很实在：
//   - 该拦不拦 → 用户付了钱、纸没出来、还得走退款（这正是原来的 bug）
//   - 不该拦却拦 → USB 打印机的用户被永久挡在门外，业务直接不可用

import (
	"testing"

	"cups-web/internal/ipp"
)

func TestDeviceUnavailableReason(t *testing.T) {
	cases := []struct {
		name string
		st   ipp.DeviceStatus
		want string
	}{
		{
			name: "探测确认不可达 → 拦截，并带上原因",
			st:   ipp.DeviceStatus{Probed: true, Reachable: false, Message: "连接超时，打印机可能已关机或不在网络上"},
			want: "连接超时，打印机可能已关机或不在网络上",
		},
		{
			name: "探测确认不可达但没给原因 → 仍要拦截，用兜底文案",
			st:   ipp.DeviceStatus{Probed: true, Reachable: false},
			want: "无法连接到打印机",
		},
		{
			name: "探测到可达 → 放行",
			st:   ipp.DeviceStatus{Probed: true, Reachable: true},
			want: "",
		},
		{
			// ⚠️ 这一条最要紧：usb:// 队列没法用网络探，Probed=false。
			// 把「探不了」当成「不可达」，USB 打印机的用户会被永久拦死。
			name: "没探成（usb 队列 / 拿不到 device-uri / 超时）→ 放行",
			st:   ipp.DeviceStatus{Probed: false, Reachable: false},
			want: "",
		},
		{
			name: "零值（完全没信息）→ 放行",
			st:   ipp.DeviceStatus{},
			want: "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := deviceUnavailableReason(c.st); got != c.want {
				t.Errorf("deviceUnavailableReason(%+v) = %q，期望 %q", c.st, got, c.want)
			}
		})
	}
}

// TestDeviceURIOf 覆盖从 cupsd 属性里取设备地址。
//
// 取不到时必须返回空串（调用方据此放行），而不是猜一个地址 ——
// 猜错了会把「另一台机器的可达性」当成这台的结果。
func TestDeviceURIOf(t *testing.T) {
	if got := deviceURIOf(nil); got != "" {
		t.Errorf("nil 应返回空串，实际 %q", got)
	}
	if got := deviceURIOf(&ipp.PrinterInfo{}); got != "" {
		t.Errorf("没有 Attributes 应返回空串，实际 %q", got)
	}
	if got := deviceURIOf(&ipp.PrinterInfo{Attributes: map[string]string{"other": "x"}}); got != "" {
		t.Errorf("没有 device-uri 应返回空串，实际 %q", got)
	}
	info := &ipp.PrinterInfo{Attributes: map[string]string{"device-uri": "ipp://192.168.1.100/ipp/print"}}
	if got := deviceURIOf(info); got != "ipp://192.168.1.100/ipp/print" {
		t.Errorf("应取到 device-uri，实际 %q", got)
	}
}
