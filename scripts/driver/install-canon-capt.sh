#!/usr/bin/env bash
# 编译并安装 Canon CAPT (LBP2900/LBP2900B) 开源驱动。
#
# 基于逆向工程的开源 CAPT 协议实现（GPL-3.0，alpha 阶段），覆盖 Canon LBP2900/
# LBP2900B 等走 CAPT 协议的旧款 Canon 激光打印机。
# 纯 C 源码，依赖 CUPS 开发头文件（cups-config），全架构编译。
#
# issue #43。源码来自 https://github.com/itapplication/Canon-LBP2900B
#
# ⚠️ 下载策略：
# 该仓库无 release/tag，直接从 GitHub 下载 master 分支的 tarball。
# 如果上游仓库不可用或代码发生破坏性变更导致编译失败，脚本以非零退出码结束
# （fail-fast），避免发布镜像里缺少该驱动却静默成功。

set -eo pipefail

BUILD_DEPS="build-essential autoconf automake libtool gcc pkg-config git"
_AIO_DEPS_INSTALLED=0
BUILD_DIR=""

# ── 统一的 EXIT 清理函数 ───────────────────────────────────────────────
# ⚠️ bash 对同一信号只保留**最后一次**注册的 handler。老实现先 `trap
# '_CUPS_AIO_CLEANUP' EXIT` 再 `trap 'rm -rf "${BUILD_DIR}"' EXIT`，后者直接
# 把前者覆盖掉 → build-essential/gcc 等编译依赖**永不卸载**。这不只是"镜像
# 变大"：driver-install 会把所有新装包（gcc、binutils、libc6-dev…）的文件
# 记进 manifest 并拷进 .drivers，卸载驱动时按 manifest 逐个 rm，把编译工具链
# 和系统库一起删掉，容器直接残废。
# 所以本脚本**全局只允许一个 EXIT trap**，所有清理动作都写进这个函数。
_cleanup() {
    local rc=$?
    if [ -n "${BUILD_DIR}" ]; then
        rm -rf "${BUILD_DIR}"
    fi
    if [ "${_AIO_DEPS_INSTALLED}" = "1" ]; then
        echo "[canon-capt] AIO mode: cleaning up build dependencies..."
        # shellcheck disable=SC2086 # BUILD_DEPS 是有意的空格分隔包名列表
        apt-get purge -y --auto-remove ${BUILD_DEPS} 2>/dev/null || true
        apt-get clean 2>/dev/null || true
        # 注意：AIO（运行中的容器）里**不能**删 /var/lib/apt/lists——后续安装
        # 别的驱动时 apt-get install 会因为没有索引而失败。构建期才需要省体积。
    fi
    return $rc
}
trap _cleanup EXIT

# ── AIO 模式：自行管理编译依赖（单容器部署时 runtime 镜像不含编译工具）──
if [ "${CUPS_AIO:-0}" = "1" ]; then
    echo "[canon-capt] AIO mode: installing build dependencies..."
    apt-get update
    # shellcheck disable=SC2086
    apt-get install -y --no-install-recommends ${BUILD_DEPS}
    _AIO_DEPS_INSTALLED=1
fi

# ────────────────────────────────────────────────────────────────────
# 配置
# ────────────────────────────────────────────────────────────────────
CANON_CAPT_REPO="https://github.com/itapplication/Canon-LBP2900B"
CANON_CAPT_BRANCH="master"

# ────────────────────────────────────────────────────────────────────
# 下载 & 编译
# ────────────────────────────────────────────────────────────────────
BUILD_DIR="$(mktemp -d /tmp/canon-capt-build.XXXXXX)"

cd "${BUILD_DIR}"

echo "[canon-capt] downloading source from ${CANON_CAPT_REPO} (branch: ${CANON_CAPT_BRANCH})"
curl -fL --retry 3 --retry-delay 3 -o capt.tar.gz \
    "${CANON_CAPT_REPO}/archive/refs/heads/${CANON_CAPT_BRANCH}.tar.gz"

mkdir src && cd src
tar xzf ../capt.tar.gz --strip-components=1

# 生成 configure（源码仓库不带 configure，需 autotools 生成）
aclocal
autoconf
automake --add-missing

# ──────────────────────────────────────────────────────────────────────
# 编译选项说明
# ──────────────────────────────────────────────────────────────────────
# 使用与 escpr2 类似的宽容 CFLAGS 应对 Debian trixie / GCC 15 可能的编译问题：
# - C23 标准把"隐式函数声明"和"隐式 int"列为构造错误
# - Debian trixie GCC 15 额外开启了 -Werror=implicit-function-declaration
# 为安全起见，对这类 alpha 阶段的第三方代码统一降级为 warning。
CAPT_CFLAGS="-O2 -std=gnu17 \
-Wno-error=implicit-function-declaration \
-Wno-error=implicit-int \
-Wno-error=incompatible-pointer-types"

./configure --prefix=/usr CFLAGS="${CAPT_CFLAGS}"
make -j"$(nproc)"

# ────────────────────────────────────────────────────────────────────
# 安装 filter 和 PPD
# ────────────────────────────────────────────────────────────────────
# rastertocapt filter → CUPS filter 目录
install -m 755 src/rastertocapt /usr/lib/cups/filter/rastertocapt

# PPD → CUPS model 目录（Canon 子目录，与 canon-ufr2 的 PPD 布局一致）
install -d /usr/share/cups/model/Canon
install -m 644 Canon-LBP-2900.ppd /usr/share/cups/model/Canon/Canon-LBP-2900.ppd

# ────────────────────────────────────────────────────────────────────
# 验证
# ────────────────────────────────────────────────────────────────────
if [ ! -f /usr/lib/cups/filter/rastertocapt ]; then
    echo "[canon-capt] FATAL: rastertocapt filter not found after install"
    exit 1
fi

echo "[canon-capt] installed successfully"
