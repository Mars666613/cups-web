package main

// 支付凭据有效期的测试。
//
// 这个值会变成发给支付宝的 timeout_express，直接决定用户能在收银台上待多久。
// 判据错了只有两种后果，都很贵：
//
//	太短 → 用户还没付完，交易就被渠道关闭，收银台报「操作已超时，请重新发起」
//	太长 → 二维码活得比订单还久，出现「码还能扫，但订单已经过期回收了」
//
// 实测踩过第一种：收银台沿用了二维码那 3 分钟。

import (
	"testing"
	"time"

	"cups-web/internal/payment"
)

func TestCredTTLFor(t *testing.T) {
	const orderTTL = 15 * time.Minute

	cases := []struct {
		name    string
		channel string
		order   time.Duration
		want    time.Duration
	}{
		{
			// 核心用例：收银台上用户已经离开我们的页面，要给足付款时间，
			// 不能受二维码那 3 分钟的限制。
			name:    "手机收银台跟订单走，不受二维码 3 分钟限制",
			channel: payment.ChannelWap, order: orderTTL, want: orderTTL,
		},
		{
			// 电脑收银台同理 —— 用户同样已经离开了我们的页面。
			name:    "电脑收银台也跟订单走",
			channel: payment.ChannelPage, order: orderTTL, want: orderTTL,
		},
		{
			name:    "电脑收银台在订单很短时也跟着订单",
			channel: payment.ChannelPage, order: time.Minute, want: time.Minute,
		},
		{
			// 但也不能比订单长，否则「还能付，订单却已过期回收」。
			name:    "订单比二维码还短时，收银台也跟着订单",
			channel: payment.ChannelWap, order: time.Minute, want: time.Minute,
		},
		{
			name:    "二维码用二维码 TTL",
			channel: payment.ChannelQRCode, order: orderTTL, want: paymentQRTTL,
		},
		{
			name:    "订单比二维码短时取较短的",
			channel: payment.ChannelQRCode, order: time.Minute, want: time.Minute,
		},
		{
			// 空串按二维码处理 —— 与 createPayment 里的默认值一致。
			name:    "空渠道按二维码算",
			channel: "", order: orderTTL, want: paymentQRTTL,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := credTTLFor(c.channel, c.order); got != c.want {
				t.Errorf("credTTLFor(%q, %v) = %v，期望 %v", c.channel, c.order, got, c.want)
			}
		})
	}

	// 反过来钉一句：两条收银台路都不该等于二维码那个短值。
	// 这条断言让「不小心改回共用一个值」直接挂测试。
	for _, ch := range []string{payment.ChannelWap, payment.ChannelPage} {
		if credTTLFor(ch, orderTTL) == paymentQRTTL {
			t.Errorf("%s 的有效期不该等于二维码 TTL（3 分钟）—— 用户来不及付完", ch)
		}
	}
}
