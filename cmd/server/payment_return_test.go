package main

// 手机网站支付的「回跳地址」相关的纯函数测试。
//
// 这一段是把用户【付完款之后】送回到哪儿去，两个方向都会出事：
//
//   送错了地方 → 用户被扔到别人的站上（开放重定向），而那个页面顶着的
//                正是「支付成功」的语境，是钓鱼的完美入口
//   送不回来   → 用户付完停在支付宝的结果页上，还得自己找我们的页面，
//                而这正是改用官方方案要解决的问题本身
//
// 所以 sanitizeReturnPath 与 requestOrigin 都按「宁可退回默认页，也不放行
// 可疑输入」来测。

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSanitizeReturnPath(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"站内普通路径放行", "/print", "/print"},
		{"钱包页放行", "/wallet", "/wallet"},
		{"带子路径放行", "/admin/orders", "/admin/orders"},
		{"前后空白会被裁掉", "  /print  ", "/print"},

		// 以下全是「必须退回默认页」的输入。
		{"空串", "", defaultReturnPath},
		{"只有空白", "   ", defaultReturnPath},
		{"相对路径", "print", defaultReturnPath},
		{"协议相对地址", "//evil.com", defaultReturnPath},
		{"协议相对地址带路径", "//evil.com/phish", defaultReturnPath},
		{"绝对地址", "https://evil.com", defaultReturnPath},
		{"http 绝对地址", "http://evil.com/x", defaultReturnPath},
		{"反斜杠绕过的协议相对地址", `/\evil.com`, defaultReturnPath},
		{"开头就是反斜杠", `\/evil.com`, defaultReturnPath},
		{"反斜杠变体", `\\evil.com`, defaultReturnPath},
		{"javascript 伪协议", "javascript:alert(1)", defaultReturnPath},
		{"data 伪协议", "data:text/html,x", defaultReturnPath},
		// 带 ? 或 # 会让拼出来的地址坏掉：渠道还会再接一个 ?out_trade_no=…，
		// 多一个 ? 就把单号吞进上一个参数的值里，回跳处理再也读不到。
		{"自带查询串", "/print?x=1", defaultReturnPath},
		{"自带 hash", "/print#frag", defaultReturnPath},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sanitizeReturnPath(c.in); got != c.want {
				t.Errorf("sanitizeReturnPath(%q) = %q，期望 %q", c.in, got, c.want)
			}
		})
	}
}

func TestRequestOrigin(t *testing.T) {
	cases := []struct {
		name    string
		host    string
		headers map[string]string
		tls     bool
		want    string
	}{
		{
			name: "直连 HTTP：用 r.Host",
			host: "192.168.1.22:1180",
			want: "http://192.168.1.22:1180",
		},
		{
			name: "直连 HTTPS：看 r.TLS",
			host: "print.example.com",
			tls:  true,
			want: "https://print.example.com",
		},
		{
			// 反代的常见形态：r.Host 是上游地址，对外主机名与协议都靠 XFH/XFP。
			name: "反代上报对外主机名与协议",
			host: "upstream:8080",
			headers: map[string]string{
				"X-Forwarded-Host":  "print.example.com",
				"X-Forwarded-Proto": "https",
			},
			want: "https://print.example.com",
		},
		{
			// 只给了主机名、没说协议：只能按 http 猜（本项目的典型部署就是
			// 内网明文，这个结果是合理的）。
			name:    "只给主机名时按 http",
			host:    "upstream:8080",
			headers: map[string]string{"X-Forwarded-Host": "192.168.1.22:1180"},
			want:    "http://192.168.1.22:1180",
		},
		{
			name:    "TLS 卸载在反代上",
			host:    "192.168.1.22:1180",
			headers: map[string]string{"X-Forwarded-Proto": "https"},
			want:    "https://192.168.1.22:1180",
		},
		{
			// 多级代理下是逗号分隔，最左边那跳才是客户端直连的那一层。
			name: "多级代理取第一跳",
			host: "last:80",
			headers: map[string]string{
				"X-Forwarded-Host":  "print.example.com, inner:80",
				"X-Forwarded-Proto": "https, http",
			},
			want: "https://print.example.com",
		},
		{
			// 头里的值是攻击者可控的，认不出来的协议名一律退回 http，
			// 不能让一个 "javascript" 之类的字符串进到地址里。
			name:    "无法识别的协议名退回 http",
			host:    "h:1",
			headers: map[string]string{"X-Forwarded-Proto": "gopher"},
			want:    "http://h:1",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.Host = c.host
			for k, v := range c.headers {
				r.Header.Set(k, v)
			}
			if c.tls {
				r.TLS = &tls.ConnectionState{}
			}
			if got := requestOrigin(r); got != c.want {
				t.Errorf("requestOrigin() = %q，期望 %q", got, c.want)
			}
		})
	}

	t.Run("nil 请求不炸", func(t *testing.T) {
		if got := requestOrigin(nil); got != "" {
			t.Errorf("requestOrigin(nil) = %q，期望空串", got)
		}
	})

	t.Run("Host 为空时返回空串", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = ""
		if got := requestOrigin(r); got != "" {
			t.Errorf("Host 为空时 = %q，期望空串（调用方据此降级成二维码）", got)
		}
	})
}
