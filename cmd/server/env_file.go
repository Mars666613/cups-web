package main

// .env 加载器。
//
// ── 为什么需要它 ──
//
// `.env.example` 里写着「真密钥写进 .env（已 gitignore）」。这句话在 Docker
// 部署下成立 —— docker compose 会自动读同目录的 .env 做 ${VAR} 替换。
// 但【本地直接跑】时没有任何人读它：程序只认进程环境变量。
//
// 于是同一个 .env 在两种运行方式下行为不一致，实测踩过：生产密钥明明写进
// .env 了，F5 调试跑起来用的却是别处的旧配置，请求打到沙箱网关 —— 而界面上
// 一切正常，只有真正付款时才失败。排查时最耗人的就是这种"配置看着都对"。
//
// 这里补上这一环：启动时读一次 .env，让它在【所有】运行方式下都生效。
//
// ⚠️ 优先级：**真实环境变量 > .env**。
// 这样 docker compose 的 environment、shell 里的 export 仍然能覆盖文件里的值，
// 符合 dotenv 的通行约定，也保证「临时覆盖某一个值」这种用法不会失效。

import (
	"bufio"
	"os"
	"strings"
)

// loadDotEnv 读取 path 指向的 .env 并注入进程环境变量。
//
// 文件不存在、读不动、某一行格式不对 —— 统统跳过，不报错：.env 是可选配置，
// 缺了它程序照样要能起来（生产上这些值本来就是由环境变量直接给的）。
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// 兼容 shell 风格的 `export KEY=value`（有人习惯这么写）。
		line = strings.TrimPrefix(line, "export ")

		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		// 只切第一个 '='：base64 私钥结尾常有 '='，多切会把值切坏。
		// （strings.Cut 就是按第一个分隔符切的。）
		val = strings.TrimSpace(val)

		// 去掉一层【成对】的引号：`KEY="v"` 与 `KEY=v` 等价。
		// 只处理两端配对的情况，免得把值里本来就有的引号吃掉。
		if len(val) >= 2 {
			first, last := val[0], val[len(val)-1]
			if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
				val = val[1 : len(val)-1]
			}
		}

		// 已经有了就不覆盖 —— 见文件头的优先级说明。
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		_ = os.Setenv(key, val)
	}
}
