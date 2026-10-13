package main

import (
	"net/http"
	"path/filepath"
)

type estimateResp struct {
	Pages     int  `json:"pages"`
	Estimated bool `json:"estimated"`
}

func estimateHandler(w http.ResponseWriter, r *http.Request) {
	applyUploadLimit(w, r)
	if err := r.ParseMultipartForm(512 << 20); err != nil {
		if isMaxBytesError(err) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "文件超出管理员设置的大小上限")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "invalid multipart form")
		return
	}
	file, fh, err := r.FormFile("file")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "missing file field")
		return
	}
	defer file.Close()

	tmpPath, cleanup, err := saveTempUpload(file, filepath.Base(fh.Filename))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, saveErrMsg(err))
		return
	}
	defer cleanup()

	countCtx, cancel := convertTimeoutContext(r.Context())
	defer cancel()
	pages, estimated, err := countPages(countCtx, tmpPath, fh.Filename)
	if err != nil {
		// ⚠️ 必须区分「服务器缺工具」和「文件读不出来」。
		//
		// 数 docx / OFD 的页数要先调外部工具（LibreOffice / java）。没装的时候
		// 原始 error 只有一句 "libreoffice not installed"，若照旧报
		// 「failed to read pages」，用户会一直怀疑是自己文件的问题 —— 实测就
		// 是这样绕了一圈。文案复用 /api/convert 的 convertErrMsg，两处不能各写一句。
		writeJSONError(w, http.StatusBadRequest, convertErrMsg(err))
		return
	}
	if pages < 1 {
		pages = 1
	}

	resp := estimateResp{
		Pages:     pages,
		Estimated: estimated,
	}
	writeJSON(w, resp)
}
