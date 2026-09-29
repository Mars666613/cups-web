package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"cups-web/internal/store"
)

// getPrintLimits 读取管理员配置的页数/大小上限，0 表示不限。
// 读失败时降级为不限，避免打印功能被 DB 抖动全站阻断。
func getPrintLimits(ctx context.Context) (maxPages, maxBytes int64) {
	_ = appStore.WithTx(ctx, true, func(tx *sql.Tx) error {
		if v, err := store.GetSettingInt(ctx, tx, store.SettingMaxPagesPerJob, 0); err == nil {
			maxPages = v
		}
		if v, err := store.GetSettingInt(ctx, tx, store.SettingMaxUploadBytes, 0); err == nil {
			maxBytes = v
		}
		return nil
	})
	return
}

// applyUploadLimit 若配置了上传大小上限，用 MaxBytesReader 包裹 r.Body。
// 超过阈值时 ParseMultipartForm 会返回错误，可通过 isMaxBytesError 识别以回 413。
func applyUploadLimit(w http.ResponseWriter, r *http.Request) {
	_, maxBytes := getPrintLimits(r.Context())
	if maxBytes > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	}
}

// isMaxBytesError 判断 err 是否来自 MaxBytesReader 触顶。
// Go 1.19+ 有具体类型 *http.MaxBytesError；旧路径也可能只带 "http: request body too large" 字样，
// 兜底一起兜住。
func isMaxBytesError(err error) bool {
	if err == nil {
		return false
	}
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return true
	}
	return strings.Contains(err.Error(), "request body too large")
}
