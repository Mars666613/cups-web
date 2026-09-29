#!/usr/bin/env bash
# Epson 国行私有驱动：仅 amd64 best-effort 安装。
#
# `epson-inkjet-printer-201601w` 与 `epson-printer-utility` 是 Epson 中国区
# 发布的**闭源专有** .deb 包（无源码，无 arm64/armhf 二进制），覆盖 L380/L455
# 等国行早期喷墨机型。对应功能大部分可以被 Debian 自带的 `printer-driver-escpr`
# 覆盖，但原厂 PPD 在墨水检测、尺寸预设等细节上更完整。
#
# ⚠️ 原下载源 download-center.epson.com.cn 的 UUID 会定期轮换导致 URL 失效，
# 因此把 .deb 镜像到本仓库的 GitHub Releases（cups-driver tag）。
# 此处采用 **fail-fast**：下载 / dpkg 任一步失败则脚本立刻中断，
# 避免发布镜像里缺少国行驱动却静默成功。arm64/armhf 在脚本入口直接退出，
# 不受影响。
# 升级方法：把新版 .deb 上传到 https://github.com/hanxi/cups-web/releases 的
# cups-driver tag，更新下方 DEB 变量即可。

set -eo pipefail

# 仅 amd64 安装。
# ── 退出码约定（全部 install-*.sh 共同遵守）───────────────────────────
#   0 = 安装成功
#   3 = 当前 CPU 架构不支持该驱动（厂商未提供该架构二进制）
#   其他非零 = 真正的失败（下载 / dpkg / 编译失败等）
# 这里必须用 3 而**不是** 0：driver-install 对退出码 0 会照常写 manifest.txt，
# Web UI 于是显示"已安装"，用户以为驱动可用（实际什么都没装）。用 3 让上层
# 能明确区分"本架构不支持"和"真失败"。
ARCH="$(dpkg --print-architecture)"
if [ "${ARCH}" != "amd64" ]; then
    echo "[epson-cn] unsupported arch=${ARCH} (only amd64 supported)"
    exit 3
fi

# ────────────────────────────────────────────────────────────────────
# 配置
# ────────────────────────────────────────────────────────────────────
EPSON_PROP_DRIVER_DEB="epson-inkjet-printer-201601w_1.0.1-1_amd64.deb"
EPSON_PROP_UTILITY_DEB="epson-printer-utility_1.2.2-1_amd64.deb"
EPSON_PROP_UA="Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

EPSON_DRV_URL="https://github.com/hanxi/cups-web/releases/download/cups-driver/${EPSON_PROP_DRIVER_DEB}"
EPSON_UTIL_URL="https://github.com/hanxi/cups-web/releases/download/cups-driver/${EPSON_PROP_UTILITY_DEB}"

# ────────────────────────────────────────────────────────────────────
# 下载 & dpkg
# ────────────────────────────────────────────────────────────────────
BUILD_DIR="$(mktemp -d /tmp/epson-cn.XXXXXX)"
trap 'rm -rf "${BUILD_DIR}"' EXIT

cd "${BUILD_DIR}"

echo "[epson-cn] downloading ${EPSON_DRV_URL}"
wget --tries=3 --timeout=60 --retry-connrefused \
     --user-agent="${EPSON_PROP_UA}" \
     -O "${EPSON_PROP_DRIVER_DEB}" "${EPSON_DRV_URL}"

echo "[epson-cn] downloading ${EPSON_UTIL_URL}"
wget --tries=3 --timeout=60 --retry-connrefused \
     --user-agent="${EPSON_PROP_UA}" \
     -O "${EPSON_PROP_UTILITY_DEB}" "${EPSON_UTIL_URL}"

# ── 把 .deb 原件交给 driver-install 归档（包级持久化）────────────────────
# 本脚本的临时目录会被 EXIT trap 删掉，不交接的话重启后无从重装。而驱动本体的
# 26 个文件（filter / libEpson_201601w.so / PPD / .data 资源 / 水印 EID）**全部**
# 装在 /opt/epson-inkjet-printer-201601w/ 下，文件级快照只能抓到 utility 包提供的
# 那一个 /usr/lib/cups/backend/ecblp —— 归档 .deb 才能完整恢复。
# 故意用 `|| true`：归档失败绝不影响安装成败判定与退出码语义（0/3/其他）。
if [ -n "${DRIVER_PKG_DIR:-}" ]; then
    cp -a "${EPSON_PROP_DRIVER_DEB}" "${EPSON_PROP_UTILITY_DEB}" "${DRIVER_PKG_DIR}/" 2>/dev/null || true
fi

# ⚠️ 这里**故意不用 `apt-get -f install` 兜底依赖**（老实现用过，是个大坑）：
#
# `epson-printer-utility` 是一个 **Qt5 GUI 工具**，声明依赖 libqt5core5a /
# libqt5gui5 / libqt5widgets5 / libgcc1 —— 这些名字在 trixie 上都已 t64 改名，
# 但改名包声明了 Provides，所以 apt "能"满足它们：于是 `apt-get -f install` 会把
# 整套 Qt5 + X11/GL 栈（libQt5Core/Gui/Widgets/DBus/Network、libicu、libinput、
# libxcb-* 等上百 MB）装进这个**无头容器**。后果有两层：
#   ① 这些 .so 落在 /usr/lib/<triplet>，正好在 driver-install 的路径白名单**内**
#      → 被当成"驱动产物"写进 epson-cn 的 manifest → `driver-remove epson-cn`
#      会逐条 rm 掉系统库，restore 时还会用旧副本覆盖回去（实打实的破坏）；
#   ② 快照体积暴涨上百 MB，restore 时 dpkg 要跑几十秒。
#
# 我们要的只是打印链路：驱动本体（filter/PPD/.so/资源）+ `epson-printer-utility`
# 里的 CUPS backend `/usr/lib/cups/backend/ecblp` 与 `/usr/lib/epson-backend/ecbd`。
# 这些都不需要 Qt5——Qt5 只是那个图形界面程序的依赖。所以用 `--force-depends`
# 解包并 configure 两个包，把未满足的 GUI 依赖留在未满足状态即可。
# （同样的结论 install-canon-ufr2.sh 也踩过：apt 修依赖时会选择**删掉**装不上的
# 厂商包，比不修更糟。）
if ! dpkg -i --force-depends ./*.deb; then
    echo "[epson-cn] ERROR: dpkg -i --force-depends failed"
    exit 1
fi

echo "[epson-cn] installed Epson CN proprietary driver + utility (GUI deps intentionally unmet)"
# 只在构建期（非 AIO）清 apt 索引省镜像体积。
# ⚠️ 在运行中的容器里清空 /var/lib/apt/lists 会让**后续安装的其他驱动**因为
# 没有包索引而 apt-get install 失败（"连续装两个驱动"直接翻车）。
if [ "${CUPS_AIO:-0}" != "1" ]; then
    rm -rf /var/lib/apt/lists/*
fi
