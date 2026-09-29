#!/usr/bin/env bash
# Canon UFR II / UFRII LT 官方驱动：amd64 + arm64 best-effort 安装（issue #34）。
#
# 覆盖机型范围：i-SENSYS LBP / MF 系列、imageCLASS、imageRUNNER (iR) /
# imagePRESS (iPR) 等所有走 UFR II / UFRII LT 协议的 Canon 激光机；新款
# LBP 墨彩机型即便支持 driverless (IPP Everywhere)，装上原厂 PPD 后双面/
# 分页/纸盒等高级选项才会齐全。
#
# ────────────────────────────────────────────────────────────────────
# 架构覆盖说明
# ────────────────────────────────────────────────────────────────────
# Canon 官方 tarball（linux-UFRII-drv-vXXX-m17n-NN.tar.gz）从 v6.30 起
# 解压后按架构分目录：
#   x64/Debian/cnrdrvcups-ufr2-uk_<ver>_amd64.deb   → amd64
#   ARM64/Debian/cnrdrvcups-ufr2-uk_<ver>_arm64.deb → arm64
#   x86/Debian/cnrdrvcups-ufr2-uk_<ver>_i386.deb    → i386（Debian 已弃，不用）
#   MIPS64/                                           → 龙芯/MIPS（本镜像不覆盖）
# 没有任何 32-bit ARM (armhf/armel) 二进制；社区 vicamo/cndrvcups-lb 也
# 只能在 x86/arm64 上 make——根因是核心 filter（`libcnpkbidir*.so` 等）
# 是 Canon 不公开源码的闭源 .so。所以 armhf/armel 直接 skip，避免误导。
#
# 注意包名是 `cnrdrvcups-ufr2-uk`（cnrdrvcups，r 在 d 后），不是早期文档/
# AUR 里写的 `cndrvcups-ufr2-*`——v6.30 的 Debian 包合并了原 cndrvcups-common
# 与 cndrvcups-ufr2-uk 两个包，单一 .deb 即可。
#
# ────────────────────────────────────────────────────────────────────
# 下载策略
# ────────────────────────────────────────────────────────────────────
# Canon 官方下载点 gdlp01.c-wss.com 是 CloudFront/AWS S3 后端，UA 检查
# 不严但偶有 4xx；URL 里的 GDS 路径（/gds/0/0100009240/40/）跟随版本号
# 变化，升级时需要去 Canon 各国家区下载页（如 https://asia.canon/en/support/0100924010）
# 的 "Download" 按钮里抓最新 URL（点击后浏览器抓 redirect 即可）。
# fail-fast：下载或 dpkg 任一步失败立即非零退出，避免发布镜像里缺少
# UFR II 驱动却静默成功（与 escpr2 / epson-cn 同策略）。
# 升级版本时同步更新下方 CANON_UFR2_VERSION / CANON_UFR2_DEB_VERSION /
# CANON_UFR2_TARBALL / CANON_UFR2_URL 四个变量。

set -eo pipefail

# ────────────────────────────────────────────────────────────────────
# 架构判断 → 选择 tarball 内的子目录与 .deb 名
# ────────────────────────────────────────────────────────────────────
ARCH="$(dpkg --print-architecture)"
case "${ARCH}" in
    amd64)
        CANON_UFR2_DEB_SUBDIR="x64/Debian"
        CANON_UFR2_DEB_ARCH="amd64"
        ;;
    arm64)
        CANON_UFR2_DEB_SUBDIR="ARM64/Debian"
        CANON_UFR2_DEB_ARCH="arm64"
        ;;
    *)
        # ── 退出码约定（全部 install-*.sh 共同遵守）───────────────────
        #   0 = 安装成功
        #   3 = 当前 CPU 架构不支持该驱动（厂商未提供该架构二进制）
        #   其他非零 = 真正的失败
        # 必须用 3 而**不是** 0：driver-install 对退出码 0 会照常写
        # manifest.txt，Web UI 于是显示"已安装"，用户以为驱动可用。
        echo "[canon-ufr2] unsupported arch=${ARCH} (Canon UFR II driver has no ${ARCH} binary; supported: amd64/arm64)"
        exit 3
        ;;
esac

# ────────────────────────────────────────────────────────────────────
# 配置（升级版本时同步更新这一组）
# ────────────────────────────────────────────────────────────────────
CANON_UFR2_VERSION="6.30"
CANON_UFR2_DEB_VERSION="6.30-1.07"
CANON_UFR2_TARBALL="linux-UFRII-drv-v630-m17n-07.tar.gz"
CANON_UFR2_URL="https://gdlp01.c-wss.com/gds/0/0100009240/40/${CANON_UFR2_TARBALL}"
CANON_UFR2_UA="Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

CANON_UFR2_DEB_NAME="cnrdrvcups-ufr2-uk_${CANON_UFR2_DEB_VERSION}_${CANON_UFR2_DEB_ARCH}.deb"

# ────────────────────────────────────────────────────────────────────
# 下载 & 解压 & dpkg
# ────────────────────────────────────────────────────────────────────
BUILD_DIR="$(mktemp -d /tmp/canon-ufr2.XXXXXX)"
trap 'rm -rf "${BUILD_DIR}"' EXIT

cd "${BUILD_DIR}"

echo "[canon-ufr2] arch=${ARCH} → ${CANON_UFR2_DEB_SUBDIR}/${CANON_UFR2_DEB_NAME}"
echo "[canon-ufr2] downloading ${CANON_UFR2_URL}"
wget --tries=3 --timeout=60 --retry-connrefused \
     --user-agent="${CANON_UFR2_UA}" \
     -O "${CANON_UFR2_TARBALL}" "${CANON_UFR2_URL}"

# tarball 顶层目录名跟随版本号变化（如 linux-UFRII-drv-v630-m17n/），
# 用 --strip-components=1 把第一层目录剥掉，统一展开到 src/ 下，
# 避免后续路径里再写一遍版本号。
mkdir -p src
tar xzf "${CANON_UFR2_TARBALL}" -C src --strip-components=1

# 用 find 兜底实际路径：Canon 偶尔会调整子目录大小写或层级，find 比
# 硬编码 src/${SUBDIR}/${DEB} 更稳。同时也方便诊断（找不到时打印 layout）。
DEB_PATH="$(find src -type f -name "${CANON_UFR2_DEB_NAME}" -print -quit 2>/dev/null || true)"

if [ -z "${DEB_PATH}" ]; then
    echo "[canon-ufr2] FATAL: deb file not found in tarball"
    echo "[canon-ufr2]   expected: ${CANON_UFR2_DEB_NAME}"
    echo "[canon-ufr2]   tarball layout:"
    find src -maxdepth 4 -type f -name "*.deb" || true
    exit 1
fi

echo "[canon-ufr2] installing ${DEB_PATH}"

# ── 把 .deb 原件交给 driver-install 归档（包级持久化）────────────────────
# 本脚本的临时构建目录会被 EXIT trap 删掉，不交接的话重启后无从重装。而 Canon 的
# 产物大量落在 /usr/bin（渲染引擎 cnrsdrvufr2 / cnpdfdrv）、裸 /usr/lib（9 个闭源
# .so）、/usr/share/caepcm（356 个 ICC）、/usr/share/ufr2filterr（半色调表）——
# 这些全在文件级路径白名单之外，只有归档 .deb 才能在容器重启后完整恢复。
# 故意用 `|| true`：归档失败绝不影响本次安装的成败判定，也绝不改变本脚本的退出码
# 语义（0/3/其他）。DRIVER_PKG_DIR 未设置时（构建期或手工执行）行为与以前完全一致。
if [ -n "${DRIVER_PKG_DIR:-}" ]; then
    cp -a "${DEB_PATH}" "${DRIVER_PKG_DIR}/" 2>/dev/null || true
fi

# Canon deb 声明依赖 cups-bsd（Debian trixie 已移除，功能合并到 cups-client）
# 和 libgtk-3-0/libgtk-3-0t64（状态监控 GUI 依赖，无头容器不需要）。
# 这两个依赖对核心 filter（rastertoufr2）的运行毫无影响，filter 实际
# 只链接 libcups/libcupsimage（已安装）。
# 使用 --force-depends 跳过过时的依赖声明，避免 apt-get -f install
# 把整个包回滚删除（trixie 上的实际表现——无法满足 cups-bsd 时 apt 选择
# 删除 Canon 包来 "修复" 依赖关系）。
dpkg -i --force-depends "${DEB_PATH}"

# 验证核心 filter 确实落盘
if [ ! -f /usr/lib/cups/filter/rastertoufr2 ]; then
    echo "[canon-ufr2] FATAL: /usr/lib/cups/filter/rastertoufr2 not found after dpkg install"
    exit 1
fi

echo "[canon-ufr2] installed Canon UFR II/UFRII LT driver v${CANON_UFR2_VERSION} (${CANON_UFR2_DEB_ARCH})"
# 只在构建期（非 AIO）清 apt 索引省镜像体积。
# ⚠️ 在运行中的容器里清空 /var/lib/apt/lists 会让**后续安装的其他驱动**因为
# 没有包索引而 apt-get install 失败（"连续装两个驱动"直接翻车）。
if [ "${CUPS_AIO:-0}" != "1" ]; then
    rm -rf /var/lib/apt/lists/*
fi
