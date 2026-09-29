#!/usr/bin/env bash
# 编译并安装 Epson ESC/P-R 2 驱动。
#
# 支持 ET-18100, L8050, L8160, WF-7840 等新款 Epson 喷墨打印机的完整功能（含无边距打印）。
# Debian 官方仓库不提供此包，从 Epson 源码编译。
#
# 注：apt 阶段的 libcups2-dev / libcupsimage2-dev 不再单独安装——上一步源码编译
# CUPS 的 `make install` 已把 cups/cupsimage 的头文件与 .so 放入 /usr/include 与
# /usr/lib，ESC/P-R 2 的 configure 会通过 cups-config 找到新编译出的 libcups，
# 避免 apt 版 -dev 包回踩覆盖源码装好的 /usr/include/cups/*.h。
#
# ⚠️ 下载策略：
# Epson 官方下载中心（download-center.epson.com）挂在 Akamai CDN 后面，对请求做
# UA/Header/TLS 指纹等多维度风控，HTTP 403 概率高且 UUID 会被 Epson 定期轮换，
# 不适合作为稳定的 CI 构建源。所以这里只从我们自维护的 GitHub Releases 镜像下载
# 源码 tarball / 预编译 deb，下载失败则脚本以非零退出码结束（fail-fast），
# 避免发布镜像里缺少 ESCPR2 驱动却静默成功。
#
# 安装策略：
#   amd64 / armhf → 直接 dpkg 安装预编译 .deb（省 5~10 分钟编译时间）
#   其他架构（arm64 等） → 回退到源码编译
# 升级版本：① 在本仓库的 Releases 里上传新版 tarball + amd64/armhf deb；
#          ② 修改下方 ESCPR2_VERSION / ESCPR2_MIRROR_URL /
#             ESCPR2_DEB_AMD64_URL / ESCPR2_DEB_ARMHF_URL。

set -eo pipefail

# ────────────────────────────────────────────────────────────────────
# 配置
# ────────────────────────────────────────────────────────────────────
ESCPR2_VERSION="1.2.39"
# 镜像 release tag 统一为 cups-driver，多个第三方驱动 tarball / deb 共用一个 release，
# 升级版本时上传新文件到同一 release，无需创建新 tag。
ESCPR2_MIRROR_URL="https://github.com/hanxi/cups-web/releases/download/cups-driver/epson-inkjet-printer-escpr2-1.2.39-1.tar.gz"
ESCPR2_DEB_AMD64_URL="https://github.com/hanxi/cups-web/releases/download/cups-driver/epson-inkjet-printer-escpr2_1.2.39-1_amd64.deb"
ESCPR2_DEB_ARMHF_URL="https://github.com/hanxi/cups-web/releases/download/cups-driver/epson-inkjet-printer-escpr2_1.2.39_armhf.deb"

BUILD_DEPS="build-essential autoconf automake libtool gcc pkg-config libcups2-dev"
_AIO_DEPS_INSTALLED=0
BUILD_DIR=""

# ── 统一的 EXIT 清理函数 ───────────────────────────────────────────────
# ⚠️ bash 对同一信号只保留**最后一次**注册的 handler。老实现在这里先
# `trap 'rm -rf "${BUILD_DIR}"' EXIT`，源码编译分支后面又注册了 AIO 清理
# trap，把前者覆盖掉 → 临时构建目录泄漏（几十 MB 留在容器 /tmp 里）。
# 反过来若顺序相反则编译依赖永不卸载，会让 driver-install 把整条工具链写进
# manifest（卸载驱动时删掉系统文件）。
# 所以本脚本**全局只允许一个 EXIT trap**，所有清理动作都写进这个函数。
_cleanup() {
    local rc=$?
    if [ -n "${BUILD_DIR}" ]; then
        rm -rf "${BUILD_DIR}"
    fi
    if [ "${_AIO_DEPS_INSTALLED}" = "1" ]; then
        echo "[escpr2] AIO mode: cleaning up build dependencies..."
        # shellcheck disable=SC2086 # BUILD_DEPS 是有意的空格分隔包名列表
        apt-get purge -y --auto-remove ${BUILD_DEPS} 2>/dev/null || true
        apt-get clean 2>/dev/null || true
        # 注意：AIO（运行中的容器）里**不能**删 /var/lib/apt/lists——后续安装
        # 别的驱动时 apt-get install 会因为没有索引而失败。
    fi
    return $rc
}
trap _cleanup EXIT

BUILD_DIR="$(mktemp -d /tmp/escpr2-build.XXXXXX)"

cd "${BUILD_DIR}"

# ────────────────────────────────────────────────────────────────────
# 架构判断 → amd64/armhf 直接 dpkg 安装预编译 .deb，其他架构源码编译
# ────────────────────────────────────────────────────────────────────
# amd64/armhf 是发布镜像最常见的两类目标，直接装 .deb 可以省掉容器里
# autoreconf/gcc 的 5~10 分钟编译时间。arm64（树莓派 4/5、Apple Silicon
# 等）暂时没有预编译包，回退到源码编译。
ARCH="$(dpkg --print-architecture)"
case "${ARCH}" in
    amd64)
        ESCPR2_DEB_URL="${ESCPR2_DEB_AMD64_URL}"
        ;;
    armhf)
        ESCPR2_DEB_URL="${ESCPR2_DEB_ARMHF_URL}"
        ;;
    *)
        ESCPR2_DEB_URL=""
        ;;
esac

if [ -n "${ESCPR2_DEB_URL}" ]; then
    ESCPR2_DEB_FILE="$(basename "${ESCPR2_DEB_URL}")"
    echo "[escpr2] arch=${ARCH} → installing prebuilt deb ${ESCPR2_DEB_FILE}"
    echo "[escpr2] downloading from mirror ${ESCPR2_DEB_URL}"
    curl -fL --retry 3 --retry-delay 3 -o "${ESCPR2_DEB_FILE}" "${ESCPR2_DEB_URL}"

    # ── 把 .deb 原件交给 driver-install 归档（包级持久化）────────────────
    # 这个 deb 的 **298 个文件全部**装在 /opt/epson-inkjet-printer-escpr2/ 下，
    # postinst 只在 /usr/share/ppd/ 建一个指向它的目录符号链接。文件级快照因此
    # 一个文件都收不到（/opt 不在白名单、符号链接又不是 -type f）→ 老版本
    # driver-install 会误判"什么都没装"直接 exit 1，而文件其实已经进了容器。
    # 归档 .deb 是这个驱动能被持久化的**唯一**途径。
    # 故意用 `|| true`：归档失败绝不影响安装成败判定与退出码语义（0/3/其他）。
    if [ -n "${DRIVER_PKG_DIR:-}" ]; then
        cp -a "${ESCPR2_DEB_FILE}" "${DRIVER_PKG_DIR}/" 2>/dev/null || true
    fi

    # dpkg -i 报依赖问题时用 apt-get -f install 兜底。
    # ⚠️ 必须先 apt-get update：apt 需要包索引才能下载缺失的依赖，而 AIO 运行时
    # 的镜像里 /var/lib/apt/lists 可能是空的（构建期为省体积清过）。
    if ! dpkg -i "${ESCPR2_DEB_FILE}"; then
        echo "[escpr2] dpkg reported dependency issues, fixing with apt-get -f install"
        apt-get update
        apt-get install -y -f --no-install-recommends
    fi

    echo "[escpr2] installed version ${ESCPR2_VERSION} (${ARCH} prebuilt deb)"
    # 只在构建期（非 AIO）清 apt 索引省镜像体积。
    # ⚠️ 在运行中的容器里清空 /var/lib/apt/lists 会让**后续安装的其他驱动**因为
    # 没有包索引而 apt-get install 失败（"连续装两个驱动"直接翻车）。
    if [ "${CUPS_AIO:-0}" != "1" ]; then
        rm -rf /var/lib/apt/lists/*
    fi
    exit 0
fi

# ────────────────────────────────────────────────────────────────────
# 源码编译路径（arm64 等无预编译包的架构）
# ────────────────────────────────────────────────────────────────────

# ── AIO 模式：自行管理编译依赖（单容器部署时 runtime 镜像不含编译工具）──
# 卸载逻辑在文件头部的统一 _cleanup 里（这里**不要**再注册 EXIT trap）。
if [ "${CUPS_AIO:-0}" = "1" ]; then
    echo "[escpr2] AIO mode: installing build dependencies..."
    apt-get update
    # shellcheck disable=SC2086
    apt-get install -y --no-install-recommends ${BUILD_DEPS}
    _AIO_DEPS_INSTALLED=1
fi

echo "[escpr2] arch=${ARCH} → no prebuilt deb, building from source"
echo "[escpr2] downloading from mirror ${ESCPR2_MIRROR_URL}"
curl -fL --retry 3 --retry-delay 3 -o escpr2.tar.gz "${ESCPR2_MIRROR_URL}"

mkdir src
cd src
tar xzf ../escpr2.tar.gz --strip-components=1
autoreconf -fi

# ──────────────────────────────────────────────────────────────────────
# 编译选项说明（修复 Debian trixie / GCC 15 上的编译错误）
# ──────────────────────────────────────────────────────────────────────
# ESCPR2 源码（Epson 闭源 + 开源混合体）写得相当随意，filter.c / mem.c 里
# 大量调用 `PrintBand` / `SendStartJob` / `SetupJobAttrib` / `err_system` 等
# 函数却**没有任何头文件声明**——实际定义在同 .c 里以 `epsPrintBand` /
# `epsStartJob` 等带前缀的形式存在，编译期 GCC 把它们当作隐式声明的外部函数
# 处理，依赖链接期符号兜底。
#
# 这种代码在 GCC 13 之前只是 warning，但：
#   ① C23 标准（GCC 15 在 trixie 上的默认 -std）把"隐式函数声明"和"隐式 int"
#      列为构造错误；
#   ② Debian trixie GCC 15 还在 default specs 里独立开启了
#      `-Werror=implicit-function-declaration` / `-Werror=implicit-int`，
#      所以单纯 `-std=gnu17` 回退语言标准也压不住——必须显式
#      `-Wno-error=implicit-function-declaration` 把它降级回 warning。
#
# AUR 维护的 epson-inkjet-printer-escpr2 PKGBUILD 也是同样思路（CFLAGS
# 加 -Wno-error=implicit-function-declaration），社区验证过的最小修复。
#
# 同时一并加上 `-Wno-error=incompatible-pointer-types`，因为 ESCPR2 内部
# 有 `unsigned char *` 与 `char *` 互传的老代码，C23 也对这类情况收紧了。
ESCPR2_CFLAGS="-O2 -std=gnu17 \
-Wno-error=implicit-function-declaration \
-Wno-error=implicit-int \
-Wno-error=incompatible-pointer-types"

export CC="gcc"
export CXX="g++"

# ── 为什么必须显式 --libdir ──────────────────────────────────────────────
# escprlib/Makefile.am 里 `lib_LTLIBRARIES = libescpr2.la`，产物装进 $(libdir)。
# autoconf 默认 libdir = ${exec_prefix}/lib，配上 --prefix=/usr 就是**裸 /usr/lib**
# —— 而它不在 driver-install 的路径白名单里（白名单只有 /usr/lib/cups、
# /usr/lib/firmware 和 multiarch 目录），于是 libescpr2.so.1.0.0 不进 manifest，
# 容器重启后 filter 因为找不到共享库直接起不来。
# 🚫 不要为此把裸 /usr/lib 加进白名单 —— 那是系统库的家，等于把"apt 依赖污染
#    manifest 进而 driver-remove 删系统库"的门重新打开。正解是把库装到 multiarch
#    目录（那本来就是 Debian 上共享库的正确位置）。
# 探测方式与 driver-install.sh::detect_multiarch_libdir 保持一致（glob，不用
# dpkg-architecture —— 它属于 dpkg-dev，runtime 镜像没装）；探测不到就保持
# autoconf 默认行为，绝不用猜的路径。
ESCPR2_LIBDIR=""
for _d in /usr/lib/*-linux-gnu*; do
    if [ -d "$_d" ]; then
        ESCPR2_LIBDIR="$_d"
        break
    fi
done

if [ -n "${ESCPR2_LIBDIR}" ]; then
    echo "[escpr2] using --libdir=${ESCPR2_LIBDIR}"
    ./configure --prefix=/usr --libdir="${ESCPR2_LIBDIR}" --disable-static \
        CFLAGS="${ESCPR2_CFLAGS}" \
        CXXFLAGS="-O2 -std=gnu++17"
else
    echo "[escpr2] WARNING: 探测不到 multiarch 库目录，回退 autoconf 默认 libdir。"
    echo "[escpr2] WARNING: libescpr2.so 可能落在 /usr/lib 而进不了驱动快照，重启后需重装。"
    ./configure --prefix=/usr --disable-static \
        CFLAGS="${ESCPR2_CFLAGS}" \
        CXXFLAGS="-O2 -std=gnu++17"
fi
make -j"$(nproc)"
make install

# 验证共享库确实落在能被快照收进去的位置（不是致命错误，但要让日志说清楚）。
if [ -n "${ESCPR2_LIBDIR}" ] && [ ! -f "${ESCPR2_LIBDIR}/libescpr2.so.1.0.0" ]; then
    echo "[escpr2] WARNING: ${ESCPR2_LIBDIR}/libescpr2.so.1.0.0 未找到，请检查 make install 输出。"
fi

echo "[escpr2] installed version ${ESCPR2_VERSION}"
