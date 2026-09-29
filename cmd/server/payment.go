package main

import (
	"context"
	"database/sql"
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
		// 微信 v3 需要平台证书下载轮换 + 回调 AES-GCM 解密，尚未实现。
		// 显式返回「不可用」而不是静默失败，好让日志能定位。
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

// alipayFromEnv 读支付宝凭据。缺任何一项都返回 ErrUnavailable ——
// 半个配置的渠道不该出现在支付方式列表里。
func alipayFromEnv() (payment.AlipayConfig, bool) {
	cfg := payment.AlipayConfig{
		AppID:      strings.TrimSpace(os.Getenv("ALIPAY_APP_ID")),
		PrivateKey: os.Getenv("ALIPAY_PRIVATE_KEY"),
		PublicKey:  os.Getenv("ALIPAY_PUBLIC_KEY"),
		Gateway:    strings.TrimSpace(os.Getenv("ALIPAY_GATEWAY")),
	}
	if cfg.AppID == "" || strings.TrimSpace(cfg.PrivateKey) == "" || strings.TrimSpace(cfg.PublicKey) == "" {
		return payment.AlipayConfig{}, false
	}
	return cfg, true
}

// buildAlipay 构造支付宝渠道。
//
// 每次都重新构造（而不是缓存实例）：NewAlipay 只是两次 DER 解析，
// 相比一次打印请求的文档转换完全是噪音，而省掉的是「改了环境变量要重启
// 才生效」这类难以察觉的状态。密钥解析失败会在这里立刻暴露，
// 而不是等用户点了支付才炸。
func buildAlipay() (payment.Provider, error) {
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
