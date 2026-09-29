# 容器启动流程深度说明

> 本文档是 [AGENTS.md](../AGENTS.md)「容器启动流程」章节的深度补充，收录 entrypoint 各步骤的设计理由、cupsd watchdog 的 127 死循环案例与 restore-drivers 的容错设计。

单容器形态下 `entrypoint.sh` 是 `ENTRYPOINT`，顺序如下（编号与文件中的注释块一致）：

1. **`restore-drivers`**：恢复 `.drivers` 快照。每个驱动**先包级**（`dpkg -i` 归档的 `.deb`）**再文件级**（按 manifest `cp -aT`）——文件级条目的语义是"覆盖/补充"，必须后落才生效
2. **CUPS 管理员用户**：`/etc/shadow` 里没有 `$CUPSADMIN` 时 `useradd -r -G lpadmin` + `chpasswd`，并按 `$TZ` 配 tzdata
3. **CUPS 配置还原**：`/etc/cups/cupsd.conf` 不存在（挂了空卷）时从镜像内的 `/etc/cups-bak/` 复制；随后**在 if 之外**幂等补 `ssl/` 目录与 `ReadyPaperSizes`（见下方专门说明）
4. **HP 1020 PPD 的 Letter → A4 一次性修补**（issue #48）：只改 `*Product` + `*FoomaticIDs` 双重指纹命中、且当前默认仍是 Letter、且 `*PageSize A4` 存在的存量 PPD，改前备份 `.bak-cupsweb-issue48`
5. **HP host-based 固件上传**：容器内没有 udev daemon，手动喂 `SUBSYSTEM=usb` 调用 foo2zjs 上游的 `/usr/lib/udev/hplj{1000,1005,1018,1020}` + `hpljP{1005,1006,1505}`，**后台跑**（上游脚本里有 `sleep 3`，同步调用会拖慢 cupsd 启动），日志在 `/var/log/cups/hp-firmware.log`
6. **dbus + avahi + ipp-usb**：后台拉起，用于 driverless / IPP Everywhere 发现与 AirPrint 广播；三者均允许缺失/失败，不影响 cupsd。⚠️ dbus 用**容器内自启**的 `dbus-daemon --system`，不要挂载宿主 `/run/dbus/system_bus_socket`——socket 路径被占会导致容器 dbus 起不来、avahi 跟着失效（issue #107）；dbus/avahi 启动失败现在会打 `WARN` 日志而非静默
7. **cupsd + watchdog**（见下方专门说明）
8. **等 cupsd 就绪**：`lpstat -r` 轮询，最多 30 次 × 1s
9. **HP 1020 队列 `media-default=A4`**：后台对命中的 HP 1020 队列执行 `lpadmin -p NAME -o media=iso_a4_210x297mm`，让 iOS 打印面板打开时**预选** A4。⚠️ 见下方「media-ready 的真相」——这一句**不**影响纸张候选**列表**
10. **`exec /cups-web`**：作为 PID 1 前台运行（`exec` 替换父进程不会杀掉已 fork 的后台子 shell）

## ⚠️ 第 3 步的两条补丁为什么必须在 `if` 之外

第 3 步的守卫是 `if [ ! -f /etc/cups/cupsd.conf ]` —— **只看一个文件**。用户挂载的 `./.etc` 卷里只要存在一份 `cupsd.conf`（旧版本留下的、从别处拷来的、手工放的），整块 `cp -rpn /etc/cups-bak/*` 就被跳过。于是新版镜像往基线里加的任何东西，**存量用户永远拿不到**。

所以下面两条写在 `if` 外面、每次启动都幂等执行：

### `ssl/` 目录

`cupsd` **不会自己创建** `ServerRoot/ssl`：源码里那处 `cupsdCheckPermissions(ServerRoot, "ssl", …)` 的 `create_dir` 参数传的是 **0**（紧邻的 `"ppd"` 那行传的是 1），`lstat` 失败时既不 mkdir 也不报错，直接 `return 1` —— 启动静默通过。

要到**第一次 ipps 握手**才炸：`cupsMakeServerCredentials()` 用 `cupsFileOpen(keyfile, "w")` 写 `/etc/cups/ssl/<cn>.key`，没有 mkdir → ENOENT → error_log 刷 `Unable to create server credentials` + `Unable to encrypt connection`。而 `_ipps._tcp` 的 DNS-SD 广播**无条件**进行（编译时 `--enable-gnutls`，与证书是否就绪毫无耦合），macOS/iOS 又优先挑 ipps 记录 —— 这就是"Bonjour 里看得到、打不了"的一条经典成因。

以前这个目录之所以一直存在，纯粹是因为 Debian 的 `cups-daemon` 把它作为 package-owned 空目录发布了。那是别人的实现细节，不该依赖。

### `ReadyPaperSizes`（media-ready 的真相）

**iPhone AirPrint 面板的纸张候选列表读 IPP 的 `media-ready`（当前已装纸），而 CUPS 里 `media-ready` 只由 cupsd.conf 的 `ReadyPaperSizes` ∩ PPD 支持的尺寸决定** —— `scheduler/printers.c` 的 `load_ppd()` 里那一处是全 scheduler 唯一的写入点，判据是"PPD 尺寸名在 `ReadyPaperSizes` 里"。

> 🚫 **`lpadmin -p X -o media=iso_a4_210x297mm` 修不了这个问题。** 它只写 `media-default`（影响未指定纸张时的默认值、以及面板的预选项），对 `media-ready` **零影响**。本文档与 `entrypoint.sh` 的注释以前都写着"CUPS 随之把 media-ready 通告成 A4"，**那句话是错的**；issue #82 当时大概率只是"预选项变成 A4 看起来好了"，列表并没有变。

不配 `ReadyPaperSizes` 时 cupsd 按 locale 兜底：`DefaultPaperSize` 未配 → locale 属于 `C/POSIX/en*/en_US/en_CA/fr_CA` 就取 `Letter`，否则 `A4`；然后 Letter 对应 `Letter,Legal,Tabloid,4x6,Env10`，A4 对应 `A4,A3,A5,A6,EnvDL`。本镜像 **没有设 `LANG`/`LC_ALL`** → locale 是 C → 走 Letter 分支 → **A4 永远不出现在 iOS 面板里**。

Debian 的逃生口 `/etc/papersize`（libpaper）在本镜像也**失效**：源码编译时没传 `--enable-libpaper`，`HAVE_LIBPAPER` 未定义，那段分支被编译掉了。

所以真正的修法只有一条：cupsd.conf 里写 `ReadyPaperSizes A4,A3,A5,A6,EnvDL`。⚠️ 值必须是 **PPD 尺寸名**（`A4`），不是 PWG 名（`iso_a4_210x297mm`）。

好消息：**不需要手工清 `var/cache/*.data`**。PPD 缓存的有效性判据包含 `ConfigurationFile`（即 cupsd.conf）的 mtime，改配置就会让缓存失效并重新解析 PPD。

## ⚠️ `restore-drivers` 现在会跑 `dpkg -i`

包级恢复放在 entrypoint **第一步**是有意的，而且比放后面更好：此时 cupsd 还没起来，厂商 postinst 里的 `invoke-rc.d cups restart`（都带 `|| true`）是无害 no-op；等 cupsd 真正启动时 PPD/filter 已经就位，不需要二次 reload。

但它跑在容器启动的关键路径上，所以有四条硬约束：

| 措施 | 不做会怎样 |
| --- | --- |
| `DEBIAN_FRONTEND=noninteractive` + `dpkg -i --force-confold` | conffile 冲突让 dpkg **交互式等输入** → 容器永久卡住起不来，比崩掉更糟 |
| `timeout 600` 包一层 | 驱动恢复是尽力而为，绝不允许无限阻塞启动 |
| 临时 `policy-rc.d`（`exit 101`，仅当原本不存在时接管，结束后删） | 某些厂商 postinst 会 `service cups start`，那会在 watchdog 之外再起一个 cupsd 抢 631 端口 → 重启风暴 |
| 失败梯度 `dpkg -i` → `--force-depends` → `dpkg --configure -a --force-depends` | Canon 的 `cups-bsd`/`libgtk-3-0` 依赖在 trixie 永远满足不了，不降级就装不上；而 `--force-depends` 会解包**并 configure**，Konica 那个靠 postinst 现建的 filter 符号链接就指望这一步 |

> 🚫 **永不在 restore 里跑 `apt-get -f install`**：entrypoint 第一步不保证有网络、镜像的 `/var/lib/apt/lists` 通常是空的，而且 apt 修依赖时会选择**删掉**装不上的厂商包（`install-canon-ufr2.sh` 的注释里记录过这个实际表现）—— 比不修更糟。

## ⚠️ cupsd 必须在 watchdog 子 shell **内部前台**启动

bash 的 `wait` 只能等待**当前 shell 自己的子进程**。老实现在主 shell 里 `cupsd -f &` 拿 PID，再在 watchdog 子 shell 里 `wait $CUPSD_PID`——那个 PID 对子 shell 来说是**兄弟进程**，bash 立刻返回 **127**（not a child of this shell）而不阻塞（老代码还用 `|| true` 把这个错误吞了）。于是循环秒进下一轮 → `sleep 2` → 又 fork 一个 cupsd → 631 端口已被占用、新进程秒退 → **每 2 秒一次重启风暴，日志刷满**。

正确形态（当前实现）：`/usr/sbin/cupsd -f` 写在 `( while true; … ) &` 子 shell **里面**、以前台方式跑。这样它是子 shell 的直接子进程，子 shell 会阻塞到 cupsd 真正退出，`$?` 也是 cupsd 的真实退出码；整个子 shell 再 `&` 到后台，不阻塞后续启动步骤。

**🚫 不要把 `/usr/sbin/cupsd -f` 挪到子 shell 外面再配 `wait`** —— 那就是上面那个 127 死循环。

### fast-fail 退避

`CUPSD_MIN_UPTIME=5`、`CUPSD_MAX_FAST_FAILS=5`。存活 < 5s 记一次"短命退出"，连续 5 次就打印醒目中文错误（提示大概率是 `cupsd.conf` 语法错或 631 端口被占用）并 `break` 彻底放弃重启；只要有一次存活超过 5s（说明是偶发崩溃而非配置问题）计数器清零。

## ⚠️ `restore-drivers` 必须永远 `exit 0`

`entrypoint.sh` 是 `set -e`，而 `restore-drivers` 是它的**第一步**。驱动恢复是"尽力而为"的操作：快照可能被旧版本写坏、目标路径可能被别的包占成目录、挂载可能只读。这些**都不该阻塞启动**——一旦容器起不来，用户连 Web UI 都进不去，**没法自救卸载那个坏驱动**。

所以是双层保险：`restore-drivers.sh` 本身**故意不用 `set -e`**（只 `set -uo pipefail`），逐文件记账、结尾汇总 `total_errors` / `total_skipped` 并打印"驱动恢复不完整，但不阻塞容器启动；可在 Web UI 里卸载后重新安装该驱动"，最后**无条件 `exit 0`**；`entrypoint.sh` 那一行再补一层 `|| echo "[entrypoint] WARN: restore-drivers 部分失败，继续启动"` 兜底，防它因意外信号/非零退出把 `set -e` 的 entrypoint 带崩。
