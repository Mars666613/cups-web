package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// ─── 支付宝授权登录（OAuth）────────────────────────────────────────────
//
// 与付款是【两件独立的事】：这里只拿一个用户身份标识（open_id），不涉及任何
// 资金，也不碰 Create / Query / Refund 那条链路。
//
// 用到的两个东西：
//  1. 授权页 —— 拼一个 URL 让用户浏览器过去（不需要签名，见 AuthorizeURL）
//  2. alipay.system.oauth.token —— 用回跳带回来的 auth_code 换 open_id

// OAuthToken 是 alipay.system.oauth.token 的结果。
//
// 我们只持久化 OpenID。access_token / refresh_token 【刻意不存】——
// 登录只需要认一次人，之后靠自己的会话 cookie；存渠道令牌等于凭空多出一份
// 需要保护和过期的凭据，而我们根本用不到它。
type OAuthToken struct {
	OpenID       string
	UserID       string // 旧字段。官方建议用 OpenID 替代，保留是为了报错时能区分
	AccessToken  string
	RefreshToken string
}

// AuthorizeURL 拼用户授权链接。
//
// ⚠️ 这个链接【不需要签名】—— 与 cashierURL 形状相似但性质完全不同：
// 只有 app_id / scope / state / redirect_uri 四个参数，没有 method、没有
// biz_content、没有 charset / sign_type / timestamp / sign。
// 🚫 不要照抄 cashierURL 的签名逻辑走一遍，那只会往授权链接里塞一堆多余参数。
//
// 手工拼串而不是用 url.Values.Encode()：Encode 会按 key 的字母序排，把
// redirect_uri 排到 state 前面；而官方建议 state 放在 redirect_uri 【之前】，
// 免得 state 被当成回调地址自身的参数。没有签名，所以顺序可以随便定。
//
// redirectURI 里含 `#`（前端是 hash 路由），QueryEscape 会把它编成 %23 ——
// 这一步不能省，否则参数的归属会乱。
func (a *Alipay) AuthorizeURL(state, redirectURI string) string {
	return a.authURL +
		"?app_id=" + url.QueryEscape(a.appID) +
		"&scope=" + url.QueryEscape(alipayAuthScopeBase) +
		"&state=" + url.QueryEscape(state) +
		"&redirect_uri=" + url.QueryEscape(redirectURI)
}

// ExchangeAuthCode 用 auth_code 换 open_id。
//
// ⚠️ auth_code 是【一次有效】的：官方文档说未使用时有效期在 3 分钟到 24 小时
// 之间动态调整，换过一次就作废（无论成功与否）。所以调用方拿到回跳后要立刻换；
// 换失败应当引导用户重新授权，而不是重试同一个 code。
//
// ⚠️ 待真机确认：verifyResponse 对【响应里没有 sign】是刻意直接失败的。这个
// 接口的响应该不该带 sign，官方文档没有明说。真机联调第一步就是确认它 ——
// 若确实没有，再作为有据的例外单独讨论，🚫 不要为了"先跑通"在这里放开验签。
func (a *Alipay) ExchangeAuthCode(ctx context.Context, code string) (OAuthToken, error) {
	// 换 token 是纯服务端调用，没有异步通知要收。
	//
	// ⚠️ 必须用 callFlat：这个接口的业务参数在【顶级表单项】里，不套 biz_content
	// （官方示例就是 `-F 'grant_type=...' -F 'code=...'`）。用 call() 会得到
	// isv.isv.grant-type-invalid —— 那条错误不会提示"参数放错位置了"，实测卡过。
	body, err := a.callFlat(ctx, "alipay.system.oauth.token", url.Values{
		"grant_type": {"authorization_code"},
		"code":       {code},
	}, "")
	if err != nil {
		return OAuthToken{}, err
	}

	node, err := rawResponseNode(body, "alipay_system_oauth_token_response")
	if err != nil {
		// 网关级错误（验签失败、应用不存在之类）落在 error_response 节点上，
		// 不是业务节点。把它捞出来 —— 否则排查的人看到的是一句
		// "响应里没有 alipay_system_oauth_token_response 节点"，指不到根因。
		if errNode, e2 := rawResponseNode(body, "error_response"); e2 == nil {
			return OAuthToken{}, fmt.Errorf("支付宝网关返回错误: %s", truncate(string(errNode), 200))
		}
		return OAuthToken{}, err
	}
	if err := a.verifyResponse(body, node); err != nil {
		return OAuthToken{}, err
	}

	// 业务错误码在【业务节点内部】读（与 createQRCode 同一套做法，:209-222）。
	var resp struct {
		Code         string `json:"code"`
		Msg          string `json:"msg"`
		SubCode      string `json:"sub_code"`
		SubMsg       string `json:"sub_msg"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		OpenID       string `json:"open_id"`
		UserID       string `json:"user_id"`
	}
	if err := json.Unmarshal(node, &resp); err != nil {
		return OAuthToken{}, fmt.Errorf("解析授权响应失败: %w", err)
	}
	// ⚠️ 这个接口的【成功响应里没有 code 字段】—— 官方示例里
	// alipay_system_oauth_token_response 直接就是 open_id / access_token / ...，
	// 没有 code / msg / sub_code。
	//
	// 所以不能照抄别的接口那套 `if Code != "10000" 判失败`：那会把每一次成功
	// 都判成失败，而且拼出来的错误是空的（"支付宝授权失败: /"），完全指不到根因。
	// 实测踩过 —— 现象是"换了半天参数都失败"，其实早就成功了。
	//
	// 失败走的是网关层的 error_response（上面的 rawResponseNode 兜底已处理）。
	// 这里的判断只是防御：万一某个版本带了 code 且不是成功码，仍然按错误处理。
	if resp.Code != "" && resp.Code != "10000" {
		return OAuthToken{}, fmt.Errorf("%s", oauthErrorText(resp.SubCode, resp.Msg, resp.SubMsg))
	}
	if resp.OpenID == "" {
		// 只拿到 user_id 而没有 open_id，几乎总是「openid 配置管理」没启用。
		// 这条要单独报，否则现象是"登录成功但没身份"，非常难查。
		if resp.UserID != "" {
			return OAuthToken{}, fmt.Errorf("支付宝只返回了 user_id 而没有 open_id —— " +
				"请在开放平台「应用 - 开发配置 - openid 配置管理」中启用后重试")
		}
		return OAuthToken{}, fmt.Errorf("支付宝未返回 open_id")
	}

	return OAuthToken{
		OpenID:       resp.OpenID,
		UserID:       resp.UserID,
		AccessToken:  resp.AccessToken,
		RefreshToken: resp.RefreshToken,
	}, nil
}

// oauthErrorText 把支付宝的业务错误码翻成指向根因的话。
//
// 文档里这张表的作用就是让实现者分流：不同类型该给用户看的、该给管理员看的
// 完全不同。🚫 不要把它们都吞成一句"登录失败"——那是本仓库返工最多的一类问题。
func oauthErrorText(subCode, msg, subMsg string) string {
	switch subCode {
	case "isv.code-invalid":
		return "授权码已失效，请重新点击支付宝登录"
	case "isv.grant-type-invalid":
		return "获取授权令牌的请求参数有误（grant_type），请反馈给管理员"
	case "isv.unmatched-app-id":
		return "本次授权来自另一个支付宝应用（app_id 不匹配），请反馈给管理员"
	case "isv.refresh-token-invalid", "isv.refresh-token-time-out":
		return "授权已过期，请重新点击支付宝登录"
	}
	// 三个都空时不要吐一句"支付宝授权失败: /"——那是【什么都没说】，
	// 而排查的人会以为参数不对，往错的方向查（实测踩过）。
	if strings.TrimSpace(subCode+msg+subMsg) == "" {
		return "支付宝返回了无法识别的响应（业务节点里没有 code / sub_code），请查看服务端日志"
	}
	return fmt.Sprintf("支付宝授权失败: %s/%s %s", subCode, msg, subMsg)
}
