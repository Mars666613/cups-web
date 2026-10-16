package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"cups-web/internal/devicealert"
	"cups-web/internal/ipp"
	"cups-web/internal/store"
)

// 设备告警的通知外发。
//
// 这一层只做一件事：把一条【已经是人话】的消息 POST 到管理员配的 webhook。
// 判定在 internal/devicealert，决定「这一条该不该发、什么时候发」在
// device_alert.go。分开的好处是改消息格式不会碰到去重逻辑 —— 而后者是这类
// 功能里最容易出错、也最需要单独测的部分。
//
// ── 支持的渠道与它们的差别 ──
//
// 企业微信机器人与钉钉机器人是同一个形状（{"msgtype":"text","text":{"content":…}}），
// 飞书是另一个（{"msg_type":"text","content":{"text":…}}），其余按通用
// {"text":…} 发（Slack、自建转发、ntfy 之类大多吃这一种）。
//
// ⚠️ 三种渠道【失败时都可能返回 HTTP 200】+ 非 0 的 errcode。只看状态码会把
// 「token 少打了一个字符」当成发送成功，而告警静默失效是最难发现的故障。
// 所以响应体一定要解析（见 alertResponseErr）。

// alertWebhookTimeout 是单次通知的超时。
//
// 8 秒：告警消息几百字节，正常毫秒级返回。这个调用发生在巡检 goroutine 里、
// 在数据库事务【之外】（见 device_alert.go），所以慢一点不会锁住任何东西，
// 但也不该让一轮巡检停在这里。
const alertWebhookTimeout = 8 * time.Second

// alertHTTPClient 是通知专用的出站客户端，进程内共用一个以复用连接池。
//
// 用 ipp.NewNotifyClient 而不是打印机那个客户端：它不吃
// PRINTER_HOST_ALLOWLIST / PRINTER_BLOCK_PRIVATE —— 那两条是为「登录用户
// 传入的打印机地址」设的，而告警最需要送达的恰恰是内网机器人。理由写在
// internal/ipp/security.go 的 NewNotifyClient 上。
var alertHTTPClient = ipp.NewNotifyClient(alertWebhookTimeout)

// alertConfig 是一次巡检用到的全部告警配置。
//
// 一次读出来再往下传，而不是让每处调用各读各的：同一轮巡检里开关和地址
// 必须来自同一个瞬间，否则会出现「这一轮按新开关、旧地址发」。
type alertConfig struct {
	Enabled       bool
	WebhookURL    string
	WebhookKind   string
	RepeatMinutes int64
	SupplyLowPct  int
}

func loadAlertConfig(ctx context.Context, tx *sql.Tx) (alertConfig, error) {
	var cfg alertConfig
	var err error

	enabled, err := store.GetSettingInt(ctx, tx, store.SettingAlertEnabled, 0)
	if err != nil {
		return cfg, err
	}
	cfg.Enabled = enabled != 0

	if cfg.WebhookURL, err = store.GetSettingString(ctx, tx, store.SettingAlertWebhookURL, ""); err != nil {
		return cfg, err
	}
	cfg.WebhookURL = strings.TrimSpace(cfg.WebhookURL)

	if cfg.WebhookKind, err = store.GetSettingString(ctx, tx, store.SettingAlertWebhookKind, "auto"); err != nil {
		return cfg, err
	}
	if cfg.RepeatMinutes, err = store.GetSettingInt(ctx, tx, store.SettingAlertRepeatMinutes, 60); err != nil {
		return cfg, err
	}
	lowPct, err := store.GetSettingInt(ctx, tx, store.SettingAlertSupplyLowPercent, 10)
	if err != nil {
		return cfg, err
	}
	if lowPct < 0 {
		lowPct = 0
	}
	cfg.SupplyLowPct = int(lowPct)
	return cfg, nil
}

// repeatInterval 把「分钟」换算成时长。0 表示只发首次、不重发。
func (c alertConfig) repeatInterval() time.Duration {
	if c.RepeatMinutes <= 0 {
		return 0
	}
	return time.Duration(c.RepeatMinutes) * time.Minute
}

// resolveAlertKind 决定消息体的格式。
//
// "auto"（或认不出的值）按主机名推断：管理员粘贴 URL 时不必再去想「这是哪个
// 渠道」—— 而这个信息本来就在 URL 里。
func resolveAlertKind(kind, rawURL string) string {
	switch k := strings.ToLower(strings.TrimSpace(kind)); k {
	case "wechat", "dingtalk", "feishu", "generic":
		return k
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "generic"
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case strings.HasSuffix(host, "weixin.qq.com"):
		return "wechat"
	case strings.HasSuffix(host, "dingtalk.com"):
		return "dingtalk"
	case strings.HasSuffix(host, "feishu.cn"), strings.HasSuffix(host, "larksuite.com"):
		return "feishu"
	default:
		return "generic"
	}
}

// alertBody 按渠道拼请求体。
func alertBody(kind, text string) ([]byte, error) {
	switch kind {
	case "wechat", "dingtalk":
		return json.Marshal(map[string]any{
			"msgtype": "text",
			"text":    map[string]string{"content": text},
		})
	case "feishu":
		return json.Marshal(map[string]any{
			"msg_type": "text",
			"content":  map[string]string{"text": text},
		})
	default:
		return json.Marshal(map[string]any{"text": text})
	}
}

// alertResponse 覆盖三种渠道的响应形状。指针字段是为了区分「渠道没返回这个
// 字段」和「返回了 0」—— 0 才是成功。
type alertResponse struct {
	ErrCode    *int   `json:"errcode"`       // 企业微信
	ErrMsg     string `json:"errmsg"`        //
	Code       *int   `json:"code"`          // 钉钉
	Message    string `json:"message"`       //
	StatusCode *int   `json:"StatusCode"`    // 飞书
	StatusMsg  string `json:"StatusMessage"` //
}

// alertResponseErr 把「HTTP 200 但其实失败了」判出来。
//
// 响应体解析不了就不当失败：自建的转发服务常常回一个空 body 或一句纯文本，
// 那不是错 —— 而把正常发送误判成失败，会让同一轮告警每 60 秒重试一次。
func alertResponseErr(body []byte) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	var r alertResponse
	if err := json.Unmarshal(trimmed, &r); err != nil {
		return nil
	}
	switch {
	case r.ErrCode != nil && *r.ErrCode != 0:
		return fmt.Errorf("渠道返回 errcode=%d %s", *r.ErrCode, r.ErrMsg)
	case r.Code != nil && *r.Code != 0:
		return fmt.Errorf("渠道返回 code=%d %s", *r.Code, r.Message)
	case r.StatusCode != nil && *r.StatusCode != 0:
		return fmt.Errorf("渠道返回 StatusCode=%d %s", *r.StatusCode, r.StatusMsg)
	}
	return nil
}

// postAlert 发一次通知。失败返回 error —— 调用方据此【不】写 notified_at，
// 于是下一轮巡检会重试。这条约定是「配置写错一个字符不该静默吃掉整轮告警」
// 的全部依据。
func postAlert(ctx context.Context, cfg alertConfig, text string) error {
	if cfg.WebhookURL == "" {
		return fmt.Errorf("未配置通知地址")
	}
	kind := resolveAlertKind(cfg.WebhookKind, cfg.WebhookURL)
	payload, err := alertBody(kind, text)
	if err != nil {
		return err
	}

	reqCtx, cancel := context.WithTimeout(ctx, alertWebhookTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, cfg.WebhookURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := alertHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))

	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return alertResponseErr(body)
}

// ── 消息文案 ────────────────────────────────────────────────────────

// alertNotice 是一条通知的全部素材。
//
// 刻意收成一个结构体而不是一长串参数：正文字段时有增减，位置参数一多就
// 「把开始时间传到了详情那一格」，而那种错在测试里才看得出来。
type alertNotice struct {
	PrinterName string
	PrinterURI  string
	Kind        string
	Severity    string
	Detail      string
	// At 是消息里那一个时间：未恢复时是「开始时间」，已恢复时是「恢复时间」。
	At       string
	Resolved bool
}

// notifyTimeZone 是通知里显示时间的时区。
//
// 固定 +8，不走 time.LoadLocation：容器镜像里可能没有 tzdata，那时
// LoadLocation 会报错 —— 而这行字只是给人看的时间，不该有失败路径。
// 与 internal/payment 的 alipayLocation 是同一条理由（那边多一层
// LoadLocation 是因为它要先试命名时区）。
var notifyTimeZone = time.FixedZone("CST", 8*3600)

// alertTimeText 把库里的 RFC3339 UTC 转成东八区的「月-日 时:分」。
func alertTimeText(rfc3339 string) string {
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return rfc3339
	}
	return t.In(notifyTimeZone).Format("2006-01-02 15:04")
}

// printerShortName 取队列 URI 的最后一段（界面上也是这么显示的）。
func printerShortName(uri string) string {
	if i := strings.LastIndex(uri, "/"); i >= 0 && i+1 < len(uri) {
		return uri[i+1:]
	}
	return uri
}

// formatAlertNotice 拼一条人读的正文。
//
// 顺序按「一眼看到最要紧的」排：什么毛病 → 哪台机器 → 原文 → 什么时候开始。
// 最后一行是给半夜看到这条消息的人的行动指引 —— 没有它，消息读完仍然不知道
// 「那我该干什么」。
func formatAlertNotice(n alertNotice) string {
	head := "【打印服务告警】"
	if n.Resolved {
		head = "【打印服务恢复】"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s%s（%s）\n", head, devicealert.KindLabel(n.Kind), devicealert.SeverityLabel(n.Severity))
	fmt.Fprintf(&b, "打印机：%s\n", n.PrinterName)

	if n.Detail != "" {
		fmt.Fprintf(&b, "详情：%s\n", n.Detail)
	}
	if n.Resolved {
		fmt.Fprintf(&b, "恢复时间：%s\n", alertTimeText(n.At))
	} else {
		fmt.Fprintf(&b, "开始时间：%s\n", alertTimeText(n.At))
		b.WriteString("请检查纸张、耗材与电源；处理完可在「系统管理 → 设备告警」标记已处理。\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// noticeFromEvent 把库里的事件行翻成通知素材。
//
// printerName 由调用方给（它来自这一轮列队列的结果）；为空时退回 URI 末段，
// 免得消息里出现「打印机：」后面什么都没有。
func noticeFromEvent(e store.DeviceEvent, printerName string, resolved bool) alertNotice {
	name := strings.TrimSpace(printerName)
	if name == "" {
		name = printerShortName(e.PrinterURI)
	}
	at := e.StartedAt
	if resolved {
		at = e.ResolvedAt
	}
	return alertNotice{
		PrinterName: name,
		PrinterURI:  e.PrinterURI,
		Kind:        e.Kind,
		Severity:    e.Severity,
		Detail:      e.Detail,
		At:          at,
		Resolved:    resolved,
	}
}
