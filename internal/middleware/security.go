package middleware

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// CrossOriginProtection 返回 Go 1.25 内置的 CSRF 防护中间件。它基于浏览器自
// 2023 年起普遍发送的 Sec-Fetch-Site 头（或 Origin 与 Host 比对）拒绝跨源的非
// 安全请求（POST/PUT/DELETE 等），作为 double-submit cookie（见 csrf.go）之外
// 的纵深防御。安全方法（GET/HEAD/OPTIONS）与非浏览器客户端（无 Sec-Fetch-Site）
// 会被放行，因此不影响正常的同源前端与 API 调用。
//
// 若前后端在不同源部署（例如本地开发把 Vite :5173 直连后端，而非走代理），
// 可用环境变量 TRUSTED_ORIGINS（逗号分隔，形如 http://localhost:5173）声明可信源。
//
// 反向代理适配（Issue #99）：标准库在 Sec-Fetch-Site 缺失时退化为「Origin 的 host
// 是否等于 r.Host」。反代场景下 r.Host 往往不是用户在浏览器里输入的域名——nginx
// 不写 proxy_set_header Host $host 时默认发的是 $proxy_host（形如 localhost:1180），
// 于是 Origin 与 Host 天然不等，整站所有 POST 被 403 拒掉。这里在标准库之前补一次
// X-Forwarded-Host 比对来消掉这个假阳性，详见 proxyOriginAllowed 的论证。
func CrossOriginProtection() func(http.Handler) http.Handler {
	cop := http.NewCrossOriginProtection()
	if v := strings.TrimSpace(os.Getenv("TRUSTED_ORIGINS")); v != "" {
		for o := range strings.SplitSeq(v, ",") {
			if o = strings.TrimSpace(o); o != "" {
				_ = cop.AddTrustedOrigin(o)
			}
		}
	}
	cop.SetDenyHandler(http.HandlerFunc(denyCrossOrigin))
	return func(next http.Handler) http.Handler {
		guarded := cop.Handler(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if proxyOriginAllowed(r) {
				next.ServeHTTP(w, r)
				return
			}
			guarded.ServeHTTP(w, r)
		})
	}
}

// proxyOriginAllowed 报告该请求是否属于「反代改写了 Host，但 Origin 与代理自报的
// 对外主机名一致」这一类同源请求。
//
// 为什么默认信任 X-Forwarded-Host 不削弱防护：本函数只在 Sec-Fetch-Site **缺失**
// 时才生效。真实浏览器自 2023 年起都会发送该头，跨站请求会被标记成 cross-site /
// same-site 并在标准库那一层直接拒掉，走不到这里；而能自由伪造 X-Forwarded-Host
// 的客户端（curl、脚本）本来就能通过「既不发 Sec-Fetch-Site 也不发 Origin」让标准
// 库 fail-open 放行。也就是说这条放行不给攻击者提供任何新的通路，只是把反代下的
// 合法请求从假阳性里救出来。
func proxyOriginAllowed(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		// 安全方法由标准库直接放行，无需在此短路。
		return false
	}
	// 浏览器明确报告了跨源属性时一律交给标准库判定，不放宽。
	if r.Header.Get("Sec-Fetch-Site") != "" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	o, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return sameHost(o.Host, forwardedHost(r))
}

// forwardedHost 取代理自报的对外主机名。X-Forwarded-Host 在多级代理下是逗号分隔
// 的链，第一跳才是最初的客户端请求头。nginx / caddy / traefik 都设置该头。
func forwardedHost(r *http.Request) string {
	v := r.Header.Get("X-Forwarded-Host")
	if v == "" {
		return ""
	}
	first, _, _ := strings.Cut(v, ",")
	return strings.TrimSpace(first)
}

// sameHost 比对两个 host。先严格比对（含端口），再退回只比主机名：代理链里端口信息
// 极易丢失（nginx 的 $host 不含端口，而浏览器 Origin 在非默认端口下一定带端口），
// 若要求端口也一致会重新引入假阳性。
func sameHost(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if strings.EqualFold(a, b) {
		return true
	}
	ha, hb := stripPort(a), stripPort(b)
	return ha != "" && strings.EqualFold(ha, hb)
}

func stripPort(host string) string {
	// 不能用 net.SplitHostPort：无端口时它会报错，且 IPv6 字面量需要保留方括号语义。
	if strings.HasPrefix(host, "[") {
		if end := strings.LastIndex(host, "]"); end >= 0 {
			return host[:end+1]
		}
		return host
	}
	if i := strings.LastIndex(host, ":"); i >= 0 {
		return host[:i]
	}
	return host
}

// denyCrossOrigin 替换标准库默认的 text/plain 403。
//
// 为什么必须换成 JSON：前端把「响应不是合法 JSON」的失败一律兜底显示成「用户名或
// 密码错误」，于是反代配错时用户看到的是密码错误，排查方向被彻底带偏（Issue #99）。
// 这里返回结构化 JSON 并把真实原因写进 error 字段，同时打一条含全部判定依据的日志。
func denyCrossOrigin(w http.ResponseWriter, r *http.Request) {
	secFetchSite := r.Header.Get("Sec-Fetch-Site")
	origin := r.Header.Get("Origin")
	fwdHost := r.Header.Get("X-Forwarded-Host")

	log.Printf("[cross-origin] 拒绝请求: method=%s path=%s host=%q origin=%q sec-fetch-site=%q x-forwarded-host=%q x-forwarded-proto=%q",
		r.Method, r.URL.Path, r.Host, origin, secFetchSite, fwdHost, r.Header.Get("X-Forwarded-Proto"))

	var hint string
	switch secFetchSite {
	case "":
		hint = "跨源请求被拒绝：请求的 Origin 与服务端看到的 Host 不一致。" +
			"若你使用了反向代理，请让它转发真实域名（nginx：proxy_set_header Host $host; " +
			"proxy_set_header X-Forwarded-Host $host;），或用环境变量 TRUSTED_ORIGINS 声明访问地址。"
	default:
		hint = "跨源请求被拒绝：浏览器判定本次请求跨源（Sec-Fetch-Site: " + secFetchSite + "）。" +
			"常见原因是页面与接口的协议或域名不一致（例如页面走 http 而接口被升级到 https、" +
			"或经由不同子域访问）。请始终用同一个协议加域名访问，或用环境变量 TRUSTED_ORIGINS 声明访问地址。"
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":  hint,
		"code":   "cross_origin_blocked",
		"origin": origin,
		"host":   r.Host,
	})
}

// SecurityHeaders 为所有响应加上一组基础安全响应头。
//
// CSP 说明：本项目是同源 SPA。前端 pdf.js 依赖 Web Worker（blob:）与 wasm，
// Vite/Nuxt UI/Tailwind 会注入 inline script/style，故 script-src/style-src 放宽
// 到 'unsafe-inline' 'unsafe-eval' 'wasm-unsafe-eval' blob: 以避免打断功能；但仍
// 锁死 object-src、base-uri 与 frame-ancestors，收敛点击劫持、插件注入与 <base>
// 篡改等向量。若后续前端收敛了 inline 用法，可进一步收紧 script-src。
//
// connect-src 必须包含 blob:：PdfCanvas 把转换/标准化后的 PDF 存成 blob: URL 后交给
// pdf.js 的 getDocument({url}) 渲染预览，pdf.js 内部用 fetch 拉取该 URL，受 connect-src
// 管控——漏掉 blob: 会导致所有 PDF 预览「加载失败」（图片走 img-src 不受影响，见 Issue #86）。
func SecurityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; " +
		"script-src 'self' 'unsafe-inline' 'unsafe-eval' 'wasm-unsafe-eval' blob:; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: blob:; " +
		"font-src 'self' data:; " +
		"worker-src 'self' blob:; " +
		"connect-src 'self' blob:; " +
		"object-src 'none'; " +
		"base-uri 'self'; " +
		"frame-ancestors 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", csp)
		next.ServeHTTP(w, r)
	})
}
