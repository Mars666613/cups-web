#!/usr/bin/env bash
# 在打印机旁那台机器上安装刷卡取件终端。
#
# 用法（在那台机器上，用 sudo）：
#   ./install.sh /path/to/kiosk.py                   # PC/SC 读卡器（推荐）
#   ./install.sh /path/to/cups-kiosk-linux-arm64     # 键盘模拟读卡器
#
# 它做三件事：放程序 → 放 systemd 单元 → 提示填配置并启动。
# 它【不会】替你写配置文件：token 是每台终端独有的凭据，
# 得从管理界面「取件终端 → 配置指引」里复制过来。
#
# 两种程序它都能装：看传进来的是不是一个以 python 开头的脚本。
# 两份配置文件的格式是一样的，systemd 单元也是同一个 —— 差别只在
# 「读卡器怎么接、怎么验证」那几句提示。
#
# ⚠️ 正式部署请照着 docs/刷卡取件终端部署手册.md 一步步来，
#    这个脚本只是把其中「放程序 + 装服务」那两步打成一个包。
set -euo pipefail

SRC="${1:-}"
if [ -z "$SRC" ] || [ ! -f "$SRC" ]; then
  echo "用法: $0 <kiosk.py | cups-kiosk-linux-<arch>>" >&2
  echo "" >&2
  echo "  PC/SC 读卡器（ACR122U 等）：传 kiosk.py" >&2
  echo "  键盘模拟读卡器：从发布页下载对应架构的二进制 ——" >&2
  echo "    树莓派 64 位用 linux-arm64，32 位用 linux-armv7，用 'uname -m' 确认" >&2
  exit 1
fi

if [ "$(id -u)" -ne 0 ]; then
  echo "请用 sudo 运行：sudo $0 $SRC" >&2
  exit 1
fi

# 判据是「第一行 shebang 里有没有 python」—— 比看扩展名可靠（.py 也可能没有）。
if head -1 "$SRC" | grep -qi python; then
  KIND=python
else
  KIND=binary
fi

echo "==> 安装到 /usr/local/bin/cups-kiosk（类型：$KIND）"
install -m 0755 "$SRC" /usr/local/bin/cups-kiosk

echo "==> 安装 systemd 单元"
install -m 0644 "$(dirname "$0")/cups-kiosk.service" /etc/systemd/system/cups-kiosk.service

if [ -f /etc/cups-kiosk.conf ]; then
  echo "==> 启动服务"
  systemctl daemon-reload
  systemctl enable --now cups-kiosk
  systemctl --no-pager --lines=20 status cups-kiosk || true
  echo
  echo "看日志：journalctl -u cups-kiosk -f"
  echo "刷卡后这里会打出结果；员工说「刷了没反应」时，这是唯一的排查入口。"
  exit 0
fi

echo
echo "!! 还没有 /etc/cups-kiosk.conf。先创建它："
echo
echo "     server = http://<服务器地址>:<端口>"
echo "     token  = <到管理界面「取件终端」里复制>"
if [ "$KIND" = python ]; then
  echo "     device = ACS           # 可留空；多台读卡器时用来选（pcsc_scan 能看名字）"
  echo "     # beep_cmd = /usr/local/bin/kiosk-beep    # 接了蜂鸣器才需要"
else
  echo "     device = RFID          # 可留空，但强烈建议填；用下面的 -list 看名字"
  echo "     buzzer_pin    = 17     # 没接蜂鸣器就删掉这几行"
  echo "     led_green_pin = 27"
  echo "     led_red_pin   = 22"
fi
echo
echo "   然后：sudo chmod 600 /etc/cups-kiosk.conf   （token 等同于密码）"
echo

if [ "$KIND" = python ]; then
  echo "==> 依赖装了吗（没装的话现在装）："
  echo "     sudo apt install -y pcscd python3-pyscard python3-requests"
  echo "     sudo systemctl enable --now pcscd"
  echo
  echo "==> 当前机器上的读卡器（device = 填名字里的一段）："
  # 不写 pcsc_scan 的参数：它不同版本的选项不一样，而它启动时本来就会
  # 把所有读卡器列出来。这里的目的是「看到名字」，不是「跑个脚本」。
  if command -v pcsc_scan >/dev/null; then
    echo "     运行 pcsc_scan，它会列出所有读卡器（看完按 Ctrl+C 退出）"
  else
    echo "     装一下：sudo apt install -y pcsc-tools，然后运行 pcsc_scan"
  fi
  echo
  echo "==> 配置写好之后：sudo /usr/local/bin/cups-kiosk --read   先验证读卡器"
  echo "    再：sudo systemctl enable --now cups-kiosk"
else
  echo "==> 当前机器上的输入设备（device = 填名称里的一段）："
  /usr/local/bin/cups-kiosk -list || true
  echo
  echo "==> 配置写好之后再执行：systemctl enable --now cups-kiosk"
fi
