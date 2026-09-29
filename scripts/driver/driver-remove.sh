#!/bin/bash
set -eo pipefail

DRIVERS_BASE="/opt/cups-drivers"
DATA_DIR="${DRIVERS_BASE}/data"

log() {
    echo "[driver-remove] $*"
}

# ── 删除白名单（安全网）─────────────────────────────────────────────────
# 即使 driver-install 已经把 manifest 过滤到驱动产物路径，这里也要**再校验
# 一遍**：跑过旧版本的用户手上已经存在被污染的 .drivers 快照（老 driver-install
# 把 `dpkg -L build-essential` 之类的全部文件写进了 manifest），直接按老
# manifest 逐条 rm 会删掉 /usr/bin/gcc、/usr/share/man/**、libc6-dev 的库，
# 把容器搞残。不在白名单里的路径一律**跳过并告警，绝不 rm**。
detect_multiarch_libdir() {
    local triplet="" d
    if command -v dpkg-architecture >/dev/null 2>&1; then
        triplet="$(dpkg-architecture -qDEB_HOST_MULTIARCH 2>/dev/null || true)"
    fi
    if [ -z "$triplet" ]; then
        # dpkg-architecture 属于 dpkg-dev，runtime 镜像没有；用 glob 探测。
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

MULTIARCH_LIBDIR="$(detect_multiarch_libdir)"

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

_is_removable_path() {
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

# ── baseline 归属守卫（第二道安全网）─────────────────────────────────────
# 为什么路径白名单不够：白名单按**路径前缀**判断，而 multiarch 目录
# （/usr/lib/<triplet>）既是驱动共享库的家、也是系统库的家。老版本的
# install-epson-cn.sh 用 `apt-get -f install` 兜底依赖，会把整套 Qt5/X11/GL
# 拖进容器，这些 .so 正好落在白名单**内** → 被当成驱动产物写进 manifest →
# `driver-remove epson-cn` 按 manifest 逐条 rm 就把系统库删了（restore 时还会
# 用几个月前的旧副本覆盖回去）。
# 按路径分不开，按**包属主**分得干干净净：镜像构建期把当时已安装的全部包名存进
# baseline-packages.txt（镜像层，不在挂载卷里），这里把这些包的 dpkg 文件清单
# 汇成索引，命中的一律只告警、不删。
# ⚠️ 这份代码在 driver-install.sh / driver-remove.sh / restore-drivers.sh 三处
# 各有一份，与三份路径白名单同理——每处独立自洽，**不要**因为"别处已经拦过"就
# 删掉任何一份（remove/restore 侧是专门给存量被污染快照兜底的）。
BASELINE_PKG_LIST="/opt/cups-drivers/baseline-packages.txt"
BASELINE_OWNED_INDEX=""
BASELINE_INDEX_STATE="none"   # none | ready | unavailable
declare -A BASELINE_HITS=()

# 本脚本**唯一**的 EXIT trap（bash 对同一信号只保留最后注册的 handler）。
# 后人新增清理动作请写进这个函数，不要再 trap 一次。
_baseline_cleanup() {
    if [ -n "${BASELINE_OWNED_INDEX}" ]; then
        rm -f "${BASELINE_OWNED_INDEX}"
    fi
    return 0
}
trap _baseline_cleanup EXIT

_load_baseline_owned_index() {
    [ "${BASELINE_INDEX_STATE}" = "none" ] || return 0

    if [ ! -f "${BASELINE_PKG_LIST}" ]; then
        BASELINE_INDEX_STATE="unavailable"
        log "WARNING: 找不到 ${BASELINE_PKG_LIST}（旧镜像？），本次只做路径白名单校验。"
        return 0
    fi

    local idx
    if ! idx="$(mktemp /tmp/baseline-owned.XXXXXX 2>/dev/null)"; then
        BASELINE_INDEX_STATE="unavailable"
        log "WARNING: 无法创建 baseline 索引临时文件，本次只做路径白名单校验。"
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
        log "WARNING: baseline 包清单为空（dpkg 数据库异常？），本次只做路径白名单校验。"
        return 0
    fi

    cat "${lists[@]}" 2>/dev/null | sort -u > "$idx"
    BASELINE_INDEX_STATE="ready"
    return 0
}

# 预计算 manifest ∩ baseline-owned：一次 comm 求交集，之后逐条查是 O(1)。
# 比"每条 manifest 记录都 grep 一遍十几万行的索引"快几个数量级。
_mark_baseline_hits() {
    local manifest="$1" line
    BASELINE_HITS=()
    _load_baseline_owned_index
    [ "${BASELINE_INDEX_STATE}" = "ready" ] || return 0
    while IFS= read -r line; do
        [ -n "$line" ] && BASELINE_HITS["$line"]=1
    done < <(sort -u "$manifest" | comm -12 - "${BASELINE_OWNED_INDEX}")
    return 0
}

_is_baseline_owned() {
    [ -n "${BASELINE_HITS[$1]:-}" ]
}

if [ -z "$1" ]; then
    echo "Usage: driver-remove <driver-name>"
    echo ""
    echo "Remove an installed printer driver."
    echo ""
    echo "Installed drivers:"
    if [ -d "${DATA_DIR}" ]; then
        found=false
        for driver_dir in "${DATA_DIR}"/*/; do
            [ -d "$driver_dir" ] || continue
            [ -f "${driver_dir}manifest.txt" ] || continue
            found=true
            echo "  $(basename "$driver_dir")"
        done
        if ! $found; then
            echo "  (none)"
        fi
    else
        echo "  (none)"
    fi
    exit 1
fi

DRIVER_NAME="$1"
DRIVER_DATA="${DATA_DIR}/${DRIVER_NAME}"
MANIFEST="${DRIVER_DATA}/manifest.txt"

if [ ! -f "${MANIFEST}" ]; then
    log "ERROR: Driver '${DRIVER_NAME}' is not installed."
    log "No manifest found at: ${MANIFEST}"
    exit 1
fi

log "Removing driver '${DRIVER_NAME}'..."

# ── 包级卸载：先按 dpkg 包边界 purge，再处理文件级散落条目 ─────────────────
# 为什么按包 purge 比按 manifest 逐条 rm 正确得多：删哪些文件由 dpkg 自己的 .list
# 决定，天然精确，不会漏（/opt、/usr/bin、/usr/share/<vendor> 都覆盖到）、也不会
# 误伤别的包共享的文件。
PACKAGES_FILE="${DRIVER_DATA}/packages.txt"
pkg_purged=0
pkg_kept=0

if [ -f "${PACKAGES_FILE}" ]; then
    purge_list=()
    while read -r pkg ver arch rel; do
        [ -n "$pkg" ] || continue

        # 绝不 purge 镜像自带包。
        if [ -f "${BASELINE_PKG_LIST}" ] && grep -qxF "$pkg" "${BASELINE_PKG_LIST}" 2>/dev/null; then
            log "  keep ${pkg}（镜像自带包）"
            pkg_kept=$((pkg_kept + 1))
            continue
        fi

        # 绝不 purge 别的已安装驱动也记录了的包（跨驱动共享依赖）。
        shared=0
        shopt -s nullglob
        for other in "${DATA_DIR}"/*/packages.txt; do
            [ "$other" = "${PACKAGES_FILE}" ] && continue
            if awk '{print $1}' "$other" 2>/dev/null | grep -qxF "$pkg"; then
                shared=1
                log "  keep ${pkg}（$(basename "$(dirname "$other")") 也在用）"
                break
            fi
        done
        shopt -u nullglob
        if [ "$shared" = "1" ]; then
            pkg_kept=$((pkg_kept + 1))
            continue
        fi

        dpkg-query -W "$pkg" >/dev/null 2>&1 || continue
        purge_list+=("$pkg")
    done < "${PACKAGES_FILE}"

    if [ ${#purge_list[@]} -gt 0 ]; then
        log "Purging ${#purge_list[@]} package(s): ${purge_list[*]}"
        # 先 dry-run 确认 apt 不会顺手动到集合外的包（apt 的启发式有时会为了"修复"
        # 依赖而删掉别的东西）。dry-run 通不过就退回 dpkg -P：确定性、离线、
        # 不受 apt 启发式影响。
        extra=""
        if apt_plan="$(DEBIAN_FRONTEND=noninteractive apt-get purge -y -s "${purge_list[@]}" 2>/dev/null)"; then
            extra="$(echo "$apt_plan" | awk '/^Remv /{print $2}' | grep -vxF -f <(printf '%s\n' "${purge_list[@]}") || true)"
        else
            extra="APT_PLAN_FAILED"
        fi

        if [ -z "$extra" ]; then
            if DEBIAN_FRONTEND=noninteractive apt-get purge -y "${purge_list[@]}" >/dev/null 2>&1; then
                pkg_purged=${#purge_list[@]}
            else
                log "  WARNING: apt-get purge 失败，退回 dpkg -P --force-depends"
                DEBIAN_FRONTEND=noninteractive dpkg -P --force-depends "${purge_list[@]}" >/dev/null 2>&1 \
                    && pkg_purged=${#purge_list[@]} \
                    || log "  WARNING: dpkg -P 也失败了，这些包留在系统里（不影响其它驱动）"
            fi
        else
            if [ "$extra" = "APT_PLAN_FAILED" ]; then
                log "  apt-get purge 试运行失败，改用 dpkg -P --force-depends（确定性卸载）"
            else
                log "  apt 试运行显示会波及集合外的包，放弃 apt 改用 dpkg -P --force-depends："
                log "  （会被 apt 顺手删掉的：$(echo "$extra" | tr '\n' ' ')）"
            fi
            DEBIAN_FRONTEND=noninteractive dpkg -P --force-depends "${purge_list[@]}" >/dev/null 2>&1 \
                && pkg_purged=${#purge_list[@]} \
                || log "  WARNING: dpkg -P 失败，这些包留在系统里（不影响其它驱动）"
        fi
    fi
fi

# Remove files listed in the manifest from system paths
removed_count=0
missing_count=0
skipped_count=0
failed_count=0

_mark_baseline_hits "${MANIFEST}"

while IFS= read -r filepath; do
    [ -z "$filepath" ] && continue

    # 安全网①：manifest 里出现非驱动产物路径（老版本污染的快照）时只告警不删。
    if ! _is_removable_path "$filepath"; then
        log "  WARNING: refusing to remove non-driver path: ${filepath}"
        skipped_count=$((skipped_count + 1))
        continue
    fi

    # 安全网②：路径虽在白名单内，但这个文件属于**镜像自带包**（典型是 multiarch
    # 目录下被 apt 依赖解析拖进来的系统库）→ 绝不删，见文件头 baseline 守卫注释。
    if _is_baseline_owned "$filepath"; then
        log "  WARNING: refusing to remove baseline (image-owned) file: ${filepath}"
        skipped_count=$((skipped_count + 1))
        continue
    fi

    # ⚠️ 必须带 `-L`：符号链接在 `-f` 下可能判假（指向目录时，或链接已悬空时），
    # 那样它永远删不掉、只被算进 missing_count，卸载后留一个悬空链接在
    # /usr/lib/cups/filter/ 里，CUPS 每次枚举都会踩到它。
    if [ -f "$filepath" ] || [ -L "$filepath" ]; then
        if rm -f "$filepath"; then
            removed_count=$((removed_count + 1))
        else
            log "  WARNING: failed to remove ${filepath}"
            failed_count=$((failed_count + 1))
        fi
    else
        missing_count=$((missing_count + 1))
    fi
done < "${MANIFEST}"

# Clean up empty directories left behind in monitored paths
for dir in /usr/lib/cups /usr/share/cups /usr/share/ppd /lib/firmware /usr/share/foomatic; do
    if [ -d "$dir" ]; then
        find "$dir" -type d -empty -delete 2>/dev/null || true
    fi
done

# Remove the driver data directory
log "Removing persisted driver data..."
rm -rf "${DRIVER_DATA}"

# Update shared library cache
log "Updating shared library cache..."
ldconfig 2>/dev/null || true

log "Driver '${DRIVER_NAME}' removed successfully."
if [ "${pkg_purged}" -gt 0 ]; then
    log "  Packages purged: ${pkg_purged}"
fi
if [ "${pkg_kept}" -gt 0 ]; then
    log "  Packages kept (image-owned or shared with another driver): ${pkg_kept}"
fi
log "  Files removed: ${removed_count}"
if [ $missing_count -gt 0 ]; then
    log "  Files already missing: ${missing_count}"
fi
if [ $skipped_count -gt 0 ]; then
    # 两类跳过都计在这里：① 路径不在驱动产物白名单内；② 路径在白名单内但文件
    # 属于镜像自带包（baseline）。逐条的具体原因已在上面的 WARNING 里打过。
    log "  Files skipped (not driver-owned, kept for safety): ${skipped_count}"
fi
if [ $failed_count -gt 0 ]; then
    log "  Files failed to remove: ${failed_count}"
fi
