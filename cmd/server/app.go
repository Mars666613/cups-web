package main

import (
	"os"
	"strconv"
	"strings"

	"cups-web/internal/store"
)

var appStore *store.Store
var uploadDir string

// 默认连接数。见 dbMaxConns 的说明。
const defaultDBMaxConns = 10

// dbMaxConns 每个实例向中心库开多少条连接。
//
// ⚠️ 这个值要跟中心库的 max_connections 一起算：所有店加起来不能超过它。
// 比如 20 家店 × 10 = 200 条，而 PostgreSQL 默认 max_connections 只有 100 ——
// 直接连不上。要么调大服务端的 max_connections，要么把这里调小。
// 单店实际并发很低（一次打印请求 + 30 秒一轮的后台巡检），调小到 3~5 完全够。
func dbMaxConns() int {
	if v := strings.TrimSpace(os.Getenv("DB_MAX_CONNS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultDBMaxConns
}

// redactDSN 抹掉连接串里的密码再打日志。
//
// 🚫 别图省事直接 log 整个 DSN —— 它会出现在容器日志、journald、
// docker logs 输出里，而运维排查时经常把这些贴进 issue。
func redactDSN(dsn string) string {
	at := strings.LastIndex(dsn, "@")
	if at < 0 {
		return dsn
	}
	scheme := strings.Index(dsn, "://")
	if scheme < 0 || scheme > at {
		return dsn
	}
	credStart := scheme + 3
	creds := dsn[credStart:at]
	if i := strings.Index(creds, ":"); i >= 0 {
		creds = creds[:i] + ":***"
	}
	return dsn[:credStart] + creds + dsn[at:]
}
