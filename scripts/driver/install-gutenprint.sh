#!/usr/bin/env bash
# 安装 printer-driver-gutenprint：仅 amd64/arm64 上安装。
#
# printer-driver-gutenprint 在 trixie armhf 上没有 binary 包（gutenprint 的
# 链接依赖 libgutenprint9 未完成 t64 迁移），仅在 amd64/arm64 上安装。
# armhf 用户仍可通过 printer-driver-all 推荐的其他驱动覆盖大部分打印机。

set -eux

# ── 退出码约定（全部 install-*.sh 共同遵守）───────────────────────────
#   0 = 安装成功
#   3 = 当前 CPU 架构不支持该驱动（无对应架构的二进制包）
#   其他非零 = 真正的失败
# 这里必须用 3 而**不是** 0：driver-install 对退出码 0 会照常写 manifest.txt，
# Web UI 于是显示"已安装"，用户以为驱动可用（实际什么都没装）。
ARCH="$(dpkg --print-architecture)"
if [ "${ARCH}" = "armhf" ] || [ "${ARCH}" = "armel" ]; then
    echo "[gutenprint] unsupported arch=${ARCH} (no binary package on trixie)"
    exit 3
fi

apt-get update

# ── 为什么不直接 `apt-get install printer-driver-gutenprint` ──────────────
# 它硬依赖 `cups` **元包**（Depends: cups, cups-client, cups-filters | ...），而本镜像
# 的 CUPS 是**源码编译**的（scripts/build/install-cups.sh，装进 /usr 后由 Dockerfile
# 的 overlay tar 解包），apt 侧只装了 cups-daemon / cups-client / cups-filters，
# 故意没有 `cups` 元包。
# 一旦让 apt 去满足这个依赖，它就会连带装上 Debian 的 `cups-core-drivers`（还有
# cups-ppdc / cups 元包），而 cups-core-drivers 提供的正是
# /usr/lib/cups/backend/{usb,socket,lpd,dnssd,snmp,mdns} 与一批 filter ——
# **直接覆盖源码编译的同名文件**，让 2.4.19 与 Debian 2.4.10 的组件混用。
# 更糟的是这些文件会被包级快照归档，于是每次容器启动 restore 都重新覆盖一遍。
#
# 正解：只用 apt 装真正的库依赖（libgutenprint9 会带上 libgutenprint-common，
# 那里面是 370 多个机型定义 XML，缺了 lpinfo -m 一个机型都列不出来），驱动包本身
# 下载下来用 `dpkg -i --force-depends` 装，跳过 `cups` 元包这个空壳。
# gutenprint 的 filter 实际只链接 libcups/libcupsimage/libgutenprint，元包对它毫无
# 意义 —— 这与 install-canon-ufr2.sh / install-epson-cn.sh 已验证的同一套思路。
apt-get install -y --no-install-recommends libgutenprint9 libgutenprint-common

GP_DL_DIR="$(mktemp -d /tmp/gutenprint-dl.XXXXXX)"
if ! ( cd "${GP_DL_DIR}" && apt-get download printer-driver-gutenprint ); then
    echo "[gutenprint] ERROR: apt-get download printer-driver-gutenprint failed"
    rm -rf "${GP_DL_DIR}"
    exit 1
fi

# 把 .deb 原件交给 driver-install 归档（包级持久化）。故意 `|| true`：归档失败
# 不影响安装成败与退出码语义。
if [ -n "${DRIVER_PKG_DIR:-}" ]; then
    cp -a "${GP_DL_DIR}"/*.deb "${DRIVER_PKG_DIR}/" 2>/dev/null || true
fi

dpkg -i --force-depends "${GP_DL_DIR}"/printer-driver-gutenprint_*.deb
rm -rf "${GP_DL_DIR}"

# 验证关键产物确实落盘（PPD 生成器 + filter + 机型数据）
for f in /usr/lib/cups/driver/gutenprint.5.3 /usr/lib/cups/filter/rastertogutenprint.5.3; do
    if [ ! -f "$f" ]; then
        echo "[gutenprint] FATAL: $f not found after install"
        exit 1
    fi
done
if [ ! -d /usr/share/gutenprint/5.3/xml ]; then
    echo "[gutenprint] FATAL: /usr/share/gutenprint/5.3/xml missing (libgutenprint-common 没装上?)"
    exit 1
fi

apt-get clean
# 只在构建期（非 AIO）清 apt 索引省镜像体积。
# ⚠️ 在运行中的容器里清空 /var/lib/apt/lists 会让**后续安装的其他驱动**因为
# 没有包索引而 apt-get install 失败（"连续装两个驱动"直接翻车）。
if [ "${CUPS_AIO:-0}" != "1" ]; then
    rm -rf /var/lib/apt/lists/*
fi

echo "[gutenprint] installed"
