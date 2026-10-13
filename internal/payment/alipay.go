package payment

// 支付宝渠道。
//
// ── 收款方式，按设备分流 ──
//
// 这个程序主要给手机用（手机访问部署好的网址 → 传文件 → 打印），但电脑上
// 也得能用。两种设备拿什么付款不同，所以同一个渠道下备了两条路走：
//
//	手机 → alipay.trade.wap.pay（手机网站支付）
//	电脑 → alipay.trade.page.pay（电脑网站支付）
//
// 两者机制完全一样：给一个收银台地址，当前浏览器跳过去；手机上会自动唤起
// 支付宝 App，电脑上收银台页面显示二维码可以扫。付完都由【支付宝把浏览器
// 送回 return_url】—— 用户不必自己去任务切换器里找我们的页面。
//
// 另有一条 alipay.trade.precreate（当面付，二维码直接渲染在我们页面上、不跳转，
// 体验更连贯），但现在前端不会选它：当面付是【线下收银场景】的产品，网页应用
// 一般签不下来，官方给网页应用的就是上面两个。保留它是为了将来万一签到了，
// 前端改一行就能切回来（见 ChannelQRCode 的说明）。
//
// 几条路共用同一套 out_trade_no、查单、退款、关单 —— 区别只在「怎么把用户
// 送到收银台」，钱到账之后的事完全一样。
//
// ── 为什么 return_url 用内网地址也行 ──
//
// 回跳是【手机浏览器】去访问，支付宝的服务器不需要能连到它。所以
// http://192.168.1.22:1180/#/wallet 这种内网地址完全可用，不需要公网域名、
// 不需要备案。真正需要公网可达的只有 notify_url（异步通知），而那个本项目
// 根本不用 —— 到账一律靠 alipay.trade.query 主动查（见 Provider.Query）。
//
// ── 为什么不用官方 SDK ──
//
// 支付宝没有官方 Go SDK，社区实现（smartwalle/alipay 等）会把一堆本项目
// 用不到的能力一起拖进来。这里真正需要的只有三件：RSA2 签名、几个接口调用、
// 回调验签 —— 全部由标准库覆盖。本项目连 IPP 与 PDF 解析都是手写的，
// 为一层薄薄的 HTTP + 签名协议引一个外部依赖不划算。
//
// ⚠️ 配置里要的是【支付宝公钥】，不是应用公钥。两个都是从开放平台复制的
// 一行 base64，传错的话所有验签都失败，而报错只有「验签失败」四个字。

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// AlipayDefaultGateway 是正式环境网关。沙箱是
	// https://openapi-sandbox.dl.alipaydev.com/gateway.do
	AlipayDefaultGateway = "https://openapi.alipay.com/gateway.do"

	// 用户授权（授权登录）的授权页主机【和网关不是同一台】，所以不能从 Gateway
	// 机械替换出来 —— 必须单独给一个默认值：
	//   正式  openapi.alipay.com        / openauth.alipay.com
	//   沙箱  openapi-sandbox.dl...     / openauth.alipaydev.com
	// 配错的代价是「用户浏览器里出现一个支付宝错误页，而我们这边毫无信号」，
	// 与 cashierURL 那类问题同源，所以这里要显式、可覆盖。
	AlipayDefaultAuthorizeURL = "https://openauth.alipay.com/oauth2/publicAppAuthorize.htm"
	AlipaySandboxAuthorizeURL = "https://openauth.alipaydev.com/oauth2/publicAppAuthorize.htm"

	// alipayAuthScopeBase 是【静默授权】：用户看不到确认页，登录成功后直接跳回
	// 回调地址。代价是拿不到昵称/头像（那要用 auth_user 再调 alipay.user.info.share）。
	// 打印场景只需要一个稳定身份标识，所以用 auth_base 换最好的体验。
	alipayAuthScopeBase = "auth_base"

	alipaySignType = "RSA2" // RSA2 = SHA256withRSA，支付宝只推荐这个

	// 两个「页面跳转类」产品的固定 product_code，传错会被网关直接拒掉。
	//   alipay.trade.wap.pay  → QUICK_WAP_WAY        手机网站支付
	//   alipay.trade.page.pay → FAST_INSTANT_TRADE_PAY 电脑网站支付
	alipayWapProductCode  = "QUICK_WAP_WAY"
	alipayPageProductCode = "FAST_INSTANT_TRADE_PAY"

	// alipayResponseLimit 限制网关响应体大小。响应很小（几 KB），
	// 设个上限免得对端异常时把内存吃满。
	alipayResponseLimit = 1 << 20

	alipayHTTPTimeout = 15 * time.Second

	// 当面付二维码的最短/最长有效期，渠道侧的硬约束。
	alipayMinTimeout = 1 * time.Minute
	alipayMaxTimeout = 24 * time.Hour
)

// AlipayConfig 是接入一个支付宝应用所需的全部信息。
type AlipayConfig struct {
	AppID      string
	PrivateKey string // 应用私钥（PKCS8/PKCS1、PEM 或裸 base64 均可）
	PublicKey  string // 支付宝公钥（⚠️ 不是应用公钥）
	Gateway    string // 留空用正式网关
	// AuthorizeURL 是授权页地址（OAuth 登录用）。留空按 Gateway 是否沙箱推断，
	// 推断规则见 NewAlipay。只有非常规部署才需要显式指定。
	AuthorizeURL string
	HTTPClient   *http.Client
}

// Alipay 实现 Provider。
type Alipay struct {
	appID   string
	gateway string
	authURL string
	priv    *rsa.PrivateKey
	pub     *rsa.PublicKey
	http    *http.Client
}

// NewAlipay 解析密钥并构造渠道。
//
// 密钥在这里【一次性】解析好，而不是每次请求现解析：解析失败必须在
// 配置阶段就暴露出来（服务启动/首次使用时立刻报错），而不是等用户
// 点了支付才炸。同时省掉每次请求的重复 DER 解析。
func NewAlipay(cfg AlipayConfig) (*Alipay, error) {
	if strings.TrimSpace(cfg.AppID) == "" {
		return nil, fmt.Errorf("%w: 缺少支付宝 AppID", ErrUnavailable)
	}
	priv, err := parseAlipayPrivateKey(cfg.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	pub, err := parseAlipayPublicKey(cfg.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	gateway := strings.TrimSpace(cfg.Gateway)
	if gateway == "" {
		gateway = AlipayDefaultGateway
	}
	authURL := strings.TrimSpace(cfg.AuthorizeURL)
	if authURL == "" {
		authURL = AuthorizeURLFor(gateway)
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: alipayHTTPTimeout}
	}
	return &Alipay{
		appID:   strings.TrimSpace(cfg.AppID),
		gateway: gateway,
		authURL: authURL,
		priv:    priv,
		pub:     pub,
		http:    client,
	}, nil
}

// AuthorizeURLFor 由网关地址推断对应的授权页地址。
//
// 授权页主机与网关不同，但属于【同一套环境】：
//
//	正式  openapi.alipay.com      → openauth.alipay.com
//	沙箱  openapi-sandbox.dl...   → openauth.alipaydev.com
//
// 这条规则只有一份 —— NewAlipay 与启动日志（alipayConfigSummary）都调它，
// 免得日志里报出来的和实际用的不是同一个。
//
// ⚠️ 这是"认字符串"的判断：沙箱的两个已知网关主机是 openapi.alipaydev.com 与
// openapi-sandbox.dl.alipaydev.com，共同特征是 sandbox / alipaydev。支付宝哪天
// 改了沙箱域名这里就会失准，所以 AlipayConfig.AuthorizeURL 留了显式覆盖的口子。
func AuthorizeURLFor(gateway string) string {
	g := strings.ToLower(gateway)
	if strings.Contains(g, "sandbox") || strings.Contains(g, "alipaydev") {
		return AlipaySandboxAuthorizeURL
	}
	return AlipayDefaultAuthorizeURL
}

func (a *Alipay) Name() string { return "alipay" }

// Create 发起一笔支付，按 req.Channel 决定走哪种收款方式。
func (a *Alipay) Create(ctx context.Context, req Request) (Credential, error) {
	if req.AmountCents <= 0 {
		return Credential{}, errors.New("支付金额必须大于 0")
	}

	biz := map[string]string{
		"out_trade_no": req.OutTradeNo,
		"total_amount": centsToYuan(req.AmountCents),
		"subject":      alipaySubject(req.Subject),
	}
	// 有效期与订单 TTL 对齐：用户能在支付页停留多久，这笔交易就活多久。
	// 不传的话渠道用默认值（可能远长于我们的订单有效期），
	// 会出现「码还能扫 / 收银台还能付，但订单已经过期回收了」的错位。
	if !req.ExpiresAt.IsZero() {
		biz["timeout_express"] = alipayTimeoutExpress(time.Until(req.ExpiresAt))
	}

	switch req.Channel {
	case ChannelWap:
		return a.createWap(biz, req)
	case ChannelPage:
		return a.createPage(biz, req)
	default:
		return a.createQRCode(ctx, biz, req)
	}
}

// createWap 走手机网站支付：给一个收银台地址，当前浏览器跳过去。
//
// ⚠️ 这一步【不会真的联系支付宝】—— 见 wapPayURL 的说明。所以它几乎不会失败，
// 失败基本只可能是「上层没给 return_url」这种编程错误，为此宁可显式报错，
// 也不要发一个用户付完回不来的链接出去。
func (a *Alipay) createWap(biz map[string]string, req Request) (Credential, error) {
	if req.ReturnURL == "" {
		// 没有回跳地址的话，用户付完会停在支付宝自己的结果页上，还得自己
		// 找回来 —— 那正是手机网站支付要解决的问题本身。
		// 上层（createPayment）本该在拿不到地址时降级成二维码，走到这里说明漏了。
		return Credential{}, fmt.Errorf("%w: 手机网站支付必须提供 return_url", ErrUnavailable)
	}

	// product_code 是手机网站支付的固定值，传错会被网关拒。
	biz["product_code"] = alipayWapProductCode
	// quit_url：用户在收银台点「返回/放弃」时落到哪。
	// 与 return_url 同址 —— 付完回来、放弃也回来，两种情况都该回到我们这儿。
	biz["quit_url"] = req.ReturnURL

	payURL, err := a.cashierURL("alipay.trade.wap.pay", biz, req)
	if err != nil {
		return Credential{}, err
	}
	return Credential{
		OutTradeNo: req.OutTradeNo,
		// QRCodeText 刻意留空：这条路上没有码可扫，前端据此知道
		// 「该跳转，不是该显示二维码」。
		PayURL:    payURL,
		ExpiresAt: req.ExpiresAt,
		Raw:       payURL,
	}, nil
}

// createQRCode 走当面付：拿回一串 qr_code，由用户用另一台设备扫。
func (a *Alipay) createQRCode(ctx context.Context, biz map[string]string, req Request) (Credential, error) {
	body, err := a.call(ctx, "alipay.trade.precreate", biz, req.NotifyURL)
	if err != nil {
		return Credential{}, err
	}

	node, err := rawResponseNode(body, "alipay_trade_precreate_response")
	if err != nil {
		return Credential{}, err
	}
	if err := a.verifyResponse(body, node); err != nil {
		return Credential{}, err
	}

	var resp struct {
		Code    string `json:"code"`
		Msg     string `json:"msg"`
		SubCode string `json:"sub_code"`
		SubMsg  string `json:"sub_msg"`
		QRCode  string `json:"qr_code"`
	}
	if err := json.Unmarshal(node, &resp); err != nil {
		return Credential{}, fmt.Errorf("解析下单响应失败: %w", err)
	}
	if resp.Code != "10000" {
		return Credential{}, fmt.Errorf("支付宝下单失败: %s/%s %s %s",
			resp.Code, resp.SubCode, resp.Msg, resp.SubMsg)
	}
	if resp.QRCode == "" {
		return Credential{}, errors.New("支付宝未返回 qr_code")
	}

	// ⚠️ qr_code 与 pay_url 是【同一个值】，两个字段都填是刻意的。
	//
	// 当面付返回的 qr_code 本身就是一个 URL（https://qr.alipay.com/bax...）：
	//   - 电脑端把它渲染成二维码，用手机扫
	//   - 手机端拿它当兜底：系统认这个链接，会弹「用哪个应用打开」唤起支付宝
	//
	// 手机端现在优先走手机网站支付（见 createWap），只有拿不到 return_url
	// 时才落到这里。所以这条兜底路径要留着 —— 它不理想（多一次网页跳转），
	// 但比「什么都付不了」强。
	return Credential{
		OutTradeNo: req.OutTradeNo,
		QRCodeText: resp.QRCode,
		PayURL:     resp.QRCode,
		ExpiresAt:  req.ExpiresAt,
		Raw:        string(node),
	}, nil
}

// cashierURL 组装「页面跳转类」接口的收银台地址。
//
// 手机网站支付（wap.pay）与电脑网站支付（page.pay）机制完全一样 ——
// 商户把签名好的参数交给浏览器，由浏览器跳过去，支付宝渲染收银台；付完再由
// 支付宝把浏览器送回 return_url。区别只有 method 名与 product_code，
// 所以共用这一个拼装函数。
//
// ⚠️ 与文件里其它调用【根本不同】：这里不发任何 HTTP 请求。
// 官方 SDK 的 pageExecute 也是这么做的（在本地拼一个自动提交的 form）。
//
// 代价必须说清楚：**这一步不会报错**。签名写错、应用没签这个产品、app_id
// 不对 —— 全都不会在这里暴露，它们只会在用户浏览器里变成一个支付宝错误页，
// 而我们这边一切正常。所以联调必须真机完整走一遍，不能看到接口返回 200
// 就以为通了。
func (a *Alipay) cashierURL(method string, biz map[string]string, req Request) (string, error) {
	bizJSON, err := json.Marshal(biz)
	if err != nil {
		return "", fmt.Errorf("序列化业务参数失败: %w", err)
	}

	params := url.Values{}
	params.Set("app_id", a.appID)
	params.Set("method", method)
	params.Set("format", "JSON")
	params.Set("charset", "utf-8")
	params.Set("sign_type", alipaySignType)
	params.Set("timestamp", alipayTimestamp())
	params.Set("version", "1.0")
	params.Set("biz_content", string(bizJSON))
	params.Set("return_url", req.ReturnURL)
	if req.NotifyURL != "" {
		params.Set("notify_url", req.NotifyURL)
	}

	// 与 call 同一条规矩：请求签名要【包含】sign_type。
	sign, err := alipaySign(alipaySignContent(params, true), a.priv)
	if err != nil {
		return "", err
	}
	params.Set("sign", sign)

	return a.gateway + "?" + params.Encode(), nil
}

// createPage 走电脑网站支付（page.pay）：电脑端的正路。
//
// 用户跳到支付宝的收银台页面，那里会显示二维码（也支持在电脑上登录支付），
// 用手机扫完由支付宝把浏览器送回 return_url。
//
// 早先电脑端用的是当面付的 alipay.trade.precreate（二维码直接渲染在我们页面上，
// 体验更连贯），但当面付是【线下收银场景】的产品，网页应用通常签不下来 ——
// 官方为网页应用提供的就是电脑网站支付。见 ChannelPage 的说明。
func (a *Alipay) createPage(biz map[string]string, req Request) (Credential, error) {
	if req.ReturnURL == "" {
		// 与手机端同理：没有回跳地址，用户付完会停在支付宝自己的结果页上，
		// 还得自己找回我们的页面。宁可显式报错，让上层降级成别的方式。
		return Credential{}, fmt.Errorf("%w: 电脑网站支付必须提供 return_url", ErrUnavailable)
	}
	biz["product_code"] = alipayPageProductCode

	payURL, err := a.cashierURL("alipay.trade.page.pay", biz, req)
	if err != nil {
		return Credential{}, err
	}
	return Credential{
		OutTradeNo: req.OutTradeNo,
		// QRCodeText 空 = 前端据此知道「该跳转，不是该显示二维码」。
		PayURL:    payURL,
		ExpiresAt: req.ExpiresAt,
		Raw:       payURL,
	}, nil
}

// Query 调 alipay.trade.query 主动查单。
//
// ⚠️ 查无此单【不是】错误：用户在二维码过期后才点「我已支付」就会这样。
// 调用方只需要「有没有到账」这个结论，所以这类情况返回 Paid=false 而不是 error ——
// 回 error 的话前端会显示「查询失败」，而真相是「你还没付」。
func (a *Alipay) Query(ctx context.Context, outTradeNo string) (Result, error) {
	body, err := a.call(ctx, "alipay.trade.query", map[string]string{"out_trade_no": outTradeNo}, "")
	if err != nil {
		return Result{}, err
	}

	node, err := rawResponseNode(body, "alipay_trade_query_response")
	if err != nil {
		return Result{}, err
	}
	if err := a.verifyResponse(body, node); err != nil {
		return Result{}, err
	}

	var resp struct {
		Code        string `json:"code"`
		Msg         string `json:"msg"`
		SubCode     string `json:"sub_code"`
		SubMsg      string `json:"sub_msg"`
		TradeNo     string `json:"trade_no"`
		OutTradeNo  string `json:"out_trade_no"`
		TradeStatus string `json:"trade_status"`
		TotalAmount string `json:"total_amount"`
	}
	if err := json.Unmarshal(node, &resp); err != nil {
		return Result{}, fmt.Errorf("解析查单响应失败: %w", err)
	}
	if resp.Code != "10000" {
		// ACQ.TRADE_NOT_EXIST 是最常见的一种：用户还没扫。
		return Result{OutTradeNo: outTradeNo, Paid: false, Raw: string(node)}, nil
	}

	paid := alipayTradePaid(resp.TradeStatus)
	cents, err := yuanToCents(resp.TotalAmount)
	if err != nil {
		return Result{}, fmt.Errorf("支付宝返回的金额无法解析: %w", err)
	}
	tradeNo := resp.OutTradeNo
	if tradeNo == "" {
		tradeNo = outTradeNo
	}
	return Result{
		OutTradeNo:    tradeNo,
		TransactionID: resp.TradeNo,
		AmountCents:   cents,
		Paid:          paid,
		Raw:           string(node),
	}, nil
}

// VerifyCallback 验签并解析异步通知。
//
// 支付宝用 application/x-www-form-urlencoded 投递，所以从已经读好的 body
// 重新 ParseQuery，而不是再读一遍 r.Body（body 已被 handler 限长读过一次）。
func (a *Alipay) VerifyCallback(r *http.Request, body []byte) (Result, error) {
	if r.Method != http.MethodPost {
		return Result{}, fmt.Errorf("%w: 回调必须是 POST", ErrBadSignature)
	}
	params, err := url.ParseQuery(string(body))
	if err != nil {
		return Result{}, fmt.Errorf("%w: 回调不是合法 form 编码", ErrBadSignature)
	}

	sign := params.Get("sign")
	if sign == "" {
		return Result{}, fmt.Errorf("%w: 回调没有 sign", ErrBadSignature)
	}
	// 通知验签要排除 sign_type —— 与请求签名相反，别改错。
	if err := alipayVerify(alipaySignContent(params, false), sign, a.pub); err != nil {
		return Result{}, err
	}

	// 验签只证明「这条通知出自持有该私钥的支付宝应用」，不证明是我们这个应用。
	// 同一个开放平台账号下的其它应用签发的通知同样能通过验签，所以必须再核对
	// app_id 是不是自己。这一步花两行，省掉的是「给别的应用的单子发货」。
	if got := params.Get("app_id"); got != a.appID {
		return Result{}, fmt.Errorf("%w: 回调 app_id 不匹配（收到 %q，期望 %q）",
			ErrBadSignature, got, a.appID)
	}

	cents, err := yuanToCents(params.Get("total_amount"))
	if err != nil {
		return Result{}, fmt.Errorf("回调金额无法解析: %w", err)
	}

	// ⚠️ Paid 可能为 false —— 支付宝在交易关闭（TRADE_CLOSED）时也会投递通知。
	// 调用方【必须】先看 Paid 再决定要不要结算（见 paymentCallbackHandler）。
	// 这里如实反映渠道状态，不替调用方做决定。
	return Result{
		OutTradeNo:    params.Get("out_trade_no"),
		TransactionID: params.Get("trade_no"),
		AmountCents:   cents,
		Paid:          alipayTradePaid(params.Get("trade_status")),
		Raw:           string(body),
	}, nil
}

// Refund 调 alipay.trade.refund 把已支付的交易原路退回。
//
// 支付宝的退款是【同步受理、异步到账】：返回 code=10000 只代表受理成功，
// 钱到用户账上可能还要几秒。这不需要我们轮询 —— 受理成功就一定会退，
// 失败会走退款失败通知（本项目内网部署收不到，靠渠道侧的退款查询兜底）。
func (a *Alipay) Refund(ctx context.Context, req RefundRequest) (RefundResult, error) {
	if req.AmountCents <= 0 {
		return RefundResult{}, errors.New("退款金额必须大于 0")
	}
	if req.RefundNo == "" {
		// 没有退款单号就没有幂等性可言，宁可直接拒绝。
		return RefundResult{}, errors.New("退款单号不能为空")
	}

	biz := map[string]string{
		"out_trade_no":   req.OutTradeNo,
		"refund_amount":  centsToYuan(req.AmountCents),
		"out_request_no": req.RefundNo,
	}
	if req.Reason != "" {
		biz["refund_reason"] = req.Reason
	}

	body, err := a.call(ctx, "alipay.trade.refund", biz, "")
	if err != nil {
		return RefundResult{}, err
	}
	node, err := rawResponseNode(body, "alipay_trade_refund_response")
	if err != nil {
		return RefundResult{}, err
	}
	if err := a.verifyResponse(body, node); err != nil {
		return RefundResult{}, err
	}

	var resp struct {
		Code       string `json:"code"`
		Msg        string `json:"msg"`
		SubCode    string `json:"sub_code"`
		SubMsg     string `json:"sub_msg"`
		TradeNo    string `json:"trade_no"`
		OutTradeNo string `json:"out_trade_no"`
		RefundFee  string `json:"refund_fee"`
		RefundID   string `json:"refund_settlement_id"`
	}
	if err := json.Unmarshal(node, &resp); err != nil {
		return RefundResult{}, fmt.Errorf("解析退款响应失败: %w", err)
	}
	if resp.Code != "10000" {
		return RefundResult{}, fmt.Errorf("支付宝退款失败: %s/%s %s %s",
			resp.Code, resp.SubCode, resp.Msg, resp.SubMsg)
	}

	// 用渠道回的 refund_fee 而不是我们请求的金额：万一渠道按比例退或
	// 有手续费，以它为准才不会让账目和实际差一分钱。
	actual := req.AmountCents
	if resp.RefundFee != "" {
		if v, cerr := yuanToCents(resp.RefundFee); cerr == nil {
			actual = v
		}
	}

	outTradeNo := resp.OutTradeNo
	if outTradeNo == "" {
		outTradeNo = req.OutTradeNo
	}
	return RefundResult{
		OutTradeNo:      outTradeNo,
		RefundNo:        req.RefundNo,
		AmountCents:     actual,
		ChannelRefundID: resp.RefundID,
		Raw:             string(node),
	}, nil
}

// Close 调 alipay.trade.close 关闭未支付的交易。
//
// 「交易不存在」与「已经是终态」都按成功处理 —— 调用方的意图是
// 「确保这笔单付不了款」，那两种情况下它本来就付不了。
// 只有真的关不掉（网络故障、签名错误）才返回 error。
func (a *Alipay) Close(ctx context.Context, outTradeNo string) error {
	body, err := a.call(ctx, "alipay.trade.close", map[string]string{"out_trade_no": outTradeNo}, "")
	if err != nil {
		return err
	}
	node, err := rawResponseNode(body, "alipay_trade_close_response")
	if err != nil {
		return err
	}
	if err := a.verifyResponse(body, node); err != nil {
		return err
	}

	var resp struct {
		Code    string `json:"code"`
		Msg     string `json:"msg"`
		SubCode string `json:"sub_code"`
		SubMsg  string `json:"sub_msg"`
	}
	if err := json.Unmarshal(node, &resp); err != nil {
		return fmt.Errorf("解析关单响应失败: %w", err)
	}
	if resp.Code == "10000" {
		return nil
	}
	// 交易不存在（用户压根没扫过）或已关闭 —— 「本来就没法付款」，视为成功。
	switch resp.SubCode {
	case "ACQ.TRADE_NOT_EXIST", "ACQ.TRADE_HAS_CLOSE", "ACQ.TRADE_STATUS_ERROR":
		return nil
	}
	return fmt.Errorf("支付宝关单失败: %s/%s %s %s", resp.Code, resp.SubCode, resp.Msg, resp.SubMsg)
}

// CallbackAck 支付宝要求回 "success" 纯文本，否则会持续重试投递
// （25 小时内最多 8 次）。
func (a *Alipay) CallbackAck() (int, []byte) {
	return http.StatusOK, []byte("success")
}

// ─── 内部 ────────────────────────────────────────────────────────────

// call 组装公共参数、签名并 POST 到网关，返回原始响应体。
//
// 业务参数走 biz_content（绝大多数接口的形态）。少数老式接口不吃这一套，
// 见 callFlat。
func (a *Alipay) call(ctx context.Context, method string, biz interface{}, notifyURL string) ([]byte, error) {
	bizJSON, err := json.Marshal(biz)
	if err != nil {
		return nil, fmt.Errorf("序列化业务参数失败: %w", err)
	}
	return a.postGateway(ctx, method, url.Values{"biz_content": {string(bizJSON)}}, notifyURL)
}

// callFlat 与 call 的唯一区别：业务参数【直接作为顶级表单项】发出去，不套 biz_content。
//
// ⚠️ alipay.system.oauth.token（授权登录换令牌）就是这种老式形态 —— 官方文档给的
// 调用示例里，grant_type / code 是 `-F` 表单项，压根没有 biz_content：
//
//	curl '.../gateway.do?method=alipay.system.oauth.token&...' \
//	  -F 'grant_type=authorization_code' -F 'code=xxx'
//
// 按 call() 发（塞进 biz_content）网关在顶级找不到 grant_type，回
// `isv.isv.grant-type-invalid`（40002 Invalid Arguments）—— 这条错误信息只说
// "grant_type 参数不正确"，完全不会提示"参数位置放错了"，实测卡过。
// 🚫 别改回 call()。
func (a *Alipay) callFlat(ctx context.Context, method string, biz url.Values, notifyURL string) ([]byte, error) {
	return a.postGateway(ctx, method, biz, notifyURL)
}

// postGateway 是所有网关调用的公共骨架：公共参数 + 额外参数 → 签名 → POST。
func (a *Alipay) postGateway(ctx context.Context, method string, extra url.Values, notifyURL string) ([]byte, error) {
	params := url.Values{}
	params.Set("app_id", a.appID)
	params.Set("method", method)
	params.Set("format", "JSON")
	params.Set("charset", "utf-8")
	params.Set("sign_type", alipaySignType)
	params.Set("timestamp", alipayTimestamp())
	params.Set("version", "1.0")
	for k, vs := range extra {
		for _, v := range vs {
			params.Add(k, v)
		}
	}
	if notifyURL != "" {
		params.Set("notify_url", notifyURL)
	}

	// 请求签名要【包含】sign_type —— 网关就是这么验的。
	// 这一段实测踩过：少了它网关回 isv.invalid-signature。
	sign, err := alipaySign(alipaySignContent(params, true), a.priv)
	if err != nil {
		return nil, err
	}
	params.Set("sign", sign)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.gateway,
		strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=utf-8")

	resp, err := a.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求支付宝网关失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, alipayResponseLimit))
	if err != nil {
		return nil, fmt.Errorf("读取支付宝响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("支付宝网关返回 HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return body, nil
}

// verifyResponse 用支付宝公钥校验响应签名。
//
// 响应没有 sign 时【直接失败】，不做任何「沙箱偶尔不签」之类的放行 ——
// 放行一次就等于给伪造响应开了永久的口子。
func (a *Alipay) verifyResponse(rawBody, node []byte) error {
	var envelope struct {
		Sign string `json:"sign"`
	}
	if err := json.Unmarshal(rawBody, &envelope); err != nil {
		return fmt.Errorf("响应不是合法 JSON: %w", err)
	}
	if envelope.Sign == "" {
		return fmt.Errorf("%w: 响应里没有 sign", ErrBadSignature)
	}
	if err := alipayVerify(string(node), envelope.Sign, a.pub); err != nil {
		return fmt.Errorf("响应验签失败: %w", err)
	}
	return nil
}

func alipayTradePaid(status string) bool {
	return status == "TRADE_SUCCESS" || status == "TRADE_FINISHED"
}

// alipaySubject 裁剪支付标题。支付宝要求 subject 不超过 256 字符，
// 且不能为空 —— 空的话整笔下单会被拒。
func alipaySubject(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "打印费用"
	}
	r := []rune(s)
	if len(r) > 100 {
		return string(r[:100])
	}
	return s
}

// alipayTimeoutExpress 把时长转成支付宝的 timeout_express 格式（如 "15m"）。
// 渠道对上下限有硬约束，越界会直接拒绝下单，所以在这里夹紧。
func alipayTimeoutExpress(d time.Duration) string {
	if d < alipayMinTimeout {
		d = alipayMinTimeout
	}
	if d > alipayMaxTimeout {
		d = alipayMaxTimeout
	}
	// 向上取整到分钟：宁可让二维码多活几十秒，也不要早于订单 TTL 失效。
	minutes := int64((d + time.Minute - 1) / time.Minute)
	return fmt.Sprintf("%dm", minutes)
}

// alipayTimestamp 返回支付宝要求的时间戳（东八区、秒级）。
//
// ⚠️ 容器里默认是 UTC。用本地时间拼时间戳会让请求差 8 小时，支付宝侧
// 可能判为过期 —— 这个错在开发机上永远复现不了（开发机多半就在东八区），
// 只会在容器里炸。所以显式指定时区，不依赖 TZ 环境变量。
func alipayTimestamp() string {
	loc := alipayLocation()
	return time.Now().In(loc).Format("2006-01-02 15:04:05")
}

func alipayLocation() *time.Location {
	if loc, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		return loc
	}
	// 镜像里 tzdata 缺失时的兜底：固定 +8 偏移，语义与 CST 完全一致。
	return time.FixedZone("CST", 8*3600)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
