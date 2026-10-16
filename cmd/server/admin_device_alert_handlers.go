package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cups-web/internal/devicealert"
	"cups-web/internal/store"

	"github.com/gorilla/mux"
)

// 设备告警的管理端接口。
//
// 三个都是给「看到告警之后要做什么」用的：
//
//	GET  /api/admin/device-events          现在和过去都出过什么问题
//	POST /api/admin/device-events/{id}/resolve
//	                                       我知道这个毛病了，别再提醒我
//	POST /api/admin/device-events/test     我配的 webhook 到底通不通
//
// 最后一个看着不起眼，但它是这个功能能不能被用起来的关键：配 webhook 是
// 「粘一串 URL 到一个输入框」，粘错了不会有任何反馈 —— 直到某天真的卡纸而
// 一条消息都没发出去。有了它，配完当场就能验。

// deviceEventPageSize 是列表每页条数。与审计同样的服务端分页。
const deviceEventPageSize = 100

type deviceEventResponse struct {
	store.DeviceEvent
	KindLabel     string `json:"kindLabel"`
	SeverityLabel string `json:"severityLabel"`
	// PrinterName 是队列名（URI 末段）。界面上写全 URI 太长，而队列名足够定位。
	PrinterName string `json:"printerName"`
	// Muted 表示这一类异常当前被静音（管理员已点过「已处理」，而毛病还在）。
	Muted bool `json:"muted"`
}

type deviceEventListResponse struct {
	Events   []deviceEventResponse `json:"events"`
	Total    int                   `json:"total"`
	Page     int                   `json:"page"`
	PageSize int                   `json:"pageSize"`
	// OpenCount 是「现在还没恢复」的条数，前端横幅直接用。
	OpenCount int `json:"openCount"`
	// Kinds 是全部类别及其中文标签，供筛选下拉。
	//
	// 这里【写全】而不是像审计那样从数据里取：类别的取值域是有限的、由代码
	// 定义的（devicealert.AllKinds），而按数据取的话，没出现过的类别就选不到 ——
	// 管理员想「看看历史上卡纸出过几次」时会发现下拉里没有「卡纸」。
	Kinds map[string]string `json:"kinds"`
}

func adminListDeviceEventsHandler(w http.ResponseWriter, r *http.Request) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	switch status {
	case "open", "resolved":
	default:
		status = ""
	}
	page := 1
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 0 {
		page = p
	}

	var (
		rows      []store.DeviceEvent
		total     int
		openCount int
		mutes     map[string]map[string]bool
	)
	err := appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
		var err error
		if rows, err = store.ListDeviceEvents(r.Context(), tx, status, deviceEventPageSize, (page-1)*deviceEventPageSize); err != nil {
			return err
		}
		if total, err = store.CountDeviceEvents(r.Context(), tx, status); err != nil {
			return err
		}
		if openCount, err = store.CountOpenDeviceEvents(r.Context(), tx); err != nil {
			return err
		}
		mutes, err = store.ListAllDeviceMutes(r.Context(), tx)
		return err
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load device events")
		return
	}

	kinds := map[string]string{}
	for _, k := range devicealert.AllKinds() {
		kinds[k] = devicealert.KindLabel(k)
	}

	events := make([]deviceEventResponse, 0, len(rows))
	for _, e := range rows {
		events = append(events, deviceEventResponse{
			DeviceEvent:   e,
			KindLabel:     devicealert.KindLabel(e.Kind),
			SeverityLabel: devicealert.SeverityLabel(e.Severity),
			PrinterName:   printerShortName(e.PrinterURI),
			Muted:         mutes[e.PrinterURI][e.Kind],
		})
	}

	writeJSON(w, deviceEventListResponse{
		Events:    events,
		Total:     total,
		Page:      page,
		PageSize:  deviceEventPageSize,
		OpenCount: openCount,
		Kinds:     kinds,
	})
}

// adminResolveDeviceEventHandler 处理 POST /api/admin/device-events/{id}/resolve
//
// 语义是「已知悉，别再提醒我这一类」，【不只是】把这一条关掉：
//
// 只关这一条的话，下一轮巡检（60 秒后）会发现原因还在，重新开一条并重新发通知
// —— 按钮的效果只维持一分钟。而真需要这个按钮的场景恰恰是信号本身是错的
// （比如某台 USB 打印机因为一次历史作业一直报着 media-empty，而纸盒里明明有纸），
// 那种信号不会自己消失。
//
// 所以这里同时写一条静音。静音的解除条件是「该类别先彻底消失一次」，之后
// 再犯就是新问题，照常告警（见 schema.sql 里 device_alert_mutes 的说明）。
func adminResolveDeviceEventHandler(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil || id <= 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid device event id")
		return
	}
	actor := sessionUsername(r)
	now := time.Now().UTC().Format(time.RFC3339)

	var (
		ev      store.DeviceEvent
		changed bool
	)
	err = appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
		var err error
		if ev, err = store.GetDeviceEvent(r.Context(), tx, id); err != nil {
			return err
		}
		if changed, err = store.ResolveDeviceEventByID(r.Context(), tx, id, now); err != nil {
			return err
		}
		if err := store.MuteDeviceKind(r.Context(), tx, ev.PrinterURI, ev.Kind, actor, now); err != nil {
			return err
		}
		summary := fmt.Sprintf("静音「%s」@ %s（该类别再彻底消失一次后恢复正常告警）",
			devicealert.KindLabel(ev.Kind), ev.PrinterURI)
		return recordAudit(r.Context(), tx, actor, store.AuditDeviceResolve,
			ev.PrinterURI, summary)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "device event not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to resolve device event")
		return
	}

	writeJSON(w, map[string]any{
		"ok": true,
		// wasOpen 为假说明这一条本来就已恢复 —— 静音仍然写进去了，
		// 所以前端应当提示「这一类已静音」而不是「已处理」。
		"wasOpen": changed,
	})
}

// adminTestAlertHandler 处理 POST /api/admin/device-events/test
//
// 用当前（未保存的）配置发一条测试消息 —— 请求体里带 URL 与渠道，所以管理员
// 可以在【保存之前】先试通，而不是「保存 → 等下一轮巡检 → 什么也没发生」。
func adminTestAlertHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WebhookURL  string `json:"webhookUrl"`
		WebhookKind string `json:"webhookKind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	cfg := alertConfig{
		WebhookURL:  strings.TrimSpace(body.WebhookURL),
		WebhookKind: strings.TrimSpace(body.WebhookKind),
	}
	if cfg.WebhookURL == "" {
		// 没给地址就退回已保存的配置：管理员想验的是「我存进去的那个通不通」。
		saved, err := readAlertConfig(r.Context(), appStore)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to load alert config")
			return
		}
		cfg = saved
	}
	if !cfg.hasWebhook() {
		writeJSONError(w, http.StatusBadRequest, "还没有配置通知地址")
		return
	}

	text := formatAlertNotice(alertNotice{
		PrinterName: "测试打印机",
		Kind:        devicealert.KindOutOfPaper,
		Severity:    devicealert.SeverityError,
		Detail:      "这是一条测试消息，收到即表示告警通道可用",
		At:          time.Now().UTC().Format(time.RFC3339),
	})
	if err := postAlert(r.Context(), cfg, text); err != nil {
		// 把渠道原话回给前端：这一步的价值就在于「为什么没通」要当场看见。
		writeJSONError(w, http.StatusBadGateway, "发送失败："+err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
