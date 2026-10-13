//go:build !linux

package main

import "errors"

// 非 Linux 平台的桩。
//
// 这个程序【只给树莓派用】—— 它读的是 Linux 的 evdev 设备节点。做这层桩
// 纯粹是为了让 `go build ./...` / `go vet ./...` 在开发机（Windows/macOS）
// 上仍然是绿的，别让一个只为部署目标存在的包把整个仓库的构建搞红。
//
// 真在非 Linux 上跑起来，会在启动时报一句明确的错，而不是静默什么都不做。

var errNotLinux = errors.New("cups-kiosk 只能在 Linux 上运行（它读的是 /dev/input/event*）")

func openInputDevice(string) (*inputDevice, error) {
	return nil, errNotLinux
}

func (d *inputDevice) readEvent() (uint16, int32, bool, error) {
	return 0, 0, false, errNotLinux
}

func (d *inputDevice) Close() error { return nil }
