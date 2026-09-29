package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// ppdTestFixture 是 `lpinfo -m` 的样例行。
// 来自 Step 0-B 的真实容器输出（debian trixie + cups 2.4.x + 全套驱动包），
// Canon LBP2900 行是构造的（需要单独安装 canon-capt 驱动才会出现）。
var ppdTestFixture = []string{
	"everywhere IPP Everywhere",
	"drv:///sample.drv/generic.ppd Generic PostScript Printer",
	"drv:///sample.drv/generpcl.ppd Generic PCL Laser Printer",
	"drv:///hpijs.drv/hp-laserjet_1020-hpijs.ppd HP LaserJet 1020 hpijs, 3.22.10, requires proprietary plugin",
	"custom/HP-LaserJet_1020-A4.ppd HP LaserJet 1020 (A4 default)",
	"foomatic-db-compressed-ppds:0/ppd/foomatic-ppd/HP-LaserJet_1020-foo2zjs.ppd HP LaserJet 1020 Foomatic/foo2zjs (recommended)",
	"foomatic-db-compressed-ppds:0/ppd/foomatic-ppd/Canon-LBP2900-capt.ppd Canon LBP2900 Foomatic/capt (recommended)",
	"Canon/CNRCUPSLBP2900CAPTK.ppd Canon LBP2900 CAPT ver.2.71",
	"escpr:0/cups/model/epson-inkjet-printer-escpr/Epson-L3250_Series-epson-escpr-en.ppd EPSON L3250 Series , Epson Inkjet Printer Driver (ESC/P-R) for Linux",
	"gutenprint.5.3://escp2-l3250/expert Epson L3250 - CUPS+Gutenprint v5.3.4",
	"gutenprint.5.3://pcl-4/expert Generic PCL 4 Printer - CUPS+Gutenprint v5.3.4",
	"openprinting-ppds:0/ppd/openprinting/HP/LaserJet_1020.ppd HP LaserJet 1020, hpcups 3.23.12",
	"drv:///brlaser.drv/br1510.ppd Brother DCP-1510 series, using Owl-Maintain/brlaser v6.2.7",
	"foomatic-db-compressed-ppds:0/ppd/foomatic-ppd/Canon-LaserJet_1020_clone.ppd Canon LaserJet 1020 clone Foomatic/pxlmono",
	"zh/HP-LaserJet_1020-foo2zjs.ppd HP LaserJet 1020 Foomatic/foo2zjs,zh",
	"foomatic-db-compressed-ppds:0/ppd/foomatic-ppd/HP-LaserJet_Pro_P1102-foo2zjs-z2.ppd HP LaserJet Pro P1102 Foomatic/foo2zjs-z2 (recommended)",
}

func parseFixture() []PPDEntry {
	return ParsePPDLines(ppdTestFixture)
}

// ── 场景 1：HP LaserJet 1020 / USB ─────────────────────────────────────────────

func TestScorePPD_HPLaserJet1020(t *testing.T) {
	entries := parseFixture()
	cands := ScorePPDCandidates(entries, MatchInput{
		Manufacturer: "HP",
		Model:        "LaserJet 1020",
		PreferLang:   "zh",
	}, 8)

	if len(cands) == 0 {
		t.Fatal("expected candidates, got none")
	}
	top := cands[0]
	// Top-1 应该是 custom/vendor/hplip 之一（来源偏好最高的精确匹配项）
	switch top.Source {
	case PPDSourceCustom, PPDSourceVendor, PPDSourceHPLIP:
		// ok
	default:
		t.Errorf("Top-1 source = %q, want custom/vendor/hplip", top.Source)
	}
	if !top.Recommended {
		t.Error("Top-1 should be Recommended")
	}
	if top.Confidence != ppdConfidenceHigh {
		t.Errorf("Top-1 confidence = %q, want high", top.Confidence)
	}
	// hplip 项分数 > foomatic 项
	var hplipScore, foomaticScore int
	for _, c := range cands {
		if c.Source == PPDSourceHPLIP && hplipScore == 0 {
			hplipScore = c.Score
		}
		if c.Source == PPDSourceFoomatic && foomaticScore == 0 {
			foomaticScore = c.Score
		}
	}
	if hplipScore <= foomaticScore {
		t.Errorf("hplip score %d should > foomatic score %d", hplipScore, foomaticScore)
	}
	// generic 不在前 3
	for i, c := range cands {
		if i >= 3 {
			break
		}
		if c.Source == PPDSourceGeneric {
			t.Errorf("generic at position %d, should not be in top 3", i)
		}
	}
}

// ── 场景 2：Canon LBP2900 ──────────────────────────────────────────────────────

func TestScorePPD_CanonLBP2900(t *testing.T) {
	entries := parseFixture()
	cands := ScorePPDCandidates(entries, MatchInput{
		Manufacturer: "Canon",
		Model:        "LBP2900",
	}, 8)

	if len(cands) == 0 {
		t.Fatal("expected candidates, got none")
	}
	// CAPT 厂商项排在 foomatic 之前
	var vendorIdx, foomaticIdx = -1, -1
	for i, c := range cands {
		if c.Source == PPDSourceVendor && vendorIdx < 0 {
			vendorIdx = i
		}
		if c.Source == PPDSourceFoomatic && foomaticIdx < 0 {
			foomaticIdx = i
		}
	}
	if vendorIdx < 0 {
		t.Fatal("no vendor candidate found")
	}
	if foomaticIdx >= 0 && vendorIdx > foomaticIdx {
		t.Errorf("vendor at %d should be before foomatic at %d", vendorIdx, foomaticIdx)
	}
}

// ── 场景 3：LBP-2900 连字符归一化 ──────────────────────────────────────────────

func TestScorePPD_LBP2900Hyphen(t *testing.T) {
	entries := parseFixture()
	normal := ScorePPDCandidates(entries, MatchInput{
		Manufacturer: "Canon",
		Model:        "LBP2900",
	}, 8)
	hyphen := ScorePPDCandidates(entries, MatchInput{
		Manufacturer: "Canon",
		Model:        "LBP-2900",
	}, 8)

	if len(normal) == 0 || len(hyphen) == 0 {
		t.Fatal("expected candidates for both forms")
	}
	// compact 归一化后 Top-1 应该相同（"LBP-2900" ≡ "LBP2900"）
	if normal[0].PPD != hyphen[0].PPD {
		t.Errorf("Top-1 differs: normal=%q hyphen=%q", normal[0].PPD, hyphen[0].PPD)
	}
	if normal[0].Score != hyphen[0].Score {
		t.Errorf("Top-1 score differs: normal=%d hyphen=%d", normal[0].Score, hyphen[0].Score)
	}
}

// ── 场景 4：Epson L3250 + Series ───────────────────────────────────────────────

func TestScorePPD_EpsonL3250(t *testing.T) {
	entries := parseFixture()
	cands := ScorePPDCandidates(entries, MatchInput{
		Manufacturer: "Epson",
		Model:        "L3250",
	}, 8)

	if len(cands) == 0 {
		t.Fatal("expected candidates, got none")
	}
	// escpr 厂商项 > gutenprint 项
	var vendorIdx, gutenprintIdx = -1, -1
	for i, c := range cands {
		if c.Source == PPDSourceVendor && vendorIdx < 0 {
			vendorIdx = i
		}
		if c.Source == PPDSourceGutenprint && gutenprintIdx < 0 {
			gutenprintIdx = i
		}
	}
	if vendorIdx < 0 {
		t.Fatal("no vendor candidate found")
	}
	if gutenprintIdx >= 0 && vendorIdx > gutenprintIdx {
		t.Errorf("vendor at %d should be before gutenprint at %d", vendorIdx, gutenprintIdx)
	}
	// Top-1 confidence >= medium
	if cands[0].Confidence == ppdConfidenceLow {
		t.Errorf("Top-1 confidence = low, want >= medium")
	}
}

// ── 场景 4b：HP LaserJet Professional P1108（系列匹配 + professional→pro）────

func TestScorePPD_HPLaserJetP1108(t *testing.T) {
	entries := parseFixture()
	cands := ScorePPDCandidates(entries, MatchInput{
		Manufacturer: "HP",
		Model:        "LaserJet Professional P1108",
		PreferLang:   "zh",
	}, 8)

	if len(cands) == 0 {
		t.Fatal("expected candidates, got none")
	}
	// 应找到 P1102 Foomatic/foo2zjs-z2 PPD
	var p1102Found bool
	for _, c := range cands {
		if strings.Contains(c.PPD, "P1102") {
			p1102Found = true
			if c.Confidence == ppdConfidenceLow {
				t.Errorf("P1102 candidate confidence should be >= medium, got low (score=%d)", c.Score)
			}
			break
		}
	}
	if !p1102Found {
		t.Error("P1102 Foomatic/foo2zjs-z2 PPD not found in candidates")
		for i, c := range cands {
			t.Logf("  [%d] %s (source=%s score=%d)", i, c.PPD, c.Source, c.Score)
		}
	}
}

// ── 场景 5：空/垃圾型号（历史缺陷回归锁）──────────────────────────────────────

func TestScorePPD_GarbageModel(t *testing.T) {
	entries := parseFixture()
	for _, model := range []string{"", "L", "Unknown"} {
		cands := ScorePPDCandidates(entries, MatchInput{
			Manufacturer: "HP",
			Model:        model,
		}, 8)
		for _, c := range cands {
			if c.Recommended {
				t.Errorf("model=%q: candidate %q should not be Recommended", model, c.PPD)
			}
			if c.Source != PPDSourceGeneric {
				t.Errorf("model=%q: non-generic candidate %q (source=%s) should not appear",
					model, c.PPD, c.Source)
			}
		}
		// 绝不返回 fixture 第一条（everywhere）
		for _, c := range cands {
			if c.PPD == "everywhere" {
				t.Errorf("model=%q: should never return 'everywhere' for garbage model", model)
			}
		}
	}
}

// ── 场景 6：中文 locale 行 ─────────────────────────────────────────────────────

func TestScorePPD_ChineseLocale(t *testing.T) {
	entries := parseFixture()
	cands := ScorePPDCandidates(entries, MatchInput{
		Manufacturer: "HP",
		Model:        "LaserJet 1020",
		PreferLang:   "zh",
	}, 8)

	// zh 行应被识别
	var zhIdx = -1
	for i, c := range cands {
		if c.Language == "zh" {
			zhIdx = i
			break
		}
	}
	if zhIdx < 0 {
		t.Fatal("no zh-language candidate found")
	}
	// 同分时 zh 排在 en 之前（zh +8 vs en +0）
	for i := zhIdx + 1; i < len(cands); i++ {
		if cands[i].Source == PPDSourceFoomatic && cands[i].Language == "" {
			// 如果 foomatic en 项分数与 zh 项相同，zh 应在前
			if cands[i].Score >= cands[zhIdx].Score {
				t.Errorf("zh candidate (score %d) should rank before en candidate (score %d) at same tier",
					cands[zhIdx].Score, cands[i].Score)
			}
			break
		}
	}
}

// ── 场景 7：虚拟设备剔除 ───────────────────────────────────────────────────────

func TestScorePPD_VirtualExcluded(t *testing.T) {
	entries := parseFixture()
	cands := ScorePPDCandidates(entries, MatchInput{
		Manufacturer: "HP",
		Model:        "LaserJet 1020",
	}, 20)
	for _, c := range cands {
		if c.Source == PPDSourceVirtual {
			t.Errorf("virtual candidate %q should never appear", c.PPD)
		}
		if strings.Contains(strings.ToLower(c.MakeAndModel), "cups-pdf") {
			t.Errorf("CUPS-PDF candidate %q should never appear", c.PPD)
		}
		if strings.Contains(strings.ToLower(c.MakeAndModel), "text-only") {
			t.Errorf("Text-Only candidate %q should never appear", c.PPD)
		}
	}
}

// ── 场景 8：厂商冲突 ───────────────────────────────────────────────────────────

func TestScorePPD_MakeConflict(t *testing.T) {
	entries := parseFixture()
	cands := ScorePPDCandidates(entries, MatchInput{
		Manufacturer: "HP",
		Model:        "LaserJet 1020",
	}, 20)

	// Canon LaserJet 1020 clone 分数应低于所有 HP 项
	var cloneScore = -9999
	var minHPScore = 9999
	for _, c := range cands {
		if strings.Contains(c.MakeAndModel, "clone") {
			cloneScore = c.Score
		}
		if c.Source == PPDSourceHPLIP || c.Source == PPDSourceCustom {
			if c.Score < minHPScore {
				minHPScore = c.Score
			}
		}
	}
	if cloneScore == -9999 {
		t.Fatal("clone candidate not found")
	}
	if cloneScore >= minHPScore {
		t.Errorf("clone score %d should be < HP score %d (make conflict penalty)", cloneScore, minHPScore)
	}
}

// ── 场景 9/10/11：everywhere 优先级 ────────────────────────────────────────────

func TestScorePPD_EverywherePriority(t *testing.T) {
	entries := parseFixture()

	// 9: 有厂商项时 everywhere 排其后
	cands := ScorePPDCandidates(entries, MatchInput{
		Manufacturer:       "Canon",
		Model:              "LBP2900",
		EverywhereEvidence: 2,
	}, 8)
	var ewIdx, vendorIdx = -1, -1
	for i, c := range cands {
		if c.Source == PPDSourceEverywhere && ewIdx < 0 {
			ewIdx = i
		}
		if c.Source == PPDSourceVendor && vendorIdx < 0 {
			vendorIdx = i
		}
	}
	if ewIdx >= 0 && vendorIdx >= 0 && ewIdx < vendorIdx {
		t.Errorf("everywhere at %d should be after vendor at %d when vendor match exists", ewIdx, vendorIdx)
	}

	// 10: 无匹配项 + level 2 → everywhere 置顶
	cands = ScorePPDCandidates(entries, MatchInput{
		Manufacturer:       "Brother",
		Model:              "MFC-XXXX",
		EverywhereEvidence: 2,
	}, 8)
	if len(cands) == 0 {
		t.Fatal("expected candidates")
	}
	if cands[0].Source != PPDSourceEverywhere {
		t.Errorf("Top-1 should be everywhere when no match + level 2, got %s", cands[0].Source)
	}
	if !cands[0].Recommended {
		t.Error("everywhere Top-1 should be Recommended")
	}

	// 11: level 0 → 不出现
	cands = ScorePPDCandidates(entries, MatchInput{
		Manufacturer:       "HP",
		Model:              "LaserJet 1020",
		EverywhereEvidence: 0,
	}, 8)
	for _, c := range cands {
		if c.Source == PPDSourceEverywhere {
			t.Error("everywhere should not appear when evidence=0")
		}
	}
}

// ── 场景 12：driverd 加分量级 ──────────────────────────────────────────────────

func TestScorePPD_DriverdBonus(t *testing.T) {
	entries := parseFixture()
	base := ScorePPDCandidates(entries, MatchInput{
		Manufacturer: "HP",
		Model:        "LaserJet 1020",
	}, 8)
	boosted := ScorePPDCandidates(entries, MatchInput{
		Manufacturer: "HP",
		Model:        "LaserJet 1020",
		DriverdRanks: map[string]int{"foomatic-db-compressed-ppds:0/ppd/foomatic-ppd/HP-LaserJet_1020-foo2zjs.ppd": 1},
	}, 8)

	// 找 foomatic 项的分数
	var baseScore, boostedScore int
	for _, c := range base {
		if c.Source == PPDSourceFoomatic {
			baseScore = c.Score
			break
		}
	}
	for _, c := range boosted {
		if c.Source == PPDSourceFoomatic {
			boostedScore = c.Score
			break
		}
	}
	if boostedScore <= baseScore {
		t.Errorf("driverd rank 1 should boost score: base=%d boosted=%d", baseScore, boostedScore)
	}
	// tier 间距保证：driverd 最大加分 120（rank 1）< 最小 tier 间距 150（1000-850），
	// 数学上保证 driverd 不可能把弱 tier 抬过强 tier。
	// 同 tier 内 driverd 改变顺序是设计意图（指纹匹配比来源偏好更权威）。
	const maxDriverdBonus = 120 // 120 - min(1,10)*10
	const minTierGap = ppdTierExactWithMake - ppdTierBoundaryWithMake
	if maxDriverdBonus >= minTierGap {
		t.Errorf("driverd max bonus %d must be < min tier gap %d", maxDriverdBonus, minTierGap)
	}
}

// ── 场景 13：排序确定性 ────────────────────────────────────────────────────────

func TestScorePPD_Deterministic(t *testing.T) {
	entries := parseFixture()
	// 反转输入
	reversed := make([]string, len(ppdTestFixture))
	for i, line := range ppdTestFixture {
		reversed[len(ppdTestFixture)-1-i] = line
	}
	revEntries := ParsePPDLines(reversed)

	for _, in := range []MatchInput{
		{Manufacturer: "HP", Model: "LaserJet 1020"},
		{Manufacturer: "Canon", Model: "LBP2900"},
		{Manufacturer: "Epson", Model: "L3250"},
	} {
		normal := ScorePPDCandidates(entries, in, 8)
		rev := ScorePPDCandidates(revEntries, in, 8)
		if !reflect.DeepEqual(normal, rev) {
			t.Errorf("input order should not affect result for %s %s:\nnormal=%v\nrev=%v",
				in.Manufacturer, in.Model, normal, rev)
		}
	}
}

// ── 场景 14：解析健壮性 ────────────────────────────────────────────────────────

func TestParsePPDLines_Robustness(t *testing.T) {
	lines := []string{
		"",                          // 空行
		"   ",                       // 只有空格
		"onlyonecolumn",             // 只有一列
		"  leading-space.ppd Desc",  // 前导空格
		"valid.ppd Valid Printer\r", // CRLF
		"a  b   c   d",              // 多空格
	}
	// 不 panic 就行
	entries := ParsePPDLines(lines)
	if len(entries) != 3 { // leading-space, valid, a
		t.Errorf("expected 3 valid entries, got %d", len(entries))
	}
}

// ── 场景 15：ValidatePPDNameSyntax ─────────────────────────────────────────────

func TestValidatePPDNameSyntax(t *testing.T) {
	valid := []string{
		"everywhere",
		"drv:///sample.drv/generic.ppd",
		"gutenprint.5.3://escp2-l3250/expert",
		"foomatic:HP-LaserJet_1020-foo2zjs.ppd",
		"lsb/usr/HP/hp-laserjet_1020.ppd.gz",
		"custom/HP-LaserJet_1020-A4.ppd",
	}
	for _, s := range valid {
		if err := ValidatePPDNameSyntax(s); err != nil {
			t.Errorf("ValidatePPDNameSyntax(%q) = %v, want nil", s, err)
		}
	}

	invalid := []string{
		"",
		"../../etc/passwd",
		"/etc/shadow",
		"a b",
		"a;rm -rf /",
		"a$(x)",
		"a\nb",
		strings.Repeat("a", 513),
	}
	for _, s := range invalid {
		if err := ValidatePPDNameSyntax(s); err == nil {
			t.Errorf("ValidatePPDNameSyntax(%q) = nil, want error", s)
		}
	}
}

// ── 场景 16：uniquePrinterName ─────────────────────────────────────────────────

func TestUniquePrinterName(t *testing.T) {
	existing := map[string]string{
		"LaserJet_1020":   "usb://HP/LaserJet%201020?serial=A",
		"LaserJet_1020-2": "usb://HP/LaserJet%201020?serial=B",
	}
	name, isNew := uniquePrinterName("LaserJet_1020", existing)
	if name != "LaserJet_1020-3" {
		t.Errorf("expected LaserJet_1020-3, got %q", name)
	}
	if !isNew {
		t.Error("expected isNew=true")
	}

	// 全非法字符 → Printer
	name, _ = uniquePrinterName("!!!", existing)
	if name != "Printer" {
		t.Errorf("expected Printer for all-invalid base, got %q", name)
	}

	// 超长截断
	longBase := strings.Repeat("a", 200)
	name, _ = uniquePrinterName(longBase, existing)
	if len(name) > 127 {
		t.Errorf("name length %d exceeds 127", len(name))
	}

	// 全占满 → 报错
	full := make(map[string]string)
	full["X"] = "uri"
	for i := 2; i <= 50; i++ {
		full[fmt.Sprintf("X-%d", i)] = "uri"
	}
	_, err := uniquePrinterNameChecked("X", full)
	if err == nil {
		t.Error("expected error when all 50 slots taken")
	}
}

// ── 场景 17：parseDeviceURI 扩展 ───────────────────────────────────────────────

func TestParseDeviceURI(t *testing.T) {
	tests := []struct {
		uri       string
		wantMake  string
		wantModel string
	}{
		{"usb://Canon%20Inc./LBP2900?serial=X", "Canon Inc.", "LBP2900"},
		{"dnssd://Brother%20DCP-T425W._ipp._tcp.local/?uuid=abc", "Brother", "DCP-T425W"},
		{"socket://192.168.1.50:9100", "", ""},
		{"ipp://BRW001.local:631/ipp/print", "", ""},
	}
	for _, tt := range tests {
		make, model := parseDeviceURI(tt.uri)
		if make != tt.wantMake || model != tt.wantModel {
			t.Errorf("parseDeviceURI(%q) = (%q, %q), want (%q, %q)",
				tt.uri, make, model, tt.wantMake, tt.wantModel)
		}
	}
}

// ── 场景 18：parseLpinfoDevices 新字段 ─────────────────────────────────────────

func TestParseLpinfoDevices_NewFields(t *testing.T) {
	output := `Device: uri = usb://HP/LaserJet%201020?serial=XXXX
        class = direct
        info = HP LaserJet 1020
        make-and-model = HP LaserJet 1020
        device-id = MFG:HP;MDL:LaserJet 1020;CMD:PJL,MLC,PCL;
        location = Office

Device: uri = socket
        class = network
`
	printers := parseLpinfoDevices(output)
	if len(printers) != 1 {
		t.Fatalf("expected 1 printer, got %d", len(printers))
	}
	p := printers[0]
	// ⚠️ device URI 必须**逐字节原样保留**：%20 不能被解码成空格、`?serial=` 查询段
	// 不能被截断。CUPS 的 usb backend 生成的 URI 来自 IEEE-1284 Device ID 的 MFG/MDL
	// 字段，空格就是 %20；一旦我们在传给 `lpadmin -v` 之前动过它（解码、替换成下划线、
	// 或 url.Parse 往返），backend 就再也匹配不上"那一台"打印机，队列会永久停在
	// "Waiting for printer to become available"。
	// 这个不变量以前只靠代码自觉（parseDeviceURI 里的 PathUnescape 只用于推导型号，
	// 不碰 DeviceURI），没有回归护栏 —— 补上。
	if p.DeviceURI != "usb://HP/LaserJet%201020?serial=XXXX" {
		t.Errorf("DeviceURI 必须原样保留，got %q", p.DeviceURI)
	}
	if p.DeviceID != "MFG:HP;MDL:LaserJet 1020;CMD:PJL,MLC,PCL;" {
		t.Errorf("DeviceID = %q", p.DeviceID)
	}
	if p.MakeAndModel != "HP LaserJet 1020" {
		t.Errorf("MakeAndModel = %q", p.MakeAndModel)
	}
	if p.Location != "Office" {
		t.Errorf("Location = %q", p.Location)
	}
	// 裸 backend 行（socket 无 ://）仍被过滤
	if len(printers) != 1 {
		t.Error("bare backend line should be filtered")
	}
}

// ── 场景 19：detectDriverless scheme 门槛 ──────────────────────────────────────

func TestDriverlessSchemeGate(t *testing.T) {
	entries := parseFixture()
	tests := []struct {
		uri       string
		wantAvail bool
	}{
		{"usb://HP/LaserJet%201020", false},
		{"socket://192.168.1.50:9100", false},
		{"ipp://192.168.1.50/ipp/print", true},
		{"ipps://192.168.1.50:443/ipp/print", true},
		{"dnssd://HP%20LaserJet._ipp._tcp.local/", true},
	}
	for _, tt := range tests {
		info := driverlessSchemeCheck(tt.uri, entries)
		if info.Available != tt.wantAvail {
			t.Errorf("driverlessSchemeCheck(%q).Available = %v, want %v (reason: %s)",
				tt.uri, info.Available, tt.wantAvail, info.Reason)
		}
		if !tt.wantAvail && info.Reason == "" {
			t.Errorf("driverlessSchemeCheck(%q) should have non-empty Reason when unavailable", tt.uri)
		}
	}
}

// ── 场景 20：seriesTokenMatch 边界测试 ─────────────────────────────────────────

func TestSeriesTokenMatch(t *testing.T) {
	tests := []struct {
		name string
		dev  []string
		ppd  []string
		want bool
	}{
		{"P1108 vs P1102", []string{"laserjet", "pro", "p1108"}, []string{"laserjet", "pro", "p1102"}, true},
		{"L3250 vs L3251", []string{"l3250"}, []string{"l3251"}, true},
		{"L3250 vs L3150 (different series)", []string{"l3250"}, []string{"l3150"}, false},
		{"P1005 vs P1505 (different series)", []string{"laserjet", "p1005"}, []string{"laserjet", "p1505"}, false},
		{"MF3010 vs MF3014", []string{"mf3010"}, []string{"mf3014"}, true},
		{"empty dev", []string{}, []string{"p1102"}, false},
		{"no digit tokens", []string{"laserjet", "pro"}, []string{"laserjet", "pro"}, false},
		{"non-digit token mismatch", []string{"laserjet", "p1108"}, []string{"deskjet", "p1102"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := seriesTokenMatch(tt.dev, tt.ppd)
			if got != tt.want {
				t.Errorf("seriesTokenMatch(%v, %v) = %v, want %v", tt.dev, tt.ppd, got, tt.want)
			}
		})
	}
}

// ── 场景 21：NormalizeModelKey professional→pro 归一化 ────────────────────────

func TestNormalizeModelKey_ProfessionalToPro(t *testing.T) {
	_, compact, tokens := NormalizeModelKey("LaserJet Professional P1108")
	if compact != "laserjetprop1108" {
		t.Errorf("compact = %q, want %q", compact, "laserjetprop1108")
	}
	found := false
	for _, tok := range tokens {
		if tok == "professional" {
			t.Error("'professional' should have been replaced by 'pro'")
		}
		if tok == "pro" {
			found = true
		}
	}
	if !found {
		t.Errorf("'pro' not found in tokens: %v", tokens)
	}
}

// ── ClassifyPPDSource 单测 ─────────────────────────────────────────────────────

func TestClassifyPPDSource(t *testing.T) {
	tests := []struct {
		name string
		desc string
		want PPDSource
	}{
		{"everywhere", "IPP Everywhere", PPDSourceEverywhere},
		{"driverless:ipp://x", "IPP Everywhere", PPDSourceEverywhere},
		{"drv:///sample.drv/generic.ppd", "Generic PostScript Printer", PPDSourceGeneric},
		{"gutenprint.5.3://pcl-4/expert", "Generic PCL 4 Printer - CUPS+Gutenprint v5.3.4", PPDSourceGeneric},
		{"gutenprint.5.3://escp2-l3250/expert", "Epson L3250 - CUPS+Gutenprint v5.3.4", PPDSourceGutenprint},
		{"foomatic-db-compressed-ppds:0/ppd/foomatic-ppd/HP-LaserJet_1020-foo2zjs.ppd", "HP LaserJet 1020 Foomatic/foo2zjs (recommended)", PPDSourceFoomatic},
		{"drv:///hpijs.drv/hp-laserjet_1020-hpijs.ppd", "HP LaserJet 1020 hpijs, 3.22.10", PPDSourceHPLIP},
		{"openprinting-ppds:0/ppd/openprinting/HP/LaserJet_1020.ppd", "HP LaserJet 1020, hpcups 3.23.12", PPDSourceHPLIP},
		{"Canon/CNRCUPSLBP2900CAPTK.ppd", "Canon LBP2900 CAPT ver.2.71", PPDSourceVendor},
		{"escpr:0/cups/model/epson-inkjet-printer-escpr/Epson-L3250_Series.ppd", "EPSON L3250 Series, Epson Inkjet Printer Driver (ESC/P-R) for Linux", PPDSourceVendor},
		{"drv:///brlaser.drv/br1510.ppd", "Brother DCP-1510 series, using Owl-Maintain/brlaser v6.2.7", PPDSourceVendor},
		{"custom/HP.ppd", "HP LaserJet 1020", PPDSourceCustom},
		{"lsb/usr/cupsfilters/pxlmono.ppd", "Generic PCL Mono Printer", PPDSourceGeneric},
	}
	for _, tt := range tests {
		got := ClassifyPPDSource(tt.name, tt.desc)
		if got != tt.want {
			t.Errorf("ClassifyPPDSource(%q, %q) = %q, want %q", tt.name, tt.desc, got, tt.want)
		}
	}
}

// ── NormalizeModelKey 单测 ─────────────────────────────────────────────────────

func TestNormalizeModelKey(t *testing.T) {
	tests := []struct {
		input       string
		wantCompact string
	}{
		{"LBP2900", "lbp2900"},
		{"LBP-2900", "lbp2900"},
		{"LaserJet 1020", "laserjet1020"},
		{"L3250 Series", "l3250"},
		{"LaserJet 1020", "laserjet1020"},
		{"", ""},
	}
	for _, tt := range tests {
		_, compact, _ := NormalizeModelKey(tt.input)
		if compact != tt.wantCompact {
			t.Errorf("NormalizeModelKey(%q).compact = %q, want %q", tt.input, compact, tt.wantCompact)
		}
	}
}
