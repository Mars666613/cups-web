package escl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// capabilities 夹具来自现场 Ricoh Pro C5200S 的真实响应（截选），
// 结构与其余厂商的实现兼容（元素有无比取值更重要）。
const ricohCapsXML = `<?xml version="1.0" encoding="UTF-8"?>
<scan:ScannerCapabilities xmlns:pwg="http://www.pwg.org/schemas/2010/12/sm" xmlns:scan="http://schemas.hp.com/imaging/escl/2011/05/03">
 <pwg:Version>2.63</pwg:Version>
 <pwg:MakeAndModel>RICOH Pro C5200S</pwg:MakeAndModel>
 <scan:Platen>
  <scan:PlatenInputCaps>
   <scan:MaxWidth>3507</scan:MaxWidth>
   <scan:MaxHeight>5102</scan:MaxHeight>
   <scan:SettingProfiles>
    <scan:SettingProfile>
     <scan:ColorModes>
      <scan:ColorMode>BlackAndWhite1</scan:ColorMode>
      <scan:ColorMode>Grayscale8</scan:ColorMode>
      <scan:ColorMode>RGB24</scan:ColorMode>
     </scan:ColorModes>
     <scan:DocumentFormats>
      <pwg:DocumentFormat>application/pdf</pwg:DocumentFormat>
      <pwg:DocumentFormat>image/jpeg</pwg:DocumentFormat>
     </scan:DocumentFormats>
    </scan:SettingProfile>
   </scan:SettingProfiles>
   <scan:MaxPhysicalWidth>3507</scan:MaxPhysicalWidth>
   <scan:MaxPhysicalHeight>5102</scan:MaxPhysicalHeight>
  </scan:PlatenInputCaps>
 </scan:Platen>
 <scan:Adf>
  <scan:AdfSimplexInputCaps></scan:AdfSimplexInputCaps>
  <scan:AdfDuplexInputCaps></scan:AdfDuplexInputCaps>
  <scan:AdfOption>Duplex</scan:AdfOption>
 </scan:Adf>
</scan:ScannerCapabilities>`

func TestParseCapabilities(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/eSCL/ScannerCapabilities" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(ricohCapsXML))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/eSCL"}
	caps, err := c.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if caps.MakeAndModel != "RICOH Pro C5200S" {
		t.Errorf("MakeAndModel = %q", caps.MakeAndModel)
	}
	if !caps.HasPlaten || !caps.HasAdf || !caps.AdfDuplex {
		t.Errorf("platen/adf/duplex = %v/%v/%v", caps.HasPlaten, caps.HasAdf, caps.AdfDuplex)
	}
	if !caps.Color {
		t.Error("应支持彩色扫描")
	}
	hasPDF := false
	for _, f := range caps.Formats {
		if f == "application/pdf" {
			hasPDF = true
		}
	}
	if !hasPDF {
		t.Errorf("formats 缺少 pdf: %v", caps.Formats)
	}
	if caps.MaxWidth != 3507 || caps.MaxHeight != 5102 {
		t.Errorf("max region = %dx%d", caps.MaxWidth, caps.MaxHeight)
	}
}

func TestParseStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<scan:ScannerStatus xmlns:pwg="http://www.pwg.org/schemas/2010/12/sm">
			<pwg:State>Idle</pwg:State>
			<pwg:StateReasons>AttentionRequired</pwg:StateReasons>
			<scan:AdfState>ScannerAdfEmpty</scan:AdfState>
		</scan:ScannerStatus>`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/eSCL"}
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.Idle() {
		t.Errorf("Idle() = false, state=%q", st.State)
	}
	if st.AdfState != "ScannerAdfEmpty" {
		t.Errorf("AdfState = %q", st.AdfState)
	}
}

// 进稿器装载状态的判定：只有设备【明确】说 loaded/empty 才算数。
// 把「不知道」当成「空」，会让不上报该状态的机型彻底没法用进稿器复印。
func TestAdfStateJudgement(t *testing.T) {
	cases := []struct {
		adfState   string
		wantLoaded bool
		wantEmpty  bool
	}{
		{adfState: "ScannerAdfLoaded", wantLoaded: true},
		{adfState: "ScannerAdfProcessing", wantLoaded: true},
		{adfState: "ScannerAdfEmpty", wantEmpty: true},
		{adfState: "scanneradfempty", wantEmpty: true}, // 大小写不敏感
		{adfState: "ScannerAdfJam"},
		{adfState: "ScannerAdfDoorOpen"},
		{adfState: ""},
	}
	for _, c := range cases {
		st := Status{AdfState: c.adfState}
		if got := st.AdfLoaded(); got != c.wantLoaded {
			t.Errorf("AdfLoaded(%q) = %v, want %v", c.adfState, got, c.wantLoaded)
		}
		if got := st.AdfEmpty(); got != c.wantEmpty {
			t.Errorf("AdfEmpty(%q) = %v, want %v", c.adfState, got, c.wantEmpty)
		}
	}
}

// 真机返回的是带命名空间前缀的 <scan:AdfState>，「有原件」这一支也要
// 走一遍完整的 XML 解析路径。
func TestParseStatusAdfLoaded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<scan:ScannerStatus xmlns:pwg="http://www.pwg.org/schemas/2010/12/sm" xmlns:scan="http://schemas.hp.com/imaging/escl/2011/05/03">
			<pwg:State>Idle</pwg:State>
			<scan:AdfState>ScannerAdfLoaded</scan:AdfState>
		</scan:ScannerStatus>`))
	}))
	defer srv.Close()

	st, err := (&Client{BaseURL: srv.URL + "/eSCL"}).Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.AdfLoaded() || st.AdfEmpty() {
		t.Errorf("AdfState=%q 应判为「有原件」", st.AdfState)
	}
}

func TestEsclBaseFromPrinterURI(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ipp://192.168.1.100/ipp/print", "http://192.168.1.100/eSCL"},
		{"ipps://192.168.1.100/ipp/print", "https://192.168.1.100/eSCL"},
		{"ipp://printer.local:631/ipp/print", "http://printer.local/eSCL"},
		{"http://192.168.0.9:8080/", "http://192.168.0.9:8080/eSCL"},
		{"usb://RICOH/Pro?serial=1", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := EsclBaseFromPrinterURI(tc.in); got != tc.want {
			t.Errorf("EsclBaseFromPrinterURI(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestScanSettingsXML(t *testing.T) {
	xmlStr := ScanSettings{
		InputSource:      SourceADF,
		ColorMode:        ColorModeGray8,
		Duplex:           true,
		Format:           "application/pdf",
		XResolution:      300,
		YResolution:      300,
		ImagesToTransfer: 0,
	}.scanSettingsXML()

	for _, want := range []string{
		`<scan:InputSource>ADF</scan:InputSource>`,
		`<scan:Duplex>true</scan:Duplex>`,
		`<scan:ColorMode>Grayscale8</scan:ColorMode>`,
		`<pwg:DocumentFormat>application/pdf</pwg:DocumentFormat>`,
		`<scan:ImagesToTransfer>0</scan:ImagesToTransfer>`,
		`xmlns:pwg=`,
	} {
		if !strings.Contains(xmlStr, want) {
			t.Errorf("XML 缺少 %s\n%s", want, xmlStr)
		}
	}

	// 玻璃板强制单页，无论调用方传了什么。
	xmlStr = ScanSettings{InputSource: SourcePlaten, ImagesToTransfer: 9}.scanSettingsXML()
	if !strings.Contains(xmlStr, `<scan:ImagesToTransfer>1</scan:ImagesToTransfer>`) {
		t.Errorf("玻璃板应强制 ImagesToTransfer=1:\n%s", xmlStr)
	}
	if strings.Contains(xmlStr, "<scan:Duplex>") {
		t.Errorf("玻璃板不应带 Duplex:\n%s", xmlStr)
	}
}

func TestScanEndToEnd(t *testing.T) {
	// 模拟一台设备：POST 建任务 → 轮询 → 取件 → 清理。
	var jobDeleted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/eSCL/ScanJobs":
			// 校验请求体是我们发的设置
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "<scan:InputSource>Platen</scan:InputSource>") {
				t.Errorf("意外的扫描设置: %s", body)
			}
			w.Header().Set("Location", "/eSCL/ScanJobs/job-1")
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodGet && r.URL.Path == "/eSCL/ScanJobs/job-1":
			w.Write([]byte(`<scan:ScannerStatus xmlns:pwg="http://www.pwg.org/schemas/2010/12/sm">
				<pwg:JobState>Completed</pwg:JobState></scan:ScannerStatus>`))
		case r.Method == http.MethodGet && r.URL.Path == "/eSCL/ScanJobs/job-1/NextDocument":
			w.Write([]byte("%PDF-1.4 fake scanned document"))
		case r.Method == http.MethodDelete && r.URL.Path == "/eSCL/ScanJobs/job-1":
			jobDeleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/eSCL"}
	doc, err := c.Scan(context.Background(), ScanSettings{InputSource: SourcePlaten})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !strings.HasPrefix(string(doc), "%PDF") {
		t.Errorf("扫描件不是 PDF: %q", doc)
	}
	if !jobDeleted {
		t.Error("扫描结束后应 DELETE 任务")
	}
}

func TestScanBusy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/eSCL"}
	_, err := c.Scan(context.Background(), ScanSettings{})
	if !errors.Is(err, ErrScannerBusy) {
		t.Fatalf("应返回 ErrScannerBusy, got %v", err)
	}
}

// TestScanWaitsForSlowDevice 是「固定等 3 秒就判超时」那个 bug 的回归用例。
//
// 设备先吐 Processing、NextDocument 404 一段时间（真机扫描就是这个样子，
// 玻璃板 5-20 秒，进稿器更久），之后才 Completed 并出件 —— 必须成功。
// 轮询间隔调到 500ms 让用例快一点，但总时长仍刻意压在 3 秒的旧窗口之上：
// 用例跑在旧实现上一定会红。
func TestScanWaitsForSlowDevice(t *testing.T) {
	var statusCalls int32
	const slowPolls = 8 // 8 × 500ms ≈ 4s > 旧的 3s 窗口

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/eSCL/ScanJobs":
			w.Header().Set("Location", "/eSCL/ScanJobs/job-slow")
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodGet && r.URL.Path == "/eSCL/ScanJobs/job-slow":
			if atomic.AddInt32(&statusCalls, 1) <= slowPolls {
				w.Write([]byte(`<scan:ScannerStatus><pwg:JobState>Processing</pwg:JobState></scan:ScannerStatus>`))
				return
			}
			w.Write([]byte(`<scan:ScannerStatus><pwg:JobState>Completed</pwg:JobState></scan:ScannerStatus>`))
		case r.Method == http.MethodGet && r.URL.Path == "/eSCL/ScanJobs/job-slow/NextDocument":
			// 扫描还没写完时设备就是 404。
			if atomic.LoadInt32(&statusCalls) <= slowPolls {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Write([]byte("%PDF-1.4 slow device"))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/eSCL", pollInterval: 500 * time.Millisecond}
	doc, err := c.Scan(context.Background(), ScanSettings{InputSource: SourcePlaten})
	if err != nil {
		t.Fatalf("设备扫描耗时超过 3 秒不该被判失败: %v", err)
	}
	if !strings.HasPrefix(string(doc), "%PDF") {
		t.Errorf("扫描件不是 PDF: %q", doc)
	}
}

// TestScanAbortedReportsDeviceReason：设备明确终止（进稿器空是常见原因）
// 时要给出可行动的原因，而不是一句笼统的超时。
func TestScanAbortedReportsDeviceReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/eSCL/ScanJobs":
			w.Header().Set("Location", "/eSCL/ScanJobs/job-aborted")
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodGet && r.URL.Path == "/eSCL/ScanJobs/job-aborted":
			w.Write([]byte(`<scan:ScannerStatus><pwg:JobState>Aborted</pwg:JobState></scan:ScannerStatus>`))
		default:
			// 没有扫描件：NextDocument 一直 404。
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/eSCL", pollInterval: 10 * time.Millisecond}
	_, err := c.Scan(context.Background(), ScanSettings{InputSource: SourceADF})
	if err == nil {
		t.Fatal("设备终止任务时必须报错")
	}
	if !strings.Contains(err.Error(), "终止") {
		t.Errorf("错误信息应说明设备终止了任务，实际: %v", err)
	}
}

// TestScanAbortedButDocumentReady：有些机型扫完最后一页也会把任务标成
// Aborted，件其实已经拿到了 —— 这种情况要当成功，不能把用户的一次成功
// 扫描报成失败。
func TestScanAbortedButDocumentReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/eSCL/ScanJobs":
			w.Header().Set("Location", "/eSCL/ScanJobs/job-x")
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodGet && r.URL.Path == "/eSCL/ScanJobs/job-x":
			w.Write([]byte(`<scan:ScannerStatus><pwg:JobState>Aborted</pwg:JobState></scan:ScannerStatus>`))
		case r.Method == http.MethodGet && r.URL.Path == "/eSCL/ScanJobs/job-x/NextDocument":
			w.Write([]byte("%PDF-1.4 already scanned"))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/eSCL", pollInterval: 10 * time.Millisecond}
	doc, err := c.Scan(context.Background(), ScanSettings{InputSource: SourcePlaten})
	if err != nil {
		t.Fatalf("件已经取到就该算成功: %v", err)
	}
	if !strings.HasPrefix(string(doc), "%PDF") {
		t.Errorf("扫描件不是 PDF: %q", doc)
	}
}

// TestScanStatusHiccupsAreNotFatal：状态口抖动（404/500）不能被当成
// 扫描失败 —— 真机理光的嵌入式 Web 服务会随机吐 400，取件口才是裁判。
func TestScanStatusHiccupsAreNotFatal(t *testing.T) {
	var statusCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/eSCL/ScanJobs":
			w.Header().Set("Location", "/eSCL/ScanJobs/job-flaky")
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodGet && r.URL.Path == "/eSCL/ScanJobs/job-flaky":
			// 头几次全炸（含 404 与 500），之后正常给 Completed。
			if atomic.AddInt32(&statusCalls, 1) <= 3 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Write([]byte(`<scan:ScannerStatus><pwg:JobState>Completed</pwg:JobState></scan:ScannerStatus>`))
		case r.Method == http.MethodGet && r.URL.Path == "/eSCL/ScanJobs/job-flaky/NextDocument":
			w.Write([]byte("%PDF-1.4 flaky"))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/eSCL", pollInterval: 5 * time.Millisecond}
	doc, err := c.Scan(context.Background(), ScanSettings{InputSource: SourceADF})
	if err != nil {
		t.Fatalf("状态口抖动不该让扫描失败: %v", err)
	}
	if !strings.HasPrefix(string(doc), "%PDF") {
		t.Errorf("扫描件不是 PDF: %q", doc)
	}
}

// TestLiveScanPlaten 真机会扫描一次玻璃板 —— 会亮扫描灯、但不走纸，
// 也不会碰进稿器。默认跳过：只有确认玻璃板上的东西可以扫时才跑。
//
//	CUPS_ESCL_LIVE_SCAN="ipp://192.168.1.100/ipp/print" \
//	  go test ./internal/escl/ -run TestLiveScanPlaten -v
//
// 这是「后端这一整条扫描链路真的能出件」的最终证据；用完即删设备上的任务。
func TestLiveScanPlaten(t *testing.T) {
	printerURI := os.Getenv("CUPS_ESCL_LIVE_SCAN")
	if printerURI == "" {
		t.Skip("设置 CUPS_ESCL_LIVE_SCAN=<打印机URI> 才启用真机扫描（会真的扫玻璃板）")
	}
	c := NewClient(printerURI)
	if c == nil {
		t.Fatalf("无法从 %q 推导 eSCL 地址", printerURI)
	}
	caps, err := c.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if !caps.HasPlaten {
		t.Skip("这台设备没有玻璃板，跳过")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	defer cancel()

	start := time.Now()
	doc, err := c.Scan(ctx, ScanSettings{
		InputSource: SourcePlaten,
		ColorMode:   ColorModeGray8,
		Format:      "application/pdf",
		XResolution: 300,
		YResolution: 300,
		Width:       caps.MaxWidth,
		Height:      caps.MaxHeight,
	})
	if err != nil {
		t.Fatalf("真机扫描失败: %v", err)
	}
	t.Logf("扫描成功：%s 共 %d 字节（耗时 %s）", caps.MakeAndModel, len(doc), time.Since(start).Round(time.Second))
	if !strings.HasPrefix(string(doc), "%PDF") {
		t.Errorf("产出不是 PDF，前 16 字节: %q", doc[:min(len(doc), 16)])
	}
}

// TestLiveScanADF 真机走一次进稿器 —— 会真的把纸吸进去扫一遍（不走纸打印，
// 但会把原件送出进稿器）。默认跳过：跑之前请确认进稿器里放的是可扫的件。
//
//	CUPS_ESCL_LIVE_ADF_SCAN="ipp://192.168.1.100/ipp/print" \
//	  CUPS_ESCL_LIVE_ADF_REGION="3507x2480" \
//	  go test ./internal/escl/ -run TestLiveScanADF -v
//
// 验证的是「区域 → 页面几何」这条链路：扫描件页面的朝向必须与纸张一致。
// 早先一律用设备最大幅面，A4 原件被采成 A3 横向页面，打印端为适应 A4 会把
// 整页转 90°，用户拿到横着的复印件。
func TestLiveScanADF(t *testing.T) {
	printerURI := os.Getenv("CUPS_ESCL_LIVE_ADF_SCAN")
	if printerURI == "" {
		t.Skip("设置 CUPS_ESCL_LIVE_ADF_SCAN=<打印机URI> 才启用真机进稿器扫描")
	}
	c := NewClient(printerURI)
	if c == nil {
		t.Fatalf("无法从 %q 推导 eSCL 地址", printerURI)
	}
	width, height := 3507, 2480 // A4 走本机进稿器：长边横跨、短边走纸
	if region := os.Getenv("CUPS_ESCL_LIVE_ADF_REGION"); region != "" {
		if _, err := fmt.Sscanf(region, "%dx%d", &width, &height); err != nil {
			t.Fatalf("CUPS_ESCL_LIVE_ADF_REGION 应形如 3507x2480: %v", err)
		}
	}

	caps, err := c.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if !caps.HasAdf {
		t.Skip("这台设备没有进稿器，跳过")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	defer cancel()

	start := time.Now()
	doc, err := c.Scan(ctx, ScanSettings{
		InputSource: SourceADF,
		ColorMode:   ColorModeGray8,
		Format:      "application/pdf",
		XResolution: 300,
		YResolution: 300,
		Width:       width,
		Height:      height,
	})
	if err != nil {
		t.Fatalf("真机进稿器扫描失败: %v", err)
	}
	logPDFGeometry(t, doc, width, height, time.Since(start))
}

// logPDFGeometry 把扫描件的页面几何打出来：区域、实际页面（点与毫米）、
// /Rotate、以及页面里图像的像素尺寸。判断朝向对不对全靠这几个数。
func logPDFGeometry(t *testing.T, doc []byte, reqW, reqH int, took time.Duration) {
	t.Helper()
	t.Logf("请求区域 %d×%d (1/300in) = %d×%dmm，耗时 %s",
		reqW, reqH, reqW*254/3000, reqH*254/3000, took.Round(time.Second))
	if !bytes.HasPrefix(doc, []byte("%PDF")) {
		t.Errorf("产出不是 PDF，前 16 字节: %q", doc[:min(len(doc), 16)])
	}
	s := string(doc)
	if m := reMediaBox.FindStringSubmatch(s); m != nil {
		pw, ph := atof(m[3]), atof(m[4]) // [0 0 W H]
		t.Logf("页面 MediaBox %.0f×%.0f 点 = %.0f×%.0fmm", pw, ph, pw*25.4/72, ph*25.4/72)
	} else {
		t.Errorf("扫描件里找不到 /MediaBox")
	}
	if m := reRotate.FindStringSubmatch(s); m != nil {
		t.Logf("页面 /Rotate %s（90/270 时显示尺寸要交换）", m[1])
	} else {
		t.Logf("页面 /Rotate 无（按页面自身朝向显示）")
	}
	if m := reImageDim.FindStringSubmatch(s); m != nil {
		t.Logf("页面内图像 %s×%s px（300dpi 下 = %s×%smm）",
			m[1], m[2], mul254(m[1]), mul254(m[2]))
	}
}

// mul254 把像素数（300dpi）换成毫米的字符串。
func mul254(px string) string {
	v := atof(px)
	return fmt.Sprintf("%.0f", v*25.4/300)
}

func atof(s string) float64 {
	var f float64
	fmt.Sscanf(s, "%g", &f)
	return f
}

var (
	reMediaBox = regexp.MustCompile(`/MediaBox\s*\[\s*([\d.]+)\s+([\d.]+)\s+([\d.]+)\s+([\d.]+)`)
	reRotate   = regexp.MustCompile(`/Rotate\s+(-?\d+)`)
	reImageDim = regexp.MustCompile(`/Subtype\s*/Image[\s\S]{0,200}?/Width\s+(\d+)[\s\S]{0,80}?/Height\s+(\d+)`)
)

// TestLiveProbe 只读真机探测，默认跳过。跑法：
//
//	CUPS_ESCL_LIVE="ipp://192.168.1.100/ipp/print" go test ./internal/escl/ -run TestLiveProbe -v
//
// 只调 Capabilities 与 Status 两个只读端点，不创建扫描任务、不动硬件。
func TestLiveProbe(t *testing.T) {
	printerURI := os.Getenv("CUPS_ESCL_LIVE")
	if printerURI == "" {
		t.Skip("设置 CUPS_ESCL_LIVE=<打印机URI> 才启用真机只读探测")
	}
	c := NewClient(printerURI)
	if c == nil {
		t.Fatalf("无法从 %q 推导 eSCL 地址", printerURI)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	caps, err := c.Capabilities(ctx)
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	t.Logf("型号: %s", caps.MakeAndModel)
	t.Logf("能力: platen=%v adf=%v duplex=%v color=%v", caps.HasPlaten, caps.HasAdf, caps.AdfDuplex, caps.Color)

	st, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	t.Logf("状态: %s (adf=%s reasons=%v)", st.State, st.AdfState, st.StateReasons)
}
