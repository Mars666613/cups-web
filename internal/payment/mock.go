package payment

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

// Mock 是本地开发用的支付渠道。
//
// 它【不假装自己是真的】：
//   - Create 返回一个可直接触发的凭据（没有真二维码可扫）
//   - Query 永远返回「未支付」—— mock 没有外部状态可查，结算靠显式触发
//   - VerifyCallback 不验签，直接从请求体里取字段
//
// 关键点：mock 的「支付成功」走的是与真实回调【完全相同】的 settlePayment 内核。
// 于是「下单 → 支付 → 打印 → 退款」的全部业务分支在本地都会真实执行，
// 只有「跟渠道通信」这一段是假的 —— 真实渠道接进来时换的也只是这一段。
type Mock struct{}

func (m *Mock) Name() string { return "mock" }

func (m *Mock) Create(_ context.Context, req Request) (Credential, error) {
	return Credential{
		OutTradeNo: req.OutTradeNo,
		QRCodeText: "mockpay://" + req.OutTradeNo,
		PayURL:     "/#/wallet?mock=" + req.OutTradeNo,
		ExpiresAt:  req.ExpiresAt,
		Raw:        `{"provider":"mock"}`,
	}, nil
}

// Query 恒返回「未支付」。
//
// 这不是偷懒：mock 背后没有任何真实渠道，也就没有「钱到没到」这个外部事实
// 可供查询。本地演练走的是 mockPayHandler 显式构造 Result 那条路。
func (m *Mock) Query(_ context.Context, outTradeNo string) (Result, error) {
	return Result{OutTradeNo: outTradeNo, Paid: false}, nil
}

// VerifyCallback 解析回调。
//
// 支持表单与 JSON 两种格式：表单是为了能用 curl 手工重放回调做演练
// （真实渠道的回调大多是表单），JSON 方便脚本构造。
func (m *Mock) VerifyCallback(_ *http.Request, body []byte) (Result, error) {
	outTradeNo, amountCents, txn := parseMockPayload(body)
	if outTradeNo == "" {
		return Result{}, ErrBadSignature
	}
	return Result{
		OutTradeNo:    outTradeNo,
		TransactionID: txn,
		AmountCents:   amountCents,
		Paid:          true,
		Raw:           string(body),
	}, nil
}

func (m *Mock) CallbackAck() (int, []byte) {
	return http.StatusOK, []byte("success")
}

// Close 是空操作：mock 背后没有真实渠道，没有「未支付的交易」这种东西。
// 返回 nil 而不是 ErrNotSupported —— 刷新二维码时不该因为用的是 mock 就报错。
func (m *Mock) Close(_ context.Context, _ string) error { return nil }

// Refund 恒成功：mock 没有真实的资金流，无从失败。
//
// 但它在整条链路里【不是空转】—— 调用方照样要扣余额、写账本、更新
// payments.refunded_cents，那些才是退款里真正容易写错的部分。
func (m *Mock) Refund(_ context.Context, req RefundRequest) (RefundResult, error) {
	return RefundResult{
		OutTradeNo:      req.OutTradeNo,
		RefundNo:        req.RefundNo,
		AmountCents:     req.AmountCents,
		ChannelRefundID: "MOCKRF" + req.RefundNo,
		Raw:             `{"provider":"mock"}`,
	}, nil
}

func parseMockPayload(body []byte) (outTradeNo string, amountCents int64, txn string) {
	if len(body) == 0 {
		return "", 0, ""
	}

	// 表单格式（真实渠道的常见形态）
	if values, err := url.ParseQuery(string(body)); err == nil && values.Get("out_trade_no") != "" {
		amount, _ := strconv.ParseInt(values.Get("amount_cents"), 10, 64)
		return values.Get("out_trade_no"), amount, values.Get("transaction_id")
	}

	// JSON 格式
	var payload struct {
		OutTradeNo    string `json:"out_trade_no"`
		AmountCents   int64  `json:"amount_cents"`
		TransactionID string `json:"transaction_id"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		return payload.OutTradeNo, payload.AmountCents, payload.TransactionID
	}
	return "", 0, ""
}
