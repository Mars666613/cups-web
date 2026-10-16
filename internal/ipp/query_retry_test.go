package ipp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// 这一组用例钉的是「只读查询卡住时会发生什么」。
//
// 起因是一次实测故障：/api/printer-info 偶发 502，浏览器网络面板显示
// 「TCP 连接 1ms 建好 → 一个字节都没收到 → 恰好卡满 15.00 秒」。
// 那 15 秒来自 dialTimeout，而它是为【送印】定的（多页文档要过过滤器、
// 要上传），套在几十字节的只读查询上纯属误伤。
//
// 修法是两条：只读查询用 queryTimeout（5 秒），并允许换条连接重试一次。
// 下面分别钉住「哪些错值得重试」和「重试必须重放请求体」。

func TestRetryableQueryErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		// 本次故障的错误原文就是这个（Client.Timeout exceeded while awaiting headers）。
		{"等响应头超时", errors.New("net/http: timeout awaiting response headers"), true},
		{"上下文超时", context.DeadlineExceeded, true},
		{"对端把连接丢了", errors.New("read tcp 127.0.0.1:5000->127.0.0.1:631: connection reset by peer"), true},
		{"半路 EOF", io.ErrUnexpectedEOF, true},
		{"写不进去", errors.New("write tcp: broken pipe"), true},

		// 下面这些重试多少次结果都一样，只会在响应时间上叠加 5 秒并刷屏日志。
		{"被出站策略拒绝", fmt.Errorf("%w: 169.254.169.254", errSSRFBlocked), false},
		{"对端拒连", fmt.Errorf("dial tcp 127.0.0.1:631: %w", syscall.ECONNREFUSED), false},
		{"域名解析失败", &net.DNSError{Err: "no such host", Name: "printer.invalid"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := retryableQueryErr(c.err); got != c.want {
				t.Errorf("retryableQueryErr(%v) = %v，期望 %v", c.err, got, c.want)
			}
		})
	}
}

// TestQueryTimeoutShorterThanDial 是一条防回退的断言：只读查询的超时必须
// 明显短于送印用的 dialTimeout，否则这次修复会被一次「统一一下超时」的
// 清理顺手抹掉，而症状只会在网络卡顿时重现。
func TestQueryTimeoutShorterThanDial(t *testing.T) {
	if queryTimeout >= dialTimeout {
		t.Fatalf("queryTimeout(%v) 必须小于 dialTimeout(%v)", queryTimeout, dialTimeout)
	}
	// 重试一次的总耗时也得短于原来的 15 秒，否则「修完更慢」。
	if 2*queryTimeout >= dialTimeout {
		t.Errorf("queryTimeout 的两倍（%v）应当仍小于 dialTimeout(%v)", 2*queryTimeout, dialTimeout)
	}
}

// TestDoQueryRetryRetriesAndReplaysBody 覆盖重试的核心：第一次「连上了但
// 不会回话」，第二次成功，且**请求体完整重放**。
//
// 请求体那一条是重点：IPP 查询的报文全在 body 里，重试时如果发出去一个
// 空 body，对端会回一个参数错误 —— 那比超时更难排查，因为它看起来像
// 「打印机不认这个请求」，而不是「我们发空了」。
func TestDoQueryRetryRetriesAndReplaysBody(t *testing.T) {
	var attempts int32
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			// 复现故障形态：连接建得起来，但对端一个字都不回就把连接关掉。
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("httptest 的响应器不支持 Hijack，用例前提不成立")
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Errorf("hijack 失败: %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	payload := []byte("IPP-PAYLOAD-0123456789-abcdefghij")
	req, err := http.NewRequest(http.MethodPost, srv.URL, bytes.NewBuffer(payload))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	if req.GetBody == nil {
		t.Fatal("bytes.Buffer 建的请求应当带 GetBody，否则重试无法重放")
	}

	resp, err := doQueryRetry(newSafeClient(3*time.Second), req, 2)
	if err != nil {
		t.Fatalf("第二次应当成功，却返回错误: %v", err)
	}
	defer resp.Body.Close()

	if n := atomic.LoadInt32(&attempts); n != 2 {
		t.Errorf("期望恰好尝试 2 次，实际 %d 次", n)
	}
	if !bytes.Equal(gotBody, payload) {
		t.Errorf("重试必须原样重放请求体\n  期望 %q\n  实际 %q", payload, gotBody)
	}
}

// TestDoQueryRetryDoesNotRetryWithoutBody 钉住「拿不到 GetBody 就不重试」。
//
// 这条是安全边界：body 只能读一次的请求，重试会发出一个空包 —— 而空包在
// IPP 里是个合法但语义错误的请求，对端会正经回一个错误码。宁可如实报
// 第一次的错误，也不要发出一个我们无法解释的请求。
func TestDoQueryRetryDoesNotRetryWithoutBody(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("用例前提不成立：不支持 Hijack")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			return
		}
		_ = conn.Close()
	}))
	defer srv.Close()

	// NopCloser 不在 http.NewRequest 认识的那几种读取器里，因此 GetBody 为 nil。
	req, err := http.NewRequest(http.MethodPost, srv.URL, io.NopCloser(strings.NewReader("secret")))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	if req.GetBody != nil {
		t.Fatalf("用例前提不成立：这种 body 的 GetBody 应当为 nil，实际非空")
	}

	if _, err := doQueryRetry(newSafeClient(3*time.Second), req, 2); err == nil {
		t.Fatal("请求体不可重放时应当如实返回错误，而不是重发")
	}
	if n := atomic.LoadInt32(&attempts); n != 1 {
		t.Errorf("不应当重试，实际请求了 %d 次", n)
	}
}

// TestDoQueryRetryDoesNotLingerOnRefused 确认「对端拒连」这类错误是立刻
// 返回的，不会被重试拖成倍数的等待 —— 用户看到的是「打印机连不上」，
// 这句话没必要晚 5 秒才说出口。
func TestDoQueryRetryDoesNotLingerOnRefused(t *testing.T) {
	// 起一个监听再立刻关掉，拿到一个「有端口但没人听」的地址。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起监听失败: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/", bytes.NewBuffer([]byte("x")))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	start := time.Now()
	_, err = doQueryRetry(newSafeClient(5*time.Second), req, 2)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("连一个没人监听的端口应当报错")
	}
	if elapsed > time.Second {
		t.Errorf("拒连应当立刻返回，实际耗时 %v", elapsed)
	}
}
