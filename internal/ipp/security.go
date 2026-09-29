package ipp

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

// 出站请求安全加固（防 SSRF，安全审查 H-1）。
//
// 打印机地址（printer URI）由前端已登录用户传入，服务端会向其发起 IPP/HTTP
// 请求。若不加约束，任何登录用户都能借服务端探测内网、访问云厂商元数据端点
// （169.254.169.254）等。本文件提供统一的安全 HTTP 客户端与 URI 校验：
//
//   - scheme 白名单：仅允许 http/https/ipp/ipps
//   - Dialer.Control 在 socket 连接层按解析后的真实 IP 校验，可防 DNS rebinding
//   - 默认拒绝 link-local（含云元数据 169.254.0.0/16、fe80::/10）、未指定、多播地址
//   - CheckRedirect 禁止跟随重定向（避免校验被 302 绕过）
//   - 响应体 io.LimitReader 限制大小，避免恶意目标返回超大响应耗尽内存
//
// CUPS 打印机通常就在内网，因此环回与私有网段默认放行，避免打断正常打印；
// 部署者可通过环境变量进一步收紧：
//
//   - PRINTER_HOST_ALLOWLIST：逗号分隔的主机名白名单，设置后只允许列表内主机
//   - PRINTER_BLOCK_PRIVATE=true：额外拒绝环回与私有网段（RFC1918 等）

const (
	// maxIPPResponseBytes 限制单次 IPP/HTTP 响应体读取上限，防止内存耗尽 DoS。
	maxIPPResponseBytes = 16 << 20 // 16 MiB

	// dialTimeout 为连接层超时；查询类与打印类请求的整体超时另行设置。
	dialTimeout = 15 * time.Second
)

var allowedSchemes = map[string]bool{
	"http":  true,
	"https": true,
	"ipp":   true,
	"ipps":  true,
}

var (
	secOnce       sync.Once
	hostAllowlist map[string]bool
	blockPrivate  bool
)

func loadSecConfig() {
	secOnce.Do(func() {
		hostAllowlist = map[string]bool{}
		if v := strings.TrimSpace(os.Getenv("PRINTER_HOST_ALLOWLIST")); v != "" {
			for h := range strings.SplitSeq(v, ",") {
				h = strings.ToLower(strings.TrimSpace(h))
				if h != "" {
					hostAllowlist[h] = true
				}
			}
		}
		blockPrivate = strings.EqualFold(strings.TrimSpace(os.Getenv("PRINTER_BLOCK_PRIVATE")), "true")
	})
}

// validatePrinterURI 校验用户传入的打印机 URI 的 scheme 与主机名。IP 层的校验
// 在连接时由 dialControl 完成（防 DNS rebinding），此处只做 scheme / 主机名 /
// 可选白名单的快速拒绝。
func validatePrinterURI(raw string) error {
	loadSecConfig()
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("invalid printer uri")
	}
	if !allowedSchemes[strings.ToLower(u.Scheme)] {
		return fmt.Errorf("unsupported printer uri scheme")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("printer uri missing host")
	}
	if len(hostAllowlist) > 0 && !hostAllowlist[strings.ToLower(host)] {
		return fmt.Errorf("printer host not allowed")
	}
	// 若 host 本身就是 IP 字面量，提前校验一次（连接层还会再校验一遍）。
	if ip := net.ParseIP(host); ip != nil && isBlockedIP(ip) {
		return fmt.Errorf("printer host address not allowed")
	}
	return nil
}

// isBlockedIP 判断目标 IP 是否应被拒绝。默认拒绝未指定 / link-local（含云元数据）/
// 多播地址；当 PRINTER_BLOCK_PRIVATE=true 时额外拒绝环回与私有网段。
func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || // 169.254.0.0/16（含 AWS/GCP/阿里云元数据）、fe80::/10
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsInterfaceLocalMulticast() {
		return true
	}
	if blockPrivate {
		if ip.IsLoopback() || ip.IsPrivate() {
			return true
		}
	}
	return false
}

// dialControl 在 socket 实际连接前拿到解析后的目标地址，按真实 IP 校验。放在
// Control 回调里可确保即便 DNS 返回内网地址（rebinding）也会被拦截。
func dialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid dial address")
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("could not resolve dial address to ip")
	}
	if isBlockedIP(ip) {
		return fmt.Errorf("connection to %s blocked by ssrf policy", ip)
	}
	return nil
}

// newSafeClient 构造一个带 SSRF 防护的 HTTP 客户端：连接层 IP 校验、禁止跟随
// 重定向、整体超时。timeout 为整个请求（含请求体上传）的超时上限。
func newSafeClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: 30 * time.Second,
		Control:   dialControl,
	}
	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   dialTimeout,
		ResponseHeaderTimeout: dialTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// limitedBody 返回一个受大小限制的响应体读取器，防止超大响应耗尽内存。
func limitedBody(r io.Reader) io.Reader {
	return io.LimitReader(r, maxIPPResponseBytes)
}
