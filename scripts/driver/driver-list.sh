#!/bin/bash
set -e

DRIVERS_BASE="/opt/cups-drivers"
SCRIPTS_DIR="${DRIVERS_BASE}/scripts"
DATA_DIR="${DRIVERS_BASE}/data"

SHOW_INSTALLED_ONLY=false
if [ "$1" = "--installed" ]; then
    SHOW_INSTALLED_ONLY=true
fi

# Detect current architecture
# ⚠️ 不能用 dpkg-architecture：它属于 dpkg-dev 包，runtime 镜像**没有安装**，
# 调用总是失败 → 回落到 uname -m 得到 aarch64/armv7l，而 metadata.txt 里
# driver-install 写的是 Debian 架构名 arm64/armhf，两者永远不相等，
# 于是每个已安装驱动都被误报"架构不一致"警告。
# `dpkg --print-architecture` 由 dpkg 本体提供，runtime 一定有，且与
# install-*.sh / driver-install.sh 的判断基准完全一致。
CURRENT_ARCH="$(dpkg --print-architecture 2>/dev/null || uname -m)"

echo "=========================================="
echo "  CUPS Driver Manager"
echo "=========================================="
echo "  Architecture: ${CURRENT_ARCH}"
echo "=========================================="
echo ""

if $SHOW_INSTALLED_ONLY; then
    echo "Installed drivers:"
    echo ""

    found=false
    if [ -d "${DATA_DIR}" ]; then
        for driver_dir in "${DATA_DIR}"/*/; do
            [ -d "$driver_dir" ] || continue
            manifest="${driver_dir}manifest.txt"
            [ -f "$manifest" ] || continue

            found=true
            driver_name="$(basename "$driver_dir")"
            file_count=$(wc -l < "$manifest" 2>/dev/null || echo "?")
            install_date=""
            install_arch=""

            restore_mode=""
            pkg_count=""

            if [ -f "${driver_dir}metadata.txt" ]; then
                install_date=$(grep '^installed_at=' "${driver_dir}metadata.txt" 2>/dev/null | cut -d= -f2-)
                install_arch=$(grep '^arch=' "${driver_dir}metadata.txt" 2>/dev/null | cut -d= -f2-)
                restore_mode=$(grep '^restore_mode=' "${driver_dir}metadata.txt" 2>/dev/null | cut -d= -f2-)
                pkg_count=$(grep '^package_count=' "${driver_dir}metadata.txt" 2>/dev/null | cut -d= -f2-)
            fi

            echo "  * ${driver_name}"
            if [ -n "$install_date" ]; then
                echo "      Installed: ${install_date}"
            fi
            if [ -n "$install_arch" ]; then
                echo "      Architecture: ${install_arch}"
            fi
            echo "      Files: ${file_count}"
            # restore_mode 是 v2 快照才有的键；老快照没有这一行，此时不打印（纯文件级）。
            if [ -n "$restore_mode" ]; then
                if [ -n "$pkg_count" ] && [ "$pkg_count" != "0" ]; then
                    echo "      Restore: ${restore_mode} (${pkg_count} package(s))"
                else
                    echo "      Restore: ${restore_mode}"
                fi
            fi
            echo ""
        done
    fi

    if ! $found; then
        echo "  (no drivers installed)"
        echo ""
    fi
else
    echo "Available drivers:"
    echo ""

    if [ ! -d "${SCRIPTS_DIR}" ]; then
        echo "  (no driver scripts directory found at ${SCRIPTS_DIR})"
        echo ""
        exit 0
    fi

    found=false
    for script in "${SCRIPTS_DIR}"/install-*.sh; do
        [ -f "$script" ] || continue

        found=true
        name="$(basename "$script" .sh)"
        name="${name#install-}"

        if [ -f "${DATA_DIR}/${name}/manifest.txt" ]; then
            status="INSTALLED"
            install_date=""
            install_arch=""

            if [ -f "${DATA_DIR}/${name}/metadata.txt" ]; then
                install_date=$(grep '^installed_at=' "${DATA_DIR}/${name}/metadata.txt" 2>/dev/null | cut -d= -f2-)
                install_arch=$(grep '^arch=' "${DATA_DIR}/${name}/metadata.txt" 2>/dev/null | cut -d= -f2-)
            fi

            printf "  %-30s [%s]\n" "$name" "$status"
            if [ -n "$install_date" ]; then
                printf "  %-30s   Installed: %s\n" "" "$install_date"
            fi
            if [ -n "$install_arch" ]; then
                if [ "$install_arch" != "$CURRENT_ARCH" ]; then
                    printf "  %-30s   Architecture: %s (WARNING: current is %s)\n" "" "$install_arch" "$CURRENT_ARCH"
                else
                    printf "  %-30s   Architecture: %s\n" "" "$install_arch"
                fi
            fi
        else
            status="not installed"
            printf "  %-30s [%s]\n" "$name" "$status"
        fi
    done

    if ! $found; then
        echo "  (no driver install scripts found)"
    fi

    echo ""
    echo "Usage:"
    echo "  driver-install <driver-name>    Install a driver"
    echo "  driver-remove <driver-name>     Remove an installed driver"
    echo "  driver-list --installed          Show only installed drivers"
    echo ""
    echo "Note: Some drivers may only support specific architectures."
    echo "      Current architecture: ${CURRENT_ARCH}"
    echo ""
fi
