package middleware

import (
	"crypto/subtle"
	"net/http"

	"cups-web/internal/auth"
)

// RequireSession ensures a valid session cookie exists.
func RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := auth.GetSession(r)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAdmin ensures the session belongs to an admin user.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, err := auth.GetSession(r)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if sess.Role != "admin" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ValidateCSRF checks that X-CSRF-Token matches csrf_token cookie for state-changing requests.
func ValidateCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only validate for state-changing methods
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie("csrf_token")
		if err != nil {
			http.Error(w, "missing csrf cookie", http.StatusForbidden)
			return
		}
		header := r.Header.Get("X-CSRF-Token")
		// 常数时间比较，避免通过响应时序侧信道逐字节推断 token。
		if header == "" || subtle.ConstantTimeCompare([]byte(header), []byte(cookie.Value)) != 1 {
			http.Error(w, "invalid csrf token", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
