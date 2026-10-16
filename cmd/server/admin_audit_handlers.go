package main

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"

	"cups-web/internal/store"
)

// auditPageSize 是审计列表每页条数。
const auditPageSize = 100

// adminListAuditHandler 处理 GET /api/admin/audit?action=&page=
//
// 只读。审计记录的写入没有 HTTP 入口，也不该有 —— 它只能由业务改动
// 顺带产生（见 cmd/server/audit.go）。
type auditListResponse struct {
	Entries  []auditEntryResponse `json:"entries"`
	Total    int                  `json:"total"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"pageSize"`
	// Actions 是【库里出现过】的动作键及其中文标签，供前端渲染筛选下拉。
	// 从数据里取而不是写死一份清单：动作键随功能增加，写死的下拉会漏掉
	// 新的那一类，而管理员恰恰想「看看最近都动了些什么」。
	Actions map[string]string `json:"actions"`
}

func adminListAuditHandler(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimSpace(r.URL.Query().Get("action"))
	page := 1
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 0 {
		page = p
	}

	var (
		rows    []store.AuditEntry
		total   int
		actions []string
	)
	err := appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
		var err error
		if rows, err = store.ListAudit(r.Context(), tx, action, auditPageSize, (page-1)*auditPageSize); err != nil {
			return err
		}
		if total, err = store.CountAudit(r.Context(), tx, action); err != nil {
			return err
		}
		actions, err = store.ListAuditActions(r.Context(), tx)
		return err
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load audit log")
		return
	}

	labels := make(map[string]string, len(actions))
	for _, a := range actions {
		labels[a] = auditActionLabel(a)
	}

	writeJSON(w, auditListResponse{
		Entries:  toAuditResponse(rows),
		Total:    total,
		Page:     page,
		PageSize: auditPageSize,
		Actions:  labels,
	})
}
