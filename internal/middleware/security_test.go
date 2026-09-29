package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestProxyOriginAllowed 锁死 issue #99 的修复边界：反向代理没把 Host 改写成对外域名
// 时（nginx 不写 proxy_set_header Host $host 的默认行为），标准库会因 Origin != r.Host
// 把所有 POST 判成跨源。补的 X-Forwarded-Host 比对必须救回这类合法请求，同时不能给
// 浏览器已明确标记为跨源的请求开后门。
func TestProxyOriginAllowed(t *testing.T) {
	cases := []struct {
		name         string
		method       string
		host         string
		origin       string
		secFetchSite string
		fwdHost      string
		want         bool
	}{
		{
			name: "反代未改写 Host，XFH 与 Origin 一致：放行",
			host: "localhost:1180", origin: "http://print.example.com",
			fwdHost: "print.example.com", want: true,
		},
		{
			name: "外部端口非默认，Origin 带端口而 XFH 不带：按主机名放行",
			host: "localhost:1180", origin: "http://print.example.com:8080",
			fwdHost: "print.example.com", want: true,
		},
		{
			name: "https 对外，XFH 一致：放行",
			host: "localhost:1180", origin: "https://print.example.com",
			fwdHost: "print.example.com", want: true,
		},
		{
			name: "XFH 多级代理链，取第一跳",
			host: "localhost:1180", origin: "http://print.example.com",
			fwdHost: "print.example.com, inner.internal", want: true,
		},
		{
			name: "大小写不同的主机名：放行",
			host: "localhost:1180", origin: "http://Print.Example.com",
			fwdHost: "print.example.com", want: true,
		},
		{
			name: "XFH 与 Origin 不一致：不放行",
			host: "localhost:1180", origin: "http://evil.com",
			fwdHost: "print.example.com", want: false,
		},
		{
			name: "代理未设 XFH：无从判定，不放行",
			host: "localhost:1180", origin: "http://print.example.com",
			want: false,
		},
		{
			name: "浏览器报 cross-site，即便 XFH 匹配也不放行",
			host: "print.example.com", origin: "http://evil.com",
			secFetchSite: "cross-site", fwdHost: "evil.com", want: false,
		},
		{
			name: "浏览器报 same-site（http/https 混用），交回标准库判定",
			host: "print.example.com", origin: "http://print.example.com",
			secFetchSite: "same-site", fwdHost: "print.example.com", want: false,
		},
		{
			name: "浏览器报 same-origin：标准库已会放行，此处不短路",
			host: "print.example.com", origin: "http://print.example.com",
			secFetchSite: "same-origin", fwdHost: "print.example.com", want: false,
		},
		{
			name: "无 Origin：标准库 fail-open，此处不短路",
			host: "localhost:1180", fwdHost: "print.example.com", want: false,
		},
		{
			name:   "安全方法由标准库放行，此处不短路",
			method: http.MethodGet, host: "localhost:1180",
			origin: "http://print.example.com", fwdHost: "print.example.com", want: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			method := c.method
			if method == "" {
				method = http.MethodPost
			}
			r := httptest.NewRequest(method, "/api/login", nil)
			r.Host = c.host
			if c.origin != "" {
				r.Header.Set("Origin", c.origin)
			}
			if c.secFetchSite != "" {
				r.Header.Set("Sec-Fetch-Site", c.secFetchSite)
			}
			if c.fwdHost != "" {
				r.Header.Set("X-Forwarded-Host", c.fwdHost)
			}
			if got := proxyOriginAllowed(r); got != c.want {
				t.Fatalf("proxyOriginAllowed = %v, want %v", got, c.want)
			}
		})
	}
}

func TestStripPort(t *testing.T) {
	cases := map[string]string{
		"print.example.com":      "print.example.com",
		"print.example.com:8080": "print.example.com",
		"192.168.1.10:1180":      "192.168.1.10",
		"[2001:db8::1]:1180":     "[2001:db8::1]",
		"[2001:db8::1]":          "[2001:db8::1]",
		"":                       "",
	}
	for in, want := range cases {
		if got := stripPort(in); got != want {
			t.Errorf("stripPort(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCrossOriginDenyIsJSON 前端把「响应不是合法 JSON」的登录失败一律兜底显示成
// 「用户名或密码错误」，所以跨源拒绝必须是 JSON —— 否则反代配错会被谎报成密码错误。
func TestCrossOriginDenyIsJSON(t *testing.T) {
	h := CrossOriginProtection()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("请求本应被跨源防护拦下")
	}))

	r := httptest.NewRequest(http.MethodPost, "/api/login", nil)
	r.Host = "print.example.com"
	r.Header.Set("Origin", "http://evil.com")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("状态码 = %d, want %d", w.Code, http.StatusForbidden)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q, 必须是 JSON", ct)
	}
	if body := w.Body.String(); !strings.Contains(body, `"code":"cross_origin_blocked"`) {
		t.Fatalf("响应体缺少可诊断的 code 字段: %s", body)
	}
}
