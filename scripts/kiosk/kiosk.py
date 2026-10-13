#!/usr/bin/env python3
"""刷卡取件终端 —— PC/SC 读卡器版（ACR122U 等）。

读卡 → POST 给服务器 → 按结果反馈。

    kiosk.py --read                  读一次卡，把 UID 打出来（绑卡时用）
    kiosk.py                         一直跑，刷卡就取件
    kiosk.py --verbose               多打一些诊断信息

用法与配置见 docs/pull-printing.md。

为什么是 PC/SC 而不是「读卡器当键盘用」：
    ACR122U 这类读卡器通过 pcscd 提供卡片访问，吐出来的是**卡片的 UID**
    （十六进制串）。这比键盘模拟的读卡器好得多 —— 后者按**键码**输出，
    会被树莓派/电脑的**键盘布局**影响，同一张卡在两台机器上可能读出
    不同的字符串。用 PC/SC 就没有这一整类问题。

前提：
    sudo apt install pcscd python3-pyscard python3-requests
    sudo systemctl enable --now pcscd
    插上读卡器，`pcsc_scan` 能看到卡，就说明底层通了。
"""

import argparse
import json
import os
import subprocess
import sys
import time

try:
    import requests
except ImportError:
    sys.exit("缺少 requests：sudo apt install python3-requests")

try:
    import fcntl
except ImportError:  # Windows 上没有；这个脚本本来就只跑在 Linux 上
    fcntl = None

try:
    from smartcard.Exceptions import (
        CardConnectionException,
        NoCardException,
        NoReadersException,
    )
    from smartcard.System import readers
except ImportError:
    sys.exit("缺少 pyscard：sudo apt install python3-pyscard")


# ── PC/SC 的 Get Data（UID）伪 APDU ──────────────────────────────────
#
# `FF CA 00 00 00` 是 PC/SC 规范里的「取 UID」，ACR122U 与绝大多数
# Mifare Classic / Ultralight 卡都吃这一条。成功返回 SW=9000，
# 数据部分是 UID 的字节（4 字节或 7 字节都有）。
#
# 换别的卡型（DESFire、CPU 卡）可能不吃这条 —— 那时 SW 会不是 9000，
# 脚本会把 SW 原样打出来。看到 SW != 9000 时别去怀疑系统，
# 那是卡型问题，需要换一条 APDU。
GET_UID = [0xFF, 0xCA, 0x00, 0x00, 0x00]

# 两次读卡之间的最小间隔，以及「等卡拿开」的轮询间隔。
POLL_INTERVAL = 0.25

DEFAULT_CONFIG = "/etc/cups-kiosk.conf"

# 服务端返回的状态码 → 给人看的话。
#
# 与 cmd/server/kiosk_handlers.go 里的 kioskStatus* 常量一一对应。
# 新增状态码时这里必须同步补一行 —— 少了的话员工会看到一串英文码，
# 而现场没人查得到那是什么意思。
STATUS_TEXT = {
    "released": "✅ 已开始打印，请稍候取纸",
    "partial": "⚠️ 部分任务打印失败，请找管理员",
    "failed": "❌ 全部任务打印失败，请找管理员",
    "no_jobs": "ℹ️ 这台打印机上没有你的待取件任务",
    "card_unbound": "❌ 工牌未登记，请联系管理员绑卡",
    "printer_unavailable": "❌ 打印机当前不可用，请稍后再试",
    "unknown_terminal": "❌ 终端未注册（token 填错了？）",
    "terminal_disabled": "❌ 此终端已被停用，请联系管理员",
    "rate_limited": "❌ 刷卡太频繁，请稍等一下",
    "bad_swipe": "（空刷卡，已忽略）",
    "unreachable": "❌ 连不上服务器，请找管理员",
    "old_server": "❌ 服务器上没有取件接口（跑的还是旧版本）",

    # 下面几个只在「管理界面正开着绑卡窗口」时出现 ——
    # 那时刷卡的含义是绑定，不是取件。
    "bound": "✅ 工牌绑定成功",
    "bind_taken": "⚠️ 这张卡已经绑给别的账号了，换一张卡再刷",
    "bind_failed": "❌ 绑定失败，请到管理界面重试",
    "bind_idle": "ℹ️ 这是绑卡专用终端：请先在管理界面点「开始等待刷卡」，再刷卡",
    "ok": "✅ 终端自检通过",
}

# 这些状态应当让终端「响一声」。其余（没有任务、误触）不出声 ——
# 员工什么都没做错的时候冲他响，只会让人以为是机器坏了。
BEEP_STATUSES = {"released", "partial", "failed", "card_unbound",
                 "printer_unavailable", "unknown_terminal",
                 "terminal_disabled", "unreachable", "old_server",
                 "bound", "bind_taken", "bind_failed", "bind_idle"}


def load_config(path):
    """解析 key = value 形式的配置。格式与 Go 版终端程序一致，
    所以管理界面「取件终端 → 配置指引」里那份内容两边通用。"""
    cfg = {}
    if not os.path.exists(path):
        return cfg
    try:
        fh = open(path, encoding="utf-8")
    except PermissionError:
        # 这个坑几乎每个人都会踩一次：配置文件按建议设成了 600（只有 root
        # 能读），然后用普通用户去跑程序。直接用 open 的话抛的是一句
        # Python traceback，对现场的人来说「没反应」和「报错了」几乎一样难查。
        sys.exit(
            f"读不了配置文件 {path}：权限不足。\n"
            f"  它是 600（只有 root 能读）—— 这是对的，token 等同于密码。\n"
            f"  所以二选一：\n"
            f"    sudo /usr/local/bin/cups-kiosk          （直接用 root 跑）\n"
            f"    sudo chown $USER {path}                 （把文件交给你自己）")
    with fh:
        for lineno, raw in enumerate(fh, 1):
            line = raw.strip()
            if not line or line.startswith("#"):
                continue
            key, sep, value = line.partition("=")
            if not sep:
                sys.exit(f"配置文件第 {lineno} 行不是 key = value 形式：{line!r}")
            cfg[key.strip().lower()] = value.strip()
    return cfg


# 单实例锁的路径。放 /tmp 是因为它只要在「同一台机器、同一次开机」内唯一。
LOCK_PATH = "/tmp/cups-kiosk.lock"


def acquire_single_instance_lock():
    """保证同一台机器上只有一个本程序在跑。拿不到就直接退出。

    ⚠️ 两个进程会抢同一张卡：一个正在 transmit，另一个上来 connect，
    PC/SC 就把卡重置了 —— 表现是**满屏的 `Card was reset`**，而且两个进程
    谁也读不出东西来。现场最常见的触发方式是「后台服务在跑，又手工开了个
    `--read` 去验证读卡器」——那是个很自然的动作，所以这里必须先拦住。

    拿不到锁文件（比如 /tmp 不可写）时不阻断启动：这是道保险，
    不该因为它自己出问题就把终端弄停。
    """
    if fcntl is None:
        return None
    try:
        # 用 0666 创建：服务是 root 起的，而管理员手工验证时可能是普通用户。
        # 锁文件按默认 0644 建的话，那个普通用户连打开都打不开 ——
        # 于是「拿不到锁」被误当成「没有别的实例」，保护静默失效。
        fd = os.open(LOCK_PATH, os.O_CREAT | os.O_RDWR, 0o666)
        fh = os.fdopen(fd, "r+")
    except OSError:
        return None
    try:
        fcntl.flock(fh, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError:
        fh.close()
        sys.exit(
            "已经有一个 cups-kiosk 在运行了（多半就是后台那个服务）。\n"
            "  两个进程会抢同一张卡，现象是满屏的 `Card was reset`。\n"
            "  想手工验证读卡器的话，先停掉服务：\n"
            "    sudo systemctl stop cups-kiosk\n"
            "    sudo /usr/local/bin/cups-kiosk --read\n"
            "    sudo systemctl start cups-kiosk")
    return fh


def pick_reader(pattern):
    """按名字子串挑读卡器。pattern 为空时取第一个。

    ⚠️ 不要写「事件编号」之类会漂移的东西 —— 插拔/重启之后顺序会变，
    表现是「昨天还好好的，今天就不读了」。
    """
    try:
        available = readers()
    except NoReadersException:
        sys.exit("没有找到任何读卡器。pcscd 起来了吗？读卡器插上了吗？")
    if not available:
        sys.exit("没有找到任何读卡器。pcscd 起来了吗？读卡器插上了吗？")

    if not pattern:
        return available[0]

    for r in available:
        if pattern.lower() in str(r).lower():
            return r
    names = "\n  ".join(str(r) for r in available)
    sys.exit(f"没找到名字含 {pattern!r} 的读卡器。当前有：\n  {names}")


def close_quietly(conn):
    """释放一个 connection，出错也不管。

    🚫 **不要省掉这一步。** 不 disconnect 的话读卡器一直认为卡还被这个进程
    占着；下一次 createConnection().connect() 撞上那个没释放的旧句柄，
    PC/SC 就会重置卡片，报出 `Card was reset` —— 而且会一直报下去，
    直到进程重启。这正是「绑完一次之后刷卡就一直读卡出错」的根因。
    """
    if conn is None:
        return
    try:
        conn.disconnect()
    except Exception:  # noqa: BLE001 —— 卡已经拿开时 disconnect 也会抛
        pass


def card_present(reader):
    """卡还在读卡器上吗。

    ⚠️ 每次都要 connect + disconnect 配对：只 connect 不 disconnect
    等于每轮泄漏一个句柄，读卡器很快就会开始报 `Card was reset`。
    """
    conn = None
    try:
        conn = reader.createConnection()
        conn.connect()
        return True
    except (NoCardException, CardConnectionException):
        return False
    finally:
        close_quietly(conn)


def read_uid(conn):
    """读卡片的 UID。失败时返回 (None, 说明)。"""
    try:
        data, sw1, sw2 = conn.transmit(GET_UID)
    except Exception as exc:  # noqa: BLE001 —— pyscard 抛的异常类型很杂
        # ⚠️ 这【多半不是故障】：卡在读的过程中被拿开、或者接触不良，
        # pyscard 就会抛 "Card was reset" / "Card is not connected" 之类。
        # 说成「读卡出错」会让人以为读卡器坏了 —— 而实际上多数情况下
        # 再刷一次就好了。如实说清楚，别把人往硬件上带。
        return None, (f"这次没读全（卡多半是在读的过程中被拿开了）：{exc}\n"
                      f"     再刷一次即可；每次都这样的话，看看是不是卡没放稳，"
                      f"或者有别的东西（pcsc_scan 之类）在抢这张卡。")

    if (sw1, sw2) != (0x90, 0x00):
        return None, (f"读卡器返回 SW={sw1:02X}{sw2:02X}（不是 9000）。"
                      f"多半是这张卡的卡型不吃 `FF CA 00 00 00` 这条取 UID 命令，"
                      f"需要换一条 APDU —— 这是卡型问题，不是系统故障。")
    if not data:
        return None, "读到的 UID 是空的"

    return "".join("%02X" % b for b in data), None


def wait_for_card(reader):
    """阻塞直到有卡放上来，返回已连接的 connection。"""
    while True:
        try:
            conn = reader.createConnection()
            conn.connect()
            return conn
        except (NoCardException, CardConnectionException):
            time.sleep(POLL_INTERVAL)


def wait_for_removal(reader):
    """阻塞直到卡被拿开。

    ⚠️ 这一步不是可选的：PC/SC 每轮都会重新呈现同一张卡，
    不等它拿开的话，一次刷卡会被连续处理几百次 —— 员工刷一下，
    服务器收到一屏请求。

    ⚠️ 检测手法必须「连上再松开」（见 card_present）：
    只 connect 不 disconnect 会一轮泄漏一个句柄，读卡器很快就报
    `Card was reset`，然后一直坏到进程重启为止。
    """
    time.sleep(POLL_INTERVAL)
    while card_present(reader):
        time.sleep(POLL_INTERVAL)


def release(server, token, card_no, timeout=60):
    """把卡号送给服务器。返回 (status, 响应体)。

    ⚠️ 超时给得宽（60 秒）：服务端在这一条请求里**同步**把该员工所有
    待取件任务逐个送去 CUPS，任务多时可能要几秒。
    """
    url = server.rstrip("/") + "/api/kiosk/release"
    try:
        resp = requests.post(url, json={"token": token, "cardNo": card_no},
                             timeout=timeout)
    except requests.RequestException as exc:
        print(f"  ⚠️ 连不上服务器 {server}：{exc}")
        return "unreachable", None

    if resp.status_code == 404:
        # 最可能的一种「刷了完全没反应」：服务端跑的还是没有这个接口的旧版本。
        # 与「连不上」分开报，因为两者的修法完全不同。
        return "old_server", None
    if resp.status_code != 200:
        # 服务端只在请求本身畸形时才用非 200（见 writeKioskResult）。
        # 走到这里基本意味着地址填错、打到了别的东西上。
        print(f"  ⚠️ 服务端返回 {resp.status_code}：{resp.text[:200]}")
        return "unreachable", None

    try:
        body = resp.json()
    except ValueError:
        print(f"  ⚠️ 响应不是合法 JSON：{resp.text[:200]}")
        return "unreachable", None

    return body.get("status", ""), body


def beep(cmd, status):
    """跑一条自定义的提示命令（可选）。

    配了就按它响；没配就什么都不做。用命令而不是内建 GPIO，是因为
    「怎么响」取决于现场接了什么东西（蜂鸣器、音箱、LED），
    而那是装的人才知道的事。
    """
    if not cmd:
        return
    try:
        subprocess.run(cmd, shell=True, timeout=5,
                       env={**os.environ, "KIOSK_STATUS": status})
    except Exception as exc:  # noqa: BLE001
        print(f"  （提示命令执行失败：{exc}）")


def report(status, body):
    """把结果打进终端。无屏幕时这是唯一的可读反馈。"""
    text = STATUS_TEXT.get(status)
    if text is None:
        # 服务端加了一个这个脚本不认识的状态码。不要沉默 ——
        # 沉默等于「刷了没反应」，是最难排查的一种现象。
        text = f"❓ 未知结果码：{status}"
    print(f"  {text}")

    if body:
        if body.get("user"):
            print(f"  用户：{body['user']}")
        if body.get("reason"):
            print(f"  原因：{body['reason']}")
        for job in body.get("jobs") or []:
            mark = "✓" if job.get("ok") else "✗"
            line = f"    {mark} {job.get('filename', '')}"
            if job.get("error"):
                line += f" —— {job['error']}"
            print(line)


def preflight(server, token, reader):
    """启动自检。

    没有屏幕的终端上，「刷卡没反应」是最难查的一类现象 —— 而它背后可能是
    完全不同的几件事：连不上服务器 / 服务端是旧版本 / token 抄错了 /
    读卡器根本没读到。这四件事在现场看起来**一模一样**，却要往四个方向修。

    ⚠️ 用 `/api/kiosk/ping` 而不是拿个假卡号打 `/release`：后者有副作用 ——
    如果启动那一刻管理界面正好开着绑卡窗口，那张假卡会被**绑给一个真人**。
    自检不该改变任何状态。
    """
    print("自检中…")
    url = server.rstrip("/") + "/api/kiosk/ping"
    try:
        resp = requests.post(url, json={"token": token}, timeout=15)
    except requests.RequestException as exc:
        sys.exit(f"❌ 连不上服务器 {server}：{exc}\n"
                 f"   在终端上验一下：\n"
                 f"     curl -s -o /dev/null -w '%{{http_code}}\\n' {server}/api/version\n"
                 f"   200 = 地址对；000 = 不通（地址/端口错，或防火墙）")

    if resp.status_code == 404:
        sys.exit(f"❌ 服务器上还没有取件接口 —— {server} 跑的还是旧版本。\n"
                 f"   /api/kiosk/ping 返回 404。先把服务端重新构建并重启。")
    if resp.status_code != 200:
        sys.exit(f"❌ 服务器返回 {resp.status_code}：{resp.text[:200]}")

    try:
        status = resp.json().get("status", "")
    except ValueError:
        sys.exit(f"❌ 响应不是合法 JSON：{resp.text[:200]}")

    if status == "unknown_terminal":
        sys.exit("❌ token 不对：服务器不认识这个终端。\n"
                 "   到管理界面「取件终端」里重新复制一次（注意别带上首尾空格）。")
    if status == "terminal_disabled":
        sys.exit("❌ 这个终端在管理界面里被停用了。把它重新启用再试。")
    if status != "ok":
        sys.exit(f"❌ 自检得到意外结果：{status}")

    print("  ✅ 服务器、token、地址都对")
    print(f"  ✅ 读卡器已就绪（{reader}）\n")


def read_once(reader, verbose):
    """读一次卡并打印 UID。绑卡时用：管理员把这里打出来的串抄进管理界面。"""
    print("请把卡放到读卡器上…")
    conn = wait_for_card(reader)
    try:
        uid, err = read_uid(conn)
    finally:
        # ⚠️ 读完立刻释放。不释放的话下一次 connect 会撞上这个没松开的句柄，
        # 读卡器开始报 `Card was reset` 并且一直坏下去（见 close_quietly）。
        close_quietly(conn)
    if err:
        print(f"  {err}")
        return 1
    print()
    print(f"  UID = {uid}")
    if verbose:
        # 便于与 pcsc_scan 的输出对照：确认拿到的是同一张卡。
        print(f"  （读卡器 {reader}）")
    print()
    print("把上面那串 UID 填进管理界面「用户管理 → 绑卡」的输入框即可。")
    print("大小写与分隔符服务器会自动统一，照抄就行。")
    return 0


def main():
    parser = argparse.ArgumentParser(
        description="刷卡取件终端（PC/SC 读卡器）",
        formatter_class=argparse.RawDescriptionHelpFormatter)
    # ⚠️ 同时注册 `-config` 与 `--config`：随仓库提供的
    # scripts/kiosk/cups-kiosk.service 传的是单横线的 `-config`（Go 版
    # 两种都认，Python 的 argparse【只认注册过的那种】）。
    # 只写 `--config` 的话，systemd 启动会直接以
    # "unrecognized arguments" 退出 —— 而且这个错误只在开机日志里，
    # 现场表现是「服务起不来，刷卡没反应」。
    parser.add_argument("-config", "--config", default=DEFAULT_CONFIG,
                        help=f"配置文件路径（默认 {DEFAULT_CONFIG}）")
    parser.add_argument("--server", help="服务器地址，如 http://192.168.1.22:1180")
    parser.add_argument("--token", help="终端 token（管理界面「取件终端」里复制）")
    parser.add_argument("--reader", help="读卡器名字里的一段（多台时用来选）")
    parser.add_argument("--read", action="store_true",
                        help="只读一次卡并打印 UID，然后退出（绑卡时用）")
    parser.add_argument("--verbose", action="store_true", help="多打一些诊断信息")
    args = parser.parse_args()

    cfg = load_config(args.config)
    if args.config != DEFAULT_CONFIG and not cfg:
        sys.exit(f"配置文件 {args.config} 不存在或为空")

    server = args.server or cfg.get("server") or ""
    token = args.token or cfg.get("token") or ""
    reader_pattern = args.reader or cfg.get("device") or ""
    beep_cmd = cfg.get("beep_cmd", "")

    # 单实例锁要在碰读卡器【之前】拿 —— 它防的正是「两个进程抢同一张卡」。
    lock = acquire_single_instance_lock()  # noqa: F841 —— 持有到进程结束即可

    reader = pick_reader(reader_pattern)
    print(f"读卡器：{reader}")

    if args.read:
        return read_once(reader, args.verbose)

    # 服务器与 token 只有「取件」时才需要 —— 上面 --read 那条路不需要，
    # 所以这里才检查。
    if not server:
        sys.exit("缺少服务器地址。用 --server，或在配置文件里写 server = ...")
    if not server.startswith(("http://", "https://")):
        sys.exit(f"服务器地址必须以 http:// 或 https:// 开头，现在是 {server!r}")
    if not token:
        sys.exit("缺少 token。用 --token，或在配置文件里写 token = ...")

    print(f"服务器：{server}")
    print(f"终端：  {token[:8]}…")
    preflight(server, token, reader)
    print("等待刷卡…（Ctrl+C 退出）")

    while True:
        try:
            conn = wait_for_card(reader)
        except KeyboardInterrupt:
            print("\n退出")
            return 0

        try:
            uid, err = read_uid(conn)
        finally:
            # ⚠️ 读完【立刻】释放。这是「绑完一次之后刷卡就一直读卡出错」的
            # 根因所在：不松开的话读卡器认为卡还被占着，下一次 connect 撞上
            # 这个旧句柄就会重置卡片，然后一路报 `Card was reset`。
            close_quietly(conn)

        if err:
            print(f"  {err}")
            # 读不出 UID 也要等卡拿开，否则会疯狂重试同一张卡。
            wait_for_removal(reader)
            continue

        print(f"\n读到卡：{uid}")
        status, body = release(server, token, uid)
        report(status, body)
        if beep_cmd and status in BEEP_STATUSES:
            beep(beep_cmd, status)

        # ⚠️ 处理完必须等卡拿开，理由见 wait_for_removal 的注释。
        wait_for_removal(reader)
        print("\n等待刷卡…（Ctrl+C 退出）")


if __name__ == "__main__":
    try:
        sys.exit(main())
    except KeyboardInterrupt:
        print("\n退出")
        sys.exit(0)
