#!/usr/bin/env bash
# 柯尼卡美能达 bizhub 3000MF 黑白激光打印机驱动：amd64 + arm64 best-effort 安装。
#
# 背景（issue #35）：
# 柯尼卡美能达官方未把任何 Linux 驱动上传 Debian 仓库；其官方下载站
# (konicaminolta.com.cn) 只针对国产化平台（银河麒麟 / UOS）发布了 .deb，
# 原始格式是 .7z（中文顶层目录「银河麒麟」），且真实下载点是 IIS 302 跳
# CloudFront 签名 URL，fileId 长期稳定但下载链路不易在 CI 里稳定复现。
# 所以这里只从我们自维护的 GitHub Releases 镜像下载 tar.gz——已经把官方
# .7z 解包后重新打成 .tar.gz 上传，省掉了 p7zip-full 依赖与中文路径处理。
#
# ────────────────────────────────────────────────────────────────────
# 架构覆盖说明
# ────────────────────────────────────────────────────────────────────
# tar.gz 解压后顶层目录与 tarball 同名（bizhub3000mfpdrvchn_<ver>/），按
# 架构分四个子目录：
#   amd64/        bizhub3000mfpdrvchn_<ver>_amd64.deb       → amd64
#   arm64/        bizhub3000mfpdrvchn_<ver>_arm64.deb       → arm64
#   loongarch64/  bizhub3000mfpdrvchn_<ver>_loongarch64.deb → 龙芯（本镜像未发布）
#   mips64/       bizhub3000mfpdrvchn_<ver>_mips64el.deb    → MIPS（本镜像未发布）
# 每个架构同时包含 konicaminoltascan1（扫描驱动）的同名 .deb，
# **本脚本不安装扫描驱动**——理由：
#   ① 本仓库是 Web 打印工具，scan 没业务诉求；
#   ② konicaminoltascan1 依赖 sane-utils/libsane 等扫描栈，trixie 上的
#      包名/ABI 跟银河麒麟可能错位，装失败会让 dpkg 回退跑 `apt -f install`
#      拖慢构建甚至误装一堆无用依赖。
# armhf/armel 没有 32-bit ARM 包，脚本入口直接 skip。
#
# ────────────────────────────────────────────────────────────────────
# 下载策略
# ────────────────────────────────────────────────────────────────────
# 与 install-escpr2.sh 同模式：只从仓库自维护的 GitHub Releases 镜像
# （tag 统一为 cups-driver）下载，避免厂商 IIS / CloudFront 签名 URL
# 在 CI 里的不稳定性（fileId 失效、URL 签名过期、TLS 指纹风控等）。
# fail-fast：下载或 dpkg 任一步失败立即非零退出，避免发布镜像里缺少
# 驱动却静默成功。
# 升级版本：①在本仓库 cups-driver release 上传新版 tar.gz；②修改下方
# KM_VERSION / KM_TARBALL / KM_MIRROR_URL 三个变量。

set -eo pipefail

# ────────────────────────────────────────────────────────────────────
# 架构判断 → 选择 tarball 内的子目录与 .deb 名
# ────────────────────────────────────────────────────────────────────
ARCH="$(dpkg --print-architecture)"
case "${ARCH}" in
    amd64)
        KM_DEB_ARCH="amd64"
        ;;
    arm64)
        KM_DEB_ARCH="arm64"
        ;;
    *)
        # ── 退出码约定（全部 install-*.sh 共同遵守）───────────────────
        #   0 = 安装成功
        #   3 = 当前 CPU 架构不支持该驱动（厂商未提供该架构二进制）
        #   其他非零 = 真正的失败
        # 必须用 3 而**不是** 0：driver-install 对退出码 0 会照常写
        # manifest.txt，Web UI 于是显示"已安装"，用户以为驱动可用。
        echo "[konica-bizhub] unsupported arch=${ARCH} (no ${ARCH} binary; supported: amd64/arm64)"
        exit 3
        ;;
esac

# ────────────────────────────────────────────────────────────────────
# 配置（升级版本时同步更新这一组）
# ────────────────────────────────────────────────────────────────────
KM_VERSION="1.0.0-1"
KM_TARBALL="bizhub3000mfpdrvchn_${KM_VERSION}.tar.gz"
KM_MIRROR_URL="https://github.com/hanxi/cups-web/releases/download/cups-driver/${KM_TARBALL}"

# tarball 内 .deb 路径形如 bizhub3000mfpdrvchn_<ver>/<arch>/<deb>，
# 用 find -name 按 .deb 文件名兜底定位，避免硬编码顶层目录路径。
KM_DEB_NAME="bizhub3000mfpdrvchn_${KM_VERSION}_${KM_DEB_ARCH}.deb"

# ────────────────────────────────────────────────────────────────────
# 下载 & 解压 & dpkg
# ────────────────────────────────────────────────────────────────────
BUILD_DIR="$(mktemp -d /tmp/konica-bizhub.XXXXXX)"
trap 'rm -rf "${BUILD_DIR}"' EXIT

cd "${BUILD_DIR}"

echo "[konica-bizhub] arch=${ARCH} → ${KM_DEB_NAME}"
echo "[konica-bizhub] downloading from mirror ${KM_MIRROR_URL}"
curl -fL --retry 3 --retry-delay 3 -o "${KM_TARBALL}" "${KM_MIRROR_URL}"

mkdir -p extracted
tar xzf "${KM_TARBALL}" -C extracted

# find 兜底定位 .deb：tarball 顶层目录跟随版本号变化，find 比硬编码更稳。
DEB_PATH="$(find extracted -type f -name "${KM_DEB_NAME}" -print -quit 2>/dev/null || true)"

if [ -z "${DEB_PATH}" ]; then
    echo "[konica-bizhub] FATAL: deb file not found in tarball"
    echo "[konica-bizhub]   expected: ${KM_DEB_NAME}"
    echo "[konica-bizhub]   tarball layout:"
    find extracted -maxdepth 4 -type f -name "*.deb" || true
    exit 1
fi

echo "[konica-bizhub] installing ${DEB_PATH}"

# ── 把 .deb 原件交给 driver-install 归档（包级持久化）────────────────────
# 本脚本的临时目录会被 EXIT trap 删掉，不交接的话重启后无从重装。而这个驱动的
# **filter 真身**（/opt/km/bizhub3000mf/bin/cupswrapper/lpdwrapper）、rawtobr3、
# brprintconflsr3 全在 /opt/km 下，加上 postinst 现建的符号链接
# /usr/lib/cups/filter/km_BIZHUB3000MF —— 文件级快照一个都抓不到（/opt 不在白名单，
# 符号链接又不在 dpkg -L 输出里），只有归档 .deb 才能完整恢复。
# 故意用 `|| true`：归档失败绝不影响安装成败判定与退出码语义（0/3/其他）。
if [ -n "${DRIVER_PKG_DIR:-}" ]; then
    cp -a "${DEB_PATH}" "${DRIVER_PKG_DIR}/" 2>/dev/null || true
fi

# ⚠️ 这里**故意不用 `apt-get -f install` 兜底**（老实现用过，会污染 CUPS）：
# 这个 deb 声明 `Depends: cups | cupsd`，而本镜像的 CUPS 是**源码编译**的
# （install-cups.sh 装进 /usr，再由 Dockerfile 的 overlay tar 解包），apt 侧只装了
# cups-daemon / cups-client / cups-filters，故意没有 `cups` 元包。
# 一旦让 apt 去满足这个依赖，它就会连带装上 Debian 的 `cups-core-drivers`
# （还有 cups-ppdc / cups-server-common / cups 元包），而 cups-core-drivers 提供的
# 正是 /usr/lib/cups/backend/{usb,socket,lpd,dnssd,snmp,mdns} 与一批 filter ——
# **直接覆盖源码编译的同名文件**，让 2.4.19 与 Debian 2.4.10 的组件混用。
# 实测这会让 564 个 CUPS 自身的文件被算进本驱动的快照，卸载驱动时把它们一起删掉。
#
# 这个 deb 真正需要的运行期依赖都已在镜像里，`cups` 只是个空壳元包，
# 用 --force-depends 跳过即可（与 install-canon-ufr2.sh / install-epson-cn.sh 同一思路）。
if ! dpkg -i --force-depends "${DEB_PATH}"; then
    echo "[konica-bizhub] ERROR: dpkg -i --force-depends failed"
    exit 1
fi

echo "[konica-bizhub] installed Konica Minolta bizhub 3000MF driver v${KM_VERSION} (${KM_DEB_ARCH})"
# 只在构建期（非 AIO）清 apt 索引省镜像体积。
# ⚠️ 在运行中的容器里清空 /var/lib/apt/lists 会让**后续安装的其他驱动**因为
# 没有包索引而 apt-get install 失败（"连续装两个驱动"直接翻车）。
if [ "${CUPS_AIO:-0}" != "1" ]; then
    rm -rf /var/lib/apt/lists/*
fi
