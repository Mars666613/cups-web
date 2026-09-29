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
		writeJSONError(w, http.StatusInternalServerError, "failed to save file")
		return
	}
	defer cleanup()

	countCtx, cancel := convertTimeoutContext(r.Context())
	defer cancel()
	pages, estimated, err := countPages(countCtx, tmpPath, fh.Filename)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "failed to read pages")
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
