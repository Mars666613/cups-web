//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"golang.org/x/sys/unix"
)

// 本文件是 evdev 读取里【只能在 Linux 上编译】的那一半。
// 其余平台有一份返回明确错误的桩（evdev_other.go），
// 这样 `go build ./...` 在开发机上照样是绿的。

// openInputDevice 打开读卡器。pattern 为空时自动挑一个（规则见 pickDevice）。
func openInputDevice(pattern string) (*inputDevice, error) {
	paths, _ := filepath.Glob("/dev/input/event*")
	sort.Strings(paths)
	chosen, err := pickDevice(paths, pattern)
	if err != nil {
		return nil, err
	}

	// O_NONBLOCK：非阻塞读。这样主循环不必卡在 Read 上等下一次刷卡，
	// 还能处理应答超时、断线重连这类事。
	fd, err := unix.Open(chosen, unix.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		if err == unix.EACCES {
			// 权限是现场最常见的一个坎。把修法直接写进错误里 ——
			// 这台机器没有屏幕，用户翻不了文档，只能看日志。
			return nil, fmt.Errorf("没有权限读 %s。把运行用户加进 input 组："+
				"usermod -aG input cups-kiosk（root 运行则不需要）", chosen)
		}
		return nil, fmt.Errorf("打开 %s 失败: %w", chosen, err)
	}

	size := inputEventSize()
	return &inputDevice{
		path: chosen,
		name: deviceName(chosen),
		size: size,
		f:    os.NewFile(uintptr(fd), chosen),
		buf:  make([]byte, size*64),
	}, nil
}

func (d *inputDevice) Close() error {
	if d.f == nil {
		return nil
	}
	return d.f.Close()
}

// readEvent 取一个按键事件。暂时没有数据时返回 ok=false（不阻塞）。
func (d *inputDevice) readEvent() (code uint16, value int32, ok bool, err error) {
	// 先把上一次 read 攒下的发完。刷卡是一口气灌进来的，
	// 整串按键都在这里排队 —— 逐条返回，一条都不能丢。
	if len(d.pending) > 0 {
		return d.pop()
	}

	n, rerr := d.f.Read(d.buf)
	if rerr != nil {
		var pe *os.PathError
		if errors.As(rerr, &pe) {
			// ⚠️ 用 errors.Is 而不是 switch pe.Err：Linux 上 EAGAIN 与
			// EWOULDBLOCK 是【同一个值】，写成两个 case 直接编译不过。
			switch {
			case errors.Is(pe.Err, unix.EAGAIN):
				return 0, 0, false, nil // 暂时没数据，正常
			case errors.Is(pe.Err, unix.ENODEV), errors.Is(pe.Err, unix.EIO):
				return 0, 0, false, errDeviceGone // 读卡器被拔了
			}
		}
		return 0, 0, false, rerr
	}
	if n < d.size {
		return 0, 0, false, nil
	}

	d.pending = d.pending[:0]
	for off := 0; off+d.size <= n; off += d.size {
		typ, c, v := parseInputEvent(d.buf[off:off+d.size], d.size)
		// 只关心按键事件；EV_SYN / EV_MSC 一律丢掉。
		if typ == evKey {
			d.pending = append(d.pending, keyEvent{code: c, value: v})
		}
	}
	if len(d.pending) == 0 {
		return 0, 0, false, nil
	}
	return d.pop()
}

func (d *inputDevice) pop() (uint16, int32, bool, error) {
	e := d.pending[0]
	d.pending = d.pending[1:]
	return e.code, e.value, true, nil
}
