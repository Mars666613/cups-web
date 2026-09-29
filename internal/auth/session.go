package auth

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"cups-web/internal/store"

	"github.com/gorilla/securecookie"
)

type secureMode int

const (
	secureAuto secureMode = iota // 按每个请求的实际协议判定
	secureAlways
	secureNever
)

var (
	secureCookieOnce sync.Once
	secureCookieMode secureMode
)

func cookieSecureMode() secureMode {
	secureCookieOnce.Do(func() {
		switch strings.ToLower(strings.TrimSpace(os.Getenv("COOKIE_SECURE"))) {
		case "true", "1", "yes", "on":
			secureCookieMode = secureAlways
		case "false", "0", "no", "off":
			secureCookieMode = secureNever
		default: // 含空值：auto
			secureCookieMode = secureAuto
		}
	})
	return secureCookieMode
}

// CookieSecure 报告是否应在会话 / CSRF cookie 上设置 Secure 属性。
//
// 环境变量 COOKIE_SECURE 三态：true 恒开、false 恒关、缺省（auto）按本次请求的实际
// 协议逐请求判定——直连 HTTPS 看 r.TLS，TLS 卸载在反代上时看 X-Forwarded-Proto。
//
// 为什么默认改成 auto：旧实现是进程级常量且默认关闭，于是 HTTPS 反代部署（最常见的
// 公网形态）下 session 与 csrf cookie 都拿不到 Secure 标记，除非用户知道有这个变量
// ——而它此前没有出现在任何文档、compose 或 .env.example 里。auto 让 HTTP 内网部署
// 行为完全不变，同时把 HTTPS 部署自动收紧。若代理错误地上报了 X-Forwarded-Proto:
// https 而浏览器实际走 HTTP，浏览器会丢弃 cookie 导致登录后立刻掉线，此时用
// COOKIE_SECURE=false 显式关闭。
func CookieSecure(r *http.Request) bool {
	switch cookieSecureMode() {
	case secureAlways:
		return true
	case secureNever:
		return false
	}
	if r == nil {
		return false
	}
	if r.TLS != nil {
		return true
	}
	return forwardedHTTPS(r)
}

// forwardedHTTPS 判断反向代理是否上报了 HTTPS。X-Forwarded-Proto 在多级代理下是
// 逗号分隔的链，第一跳才是最初面向浏览器的那一层。
func forwardedHTTPS(r *http.Request) bool {
	v := r.Header.Get("X-Forwarded-Proto")
	if v == "" {
		return false
	}
	first, _, _ := strings.Cut(v, ",")
	return strings.EqualFold(strings.TrimSpace(first), "https")
}

var s *securecookie.SecureCookie

const sessionCookieName = "session"
const csrfCookieName = "csrf_token"

const (
	settingHashKey  = "session_hash_key"
	settingBlockKey = "session_block_key"
)

func SetupSecureCookie(db *sql.DB) error {
	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	hashKeyStr, err := store.GetSettingString(ctx, tx, settingHashKey, "")
	if err != nil {
		return err
	}
	blockKeyStr, err := store.GetSettingString(ctx, tx, settingBlockKey, "")
	if err != nil {
		return err
	}

	var hashKey, blockKey []byte

	if hashKeyStr == "" {
		hashKey = securecookie.GenerateRandomKey(32)
		hashKeyStr = base64.StdEncoding.EncodeToString(hashKey)
		if err := store.SetSettingString(ctx, tx, settingHashKey, hashKeyStr); err != nil {
			return err
		}
	} else {
		hashKey, _ = base64.StdEncoding.DecodeString(hashKeyStr)
		if len(hashKey) == 0 {
			hashKey = []byte(hashKeyStr)
		}
	}

	if blockKeyStr == "" {
		blockKey = securecookie.GenerateRandomKey(32)
		blockKeyStr = base64.StdEncoding.EncodeToString(blockKey)
		if err := store.SetSettingString(ctx, tx, settingBlockKey, blockKeyStr); err != nil {
			return err
		}
	} else {
		blockKey, _ = base64.StdEncoding.DecodeString(blockKeyStr)
		if len(blockKey) == 0 {
			blockKey = []byte(blockKeyStr)
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	s = securecookie.New(hashKey, blockKey)
	return nil
}

type Session struct {
	UserID   int64     `json:"userId"`
	Username string    `json:"username"`
	Role     string    `json:"role"`
	Expires  time.Time `json:"expires"`
}

func SetSession(w http.ResponseWriter, r *http.Request, sess Session) error {
	if s == nil {
		return errors.New("securecookie not initialized")
	}
	encoded, err := s.Encode(sessionCookieName, sess)
	if err != nil {
		return err
	}
	cookie := &http.Cookie{
		Name:     sessionCookieName,
		Value:    encoded,
		Path:     "/",
		HttpOnly: true,
		Secure:   CookieSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400,
	}
	http.SetCookie(w, cookie)
	return nil
}

func ClearSession(w http.ResponseWriter, r *http.Request) {
	cookie := &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   CookieSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	}
	http.SetCookie(w, cookie)
	// 清除 csrf cookie。属性必须与签发时对称（见 NewCSRFCookie）：浏览器按
	// name+path+domain 匹配删除，属性不一致虽然多数情况仍能删掉，但保持对称更稳。
	csrf := NewCSRFCookie(r, "")
	csrf.MaxAge = -1
	http.SetCookie(w, csrf)
}

// NewCSRFCookie 构造 csrf_token cookie。HttpOnly 必须为 false——前端 utils/api.js
// 的 getCSRF() 要用 JS 读它再放进 X-CSRF-Token 头，完成 double-submit 校验。
//
// 抽成公共函数是为了让签发（登录、/api/csrf）与清除（登出）共用同一套属性，避免
// 属性漂移。
func NewCSRFCookie(r *http.Request, token string) *http.Cookie {
	return &http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: false,
		Secure:   CookieSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400,
	}
}

func GetSession(r *http.Request) (Session, error) {
	var sess Session
	if s == nil {
		return sess, errors.New("securecookie not initialized")
	}
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return sess, err
	}
	err = s.Decode(sessionCookieName, c.Value, &sess)
	if err != nil {
		return sess, err
	}
	return sess, nil
}
