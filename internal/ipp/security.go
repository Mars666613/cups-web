package ipp

import (
	"errors"
	"fmt"
	"io"
	"log"
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

	// queryTimeout 是【只读查询】的整体超时上限（取属性 / 列队列 / 列任务）。
	//
	// ── 为什么必须与 dialTimeout 分开 ──
	//
	// dialTimeout 的 15 秒是为【送印】定的：一个多页文档经 pdftopdf 过滤、
	// 拼成 IPP 请求体再上传，慢一点是正常的，掐早了会打成半截。
	//
	// 但只读查询完全是另一回事：它是几十字节的 IPP 报文，cupsd 在局域网里
	// 正常毫秒级返回（实测 2~14ms）。把 15 秒套在它身上，等于「一次查询卡住
	// 就让用户干等 15 秒」——而这个查询挂在【打印机选择】这种交互路径上，
	// 还带着一个 15 秒的前端轮询（见 PrintView 的 printerInfoTimer）。
	//
	// 实测故障（用户报的 502 Bad Gateway "failed to get printer info"）：
	// 请求在 1ms 内完成 TCP 连接，然后【一个字节都没回来】，恰好卡满 15.00 秒
	// 撞上客户端超时。那一次停顿发生在链路中间那一跳（开发机上 CUPS 跑在
	// WSL 里，Windows 走 wslrelay 转发；生产是单容器，没这一跳）。
	//
	// 取 5 秒：对正常响应是 500 倍的余量，对真的卡住则把代价从 15 秒压到 5 秒。
	// ⚠️ 5 秒 × 2 次尝试（见 doQueryRetry）仍小于原来的 15 秒，所以单次请求的
	// 最坏耗时只会变短，不会变长。
	queryTimeout = 5 * time.Second
)

// errSSRFBlocked 是拨号层按 SSRF 策略拒绝目标地址时的哨兵错误。
//
// 单独成一个哨兵是为了让重试逻辑能把它挑出来：被策略拒绝的地址重试一万次
// 也还是被拒绝，重试只会拖长响应时间并刷屏日志。
var errSSRFBlocked = errors.New("connection blocked by ssrf policy")

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
	if isAlwaysBlockedIP(ip) {
		return true
	}
	if blockPrivate {
		if ip.IsLoopback() || ip.IsPrivate() {
			return true
		}
	}
	return false
}

// isAlwaysBlockedIP 只看「任何情况下都不该连」的那几类：未指定 / link-local
// （169.254.0.0/16 含云厂商元数据、fe80::/10）/ 多播。
//
// 与 isBlockedIP 分开是因为两处策略不同：打印机策略可以被
// PRINTER_BLOCK_PRIVATE 收紧，而【告警通知】的出站不能跟着收紧 ——
// 见 NewNotifyClient。这一层拦的是「把通知发到 169.254.169.254」这种
// 在任何部署里都说不通的目标。
func isAlwaysBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || // 169.254.0.0/16（含 AWS/GCP/阿里云元数据）、fe80::/10
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsInterfaceLocalMulticast()
}

// dialControl 在 socket 实际连接前拿到解析后的目标地址，按真实 IP 校验。放在
// Control 回调里可确保即便 DNS 返回内网地址（rebinding）也会被拦截。
func dialControl(_, address string, _ syscall.RawConn) error {
	return checkDialAddress(address, isBlockedIP)
}

// dialControlNotify 是告警通知专用拨号校验：只拦「任何部署里都说不通的」地址。
func dialControlNotify(_, address string, _ syscall.RawConn) error {
	return checkDialAddress(address, isAlwaysBlockedIP)
}

func checkDialAddress(address string, blocked func(net.IP) bool) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid dial address")
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("could not resolve dial address to ip")
	}
	if blocked(ip) {
		return fmt.Errorf("%w: %s", errSSRFBlocked, ip)
	}
	return nil
}

// newSafeClient 构造一个带 SSRF 防护的 HTTP 客户端：连接层 IP 校验、禁止跟随
// 重定向、整体超时。timeout 为整个请求（含请求体上传）的超时上限。
//
// ⚠️ timeout 就是这次请求的硬上限，不要以为还有别的层兜着：下面的
// ResponseHeaderTimeout 也取同一个值（原先写死 dialTimeout，于是
// 「整体 2 秒的探测 + 表头等 15 秒」这种自相矛盾的组合是可能的 ——
// 整体超时先生效，所以没爆出来，但那是一条只能靠读代码发现的暗规则）。
func newSafeClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = dialTimeout
	}
	dialer := &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: 30 * time.Second,
		Control:   dialControl,
	}
	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   dialTimeout,
		ResponseHeaderTimeout: timeout,
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

// doQueryRetry 发一次【只读】IPP 查询，传输层失败时用新连接重试。
//
// ── 为什么只读查询可以重试，送印不行 ──
//
// 「取打印机属性」「列队列」「列任务」都是幂等的读：重复执行一万次，机器的
// 状态不变、纸不会多出来一张。而 SendPrintJob 是写 —— 超时 ≠ 没打出去，
// 重试会真的多印一份。所以这条路上限只给读。
//
// ── 重试解决的是什么 ──
//
// 实测的停顿形态是「TCP 连上了、对端一个字节都不回」，卡满超时才报错。
// 这种停顿常常是【单条连接】级别的（链路中间那一跳把某条连接丢了），
// 换一条新连接就通了 —— 所以重试值得做。
//
// ⚠️ 但它只是保险，不是根治：如果停的是整条链路，重试同样会卡住，
// 代价就是 queryTimeout 的 2 倍。因此 attempts 默认 2，且两个上限都在
// queryTimeout 量级。
func doQueryRetry(client *http.Client, req *http.Request, attempts int) (*http.Response, error) {
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		attempt := req
		if i > 0 {
			// 重试必须重放请求体：bytes.Buffer 建的请求自带 GetBody，
			// 可以直接复制出一份新的。拿不到 GetBody 就别重试了 ——
			// 那意味着这个请求体只能读一次，重试会发出一个空包。
			if req.GetBody == nil {
				return nil, lastErr
			}
			body, err := req.GetBody()
			if err != nil {
				return nil, lastErr
			}
			attempt = req.Clone(req.Context())
			attempt.Body = body
		}
		resp, err := client.Do(attempt)
		if err == nil {
			if i > 0 {
				log.Printf("[ipp] 查询重试第 %d 次成功: %s %s", i+1, req.Method, req.URL)
			}
			return resp, nil
		}
		lastErr = err
		if resp != nil {
			_ = resp.Body.Close()
		}
		// 上下文已取消（用户关了页面 / 服务端在收尾）就别再试了。
		if attempt.Context().Err() != nil || !retryableQueryErr(err) {
			break
		}
		log.Printf("[ipp] 查询失败，准备重试 (%d/%d): %s %s: %v",
			i+1, attempts, req.Method, req.URL, err)
	}
	return nil, lastErr
}

// retryableQueryErr 判断一个传输层错误值不值得换条连接重试。
//
// 按【拒绝重试】来写而不是按【允许重试】：只有明确是「连接层面出了问题」的
// 才重试，其余一律直接返回。这样将来新出现一类错误时，默认行为是「不重试」
// （快、可预测），而不是「悄悄多打一次」（慢、且可能放大故障）。
func retryableQueryErr(err error) bool {
	if err == nil {
		return false
	}
	// 被 SSRF 策略拒绝：同一个地址再拨多少次都一样。
	if errors.Is(err, errSSRFBlocked) {
		return false
	}
	// 对端拒连：重试不会让它变得可连，只会让「打印机没开这个端口」这句话
	// 晚 5 秒才说出口。
	if errors.Is(err, syscall.ECONNREFUSED) {
		return false
	}
	// 域名解析不了：同上。
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return false
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "timeout"),
		strings.Contains(s, "deadline exceeded"),
		strings.Contains(s, "EOF"),
		strings.Contains(s, "connection reset"),
		strings.Contains(s, "broken pipe"):
		return true
	}
	return false
}

// NewNotifyClient 构造一个用于【服务端主动外发通知】的 HTTP 客户端。
//
// 与 newSafeClient 的差别只有一处：它【不】吃 PRINTER_HOST_ALLOWLIST 与
// PRINTER_BLOCK_PRIVATE。
//
// ── 为什么这两条策略不该牵连通知 ──
//
// 那两条变量约束的是【登录用户传入的打印机地址】（用户能在打印页任意填 URI，
// 所以要在连接层挡内网探测）。通知地址是管理员在系统设置里配的，而且告警最
// 需要送达的恰恰是内网机器人 —— 企业微信/钉钉的自建应用、公司内网的转发服务
// 都在私有网段。让两者共用一份策略，就会出现「为了收紧打印机策略，顺手把
// 告警一起打瞎了」，而表现是【告警静默失效】—— 最难发现的一种故障。
//
// 保留的仍然是实打实的防护：连接层按解析后的真实 IP 拦 link-local（含云元
// 数据）/ 组播 / 未指定、禁止跟随重定向、整体超时。这些拦的目标在任何部署里
// 都说不通，不会影响正常的内网机器人。
//
// ⚠️ 它【不是】给打印机用的。打印机 URI 走 newSafeClient。
func NewNotifyClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = dialTimeout
	}
	dialer := &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: 30 * time.Second,
		Control:   dialControlNotify,
	}
	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   dialTimeout,
		ResponseHeaderTimeout: timeout,
		ExpectContinueTimeout: 1 * time.Second,
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		// 与打印机客户端同一条理由：webhook 若回了 302，跟着跳过去就等于
		// 把「地址是不是我们的内网机器人」这个判断交给对方。
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// limitedBody 返回一个受大小限制的响应体读取器，防止超大响应耗尽内存。
func limitedBody(r io.Reader) io.Reader {
	return io.LimitReader(r, maxIPPResponseBytes)
}
