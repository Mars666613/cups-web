package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"sync"
	"time"
)

// ─── 刷卡结果的反馈：蜂鸣器 + 双色 LED ────────────────────────────────
//
// 这是取件终端【唯一】能告诉员工「成了没有、为什么没成」的通道 ——
// 它没有屏幕。所以声码不是锦上添花，是这个方案选「不带屏幕」之后
// 必须做出来的补偿。
//
//	长响 1 声 + 绿灯   任务已开始打印
//	长响 2 声 + 红灯   部分任务失败（有些打出来了、有些没有）
//	长响 3 声 + 红灯   全部失败
//	短响 2 声 + 红灯   这台打印机上没有你的待取件任务
//	短响 3 声 + 红灯   工牌未登记，请联系管理员
//	短响 4 声 + 红灯   打印机当前不可用，请稍后再试
//	短响 5 声 + 红灯   终端未注册或已停用（叫管理员）
//	短响 6 声 + 红灯   刷得太频繁
//	长响 4 声 + 红灯   连不上服务器（本地判断，服务端不会返回这一档）
//	无声               误触（空刷），不打扰
//
// ⚠️ 用「长/短」两个族把信号空间分成两半，是因为纯靠次数分辨
// 「3 声」和「4 声」对人来说很难。长响只表示「和打印有关的结果」，
// 短响只表示「这次刷卡没能进行」。读卡器旁必须贴一张声码卡 ——
// 不贴的话没人记得住，等于没有反馈。
//
// 硬件走 /sys/class/gpio（sysfs）。它在 Raspberry Pi OS 上默认可用，
// 而且只要文件读写，不需要任何第三方库。若哪天内核把它关了，
// 换 libgpiod 的 gpioset 或用 USB 小音箱跑 aplay 都能顶替 ——
// 配置里那三个引脚号是唯一需要改的地方。

const (
	beepLong  = 600 * time.Millisecond
	beepShort = 80 * time.Millisecond
	// beepGap 两声之间的静音。太短会连成一声，太长整套提示会拖到好几秒。
	beepGap = 120 * time.Millisecond
)

// beepPattern 是某个结果对应的响声（true = 长音）。
type beepPattern struct {
	beeps []bool
	green bool // true = 绿灯，false = 红灯
	// silent 表示这一档不出声也不亮灯（误触专用）。
	silent bool
}

// 与 kiosk_handlers.go 里的 kioskStatus* 常量一一对应。
// 新增一个结果码时，这里必须同时补一行 —— 少了的话员工刷卡会完全没反应。
var beepPatterns = map[string]beepPattern{
	"released":            {beeps: []bool{true}, green: true},
	"partial":             {beeps: []bool{true, true}, green: false},
	"failed":              {beeps: []bool{true, true, true}, green: false},
	"no_jobs":             {beeps: []bool{false, false}, green: false},
	"card_unbound":        {beeps: []bool{false, false, false}, green: false},
	"printer_unavailable": {beeps: []bool{false, false, false, false}, green: false},
	"unknown_terminal":    {beeps: []bool{false, false, false, false, false}, green: false},
	"terminal_disabled":   {beeps: []bool{false, false, false, false, false}, green: false},
	"rate_limited":        {beeps: []bool{false, false, false, false, false, false}, green: false},
	"bad_swipe":           {silent: true},

	// ⚠️ 这一档是【本地】的，服务端不会返回它：连不上服务器、响应读不出来、
	// 地址填错了都归到这里。员工需要知道的只是「这次没成，去找管理员」。
	// 用 4 声【长】响 —— 与上面那串短响明确分开，否则「3 短」和「4 长」
	// 在嘈杂的打印机旁边根本听不出差别。
	"unreachable": {beeps: []bool{true, true, true, true}, green: false},
}

// gpioPin 是 sysfs GPIO 上的一个输出脚。
type gpioPin struct {
	valuePath string
}

// newGPIOPin 导出并配置一个 GPIO 输出脚。num <= 0 表示「没配这个器件」。
func newGPIOPin(num int) (*gpioPin, error) {
	if num <= 0 {
		return nil, nil
	}
	// 已经导出过的话这一步会返回 EBUSY，忽略即可 —— 它不是错误。
	_ = os.WriteFile("/sys/class/gpio/export", []byte(strconv.Itoa(num)), 0o644)

	dir := fmt.Sprintf("/sys/class/gpio/gpio%d/direction", num)
	if err := os.WriteFile(dir, []byte("out"), 0o644); err != nil {
		return nil, fmt.Errorf("配置 GPIO %d 失败: %w（引脚号填错了？内核没开 sysfs GPIO？）", num, err)
	}
	return &gpioPin{valuePath: fmt.Sprintf("/sys/class/gpio/gpio%d/value", num)}, nil
}

func (p *gpioPin) set(on bool) {
	if p == nil {
		return
	}
	v := "0"
	if on {
		v = "1"
	}
	// 写失败只可能是硬件没接或权限不对；反馈坏了不该影响取件本身，
	// 所以只忽略（真要查，日志里前面的 initialize 那行已经报过错了）。
	_ = os.WriteFile(p.valuePath, []byte(v), 0o644)
}

// feedback 负责把一次刷卡的结果变成声光。
//
// 加锁串行化：两次刷卡挨得很近时（员工连刷两下），
// 两声提示重叠在一起会变成一串谁也听不懂的噪音。
type feedback struct {
	mu     sync.Mutex
	buzzer *gpioPin
	green  *gpioPin
	red    *gpioPin
}

func newFeedback(cfg config) *feedback {
	f := &feedback{}
	var err error

	if f.buzzer, err = newGPIOPin(cfg.BuzzerPin); err != nil {
		log.Printf("[kiosk] 蜂鸣器不可用，将只亮灯: %v", err)
		f.buzzer = nil
	}
	if f.green, err = newGPIOPin(cfg.LedGreenPin); err != nil {
		log.Printf("[kiosk] 绿灯不可用: %v", err)
		f.green = nil
	}
	if f.red, err = newGPIOPin(cfg.LedRedPin); err != nil {
		log.Printf("[kiosk] 红灯不可用: %v", err)
		f.red = nil
	}

	if f.buzzer == nil && f.green == nil && f.red == nil {
		// 不配器件是完全允许的：员工仍能取到纸，只是没有声光提示。
		// 但必须说清楚，否则现场会以为「程序没跑」。
		log.Printf("[kiosk] ⚠️ 没有配置任何声光器件，刷卡将没有任何反馈（结果只写进日志）")
	}
	return f
}

// signal 按结果给出声光反馈。阻塞到提示播完（最长约 2 秒）。
func (f *feedback) signal(status string) {
	pat, ok := beepPatterns[status]
	if !ok {
		// 服务端加了一个这个版本不认识的结果码。不要沉默 ——
		// 沉默等于「刷了没反应」，是最难排查的一种现象。
		log.Printf("[kiosk] ⚠️ 未知的结果码 %q，按失败提示", status)
		pat = beepPatterns["failed"]
	}
	if pat.silent {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	// 亮灯先于响声：灯一直在，声会过去，员工扫一眼就知道成没成。
	if pat.green {
		f.green.set(true)
	} else {
		f.red.set(true)
	}
	defer func() {
		f.green.set(false)
		f.red.set(false)
	}()

	for i, long := range pat.beeps {
		if i > 0 {
			time.Sleep(beepGap)
		}
		d := beepShort
		if long {
			d = beepLong
		}
		f.buzzer.set(true)
		time.Sleep(d)
		f.buzzer.set(false)
	}
}
