package main

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// 登录暴力破解防护：进程内失败计数 + 临时锁定（安全审查 M-1）。
//
// 以「客户端 IP + 用户名」为键统计连续失败次数，超过阈值后在锁定窗口内直接
// 拒绝，避免在线爆破。计数仅存于内存：进程重启即清零，多实例不共享——对单
// 二进制部署足够，且不引入额外依赖。成功登录会清除对应键的计数。

const (
	maxLoginFailures = 5                // 锁定前允许的连续失败次数
	loginFailWindow  = 15 * time.Minute // 失败计数的滑动窗口
	loginLockout     = 15 * time.Minute // 触发后锁定时长
)

type loginAttempt struct {
	failures  int
	windowEnd time.Time
	lockUntil time.Time
}

var (
	loginAttemptsMu sync.Mutex
	loginAttempts   = make(map[string]*loginAttempt)
	lastSweep       time.Time
)

// clientIP 提取请求来源 IP。优先取 X-Forwarded-For 的第一跳（部署在反向代理后
// 时 RemoteAddr 会是代理地址），否则回退到 RemoteAddr。
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first, _, ok := strings.Cut(xff, ","); ok {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func loginKey(r *http.Request, username string) string {
	return clientIP(r) + "|" + strings.ToLower(strings.TrimSpace(username))
}

// loginAllowed 报告该键当前是否可尝试登录；被锁定时返回 false 与建议的重试等待。
func loginAllowed(key string) (bool, time.Duration) {
	now := time.Now()
	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()
	sweepLocked(now)

	a := loginAttempts[key]
	if a == nil {
		return true, 0
	}
	if now.Before(a.lockUntil) {
		return false, time.Until(a.lockUntil)
	}
	return true, 0
}

// registerLoginFailure 记录一次失败，达到阈值则锁定。
func registerLoginFailure(key string) {
	now := time.Now()
	loginAttemptsMu.Lock()
	defer loginAttemptsMu.Unlock()

	a := loginAttempts[key]
	if a == nil || now.After(a.windowEnd) {
		a = &loginAttempt{windowEnd: now.Add(loginFailWindow)}
		loginAttempts[key] = a
	}
	a.failures++
	if a.failures >= maxLoginFailures {
		a.lockUntil = now.Add(loginLockout)
	}
}

// clearLoginFailures 在登录成功后清除计数。
func clearLoginFailures(key string) {
	loginAttemptsMu.Lock()
	delete(loginAttempts, key)
	loginAttemptsMu.Unlock()
}

// sweepLocked 周期性清理过期条目，避免 map 无限增长。调用方须持锁。
func sweepLocked(now time.Time) {
	if now.Sub(lastSweep) < loginFailWindow {
		return
	}
	lastSweep = now
	for k, a := range loginAttempts {
		if now.After(a.windowEnd) && now.After(a.lockUntil) {
			delete(loginAttempts, k)
		}
	}
}
