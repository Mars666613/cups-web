package ipp

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	goipp "github.com/OpenPrinting/goipp"
)

// TestNormalizePageSet 覆盖 CUPS page-set 合法值（all / odd / even）以及
// 非法值、大小写、前后空白的规范化逻辑。空串必须映射为 "all"，未知值
// 必须返回空串，让调用方完全跳过 IPP 属性，避免被打印机拒绝。
func TestNormalizePageSet(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"", "all"},
		{"all", "all"},
		{"ALL", "all"},
		{"  odd ", "odd"},
		{"Odd", "odd"},
		{"even", "even"},
		{"EVEN", "even"},
		{"bogus", ""},
		{"1-5", ""},
	}
	for _, c := range cases {
		if got := normalizePageSet(c.input); got != c.want {
			t.Errorf("normalizePageSet(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

// TestPageRangeValues 锁死「非法页码必须报错」这条契约。
//
// 这条测试取代了旧的 TestParsePageRange。旧实现用 fmt.Sscanf，非法片段
// 被静默丢弃，全部片段都非法时更是返回空列表 —— 而空列表意味着
// SendPrintJob 干脆不下发 page-ranges 属性，对 CUPS 就是「打印全部页」。
// 旧测试当时把「0 -3 5-3 应被静默丢弃」写成了期望行为，等于把 bug 固化成契约。
//
// 现在解析交给 internal/pricing（那里有 65 条跨语言用例守着），
// 这里只验证「转换 + 绝不静默降级」这两件事。
func TestPageRangeValues(t *testing.T) {
	t.Run("正常转换", func(t *testing.T) {
		vals, err := pageRangeValues("1-5 8 10-12")
		if err != nil {
			t.Fatalf("意外报错: %v", err)
		}
		want := [][2]int{{1, 5}, {8, 8}, {10, 12}}
		if len(vals) != len(want) {
			t.Fatalf("值个数 = %d，期望 %d（%v）", len(vals), len(want), vals)
		}
		for i, w := range want {
			if vals[i].T != goipp.TagRange {
				t.Errorf("vals[%d] 的标签 = %v，期望 TagRange", i, vals[i].T)
			}
			r, ok := vals[i].V.(goipp.Range)
			if !ok {
				t.Fatalf("vals[%d] 的值类型 = %T，期望 goipp.Range", i, vals[i].V)
			}
			if r.Lower != w[0] || r.Upper != w[1] {
				t.Errorf("vals[%d] = %d-%d，期望 %d-%d", i, r.Lower, r.Upper, w[0], w[1])
			}
		}
	})

	// 下面这些以前会被静默丢弃 → 属性不下发 → 打印整个文档。
	t.Run("非法页码必须报错而不是降级", func(t *testing.T) {
		for _, bad := range []string{"5-3", "0", "0-5", "abc", "1-2-3", "-5", "，"} {
			if _, err := pageRangeValues(bad); err == nil {
				t.Errorf("pageRangeValues(%q) 应当报错，却成功了 —— "+
					"这会导致 page-ranges 不下发、CUPS 打印整个文档", bad)
			}
		}
	})

	// 空串由调用方负责跳过（它表示「全部页」），到了这一层就是误用。
	t.Run("空串报错，调用方应自行跳过", func(t *testing.T) {
		if _, err := pageRangeValues(""); err == nil {
			t.Error("空串应当报错，调用方必须先判空")
		}
	})

	// pricing 比旧实现宽松的地方：接受逗号/全角标点等分隔符。
	// 这些写法在旧实现里靠 Sscanf 的宽容歪打正着，现在是正式支持的。
	t.Run("宽容分隔符仍然接受", func(t *testing.T) {
		for _, ok := range []string{"1-3 5", "1,3,5", "1，3", "1 - 3"} {
			if _, err := pageRangeValues(ok); err != nil {
				t.Errorf("pageRangeValues(%q) 不应报错: %v", ok, err)
			}
		}
	})
}

// buildJobMessage 复刻 SendPrintJob 的请求构造流程（只取 Job 组），
// 用于离线校验 PrintJobOptions → IPP 属性的映射正确性。之所以不直接
// 跑 SendPrintJob，是因为那个函数内部需要真实的 HTTP 连接，测试中
// 没法方便地 mock。
func buildJobMessage(opts PrintJobOptions) *goipp.Message {
	req := goipp.NewRequest(goipp.DefaultVersion, goipp.OpPrintJob, 1)
	// 与 SendPrintJob 保持同样的 duplex/pageSet 互斥兜底（issue #109）。
	isDuplex := opts.IsDuplex
	if set := normalizePageSet(opts.PageSet); set == "odd" || set == "even" || set == "even-reverse" {
		isDuplex = false
	}
	if isDuplex {
		req.Job.Add(goipp.MakeAttribute("sides", goipp.TagKeyword, goipp.String("two-sided-long-edge")))
	} else {
		req.Job.Add(goipp.MakeAttribute("sides", goipp.TagKeyword, goipp.String("one-sided")))
	}
	if set := normalizePageSet(opts.PageSet); set != "" && set != "all" && set != "even-reverse" {
		req.Job.Add(goipp.MakeAttribute("page-set", goipp.TagKeyword, goipp.String(set)))
	}
	return req
}

// findJobAttr 在 Job 组中按名称查找属性，返回其第一个字符串值。
// 未命中时返回空串，供测试用 "应当/不应当存在" 两路断言。
func findJobAttr(msg *goipp.Message, name string) string {
	for _, a := range msg.Job {
		if a.Name == name && len(a.Values) > 0 {
			return a.Values[0].V.String()
		}
	}
	return ""
}

// TestSendPrintJob_PageSetEncoding 验证 page-set 在不同 PageSet 入参下
// 是否按预期进入 IPP Job 组（all 不发、odd/even 落地、非法值丢弃）。
func TestSendPrintJob_PageSetEncoding(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantHas bool
		wantVal string
	}{
		{"default empty -> omitted", "", false, ""},
		{"explicit all -> omitted", "all", false, ""},
		{"odd injected", "odd", true, "odd"},
		{"even injected", "even", true, "even"},
		{"mixed case normalized", "EVEN", true, "even"},
		{"bogus dropped", "first-page", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := buildJobMessage(PrintJobOptions{PageSet: c.input})
			got := findJobAttr(msg, "page-set")
			if c.wantHas {
				if got != c.wantVal {
					t.Errorf("page-set = %q, want %q", got, c.wantVal)
				}
			} else if got != "" {
				t.Errorf("page-set should be omitted, got %q", got)
			}

			// 同时通过编解码验证属性能在 wire format 上 round-trip，
			// 避免 goipp 对 keyword 类型的编码路径出现意外回归。
			payload, err := msg.EncodeBytes()
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			var decoded goipp.Message
			if err := decoded.Decode(bytes.NewReader(payload)); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got2 := findJobAttr(&decoded, "page-set"); got2 != got {
				t.Errorf("round-trip mismatch: encoded %q, decoded %q", got, got2)
			}
		})
	}
}

// TestSendPrintJob_PageSetNotLeakingIntoOperation 额外确认 page-set 不会被
// 误写到 Operation 组（CUPS 对组别敏感，写错位置会被直接忽略或报错）。
func TestSendPrintJob_PageSetNotLeakingIntoOperation(t *testing.T) {
	msg := buildJobMessage(PrintJobOptions{PageSet: "odd"})
	for _, a := range msg.Operation {
		if strings.EqualFold(a.Name, "page-set") {
			t.Fatalf("page-set leaked into Operation group: %+v", a)
		}
	}
}

// TestSendPrintJob_PageSetForcesOneSided 覆盖 issue #109：当选择了奇/偶页手动
// 双面时，即使 IsDuplex=true 也必须落成 sides=one-sided，避免 CUPS 用空白页
// 填补被过滤掉的另一面而产生间隔空白页。
func TestSendPrintJob_PageSetForcesOneSided(t *testing.T) {
	cases := []struct {
		name      string
		opts      PrintJobOptions
		wantSides string
	}{
		{"odd + duplex -> forced one-sided", PrintJobOptions{IsDuplex: true, PageSet: "odd"}, "one-sided"},
		{"even + duplex -> forced one-sided", PrintJobOptions{IsDuplex: true, PageSet: "even"}, "one-sided"},
		{"all + duplex -> two-sided", PrintJobOptions{IsDuplex: true, PageSet: "all"}, "two-sided-long-edge"},
		{"empty + duplex -> two-sided", PrintJobOptions{IsDuplex: true, PageSet: ""}, "two-sided-long-edge"},
		{"even-reverse + duplex -> forced one-sided", PrintJobOptions{IsDuplex: true, PageSet: "even-reverse"}, "one-sided"},
		{"odd + one-sided stays one-sided", PrintJobOptions{IsDuplex: false, PageSet: "odd"}, "one-sided"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := buildJobMessage(c.opts)
			if got := findJobAttr(msg, "sides"); got != c.wantSides {
				t.Errorf("sides = %q, want %q", got, c.wantSides)
			}
		})
	}
}

// cupsGetPrintersResponse 拼一个含两台打印机的 CUPS-Get-Printers 响应，
// 用来验证 listPrintersIPP 会遍历所有 printer-attributes 组 —— goipp 的
// Message.Printer 只保留被压平的那一组，多队列时会静默丢数据。
func cupsGetPrintersResponse() ([]byte, error) {
	msg := goipp.NewResponse(goipp.DefaultVersion, goipp.Status(goipp.StatusOk), 1)
	msg.Groups = goipp.Groups{
		{Tag: goipp.TagOperationGroup, Attrs: goipp.Attributes{
			goipp.MakeAttribute("attributes-charset", goipp.TagCharset, goipp.String("utf-8")),
		}},
		{Tag: goipp.TagPrinterGroup, Attrs: goipp.Attributes{
			goipp.MakeAttribute("printer-name", goipp.TagName, goipp.String("HP_1020")),
			goipp.MakeAttribute("printer-info", goipp.TagText, goipp.String("三楼财务室")),
			goipp.MakeAttribute("printer-location", goipp.TagText, goipp.String("3F")),
			goipp.MakeAttribute("printer-make-and-model", goipp.TagText, goipp.String("HP LaserJet 1020")),
		}},
		{Tag: goipp.TagPrinterGroup, Attrs: goipp.Attributes{
			goipp.MakeAttribute("printer-name", goipp.TagName, goipp.String("HP_1020_2")),
			goipp.MakeAttribute("printer-info", goipp.TagText, goipp.String("  一楼前台  ")),
		}},
		// 没有 printer-name 的组必须被丢掉，否则会拼出 /printers/ 这种空 URI
		{Tag: goipp.TagPrinterGroup, Attrs: goipp.Attributes{
			goipp.MakeAttribute("printer-info", goipp.TagText, goipp.String("孤儿组")),
		}},
	}
	return msg.EncodeBytes()
}

// TestListPrintersIPP 验证 CUPS-Get-Printers 路径：多队列全都要解析出来、
// 描述字段去空白、URI 按 host+队列名拼（而不是打印机自报的主机名）。
func TestListPrintersIPP(t *testing.T) {
	payload, err := cupsGetPrintersResponse()
	if err != nil {
		t.Fatalf("build response: %v", err)
	}

	var gotRequested []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req goipp.Message
		if err := req.Decode(bytes.NewReader(body)); err == nil {
			for _, a := range req.Operation {
				if a.Name == "requested-attributes" {
					for _, v := range a.Values {
						gotRequested = append(gotRequested, v.V.String())
					}
				}
			}
		}
		w.Header().Set("Content-Type", goipp.ContentType)
		w.Write(payload)
	}))
	defer srv.Close()

	hostOnly := strings.TrimPrefix(srv.URL, "http://")
	printers, err := listPrintersIPP(hostOnly)
	if err != nil {
		t.Fatalf("listPrintersIPP: %v", err)
	}
	if len(printers) != 2 {
		t.Fatalf("got %d printers, want 2: %+v", len(printers), printers)
	}

	p := printers[0]
	if p.Name != "HP_1020" || p.Info != "三楼财务室" || p.Location != "3F" || p.MakeAndModel != "HP LaserJet 1020" {
		t.Errorf("printer[0] = %+v", p)
	}
	if want := "http://" + hostOnly + "/printers/HP_1020"; p.URI != want {
		t.Errorf("printer[0].URI = %q, want %q", p.URI, want)
	}
	if printers[1].Info != "一楼前台" {
		t.Errorf("printer[1].Info = %q, want trimmed 一楼前台", printers[1].Info)
	}

	// 必须点名 requested-attributes，否则 CUPS 会为每台打印机回上百个属性
	if len(gotRequested) == 0 {
		t.Error("request did not carry requested-attributes")
	}
	for _, want := range []string{"printer-name", "printer-info"} {
		if !slices.Contains(gotRequested, want) {
			t.Errorf("requested-attributes missing %q: %v", want, gotRequested)
		}
	}
}

// TestListPrintersFallbackToHTML 确认 IPP 操作被拒时会退回抓 /printers 页面，
// 让远端 CUPS 禁掉 CUPS-Get-Printers 的部署仍然列得出队列（只是没有描述）。
func TestListPrintersFallbackToHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><body><a href="/printers/Canon_LBP">Canon_LBP</a></body></html>`))
	}))
	defer srv.Close()

	printers, err := ListPrinters(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("ListPrinters: %v", err)
	}
	if len(printers) != 1 || printers[0].Name != "Canon_LBP" {
		t.Fatalf("got %+v, want single Canon_LBP", printers)
	}
	if printers[0].Info != "" {
		t.Errorf("HTML fallback should not invent an info field, got %q", printers[0].Info)
	}
}

// TestCupsHostPort 覆盖 CUPS_HOST 的几种写法归一化。
func TestCupsHostPort(t *testing.T) {
	cases := []struct{ in, want string }{
		{"localhost", "localhost:631"},
		{"localhost:631", "localhost:631"},
		{"http://cups.lan", "cups.lan:631"},
		{"http://cups.lan:6310", "cups.lan:6310"},
	}
	for _, c := range cases {
		got, err := cupsHostPort(c.in)
		if err != nil {
			t.Errorf("cupsHostPort(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("cupsHostPort(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if _, err := cupsHostPort(""); err == nil {
		t.Error("cupsHostPort(\"\") should fail")
	}
}

// TestJobIDFromURI 覆盖「从 CUPS 的任务标识里取数字 id」。
//
// ⚠️ 这个测试是补上的：早先的代码是「从字符串开头往后解析数字」，
// 而 SendPrintJob 优先返回 job-uri（"ipp://localhost:631/jobs/15"），
// 以 'i' 开头 → 直接得到 0 → 每次核对都去问「0 号任务」→ 永远查不到。
//
// 后果是四个现象同时出现，全都指向同一个根因：
//   - 订单永远停在 printing
//   - 打印记录永远停在 queued（界面显示「排队中」）
//   - 支付弹窗永远不切到成功页（因为它等的是订单变 printed）
//   - 卡满 30 分钟后被判成「打印结果无法确认」
func TestJobIDFromURI(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		// CUPS 实际会回的形式（SendPrintJob 优先返回 job-uri）
		{"ipp://localhost:631/jobs/15", 15},
		{"ipp://192.168.1.5:631/jobs/1", 1},
		{"ipps://cups.local/jobs/12345", 12345},
		// 纯数字形式也不能漏
		{"15", 15},
		{"0", 0},
		// 空格 / 脏数据
		{"  ipp://localhost:631/jobs/7  ", 7},
		// 取不到就返回 0，由调用方走「查不到」分支，不要瞎猜
		{"", 0},
		{"ipp://localhost:631/jobs/abc", 0},
		{"not a job", 0},
		{"ipp://localhost:631/jobs/", 0},
	}

	for _, c := range cases {
		if got := JobIDFromURI(c.in); got != c.want {
			t.Errorf("JobIDFromURI(%q) = %d，期望 %d", c.in, got, c.want)
		}
	}
}

// TestJobIDFromURI_MatchesRealCUPSValue 锁定「送到库里的那个字符串能被解析」。
//
// 这条防的是「改了 SendPrintJob 的返回值却忘了改解析」——
// 两者必须始终对得上，否则整个打印结果核对会静默失效。
func TestJobIDFromURI_MatchesRealCUPSValue(t *testing.T) {
	// 这是真实 CUPS 回给 SendPrintJob、并被写进 print_orders.job_id 的形式。
	const stored = "ipp://localhost:631/jobs/15"
	if got := JobIDFromURI(stored); got != 15 {
		t.Fatalf("库里存的任务标识解析不出数字 id（得到 %d）——"+
			"打印结果核对会静默失效，订单永远停在「打印中」", got)
	}
}
