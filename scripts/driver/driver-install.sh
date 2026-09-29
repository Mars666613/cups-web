#!/bin/bash
set -eo pipefail

DRIVERS_BASE="/opt/cups-drivers"
SCRIPTS_DIR="${DRIVERS_BASE}/scripts"
DATA_DIR="${DRIVERS_BASE}/data"

log() {
    echo "[driver-install] $*"
}

# ── 架构名探测 ─────────────────────────────────────────────────────────
# 必须用 Debian 架构名（amd64 / arm64 / armhf），因为：
#   ① metadata.txt 的 arch= 会被 Go 后端与 driver-list 读取比较；
#   ② install-*.sh 内部一律用 `dpkg --print-architecture` 做架构分支。
# ⚠️ 不要用 dpkg-architecture —— 它属于 dpkg-dev 包，runtime 镜像**没有安装**
# （只有构建阶段才装），调用失败会静默回落到错误的默认值。`dpkg` 本体一定在。
detect_deb_arch() {
    local arch=""
    arch="$(dpkg --print-architecture 2>/dev/null || true)"
    if [ -z "$arch" ]; then
        # 极端兜底：连 dpkg 都没有时用 uname -m（注意这不是 Debian 架构名，
        # 会得到 x86_64 / aarch64，仅作为诊断信息展示用）。
        arch="$(uname -m 2>/dev/null || echo unknown)"
    fi
    echo "$arch"
}

# ── multiarch 库目录探测 ───────────────────────────────────────────────
# 闭源驱动（Canon UFR II 的 libcnpkbidir*.so 等）会把 .so 装到
# /usr/lib/<triplet>/，必须纳入监控，否则驱动重启后丢库。
# 老实现用 `dpkg-architecture -qDEB_HOST_MULTIARCH`，但该命令在 runtime 镜像
# 里不存在 → 静默回落到硬编码的 x86_64-linux-gnu → 在 arm64/armhf 上监控的是
# 一个不存在的目录，共享库变更**完全抓不到**。这里改为：
#   ① dpkg-architecture 存在就用（构建期/开发机）；
#   ② 否则用 glob 探测 /usr/lib/*-linux-gnu*；
#   ③ 都拿不到就返回空字符串，调用方跳过该目录（绝不使用猜错的路径）。
detect_multiarch_libdir() {
    local triplet="" d
    if command -v dpkg-architecture >/dev/null 2>&1; then
        triplet="$(dpkg-architecture -qDEB_HOST_MULTIARCH 2>/dev/null || true)"
    fi
    if [ -z "$triplet" ]; then
        for d in /usr/lib/*-linux-gnu*; do
            if [ -d "$d" ]; then
                triplet="$(basename "$d")"
                break
            fi
        done
    fi
    if [ -n "$triplet" ] && [ -d "/usr/lib/${triplet}" ]; then
        echo "/usr/lib/${triplet}"
    else
        echo ""
    fi
    return 0
}

DEB_ARCH="$(detect_deb_arch)"
MULTIARCH_LIBDIR="$(detect_multiarch_libdir)"

MONITORED_DIRS=(
    /usr/lib/cups
    /usr/share/cups
    /usr/share/ppd
    /lib/firmware
    /usr/share/foomatic
)
# 探测不到 multiarch 目录时不要塞一个猜的路径进去（find 会静默扫不到东西，
# 更糟的是可能扫到另一个架构的系统库）。
if [ -n "${MULTIARCH_LIBDIR}" ]; then
    MONITORED_DIRS+=("${MULTIARCH_LIBDIR}")
fi

# ── manifest 路径白名单 ────────────────────────────────────────────────
# 为什么必须过滤：manifest.txt 会被 driver-remove 逐条 `rm -f`，被
# restore-drivers 逐条 `cp -a` 覆盖回系统。任何非"驱动产物"的路径进了
# manifest，卸载该驱动时就会删掉系统文件。
# 老实现对 dpkg 来源的文件**完全没有过滤**（`dpkg -L $pkg` 的全部文件直接
# 追加），AIO 模式下 build-essential / gcc / binutils / libc6-dev 都会被算作
# "新装包" → /usr/bin/gcc、/usr/share/man/**、/etc/** 全部进 manifest →
# 卸载一次 canon-capt 就把编译工具链和系统库删了，容器直接残废。
#
# 规则：
#   ① 必须落在 ALLOWED_PREFIXES 之内（真正的驱动产物目录）；
#   ② 即使落在①内，命中 DENIED 模式也一律排除（doc/man/locale/etc/var、
#      /usr/bin 与 /usr/sbin 下的通用系统二进制、dev 包的 .a/.o/.la/pkgconfig）。
# 驱动真正需要的可执行文件在 /usr/lib/cups/filter/ 与 /usr/lib/cups/backend/，
# 绝不会出现在 /usr/bin 或 /usr/sbin。
ALLOWED_PREFIXES=(
    /usr/lib/cups
    /usr/share/cups
    /usr/share/ppd
    /usr/share/foomatic
    /lib/firmware
    /usr/lib/firmware
)
if [ -n "${MULTIARCH_LIBDIR}" ]; then
    ALLOWED_PREFIXES+=("${MULTIARCH_LIBDIR}")
fi

_is_monitored_path() {
    local p="$1" prefix
    case "$p" in
        /usr/bin/*|/usr/sbin/*|/bin/*|/sbin/*|/usr/local/bin/*|/usr/local/sbin/*) return 1 ;;
        /etc/*|/var/*|/usr/include/*|/opt/cups-drivers/*|/tmp/*) return 1 ;;
        /usr/share/doc/*|/usr/share/man/*|/usr/share/locale/*|/usr/share/info/*) return 1 ;;
        /usr/share/cups/doc-root/*) return 1 ;;  # CUPS 自带 Web UI 静态资源，不是驱动产物
        */pkgconfig/*|*.a|*.o|*.la) return 1 ;;
    esac
    for prefix in "${ALLOWED_PREFIXES[@]}"; do
        [ -n "$prefix" ] || continue
        case "$p" in
            "${prefix}/"*) return 0 ;;
        esac
    done
    return 1
}

# ── baseline 归属守卫（源头拦截）─────────────────────────────────────────
# 上面的路径白名单有一个**结构性盲区**：它按路径前缀判断，而 multiarch 目录
# （/usr/lib/<triplet>）既是驱动共享库的家、也是系统库的家。厂商 deb 的依赖被
# apt 解析时会把与打印毫不相关的系统库拖进来（老版本 install-epson-cn.sh 的
# `apt-get -f install` 会拉进整套 Qt5/X11/GL——那只是 GUI 工具的依赖），这些 .so
# 正好落在白名单**内** → 被当成驱动产物写进 manifest → 卸载该驱动时逐条 rm 就把
# 系统库删了，restore 时还会用旧副本覆盖回去。
# 按路径分不开，按**包属主**分得干干净净：镜像构建期把当时已安装的全部包名存进
# baseline-packages.txt（镜像层，不在挂载卷里），凡是这些包拥有的文件一律不接管。
# ⚠️ 这份代码在 driver-install.sh / driver-remove.sh / restore-drivers.sh 三处
# 各有一份，与三份路径白名单同理——每处独立自洽，**不要**因为"install 侧已经拦过"
# 就删掉另外两份（那两份是给存量已被污染的快照兜底的）。
BASELINE_PKG_LIST="${DRIVERS_BASE}/baseline-packages.txt"
BASELINE_OWNED_INDEX=""
BASELINE_INDEX_STATE="none"   # none | ready | unavailable
declare -A BASELINE_HITS=()

_load_baseline_owned_index() {
    [ "${BASELINE_INDEX_STATE}" = "none" ] || return 0

    if [ ! -f "${BASELINE_PKG_LIST}" ]; then
        BASELINE_INDEX_STATE="unavailable"
        log "WARNING: 找不到 ${BASELINE_PKG_LIST}（旧镜像？），本次只做路径白名单过滤。"
        return 0
    fi

    local idx
    if ! idx="$(mktemp /tmp/baseline-owned.XXXXXX 2>/dev/null)"; then
        BASELINE_INDEX_STATE="unavailable"
        log "WARNING: 无法创建 baseline 索引临时文件，本次只做路径白名单过滤。"
        return 0
    fi
    BASELINE_OWNED_INDEX="$idx"

    # baseline 包名先读进内存，避免对 /var/lib/dpkg/info/*.list 里的每个文件
    # fork 一次 grep（镜像里有两千多个包，那样要 fork 两千多次）。
    local -A want=()
    local pkg f
    while IFS= read -r pkg; do
        [ -n "$pkg" ] || continue
        want["$pkg"]=1
    done < "${BASELINE_PKG_LIST}"

    # 只 cat baseline 包自己的 dpkg 清单。multiarch 包的清单名形如
    # <pkg>:<arch>.list，比对前要把 :arch 去掉。
    local lists=()
    shopt -s nullglob
    for f in /var/lib/dpkg/info/*.list; do
        pkg="${f##*/}"
        pkg="${pkg%.list}"
        pkg="${pkg%%:*}"
        if [ -n "${want[$pkg]:-}" ]; then
            lists+=("$f")
        fi
    done
    shopt -u nullglob

    if [ ${#lists[@]} -eq 0 ]; then
        BASELINE_INDEX_STATE="unavailable"
        log "WARNING: baseline 包清单为空（dpkg 数据库异常？），本次只做路径白名单过滤。"
        return 0
    fi

    cat "${lists[@]}" 2>/dev/null | sort -u > "$idx"
    BASELINE_INDEX_STATE="ready"
    return 0
}

# 预计算 候选列表 ∩ baseline-owned：一次 comm 求交集，之后逐条查是 O(1)。
# 比"每个候选路径都 grep 一遍十几万行的索引"快几个数量级。
_mark_baseline_hits() {
    local candidates="$1" line
    BASELINE_HITS=()
    _load_baseline_owned_index
    [ "${BASELINE_INDEX_STATE}" = "ready" ] || return 0
    while IFS= read -r line; do
        [ -n "$line" ] && BASELINE_HITS["$line"]=1
    done < <(sort -u "$candidates" | comm -12 - "${BASELINE_OWNED_INDEX}")
    return 0
}

_is_baseline_owned() {
    [ -n "${BASELINE_HITS[$1]:-}" ]
}

usage() {
    echo "Usage: driver-install <driver-name>"
    echo ""
    echo "Install a printer driver into the CUPS container."
    echo ""
    echo "Available drivers:"
    if [ -d "${SCRIPTS_DIR}" ]; then
        for script in "${SCRIPTS_DIR}"/install-*.sh; do
            [ -f "$script" ] || continue
            name="$(basename "$script" .sh)"
            name="${name#install-}"
            status="not installed"
            if [ -f "${DATA_DIR}/${name}/manifest.txt" ]; then
                status="installed"
            fi
            echo "  ${name}  (${status})"
        done
    else
        echo "  (no driver scripts found in ${SCRIPTS_DIR})"
    fi
    exit 1
}

# --- Argument validation ---
if [ -z "$1" ]; then
    usage
fi

DRIVER_NAME="$1"
INSTALL_SCRIPT="${SCRIPTS_DIR}/install-${DRIVER_NAME}.sh"
DRIVER_DATA="${DATA_DIR}/${DRIVER_NAME}"

# 安装失败 / 架构不支持时清掉可能已经创建的空数据目录：
# driver-list 只按 manifest.txt 判断"已安装"，但留一堆空目录会让人（和
# 后续排障）困惑，也会让 `driver-remove` 的列表出现幽灵条目。
# 只在确认没有 manifest.txt（即本次安装没成功）时才删，绝不碰已安装的驱动。
discard_driver_data() {
    if [ -d "${DRIVER_DATA}" ] && [ ! -f "${DRIVER_DATA}/manifest.txt" ]; then
        rm -rf "${DRIVER_DATA}"
        log "Discarded incomplete driver data dir: ${DRIVER_DATA}"
    fi
}

# ── 包级持久化：apt 捕获钩子的落点与交接目录 ─────────────────────────────
# 见 capture-debs.sh 的文件头注释。这里只负责"装钩子 / 拆钩子"和把交接目录
# 通过 DRIVER_PKG_DIR 告诉 install-*.sh。
APT_HOOK_DROPIN="/etc/apt/apt.conf.d/99-cups-driver-capture"
CAPTURE_TARGET_FILE="/run/cups-drivers/capture-target"
CAPTURE_DIR="${DRIVER_DATA}/.staging-packages"

# 临时文件统一在退出时清理（注意：bash 对同一信号只保留最后注册的 handler，
# 所以本脚本全局只允许这一个 EXIT trap——新增清理动作请写进这个函数里，
# 不要再 trap 一次，否则先注册的那个会被静默覆盖）。
cleanup_tmp() {
    rm -f /tmp/pre-install.txt /tmp/post-install.txt \
          /tmp/new-files.txt /tmp/new-files-filtered.txt \
          /tmp/pre-dpkg.txt /tmp/post-dpkg.txt /tmp/new-packages.txt \
          /tmp/archived-pkg-files.txt /tmp/excluded-pkg-files.txt
    if [ -n "${BASELINE_OWNED_INDEX}" ]; then
        rm -f "${BASELINE_OWNED_INDEX}"
    fi
    # 拆掉 apt 钩子与交接目录标记。⚠️ 必须在所有退出路径上都执行：drop-in 残留
    # 会让容器里后续**任何** apt 操作都去跑一次钩子（钩子本身设计成 target 不存在
    # 就空跑，所以最坏也只是白跑，但没必要留着）。
    rm -f "${APT_HOOK_DROPIN}" "${CAPTURE_TARGET_FILE}"
    # 中途失败时清掉暂存的 .deb（成功路径上它已经被移进 packages/ 了）
    if [ -n "${CAPTURE_DIR}" ] && [ -d "${CAPTURE_DIR}" ]; then
        rm -rf "${CAPTURE_DIR}"
    fi
}
trap cleanup_tmp EXIT

if [ ! -f "${INSTALL_SCRIPT}" ]; then
    log "ERROR: Driver '${DRIVER_NAME}' not found."
    log "No install script at: ${INSTALL_SCRIPT}"
    echo ""
    usage
fi

# --- Check if already installed ---
if [ -f "${DRIVER_DATA}/manifest.txt" ]; then
    log "Driver '${DRIVER_NAME}' is already installed."
    log "To reinstall, first remove it: driver-remove ${DRIVER_NAME}"
    exit 1
fi

# --- Record pre-install filesystem state ---
log "Recording pre-install filesystem state..."

# Capture files in monitored directories
: > /tmp/pre-install.txt
for dir in "${MONITORED_DIRS[@]}"; do
    if [ -d "$dir" ]; then
        # ⚠️ 必须同时采集符号链接（-type l）。有的驱动靠 postinst 现建符号链接把 filter
        # 挂进 /usr/lib/cups/filter/（Konica 的 km_BIZHUB3000MF），有的用符号链接做机型
        # 别名（foo2zjs 的 sihpP1007.dl -> sihpP1005.dl）。只 -type f 的话这些链接永远
        # 进不了 manifest —— 而 dpkg -L 也补不上，因为 postinst 现建的东西不在包文件列表里。
        find "$dir" \( -type f -o -type l \) >> /tmp/pre-install.txt 2>/dev/null || true
    fi
done
sort -u /tmp/pre-install.txt -o /tmp/pre-install.txt

# Capture dpkg state
# ⚠️ 用 dpkg-query 而不是 `dpkg --get-selections`：需要**精确版本号**（归档兜底时
# `apt-get download pkg=version` 要用），而且 get-selections 看不见"包被升级"这种
# 变化（版本变了但选择状态没变），那会让升级过的 baseline 包被误当成"驱动装的"。
dpkg-query -W -f '${Package} ${Version}\n' > /tmp/pre-dpkg.txt 2>/dev/null || true

# --- 装上 apt 捕获钩子（包级持久化）---
# 见 capture-debs.sh 文件头。钩子把 apt 即将安装的 .deb 原件拷进 CAPTURE_DIR；
# 厂商脚本自己 curl/wget 下来的 deb 则通过 DRIVER_PKG_DIR 主动交接到同一个目录。
mkdir -p "${CAPTURE_DIR}" 2>/dev/null || true
if [ -x "${DRIVERS_BASE}/libexec/capture-debs" ]; then
    mkdir -p "$(dirname "${CAPTURE_TARGET_FILE}")" 2>/dev/null || true
    if printf '%s\n' "${CAPTURE_DIR}" > "${CAPTURE_TARGET_FILE}" 2>/dev/null; then
        printf 'DPkg::Pre-Install-Pkgs { "%s"; };\n' "${DRIVERS_BASE}/libexec/capture-debs" \
            > "${APT_HOOK_DROPIN}" 2>/dev/null || \
            log "WARNING: 无法写入 apt 钩子 ${APT_HOOK_DROPIN}，apt 来源的 deb 将退回 cache 打捞"
    else
        log "WARNING: 无法写入 ${CAPTURE_TARGET_FILE}，apt 来源的 deb 将退回 cache 打捞"
    fi
else
    log "WARNING: 未找到 ${DRIVERS_BASE}/libexec/capture-debs（旧镜像？），apt 来源的 deb 将退回 cache 打捞"
fi

# --- Run the install script ---
# 退出码约定（install-*.sh 共同遵守）：
#   0 = 安装成功
#   3 = 当前 CPU 架构不支持该驱动（厂商没有对应架构的二进制）
#   其他非零 = 真正的失败（下载失败、编译失败、dpkg 失败……）
# 老实现不区分退出码，架构不支持的脚本 `exit 0` 后照样写 manifest.txt，
# Web UI 于是显示"已安装"，用户以为可用——必须把这两种情况分开。
log "Installing driver '${DRIVER_NAME}'..."
export CUPS_AIO=1
# 厂商脚本把自己下载的 .deb 拷到这里就能进包级快照（可选，未设置时行为不变）。
export DRIVER_PKG_DIR="${CAPTURE_DIR}"
install_rc=0
bash "${INSTALL_SCRIPT}" || install_rc=$?

if [ "${install_rc}" -eq 3 ]; then
    log "ERROR: 当前架构 ${DEB_ARCH} 不支持驱动 '${DRIVER_NAME}'（厂商未提供该架构的二进制）。"
    log "Nothing was installed; no manifest written."
    discard_driver_data
    exit 3
fi

if [ "${install_rc}" -ne 0 ]; then
    log "ERROR: install script for '${DRIVER_NAME}' failed (exit code ${install_rc})."
    discard_driver_data
    exit "${install_rc}"
fi

log "Install script completed."

# --- Record post-install filesystem state ---
log "Recording post-install filesystem state..."

: > /tmp/post-install.txt
for dir in "${MONITORED_DIRS[@]}"; do
    if [ -d "$dir" ]; then
        find "$dir" \( -type f -o -type l \) >> /tmp/post-install.txt 2>/dev/null || true
    fi
done
sort -u /tmp/post-install.txt -o /tmp/post-install.txt

# Find new files from monitored directories
comm -13 /tmp/pre-install.txt /tmp/post-install.txt > /tmp/new-files.txt

# --- Capture files from new dpkg packages ---
dpkg-query -W -f '${Package} ${Version}\n' > /tmp/post-dpkg.txt 2>/dev/null || true

# Find newly installed packages
# 只比包名：版本变化（升级）不算"新装"——那种包本来就是镜像自带的，归档它等于
# 在 restore 时把系统组件降级回旧版本。
: > /tmp/new-packages.txt
comm -13 <(awk '{print $1}' /tmp/pre-dpkg.txt | sort -u) \
         <(awk '{print $1}' /tmp/post-dpkg.txt | sort -u) \
         > /tmp/new-packages.txt || true

if [ -s /tmp/new-packages.txt ]; then
    log "New packages detected:"
    while IFS= read -r pkg; do
        log "  - ${pkg}"
        # 只接管落在驱动产物白名单内的文件（见 _is_monitored_path 的注释）：
        # 否则 gcc / binutils / libc6-dev 之类编译依赖的系统文件会进 manifest，
        # driver-remove 时把它们删掉，容器直接残废。
        while IFS= read -r f; do
            [ -n "$f" ] || continue
            [ -f "$f" ] || continue
            if _is_monitored_path "$f"; then
                echo "$f"
            fi
        done < <(dpkg -L "$pkg" 2>/dev/null || true) >> /tmp/new-files.txt
    done < /tmp/new-packages.txt
fi

# ── 包级归档：把新装包的 .deb 原件收进快照 ───────────────────────────────
# 为什么需要（详见 capture-debs.sh 头注释）：厂商 deb 把产物装在 /opt/<vendor>/、
# /usr/bin、/usr/share/<vendor>/ 等白名单外的位置，文件级快照要么空、要么缺关键件。
# 归档 .deb、restore 时 dpkg -i 装回去，产物天然完整。
#
# 剪枝规则（两个"天然正确"的条件，不需要遍历依赖闭包）：
#   ① 包在 install-*.sh 退出后**仍处于 installed 状态** —— AIO 编译型脚本会在自己
#      的 EXIT trap 里 `apt-get purge --auto-remove` 掉 build-essential/gcc 之类，
#      于是它们自动被剪掉，无需维护列表；
#   ② 包**不在 baseline-packages.txt 里** —— 绝不归档镜像自带组件，否则 restore 时
#      会把系统组件降级回旧版本。
# 再叠一层包名 DENY 作为"purge 失败时"的第二道保险（与本仓库其它地方的偏执风格一致）。
PKG_NAME_DENY_PATTERNS=(
    # 编译工具链（AIO 模式下编译型脚本会临时装，正常会被它们自己的 EXIT trap purge
    # 掉；这里是"purge 失败"时的第二道保险）
    'build-essential' 'gcc*' 'g++*' 'cpp*' 'binutils*' '*-dev' 'autoconf' 'automake'
    'libtool' 'make' 'pkg-config' 'pkgconf*' 'git' 'git-*' 'dpkg-dev' 'linux-libc-dev'
    'patch' 'm4' 'bison' 'flex' 'cmake' 'ninja-build'
    # ⚠️ CUPS 自身的包**绝不能**进驱动快照。本镜像的 CUPS 是源码编译的（2.4.19，
    # 由 Dockerfile 的 overlay tar 解包进 /usr），apt 侧只装了 cups-daemon /
    # cups-client / cups-filters，故意没有 `cups` 元包。某些驱动包（如
    # printer-driver-gutenprint）硬依赖 `cups` 元包，一旦 apt 去满足它就会装上
    # Debian 的 cups-core-drivers —— 那里面是 /usr/lib/cups/backend/{usb,socket,...}
    # 和一批 filter，**直接覆盖源码编译的同名文件**。
    # 归档这些包的后果更严重：每次容器启动 restore 都会重新覆盖一遍源码 CUPS 组件，
    # 而 driver-remove 还会把它们 purge 掉 → usb/socket backend 消失 → 所有打印机失效。
    # 各 install-*.sh 已经用 `dpkg -i --force-depends` 避免拉进这些包，这里再兜一层。
    'cups' 'cups-core-drivers' 'cups-ppdc' 'cups-server-common' 'cups-common'
    'cups-daemon' 'cups-client' 'cups-filters' 'cups-filters-core-drivers'
    'cups-ipp-utils' 'cups-browsed' 'libcups*'
)

_is_denied_pkg_name() {
    local pkg="$1" pat
    for pat in "${PKG_NAME_DENY_PATTERNS[@]}"; do
        # shellcheck disable=SC2254 # 有意用 glob 匹配包名
        case "$pkg" in
            $pat) return 0 ;;
        esac
    done
    return 1
}

_is_baseline_pkg() {
    [ -f "${BASELINE_PKG_LIST}" ] || return 1
    grep -qxF "$1" "${BASELINE_PKG_LIST}" 2>/dev/null
}

# 归档单个包的 .deb。按可靠性依次尝试：
#   Layer A/B —— CAPTURE_DIR 里已有（apt 钩子抓的 / 厂商脚本主动交接的）
#   Layer C   —— /var/cache/apt/archives/ 打捞（离线可用，但脚本可能已 apt-get clean）
#   Layer D   —— apt-get download（需要网络 + 索引，最后兜底）
#
# 注：这里**不区分"驱动本体"与"被依赖带进来的包"**。原本想分成 payload/deps 两类，
# 但 apt 钩子抓的和厂商脚本交接的 .deb 都落在同一个 CAPTURE_DIR 里，文件名无法可靠
# 反推包的归属，分类只会得到一个似是而非的标签。既然恢复逻辑对两者一视同仁（都是
# `dpkg -i` 装回去），就不做假分类，统一放 packages/。
_archive_pkg_deb() {
    local pkg="$1" ver="$2" dest_dir="${DRIVER_DATA}/packages" found=""
    local ver_esc f

    mkdir -p "${dest_dir}" 2>/dev/null || return 1

    # dpkg 的文件名把版本里的 ':' 转义成 '%3a'
    ver_esc="${ver//:/%3a}"

    # Layer A/B
    shopt -s nullglob
    for f in "${CAPTURE_DIR}/${pkg}_"*.deb; do
        found="$f"; break
    done
    # Layer C
    if [ -z "$found" ]; then
        for f in "/var/cache/apt/archives/${pkg}_${ver_esc}_"*.deb \
                 "/var/cache/apt/archives/${pkg}_"*.deb; do
            found="$f"; break
        done
    fi
    shopt -u nullglob

    if [ -n "$found" ]; then
        cp -a "$found" "${dest_dir}/" 2>/dev/null && { echo "${found##*/}"; return 0; }
    fi

    # Layer D
    local tmpd
    if tmpd="$(mktemp -d /tmp/pkgdl.XXXXXX 2>/dev/null)"; then
        if ( cd "$tmpd" && apt-get download -qq "${pkg}=${ver}" >/dev/null 2>&1 ); then
            shopt -s nullglob
            for f in "$tmpd"/*.deb; do found="$f"; break; done
            shopt -u nullglob
            if [ -n "$found" ] && cp -a "$found" "${dest_dir}/" 2>/dev/null; then
                rm -rf "$tmpd"
                echo "${found##*/}"
                return 0
            fi
        fi
        rm -rf "$tmpd"
    fi
    return 1
}

: > /tmp/archived-pkg-files.txt
# 被**有意排除**的包（baseline / 命中包名 DENY）拥有的文件。它们既不归档，也绝不能
# 进 manifest —— 否则 driver-remove 会按 manifest 逐条 rm 掉这些系统文件。
# 典型案例：konica / gutenprint 的 deb 硬依赖 `cups` 元包，apt 顺手装上
# cups-core-drivers，它提供 /usr/lib/cups/backend/{usb,socket,...} 和一批 filter，
# 这些路径**正在**文件级白名单内，而包又是运行期新装的（不在 baseline 索引里）
# → 只靠 _is_baseline_owned 拦不住，必须在这里显式排除。
: > /tmp/excluded-pkg-files.txt
pkg_count=0
pkg_missed=0
PACKAGES_FILE="${DRIVER_DATA}/packages.txt"

if [ -s /tmp/new-packages.txt ]; then
    log "Archiving .deb originals for package-level restore..."
    mkdir -p "${DRIVER_DATA}" 2>/dev/null || true
    : > "${PACKAGES_FILE}.tmp"
    while IFS= read -r pkg; do
        [ -n "$pkg" ] || continue

        # 条件① 仍处 installed（含 half-configured：--force-depends 装的厂商包会是
        # 这个状态，它的文件已经解包到位，必须归档）
        pkg_status="$(dpkg-query -W -f '${Status}' "$pkg" 2>/dev/null || true)"
        case "$pkg_status" in
            *" installed"*) ;;
            *) log "  skip ${pkg}（安装后已不在系统里，多半是编译依赖被 purge 了）"; continue ;;
        esac

        # 条件②
        if _is_baseline_pkg "$pkg"; then
            log "  skip ${pkg}（镜像自带包，归档它会在 restore 时降级系统组件）"
            dpkg -L "$pkg" 2>/dev/null >> /tmp/excluded-pkg-files.txt || true
            continue
        fi

        if _is_denied_pkg_name "$pkg"; then
            log "  skip ${pkg}（命中包名 DENY：编译工具链 / CUPS 自身组件）"
            dpkg -L "$pkg" 2>/dev/null >> /tmp/excluded-pkg-files.txt || true
            continue
        fi

        pkg_ver="$(dpkg-query -W -f '${Version}' "$pkg" 2>/dev/null || true)"
        pkg_arch="$(dpkg-query -W -f '${Architecture}' "$pkg" 2>/dev/null || true)"

        if deb_name="$(_archive_pkg_deb "$pkg" "$pkg_ver")"; then
            printf '%s %s %s %s\n' "$pkg" "$pkg_ver" "$pkg_arch" "${deb_name}" \
                >> "${PACKAGES_FILE}.tmp"
            pkg_count=$((pkg_count + 1))
            # 记下这个包拥有的全部文件：下面要把它们从文件级列表里减掉，避免同一批
            # 文件既在 .deb 里、又有一份裸副本（快照体积翻倍，remove 时双重删除）。
            dpkg -L "$pkg" 2>/dev/null >> /tmp/archived-pkg-files.txt || true
        else
            pkg_missed=$((pkg_missed + 1))
            log "  WARNING: 拿不到 ${pkg} 的 .deb 原件（钩子未命中 + cache 已清 + 下载失败），"
            log "  WARNING: 该包只能靠文件级快照恢复，可能不完整。"
        fi
    done < /tmp/new-packages.txt

    if [ "${pkg_count}" -gt 0 ]; then
        sort -u "${PACKAGES_FILE}.tmp" -o "${PACKAGES_FILE}"
        log "Archived ${pkg_count} package(s) for package-level restore."
    fi
    rm -f "${PACKAGES_FILE}.tmp"
fi

# --- Filter + deduplicate ---
# find 差分来源虽然已受 MONITORED_DIRS 限制，但同一套白名单再过一遍更安全
# （例如 /usr/share/cups/doc-root/** 之类不该由我们接管的路径）。
sort -u /tmp/new-files.txt -o /tmp/new-files.txt
_mark_baseline_hits /tmp/new-files.txt
baseline_skipped=0
: > /tmp/new-files-filtered.txt
while IFS= read -r f; do
    [ -n "$f" ] || continue
    if ! _is_monitored_path "$f"; then
        log "  skip (outside driver whitelist): $f"
        continue
    fi
    # 路径在白名单内，但文件属于镜像自带包 → 不接管（见文件头 baseline 守卫注释）。
    # 这是 multiarch 目录被 apt 依赖污染时唯一的拦截点。
    if _is_baseline_owned "$f"; then
        baseline_skipped=$((baseline_skipped + 1))
        continue
    fi
    echo "$f" >> /tmp/new-files-filtered.txt
done < /tmp/new-files.txt
mv /tmp/new-files-filtered.txt /tmp/new-files.txt
if [ "${baseline_skipped}" -gt 0 ]; then
    log "  skip ${baseline_skipped} baseline (image-owned) file(s) —— 属于镜像自带包，不纳入驱动快照"
fi

# --- 排除被有意跳过的包所拥有的文件 ---
# 这些包（baseline / 命中 DENY 的 CUPS 组件与工具链）不归档，它们的文件也绝不能进
# manifest —— 否则 driver-remove 会把 CUPS 自己的 backend/filter 逐条 rm 掉，
# restore 又会用旧副本覆盖源码编译的 CUPS。必须在 dedup 之前先减掉。
if [ -s /tmp/excluded-pkg-files.txt ]; then
    sort -u /tmp/excluded-pkg-files.txt -o /tmp/excluded-pkg-files.txt
    before_excl=$(wc -l < /tmp/new-files.txt)
    comm -23 /tmp/new-files.txt /tmp/excluded-pkg-files.txt > /tmp/new-files-filtered.txt || true
    mv /tmp/new-files-filtered.txt /tmp/new-files.txt
    after_excl=$(wc -l < /tmp/new-files.txt)
    if [ "${before_excl}" -ne "${after_excl}" ]; then
        log "  exclude: $((before_excl - after_excl)) 个文件属于被跳过的包（CUPS 组件/工具链），不纳入快照"
    fi
fi

# --- 去重：已归档 .deb 覆盖的文件不再存一份裸副本 ---
# 否则同一批文件既在 packages/*.deb 里、又在 <绝对路径镜像>/ 下各存一份，快照体积
# 翻倍；更糟的是 driver-remove 会先 purge 包、再按 manifest 逐条 rm 同一批路径。
if [ -s /tmp/archived-pkg-files.txt ]; then
    sort -u /tmp/archived-pkg-files.txt -o /tmp/archived-pkg-files.txt
    before_dedup=$(wc -l < /tmp/new-files.txt)
    comm -23 /tmp/new-files.txt /tmp/archived-pkg-files.txt > /tmp/new-files-filtered.txt || true
    mv /tmp/new-files-filtered.txt /tmp/new-files.txt
    after_dedup=$(wc -l < /tmp/new-files.txt)
    if [ "${before_dedup}" -ne "${after_dedup}" ]; then
        log "  dedup: $((before_dedup - after_dedup)) 个文件已由归档的 .deb 覆盖，不再另存裸副本"
    fi
fi

# "装完了但一个文件都没产生"一定是异常（下载到临时目录就退出、装到了未监控
# 位置、或者脚本其实什么都没做）。此时绝不能写 manifest.txt —— 否则
# driver-list / Web UI 会显示"已安装"，用户以为好了。
# 判据：文件级列表与包级归档**同时**为空才算失败。只要归档到了 .deb，就算所有文件
# 都落在白名单外（Epson escpr2 的 298 个文件全在 /opt 就是这种情况），驱动照样能在
# 重启后完整恢复 —— 那不是失败。
if [ ! -s /tmp/new-files.txt ] && [ "${pkg_count}" -eq 0 ]; then
    log "ERROR: No new files detected after installing '${DRIVER_NAME}'."
    log "The driver may have installed files in unmonitored locations, or the"
    log "install script silently did nothing. Refusing to write manifest.txt."
    # ⚠️ 这里最容易误判为"什么都没装"，实际常见情形是**装了但装在监控范围外**：
    # 厂商 deb 普遍把产物放在 /opt/<vendor>/（Epson escpr2、Konica bizhub）或
    # /usr/bin（Canon UFR II 的渲染引擎），这些路径不在 MONITORED_DIRS /
    # ALLOWED_PREFIXES 内。此时文件确实进了容器、CUPS 也能用，但快照是空的 →
    # 容器一重启就全丢。把线索打出来，别让用户对着 "exit 1" 干瞪眼。
    if [ -s /tmp/new-packages.txt ]; then
        log "但检测到本次新装了以下 dpkg 包（说明安装脚本其实成功了）："
        while IFS= read -r pkg; do
            [ -n "$pkg" ] || continue
            log "  - ${pkg}"
        done < /tmp/new-packages.txt
        log "这些包的文件很可能装在 /opt/<vendor>/ 或 /usr/bin 等监控范围外的位置，"
        log "无法生成可恢复的清单 → 本次视为失败，容器重启后这些文件会丢失。"
    fi
    discard_driver_data
    exit 1
fi

# --- Persist driver files ---
log "Persisting driver files to ${DRIVER_DATA}..."
mkdir -p "${DRIVER_DATA}"

file_count=0
while IFS= read -r filepath; do
    [ -z "$filepath" ] && continue
    # ⚠️ 不能只用 `-f`：**指向目录的符号链接**在 `-f` 下判假（-f 会跟随链接判断目标
    # 是否普通文件），于是被静默跳过。escpr2 的
    # /usr/share/ppd/epson-inkjet-printer-escpr2 -> /opt/.../ppds 正是这种情况。
    { [ -e "$filepath" ] || [ -L "$filepath" ]; } || continue
    dest="${DRIVER_DATA}${filepath}"
    mkdir -p "$(dirname "$dest")"
    # `-T` 的理由同 restore-drivers：源是指向目录的符号链接时，若 dest 已存在且也是
    # 这样的链接，cp 会跟随它把东西拷进目录里。这里 dest 通常是新建的，但加上
    # -T 语义更明确，也能防住重装/残留快照的情况。
    cp -aT "$filepath" "$dest"
    file_count=$((file_count + 1))
done < /tmp/new-files.txt

# Save manifest
# ⚠️ manifest.txt 保持**纯路径列表**，不要加 `#` 头注释：版本/模式信息一律放
# metadata.txt。这样 v2 快照被老镜像读到时，老 restore-drivers 仍能正常恢复文件级
# 部分（`#` 行会被它当成路径告警跳过），也不用改 driver-list 的 `wc -l` 计数和
# Go 侧以"manifest.txt 是否存在"为判据的 installed 逻辑。
cp /tmp/new-files.txt "${DRIVER_DATA}/manifest.txt"

# 恢复模式：有包有文件 = hybrid，只有包 = package，只有文件 = files
restore_mode="files"
if [ "${pkg_count}" -gt 0 ]; then
    if [ "${file_count}" -gt 0 ]; then
        restore_mode="hybrid"
    else
        restore_mode="package"
    fi
fi

pkg_bytes=0
if [ -d "${DRIVER_DATA}/packages" ]; then
    pkg_bytes="$(du -sb "${DRIVER_DATA}/packages" 2>/dev/null | awk '{print $1}')"
    [ -n "${pkg_bytes}" ] || pkg_bytes=0
fi

# Save install metadata
echo "driver=${DRIVER_NAME}" > "${DRIVER_DATA}/metadata.txt"
echo "installed_at=$(date -Iseconds)" >> "${DRIVER_DATA}/metadata.txt"
echo "file_count=${file_count}" >> "${DRIVER_DATA}/metadata.txt"
# arch 必须是 Debian 架构名（amd64 / arm64 / armhf）：driver-list 用它跟当前
# 架构做比较，Go 后端也会把它透给前端展示。用 detect_deb_arch 统一基准，避免
# 一边写 uname -m 的 aarch64、一边比 dpkg 的 arm64 造成误报"架构不一致"。
echo "arch=${DEB_ARCH}" >> "${DRIVER_DATA}/metadata.txt"
# v2 新增键。老镜像读不到这些键，只会走纯文件级恢复（向下兼容）。
echo "manifest_version=2" >> "${DRIVER_DATA}/metadata.txt"
echo "restore_mode=${restore_mode}" >> "${DRIVER_DATA}/metadata.txt"
echo "package_count=${pkg_count}" >> "${DRIVER_DATA}/metadata.txt"
echo "package_bytes=${pkg_bytes}" >> "${DRIVER_DATA}/metadata.txt"

# 快照体积告警：把决策权留给用户，不擅自丢弃归档。
if [ "${pkg_bytes}" -gt 209715200 ]; then
    log "WARNING: 本驱动的 .deb 归档占用 $((pkg_bytes / 1048576)) MB —— 多半是某个依赖把一整套"
    log "WARNING: 图形/桌面栈拖了进来。如果确认不需要，考虑在 install-${DRIVER_NAME}.sh 里"
    log "WARNING: 改用 'dpkg -i --force-depends' 而不是 'apt-get -f install'。"
fi

# --- Run ldconfig ---
log "Updating shared library cache..."
ldconfig 2>/dev/null || true

# --- Cleanup ---
# 临时文件与 apt 钩子由 EXIT trap（cleanup_tmp）统一清理，覆盖所有提前退出的分支。

log "Driver '${DRIVER_NAME}' installed successfully. (${file_count} files, ${pkg_count} package(s) persisted; restore_mode=${restore_mode})"
if [ "${pkg_missed}" -gt 0 ]; then
    log "WARNING: ${pkg_missed} 个包没拿到 .deb 原件，重启后这部分可能恢复不全。"
fi
