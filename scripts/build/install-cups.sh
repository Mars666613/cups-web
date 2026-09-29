#!/usr/bin/env bash
# 从 OpenPrinting/cups 源码编译并覆盖安装 CUPS 到 /usr。
#
# 设计动机：
# cups-filters 会把 apt 版 cups 作为依赖拉进来，由它负责创建 lp/lpadmin 用户组、
# /etc/cups 目录骨架和 systemd unit 文件等；随后用源码编译出的二进制（同样
# --prefix=/usr）覆盖掉 apt 版的 libcups.so.2 / cupsd / cups-client 等文件，
# 既保留 Debian 侧的集成脚手架，又替换成 OpenPrinting 上游的最新版本，且
# libcups2 ABI 兼容让 cups-filters 和所有 printer-driver-* 可以继续工作。

set -euo pipefail

# ────────────────────────────────────────────────────────────────────
# 配置
# ────────────────────────────────────────────────────────────────────
CUPS_VERSION="2.4.19"
CUPS_TARBALL_URL="https://github.com/OpenPrinting/cups/releases/download/v${CUPS_VERSION}/cups-${CUPS_VERSION}-source.tar.gz"

# ────────────────────────────────────────────────────────────────────
# 编译 & 安装
# ────────────────────────────────────────────────────────────────────
BUILD_DIR="$(mktemp -d /tmp/cups-build.XXXXXX)"
trap 'rm -rf "${BUILD_DIR}"' EXIT

cd "${BUILD_DIR}"

echo "[cups] downloading ${CUPS_TARBALL_URL}"
wget -q -O cups.tar.gz "${CUPS_TARBALL_URL}"
tar xzf cups.tar.gz --strip-components=1

MULTIARCH="$(dpkg-architecture -qDEB_HOST_MULTIARCH)"
echo "[cups] building for multiarch triplet: ${MULTIARCH}"

./configure \
    --prefix=/usr \
    --libdir="/usr/lib/${MULTIARCH}" \
    --sysconfdir=/etc \
    --localstatedir=/var \
    --with-cups-user=lp \
    --with-cups-group=lp \
    --with-system-groups=lpadmin \
    --enable-libusb \
    --enable-avahi \
    --enable-dbus \
    --enable-gnutls

make -j"$(nproc)"
make install

ldconfig

/usr/sbin/cupsd -V || true

echo "[cups] installed version ${CUPS_VERSION}"
