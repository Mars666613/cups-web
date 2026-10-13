package payment

// 支付宝授权登录（OAuth）的离线测试。
//
// 与 alipay_test.go 同一套路：真 RSA 密钥 + httptest 假网关，于是「签名对不对、
// 验签拦不拦得住篡改」都是真的在跑，只有"对面是支付宝"是假的。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const testOpenID = "074a1CcTG1LelxKe4xQC0zgNdId0nxi95b5lsNpazWYoCo5"

// ─── 授权链接 ────────────────────────────────────────────────────────

func TestAlipayAuthorizeURL(t *testing.T) {
	appPriv, alipayPriv := genKey(t), genKey(t)
	a := newTestAlipay(t, appPriv, &alipayPriv.PublicKey, "")

	// 真实形态：带 hash 的回调地址（前端是 hash 路由）。
	const redirect = "http://192.168.1.22:5173/#/print"
	got := a.AuthorizeURL("STATE123", redirect)

	if !strings.HasPrefix(got, AlipayDefaultAuthorizeURL+"?") {
		t.Fatalf("应以【授权页】地址开头（不是网关），实际: %s", got)
	}

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("授权链接不是合法 URL: %v", err)
	}
	q := u.Query()

	if q.Get("app_id") != testAppID {
		t.Errorf("app_id 不对: %q", q.Get("app_id"))
	}
	// auth_base = 静默授权，用户看不到确认页。这是本方案的体验前提。
	if q.Get("scope") != "auth_base" {
		t.Errorf("scope 应为 auth_base（静默授权），实际 %q", q.Get("scope"))
	}
	if q.Get("state") != "STATE123" {
		t.Errorf("state 丢失: %q", q.Get("state"))
	}
	// redirect_uri 含 `#`：编码后还要能原样还原，否则参数归属会乱。
	if q.Get("redirect_uri") != redirect {
		t.Errorf("redirect_uri 未原样保留:\n 得到 %q\n 期望 %q", q.Get("redirect_uri"), redirect)
	}

	// 🚫 授权链接【不需要签名】。这条断言专门防后来者照抄 cashierURL 把一整套
	// 网关参数塞进来 —— 多出来的参数不会让授权失败，但会误导后来读代码的人。
	for _, k := range []string{"sign", "sign_type", "biz_content", "method", "timestamp", "charset", "version", "format"} {
		if q.Has(k) {
			t.Errorf("授权链接不该带参数 %q", k)
		}
	}

	// 官方建议：state 放在 redirect_uri 之前，免得被当成回调地址自身的参数。
	if strings.Index(got, "state=") > strings.Index(got, "redirect_uri=") {
		t.Error("state 应排在 redirect_uri 之前")
	}
}

func TestAuthorizeURLFor(t *testing.T) {
	cases := []struct {
		name    string
		gateway string
		want    string
	}{
		{"空网关走正式", "", AlipayDefaultAuthorizeURL},
		{"正式网关", AlipayDefaultGateway, AlipayDefaultAuthorizeURL},
		{"沙箱（dl 主机）", "https://openapi-sandbox.dl.alipaydev.com/gateway.do", AlipaySandboxAuthorizeURL},
		{"沙箱（老主机）", "https://openapi.alipaydev.com/gateway.do", AlipaySandboxAuthorizeURL},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := AuthorizeURLFor(c.gateway); got != c.want {
				t.Errorf("\n 得到 %s\n 期望 %s", got, c.want)
			}
		})
	}
}

func TestAlipayAuthorizeURLExplicitOverride(t *testing.T) {
	appPriv, alipayPriv := genKey(t), genKey(t)
	const custom = "https://auth.example.com/authorize"
	a, err := NewAlipay(AlipayConfig{
		AppID:        testAppID,
		PrivateKey:   privB64(t, appPriv, false),
		PublicKey:    pubB64(t, &alipayPriv.PublicKey),
		Gateway:      "https://openapi-sandbox.dl.alipaydev.com/gateway.do",
		AuthorizeURL: custom,
	})
	if err != nil {
		t.Fatalf("NewAlipay 失败: %v", err)
	}
	// 显式给了就以显式为准（哪怕网关看着像沙箱）。
	if got := a.AuthorizeURL("s", "r"); !strings.HasPrefix(got, custom+"?") {
		t.Errorf("显式 AuthorizeURL 应优先，实际: %s", got)
	}
}

// ─── 换取令牌 ────────────────────────────────────────────────────────

// oauthGateway 起一个只回授权令牌响应的假网关，并返回配套的 Alipay 实例。
// tamper 为真时故意破坏响应签名，用来证明验签真的在拦。
func oauthGateway(t *testing.T, node string, tamper bool) (*fakeGateway, *Alipay) {
	t.Helper()
	appPriv, alipayPriv := genKey(t), genKey(t)
	gw := &fakeGateway{
		appPub:     &appPriv.PublicKey,
		alipayPriv: alipayPriv,
		tamper:     tamper,
		respond: func(url.Values) (string, string, int) {
			return "alipay_system_oauth_token_response", node, http.StatusOK
		},
	}
	srv := httptest.NewServer(gw)
	t.Cleanup(srv.Close)
	// 渠道侧验签用的是【支付宝公钥】，所以传 alipayPriv 的公钥。
	return gw, newTestAlipay(t, appPriv, &alipayPriv.PublicKey, srv.URL)
}

func TestAlipayExchangeAuthCode(t *testing.T) {
	// ⚠️ 这个节点的形状必须与【官方文档的成功示例】一致：没有 code / msg / sub_code。
	// 早先这里写成了 `{"code":"10000",...}`（别的接口的形态），于是掩盖了一个真机
	// bug —— 线上真实的成功响应没有 code，而代码用 `code != "10000"` 判失败，
	// 结果每一次成功都被当成失败，错误信息还是空的。
	gw, a := oauthGateway(t,
		`{"open_id":"`+testOpenID+`","user_id":"2088102150477652",`+
			`"access_token":"tok-123","expires_in":"3600","refresh_token":"ref-456"}`, false)

	tok, err := a.ExchangeAuthCode(context.Background(), "AUTHCODE-1")
	if err != nil {
		t.Fatalf("换取令牌失败: %v", err)
	}
	if tok.OpenID != testOpenID {
		t.Errorf("open_id 不对: %q", tok.OpenID)
	}
	if tok.AccessToken != "tok-123" {
		t.Errorf("access_token 不对: %q", tok.AccessToken)
	}

	// 请求侧：method / grant_type / code 都要对 —— 传错任何一个都会被网关拒，
	// 而错误码（isv.grant-type-invalid）不会告诉你到底哪个字段错了。
	if got := gw.lastParams.Get("method"); got != "alipay.system.oauth.token" {
		t.Errorf("method 不对: %q", got)
	}

	// ⚠️ 这条是本文件最重要的一组断言：grant_type / code 必须作为【顶级表单参数】
	// 发送，不能塞进 biz_content。
	//
	// 这是真机踩出来的：按 call()（塞 biz_content）发，网关回
	// `40002 Invalid Arguments / isv.isv.grant-type-invalid`，而那条错误只字未提
	// "参数放错位置了"，看起来像 grant_type 的值写错了 —— 照那个方向查会一无所获。
	// 官方文档的调用示例（`-F 'grant_type=...' -F 'code=...'`）才是对的。
	if got := gw.lastParams.Get("grant_type"); got != "authorization_code" {
		t.Errorf("grant_type 必须作为【顶级】参数发送（这个接口不套 biz_content），实际 %q", got)
	}
	if got := gw.lastParams.Get("code"); got != "AUTHCODE-1" {
		t.Errorf("code 必须作为【顶级】参数发送，实际 %q", got)
	}
	if bc := gw.lastParams.Get("biz_content"); bc != "" {
		t.Errorf("这个接口不该发 biz_content，实际发了 %q", bc)
	}
}

// 响应被篡改时必须拦下。verifyResponse 对「没有 sign」也是直接失败 ——
// 真机联调若发现这个接口确实不签名，要拿出来单独讨论，🚫 不要放开这里。
func TestAlipayExchangeAuthCodeRejectsTampered(t *testing.T) {
	_, a := oauthGateway(t, `{"code":"10000","open_id":"`+testOpenID+`"}`, true)

	_, err := a.ExchangeAuthCode(context.Background(), "AUTHCODE-1")
	if err == nil {
		t.Fatal("响应签名被篡改时应当报错")
	}
	if !strings.Contains(err.Error(), "验签") {
		t.Errorf("错误信息应说明是验签失败，实际: %v", err)
	}
}

func TestAlipayExchangeAuthCodeErrors(t *testing.T) {
	cases := []struct {
		name string
		node string
		// 期望错误里出现的关键词 —— 必须是【指向根因】的话，不是"登录失败"。
		wantContains string
	}{
		{
			name:         "授权码失效",
			node:         `{"code":"40004","msg":"Business Failed","sub_code":"isv.code-invalid","sub_msg":"授权码错误或已过期"}`,
			wantContains: "重新点击支付宝登录",
		},
		{
			name:         "grant_type 传错",
			node:         `{"code":"40004","sub_code":"isv.grant-type-invalid","sub_msg":"grant_type 不合法"}`,
			wantContains: "grant_type",
		},
		{
			name:         "app_id 与授权应用不一致",
			node:         `{"code":"40004","sub_code":"isv.unmatched-app-id","sub_msg":"应用不匹配"}`,
			wantContains: "app_id",
		},
		{
			// 这条最常见也最难查：支付宝只回了 user_id。根因是应用没启用
			// 「openid 配置管理」，提示里必须直接说出来。
			name:         "只有 user_id 没有 open_id",
			node:         `{"code":"10000","msg":"Success","user_id":"2088102150477652"}`,
			wantContains: "openid 配置管理",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, a := oauthGateway(t, c.node, false)
			_, err := a.ExchangeAuthCode(context.Background(), "AUTHCODE-1")
			if err == nil {
				t.Fatal("应当报错")
			}
			if !strings.Contains(err.Error(), c.wantContains) {
				t.Errorf("错误信息应指向根因（含 %q），实际: %v", c.wantContains, err)
			}
		})
	}
}

// 网关级错误（不是业务错误）落在 error_response 节点上。要把它捞出来，
// 否则排查的人只会看到一句「响应里没有 alipay_system_oauth_token_response 节点」。
func TestAlipayExchangeAuthCodeGatewayErrorNode(t *testing.T) {
	appPriv, alipayPriv := genKey(t), genKey(t)
	gw := &fakeGateway{
		appPub:     &appPriv.PublicKey,
		alipayPriv: alipayPriv,
		respond: func(url.Values) (string, string, int) {
			return "error_response", `{"code":"40002","msg":"Invalid Arguments","sub_code":"isv.invalid-app-id"}`, http.StatusOK
		},
	}
	srv := httptest.NewServer(gw)
	defer srv.Close()
	a := newTestAlipay(t, appPriv, &alipayPriv.PublicKey, srv.URL)

	_, err := a.ExchangeAuthCode(context.Background(), "AUTHCODE-1")
	if err == nil {
		t.Fatal("应当报错")
	}
	if !strings.Contains(err.Error(), "isv.invalid-app-id") {
		t.Errorf("网关级错误要把原始内容带出来便于排障，实际: %v", err)
	}
}
