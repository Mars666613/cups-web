package main

import (
	"context"
	"os"
	"testing"
	"time"

	"cups-web/internal/escl"
)

func TestCupsQueueName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"http://localhost:631/printers/printer-192-168-1-100", "printer-192-168-1-100"},
		{"ipp://cups:631/printers/office", "office"},
		{"HTTP://LOCALHOST:631/PRINTERS/UPPER", "UPPER"},
		// 设备地址：不是队列地址，必须原样留给 eSCL 推导。
		{"ipp://192.168.1.100/ipp/print", ""},
		{"http://192.168.1.100/eSCL", ""},
		// 只剩队列名（无 scheme）无法定位 CUPS 主机。
		{"printer-192-168-1-100", ""},
		{"", ""},
		// 队列名后面还有路径段的不是队列地址。
		{"http://localhost:631/printers/a/b", ""},
		{"http://localhost:631/printers/", ""},
	}
	for _, c := range cases {
		if got := cupsQueueName(c.in); got != c.want {
			t.Errorf("cupsQueueName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestToHTTPURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ipp://localhost:631/printers/x", "http://localhost:631/printers/x"},
		{"ipps://cups/printers/x", "https://cups/printers/x"},
		{"http://localhost:631/printers/x", "http://localhost:631/printers/x"},
	}
	for _, c := range cases {
		if got := toHTTPURL(c.in); got != c.want {
			t.Errorf("toHTTPURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestLiveResolveScanTarget 只读真机验证：从【CUPS 队列地址】走完整解析链路
// （队列 → device-uri → eSCL 能力/状态），默认跳过。跑法：
//
//	CUPS_COPY_QUEUE_URI="http://localhost:631/printers/printer-192-168-1-100" \
//	  go test ./cmd/server/ -run TestLiveResolveScanTarget -v
//
// 只调查询端点，不创建扫描任务、不动硬件。
// 这个用例守的就是「队列地址被当成设备地址」那个坑：解析失败时它会直接挂掉。
func TestLiveResolveScanTarget(t *testing.T) {
	queueURI := os.Getenv("CUPS_COPY_QUEUE_URI")
	if queueURI == "" {
		t.Skip("设置 CUPS_COPY_QUEUE_URI=<CUPS 队列地址> 才启用真机只读验证")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	target := resolveScanTarget(ctx, queueURI)
	t.Logf("队列 %s → 扫描目标 %s", queueURI, target)
	if target == queueURI {
		t.Fatalf("队列地址未被解析成设备地址，说明 device-uri 查询这一环没起作用")
	}

	c := escl.NewClient(target)
	if c == nil {
		t.Fatalf("无法从 %q 推导 eSCL 地址", target)
	}

	caps, err := c.Capabilities(ctx)
	if err != nil {
		t.Fatalf("Capabilities(%s): %v", c.BaseURL, err)
	}
	t.Logf("型号 %s | platen=%v adf=%v duplex=%v color=%v",
		caps.MakeAndModel, caps.HasPlaten, caps.HasAdf, caps.AdfDuplex, caps.Color)

	st, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("Status(%s): %v", c.BaseURL, err)
	}
	t.Logf("状态 %s (adf=%s idle=%v)", st.State, st.AdfState, st.Idle())
}
