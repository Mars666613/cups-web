package server

import (
	"bytes"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// NewEmbeddedServer returns an http.Handler that serves embedded frontend files
// and falls back to index.html for SPA routes.
//
// 参数声明为 fs.FS 而不是 embed.FS：embed.FS 满足 fs.FS，调用方传
// frontend.FS 不受影响，而测试可以用 fstest.MapFS 造出一份假产物
// （embed.FS 有未导出字段，测试里根本构造不出来）。
func NewEmbeddedServer(efs fs.FS) http.Handler {
	sub, err := fs.Sub(efs, "dist")
	if err != nil {
		// fallback to root
		sub, _ = fs.Sub(efs, "")
	}
	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ⚠️ /api 下未匹配的路径【绝不能】回落到 SPA。
		//
		// 回落到 index.html 会返回 200 + HTML，前端据此认为请求成功，
		// 然后 resp.json() 解析 HTML 抛出 "Unexpected token '<'" ——
		// 这个错误与真实原因毫无关系，排查方向会被带偏很远。
		// 实际最常见的原因是「后端二进制太旧、这个接口还不存在」，
		// 换成一次部署事故就要查上半天。
		//
		// 实测踩过：新版前端调 /api/admin/prices，旧后端没这个路由，
		// 于是保存操作被吞掉、界面显示成功、刷新后数据消失。
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no such api endpoint"}`))
			return
		}

		// normalize path and remove leading '/'
		p := path.Clean(r.URL.Path)
		if p == "/" {
			p = "index.html"
		} else {
			p = p[1:]
		}
		// attempt to stat the file in the embedded fs
		if _, err := fs.Stat(sub, p); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}
		// fallback to index.html
		f, err := sub.Open("index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()

		// Read file bytes and serve using a ReadSeeker (bytes.Reader)
		data, err := io.ReadAll(f)
		if err != nil {
			http.Error(w, "failed to read embedded index.html", http.StatusInternalServerError)
			return
		}

		modTime := time.Now()
		if fi, err := f.Stat(); err == nil {
			modTime = fi.ModTime()
		}

		http.ServeContent(w, r, "index.html", modTime, bytes.NewReader(data))
	})
}
