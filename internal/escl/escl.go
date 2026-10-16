package escl

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// 常用的色彩模式 / 输入源取值。全部来自 eSCL 规范的枚举，
// 实际可用集合以 ScannerCapabilities 为准。
const (
	ColorModeBnW1  = "BlackAndWhite1"
	ColorModeGray8 = "Grayscale8"
	ColorModeRGB24 = "RGB24"

	SourcePlaten  = "Platen" // 玻璃板，一次一页
	SourceADF     = "ADF"    // 进稿器单面
	SourceADFDupx = "Duplex" // 进稿器双面（eSCL 用 InputSource=Platen + Duplex=true 的也有；
	// Ricoh 的实现是 AdfOption=Duplex 时用单独的能力块，任务设置里 InputSource=ADF + Duplex=true）
)

// Client 是一台 eSCL 扫描设备的客户端。
// BaseURL 形如 http://192.168.1.100/eSCL（以 /eSCL 结尾，不带尾斜杠）。
type Client struct {
	BaseURL string
	HTTP    *http.Client

	// pollInterval 覆盖状态/取件轮询的间隔，仅测试使用（0 = 默认 1 秒）。
	// 用例要模拟「设备扫了 5 秒才出件」，真等 5 秒太慢。
	pollInterval time.Duration
}

// NewClient 从打印机 URI 推导 eSCL 端点。
//
// CUPS 里的队列地址形如 ipp://192.168.1.100/ipp/print —— 主机部分就是
// 打印机本体，eSCL 服务也开在同一台主机的 80/443 上（少数厂商用非标端口，
// 如 Kyocera 9090；那类需要显式配置，这里不做发现协议）。
// ipp→http、ipps→https；path 一律丢弃，统一走 /eSCL。
func NewClient(printerURI string) *Client {
	base := EsclBaseFromPrinterURI(printerURI)
	if base == "" {
		return nil
	}
	return &Client{BaseURL: base, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// EsclBaseFromPrinterURI 把打印机 URI 换算成 eSCL 根地址。
// 无法解析时返回空串。
func EsclBaseFromPrinterURI(printerURI string) string {
	printerURI = strings.TrimSpace(printerURI)
	if printerURI == "" {
		return ""
	}
	scheme := "http"
	lower := strings.ToLower(printerURI)
	switch {
	case strings.HasPrefix(lower, "ipp://"):
		printerURI = "http://" + printerURI[len("ipp://"):]
	case strings.HasPrefix(lower, "ipps://"):
		printerURI = "https://" + printerURI[len("ipps://"):]
		scheme = "https"
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		scheme = ""
	default:
		// usb:// 等本地连接的队列没有网络主机，无从探测 eSCL。
		return ""
	}
	u, err := url.Parse(printerURI)
	if err != nil || u.Host == "" {
		return ""
	}
	// http/https 之外的自定义端口很少见；ipp 的 631 端口上跑的往往是
	// IPP over TLS 而不是 eSCL，所以端口只在显式写了且不是 631 时才保留。
	host := u.Hostname()
	port := u.Port()
	if port != "" && port != "631" {
		host = host + ":" + port
	}
	if scheme != "" {
		return scheme + "://" + host + "/eSCL"
	}
	// 原生 http(s) 输入：保留原协议。
	if strings.HasPrefix(strings.ToLower(printerURI), "https://") {
		return "https://" + host + "/eSCL"
	}
	return "http://" + host + "/eSCL"
}

// ─── 能力与状态 ────────────────────────────────────────────────────────

// Capabilities 是应用关心的扫描能力子集（完整 XML 大得多，其余用不到）。
type Capabilities struct {
	MakeAndModel string
	HasPlaten    bool
	HasAdf       bool
	AdfDuplex    bool
	Color        bool // 支持 RGB24（彩色扫描）
	Formats      []string
	// MaxWidth/MaxHeight 是【玻璃板】可采区域的上限（1/300 英寸，全幅）。
	MaxWidth  int
	MaxHeight int
	// AdfMaxWidth/AdfMaxHeight 是进稿器走纸通道的幅面上限（1/300 英寸）：
	// 前者横跨走纸方向，后者沿走纸方向。缺省 0 表示设备没上报。
	AdfMaxWidth  int
	AdfMaxHeight int
}

// Status 是扫描仪当前状态。
type Status struct {
	State        string // Idle / Processing / Down（pwg:State）
	AdfState     string // ScannerAdfLoaded / ScannerAdfEmpty / ScannerAdfProcessing
	StateReasons []string
}

// Idle 返回扫描仪是否空闲可扫。AttentionRequired 之类的 reason 不拦
// —— 打印机常年在管理界面挂着各种软告警，以此为由拒绝复印会把功能锁死。
func (s Status) Idle() bool { return strings.EqualFold(s.State, "Idle") }

// AdfLoaded 报告进稿器里是否检测到原件。
func (s Status) AdfLoaded() bool {
	switch {
	case strings.EqualFold(s.AdfState, "ScannerAdfLoaded"),
		strings.EqualFold(s.AdfState, "ScannerAdfProcessing"):
		return true
	}
	return false
}

// AdfEmpty 报告设备是否【明确】表示进稿器是空的。
//
// 与「不知道」严格区分：AdfState 为空串（设备压根不上报）、或报了
// 卡纸/门开之类与装载状态无关的取值时都返回 false。调用方据此拒绝
// 一次注定失败的任务是安全的，但绝不能把「未知」当成「空」。
func (s Status) AdfEmpty() bool {
	return strings.EqualFold(strings.TrimSpace(s.AdfState), "ScannerAdfEmpty")
}

type scannerCapabilitiesXML struct {
	MakeAndModel string `xml:"MakeAndModel"`
	Platen       *struct {
		Caps platenCaps `xml:"PlatenInputCaps"`
	} `xml:"Platen"`
	Adf *struct {
		Simplex *adfCaps `xml:"AdfSimplexInputCaps"`
		Duplex  *adfCaps `xml:"AdfDuplexInputCaps"`
		Option  string   `xml:"AdfOption"`
	} `xml:"Adf"`
}

// adfCaps 只取走纸通道的幅面上限：MaxWidth 是横跨走纸方向的上限，
// MaxHeight 是沿走纸方向的上限。它们是判断「一张纸该怎么摆」的依据，
// 见 cmd/server/copy_scan_region.go。
type adfCaps struct {
	MaxWidth  int `xml:"MaxWidth"`
	MaxHeight int `xml:"MaxHeight"`
}

type platenCaps struct {
	MaxWidth  int `xml:"MaxPhysicalWidth"`
	MaxHeight int `xml:"MaxPhysicalHeight"`
	Profile   struct {
		ColorModes []string `xml:"ColorModes>ColorMode"`
		Formats    []string `xml:"DocumentFormats>DocumentFormat"`
		FormatsExt []string `xml:"DocumentFormats>DocumentFormatExt"`
	} `xml:"SettingProfiles>SettingProfile"`
}

// Capabilities 查询能力。失败返回错误（设备不支持 eSCL 时通常是连接拒绝或 404）。
func (c *Client) Capabilities(ctx context.Context) (*Capabilities, error) {
	body, err := c.get(ctx, "/ScannerCapabilities")
	if err != nil {
		return nil, err
	}
	var raw scannerCapabilitiesXML
	if err := xml.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("解析 ScannerCapabilities: %w", err)
	}
	caps := &Capabilities{MakeAndModel: strings.TrimSpace(raw.MakeAndModel)}
	if raw.Platen != nil {
		caps.HasPlaten = true
		caps.MaxWidth = raw.Platen.Caps.MaxWidth
		caps.MaxHeight = raw.Platen.Caps.MaxHeight
		seenFormat := map[string]bool{}
		for _, f := range raw.Platen.Caps.Profile.Formats {
			if !seenFormat[f] {
				seenFormat[f] = true
				caps.Formats = append(caps.Formats, f)
			}
		}
		for _, f := range raw.Platen.Caps.Profile.FormatsExt {
			if !seenFormat[f] {
				seenFormat[f] = true
				caps.Formats = append(caps.Formats, f)
			}
		}
		for _, m := range raw.Platen.Caps.Profile.ColorModes {
			if m == ColorModeRGB24 {
				caps.Color = true
			}
		}
	}
	if raw.Adf != nil {
		caps.HasAdf = raw.Adf.Simplex != nil || raw.Adf.Duplex != nil
		caps.AdfDuplex = raw.Adf.Duplex != nil
		// 幅面取单面通道的；只有双面通道时用它的。两者在本机一致。
		dims := raw.Adf.Simplex
		if dims == nil {
			dims = raw.Adf.Duplex
		}
		if dims != nil {
			caps.AdfMaxWidth = dims.MaxWidth
			caps.AdfMaxHeight = dims.MaxHeight
		}
	}
	return caps, nil
}

// Status 查询状态。
func (c *Client) Status(ctx context.Context) (*Status, error) {
	body, err := c.get(ctx, "/ScannerStatus")
	if err != nil {
		return nil, err
	}
	var raw struct {
		State        string   `xml:"State"`
		StateReasons []string `xml:"StateReasons"`
		AdfState     string   `xml:"AdfState"`
	}
	if err := xml.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("解析 ScannerStatus: %w", err)
	}
	return &Status{State: strings.TrimSpace(raw.State), AdfState: strings.TrimSpace(raw.AdfState), StateReasons: raw.StateReasons}, nil
}

// ─── 扫描 ──────────────────────────────────────────────────────────────

// ScanSettings 描述一次扫描请求。
type ScanSettings struct {
	InputSource string // SourcePlaten / SourceADF / SourceADFDupx
	ColorMode   string // ColorMode* 常量
	Duplex      bool   // 仅 ADF 有效
	Format      string // 通常 application/pdf
	XResolution int    // dpi
	YResolution int    // dpi
	// Width/Height 是扫描区域（1/300 英寸）。0 表示用设备能力里的最大幅面。
	Width, Height int
	// ImagesToTransfer：0 = ADF 吃到空；1 = 只扫一页（玻璃板固定为 1）。
	ImagesToTransfer int
}

// scanSettingsXML 手工拼扫描设置。命名空间挂在根元素上，与真机实测的
// 报文一致（Ricoh Pro C5200S 接受该格式；这也是 sane-airscan 发的同款）。
func (s ScanSettings) scanSettingsXML() string {
	width, height := s.Width, s.Height
	if width <= 0 {
		width = 3508 // A4 纵向兜底（297mm 宽），设备会自行裁剪到实际幅面
	}
	if height <= 0 {
		height = 4200
	}
	source := s.InputSource
	if source == "" {
		source = SourcePlaten
	}
	color := s.ColorMode
	if color == "" {
		color = ColorModeGray8
	}
	format := s.Format
	if format == "" {
		format = "application/pdf"
	}
	xres, yres := s.XResolution, s.YResolution
	if xres <= 0 {
		xres = 300
	}
	if yres <= 0 {
		yres = 300
	}
	images := s.ImagesToTransfer
	if source == SourcePlaten {
		images = 1
	}

	var b strings.Builder
	b.WriteString(`<scan:ScanSettings xmlns:scan="http://schemas.hp.com/imaging/escl/2011/05/03" xmlns:pwg="http://www.pwg.org/schemas/2010/12/sm">`)
	b.WriteString(`<pwg:Version>2.0</pwg:Version>`)
	b.WriteString(`<scan:Intent>Document</scan:Intent>`)
	b.WriteString(`<scan:ScanRegions><scan:ScanRegion>`)
	b.WriteString(`<pwg:ContentRegionUnits>escl:ThreeHundredthsOfInches</pwg:ContentRegionUnits>`)
	fmt.Fprintf(&b, `<pwg:Width>%d</pwg:Width><pwg:Height>%d</pwg:Height>`, width, height)
	b.WriteString(`<pwg:XOffset>0</pwg:XOffset><pwg:YOffset>0</pwg:YOffset>`)
	b.WriteString(`</scan:ScanRegion></scan:ScanRegions>`)
	fmt.Fprintf(&b, `<scan:InputSource>%s</scan:InputSource>`, source)
	if s.Duplex {
		b.WriteString(`<scan:Duplex>true</scan:Duplex>`)
	}
	fmt.Fprintf(&b, `<scan:ColorMode>%s</scan:ColorMode>`, color)
	fmt.Fprintf(&b, `<scan:XResolution>%d</scan:XResolution><scan:YResolution>%d</scan:YResolution>`, xres, yres)
	fmt.Fprintf(&b, `<pwg:DocumentFormat>%s</pwg:DocumentFormat>`, format)
	fmt.Fprintf(&b, `<scan:DocumentFormatExt>%s</scan:DocumentFormatExt>`, format)
	fmt.Fprintf(&b, `<scan:ImagesToTransfer>%d</scan:ImagesToTransfer>`, images)
	b.WriteString(`</scan:ScanSettings>`)
	return b.String()
}

var (
	// ErrScannerBusy 表示扫描仪正在处理别的任务。
	ErrScannerBusy = errors.New("扫描仪正忙，请稍后再试")
	// ErrUnsupportedFormat 表示设备不支持请求的输出格式。
	ErrUnsupportedFormat = errors.New("打印机不支持该扫描输出格式")
)

// Scan 执行一次完整扫描，返回扫描件字节（通常是多页 PDF）。
//
// 超时由 ctx 控制：玻璃板单页一般 5-20 秒，ADF 多页可能要一两分钟，
// 调用方应给足（建议 1.5-2 分钟）并在界面上告诉用户「正在扫描」。
// 无论成功失败，任务最后都会被 DELETE 清理（玻璃板上不残留任务状态）。
func (c *Client) Scan(ctx context.Context, s ScanSettings) ([]byte, error) {
	jobURL, err := c.createJob(ctx, s)
	if err != nil {
		return nil, err
	}
	// 收尾清理用独立 context：ctx 可能已因超时取消，但 DELETE 仍应发出，
	// 否则设备上残留任务，下一单可能 503。
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c.delete(cleanupCtx, jobURL)
	}()

	// 先盯一会儿任务状态，这一步只为「快速失败」：设备明确终止（进稿器里
	// 没有原件、任务被取消）时立刻报出来，别让用户干等满整个超时。
	//
	// 🚫 绝不能把「等不到 Completed」当成失败：扫描本身要 5-20 秒（多页
	// 更久），盯着看反而会把本来正常的扫描判死 —— 早期版本在这里固定等
	// 3 秒就返回超时，真机上一次都没扫成过。判断「什么时候有件」的权威
	// 只有取件那一步。
	abortErr := c.awaitJobOutcome(ctx, jobURL, jobSettleWindow)

	if abortErr != nil {
		// 少数机型扫完最后一页也会把任务标成 Aborted（件其实已经在里面
		// 了），所以先快速试取几次：取到了就用，取不到才把设备的终止原因
		// 报出去。不这样兜一下，用户会为一次成功的扫描收到一条失败提示。
		// 窗口刻意很短 —— 件在手边的话第一次就能拿到，拿不到就是真没扫成。
		if doc, ferr := c.fetchDocument(ctx, jobURL, 3*c.poll()); ferr == nil {
			return doc, nil
		}
		return nil, abortErr
	}

	doc, err := c.fetchDocument(ctx, jobURL, docBudget(ctx))
	if err != nil {
		return nil, err
	}
	return doc, nil
}

const (
	// jobSettleWindow 是「盯任务状态」的时间窗，只为快速失败而存在。
	// 取值不必覆盖完整扫描：窗口内没结论就转去取件轮询，两条路等价，
	// 而取件那条才是最终裁判。
	jobSettleWindow = 10 * time.Second
	// fetchFallbackBudget 是调用方 ctx 没有 deadline 时给取件用的兜底预算。
	fetchFallbackBudget = 90 * time.Second
)

// poll 是状态/取件轮询的间隔，测试里调小，免的用例真的等这么久。
func (c *Client) poll() time.Duration {
	if c.pollInterval > 0 {
		return c.pollInterval
	}
	return time.Second
}

// docBudget 取件预算：整单的硬上限由调用方 ctx 决定，这里留 5 秒余量给
// 任务清理与后续落单/送印，避免正好卡在 ctx 边界上被取消。
func docBudget(ctx context.Context) time.Duration {
	if dl, ok := ctx.Deadline(); ok {
		if left := time.Until(dl) - 5*time.Second; left > 5*time.Second {
			return left
		}
		return 5 * time.Second
	}
	return fetchFallbackBudget
}

func (c *Client) createJob(ctx context.Context, s ScanSettings) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.BaseURL, "/")+"/ScanJobs",
		strings.NewReader(s.scanSettingsXML()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "text/xml")
	resp, err := c.http().Do(req)
	if err != nil {
		return "", fmt.Errorf("连接扫描服务失败: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	switch {
	case resp.StatusCode == http.StatusCreated:
		loc := resp.Header.Get("Location")
		if loc == "" {
			return "", fmt.Errorf("扫描服务未返回任务地址（HTTP 201 但无 Location）")
		}
		return c.absURL(loc), nil
	case resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusConflict:
		return "", ErrScannerBusy
	case resp.StatusCode == http.StatusUnsupportedMediaType || resp.StatusCode == http.StatusBadRequest:
		return "", ErrUnsupportedFormat
	default:
		return "", fmt.Errorf("创建扫描任务失败（HTTP %d）", resp.StatusCode)
	}
}

// awaitJobOutcome 盯任务状态，只等一个「明确的坏消息」。
//
//   - 设备报 Aborted / Canceled → 返回带原因的错误（进稿器没原件、盖板
//     没关好、任务被取消，这类是真没扫成）；
//   - 报 Completed 系列 → 返回 nil（可以立刻取件）；
//   - 一直 Pending / Processing，或状态口抖动（404/400/500）→ 等满
//     window 后返回 nil：窗口内没结论是常态，不该据此判失败。
//
// 只有 ctx 被取消时才会把 ctx 的错误透出去。
func (c *Client) awaitJobOutcome(ctx context.Context, jobURL string, window time.Duration) error {
	deadline := time.Now().Add(window)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if state, err := c.jobState(ctx, jobURL); err == nil {
			switch strings.TrimSpace(state) {
			case "Aborted", "Canceled":
				return fmt.Errorf("打印机终止了扫描任务（进稿器里没有原件、盖板未关好或任务被取消），请检查原件后重试")
			case "Completed", "CompletedWithWarnings", "CompletedWithErrors":
				return nil
			}
		}
		if time.Now().After(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.poll()):
		}
	}
}

// jobState 读取任务的 JobState。个别设备在任务刚建好的几秒内会对该地址
// 吐 404/400，调用方按「没结论」处理即可。
func (c *Client) jobState(ctx context.Context, jobURL string) (string, error) {
	body, err := c.getURL(ctx, jobURL)
	if err != nil {
		return "", err
	}
	var st struct {
		JobState string `xml:"JobState"`
	}
	if err := xml.Unmarshal(body, &st); err != nil {
		return "", fmt.Errorf("解析任务状态: %w", err)
	}
	return st.JobState, nil
}

// fetchDocument 取扫描件。NextDocument 在设备还没写完时是 404，
// 用带截止的重试兜住；拿到后校验内容非空。
//
// 这里是整条扫描链路的最终裁判：什么时候拿到件，扫描就算什么时候完成。
// 所以超时话术要说清「我一直在等」，而不是让用户误以为参数设置有问题。
func (c *Client) fetchDocument(ctx context.Context, jobURL string, maxWait time.Duration) ([]byte, error) {
	deadline := time.Now().Add(maxWait)
	docURL := strings.TrimRight(jobURL, "/") + "/NextDocument"
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("等待扫描件超时（等了 %s）：请确认原件已放好、进稿器里没有卡纸", maxWait.Round(time.Second))
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, docURL, nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.http().Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			data, rerr := io.ReadAll(io.LimitReader(resp.Body, 512<<20))
			resp.Body.Close()
			if rerr != nil {
				return nil, rerr
			}
			if len(data) == 0 {
				return nil, fmt.Errorf("扫描件为空，请重试")
			}
			return data, nil
		}
		if resp != nil {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
			resp.Body.Close()
			// 404/409 = 扫描件还没就绪；400/5xx = 固件瞬时抖动（见 http()
			// 里 keep-alive 的说明）。轮询语境下统一重试到截止时间。
			if resp.StatusCode >= 400 {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(c.poll()):
					continue
				}
			}
			return nil, fmt.Errorf("获取扫描件失败（HTTP %d）", resp.StatusCode)
		}
		if err != nil && !isRetryableNet(err) {
			return nil, fmt.Errorf("连接扫描服务失败: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(c.poll()):
		}
	}
}

func (c *Client) delete(ctx context.Context, jobURL string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, jobURL, nil)
	if err != nil {
		return
	}
	if resp, err := c.http().Do(req); err == nil {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
		resp.Body.Close()
	}
}

// absURL 把设备返回的（可能是相对的）任务地址补全成绝对 URL。
// Location 常见三种形态：完整 URL、/eSCL/ScanJobs/xxx、ScanJobs/xxx。
func (c *Client) absURL(loc string) string {
	if strings.HasPrefix(loc, "http://") || strings.HasPrefix(loc, "https://") {
		return loc
	}
	if strings.HasPrefix(loc, "/") {
		u, err := url.Parse(c.BaseURL)
		if err != nil {
			return loc
		}
		u.Path = loc
		u.RawQuery = ""
		return u.String()
	}
	return strings.TrimRight(c.BaseURL, "/") + "/" + loc
}

// ─── HTTP 基础 ────────────────────────────────────────────────────────

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	// 🚫 不要开 keep-alive 连接复用。实测 Ricoh Pro C5200S 的 eSCL 实现在
	// 复用的连接上会【随机】返回 HTTP 400（同一请求 curl 稳定 200，
	// Go 连发三次出现 200/400/200）。扫描流程只有几个请求，
	// 每个新建连接的开销可以忽略，稳定性优先。
	c.HTTP = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
		},
	}
	return c.HTTP
}

func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	return c.getURLRetry(ctx, strings.TrimRight(c.BaseURL, "/")+path)
}

func (c *Client) getURL(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("连接扫描服务失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
		return nil, &httpError{Code: resp.StatusCode}
	}
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}

// getURLRetry 对幂等的 GET 做最多 3 次尝试。
//
// 实测 Ricoh Pro C5200S 的嵌入式 Web 服务（Web-Server/3.0）会【间歇性】对
// 完全相同的请求返回裸的 400 Bad Request（同一字节序列 raw socket 稳定 200，
// 同一进程内的相邻请求出现 200/400 交替）—— 固件 HTTP 栈对某些连接的解析
// 有缺陷。GET 是幂等的，重试没有副作用；扫描任务那几个 POST/DELETE
// 不走这里（POST 重试会造成重复扫描）。
func (c *Client) getURLRetry(ctx context.Context, rawURL string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(400 * time.Millisecond):
			}
		}
		data, err := c.getURL(ctx, rawURL)
		if err == nil {
			return data, nil
		}
		lastErr = err
		// 网络层「连接被拒/超时」重试意义不大（设备真离线），只重试 HTTP 层错误。
		var he *httpError
		if !errors.As(err, &he) {
			return nil, err
		}
	}
	return nil, lastErr
}

type httpError struct{ Code int }

func (e *httpError) Error() string { return "HTTP " + strconv.Itoa(e.Code) }

func isRetryableNet(err error) bool {
	// 网络抖动在轮询语境里统一按可重试处理；真正的连接失败会在建任务时
	// 就已经报出来，走到这里的都是「任务已建好、后续查询抖一下」。
	return true
}
