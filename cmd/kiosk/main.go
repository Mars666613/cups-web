package main

// cups-kiosk —— 打印机旁那台取件终端的程序。
//
// 它做的事只有一件：读 USB 读卡器刷出来的卡号，POST 给服务器，按结果响一声。
// 之所以不是「跑一个浏览器开取件页」：
//
//	① 现场那台机器【没有屏幕】。浏览器跑不起来，也没有可显示的东西。
//	② 一个静态二进制，树莓派上不用装任何运行时，升级就是换一个文件。
//	③ 不用浏览器也就没有「焦点被抢走、刷卡字符漏进别的输入框」这类问题。
//
// 它【不】做任何业务判断：卡是谁、有没有待取件任务、能不能打印，全在服务端。
// 这里只负责「把卡号送过去」和「把结果变成声光」。
//
// 用法：
//
//	cups-kiosk -list                列出输入设备（安装时找读卡器名字用）
//	cups-kiosk                     按 /etc/cups-kiosk.conf 运行
//	cups-kiosk -verbose            把每一个按键事件打进日志（排查卡号格式时用）
//
// 见 docs/pull-printing.md。

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultConfigPath = "/etc/cups-kiosk.conf"

// releaseTimeout 是给服务端的时限。
//
// 服务端在这一条请求里【同步】把员工所有待取件任务逐个送去 CUPS
// （见 kiosk_handlers.go 里那段说明），N 个任务时可能要几秒。
// 给得宽一点：超时重试反而会把同一批任务再打一遍的错觉带给员工。
const releaseTimeout = 60 * time.Second

// idlePoll 是没有事件时的轮询间隔。设备用 O_NONBLOCK 打开，
// 空闲时 read 立刻返回 EAGAIN，所以这个 sleep 只是别把 CPU 跑满。
const idlePoll = 20 * time.Millisecond

type config struct {
	Server string
	Token  string
	// Device 是读卡器名字里的一段（如 "RFID"、"Reader"、"Barcode"）。
	// 留空则自动挑一个（跳过树莓派自带的设备）。
	//
	// ⚠️ 不要填 /dev/input/eventN —— 重启、插拔之后编号会漂移，
	// 表现是「昨天还好好的，今天就不响了」。这是按名字匹配的原因。
	Device      string
	BuzzerPin   int
	LedGreenPin int
	LedRedPin   int
}

// loadConfig 解析 key = value 形式的配置文件。
//
// 刻意不做 TOML/YAML 依赖：这个文件只有五行，而且现场改的人是照着
// 管理界面上那段内容抄的，越简单越不容易抄错。
func loadConfig(path string) (config, error) {
	var cfg config

	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, fmt.Errorf("配置文件 %s 不存在。请到管理界面「取件终端 → 配置指引」"+
				"复制一份内容过去", path)
		}
		return cfg, fmt.Errorf("读配置文件失败: %w", err)
	}

	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return cfg, fmt.Errorf("配置文件第 %d 行不是 key = value 形式：%q", i+1, line)
		}
		key = strings.TrimSpace(strings.ToLower(key))
		value = strings.TrimSpace(value)

		switch key {
		case "server":
			cfg.Server = strings.TrimRight(value, "/")
		case "token":
			cfg.Token = value
		case "device":
			cfg.Device = value
		case "buzzer_pin":
			cfg.BuzzerPin, err = strconv.Atoi(value)
		case "led_green_pin":
			cfg.LedGreenPin, err = strconv.Atoi(value)
		case "led_red_pin":
			cfg.LedRedPin, err = strconv.Atoi(value)
		default:
			// 未知的键直接报错而不是忽略：现场手抄最容易抄错键名，
			// 静默忽略的表现是「配了但不生效」，而日志里什么线索都没有。
			return cfg, fmt.Errorf("配置文件第 %d 行有无法识别的键 %q", i+1, key)
		}
		if err != nil {
			return cfg, fmt.Errorf("配置文件第 %d 行 %s 的值不是数字：%q", i+1, key, value)
		}
	}

	if cfg.Server == "" {
		return cfg, errors.New("配置文件缺少 server（服务器地址，如 http://192.168.1.22:1180）")
	}
	if !strings.HasPrefix(cfg.Server, "http://") && !strings.HasPrefix(cfg.Server, "https://") {
		return cfg, fmt.Errorf("server 必须以 http:// 或 https:// 开头，现在是 %q", cfg.Server)
	}
	if cfg.Token == "" {
		return cfg, errors.New("配置文件缺少 token（到管理界面「取件终端」里复制）")
	}
	return cfg, nil
}

type releaseRequest struct {
	Token  string `json:"token"`
	CardNo string `json:"cardNo"`
}

type releaseResponse struct {
	Status string `json:"status"`
	User   string `json:"user,omitempty"`
	Reason string `json:"reason,omitempty"`
	Jobs   []struct {
		Filename string `json:"filename"`
		OK       bool   `json:"ok"`
		Error    string `json:"error,omitempty"`
	} `json:"jobs"`
}

func main() {
	cfgPath := flag.String("config", defaultConfigPath, "配置文件路径")
	list := flag.Bool("list", false, "列出所有输入设备后退出（安装时用来找读卡器的名字）")
	verbose := flag.Bool("verbose", false, "把每一个按键事件打进日志（排查卡号格式时用）")
	flag.Parse()

	if *list {
		devs := listInputDevices()
		if len(devs) == 0 {
			fmt.Println("没有找到任何输入设备（/dev/input/event* 为空）")
			return
		}
		fmt.Println("设备路径\t名称（device = 填名称里的一段）")
		for _, d := range devs {
			fmt.Println(d)
		}
		return
	}

	log.Printf("[kiosk] 启动，配置文件 %s", *cfgPath)

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("[kiosk] %v", err)
	}
	log.Printf("[kiosk] 服务器 %s，终端 %s", cfg.Server, shortToken(cfg.Token))

	fb := newFeedback(cfg)
	client := &http.Client{Timeout: releaseTimeout}

	// 断线重连：读卡器被拔掉/换口之后 eventN 会变，必须重新找一次。
	// 不做这件事的话，表现是「拔过一次之后就永远不响了，重启才能恢复」。
	for {
		if err := runOnce(cfg, fb, client, *verbose); err != nil {
			log.Printf("[kiosk] %v；5 秒后重新寻找读卡器", err)
			time.Sleep(5 * time.Second)
		}
	}
}

// runOnce 打开读卡器并一直读到设备消失。返回错误表示要重开设备。
func runOnce(cfg config, fb *feedback, client *http.Client, verbose bool) error {
	dev, err := openInputDevice(cfg.Device)
	if err != nil {
		return err
	}
	defer dev.Close()
	log.Printf("[kiosk] 开始读 %s（%s）", dev.path, dev.name)

	var collector swipeCollector
	for {
		code, value, ok, err := dev.readEvent()
		if err != nil {
			return err
		}
		if !ok {
			time.Sleep(idlePoll)
			continue
		}
		if verbose {
			log.Printf("[kiosk] 按键 code=%d value=%d", code, value)
		}

		card := collector.feed(code, value, time.Now())
		if card == "" {
			continue
		}
		if len(card) < minCardLen {
			// 误触（随手按几下就回车）。不出声也不上报 ——
			// 声音反而会让员工以为「刷上了但格式不对」。
			log.Printf("[kiosk] 忽略一次过短的输入（%d 位）", len(card))
			continue
		}

		status := release(cfg, client, card)
		log.Printf("[kiosk] 刷卡结果 %s（卡号 %d 位）", status, len(card))
		fb.signal(status)
	}
}

// release 把卡号送给服务器，返回结果码。
//
// 网络/服务端故障统一映射成 "unreachable"（本地码，服务端不会返回它）——
// 员工需要知道的是「这次没成、去找管理员」，而不是 HTTP 的细节。
func release(cfg config, client *http.Client, cardNo string) string {
	body, err := json.Marshal(releaseRequest{Token: cfg.Token, CardNo: cardNo})
	if err != nil {
		log.Printf("[kiosk] 组装请求失败: %v", err)
		return "unreachable"
	}

	resp, err := client.Post(cfg.Server+"/api/kiosk/release", "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("[kiosk] ⚠️ 连不上服务器 %s: %v", cfg.Server, err)
		return "unreachable"
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		log.Printf("[kiosk] 读响应失败: %v", err)
		return "unreachable"
	}
	if resp.StatusCode != http.StatusOK {
		// 服务端只在请求本身畸形时才用非 200（见 writeKioskResult）。
		// 走到这里基本意味着地址填错、打到了别的东西上。
		log.Printf("[kiosk] ⚠️ 服务端返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
		return "unreachable"
	}

	var out releaseResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		log.Printf("[kiosk] 响应不是合法 JSON: %v（原文：%.200s）", err, raw)
		return "unreachable"
	}

	// 逐条打印失败原因 —— 没有屏幕的话，这是员工跑来问「为什么没打出来」时
	// 唯一能给出答案的地方。
	for _, j := range out.Jobs {
		if !j.OK {
			log.Printf("[kiosk]   ✗ %s：%s", j.Filename, j.Error)
		}
	}
	if out.Reason != "" {
		log.Printf("[kiosk]   原因：%s", out.Reason)
	}
	if out.User != "" {
		log.Printf("[kiosk]   用户：%s", out.User)
	}
	return out.Status
}

// minCardLen 与服务端 adminBindCardHandler 的下限一致。
// 两边不一致的话，会出现「终端认为合法、服务端认为太短」的怪现象。
const minCardLen = 4

// shortToken 给日志用的 token 前缀。
//
// 🚫 不要把完整 token 写进日志：它是等价于密码的凭据。
func shortToken(token string) string {
	if len(token) <= 8 {
		return token
	}
	return token[:8] + "…"
}
