package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"cups-web/internal/payment"
	"cups-web/internal/store"
)

// paymentMockEnabled 由环境变量开启，【默认关闭】——
// 生产环境不会因为漏配而意外启用一个「不验签就能伪造到账」的渠道。
func paymentMockEnabled() bool {
	v := strings.TrimSpace(os.Getenv("PAYMENT_MOCK"))
	return v == "1" || strings.EqualFold(v, "true")
}

// alipayConfigSummary 用一句话说明支付宝当前接的是哪个环境，启动时打一次。
//
// ⚠️ 存在的理由是一次真实的排查：生产密钥明明已经写好，请求却打到了沙箱
// 网关 —— 因为支付宝配置可能来自环境变量、.env、docker-compose、IDE 调试
// 配置中的任意一个，而【界面上一切正常】，只有真正付款时才失败。当时是靠
// 起个实例、发一笔充值、把返回的收银台 URL 拆开看网关才确认的，绕了一大圈。
//
// 把结论打在启动日志里，这类错配一眼可见。
//
// ⚠️ 只打 app_id 与网关，**绝不打密钥**：app_id 本来就会出现在收银台 URL
// 里，不是秘密；密钥一旦进了日志就会跟着日志一起被备份、被贴出去。
func alipayConfigSummary() string {
	appID := strings.TrimSpace(os.Getenv("ALIPAY_APP_ID"))
	if appID == "" || os.Getenv("ALIPAY_PRIVATE_KEY") == "" || os.Getenv("ALIPAY_PUBLIC_KEY") == "" {
		return "未配置（支付方式里不会出现支付宝）"
	}
	gateway := strings.TrimSpace(os.Getenv("ALIPAY_GATEWAY"))
	env := "生产"
	switch {
	case gateway == "":
		gateway = payment.AlipayDefaultGateway
	case strings.Contains(gateway, "sandbox") || strings.Contains(gateway, "alipaydev"):
		env = "沙箱"
	default:
		env = "自定义网关"
	}
	// 授权页地址也打出来：它与网关不是同一台主机，「以为在生产、实际打沙箱」
	// 这类错配光看网关未必发现得了，而排查时最想确认的就是它。
	// 规则与 NewAlipay 共用同一个函数，不会各说各话。
	authorize := strings.TrimSpace(os.Getenv("ALIPAY_AUTH_URL"))
	if authorize == "" {
		authorize = payment.AuthorizeURLFor(gateway)
	}
	return fmt.Sprintf("%s  app_id=%s  gateway=%s  authorize=%s", env, appID, gateway, authorize)
}

// buildProvider 按名字取渠道实例。
//
// 未启用/未配置一律返回 payment.ErrUnavailable，调用方据此把该渠道
// 从支付方式列表里剔除 —— 前端因此不会展示一个点了必然失败的选项。
func buildProvider(name string) (payment.Provider, error) {
	switch name {
	case store.PaymentProviderMock:
		if !paymentMockEnabled() {
			return nil, payment.ErrUnavailable
		}
		return &payment.Mock{}, nil

	case store.PaymentProviderAlipay:
		return buildAlipay()

	case store.PaymentProviderWechat:
		// 微信支付【尚未实现】。这里只留问题清单，不写答案 ——
		// 产品形态（Native 扫码 / H5 支付 / JSAPI）还没定，定了才谈得上协议。
		//
		// 接入前要回答的（按顺序，前一个不定就没有后一个）：
		//   1. 用哪种收款产品？它决定的不是「调哪个接口」，而是整套交互：
		//      Native 是页面内渲染二维码、用户扫码；H5 是跳转唤起；JSAPI 要公众号。
		//      ⚠️ 已知资质门槛：开通 Native / H5 都需要「已认证服务号或公众号
		//      + 已 ICP 备案域名」，且 H5 还要求 H5 域名已备案并会校验来源 ——
		//      纯内网 IP 部署下 H5 基本不可行。这是【开通审核】层面的约束，
		//      不因为我们不用回调（结算走主动查单）而消失。详见 docs/wechat-pay.md。
		//   2. Provider 的七个方法里，哪几个对微信真的成立？特别是有没有必要
		//      实现 VerifyCallback —— 本项目内网部署、结算一律靠主动查单，
		//      支付宝那条路压根不发 notify_url。
		//   3. payment.Request.Channel 那三个常量（qrcode/wap/page）是【支付宝
		//      产品形态的词汇表】，微信的收款方式与它不是一一对应。这是接口
		//      适配问题，见 internal/payment/provider.go 里 Channel 的说明。
		//
		// ⚠️ 本分支返回 ErrUnavailable 是【有意的，不要顺手改掉】：
		//   · 它让 writePaymentStartError 把这种情况映射成 400「该支付方式当前
		//     不可用」，而不是落到 default 分支的 502 + 内部错误原文；
		//   · 它让查单 handler 回 400、回调 handler 回 404「unknown provider」；
		//   · 它让 closePreviousBatchPayment 静默跳过，而不是每次刷新都报一条
		//     无意义的错误日志。
		//   渠道名【没有】白名单校验，客户端今天就能 POST {"method":"wechat"}，
		//   所以上面这几条今天就是可达路径。换成别的哨兵、或返回一个「所有
		//   方法都返回未实现」的实例，会同时劣化这四处，且恰好只在最需要
		//   诊断信息的时候。改之前先看 cmd/server/payment_test.go 的两条绊线。
		return nil, payment.ErrUnavailable
	}
	return nil, payment.ErrUnavailable
}

// ─── 支付宝配置 ──────────────────────────────────────────────────────
//
// 全部走环境变量，不进 SQLite。理由与 CUPS_HOST / UPLOAD_DIR / COOKIE_SECURE
// 相同：这些是【部署期】凭据，属于跑这个容器的那台机器，不是系统设置。
// 另外私钥落库还有个具体坏处 —— 它会跟着 data/*.db 一起进备份、进
// 用户随手分享的 issue 附件里。
//
//	ALIPAY_APP_ID       开放平台应用的 AppID
//	ALIPAY_PRIVATE_KEY  应用私钥（PKCS8/PKCS1、PEM 或裸 base64 均可）
//	ALIPAY_PUBLIC_KEY   支付宝公钥（⚠️ 不是应用公钥）
//	ALIPAY_GATEWAY      可选，沙箱用 https://openapi-sandbox.dl.alipaydev.com/gateway.do
//	ALIPAY_AUTH_URL     可选，授权页地址（授权登录用）。留空按 Gateway 是否沙箱推断

// alipayFromEnv 读支付宝凭据。缺任何一项都返回 ErrUnavailable ——
// 半个配置的渠道不该出现在支付方式列表里。
func alipayFromEnv() (payment.AlipayConfig, bool) {
	cfg := payment.AlipayConfig{
		AppID:        strings.TrimSpace(os.Getenv("ALIPAY_APP_ID")),
		PrivateKey:   os.Getenv("ALIPAY_PRIVATE_KEY"),
		PublicKey:    os.Getenv("ALIPAY_PUBLIC_KEY"),
		Gateway:      strings.TrimSpace(os.Getenv("ALIPAY_GATEWAY")),
		AuthorizeURL: strings.TrimSpace(os.Getenv("ALIPAY_AUTH_URL")),
	}
	if cfg.AppID == "" || strings.TrimSpace(cfg.PrivateKey) == "" || strings.TrimSpace(cfg.PublicKey) == "" {
		return payment.AlipayConfig{}, false
	}
	return cfg, true
}

// buildAlipayClient 构造支付宝客户端，返回【具体类型】。
//
// 与 buildAlipay 分开是因为授权登录要用 AuthorizeURL / ExchangeAuthCode，
// 而那两个方法【不在 Provider 接口上】—— 它们是登录专用，mock 和将来的其它
// 渠道都不需要实现，塞进接口只会逼着每个实现写两个空方法。
//
// 每次都重新构造（而不是缓存实例）：NewAlipay 只是两次 DER 解析，
// 相比一次打印请求的文档转换完全是噪音，而省掉的是「改了环境变量要重启
// 才生效」这类难以察觉的状态。密钥解析失败会在这里立刻暴露，
// 而不是等用户点了支付才炸。
func buildAlipayClient() (*payment.Alipay, error) {
	cfg, ok := alipayFromEnv()
	if !ok {
		return nil, payment.ErrUnavailable
	}
	p, err := payment.NewAlipay(cfg)
	if err != nil {
		// 配置了但配错（密钥格式不对）时把原因打出来 —— 这类故障如果只表现为
		// 「支付方式里没有支付宝」，排查会非常痛苦。
		log.Printf("[payment] 支付宝配置无效，该渠道不可用: %v", err)
		return nil, payment.ErrUnavailable
	}
	return p, nil
}

// buildAlipay 供渠道注册表用（要的是 Provider）。
//
// ⚠️ 刻意保留这个薄封装而不是让调用方直接用 buildAlipayClient：后者返回
// 具体指针类型，一个 nil 的 *Alipay 装进 Provider 接口会得到「非 nil 的接口」，
// 那个经典 Go 陷阱会让 `if p == nil` 判不出错。
func buildAlipay() (payment.Provider, error) {
	p, err := buildAlipayClient()
	if err != nil {
		return nil, err
	}
	return p, nil
}

// ─── 微信支付配置（未实现）────────────────────────────────────────────
//
// 这里是留给微信凭据读取的落点，现在【故意是空的】——
// 一个环境变量名都没写，因为凭据的名字本身就是要先确认的东西之一。
//
// 🚫 不要先在这里写一组候选变量名占位。名字一旦落进代码和 .env.example，
// 就成了一份用户得回头推翻的契约；而且此刻它没有消费者 ——
// buildProvider 不会构造微信实例，availableProviders 也有意不列它。
//
// 接手时该做的事，按顺序：
//   1. 先定产品形态（见 buildProvider 里 wechat 分支的问题清单，
//      以及 docs/wechat-pay.md）；
//   2. 再定凭据集 —— 它与产品形态绑定，不是固定的；
//   3. 有【外部判据】之后才写代码。签名与验签尤其如此：本仓库被烧过一次
//      （alipay_test.go 里那个假网关和被测代码犯了同一个错，42 个用例全绿
//      全是错的，直到真实沙箱报 isv.invalid-signature）。没有 oracle 时，
//      写出来的「通用」函数和产品专属函数一样未经核实。
//
// 上一节 alipayConfigSummary 存在的那条理由，对微信同样适用 —— 见下面
// wechatConfigSummary。那两行启动日志是同源的。

// wechatConfigSummary 用一句话说明微信支付的当前状态，启动时打一次。
//
// 存在的理由与 alipayConfigSummary 相同：它回答的是「我明明配了微信，
// 为什么支付方式里没有」—— 不把这句打出来，这个问题的第一反应会是去翻
// 环境变量和 docker-compose，而真实原因是代码还没实现。
func wechatConfigSummary() string {
	return "未实现（支付方式里不会出现微信）"
}

// availableProviders 列出当前真正可用的在线支付渠道。
//
// 用户端展示的支付方式 = "balance" + 这个列表。
//
// ⚠️ 判据是「环境变量齐不齐」，【刻意不做】密钥解析或真实调用。
//
// 为什么不在这里 buildProvider 试一遍：那样密钥写错时该渠道会从列表里
// 无声消失，用户只看到「没有支付宝这个选项」，查不下去。现在这个写法下
// 选项照样出现，点了会得到 400「该支付方式当前不可用」，同时 buildAlipay
// 把具体的解析错误写进服务端日志 —— 故障有迹可循。
//
// 代价是可能展示一个点不通的选项。两害相权，可诊断性更重要。
//
// payment_methods 是【额外的】开关：环境变量齐全但管理员没勾，同样不展示。
//
// ⚠️ 微信【有意】不在这里 —— 不是漏了，别顺手补上。
//
// 这个列表是 /api/wallet 里 paymentMethods 的唯一来源，它驱动三个界面：
// 钱包的充值卡片与渠道下拉、打印弹窗的支付方式列表。而这三个入口最终都会
// 走到 buildProvider("wechat")，那个分支现在恒返回 ErrUnavailable。
// 一旦把它加进来，用户就会在这三个界面上看到一个【必定 400】的选项 ——
// 正是本函数上面那条注释要防的事，而且是它最坏的形态。
//
// 等微信真正实现时，注意这里的判据要给微信一份与支付宝【同构】的
// configured 判定（读它自己的那组环境变量），而不是无脑 append：
// 否则「配置齐全才展示」这套语义会在新渠道上出现分叉。
func availableProviders() []string {
	var names []string

	// 连环境变量都没有的渠道直接跳过 —— 那种情况确实是「没配」，
	// 不是「配错了」，不需要展示。
	if _, configured := alipayFromEnv(); configured {
		names = append(names, store.PaymentProviderAlipay)
	}
	if paymentMockEnabled() {
		names = append(names, store.PaymentProviderMock)
	}
	return intersectWithConfigured(names)
}

// intersectWithConfigured 与管理员勾选的 payment_methods 求交集。
//
// payment_methods 为空表示「未做限制」——此时全放行。这是为了让升级路径
// 平滑：老部署没有这个设置项，不该因为升级就丢掉刚配好的渠道。
func intersectWithConfigured(available []string) []string {
	enabled := configuredPaymentMethods(context.Background())
	if len(enabled) == 0 {
		return available
	}
	want := make(map[string]bool, len(enabled))
	for _, n := range enabled {
		want[n] = true
	}
	out := make([]string, 0, len(available))
	for _, n := range available {
		if want[n] {
			out = append(out, n)
		}
	}
	return out
}

// configuredPaymentMethods 读管理员配置的启用渠道（逗号分隔）。
func configuredPaymentMethods(ctx context.Context) []string {
	var raw string
	_ = appStore.WithTx(ctx, true, func(tx *sql.Tx) error {
		v, err := store.GetSettingString(ctx, tx, store.SettingPaymentMethods, "")
		if err != nil {
			return err
		}
		raw = v
		return nil
	})
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if name := strings.TrimSpace(part); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// newOutTradeNo 生成商户订单号。
//
// 渠道对订单号的字符集有要求（支付宝：字母数字下划线，≤64 位），
// 所以只用大写字母与数字，前缀区分用途便于对账时肉眼分类。
func newOutTradeNo(prefix string) string {
	return prefix + time.Now().UTC().Format("20060102150405") + randomToken()[:8]
}
