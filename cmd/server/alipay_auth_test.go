package main

// 支付宝授权登录的状态机测试。
//
// 要守的是这条链路：浏览器发起 → 支付宝里兑换 → 浏览器轮询领走会话。
// 它是"会话交回浏览器"这套设计的全部要害 —— 错了的表现是"登录了但浏览器还是
// 没登录"，而那种症状在真机上极难定位。
//
// 需要 PostgreSQL（状态表），未设 TEST_DATABASE_URL 时整体跳过。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cups-web/internal/payment"
)

const (
	uaBrowser = "Mozilla/5.0 (Linux; Android 14; 2211133C) AppleWebKit/537.36 Chrome/120 Mobile Safari/537.36"
	uaAlipay  = "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 AlipayClient/10.5.10"
)

// fakeAlipayOAuth 替掉真网关：只回一个固定的 open_id（或一个错误）。
type fakeAlipayOAuth struct {
	openID string
	err    error
	calls  int
}

func (f *fakeAlipayOAuth) AuthorizeURL(state, redirectURI string) string {
	return "https://example.invalid/authorize?state=" + state
}

func (f *fakeAlipayOAuth) ExchangeAuthCode(ctx context.Context, code string) (payment.OAuthToken, error) {
	f.calls++
	if f.err != nil {
		return payment.OAuthToken{}, f.err
	}
	return payment.OAuthToken{OpenID: f.openID}, nil
}

// withFakeAlipay 换上假客户端，返回还原函数。
func withFakeAlipay(t *testing.T, f *fakeAlipayOAuth) func() {
	t.Helper()
	prev := buildAlipayOAuthClient
	buildAlipayOAuthClient = func() (alipayOAuthClient, error) { return f, nil }
	return func() { buildAlipayOAuthClient = prev }
}

type startResp struct {
	AuthorizeURL string `json:"authorizeUrl"`
	State        string `json:"state"`
	FromBrowser  bool   `json:"fromBrowser"`
}

func doStart(t *testing.T, ua string) startResp {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/alipay/start", nil)
	req.Header.Set("User-Agent", ua)
	alipayAuthStartHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("start 应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	var out startResp
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析 start 响应失败: %v", err)
	}
	if out.State == "" || out.AuthorizeURL == "" {
		t.Fatalf("start 必须返回 state 与 authorizeUrl，实际 %+v", out)
	}
	return out
}

func doExchange(t *testing.T, code, state, ua string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]string{"code": code, "state": state})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/alipay/exchange", strings.NewReader(string(body)))
	req.Header.Set("User-Agent", ua)
	alipayAuthExchangeHandler(rec, req)
	return rec
}

// doPoll 返回 (status, userId)。拿到会话时 status 为空、userId 非 0。
func doPoll(t *testing.T, state string) (string, int64) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/alipay/poll?state="+state, nil)
	alipayAuthPollHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("poll 应 200（轮询不该用 4xx，会被全局兜底当成会话失效），实际 %d: %s",
			rec.Code, rec.Body.String())
	}
	var out struct {
		Status string `json:"status"`
		UserID int64  `json:"userId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析 poll 响应失败: %v", err)
	}
	return out.Status, out.UserID
}

// TestAlipayLoginStateMachine 是这套设计的主干。
func TestAlipayLoginStateMachine(t *testing.T) {
	restoreStore := setTestStoreForProviders(t)
	defer restoreStore()
	fake := &fakeAlipayOAuth{openID: "openid-flow-1"}
	defer withFakeAlipay(t, fake)()

	// ① 浏览器发起
	start := doStart(t, uaBrowser)
	if !start.FromBrowser {
		t.Error("普通浏览器发起时 fromBrowser 应为 true（意味着会有人在轮询）")
	}

	// ② 还没兑换 → pending
	if st, _ := doPoll(t, start.State); st != "pending" {
		t.Errorf("未兑换时应为 pending，实际 %q", st)
	}

	// ③ 支付宝 webview 里兑换
	rec := doExchange(t, "authcode-1", start.State, uaAlipay)
	if rec.Code != http.StatusOK {
		t.Fatalf("exchange 应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	var ex struct {
		OK                bool `json:"ok"`
		NeedBrowserReturn bool `json:"needBrowserReturn"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ex); err != nil {
		t.Fatalf("解析 exchange 响应失败: %v", err)
	}
	if !ex.OK {
		t.Fatal("exchange 应返回 ok")
	}
	// 这条决定了支付宝里那页要不要提示"请回到浏览器"。
	if !ex.NeedBrowserReturn {
		t.Error("浏览器发起的那次，needBrowserReturn 应为 true")
	}
	// ⚠️ 浏览器发起的那次【不能】给支付宝 webview 签会话。
	// 签了它就成了一个功能完整的入口（能点进个人中心、能上传），而它恰恰是最不该
	// 干活的环境；不签，它就物理上到不了任何受保护页面。
	if len(rec.Result().Cookies()) != 0 {
		t.Error("浏览器发起的那次不该给支付宝 webview 签发会话 cookie")
	}

	// ④ 浏览器轮询 → 领到会话
	st, userID := doPoll(t, start.State)
	if st != "" || userID == 0 {
		t.Fatalf("兑换后轮询应拿到会话（userId 非 0），实际 status=%q userId=%d", st, userID)
	}

	// ⑤ 再轮询 → 已被领走
	if st, _ := doPoll(t, start.State); st != "expired" {
		t.Errorf("同一 state 不该被领第二次，实际 %q", st)
	}

	// ⑥ 同一个 open_id 再登录一次 → 复用同一个账号（不重复建号）
	start2 := doStart(t, uaBrowser)
	if rec := doExchange(t, "authcode-2", start2.State, uaAlipay); rec.Code != http.StatusOK {
		t.Fatalf("第二次 exchange 应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	_, userID2 := doPoll(t, start2.State)
	if userID2 != userID {
		t.Errorf("同一个 open_id 应复用同一账号，实际 %d vs %d", userID2, userID)
	}
}

// 支付宝扫一扫直接进来的：没有轮询方，所以不做"请回浏览器"的提示。
func TestAlipayLoginFromAlipayClient(t *testing.T) {
	restoreStore := setTestStoreForProviders(t)
	defer restoreStore()
	fake := &fakeAlipayOAuth{openID: "openid-inapp"}
	defer withFakeAlipay(t, fake)()

	start := doStart(t, uaAlipay)
	if start.FromBrowser {
		t.Error("支付宝内发起时 fromBrowser 应为 false（没有轮询方）")
	}

	rec := doExchange(t, "authcode-3", start.State, uaAlipay)
	if rec.Code != http.StatusOK {
		t.Fatalf("exchange 应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	var ex struct {
		NeedBrowserReturn bool `json:"needBrowserReturn"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &ex)
	if ex.NeedBrowserReturn {
		t.Error("支付宝内发起的，不该提示用户回浏览器")
	}
	// 没有轮询方，就地继续 —— 必须把会话签给它，否则它没别处可去。
	if len(rec.Result().Cookies()) == 0 {
		t.Error("支付宝内发起的应当给调用方签发会话 cookie")
	}
}

// state 不认识时必须【在调支付宝之前】就拒绝 —— 否则伪造请求能放大成出站调用。
func TestAlipayExchangeRejectsUnknownState(t *testing.T) {
	restoreStore := setTestStoreForProviders(t)
	defer restoreStore()
	fake := &fakeAlipayOAuth{openID: "openid-should-not-be-used"}
	defer withFakeAlipay(t, fake)()

	rec := doExchange(t, "authcode-4", "not-a-real-state", uaAlipay)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("伪造的 state 应 400，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if fake.calls != 0 {
		t.Errorf("state 校验失败时不该去调支付宝，实际调了 %d 次", fake.calls)
	}
}
