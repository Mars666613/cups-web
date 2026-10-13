package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ─── 读 USB HID 键盘模拟读卡器 ────────────────────────────────────────
//
// 廉价 NFC/ID 读卡器插上就是【一把键盘】：刷卡 = 极快地「敲」出一串字符 +
// 回车。所以这里不需要任何驱动、任何厂商 SDK —— 读 evdev 的按键流就够了。
//
// 读的设备是 /dev/input/eventN。选哪一个【不能写死编号】：
// 重启、插拔之后 eventN 会漂移，写死会导致「昨天还好好的，今天就不响了」。
// 所以按 /sys/class/input/eventN/device/name 里的名字匹配。
//
// 本文件是【平台无关】的部分（选设备、解析事件、攒卡号）。
// 真正打开设备读字节的那两个函数在 evdev_linux.go 里，其余平台有一份
// 返回明确错误的桩（evdev_other.go）—— 这样 `go build ./...` 在开发机
// （Windows/macOS）上照样是绿的，而这个二进制本来就只给树莓派用。

// errDeviceGone 设备被拔掉了。调用方据此重新找设备，而不是空转。
var errDeviceGone = errors.New("输入设备已断开")

// inputEventSize 是内核 struct input_event 的字节数。
//
// ⚠️ 它【随架构而变】，而且是个纯粹的坑：
//
//	64 位：timeval(16) + type(2) + code(2) + value(4) = 24
//	32 位：timeval(8)  + type(2) + code(2) + value(4) = 16
//
// 所以下面按【字节偏移】解析，🚫 不要用 unsafe 把一个 Go 结构体盖上去 ——
// Go 在 32 位平台上对齐规则与内核不同，算出来的尺寸对不上，读出来全是垃圾。
// 而这个错误只在 32 位树莓派系统上复现，开发机（64 位）永远测不出来。
func inputEventSize() int {
	if strconv.IntSize == 64 {
		return 24
	}
	return 16
}

// parseInputEvent 按字节偏移解析一个 input_event。
//
// 只取 type / code / value —— 时间戳用不上（而且它在两种架构下宽度不同，
// 正是上面那个坑的来源）。x86 与 arm 都是小端。
func parseInputEvent(b []byte, size int) (typ, code uint16, value int32) {
	if size == 24 {
		return binary.LittleEndian.Uint16(b[16:18]),
			binary.LittleEndian.Uint16(b[18:20]),
			int32(binary.LittleEndian.Uint32(b[20:24]))
	}
	return binary.LittleEndian.Uint16(b[8:10]),
		binary.LittleEndian.Uint16(b[10:12]),
		int32(binary.LittleEndian.Uint32(b[12:16]))
}

// keyEvent 是从 evdev 流里取出的一个按键事件。
type keyEvent struct {
	code  uint16
	value int32
}

// inputDevice 是一个打开的 evdev 设备。
type inputDevice struct {
	path string
	name string
	size int

	// f / buf / pending 由平台相关的实现使用（见 evdev_linux.go）。
	f   *os.File
	buf []byte
	// pending 是【已经读出来、还没交给调用方】的按键。
	//
	// ⚠️ 这个队列不是优化，是正确性所需：读卡器刷卡时是【一口气】把整串
	// 按键灌进内核缓冲的，一次 read 往往就拿到十几个事件。少了它，
	// 每次 read 只返回第一个 EV_KEY、其余全被丢掉 ——
	// 表现是「每刷一次只能读到一个字符」，而看起来一切正常。
	pending []keyEvent
}

// listInputDevices 列出所有输入设备，供 `-list` 打印给现场安装的人看。
//
// 无屏幕方案下这条命令很重要：安装的人要知道读卡器到底叫什么名字，
// 才能把它填进配置里的 device = 那一行。
func listInputDevices() []string {
	paths, _ := filepath.Glob("/dev/input/event*")
	sort.Strings(paths)
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, fmt.Sprintf("%s\t%s", p, deviceName(p)))
	}
	return out
}

// deviceName 读设备的可读名字。拿不到返回空串（设备可能刚被拔掉）。
func deviceName(path string) string {
	base := filepath.Base(path)
	b, err := os.ReadFile("/sys/class/input/" + base + "/device/name")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// pickDevice 在候选里挑一个要读的设备。
//
// pattern 非空时按名字子串匹配（安装的人照着 -list 的结果填）；
// 为空时自动挑：跳过树莓派自带的那些（电源键、HDMI/VC4、触摸屏），
// 在剩下的里取第一个。
//
// **挑中的设备名要打进日志** —— 没有屏幕的时候，那行日志是
// 「它到底在读哪个设备」的唯一答案。
func pickDevice(paths []string, pattern string) (string, error) {
	if len(paths) == 0 {
		return "", errors.New("没有找到任何输入设备（/dev/input/event* 为空）；读卡器插上了吗？用 -list 看看")
	}

	var seen []string
	for _, p := range paths {
		name := deviceName(p)
		seen = append(seen, fmt.Sprintf("%s(%s)", p, name))
		if pattern != "" {
			if strings.Contains(strings.ToLower(name), strings.ToLower(pattern)) {
				return p, nil
			}
			continue
		}
		if isBuiltinDevice(name) {
			continue
		}
		return p, nil
	}
	if pattern != "" {
		return "", fmt.Errorf("没找到名字含 %q 的输入设备。当前设备：%s", pattern, strings.Join(seen, " "))
	}
	return "", fmt.Errorf("所有输入设备看起来都是机器自带的，没有可用的读卡器。当前设备：%s（用 device = 显式指定）",
		strings.Join(seen, " "))
}

// isBuiltinDevice 判断是不是树莓派自带的、不可能是读卡器的设备。
//
// 不加这个过滤的话，自动选择会挑到 "Power Button" 之类 ——
// 表现是「程序在跑、设备也开着，但刷卡永远没反应」，日志里还看不出异常。
func isBuiltinDevice(name string) bool {
	lower := strings.ToLower(name)
	for _, kw := range []string{
		"power button", "sleep button", "video bus", "vc4", "hdmi",
		"pwr_button", "gpio_keys", "touchscreen", "mouse", "touchpad",
		"keyboard", // ⚠️ 通用的 "Keyboard" 多半是插着的真键盘，不是读卡器
	} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// ─── 刷卡组装 ─────────────────────────────────────────────────────────

const (
	// keyGapTimeout 两次按键之间超过它就算新的一次输入。
	// 读卡器整串输出通常在几十毫秒内完成，500ms 是很宽松的上限。
	keyGapTimeout = 500 * time.Millisecond
	// maxCardLen 卡号上限，防止异常输入把内存撑爆。
	maxCardLen = 64
)

// swipeCollector 把散落的按键事件攒成一张卡号。
type swipeCollector struct {
	shift   bool
	buf     strings.Builder
	lastKey time.Time
}

// reset 清空未完成的缓冲。
// 两次刷卡之间隔得久了会调它 —— 否则上一次的半截输入会和这一次拼在一起。
func (c *swipeCollector) reset() {
	c.buf.Reset()
	c.lastKey = time.Time{}
}

// feed 喂一个按键事件。攒够一张卡（末尾收到回车）时返回卡号，
// 否则返回空串。
func (c *swipeCollector) feed(code uint16, value int32, now time.Time) string {
	if value == keyRelease {
		// 抬起事件只用来更新 Shift 状态，其余忽略。
		if code == keyLeftShift || code == keyRightShift {
			c.shift = false
		}
		return ""
	}

	switch code {
	case keyLeftShift, keyRightShift:
		c.shift = true
		return ""
	case keyBackspace:
		s := c.buf.String()
		if len(s) > 0 {
			c.buf.Reset()
			c.buf.WriteString(s[:len(s)-1])
		}
		return ""
	}

	if isTerminator(code) {
		card := strings.TrimSpace(c.buf.String())
		c.reset()
		return card
	}

	// ⚠️ 间隔太久说明这是两次独立输入的拼接（比如误按了几个键、过一会儿才刷卡）。
	// 没有这道判断，缓冲区里会攒出「误按的字符 + 真卡号」这种谁也看不懂的串。
	if !c.lastKey.IsZero() && now.Sub(c.lastKey) > keyGapTimeout {
		c.reset()
	}
	c.lastKey = now

	if ch, ok := charFor(code, c.shift); ok {
		if c.buf.Len() < maxCardLen {
			c.buf.WriteRune(ch)
		}
	}
	return ""
}
