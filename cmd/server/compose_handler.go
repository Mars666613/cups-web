package main

import (
	"mime/multipart"
	"net/http"
)

func composeHandler(w http.ResponseWriter, r *http.Request) {
	applyUploadLimit(w, r)
	if err := r.ParseMultipartForm(512 << 20); err != nil {
		if isMaxBytesError(err) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "文件超出管理员设置的大小上限")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "invalid multipart form")
		return
	}

	mode := r.FormValue("mode")
	if mode == "" {
		writeJSONError(w, http.StatusBadRequest, "missing mode field")
		return
	}

	var headers []*multipart.FileHeader
	if r.MultipartForm != nil {
		if fhs, ok := r.MultipartForm.File["files"]; ok {
			headers = fhs
		}
	}
	if len(headers) == 0 {
		writeJSONError(w, http.StatusBadRequest, "no files provided")
		return
	}

	ctx, cancel := convertTimeoutContext(r.Context())
	defer cancel()

	var outPath string
	var cleanup func()
	var err error

	switch mode {
	case "invoice":
		outPath, cleanup, err = composeInvoice2Up(ctx, headers)
	case "id_card":
		paper := r.FormValue("paper")
		if paper == "" {
			paper = "A4"
		}
		outPath, cleanup, err = composeIdCard(ctx, headers, paper)
	default:
		writeJSONError(w, http.StatusBadRequest, "unsupported mode: "+mode)
		return
	}

	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "compose failed: "+err.Error())
		return
	}
	defer cleanup()

	streamPDF(w, outPath, "composed.pdf")
}
