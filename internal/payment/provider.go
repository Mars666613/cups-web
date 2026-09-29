// Package payment 是支付渠道的抽象层。
//
// 设计目标只有一个：让「本地 mock」与「真实支付宝/微信」可以互换，
// 而结算逻辑（把到账的钱记进账本、把订单推进到已支付）【只写一份】。
//
// 渠道之间的差异被压缩到三条：怎么发起支付、怎么查单、怎么验签回调。
// 钱到账之后做什么，由 cmd/server 的 settlePayment 统一处理 ——
// 那部分是渠道无关的，也是唯一会动到账本与订单状态的地方。
package payment

import (
	"context"
	"errors"
	"net/http"
	"time"
)

var (
	// ErrNotSupported 该渠道不支持这个操作。
	ErrNotSupported = errors.New("payment: operation not supported")
	// ErrUnavailable 渠道未配置（缺密钥、未启用）。
	ErrUnavailable = errors.New("payment: provider unavailable")
	// ErrBadSignature 回调验签失败。绝不能继续结算。
	ErrBadSignature = errors.New("payment: bad signature")
)

// Request 是发起一笔支付所需的全部信息。
type Request struct {
	OutTradeNo  string // 商户订单号，全局唯一（payments.out_trade_no）
	AmountCents int64
	Subject     string // 支付标题，展示在渠道的收银台/账单上
	UserID      int64
	Username    string
	NotifyURL   string // 异步通知地址；内网部署为空
	ReturnURL   string // 前台回跳地址：付完把浏览器送回我们这儿
	ExpiresAt   time.Time

	// Channel 是【用户当下拿的是什么设备】，决定同一渠道用哪种收款方式。
	//
	// ⚠️ 这不是「渠道」（渠道是 alipay/wechat），而是渠道内部的两种形态：
	//   手机 → 跳收银台（收银台会自动唤起 App，付完支付宝把人送回来）
	//   电脑 → 给二维码（电脑上没装支付宝，只能让手机来扫）
	//
	// 同一个用户换个设备答案就变了，服务端没法猜，所以由前端判断后传上来。
	// 空值按 ChannelQRCode 处理。
	Channel string
}

// 收款方式。
const (
	// ChannelQRCode 返一串内容让【另一台设备】扫（支付宝当面付 precreate）。
	// 电脑端的正路；手机端在拿不到回跳地址时的兜底。
	ChannelQRCode = "qrcode"
	// ChannelWap 返一个收银台地址让【当前浏览器】跳过去（支付宝手机网站支付 wap.pay）。
	// 手机端的正路。
	ChannelWap = "wap"
)

// WantsWap 判断调用方要的是不是手机收银台。
func (r Request) WantsWap() bool { return r.Channel == ChannelWap }

// Credential 是发起支付后给前端的凭据。
type Credential struct {
	OutTradeNo string
	QRCodeText string // 二维码内容（真渠道才有意义，mock 不渲染二维码）
	PayURL     string
	ExpiresAt  time.Time
	Raw        string // 原样落 payments.prepay_payload，排障用
}

// RefundRequest 是一笔【原路退回】。
//
// 与订单失败退款（退回余额）是两件事：那个不经过渠道，钱没离开系统；
// 这个要把钱真的退回用户的支付宝。
type RefundRequest struct {
	OutTradeNo string // 原支付单号，退款必须挂在它上面
	// RefundNo 是商户退款单号（支付宝的 out_request_no）。
	//
	// ⚠️ 同一笔交易分多次退款时它必须【各不相同】，重试同一笔退款时
	// 必须【沿用原值】—— 渠道靠它做幂等，换了就会被当成新的一笔退款。
	RefundNo    string
	AmountCents int64
	Reason      string
}

// RefundResult 是渠道侧的退款结果。
//
// 返回 nil error 即表示【渠道已受理】。支付宝的退款是同步受理、异步到账，
// 受理成功就代表钱会退回，不需要再查。
type RefundResult struct {
	OutTradeNo      string
	RefundNo        string
	AmountCents     int64
	ChannelRefundID string // 渠道退款单号，对账用
	Raw             string
}

// Result 是一笔支付在渠道侧的最终状态。
type Result struct {
	OutTradeNo    string
	TransactionID string
	AmountCents   int64
	Paid          bool
	Raw           string
}

// Provider 是一个支付渠道。
type Provider interface {
	// Name 返回渠道标识，与 payments.provider 一致（mock / alipay / wechat）。
	Name() string

	// Create 发起一笔支付，返回给前端的凭据。
	Create(ctx context.Context, req Request) (Credential, error)

	// Query 主动查单。
	//
	// 这是内网 AIO 部署下的【唯一】结算途径：本项目的典型形态是单容器
	// 跑在内网，没有公网 HTTPS 域名，渠道的异步通知根本到不了。
	// 支付宝的 alipay.trade.query / 微信的
	// GET /v3/pay/transactions/out-trade-no/{} 都不需要回调地址。
	Query(ctx context.Context, outTradeNo string) (Result, error)

	// Refund 把一笔已支付的交易【原路退回】给付款人。
	//
	// 只对 status='paid' 的交易有效，且累计退款额不能超过原交易金额。
	// 支持部分退款：同一笔交易可以多次调用，靠 RefundNo 区分。
	Refund(ctx context.Context, req RefundRequest) (RefundResult, error)

	// Close 关闭渠道侧一笔尚未支付的交易。
	//
	// 用途：二维码到期后自动刷新时会重新建一笔支付单，必须把旧单在渠道侧
	// 关掉。否则用户若扫了【旧】二维码付款，钱会到账，但那笔支付单下面
	// 已经没有订单了（订单被重新挂到新支付单上）—— 结果是「付了钱什么
	// 都没打出来」，而且钱在渠道侧，我们无法自动原路退回。
	//
	// 已经是终态（已支付/已关闭）或交易不存在时应当返回 nil：
	// 那不是失败，只是「没什么可关的」。
	Close(ctx context.Context, outTradeNo string) error

	// VerifyCallback 验签并解析回调。
	//
	// 渠道私有的签名算法封在这里，handler 只拿到结论 —— 这样新增一个渠道
	// 不需要动 handler，也不需要让它接触任何密钥。
	VerifyCallback(r *http.Request, body []byte) (Result, error)

	// CallbackAck 渠道要求的同步应答体（支付宝要 "success"；微信 v3 是 204 空体）。
	// 应答不对的话渠道会一直重试投递。
	CallbackAck() (status int, body []byte)
}
