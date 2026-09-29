package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	"cups-web/internal/auth"
	"cups-web/internal/ipp"
)

// cupsJobsHandler 处理 GET /api/cups-jobs
// 返回 CUPS 上所有未完成的打印任务(issue #60)。前端每 5s 轮询一次,
// 展示卡在 processing/stopped 的任务并允许取消 —— 这是排查
// "任务卡在打印中" 的最直接手段。
func cupsJobsHandler(w http.ResponseWriter, r *http.Request) {
	cupsHost := os.Getenv("CUPS_HOST")
	if cupsHost == "" {
		cupsHost = "localhost"
	}
	jobs, err := ipp.ListActiveJobs(cupsHost)
	if err != nil {
		log.Printf("[cups-jobs] list failed: %v", err)
		// 与 /api/printer-info 一致:错误细节留在服务端日志,对外给泛化提示。
		writeJSONError(w, http.StatusBadGateway, "failed to list jobs")
		return
	}
	writeJSON(w, jobs)
}

// cupsCancelJobHandler 处理 POST /api/cups-jobs/cancel
// 请求体:{"printerUri": "ipp://.../printers/X", "jobId": 123}
//
// 允许普通登录用户调用 —— issue #60 的诉求就是让用户能自助取消卡住的任务。
// CUPS 侧会按 requesting-user-name 做 owner 校验:发起者的任务能取消,
// 别人的任务会被 CUPS 拒(client-error-forbidden),错误会透传到前端。
func cupsCancelJobHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PrinterURI string `json:"printerUri"`
		JobID      int    `json:"jobId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.PrinterURI == "" || body.JobID <= 0 {
		writeJSONError(w, http.StatusBadRequest, "missing printerUri or jobId")
		return
	}
	sess, _ := auth.GetSession(r)
	username := sess.Username
	if err := ipp.CancelJob(body.PrinterURI, body.JobID, username); err != nil {
		log.Printf("[cups-jobs] cancel failed printer=%q job=%d user=%q: %v",
			body.PrinterURI, body.JobID, username, err)
		writeJSONError(w, http.StatusBadGateway, "failed to cancel job: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "jobId": body.JobID})
}
