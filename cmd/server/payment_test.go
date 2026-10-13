package main

import (
	"errors"
	"testing"

	"cups-web/internal/auth"
	"cups-web/internal/payment"
	"cups-web/internal/store"
)

// setTestStoreForProviders 把包级 appStore 指到一个临时库，返回还原函数。
//
// availableProviders 会经 intersectWithConfigured 读 payment_methods，
// 那条路真的过一次 DB —— appStore 为 nil 时是空指针 panic，不是返回错误
// （见 store.WithTx 里的 s.DB）。
func setTestStoreForProviders(t *testing.T) func() {
	t.Helper()
	s, _ := newTestEnv(t)
	prev := appStore
	appStore = s

	// ⚠️ 必须一起装会话密钥。issueSession（签发登录态）要走
	// auth.SetSession，而它依赖 SetupSecureCookie 生成的密钥 ——
	// 生产里那是 main.go:91 调的，测试里没人调。
	// 少了这一句，任何走到「签发会话」的测试都会以 500「session error」失败，
	// 而那句错误完全指不到根因（看起来像鉴权逻辑坏了）。
	//
	// 🚫 不要在单个测试里各调一次：这里是唯一共享的入口，
	// 放这儿新加的测试才不会再踩同一个坑。
	if err := auth.SetupSecureCookie(s.DB); err != nil {
		t.Fatalf("初始化会话密钥失败: %v", err)
	}

	return func() { appStore = prev }
}

// TestWechatBranchStaysUnavailable 微信分支必须继续返回 ErrUnavailable。
//
// 守的是一个【今天真的可达】的行为：渠道名没有白名单校验
// （order_print.go 与 billing_handlers.go 都直接取请求体里的 method/provider），
// 所以客户端现在就能 POST {"method":"wechat"}。
//
// ErrUnavailable 让 writePaymentStartError 回 400「该支付方式当前不可用」；
// 换成别的哨兵会掉进 default 分支，变成 502 并把内部错误原文吐给客户端。
// 查单 handler（400 vs 502）与回调 handler（404「unknown provider」）同理。
//
// 期望值来源是【现有行为】而不是协议，所以这条不依赖任何未经核实的知识。
//
// 🚫 接入微信时本测试【应当失败】—— 那不是 bug，是提醒你连同 buildProvider
// 的分支与 availableProviders 一起改。别为了让绿灯把断言删掉。
func TestWechatBranchStaysUnavailable(t *testing.T) {
	p, err := buildProvider(store.PaymentProviderWechat)
	if !errors.Is(err, payment.ErrUnavailable) {
		t.Fatalf("buildProvider(%q) = (%v, %v)，期望 ErrUnavailable。\n"+
			"若你正在接入微信：请同时确认 availableProviders 的 configured 判定"+
			"（要与支付宝同构）已更新，并改掉本测试。",
			store.PaymentProviderWechat, p, err)
	}
	if p != nil {
		t.Fatalf("返回 ErrUnavailable 时实例应为 nil，实际拿到 %v", p)
	}
}

// TestWechatNotAdvertised 微信不应出现在支付方式列表里。
//
// 期望值来源是【策略】不是协议（见 AGENTS.md：避免前端出现「点了必然失败」
// 的按钮）：availableProviders 是 /api/wallet 里 paymentMethods 的唯一来源，
// 它驱动钱包充值的渠道下拉与打印弹窗的支付方式列表，而这些入口最终都会走到
// buildProvider("wechat") —— 那个分支现在恒返回 ErrUnavailable。列出来就等于
// 在三个界面上摆一个必定 400 的选项。
//
// ⚠️ 这条测试的覆盖面【得说清楚，它比看上去窄】：
//
// 实测过（往 availableProviders 里无条件 append 一个 wechat，本测试确实
// 失败了）—— 所以它能拦住「不判断配置就无脑加进去」这种写法。
//
// 但它拦不住「按支付宝那样做成 env 门控的 append」：那种实现下，本测试环境
// 没有微信的环境变量，列表里就不会有 wechat，断言照样通过。要拦住那种写法，
// 得知道微信的环境变量叫什么（现在【故意没定】，见 payment.go 里
// 「微信支付配置（未实现）」一节），或者等渠道真正接入。
//
// 也写不了更强的不变量（「列表里的渠道都必须可构造」）—— 那条在本仓库
// 【故意不成立】：availableProviders 明确选择「密钥配错也照常展示，点了报
// 400、日志里能查到原因」，见它自己的注释。
//
// 也就是说：这条是补丁，不是护栏；真正的护栏是 availableProviders 上那段
// 注释与代码评审。
//
// 🚫 接入微信时本测试【应当失败】—— 同上，改的时候一起改。
func TestWechatNotAdvertised(t *testing.T) {
	restore := setTestStoreForProviders(t)
	defer restore()

	got := availableProviders()
	for _, name := range got {
		if name == store.PaymentProviderWechat {
			t.Fatalf("微信出现在了支付方式列表里（%v），但 buildProvider 还构造不出微信实例 —— "+
				"用户会在钱包充值、渠道下拉、打印支付弹窗三处看到一个必定失败的选项。\n"+
				"接入微信时请连同 buildProvider 与该断言一起更新。", got)
		}
	}
}
