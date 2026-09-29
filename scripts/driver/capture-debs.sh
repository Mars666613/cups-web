#!/bin/bash
# apt 的 DPkg::Pre-Install-Pkgs 钩子：把 apt 即将安装的 .deb 原件归档一份，
# 供 driver-install 做「包级驱动持久化」。
#
# ── 为什么需要它 ────────────────────────────────────────────────────────
# 驱动快照原本只按**文件路径白名单**收集产物，但厂商 deb 普遍把东西装在
# /opt/<vendor>/、/usr/bin、/usr/share/<vendor>/ 这些白名单外的位置（Epson
# escpr2 的 298 个文件全在 /opt，Canon UFR II 的渲染引擎在 /usr/bin），
# 于是快照要么是空的（driver-install 判失败）、要么少了关键件（重启后驱动残废）。
# 直接归档 .deb 原件、重启时 dpkg -i 装回去，产物天然完整，也不用为每个新驱动
# 维护白名单。
#
# 为什么用 apt 钩子而不是事后 `apt-get download`：钩子拿到的是 apt **真正要装的
# 那批 deb 文件本身**（含全部传递依赖），发生在任何 apt-get clean 之前，不需要
# 重新下载、不需要猜版本、也不依赖 apt 索引还在。
#
# ── 三条铁律（改这个文件前务必读完）──────────────────────────────────────
# 🚫 ① **必须永远 exit 0。** DPkg::Pre-Install-Pkgs 返回非零会让 apt 中止**整个
#      安装事务**。一个归档辅助脚本绝不能有能力弄挂驱动安装——归档失败最多让这次
#      快照不完整（driver-install 还有 /var/cache/apt 打捞和 apt-get download 两层
#      兜底），但绝不能连驱动都装不上。
# 🚫 ② 目标目录从 /run/cups-drivers/capture-target 读，**文件不存在就静默退出**。
#      这样即使 driver-install 被 SIGKILL、apt drop-in 残留在
#      /etc/apt/apt.conf.d/ 里，后续任何 apt 操作也只是白跑一次钩子，不会往
#      随机目录里拷东西。
# 🚫 ③ stdin 里不保证全是 .deb 路径（移除操作会给出别的东西），只处理 *.deb
#      且确实存在的普通文件。
#
# 协议细节：不声明 DPkg::Tools::options::<cmd>::Version 时用的是最简协议
# （version 0）——apt 通过 stdin 一行一个地给出即将安装的 .deb 绝对路径。

TARGET_FILE="/run/cups-drivers/capture-target"

# 铁律②：没有 target 就什么都不做。
[ -f "${TARGET_FILE}" ] || exit 0

TARGET_DIR="$(cat "${TARGET_FILE}" 2>/dev/null)"
if [ -z "${TARGET_DIR}" ] || [ ! -d "${TARGET_DIR}" ]; then
    exit 0
fi

captured=0
while IFS= read -r line; do
    # 铁律③：只认真实存在的 .deb 文件。
    case "$line" in
        *.deb) ;;
        *) continue ;;
    esac
    [ -f "$line" ] || continue

    if cp -a "$line" "${TARGET_DIR}/" 2>/dev/null; then
        captured=$((captured + 1))
    fi
done

if [ "${captured}" -gt 0 ]; then
    echo "[capture-debs] archived ${captured} .deb file(s) for driver persistence"
fi

# 铁律①：无论上面发生了什么，永远成功退出。
exit 0
