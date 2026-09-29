package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"dist/index.html":    &fstest.MapFile{Data: []byte("<html>spa-shell</html>")},
		"dist/assets/app.js": &fstest.MapFile{Data: []byte("console.log(1)")},
	}
}

func do(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// TestAPIPathNeverFallsBackToSPA 是这套测试里最重要的一条。
//
// SPA 兜底会把【任何】未匹配路径变成 200 + index.html。对 /api 来说
// 这是灾难性的：前端看到 200 以为成功，接着 resp.json() 去解析 HTML
// 抛出 "Unexpected token '<'"，排查方向被带偏很远。
//
// 实测就是被这条坑过：新版前端调 /api/admin/prices，而后端是旧二进制
// （没有这个路由），于是「保存成功」的假象出现、数据从未落库、
// 用户刷新后发现配置消失。
func TestAPIPathNeverFallsBackToSPA(t *testing.T) {
	h := NewEmbeddedServer(testFS())

	for _, p := range []string{"/api", "/api/", "/api/nope", "/api/admin/prices"} {
		rec := do(h, p)

		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: 状态码 = %d，期望 404", p, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Errorf("%s: Content-Type = %q，期望 application/json", p, ct)
		}
		if body := rec.Body.String(); strings.Contains(body, "<html") {
			t.Errorf("%s: 返回了 HTML（%q），说明又回落到 SPA 了", p, body)
		}
	}
}

// TestSPAFallbackStillWorks 确认拦住 /api 之后，前端的 hash 路由与
// 深链接仍然拿得到 index.html。
func TestSPAFallbackStillWorks(t *testing.T) {
	h := NewEmbeddedServer(testFS())

	for _, p := range []string{"/", "/print", "/wallet", "/some/deep/route"} {
		rec := do(h, p)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: 状态码 = %d，期望 200", p, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), "spa-shell") {
			t.Errorf("%s: 没有返回 index.html", p)
		}
	}
}

// TestStaticAssetServed 有真实文件时应当直接服务它，不走回落。
func TestStaticAssetServed(t *testing.T) {
	h := NewEmbeddedServer(testFS())

	rec := do(h, "/assets/app.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "console.log") {
		t.Errorf("静态资源内容不对: %q", rec.Body.String())
	}
}
