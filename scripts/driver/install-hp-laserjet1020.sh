#!/usr/bin/env bash
# HP LaserJet 1020 / 1020 Plus 固件安装 + A4-default PPD 派生脚本。
#
# 背景（issue #40）：
# HP LaserJet 1020 系列属于"host-based"打印机（也称 GDI 打印机），打印机内部
# 没有 ROM 存储固件，每次上电后必须由主机上传固件（sihp1020.dl）才能工作。
# Debian 的 printer-driver-foo2zjs 包已提供了驱动过滤器（foo2zjs / foo2zjs-wrapper），
# 但固件文件本身因版权限制不包含在 Debian 包内，需要额外下载。
#
# 本脚本负责：
#   ① 从本仓库 GitHub Releases 镜像下载 sihp1020.dl 固件文件；
#   ② 放置到 /lib/firmware/hp/ 目录——foo2zjs 上游 hplj1000 加载脚本写死的
#      FWDIR；Debian 的 printer-driver-foo2zjs 包正是预留了空目录
#      /usr/lib/firmware/hp/（merged-usr 下与 /lib/firmware/hp/ 等价）等用户填；
#   ③ 从 foo2zjs.ppd-compiled 派生一份默认 A4 纸张的 PPD，安装到
#      /usr/share/cups/model/HP/HP-LaserJet_1020-foo2zjs-A4.ppd（issue #48）。
#
# 固件上传到 USB 设备：物理机上由 udev 规则 /usr/lib/udev/rules.d/85-hplj10xx.rules
# 触发同包的 /usr/lib/udev/hplj1020 脚本完成（该脚本通过 CUPS USB backend 把
# 这里下好的 sihp1020.dl 写入打印机）。容器内没有 udev daemon、规则不会触发，
# 因此 cups/entrypoint.sh 在启动 cupsd 之前会主动以 SUBSYSTEM=usb 调用一次
# /usr/lib/udev/hplj1020——等价于"在容器里手动 trigger 一次 udev 事件"。
# 这意味着本脚本下载的固件必须放在原生脚本期望的路径，否则原生脚本会因
# "Missing firmware" 直接 return 1，等于没修复 issue #48。
#
# ────────────────────────────────────────────────────────────────────
# 下载策略
# ────────────────────────────────────────────────────────────────────
# 与 install-escpr2.sh / install-konica-bizhub.sh 同模式：只从本仓库自维护的
# GitHub Releases 镜像（tag = cups-driver）下载，避免 HP 官方下载链路在 CI 里
# 的不稳定性。fail-fast：下载失败立即非零退出。
# 升级/替换固件：在本仓库 cups-driver release 上传新版文件，修改下方 URL。
#
# ────────────────────────────────────────────────────────────────────
# A4-default PPD 的来由（issue #48）
# ────────────────────────────────────────────────────────────────────
# foo2zjs 上游 HP-LaserJet_1020.ppd 的 *DefaultPageSize 是 Letter（美国默认），
# 但 *PageSize 列表里包含 A4。CUPS 把 PPD 的 PageSize 列表暴露给 IPP
# media-supported，把 *DefaultPageSize 暴露给 IPP media-default。
#
# 苹果设备走 AirPrint（IPP）连接 CUPS 共享出来的打印机时，会优先按
# media-default 渲染首屏纸张选项。当 default 是 Letter 时，国内常用的 A4
# 在 iOS 打印面板里会被折叠/隐藏，用户反映"无 A4 选项"。
#
# 修复手法：把 foo2zjs 的 HP 1020 PPD 抽出来，把四组默认值（PageSize /
# PageRegion / ImageableArea / PaperDimension）从 Letter 改成 A4，并改写
# NickName 让它在 CUPS UI / lpinfo -m 里清晰可辨。原 foo2zjs.ppd-compiled
# 里的 Letter-default PPD 保留不动——用户可在添加打印机时按需选择 A4 变体，
# 或对已存在的打印机用 lpadmin -p NAME -o media-default=iso_a4_210x297mm
# 切换。

set -eo pipefail

# ────────────────────────────────────────────────────────────────────
# 配置
# ────────────────────────────────────────────────────────────────────
FW_FILENAME="sihp1020.dl"
FW_MIRROR_URL="https://github.com/hanxi/cups-web/releases/download/cups-driver/${FW_FILENAME}"
# foo2zjs 上游 hplj1000 脚本（PPD/HP-LaserJet_1020.ppd 配套的 hotplug loader，
# 在 Debian 下被 dh_link 重命名为 /usr/lib/udev/hplj1020）写死 FWDIR=/lib/firmware/hp。
# Debian 的 printer-driver-foo2zjs 包预留了空 /usr/lib/firmware/hp/（与 /lib/firmware/hp/
# 在 merged-usr 下是同一个目录），就是等用户把固件放进来。固件目录不能改。
FW_INSTALL_DIR="/lib/firmware/hp"

PPD_INSTALL_DIR="/usr/share/cups/model/HP"
PPD_INSTALL_NAME="HP-LaserJet_1020-foo2zjs-A4.ppd"
# dh_pyppd 生成的可执行 PPD archive 候选路径，按优先级排列。
# bookworm/trixie 当前使用 /usr/lib/cups/driver/foo2zjs（CUPS driver 接口标准位置，
# dh_pyppd --archive-filename=foo2zjs 的默认输出路径）；老版本 Debian 曾把它放在
# /usr/share/cups/model/foo2zjs.ppd-compiled。两条路径都是同一种 pyppd 可执行 archive，
# 调用接口完全一致：`<archive> cat HP-LaserJet_1020.ppd` 抽内容。
PYPPD_ARCHIVES=(
    /usr/lib/cups/driver/foo2zjs
    /usr/share/cups/model/foo2zjs.ppd-compiled
)
# 极个别下游分发可能改铺裸 PPD，作为兜底也扫一下常见目录。
PPD_SEARCH_DIRS=(
    /usr/share/cups/model/foo2zjs
    /usr/share/cups/model
    /usr/share/ppd/foo2zjs
    /usr/share/ppd
)

# ────────────────────────────────────────────────────────────────────
# 下载 & 安装固件
# ────────────────────────────────────────────────────────────────────
mkdir -p "${FW_INSTALL_DIR}"

echo "[hp-laserjet1020] downloading firmware from ${FW_MIRROR_URL}"
curl -fL --retry 3 --retry-delay 3 -o "${FW_INSTALL_DIR}/${FW_FILENAME}" "${FW_MIRROR_URL}"

# 校验文件非空
if [ ! -s "${FW_INSTALL_DIR}/${FW_FILENAME}" ]; then
    echo "[hp-laserjet1020] FATAL: downloaded firmware file is empty"
    exit 1
fi

echo "[hp-laserjet1020] installed firmware: ${FW_INSTALL_DIR}/${FW_FILENAME} ($(wc -c < "${FW_INSTALL_DIR}/${FW_FILENAME}") bytes)"

# ────────────────────────────────────────────────────────────────────
# 派生 A4-default PPD（issue #48）
# ────────────────────────────────────────────────────────────────────
# foo2zjs 的 PPD 在 Debian 上由 dh_pyppd 打成单文件可执行 archive
# （pyppd-ppdfile 模板，见 https://github.com/OpenPrinting/pyppd）。
# 接口：`<archive> list` 列出所有 URI；`<archive> cat <uri>` 抽 PPD 内容。
# 不同版本 Debian 把 archive 放在不同路径——按 PYPPD_ARCHIVES 顺序探测。
# 罕见情况下下游可能改铺裸 PPD，再退化到 PPD_SEARCH_DIRS 兜底。
#
# 关键点：dh_pyppd 把 /usr/share/ 整目录喂给 pyppd（见 dh_pyppd 源码），
# 索引 key 形如 "0/ppd/foo2zjs/HP-LaserJet_1020.ppd"——前缀路径包含原 PPD
# 的相对位置，并非裸文件名。pyppd runner.cat 会剥掉首个路径段（索引号）后
# 再补 "0/" 用于查表，因此必须传一个**带至少一个 / 分隔符**的 URI；最稳的
# 做法是先调 `list` 拿到真实 URI 再 `cat`，这样无需关心 Debian 把 PPD 装到
# /usr/share/ 下的具体子路径（trixie 是 ppd/foo2zjs/，未来若改回 cups/model
# 也无需改脚本）。
#
# 注意：HP-LaserJet_1020.ppd 是 foo2zjs 源码的文件名（PPD/HP-LaserJet_1020.ppd），
# 不是 NickName。所有路径都拿不到非空内容会立即 fail-fast。

PPD_TMP="$(mktemp /tmp/hp1020-a4.ppd.XXXXXX)"
trap 'rm -f "${PPD_TMP}"' EXIT

PYPPD_ARCHIVE=""
for cand in "${PYPPD_ARCHIVES[@]}"; do
    if [ -x "${cand}" ]; then
        PYPPD_ARCHIVE="${cand}"
        break
    fi
done

if [ -n "${PYPPD_ARCHIVE}" ]; then
    echo "[hp-laserjet1020] discovering HP-LaserJet_1020.ppd URI in ${PYPPD_ARCHIVE}"
    # `list` 输出每行形如 `"<binary>:<index>/<rel-path>" <lang> "<manuf>" ...`，
    # 取第一个匹配 HP-LaserJet_1020.ppd 的 URI（双引号之间的内容）。
    PPD_URI="$("${PYPPD_ARCHIVE}" list | awk -F'"' '/HP-LaserJet_1020\.ppd/ {print $2; exit}')"
    if [ -z "${PPD_URI}" ]; then
        echo "[hp-laserjet1020] FATAL: HP-LaserJet_1020.ppd not listed by ${PYPPD_ARCHIVE}"
        exit 1
    fi
    echo "[hp-laserjet1020] extracting via ${PYPPD_ARCHIVE} cat ${PPD_URI}"
    "${PYPPD_ARCHIVE}" cat "${PPD_URI}" > "${PPD_TMP}"
else
    PPD_SRC=""
    for dir in "${PPD_SEARCH_DIRS[@]}"; do
        for cand in "${dir}/HP-LaserJet_1020.ppd" "${dir}/HP-LaserJet_1020.ppd.gz"; do
            if [ -f "${cand}" ]; then
                PPD_SRC="${cand}"
                break 2
            fi
        done
    done
    if [ -z "${PPD_SRC}" ]; then
        echo "[hp-laserjet1020] FATAL: HP-LaserJet_1020.ppd not found in pyppd archives (${PYPPD_ARCHIVES[*]}) nor in ${PPD_SEARCH_DIRS[*]}"
        exit 1
    fi
    echo "[hp-laserjet1020] using PPD source ${PPD_SRC}"
    case "${PPD_SRC}" in
        *.gz) zcat "${PPD_SRC}" > "${PPD_TMP}" ;;
        *)    cat  "${PPD_SRC}" > "${PPD_TMP}" ;;
    esac
fi

if [ ! -s "${PPD_TMP}" ]; then
    echo "[hp-laserjet1020] FATAL: extracted PPD is empty (foo2zjs upstream may have renamed HP-LaserJet_1020.ppd)"
    exit 1
fi

# 校验抽出来的 PPD 确实是 HP 1020、且 PageSize 列表里有 A4。
# 任何一项不满足都说明 foo2zjs 上游结构变了，立即 fail-fast。
if ! grep -q '^\*Product:[[:space:]]*"(HP LaserJet 1020)"' "${PPD_TMP}"; then
    echo "[hp-laserjet1020] FATAL: extracted PPD doesn't look like HP LaserJet 1020"
    exit 1
fi
if ! grep -q '^\*PageSize A4' "${PPD_TMP}"; then
    echo "[hp-laserjet1020] FATAL: A4 not in extracted PPD's PageSize list"
    exit 1
fi
if ! grep -q '^\*DefaultPageSize:[[:space:]]\+Letter' "${PPD_TMP}"; then
    echo "[hp-laserjet1020] FATAL: extracted PPD's *DefaultPageSize is not Letter (upstream may have changed defaults)"
    exit 1
fi

# 把 4 组 *Default*: Letter 改成 A4，同时改 PCFileName / ShortNickName / NickName，
# 让 lpinfo -m 里能跟原 PPD 区分开。
# - PCFileName 是 8.3 风格的内部唯一标识，避免和原 "FOO2ZJS-.PPD" 撞名。
# - ShortNickName ≤ 31 字符（PPD 规范），"HP LaserJet 1020 foo2zjs-z1 A4" = 30 字符，刚好。
sed -i -E '
    s/^\*PCFileName:[[:space:]]+"[^"]*"/\*PCFileName:\t"FOO2A4Z1.PPD"/;
    s/^\*ShortNickName:[[:space:]]+"[^"]*"/\*ShortNickName: "HP LaserJet 1020 foo2zjs-z1 A4"/;
    s|^\*NickName:[[:space:]]+"[^"]*"|\*NickName:      "HP LaserJet 1020 Foomatic/foo2zjs-z1 A4 (cups-web)"|;
    s/^\*DefaultPageSize:[[:space:]]+Letter[[:space:]]*$/\*DefaultPageSize: A4/;
    s/^\*DefaultPageRegion:[[:space:]]+Letter[[:space:]]*$/\*DefaultPageRegion: A4/;
    s/^\*DefaultImageableArea:[[:space:]]+Letter[[:space:]]*$/\*DefaultImageableArea: A4/;
    s/^\*DefaultPaperDimension:[[:space:]]+Letter[[:space:]]*$/\*DefaultPaperDimension: A4/
' "${PPD_TMP}"

# 验证 4 组默认都改成 A4，否则说明 sed 没命中任何一行（PPD 格式变了）。
for key in DefaultPageSize DefaultPageRegion DefaultImageableArea DefaultPaperDimension; do
    if ! grep -q "^\*${key}:[[:space:]]\+A4" "${PPD_TMP}"; then
        echo "[hp-laserjet1020] FATAL: failed to patch *${key} to A4"
        exit 1
    fi
done

install -d "${PPD_INSTALL_DIR}"
install -m 644 "${PPD_TMP}" "${PPD_INSTALL_DIR}/${PPD_INSTALL_NAME}"

echo "[hp-laserjet1020] installed A4-default PPD: ${PPD_INSTALL_DIR}/${PPD_INSTALL_NAME}"
