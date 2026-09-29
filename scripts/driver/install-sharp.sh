#!/usr/bin/env bash
# Sharp PostScript PPD 安装脚本：全架构通用（issue #39）。
#
# 背景：
# Sharp MX-C2622R 是 PostScript 打印机，只需要 PPD 描述文件即可被 CUPS 驱动——
# CUPS 内置的 PostScript 过滤链（pdftops / pstops）负责所有 PDF → PS 渲染，
# 无需额外的架构相关 filter 二进制。
#
# 驱动来源：
# Sharp 官方 Linux 打印驱动下载页 https://www.sharp.cn/node/1304
# 原始下载地址 https://www.sharp.cn/sites/default/files/uploads/files/Griffin2Light_Libre/Griffin2L_Linux_001.zip
# zip 解压后包含 Sharp-MX-C2622R-ps-chs.ppd（PostScript PPD）。
#
# 下载策略：
# 与 install-hp-laserjet1020.sh / install-konica-bizhub.sh 同模式：从本仓库
# 自维护的 GitHub Releases 镜像（tag = cups-driver）下载完整 zip，避免
# Sharp 官方 CDN 在 CI 里的不稳定性。fail-fast：下载或解压失败立即非零退出。
#
# 架构说明：
# PPD 是纯文本文件，与 CPU 架构无关，所有架构（amd64/arm64/armhf）统一安装。

set -eo pipefail

# ────────────────────────────────────────────────────────────────────
# 配置
# ────────────────────────────────────────────────────────────────────
SHARP_ZIP="Griffin2L_Linux_001.zip"
SHARP_MIRROR_URL="https://github.com/hanxi/cups-web/releases/download/cups-driver/${SHARP_ZIP}"
PPD_INSTALL_DIR="/usr/share/cups/model/Sharp"

# ────────────────────────────────────────────────────────────────────
# 下载 & 解压 & 安装
# ────────────────────────────────────────────────────────────────────
BUILD_DIR="$(mktemp -d /tmp/sharp-ppd.XXXXXX)"
trap 'rm -rf "${BUILD_DIR}"' EXIT

cd "${BUILD_DIR}"

echo "[sharp] downloading from ${SHARP_MIRROR_URL}"
curl -fL --retry 3 --retry-delay 3 -o "${SHARP_ZIP}" "${SHARP_MIRROR_URL}"

# 解压 PPD 文件（-j 忽略 zip 内目录结构，-d 指定输出目录）
mkdir -p "${PPD_INSTALL_DIR}"
unzip -j -o "${SHARP_ZIP}" "*.ppd" -d "${PPD_INSTALL_DIR}"

# 验证至少有一个 PPD 文件被安装
PPD_COUNT=$(find "${PPD_INSTALL_DIR}" -name "*.ppd" -type f | wc -l)
if [ "${PPD_COUNT}" -eq 0 ]; then
    echo "[sharp] FATAL: no PPD files found after extraction"
    echo "[sharp]   zip contents:"
    unzip -l "${SHARP_ZIP}" || true
    exit 1
fi

echo "[sharp] installed ${PPD_COUNT} PPD file(s) to ${PPD_INSTALL_DIR}:"
ls -la "${PPD_INSTALL_DIR}"/*.ppd
