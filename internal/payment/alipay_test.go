package payment

// 支付宝渠道的离线测试。
//
// 手上没有商户资质也要能把协议测透：这里用两对【真实】的 RSA 密钥 ——
// 一对扮演应用私钥（签请求），一对扮演支付宝密钥（签响应），
// 再起一个 httptest 假网关。于是「签名对不对」「验签拦不拦得住篡改」
// 这些真正会出错的地方都是真的在跑，只有"对面是支付宝"这件事是假的。
//
// 🚫 不要把假网关简化成「不回签名」—— 那样测试会通过，但生产会全量验签失败。

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const testAppID = "2021000000000000"

// ─── 测试用的密钥与编码工具 ───────────────────────────────────────────

func genKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	// 2048 位是 RSA2 的下限，也是支付宝的要求。
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成测试密钥失败: %v", err)
	}
	return k
}

// privB64 把私钥编码成裸 base64（开放平台让用户粘贴的正是这个形态）。
func privB64(t *testing.T, k *rsa.PrivateKey, pkcs1 bool) string {
	t.Helper()
	var der []byte
	var err error
	if pkcs1 {
		der = x509.MarshalPKCS1PrivateKey(k)
	} else {
		der, err = x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			t.Fatalf("编码 PKCS8 私钥失败: %v", err)
		}
	}
	return base64.StdEncoding.EncodeToString(der)
}

// pubB64 把公钥编码成裸 base64（PKIX）。支付宝公钥就是这个形态。
func pubB64(t *testing.T, k *rsa.PublicKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(k)
	if err != nil {
		t.Fatalf("编码公钥失败: %v", err)
	}
	return base64.StdEncoding.EncodeToString(der)
}

// newTestAlipay 造一个渠道实例：私钥用 app，验签公钥用 alipayPub（渠道侧）。
func newTestAlipay(t *testing.T, appPriv *rsa.PrivateKey, alipayPub *rsa.PublicKey, gateway string) *Alipay {
	t.Helper()
	a, err := NewAlipay(AlipayConfig{
		AppID:      testAppID,
		PrivateKey: privB64(t, appPriv, false),
		PublicKey:  pubB64(t, alipayPub),
		Gateway:    gateway,
	})
	if err != nil {
		t.Fatalf("NewAlipay 失败: %v", err)
	}
	return a
}

// ─── 待签名串的拼装规则 ──────────────────────────────────────────────

func TestAlipaySignContent(t *testing.T) {
	cases := []struct {
		name            string
		params          url.Values
		includeSignType bool
		want            string
	}{
		{
			name:   "按 key 的 ASCII 升序，不是插入顺序",
			params: url.Values{"b": {"2"}, "a": {"1"}, "c": {"3"}},
			want:   "a=1&b=2&c=3",
		},
		{
			name:   "sign 永远排除",
			params: url.Values{"a": {"1"}, "sign": {"XXX"}},
			want:   "a=1",
		},
		{
			// 请求签名：网关把 sign_type 也算进验签串。实测踩过 ——
			// 少了它支付宝回 40002 / isv.invalid-signature。
			name:            "请求签名【包含】sign_type",
			params:          url.Values{"a": {"1"}, "sign": {"XXX"}, "sign_type": {"RSA2"}},
			includeSignType: true,
			want:            "a=1&sign_type=RSA2",
		},
		{
			// 通知验签：官方 SDK 的 rsaCheckV1 会同时移除 sign 与 sign_type。
			name:            "通知验签【排除】sign_type",
			params:          url.Values{"a": {"1"}, "sign": {"XXX"}, "sign_type": {"RSA2"}},
			includeSignType: false,
			want:            "a=1",
		},
		{
			name:   "跳过空值 —— 空值参与签名会导致验签失败",
			params: url.Values{"a": {"1"}, "empty": {""}, "b": {"2"}},
			want:   "a=1&b=2",
		},
		{
			name:   "值不做 URL 编码（签名针对原文）",
			params: url.Values{"biz_content": {`{"x":"a b&c"}`}},
			want:   `biz_content={"x":"a b&c"}`,
		},
		{
			name:   "全空时返回空串",
			params: url.Values{},
			want:   "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := alipaySignContent(c.params, c.includeSignType); got != c.want {
				t.Errorf("\n 得到 %q\n 期望 %q", got, c.want)
			}
		})
	}
}

// TestAlipayRequestSignContent_MatchesGateway 锁定「请求签名包含 sign_type」这条
// 曾经踩过的规则。
//
// 报文取自真实沙箱的报错回显：支付宝 40002 / isv.invalid-signature 时会把
// 它【实际使用的】验签字符串回显出来，这里用那一段做断言 —— 它是权威的。
// 🚫 不要把它改成「排除 sign_type」，那正是当初失败的原因。
func TestAlipayRequestSignContent_MatchesGateway(t *testing.T) {
	// 前面几项是网关回显里真实出现过的顺序与内容。
	params := url.Values{}
	params.Set("app_id", "2021000143616783")
	params.Set("biz_content", `{"out_trade_no":"DBG1","total_amount":"0.01","subject":"x","timeout_express":"15m"}`)
	params.Set("charset", "utf-8")
	params.Set("format", "JSON")
	params.Set("method", "alipay.trade.precreate")
	params.Set("sign_type", "RSA2")
	params.Set("timestamp", "2026-09-22 11:38:11")
	params.Set("version", "1.0")
	params.Set("sign", "IGNORED")

	want := `app_id=2021000143616783` +
		`&biz_content={"out_trade_no":"DBG1","total_amount":"0.01","subject":"x","timeout_express":"15m"}` +
		`&charset=utf-8&format=JSON&method=alipay.trade.precreate` +
		`&sign_type=RSA2&timestamp=2026-09-22 11:38:11&version=1.0`

	if got := alipaySignContent(params, true); got != want {
		t.Errorf("请求签名串与网关回显不一致：\n 得到 %s\n 期望 %s", got, want)
	}
}

// ─── 金额换算 ────────────────────────────────────────────────────────

func TestCentsToYuan(t *testing.T) {
	cases := []struct {
		cents int64
		want  string
	}{
		{0, "0.00"},
		{1, "0.01"},
		{20, "0.20"},
		{80, "0.80"},
		{100, "1.00"},
		{1234, "12.34"},
		{100000, "1000.00"},
	}
	for _, c := range cases {
		if got := centsToYuan(c.cents); got != c.want {
			t.Errorf("centsToYuan(%d) = %q，期望 %q", c.cents, got, c.want)
		}
	}
}

func TestYuanToCents(t *testing.T) {
	ok := []struct {
		in   string
		want int64
	}{
		{"0.01", 1},
		{"0.80", 80},
		{"1", 100},
		{"1.00", 100},
		{"12.34", 1234},
		{" 0.80 ", 80}, // 支付宝偶尔带空白
		{"0.800", 80},  // 多余位数全零，允许
		{"1000.00", 100000},
	}
	for _, c := range ok {
		got, err := yuanToCents(c.in)
		if err != nil {
			t.Errorf("yuanToCents(%q) 不该报错: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("yuanToCents(%q) = %d，期望 %d", c.in, got, c.want)
		}
	}

	// 金额上宁可失败也不要静默丢精度。
	bad := []string{"", "abc", "1.2.3", "0.805", "1e3", "-0.805"}
	for _, in := range bad {
		if got, err := yuanToCents(in); err == nil {
			t.Errorf("yuanToCents(%q) 应当报错，却得到 %d", in, got)
		}
	}
}

// TestMoneyRoundTrip 金额必须在「分 → 元 → 分」后原样返回。
// 这是最该守住的性质：任何一环丢精度都会变成真实的账目差错。
func TestMoneyRoundTrip(t *testing.T) {
	for cents := int64(1); cents <= 200000; cents += 7 {
		s := centsToYuan(cents)
		back, err := yuanToCents(s)
		if err != nil {
			t.Fatalf("cents=%d → %q 回转失败: %v", cents, s, err)
		}
		if back != cents {
			t.Fatalf("cents=%d → %q → %d，丢失了精度", cents, s, back)
		}
	}
}

// ─── 密钥解析 ────────────────────────────────────────────────────────

func TestParseAlipayPrivateKey(t *testing.T) {
	k := genKey(t)

	pkcs8B64 := privB64(t, k, false)
	pkcs1B64 := privB64(t, k, true)

	pkcs8PEM := string(pem.EncodeToMemory(&pem.Block{
		Type: "PRIVATE KEY", Bytes: mustDER(t, k, false),
	}))
	pkcs1PEM := string(pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k),
	}))

	// 裸 base64 中间被编辑器插了换行 —— 实际粘贴时非常常见。
	withNewlines := pkcs8B64[:40] + "\n" + pkcs8B64[40:80] + "\r\n " + pkcs8B64[80:]

	cases := []struct {
		name string
		in   string
	}{
		{"PKCS8 裸 base64", pkcs8B64},
		{"PKCS1 裸 base64", pkcs1B64},
		{"PKCS8 PEM", pkcs8PEM},
		{"PKCS1 PEM", pkcs1PEM},
		{"带换行与空格的 base64", withNewlines},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseAlipayPrivateKey(c.in)
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if got.N.Cmp(k.N) != 0 {
				t.Error("解析出来的不是同一把密钥")
			}
		})
	}

	for _, bad := range []string{"", "不是密钥", base64.StdEncoding.EncodeToString([]byte("garbage"))} {
		if _, err := parseAlipayPrivateKey(bad); err == nil {
			t.Errorf("输入 %q 应当报错", bad)
		}
	}
}

func mustDER(t *testing.T, k *rsa.PrivateKey, pkcs1 bool) []byte {
	t.Helper()
	if pkcs1 {
		return x509.MarshalPKCS1PrivateKey(k)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	return der
}

func TestParseAlipayPublicKey(t *testing.T) {
	k := genKey(t)

	if _, err := parseAlipayPublicKey(pubB64(t, &k.PublicKey)); err != nil {
		t.Fatalf("PKIX 公钥解析失败: %v", err)
	}
	pkcs1 := base64.StdEncoding.EncodeToString(x509.MarshalPKCS1PublicKey(&k.PublicKey))
	if _, err := parseAlipayPublicKey(pkcs1); err != nil {
		t.Fatalf("PKCS1 公钥解析失败: %v", err)
	}

	// 把【私钥】当公钥传是很容易犯的错，必须明确报错而不是静默通过。
	if _, err := parseAlipayPublicKey(privB64(t, k, false)); err == nil {
		t.Error("把私钥当公钥用了，应当报错")
	}
}

// ─── 原始响应节点的切分 ──────────────────────────────────────────────

func TestRawResponseNode(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		key  string
		want string
	}{
		{
			name: "普通对象",
			raw:  `{"alipay_trade_query_response":{"code":"10000"},"sign":"AAA"}`,
			key:  "alipay_trade_query_response",
			want: `{"code":"10000"}`,
		},
		{
			name: "嵌套对象",
			raw:  `{"a":{"b":{"c":1},"d":2},"sign":"X"}`,
			key:  "a",
			want: `{"b":{"c":1},"d":2}`,
		},
		{
			name: "字符串里的花括号不能干扰配平",
			raw:  `{"a":{"msg":"} { 干扰","n":1},"sign":"X"}`,
			key:  "a",
			want: `{"msg":"} { 干扰","n":1}`,
		},
		{
			name: "转义引号不能提前结束字符串",
			raw:  `{"a":{"msg":"他说\"}\"完了","n":1},"sign":"X"}`,
			key:  "a",
			want: `{"msg":"他说\"}\"完了","n":1}`,
		},
		{
			name: "数组值",
			raw:  `{"a":[{"x":1},{"y":2}],"sign":"X"}`,
			key:  "a",
			want: `[{"x":1},{"y":2}]`,
		},
		{
			name: "前面的无关节点不能干扰",
			raw:  `{"other":{"z":1},"target":{"k":"v"},"sign":"X"}`,
			key:  "target",
			want: `{"k":"v"}`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := rawResponseNode([]byte(c.raw), c.key)
			if err != nil {
				t.Fatalf("切分失败: %v", err)
			}
			if string(got) != c.want {
				t.Errorf("\n 得到 %s\n 期望 %s", got, c.want)
			}
		})
	}

	if _, err := rawResponseNode([]byte(`{"a":1}`), "missing"); err == nil {
		t.Error("节点不存在时应当报错")
	}
}

// ─── 完整下单流程 ────────────────────────────────────────────────────

// fakeGateway 扮演支付宝网关：校验请求签名，并按支付宝的格式签名返回。
type fakeGateway struct {
	appPub     *rsa.PublicKey
	alipayPriv *rsa.PrivateKey

	// 记录最后一次收到的请求参数，供断言。
	lastParams url.Values
	// respond 由各用例定制响应节点。
	respond func(params url.Values) (nodeKey, node string, code int)
	// tamper 为真时故意破坏响应签名。
	tamper bool
}

func (g *fakeGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := readAll(r)
	params, err := url.ParseQuery(body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	g.lastParams = params

	// 校验请求签名：这一步证明【被测代码签出来的东西是对的】。
	//
	// ⚠️ 用 true（包含 sign_type）—— 与真实网关一致。这个假网关当初就是
	// 和被测代码犯了同一个错（都排除 sign_type）才让 42 个用例全绿，
	// 直到真实沙箱指出 isv.invalid-signature。别再改回去。
	sign := params.Get("sign")
	if sign == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("no sign"))
		return
	}
	if err := alipayVerify(alipaySignContent(params, true), sign, g.appPub); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("bad request signature"))
		return
	}

	nodeKey, node, code := g.respond(params)
	out := signNodeRaw(nodeKey, node, g.alipayPriv)
	if g.tamper {
		// 改掉签名里的一个字符，模拟篡改。
		out = strings.Replace(out, `"sign":"`, `"sign":"A`, 1)
	}
	w.Header().Set("Content-Type", "application/json;charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(out))
}

func readAll(r *http.Request) (string, error) {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			return string(buf), nil
		}
	}
}

// signNodeRaw 按支付宝的真实格式包一层：
//
//	{ "<key>": <node 原文>, "sign": "<对 node 原文的签名>" }
//
// ⚠️ 必须手工拼字符串，不能 Marshal 一个 struct —— 被测代码是从原始字节里
// 切出 node 的，用 struct 序列化会把「切出来的字节与签名的字节不一致」
// 这个真实存在的坑掩盖掉（那正是 rawResponseNode 存在的理由）。
func signNodeRaw(key, node string, priv *rsa.PrivateKey) string {
	sign, err := alipaySign(node, priv)
	if err != nil {
		return fmt.Sprintf(`{"%s":%s,"sign":""}`, key, node)
	}
	return fmt.Sprintf(`{"%s":%s,"sign":%q}`, key, node, sign)
}

func TestAlipayCreate(t *testing.T) {
	appPriv, alipayPriv := genKey(t), genKey(t)
	const qr = "https://qr.alipay.com/bax0123456789"

	gw := &fakeGateway{
		appPub: &appPriv.PublicKey, alipayPriv: alipayPriv,
		respond: func(params url.Values) (string, string, int) {
			if got := params.Get("method"); got != "alipay.trade.precreate" {
				t.Errorf("method 应为 alipay.trade.precreate，实际 %q", got)
			}
			var biz struct {
				OutTradeNo  string `json:"out_trade_no"`
				TotalAmount string `json:"total_amount"`
			}
			if err := json.Unmarshal([]byte(params.Get("biz_content")), &biz); err != nil {
				t.Errorf("biz_content 不是合法 JSON: %v", err)
			}
			if biz.TotalAmount != "0.80" {
				t.Errorf("total_amount 应为 0.80，实际 %q", biz.TotalAmount)
			}
			return "alipay_trade_precreate_response",
				fmt.Sprintf(`{"code":"10000","msg":"Success","out_trade_no":%q,"qr_code":%q}`,
					biz.OutTradeNo, qr), http.StatusOK
		},
	}
	srv := httptest.NewServer(gw)
	defer srv.Close()

	a := newTestAlipay(t, appPriv, &alipayPriv.PublicKey, srv.URL)
	cred, err := a.Create(context.Background(), Request{
		OutTradeNo:  "CW20260922120000ABCDEF",
		AmountCents: 80,
		Subject:     "打印费用",
		ExpiresAt:   time.Now().Add(15 * time.Minute),
	})
	if err != nil {
		t.Fatalf("Create 失败: %v", err)
	}
	if cred.QRCodeText != qr {
		t.Errorf("qr_code = %q，期望 %q", cred.QRCodeText, qr)
	}
	// ⚠️ PayURL 必须与 qr_code 同值。
	//
	// 当面付的 qr_code 本身就是个 URL（https://qr.alipay.com/...）。手机端
	// 拿它【直接打开】唤起支付宝，电脑端把它渲染成二维码 —— 同一个值两种用法。
	//
	// 前端「去支付宝付款」按钮的显示条件就是 cred.payUrl 非空。这里一旦漏填，
	// 手机用户会看到一个空白区域：按钮消失、二维码又被判定为移动端收起，
	// 整个支付界面无路可走。
	if cred.PayURL != qr {
		t.Errorf("pay_url = %q，期望与 qr_code 同值 %q", cred.PayURL, qr)
	}

	// 公共参数该带上的一样都不能少。
	for k, want := range map[string]string{
		"app_id": testAppID, "format": "JSON", "charset": "utf-8",
		"sign_type": "RSA2", "version": "1.0",
	} {
		if got := gw.lastParams.Get(k); got != want {
			t.Errorf("参数 %s = %q，期望 %q", k, got, want)
		}
	}
	if gw.lastParams.Get("timestamp") == "" {
		t.Error("缺少 timestamp")
	}
}

// ─── 手机网站支付（wap.pay）──────────────────────────────────────────

// TestAlipayCreateWap 盯住手机网站支付这条路。
//
// ⚠️ 这条路的失败模式特别隐蔽，所以这个测试必须自己把签名验一遍：
// wap.pay 是「页面跳转类」接口，我们只是把签名好的参数拼进收银台地址交给
// 浏览器，**全程不联系支付宝**。于是签名写错、参数漏了，在我们这边一律
// 表现为「成功返回一个字符串」—— 错误只在用户手机上变成一个支付宝错误页。
// 假网关在这条路上帮不上忙（它根本不会被调用），只能在这里硬验。
func TestAlipayCreateWap(t *testing.T) {
	appPriv, alipayPriv := genKey(t), genKey(t)
	a := newTestAlipay(t, appPriv, &alipayPriv.PublicKey, AlipayDefaultGateway)

	const returnURL = "http://192.168.1.22:1180/#/wallet"

	cred, err := a.Create(context.Background(), Request{
		OutTradeNo:  "CW20260929120000ABCDEF",
		AmountCents: 80,
		Subject:     "打印费用",
		Channel:     ChannelWap,
		ReturnURL:   returnURL,
		ExpiresAt:   time.Now().Add(15 * time.Minute),
	})
	if err != nil {
		t.Fatalf("Create(wap) 失败: %v", err)
	}

	// 这条路上没有码可扫 —— 前端正是靠这个空值判断「该跳转，不是该显示二维码」。
	if cred.QRCodeText != "" {
		t.Errorf("wap 不该带二维码内容，实际 %q", cred.QRCodeText)
	}
	if !strings.HasPrefix(cred.PayURL, AlipayDefaultGateway+"?") {
		t.Fatalf("PayURL 不是网关地址: %q", cred.PayURL)
	}

	u, err := url.Parse(cred.PayURL)
	if err != nil {
		t.Fatalf("PayURL 不是合法 URL: %v", err)
	}
	q := u.Query()

	if got := q.Get("method"); got != "alipay.trade.wap.pay" {
		t.Errorf("method = %q", got)
	}
	if got := q.Get("return_url"); got != returnURL {
		t.Errorf("return_url = %q，期望 %q", got, returnURL)
	}

	// 请求签名要【包含】sign_type。用通知那套拼法（排除 sign_type）验
	// 必须【验不过】—— 否则说明拼串的方式碰巧两边都能过，那就等于没测。
	if err := alipayVerify(alipaySignContent(q, true), q.Get("sign"), &appPriv.PublicKey); err != nil {
		t.Errorf("收银台地址的签名验不过（用户在手机上会看到支付宝错误页）: %v", err)
	}
	if err := alipayVerify(alipaySignContent(q, false), q.Get("sign"), &appPriv.PublicKey); err == nil {
		t.Error("用通知的拼法（排除 sign_type）也能验过，说明签名串拼错了")
	}

	var biz struct {
		OutTradeNo  string `json:"out_trade_no"`
		TotalAmount string `json:"total_amount"`
		ProductCode string `json:"product_code"`
		QuitURL     string `json:"quit_url"`
		Timeout     string `json:"timeout_express"`
	}
	if err := json.Unmarshal([]byte(q.Get("biz_content")), &biz); err != nil {
		t.Fatalf("biz_content 不是合法 JSON: %v", err)
	}
	if biz.ProductCode != alipayWapProductCode {
		t.Errorf("product_code = %q，期望 %q", biz.ProductCode, alipayWapProductCode)
	}
	if biz.TotalAmount != "0.80" {
		t.Errorf("total_amount = %q，期望 0.80", biz.TotalAmount)
	}
	// 用户在收银台点「返回/放弃」时该落回我们这儿，而不是留在支付宝的地盘上。
	if biz.QuitURL != returnURL {
		t.Errorf("quit_url = %q，期望与 return_url 同址 %q", biz.QuitURL, returnURL)
	}
	// 不传的话收银台会活得比订单还久，出现「还能付，但订单已经过期回收了」。
	if biz.Timeout == "" {
		t.Error("没传 timeout_express")
	}
}

// TestAlipayCreateWapNeedsReturnURL 盯住「没有回跳地址就别发链接出去」。
//
// 缺 return_url 时用户付完会停在支付宝自己的结果页上，还得自己找回我们的
// 页面 —— 那正是改用官方方案要解决的问题本身。宁可在这里断掉，让上层降级。
func TestAlipayCreateWapNeedsReturnURL(t *testing.T) {
	appPriv, alipayPriv := genKey(t), genKey(t)
	a := newTestAlipay(t, appPriv, &alipayPriv.PublicKey, AlipayDefaultGateway)

	_, err := a.Create(context.Background(), Request{
		OutTradeNo:  "CW20260929120000ABCDEF",
		AmountCents: 80,
		Channel:     ChannelWap,
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("期望 ErrUnavailable，实际 %v", err)
	}
}

func TestAlipayCreate_ResponseTampered(t *testing.T) {
	appPriv, alipayPriv := genKey(t), genKey(t)
	gw := &fakeGateway{
		appPub: &appPriv.PublicKey, alipayPriv: alipayPriv, tamper: true,
		respond: func(url.Values) (string, string, int) {
			return "alipay_trade_precreate_response",
				`{"code":"10000","qr_code":"https://qr.alipay.com/x"}`, http.StatusOK
		},
	}
	srv := httptest.NewServer(gw)
	defer srv.Close()

	a := newTestAlipay(t, appPriv, &alipayPriv.PublicKey, srv.URL)
	_, err := a.Create(context.Background(), Request{OutTradeNo: "X", AmountCents: 1})
	if err == nil {
		t.Fatal("响应签名被篡改，Create 必须失败")
	}
	if !errors.Is(err, ErrBadSignature) {
		t.Errorf("错误应包裹 ErrBadSignature，实际 %v", err)
	}
}

// TestAlipayCreate_RejectedByChannel 渠道返回业务失败码时要如实报错，
// 不能把它当成「下单成功但没二维码」。
func TestAlipayCreate_RejectedByChannel(t *testing.T) {
	appPriv, alipayPriv := genKey(t), genKey(t)
	gw := &fakeGateway{
		appPub: &appPriv.PublicKey, alipayPriv: alipayPriv,
		respond: func(url.Values) (string, string, int) {
			return "alipay_trade_precreate_response",
				`{"code":"40004","msg":"Business Failed","sub_code":"ACQ.INVALID_PARAMETER","sub_msg":"金额不合法"}`,
				http.StatusOK
		},
	}
	srv := httptest.NewServer(gw)
	defer srv.Close()

	a := newTestAlipay(t, appPriv, &alipayPriv.PublicKey, srv.URL)
	_, err := a.Create(context.Background(), Request{OutTradeNo: "X", AmountCents: 1})
	if err == nil {
		t.Fatal("渠道拒绝时 Create 必须失败")
	}
	if !strings.Contains(err.Error(), "ACQ.INVALID_PARAMETER") {
		t.Errorf("错误信息应带上 sub_code 便于排障，实际: %v", err)
	}
}

// ─── 查单 ────────────────────────────────────────────────────────────

func TestAlipayQuery(t *testing.T) {
	appPriv, alipayPriv := genKey(t), genKey(t)

	cases := []struct {
		name      string
		node      string
		wantPaid  bool
		wantCents int64
		wantTxID  string
	}{
		{
			name:      "支付成功",
			node:      `{"code":"10000","msg":"Success","trade_no":"2026092222001","out_trade_no":"CW1","trade_status":"TRADE_SUCCESS","total_amount":"0.80"}`,
			wantPaid:  true,
			wantCents: 80,
			wantTxID:  "2026092222001",
		},
		{
			name:      "交易已完结同样算已付",
			node:      `{"code":"10000","trade_no":"T2","out_trade_no":"CW1","trade_status":"TRADE_FINISHED","total_amount":"1.00"}`,
			wantPaid:  true,
			wantCents: 100,
			wantTxID:  "T2",
		},
		{
			name:      "等待付款不算已付",
			node:      `{"code":"10000","out_trade_no":"CW1","trade_status":"WAIT_BUYER_PAY","total_amount":"0.80"}`,
			wantPaid:  false,
			wantCents: 80,
		},
		{
			name:      "交易关闭不算已付",
			node:      `{"code":"10000","out_trade_no":"CW1","trade_status":"TRADE_CLOSED","total_amount":"0.80"}`,
			wantPaid:  false,
			wantCents: 80,
		},
		{
			name:      "查无此单不报错，只是未支付",
			node:      `{"code":"40004","msg":"Business Failed","sub_code":"ACQ.TRADE_NOT_EXIST","sub_msg":"交易不存在"}`,
			wantPaid:  false,
			wantCents: 0,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			node := c.node
			gw := &fakeGateway{
				appPub: &appPriv.PublicKey, alipayPriv: alipayPriv,
				respond: func(params url.Values) (string, string, int) {
					if got := params.Get("method"); got != "alipay.trade.query" {
						t.Errorf("method 应为 alipay.trade.query，实际 %q", got)
					}
					return "alipay_trade_query_response", node, http.StatusOK
				},
			}
			srv := httptest.NewServer(gw)
			defer srv.Close()

			a := newTestAlipay(t, appPriv, &alipayPriv.PublicKey, srv.URL)
			res, err := a.Query(context.Background(), "CW1")
			if err != nil {
				t.Fatalf("Query 失败: %v", err)
			}
			if res.Paid != c.wantPaid {
				t.Errorf("Paid = %v，期望 %v", res.Paid, c.wantPaid)
			}
			if res.AmountCents != c.wantCents {
				t.Errorf("AmountCents = %d，期望 %d", res.AmountCents, c.wantCents)
			}
			if c.wantTxID != "" && res.TransactionID != c.wantTxID {
				t.Errorf("TransactionID = %q，期望 %q", res.TransactionID, c.wantTxID)
			}
		})
	}
}

// ─── 回调验签 ────────────────────────────────────────────────────────

// buildNotify 构造一条按支付宝规则签名的异步通知。
//
// ⚠️ 通知侧用 false（排除 sign_type）—— 官方 SDK 的 rsaCheckV1 就是这么做的，
// 与请求签名相反。真实沙箱不验通知（要公网地址），所以这条规则只能靠文档，
// 不能像请求那样靠回显实测 —— 改这里之前先确认 rsaCheckV1 的语义没变。
func buildNotify(t *testing.T, priv *rsa.PrivateKey, params url.Values) []byte {
	t.Helper()
	params.Set("sign_type", "RSA2")
	sign, err := alipaySign(alipaySignContent(params, false), priv)
	if err != nil {
		t.Fatalf("签回调失败: %v", err)
	}
	params.Set("sign", sign)
	return []byte(params.Encode())
}

func notifyParams() url.Values {
	return url.Values{
		"app_id":       {testAppID},
		"out_trade_no": {"CW20260922120000ABCDEF"},
		"trade_no":     {"2026092222001"},
		"trade_status": {"TRADE_SUCCESS"},
		"total_amount": {"0.80"},
		"notify_time":  {"2026-09-22 12:00:00"},
	}
}

func TestAlipayVerifyCallback(t *testing.T) {
	appPriv, alipayPriv := genKey(t), genKey(t)
	a := newTestAlipay(t, appPriv, &alipayPriv.PublicKey, "http://unused")

	body := buildNotify(t, alipayPriv, notifyParams())
	req := httptest.NewRequest(http.MethodPost, "/api/payments/callback/alipay", nil)

	res, err := a.VerifyCallback(req, body)
	if err != nil {
		t.Fatalf("验签应当通过: %v", err)
	}
	if !res.Paid {
		t.Error("TRADE_SUCCESS 应判为已支付")
	}
	if res.AmountCents != 80 {
		t.Errorf("AmountCents = %d，期望 80", res.AmountCents)
	}
	if res.OutTradeNo != "CW20260922120000ABCDEF" {
		t.Errorf("OutTradeNo = %q", res.OutTradeNo)
	}
	if res.TransactionID != "2026092222001" {
		t.Errorf("TransactionID = %q", res.TransactionID)
	}
}

// TestAlipayVerifyCallback_Tampered 篡改金额后验签必须失败。
// 这是整个回调链路上最要紧的一条：验签是公开接口的唯一身份证明。
func TestAlipayVerifyCallback_Tampered(t *testing.T) {
	appPriv, alipayPriv := genKey(t), genKey(t)
	a := newTestAlipay(t, appPriv, &alipayPriv.PublicKey, "http://unused")

	body := buildNotify(t, alipayPriv, notifyParams())
	// 把 0.80 改成 0.01（签名不变）
	tampered := strings.Replace(string(body), "total_amount=0.80", "total_amount=0.01", 1)

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	_, err := a.VerifyCallback(req, []byte(tampered))
	if err == nil {
		t.Fatal("金额被篡改，验签必须失败")
	}
	if !errors.Is(err, ErrBadSignature) {
		t.Errorf("错误应包裹 ErrBadSignature，实际 %v", err)
	}
}

// TestAlipayVerifyCallback_WrongAppID 验签通过但 app_id 不是我们的 —— 必须拒绝。
//
// 场景：同一个开放平台账号下有多个应用，用同一把支付宝私钥签名，
// 于是别人应用的通知也能通过验签。只验签不核对 app_id 就会给别人发货。
func TestAlipayVerifyCallback_WrongAppID(t *testing.T) {
	appPriv, alipayPriv := genKey(t), genKey(t)
	a := newTestAlipay(t, appPriv, &alipayPriv.PublicKey, "http://unused")

	params := notifyParams()
	params.Set("app_id", "9999999999999999") // 别人的应用
	body := buildNotify(t, alipayPriv, params)

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	_, err := a.VerifyCallback(req, body)
	if err == nil {
		t.Fatal("app_id 不匹配必须拒绝")
	}
	if !errors.Is(err, ErrBadSignature) {
		t.Errorf("错误应包裹 ErrBadSignature，实际 %v", err)
	}
}

// TestAlipayVerifyCallback_Closed 交易关闭的通知：验签通过，但 Paid=false。
// 调用方据此【不结算】—— 这是 channel 与结算之间最重要的一条契约。
func TestAlipayVerifyCallback_Closed(t *testing.T) {
	appPriv, alipayPriv := genKey(t), genKey(t)
	a := newTestAlipay(t, appPriv, &alipayPriv.PublicKey, "http://unused")

	params := notifyParams()
	params.Set("trade_status", "TRADE_CLOSED")
	body := buildNotify(t, alipayPriv, params)

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	res, err := a.VerifyCallback(req, body)
	if err != nil {
		t.Fatalf("验签应当通过: %v", err)
	}
	if res.Paid {
		t.Error("TRADE_CLOSED 不能判为已支付 —— 那会凭空发货")
	}
}

func TestAlipayVerifyCallback_NoSign(t *testing.T) {
	appPriv, alipayPriv := genKey(t), genKey(t)
	a := newTestAlipay(t, appPriv, &alipayPriv.PublicKey, "http://unused")

	body := []byte(notifyParams().Encode()) // 没有 sign
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	if _, err := a.VerifyCallback(req, body); err == nil {
		t.Fatal("没有签名的回调必须拒绝")
	}
}

func TestAlipayCallbackAck(t *testing.T) {
	a, err := NewAlipay(AlipayConfig{
		AppID: testAppID, PrivateKey: privB64(t, genKey(t), false),
		PublicKey: pubB64(t, &genKey(t).PublicKey),
	})
	if err != nil {
		t.Fatalf("NewAlipay 失败: %v", err)
	}
	code, body := a.CallbackAck()
	if code != http.StatusOK {
		t.Errorf("状态码 = %d，期望 200", code)
	}
	// 支付宝只认 "success" 这一个字面量，回别的会持续重试投递。
	if string(body) != "success" {
		t.Errorf("应答体 = %q，必须恰好是 success", body)
	}
}

// ─── 配置与格式细节 ──────────────────────────────────────────────────

func TestNewAlipay_ConfigValidation(t *testing.T) {
	priv, pub := genKey(t), genKey(t)
	privStr, pubStr := privB64(t, priv, false), pubB64(t, &pub.PublicKey)

	if _, err := NewAlipay(AlipayConfig{PrivateKey: privStr, PublicKey: pubStr}); !errors.Is(err, ErrUnavailable) {
		t.Error("缺 AppID 应返回 ErrUnavailable")
	}
	if _, err := NewAlipay(AlipayConfig{AppID: testAppID, PrivateKey: "坏的", PublicKey: pubStr}); !errors.Is(err, ErrUnavailable) {
		t.Error("私钥非法应返回 ErrUnavailable")
	}
	if _, err := NewAlipay(AlipayConfig{AppID: testAppID, PrivateKey: privStr, PublicKey: "坏的"}); !errors.Is(err, ErrUnavailable) {
		t.Error("公钥非法应返回 ErrUnavailable")
	}

	a, err := NewAlipay(AlipayConfig{AppID: testAppID, PrivateKey: privStr, PublicKey: pubStr})
	if err != nil {
		t.Fatalf("合法配置不该报错: %v", err)
	}
	if a.Name() != "alipay" {
		t.Errorf("Name() = %q", a.Name())
	}
	if a.gateway != AlipayDefaultGateway {
		t.Errorf("未指定网关时应回落到正式网关，实际 %q", a.gateway)
	}
}

func TestAlipayTimeoutExpress(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{15 * time.Minute, "15m"},
		{30 * time.Second, "1m"},  // 低于渠道下限，夹到 1m
		{48 * time.Hour, "1440m"}, // 高于渠道上限，夹到 24h
		{90 * time.Second, "2m"},  // 向上取整，宁可多活不要早失效
	}
	for _, c := range cases {
		if got := alipayTimeoutExpress(c.in); got != c.want {
			t.Errorf("alipayTimeoutExpress(%v) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// TestAlipayTimestampIsCST 时间戳必须是东八区。
//
// ⚠️ 容器默认 UTC。这里若用本地时间，时间戳会差 8 小时，
// 而开发机多半就在东八区 —— 这个 bug 只会在容器里炸。
func TestAlipayTimestampIsCST(t *testing.T) {
	got := alipayTimestamp()
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", got, alipayLocation())
	if err != nil {
		t.Fatalf("时间戳格式不对 %q: %v", got, err)
	}
	// 与 UTC 下的当前时间比对：换算回 UTC 后应当接近 now。
	diff := time.Since(parsed.UTC())
	if diff < -time.Minute || diff > time.Minute {
		t.Errorf("时间戳 %q（UTC %v）与当前时间相差 %v，时区很可能用错了",
			got, parsed.UTC(), diff)
	}
}

func TestAlipaySubject(t *testing.T) {
	if got := alipaySubject("  "); got != "打印费用" {
		t.Errorf("空标题应回落到默认值，实际 %q", got)
	}
	long := strings.Repeat("打", 200)
	if got := []rune(alipaySubject(long)); len(got) != 100 {
		t.Errorf("超长标题应截断到 100 字，实际 %d", len(got))
	}
}
