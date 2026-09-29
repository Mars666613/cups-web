#!/bin/bash
# 容器启动时把 .drivers 快照里的驱动文件恢复回系统路径。
#
# ⚠️ 故意**不使用 `set -e`**：本脚本是 entrypoint 的第一步，任何一次
# mkdir/cp 失败（快照被旧版本写坏、目标路径已被别的包占成目录、挂载只读……）
# 都不应该中断整个恢复流程，更不能让容器起不来——用户连 Web UI 都进不去时
# 就没法自救删掉坏驱动了。策略是"尽力而为 + 汇总报错"。
set -uo pipefail

DRIVERS_BASE="/opt/cups-drivers"
DATA_DIR="${DRIVERS_BASE}/data"

# ── 恢复白名单（安全网）─────────────────────────────────────────────────
# 跑过旧版本的用户手上已经存在被污染的快照：老 driver-install 把
# `dpkg -L build-essential` 之类编译依赖的**全部文件**写进了 manifest，
# 快照里因此躺着 /usr/bin/gcc、/usr/share/man/**、libc6-dev 的头文件与库。
# 无脑 `cp -a` 回去会用几个月前的旧二进制覆盖系统当前文件，属于实打实的破坏。
# 所以恢复前必须校验路径落在"驱动产物"目录内，不在的一律跳过并告警。
detect_multiarch_libdir() {
    local triplet="" d
    if command -v dpkg-architecture >/dev/null 2>&1; then
        triplet="$(dpkg-architecture -qDEB_HOST_MULTIARCH 2>/dev/null || true)"
    fi
    if [ -z "$triplet" ]; then
        # dpkg-architecture 属于 dpkg-dev 包，runtime 镜像没装；用 glob 探测。
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

_is_restorable_path() {
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
# 每次开机 restore 都用快照里几个月前的旧副本覆盖系统当前文件，属实打实的破坏。
# 按路径分不开，按**包属主**分得干干净净：镜像构建期把当时已安装的全部包名存进
# baseline-packages.txt（镜像层，不在挂载卷里），这里把这些包的 dpkg 文件清单
# 汇成索引，命中的一律只告警、不覆盖。
# ⚠️ 这份代码在 driver-install.sh / driver-remove.sh / restore-drivers.sh 三处
# 各有一份，与三份路径白名单同理——每处独立自洽，**不要**因为"别处已经拦过"就
# 删掉任何一份（restore 侧是专门给存量被污染快照兜底的）。
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
        echo "[restore-drivers] WARNING: 找不到 ${BASELINE_PKG_LIST}（旧镜像？），本次只做路径白名单校验。"
        return 0
    fi

    local idx
    if ! idx="$(mktemp /tmp/baseline-owned.XXXXXX 2>/dev/null)"; then
        BASELINE_INDEX_STATE="unavailable"
        echo "[restore-drivers] WARNING: 无法创建 baseline 索引临时文件，本次只做路径白名单校验。"
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
        echo "[restore-drivers] WARNING: baseline 包清单为空（dpkg 数据库异常？），本次只做路径白名单校验。"
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

# Exit silently if no driver data directory exists
if [ ! -d "${DATA_DIR}" ]; then
    exit 0
fi

# Check if there are any driver subdirectories
shopt -s nullglob
driver_dirs=("${DATA_DIR}"/*)
shopt -u nullglob

if [ ${#driver_dirs[@]} -eq 0 ]; then
    exit 0
fi

restored_count=0
restored_drivers=()
total_errors=0
total_skipped=0
total_pkg_ok=0
total_pkg_failed=0

# ── 包级恢复：把归档的 .deb 用 dpkg -i 装回去 ────────────────────────────
# 为什么放在 entrypoint 第一步跑 dpkg 是安全的、而且比放后面更好：此时 cupsd 还没
# 起来，厂商 postinst 里的 `invoke-rc.d cups restart`（都带 `|| true`）是无害 no-op；
# 等 cupsd 真正启动时 PPD/filter 已经就位，不需要二次 reload。
#
# 🚫 三条铁律（改这段前务必读完）：
#  ① `DEBIAN_FRONTEND=noninteractive` + `--force-confold`：conffile 冲突会让 dpkg
#     **交互式等输入**，而这里是 entrypoint 第一步 → 容器永久卡住起不来，比崩掉更糟。
#  ② `timeout` 包一层：驱动恢复是尽力而为，绝不允许无限阻塞容器启动。
#  ③ **永不在这里跑 `apt-get -f install`**：entrypoint 第一步不保证有网络、镜像的
#     /var/lib/apt/lists 通常是空的，而且 apt 在修依赖时会选择**删掉**装不上的厂商包
#     （install-canon-ufr2.sh 的注释里记录过这个实际表现）——比不修更糟。
#     依赖不满足就用 `--force-depends` 强行解包 + configure，让 postinst 跑起来
#     （konica 那个靠 postinst 现建的 filter 符号链接就指望这一步）。
_restore_driver_packages() {
    local driver_dir="$1" driver_name="$2"
    local pkg_dir="${driver_dir}/packages"
    local packages_file="${driver_dir}/packages.txt"
    local debs=() f pkg ver arch rel installed_ver
    local policy_rc_taken=0

    [ -d "$pkg_dir" ] || return 0

    # 逐包决定要不要装：
    #   - baseline 包（镜像自带）→ 跳过，绝不降级系统组件
    #   - 已安装且版本不更旧    → 跳过（幂等，支持一个容器生命周期内重复跑）
    if [ -f "$packages_file" ]; then
        while read -r pkg ver arch rel; do
            [ -n "$pkg" ] || continue
            if [ -f "${BASELINE_PKG_LIST}" ] && grep -qxF "$pkg" "${BASELINE_PKG_LIST}" 2>/dev/null; then
                echo "[restore-drivers]   skip ${pkg}（镜像自带包，不用快照覆盖）"
                continue
            fi
            installed_ver="$(dpkg-query -W -f '${Version}' "$pkg" 2>/dev/null || true)"
            if [ -n "$installed_ver" ] && \
               dpkg --compare-versions "$installed_ver" ge "$ver" 2>/dev/null; then
                echo "[restore-drivers]   skip ${pkg}=${ver}（已装 ${installed_ver}，不更旧）"
                continue
            fi
            if [ -f "${pkg_dir}/${rel}" ]; then
                debs+=("${pkg_dir}/${rel}")
            else
                echo "[restore-drivers]   WARNING: 归档缺失 ${rel}（${pkg}）"
                total_pkg_failed=$((total_pkg_failed + 1))
            fi
        done < "$packages_file"
    else
        # 没有 packages.txt 的情形：上传的 custom-deb（历史上只归档不记账），
        # 或快照被手工放进来的 .deb。按 glob 全部收养。
        shopt -s nullglob
        for f in "${pkg_dir}"/*.deb "${pkg_dir}"/*/*.deb; do
            debs+=("$f")
        done
        shopt -u nullglob
    fi

    [ ${#debs[@]} -gt 0 ] || return 0

    echo "[restore-drivers]   installing ${#debs[@]} archived .deb(s) for ${driver_name}"

    # 铁律②的另一半：某些厂商 postinst 会自己 `service cups start`，那会在 watchdog
    # 之外再起一个 cupsd 抢 631 端口，触发重启风暴（entrypoint.sh 里记录过同类事故）。
    # 临时 policy-rc.d 让所有 init 脚本调用直接返回 101（拒绝启动服务）。
    # 仅当该文件原本不存在时才接管，避免踩掉用户/基础镜像自己的设置。
    if [ ! -e /usr/sbin/policy-rc.d ]; then
        if printf '#!/bin/sh\nexit 101\n' > /usr/sbin/policy-rc.d 2>/dev/null; then
            chmod +x /usr/sbin/policy-rc.d 2>/dev/null || true
            policy_rc_taken=1
        fi
    fi

    # 同一驱动的所有 deb 放进**一次** dpkg -i 调用：dpkg 自己会排序并统一 configure，
    # 包内相互依赖的顺序问题因此自动消失。
    if DEBIAN_FRONTEND=noninteractive timeout 600 \
         dpkg -i --force-confold "${debs[@]}" >/dev/null 2>&1; then
        total_pkg_ok=$((total_pkg_ok + ${#debs[@]}))
    else
        echo "[restore-drivers]   dpkg -i 报依赖问题，改用 --force-depends 重试"
        if DEBIAN_FRONTEND=noninteractive timeout 600 \
             dpkg -i --force-confold --force-depends "${debs[@]}" >/dev/null 2>&1; then
            total_pkg_ok=$((total_pkg_ok + ${#debs[@]}))
        else
            echo "[restore-drivers]   WARNING: ${driver_name} 的包级恢复失败（见上方 dpkg 输出）"
            total_pkg_failed=$((total_pkg_failed + ${#debs[@]}))
        fi
        # 收尾扫一遍：把 half-configured 的包尽量配置起来（postinst 要跑）
        DEBIAN_FRONTEND=noninteractive dpkg --configure -a --force-depends >/dev/null 2>&1 || true
    fi

    if [ "$policy_rc_taken" = "1" ]; then
        rm -f /usr/sbin/policy-rc.d
    fi
    return 0
}

for driver_dir in "${driver_dirs[@]}"; do
    [ -d "$driver_dir" ] || continue

    driver_name="$(basename "$driver_dir")"
    manifest="${driver_dir}/manifest.txt"

    # 处理门槛：manifest.txt / packages.txt / packages 目录 任一存在即视为有效快照。
    # 放宽这一条是为了收养历史上"只归档 .deb 不写 manifest"的 custom-deb 上传包
    # （以前它们重启后需要用户手动重装，现在能自动装回来）。
    if [ ! -f "$manifest" ] && [ ! -f "${driver_dir}/packages.txt" ] && \
       [ ! -d "${driver_dir}/packages" ]; then
        continue
    fi

    echo "[restore-drivers] Restoring driver: ${driver_name}"
    file_count=0
    missing_count=0
    error_count=0
    skipped_count=0

    # 先包级、后文件级：文件级条目的语义是"覆盖/补充"，必须后落才能生效。
    _restore_driver_packages "$driver_dir" "$driver_name"

    if [ ! -f "$manifest" ]; then
        restored_count=$((restored_count + 1))
        restored_drivers+=("$driver_name")
        continue
    fi

    _mark_baseline_hits "$manifest"

    while IFS= read -r filepath; do
        [ -z "$filepath" ] && continue

        # 安全网①：老快照可能包含系统路径（见文件头注释），拒绝恢复以免覆盖系统文件。
        if ! _is_restorable_path "$filepath"; then
            echo "[restore-drivers]   WARNING: refusing to restore non-driver path: ${filepath}"
            skipped_count=$((skipped_count + 1))
            continue
        fi

        # 安全网②：路径虽在白名单内，但这个文件属于**镜像自带包**（典型是 multiarch
        # 目录下被 apt 依赖解析拖进来的系统库）→ 绝不用旧副本覆盖，见文件头注释。
        if _is_baseline_owned "$filepath"; then
            echo "[restore-drivers]   WARNING: refusing to overwrite baseline (image-owned) file: ${filepath}"
            skipped_count=$((skipped_count + 1))
            continue
        fi

        source_file="${driver_dir}${filepath}"

        # 同 driver-install：指向目录的符号链接在 `-f` 下判假，必须用 -e/-L 兜住。
        if ! { [ -e "$source_file" ] || [ -L "$source_file" ]; }; then
            missing_count=$((missing_count + 1))
            continue
        fi

        # Create parent directory if needed（失败只记账，继续下一个文件）
        parent_dir="$(dirname "$filepath")"
        if [ ! -d "$parent_dir" ]; then
            if ! mkdir -p "$parent_dir" 2>/dev/null; then
                echo "[restore-drivers]   WARNING: mkdir failed: ${parent_dir}"
                error_count=$((error_count + 1))
                continue
            fi
        fi

        # Copy preserving permissions, ownership, and timestamps
        # ⚠️ 必须带 `-T`（--no-target-directory）。当目标已存在、且它本身是一个
        # **指向目录的符号链接**时，cp 会先跟随该链接、判定"目标是目录"，于是把源
        # 拷进那个目录里去，而不是替换链接 —— 既没恢复对，还在别人的目录里多留一个
        # 文件。实测 `--remove-destination` 对这种情况**无效**（它只处理"目标是普通
        # 文件"），只有 `-T` 能强制把目标当作路径本身。两个一起用最稳。
        # 副作用是目标若为**真目录**则 cp 失败 —— 那正是我们想要的：manifest 条目
        # 本就不该是目录，失败会被下面计入 error_count 并告警。
        if cp -aT --remove-destination "$source_file" "$filepath" 2>/dev/null; then
            file_count=$((file_count + 1))
        else
            echo "[restore-drivers]   WARNING: copy failed: ${filepath}"
            error_count=$((error_count + 1))
        fi
    done < "$manifest"

    echo "[restore-drivers]   Restored ${file_count} files"
    if [ "$missing_count" -gt 0 ]; then
        echo "[restore-drivers]   WARNING: ${missing_count} files missing from backup"
    fi
    if [ "$skipped_count" -gt 0 ]; then
        # 两类跳过都计在这里：① 路径不在驱动产物白名单内；② 路径在白名单内但文件
        # 属于镜像自带包（baseline）。逐条的具体原因已在上面的 WARNING 里打过。
        echo "[restore-drivers]   WARNING: ${skipped_count} files skipped (not driver-owned)"
    fi
    if [ "$error_count" -gt 0 ]; then
        echo "[restore-drivers]   WARNING: ${error_count} files failed to restore"
    fi

    total_errors=$((total_errors + error_count))
    total_skipped=$((total_skipped + skipped_count))
    restored_count=$((restored_count + 1))
    restored_drivers+=("$driver_name")
done

# Update shared library cache if any drivers were restored
if [ "$restored_count" -gt 0 ]; then
    echo "[restore-drivers] Updating shared library cache..."
    ldconfig 2>/dev/null || true
    echo "[restore-drivers] Restored ${restored_count} driver(s): ${restored_drivers[*]}"
fi

if [ "$total_pkg_ok" -gt 0 ] || [ "$total_pkg_failed" -gt 0 ]; then
    echo "[restore-drivers] Packages: ${total_pkg_ok} installed, ${total_pkg_failed} failed."
fi

if [ "$total_errors" -gt 0 ] || [ "$total_skipped" -gt 0 ]; then
    echo "[restore-drivers] Summary: ${total_errors} file(s) failed, ${total_skipped} file(s) skipped (not driver-owned)."
    echo "[restore-drivers] 驱动恢复不完整，但不阻塞容器启动；可在 Web UI 里卸载后重新安装该驱动。"
fi

# 始终以 0 退出：恢复是尽力而为的，失败已在上面汇总打印，绝不阻塞 entrypoint。
exit 0
