package main

import "net/http"

// Version 是构建时由 -ldflags 注入的版本号。
// 来源优先级：
//  1. 构建命令 `go build -ldflags "-X main.Version=<ver>"` 注入（CI / Makefile / Dockerfile 都会注入）
//  2. 未注入时保持默认值 "dev"，用于本地 `go run` 直接启动的开发场景
//
// 值的取值约定：
//   - tag 构建：`vX.Y.Z`（来自 `git describe --tags --exact-match`）
//   - 分支构建：`vX.Y.Z-<n>-g<sha>[-dirty]`（来自 `git describe --tags --always --dirty`）
//   - 无 tag 仓库：短 commit sha 或 "dev"
var Version = "dev"

// VersionHandler 返回当前二进制的版本号。
// 该接口公开（无需登录），前端会在登录页与主界面 footer 展示，便于用户确认自己实际跑的是哪个版本
// （场景：用户用命令行直接拉二进制覆盖升级后，想一眼看到当前页面背后的二进制版本号）。
func VersionHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"version": Version})
}
