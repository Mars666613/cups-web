package main

// Linux input 子系统的 keycode（`/usr/include/linux/input-event-codes.h`）
// 到字符的映射。
//
// ⚠️ 为什么不能像网页那样直接拿到字符：
// 网页里浏览器已经帮我们把 scancode 按【键盘布局】翻译成了 `event.key`；
// 而这里读的是 evdev 最底层的原始 keycode —— 它只说明「按下了哪个物理键」，
// 不带布局信息。所以这张表实质上【写死了 US 布局】。
//
// 🚫 这就是「树莓派的键盘布局必须设成 US，且要和绑定工牌时那台电脑一致」
// 这条部署要求的由来。布局不一致时，同一张卡在两处会被翻译成不同的字符串，
// 而现象是「员工刷卡没反应」—— 根因在键盘布局，不在卡、不在读卡器。
//
// 表里只收 ASCII 可见字符。读卡器吐的就是卡号，用不到中文/功能键。

// keyCodeToChar 是 US 布局下的 keycode → 字符（未按 Shift 时）。
var keyCodeToChar = map[uint16]rune{
	// 数字行
	2: '1', 3: '2', 4: '3', 5: '4', 6: '5',
	7: '6', 8: '7', 9: '8', 10: '9', 11: '0',
	// 字母
	16: 'q', 17: 'w', 18: 'e', 19: 'r', 20: 't', 21: 'y', 22: 'u', 23: 'i', 24: 'o', 25: 'p',
	30: 'a', 31: 's', 32: 'd', 33: 'f', 34: 'g', 35: 'h', 36: 'j', 37: 'k', 38: 'l',
	44: 'z', 45: 'x', 46: 'c', 47: 'v', 48: 'b', 49: 'n', 50: 'm',
	// 符号（卡号里偶尔会出现）
	12: '-', 13: '=', 26: '[', 27: ']', 39: ';', 40: '\'', 41: '`',
	43: '\\', 51: ',', 52: '.', 53: '/', 57: ' ',
}

// keyCodeToCharShift 是按住 Shift 时的版本。
//
// ⚠️ 这张表不是可有可无的：HID 键盘要输出大写字母或 `:`/`+` 这类符号，
// 【必须】用 Shift + 对应键的组合发送。读卡器吐十六进制卡号（常见形如
// `0A1B2C3D`）时全靠它 —— 少了它，读到的会是 `0a1b2c3d`，
// 与管理员绑定时那台电脑读到的值对不上。
var keyCodeToCharShift = map[uint16]rune{
	2: '!', 3: '@', 4: '#', 5: '$', 6: '%',
	7: '^', 8: '&', 9: '*', 10: '(', 11: ')',
	12: '_', 13: '+', 26: '{', 27: '}', 39: ':', 40: '"', 41: '~',
	43: '|', 51: '<', 52: '>', 53: '?',
}

// 特殊 keycode
const (
	keyEnter      = 28
	keyKPEnter    = 96
	keyTab        = 15
	keyBackspace  = 14
	keyLeftShift  = 42
	keyRightShift = 54
)

// 事件类型（只关心 EV_KEY）
const (
	evKey = 0x01
)

// 按键状态
const (
	keyRelease = 0
	keyPress   = 1
	keyRepeat  = 2
)

// charFor 把一次按键翻译成字符。第二个返回值表示这个 keycode 是不是
// 一个「要计入卡号」的字符（false 时调用方应当忽略它）。
func charFor(code uint16, shift bool) (rune, bool) {
	if shift {
		if ch, ok := keyCodeToCharShift[code]; ok {
			return ch, true
		}
	}
	if ch, ok := keyCodeToChar[code]; ok {
		if shift && ch >= 'a' && ch <= 'z' {
			return ch - 32, true
		}
		return ch, true
	}
	return 0, false
}

// isTerminator 判断这个 keycode 是不是「一次刷卡结束」。
// 多数读卡器末尾发回车；少数发 Tab，两个都收。
func isTerminator(code uint16) bool {
	return code == keyEnter || code == keyKPEnter || code == keyTab
}
