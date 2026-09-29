# 驱动管理深度说明

> 本文档是 [AGENTS.md](../AGENTS.md)「驱动管理」章节的深度补充，收录持久化原理、白名单翻车案例、EXIT trap 约定由来、架构探测历史、上传机制与 `lpinfo` 解析细节。规则速查（目录约定表、退出码表、ALLOW/DENY 速查）请回 AGENTS.md。

相关文件：`cmd/server/driver_handlers.go`、`cmd/server/driver_registry.go`、`scripts/driver/*.sh`、`frontend/src/views/DriversView.vue`（前端「驱动」页，路由 `/drivers`，仅 admin 可见）。

## 持久化原理（为什么不需要 `CAP_SYS_ADMIN`）

不涉及任何 overlay/mount 魔法。有**两条通道**，按驱动来源分流（为什么必须分流见下一节）：

**文件级通道**（非包来源的驱动：源码编译 / 手工 cp / unzip / 固件生成）：

1. 安装前对 `MONITORED_DIRS` 做 `find \( -type f -o -type l \)` 快照（`/usr/lib/cups`、`/usr/share/cups`、`/usr/share/ppd`、`/lib/firmware`、`/usr/share/foomatic`，外加探测到的 multiarch 目录 `/usr/lib/<triplet>`），同时 `dpkg-query -W` 记录包名+版本
2. 跑 `install-<name>.sh`（`export CUPS_AIO=1` + `export DRIVER_PKG_DIR=<交接目录>`）
3. 安装后再快照，`comm -13` 求出新增文件；再把"本次新装的 dpkg 包"（`dpkg -L` 展开）里**通过白名单的**文件也并进来
4. 路径白名单 + baseline 归属守卫过滤 → 减去被跳过的包所拥有的文件 → 减去已归档 `.deb` 覆盖的文件 → 逐个 `cp -aT` 到 `/opt/cups-drivers/data/<driver>/<绝对路径>` → 写 `manifest.txt` + `metadata.txt` → `ldconfig`
5. 容器重启时 `entrypoint.sh` 第一步跑 `restore-drivers`，逐行读 manifest，`mkdir -p` 父目录后 `cp -aT --remove-destination` 回系统路径，最后 `ldconfig`

**包级通道**（dpkg 包来源的驱动）：归档 `.deb` 原件到 `packages/` + 登记 `packages.txt`，重启时 `dpkg -i` 装回去，卸载时按包 purge。**完全绕开路径白名单** —— 删哪些文件由 dpkg 自己的 `.list` 决定，天然精确。

`.deb` 原件的捕获分四层，前面命中就不用后面：

| 层 | 手段 | 适用 | 局限 |
| --- | --- | --- | --- |
| A | apt 钩子 `DPkg::Pre-Install-Pkgs` → `capture-debs` | `apt-get install` 来源 | 需要能写 `/etc/apt/apt.conf.d/` |
| B | 厂商脚本把下载好的 `.deb` 拷到 `$DRIVER_PKG_DIR` | 自己 curl/wget 的厂商 deb | 需要脚本配合（一行） |
| C | `/var/cache/apt/archives/` 打捞 | 离线可用 | 脚本可能已 `apt-get clean` |
| D | `apt-get download <pkg>=<ver>` | 最后兜底 | 需要网络 + 索引 |

Layer A 是最可靠的：它拿到的是 apt **真正要装的那批 deb 文件本身**（含全部传递依赖），发生在任何 `apt-get clean` 之前，不需要重新下载、不需要猜版本。

> 🚫 `capture-debs` **必须永远 `exit 0`**。`DPkg::Pre-Install-Pkgs` 返回非零会让 apt 中止**整个安装事务** —— 一个归档辅助脚本绝不能有能力弄挂驱动安装。它还必须在 `/run/cups-drivers/capture-target` 不存在时静默空跑，这样即使 `driver-install` 被 SIGKILL、drop-in 残留，后续任何 apt 操作也只是白跑一次。
>
> 🚫 厂商脚本的交接行必须写成 `[ -n "${DRIVER_PKG_DIR:-}" ] && cp -a "$DEB" "$DRIVER_PKG_DIR/" 2>/dev/null || true` —— **不新增 `trap`**（不碰"只允许一个 EXIT trap"约定）、**`|| true`**（不改变 `0/3/其他` 的退出码语义）、变量未设置时行为与以前完全一致（构建期或手工执行仍可用）。

**归档剪枝**用两个"天然正确"的条件，不需要遍历依赖闭包：① 包在 `install-*.sh` 退出后**仍处于 installed 状态**（AIO 编译型脚本会在自己的 EXIT trap 里 `apt-get purge --auto-remove` 掉工具链 → 自动被剪掉，无需维护列表）；② 包**不在 `baseline-packages.txt` 里**（绝不归档镜像自带组件，否则 restore 时会把系统组件降级）。再叠一层包名 DENY 作为"purge 失败时"的第二道保险。

## ⚠️ 为什么 dpkg 来源必须走包级通道（9 个驱动的实测结果）

这不是理论推演。把仓库里每个 `install-*.sh` 的产物实际下载下来核对后，**机制边界与故障边界完全吻合**：文件级快照出问题的 5 个驱动**全部是 dpkg 包来源**，完整的 4 个**全部是非包来源**。

| 驱动 | 来源 | 老实现（纯文件级）的结果 |
| --- | --- | --- |
| `escpr2`（amd64/armhf） | 厂商 deb | 🔴 **必然 `exit 1`**。deb 的 298 个文件**全部**在 `/opt/epson-inkjet-printer-escpr2/`，postinst 只在 `/usr/share/ppd/` 建一个目录符号链接；`find -type f` 不采集符号链接、`/opt` 不在白名单 → 新文件列表为空 → 判"什么都没装"。但文件其实已进容器，于是 UI 报失败而系统里驱动生效，状态自相矛盾；又因为没写 manifest，"已安装"拦截也失效，用户重复点击会反复重下 |
| `epson-cn` | 厂商 deb ×2 | 🔴 **会删系统库**。deb 依赖 Qt5，trixie 上靠 t64 改名包满足，老实现的 `apt-get -f install` 会拉进整套 Qt5+X11/GL（实测计划新装 **146 个包**）。这些 `.so` 落在 multiarch 目录 —— 正在白名单**内** → 被当驱动产物记进 manifest → `driver-remove` 逐条 `rm` 掉系统库，restore 每次开机还用旧副本覆盖回去。同时驱动**本体**（`/opt/epson-inkjet-printer-201601w/` 的 26 个文件）0 个进 manifest |
| `canon-ufr2` | 厂商 deb | 🔴 418/1294 进 manifest。漏 `/usr/bin/cnrsdrvufr2`+`cnpdfdrv`（**真正的渲染引擎**，`rastertoufr2` 通过 exec 调用它们）、裸 `/usr/lib/lib*ufr2*.so`×9、`/usr/share/caepcm/**`×356（ICC）、`/usr/share/ufr2filterr/*.BIN`×6（半色调表）→ 队列看起来正常但**每个作业都失败** |
| `gutenprint` | apt | 🔴 漏 `libgutenprint-common` 的 `/usr/share/gutenprint/5.3/xml/**`≈370 个机型定义 → `lpinfo -m` 一个 gutenprint 机型都列不出来。UI 仍显示"已安装" |
| `konica-bizhub` | 厂商 deb | 🔴 只有 1 个 PPD 进 manifest。漏整个 `/opt/km/**`（含 filter 真身 `lpdwrapper`、`rawtobr3`）、以及 postinst **现建**的符号链接 `/usr/lib/cups/filter/km_BIZHUB3000MF`（它落在白名单目录内，但 `find -type f` 排除符号链接，而 `dpkg -L` 里也没有它 —— postinst 现建的东西不在包文件列表里）→ `filter failed` |
| `canon-capt` | 源码编译 | ✅ 完整（手工 cp filter + PPD，都在白名单内） |
| `hp-laserjet1020` | pyppd 生成 | ✅ 完整（`/lib/firmware/hp/` + `/usr/share/cups/model/HP/`） |
| `sharp` | unzip | ✅ 完整（4 个 PPD 到 `/usr/share/cups/model/Sharp/`） |
| `foo2zjs-firmware` | 固件生成 | 🟢 基本完整（仅漏 2 个别名符号链接，而上游 `hplj*` 脚本自己做了 `FWMODEL` 映射，P1007/P1008 加载的就是 `sihpP1005/1006.dl`，无功能影响） |

改成包级后逐个实测（装 → **销毁重建容器** → 验证）：escpr2 恢复出 filter + 共享库 + 280 个 PPD + postinst 符号链接；canon-ufr2 恢复出渲染引擎 + 356 ICC + 6 半色调表 + 27 个 `.so` + 414 PPD，`ldd` 零缺失；gutenprint 恢复出 318 个 XML，driver 程序列出 **3590** 个机型；konica 恢复出指向 `/opt/km/.../lpdwrapper` 的符号链接；epson-cn 恢复出驱动本体且 Qt5 包数为 **0**。

> ⚠️ **验证时必须销毁重建容器，不能 `docker restart`**。restart 保留容器可写层，驱动文件本来就还在，测不出任何东西 —— 会假通过。

### 🚫 CUPS 自身的包绝不能进驱动快照

本镜像的 CUPS 是**源码编译**的（2.4.19，`cups-compiled.tar` overlay 解包进 `/usr`），apt 侧只装了 `cups-daemon` / `cups-client` / `cups-filters`，**故意没有 `cups` 元包**。

而 `printer-driver-gutenprint` 声明 `Depends: cups, cups-client, cups-filters | ghostscript-cups`，Konica 的 deb 声明 `Depends: cups | cupsd`。一旦让 apt 去满足这个依赖，它就会装上 Debian 的 `cups-core-drivers`（还有 `cups-ppdc` / `cups-server-common` / `cups` 元包），而 `cups-core-drivers` 提供的正是 `/usr/lib/cups/backend/{usb,socket,lpd,dnssd,snmp,mdns}` 和一批 filter —— **直接覆盖源码编译的同名文件**，让 2.4.19 与 Debian 2.4.10 的组件混用。

实测 Konica 那条路径会让 **564 个 CUPS 自身的文件**被算进该驱动的快照 → `driver-remove konica-bizhub` 会把 CUPS 的 usb/socket backend 一起删掉 → 所有打印机失效。包级归档还会让它每次开机 restore 时重新覆盖一遍。

两层处理：**根治**是各 `install-*.sh` 用 `dpkg -i --force-depends` 跳过 `cups` 元包这个空壳（驱动 filter 实际只链接 libcups/libcupsimage，元包对它毫无意义）；**安全网**是 `driver-install.sh` 的 `PKG_NAME_DENY_PATTERNS` 里列上 `cups` / `cups-core-drivers` / `cups-ppdc` / `libcups*` 等，并且这些被跳过的包所拥有的文件会从 manifest 里显式减掉（只靠 baseline 守卫拦不住 —— 它们是**运行期新装**的，不在 baseline 索引里）。

## ⚠️ baseline 归属守卫：路径白名单的结构性盲区

路径白名单按**路径前缀**判断，而 multiarch 目录（`/usr/lib/<triplet>`）**既是驱动共享库的家、也是系统库的家**。上面 `epson-cn` 那一行就是后果：apt 依赖解析拖进来的 Qt5 `.so` 落在白名单**内**，于是被当成驱动产物。按路径根本分不开。

按**包属主**就分得干干净净：构建期 `dpkg-query -W -f '${Package}\n'` 把当时已安装的全部包名存进 `/opt/cups-drivers/baseline-packages.txt`，三个脚本据此把"镜像自带包拥有的文件"一律排除 —— install 侧不记录、remove 侧不删、restore 侧不覆盖。

实现要点（都是为了别让守卫本身变成性能问题）：

- baseline 包名先读进 bash 关联数组做 O(1) 判断，**不要**对两千多个 `.list` 文件各 fork 一次 `grep`
- 只 `cat` baseline 包自己的 `/var/lib/dpkg/info/*.list`；multiarch 包的清单名形如 `<pkg>:<arch>.list`，比对前要把 `:arch` 去掉
- 与 manifest 求**一次** `comm -12` 交集存进关联数组，之后逐条查是 O(1) —— 比"每条 manifest 记录都 grep 一遍十几万行的索引"快几个数量级

两个时间线都自洽：**同一容器内** remove 时，Qt5 是运行期装的（非 baseline）→ 可以按包 purge 掉，比逐个 `rm -f .so` 正确得多；**重启后**新容器的 dpkg 库 = baseline，被旧快照 `cp` 回来的 Qt5 副本是"无主文件"→ 删掉也无害，而真正的镜像自带文件被守卫挡住。

> ⚠️ 这份守卫和三份路径白名单一样**三处各有一份**，理由相同：remove/restore 侧是给**存量已被污染的快照**兜底的。🚫 不要因为 install 侧已经拦过就删掉另外两份。
>
> ⚠️ `baseline-packages.txt` 必须留在**镜像层**（不能放进 `/opt/cups-drivers/data` —— 那会被 volume 挂载覆盖），且必须生成于**所有 apt 安装之后**（否则漏记的包会被误判成"驱动装的"）。文件缺失时守卫降级为"只做路径白名单"并打印一次警告。

## ⚠️ 符号链接：四处判断必须配套

靠符号链接工作的驱动不止一个（Konica 的 postinst 现建 filter 链接、foo2zjs 的机型别名、Epson 的 `/usr/share/ppd/<pkg> -> /opt/.../ppds`），而 shell 的文件测试对符号链接有几个反直觉的地方，缺一处就是新坑：

| 位置 | 改法 | 不改会怎样 |
| --- | --- | --- |
| `driver-install.sh` 的两处 `find` | `\( -type f -o -type l \)` | 符号链接永远进不了 manifest（`dpkg -L` 也补不上 —— postinst 现建的东西不在包文件列表里） |
| `driver-install.sh` 存快照前的判断 | `[ -e ] \|\| [ -L ]` | **指向目录的符号链接在 `-f` 下判假**（`-f` 会跟随链接判断目标是否普通文件）→ 静默跳过 |
| `restore-drivers.sh` 的源存在判断 | 同上 | 同上 |
| `restore-drivers.sh` 的 `cp` | `cp -aT --remove-destination` | 目标已存在且本身是**指向目录的符号链接**时，cp 会先跟随它、判定"目标是目录"，于是把源**拷进那个目录里**而不是替换链接。实测 `--remove-destination` 单独**无效**（它只处理"目标是普通文件"），只有 `-T`（`--no-target-directory`）能强制把目标当作路径本身 |
| `driver-remove.sh` 的删除判断 | `[ -f ] \|\| [ -L ]` | 符号链接删不掉、只计入 `missing_count`，卸载后在 `/usr/lib/cups/filter/` 留一个悬空链接，CUPS 每次枚举都踩到 |

`-T` 的副作用是目标若为**真目录**则 cp 失败 —— 那正是我们想要的：manifest 条目本就不该是目录，失败会被计入 `error_count` 并告警。

## ⚠️ `--libdir`：autoconf 默认值会把库放进白名单外

`install-escpr2.sh` 的源码编译分支（arm64 等无预编译包的架构）原本只传 `--prefix=/usr`。但 `escprlib/Makefile.am` 里 `lib_LTLIBRARIES = libescpr2.la`，产物装进 `$(libdir)`，而 autoconf 默认 `libdir = ${exec_prefix}/lib` → **裸 `/usr/lib`** —— 它不在白名单里（白名单只有 `/usr/lib/cups`、`/usr/lib/firmware` 和 multiarch 目录），于是 `libescpr2.so.1.0.0` 不进 manifest，容器重启后 filter 因为找不到共享库直接起不来。

🚫 **不要为此把裸 `/usr/lib` 加进白名单** —— 那是系统库的家，等于把上面 `epson-cn` 那个"apt 依赖污染 manifest 进而删系统库"的门重新打开，而且这次连 multiarch 都不用绕。

正解是显式 `--libdir=<multiarch 目录>`（那本来就是 Debian 上共享库的正确位置）。探测方式与 `driver-install.sh::detect_multiarch_libdir` 保持一致（glob `/usr/lib/*-linux-gnu*`，**不用** `dpkg-architecture` —— 它属于 `dpkg-dev`，runtime 镜像没装）；探测不到就保持 autoconf 默认行为并打印警告，绝不用猜的路径。

## ⚠️ manifest 白名单：为什么必须存在，且三处都要有

老实现对 dpkg 来源的文件**完全没过滤**——AIO 模式下编译型驱动会现场 `apt-get install build-essential`，于是 `gcc` / `binutils` / `libc6-dev` 全被算作"新装包"，`/usr/bin/gcc`、`/usr/share/man/**`、`/etc/**` 一股脑写进 manifest。而 `driver-remove` 是**按 manifest 逐条 `rm -f`** 的：**卸载一次驱动就把系统 gcc/binutils 和一堆系统库删了，容器直接残废。** `restore-drivers` 同理会用几个月前的旧二进制 `cp -a` 覆盖系统当前文件。

现在 `driver-install.sh` / `driver-remove.sh` / `restore-drivers.sh` **三个脚本各有一份同样的白名单**（函数名分别是 `_is_monitored_path` / `_is_removable_path` / `_is_restorable_path`），规则完全一致：

**ALLOW（必须落在其中之一）**：`/usr/lib/cups`、`/usr/share/cups`、`/usr/share/ppd`、`/usr/share/foomatic`、`/lib/firmware`、`/usr/lib/firmware`，以及探测到的 `/usr/lib/<multiarch-triplet>`（闭源驱动的 `.so` 会装在这里）。

**DENY（即使落在 ALLOW 内也一律排除）**：
`/usr/bin/*`、`/usr/sbin/*`、`/bin/*`、`/sbin/*`、`/usr/local/bin/*`、`/usr/local/sbin/*`、`/etc/*`、`/var/*`、`/usr/include/*`、`/opt/cups-drivers/*`、`/tmp/*`、`/usr/share/{doc,man,locale,info}/*`、`/usr/share/cups/doc-root/*`（CUPS 自带 Web UI 静态资源，不是驱动产物）、`*/pkgconfig/*`、`*.a`、`*.o`、`*.la`。

> 驱动真正需要的可执行文件都在 `/usr/lib/cups/filter/` 与 `/usr/lib/cups/backend/`，**绝不会**出现在 `/usr/bin` 或 `/usr/sbin`——所以排除这些目录是安全的。

**🚫 不要因为"install 侧已经过滤了"就删掉 remove / restore 侧的守卫。** 跑过旧版本的用户手上已经存在**被污染的 `.drivers` 快照**，那些老 manifest 里就躺着 `/usr/bin/gcc`。remove/restore 两侧的守卫是给这批存量快照兜底的，必须**永久保留**；命中时的行为是**只告警并跳过，绝不 `rm` / 绝不 `cp`**，并在结尾汇总 `skipped_count`。

## ⚠️ AIO 编译脚本的「单一 EXIT trap」约定

bash 对同一信号**只保留最后一次注册的 handler**。老实现里编译型脚本注册了两个 `trap ... EXIT`（一个卸载 AIO 编译依赖、一个删临时构建目录），后注册的直接把前一个覆盖掉，两种翻车都真实发生过：

- `install-canon-capt.sh` / `install-foo2zjs-firmware.sh`：**AIO 清理 trap 被覆盖** → `build-essential` / `gcc` 永不卸载 → 被 `driver-install` 当成"新装包"，整条工具链的文件（在加白名单之前）被写进 manifest → 卸载驱动时删掉系统 gcc
- `install-escpr2.sh`：**删临时目录的 trap 被覆盖** → 几十 MB 的构建目录泄漏在容器 `/tmp`

现在这三个脚本统一成**全局唯一一个** `trap _cleanup EXIT`，`_cleanup()` 内部按分支做所有清理（`rm -rf "${BUILD_DIR}"` + `_AIO_DEPS_INSTALLED=1` 时 `apt-get purge -y --auto-remove ${BUILD_DEPS}`），并且 `local rc=$?` / `return $rc` 保住原退出码。**新增编译型驱动脚本时必须遵守这条约定**：一个脚本只允许一个 EXIT trap，所有清理写进那个函数里。

另一条相关约定：`_cleanup` 里 AIO 模式下**只 `apt-get clean`，绝不 `rm -rf /var/lib/apt/lists/*`**。运行中的容器清掉 apt 索引后，紧接着装第二个驱动就会因为没有包索引而 `apt-get install` 失败（"连续装两个驱动直接翻车"）。各 `install-*.sh` 末尾清索引的语句也统一加了 `if [ "${CUPS_AIO:-0}" != "1" ]` 守卫——只有构建期才为省体积清。

## 退出码约定的由来

`install-*.sh` 共同遵守（`driver-install.sh` 里以注释形式写死）：

| 退出码 | 含义 | `driver-install` 的行为 |
| --- | --- | --- |
| `0` | 安装成功 | 继续做 diff / 写 manifest |
| `3` | **当前 CPU 架构不支持该驱动**（厂商未提供该架构二进制） | 打印中文说明、`discard_driver_data`（删掉可能已创建的空数据目录）、**不写 manifest**、以 3 退出 |
| 其他非零 | 真正的失败（下载 / 编译 / dpkg 失败） | 同样 `discard_driver_data` 后原样透传退出码 |

为什么必须区分：老实现里"架构不支持"分支是 `exit 0`，`driver-install` 照常写 `manifest.txt`，Web UI 于是显示**「已安装」**，用户以为驱动可用。当前 `exit 3` 的脚本：`install-gutenprint.sh`（armhf / armel）、`install-canon-ufr2.sh`（非 amd64/arm64）、`install-epson-cn.sh`（非 amd64）、`install-konica-bizhub.sh`（非 amd64/arm64）。

还有一条同源约定：**退出码 0 但一个新文件都没产生，也视为失败。** `driver-install.sh` 在 diff+过滤后如果 `new-files.txt` 为空，会打印明确错误、`discard_driver_data`、`exit 1`，**拒绝写 manifest.txt**——否则又会出现"UI 显示已安装、实际什么都没装"。

## 架构探测约定（为什么不用 `dpkg-architecture`）

runtime 镜像**没有 `dpkg-dev`**，所以：

- **不能用 `dpkg-architecture`**。老代码用它取架构和 multiarch triplet，在 arm 上命令直接不存在 → 静默回落到硬编码的 `x86_64-linux-gnu`，导致监控目录是个不存在的路径，闭源驱动的 `.so` 变更**完全抓不到**；`driver-list.sh` 那边则回落到 `uname -m` 的 `aarch64`，和 `metadata.txt` 里 `arch=arm64` 永远不相等，于是每个已装驱动都被误报"架构不一致"。
- 统一用 **`dpkg --print-architecture`**（`dpkg` 本体一定在）拿 Debian 架构名 `amd64` / `arm64` / `armhf`。`driver-install.sh::detect_deb_arch` 在连 `dpkg` 都没有时才退到 `uname -m`，且只作诊断展示。
- multiarch 库目录用 `detect_multiarch_libdir()`：`dpkg-architecture` 存在就用（构建期/开发机）→ 否则 glob `/usr/lib/*-linux-gnu*` → 都拿不到就**返回空串，调用方跳过该目录**（绝不使用猜错的路径）。
- Go 侧 `driver_registry.go::currentDebArch()` 把 `GOARCH` 映射到**同一套 Debian 命名**（`amd64`→`amd64`、`arm64`→`arm64`、`arm`→`armhf`、`386`→`i386`，未知架构原样返回），这样 `DriverMeta.Arch`（写的是 `amd64`/`arm64`/`armhf`/`all`）、`metadata.txt` 的 `arch=`、脚本里的判断三方才能直接比较。二进制是 `CGO_ENABLED=0` 交叉编译的，`GOARCH` 就是运行架构。

## 上传自定义驱动

### `.ppd`

校验首 256 字节含 `*PPD-Adobe` → 写 `/usr/share/cups/model/custom/<name>.ppd` → 在 `custom-ppd/` 下存一份同结构副本 → **追加 manifest**（`appendManifestLine` 幂等去重）+ 写 `metadata.txt`。**能被 `restore-drivers` 恢复。**

### `.deb`

`dpkg -i` 失败时 `apt-get install -y -f --no-install-recommends` 补依赖，**然后必须再 `dpkg -i` 一次**（老实现修完依赖就返回，等于白跑一趟 apt）。

成功后把原件归档到 `custom-deb/packages/` **并登记 `packages.txt`**（`<pkg> <version> <arch> <文件名>`，包名/版本/架构用 `dpkg-deb -f` 从控制信息里读，读不到就退回用文件名当包名）。容器启动时 `restore-drivers` 走**包级通道**把它 `dpkg -i` 装回来，幂等（已装且版本不更旧就跳过）。

仍然**不写 `manifest.txt`**——文件级恢复是按 manifest 里的绝对路径逐条 `cp -aT` 回文件系统的，对 `.deb` 毫无意义（真正的安装动作在 maintainer script 里），写了只会把 `.deb` 文件拷到荒谬的路径。包级恢复才是 `.deb` 的正确归宿。

这修掉了一个长期的坑：以前这里只归档不登记，用户上传的 `.deb` **每次容器重启都得手动重新上传一遍**。

- 存量"只归档未登记"的 `.deb` 会被 `restore-drivers` 按 glob `packages/*.deb` **自动收养**，用户不需要做任何迁移动作
- ⚠️ 代价是**一个装不上的坏包会每次开机重试**。出口有两个：删掉宿主 `./.drivers/custom-deb/packages/` 下的对应文件；或从 `packages.txt` 里删掉那一行。`customDebNotice` 已经把这句话回给前端

### 🔐 安全风险面（有意保留的管理员能力）

上传 `.deb` 等价于**容器内 root RCE**——dpkg 会以 root 执行包里的 maintainer script（`preinst`/`postinst`…），可以做任何事。该接口受 `RequireSession` + `RequireAdmin` + `ValidateCSRF` 三重保护，且每次上传都把上传者用户名写进日志用于审计。**部署时请把管理员账号密码视作等同于容器 root 凭据。**

### 文件名与大小上限

- 文件名一律经 `safeUploadFilename` 收敛（先手工切掉 Windows 反斜杠路径，再 `filepath.Base`，拒绝 `.`/`..`/隐藏文件/含分隔符），不依赖标准库 multipart 恰好做过 `Base`。
- ⚠️ **大小上限的正确写法**：`r.ParseMultipartForm(n)` 的 `n` 是 **maxMemory（内存缓冲上限）而不是请求体上限**——超出部分 Go 会静默 spool 到临时文件，所以单靠它**拦不住**超大上传（本接口原先写 `ParseMultipartForm(50 << 20)` + 注释 `// 50 MB limit`，就是对 Go 语义的误解）。真正的硬上限必须 `r.Body = http.MaxBytesReader(w, r.Body, driverUploadMaxBytes)` 包一层，之后 `ParseMultipartForm` 才会在超限时报错；`maxMemory` 另外给个小值（本接口 8MB）让大包落盘而不是整个进内存。

> 📌 遗留待办：`print_handlers.go` / `convert_handler.go` / `estimate_handler.go` / `compose_handler.go` 目前都是 `ParseMultipartForm(512 << 20)`，同样把 maxMemory 当成了上限——含义是「允许把最多 512MB 塞进内存」且**没有任何请求体硬上限**。这几处属于本次改动之外的历史代码，未一并修改；后续收敛时同样应改成 `MaxBytesReader` + 小 maxMemory。

## `lpinfo` 检测：格式假设与型号解析优先级

`GET /api/admin/drivers/detect` 用的是 **`lpinfo -l -v` 长格式**。老代码调的是短格式 `lpinfo -v`（每行只有 `<class> <uri>` 两列，**根本没有厂商型号**）却按长格式去解析引号里的 make-and-model，后果是连锁的：网络打印机型号恒为空 → `checkHasDriver(" ")` 因 `strings.Contains(desc, " ")` 恒为 `true` → `findBestPPD("")` 返回 `lpinfo -m` 的**第一条 PPD**，给打印机套上一个完全无关的驱动。

现在的实现（`parseLpinfoDevices` / `buildDetectedPrinter`）：

- 以 `Device:` 开块，块内每行按第一个 `=` 拆 key/value，未知 key 忽略——刻意宽容，某版本改了字段顺序或缩进也不会整体解析失败
- **过滤裸 backend 行**：`lpinfo` 还会输出 backend 自身（`network socket` / `direct hp` / `ipp` / `lpd` / `beh` / `dnssd`…），这类行第二列不是完整 URI（**不含 `://`**），必须丢掉，否则会凭空多出 5~6 台"假打印机"；同时跳过 `cups-pdf` / `cups-brf` / `file:///dev/null` 等虚拟设备
- **型号解析优先级（按可信度）**：`make-and-model` → `device-id` 的 `MFG`/`MDL`（含 `MANUFACTURER`/`MODEL` 长写法，并去掉型号里重复的厂商前缀）→ `info` → URI 路径（仅 `usb://厂商/型号` 能解析出来）。`splitMakeAndModel` 把 CUPS 填的 `Unknown` 等价于空；只有一个词时当型号处理
- **空型号短路**：打分引擎（`ppd_match.go::ScorePPDCandidates`）里 `len(compact) < 2` 时不产出任何非 generic 候选（连"单字符解析残渣"一起挡掉）。⚠️ **历史实现挑不到 PPD 时不传 `-m`，注释声称"让 CUPS 走 driverless / IPP Everywhere"——这是错的。** `lpadmin` 不传 `-m` 建的是 **raw 队列**（无 PPD），不是 IPP Everywhere。raw 队列拿不到 PPD 选项 → `/api/printer-info` 的 `mediaSourceSupported` 为空 → 前端进纸盒下拉消失。现在的实现：无候选且支持 driverless → 显式 `-m everywhere`；无候选且不支持 → **报错**，绝不静默建 raw
- PPD 匹配走打分引擎（`ppd_match.go` 纯函数 + `ppd_query.go` 副作用层），`lpinfo -m` 走 TTL 10 分钟缓存（装卸驱动后显式失效）

## 一键设置（`/drivers/setup`）的步骤

请求字段名以 `/detect` 的响应为准：`{deviceUri, driverName?, manufacturer?, model?, deviceId?, ppdUri?, printerName?, allowRaw?}`。

任务内部依次：驱动未装则 `driver-install`（以 `manifest.txt` 存在与否判断）→ 确定厂商/型号（优先级：`req.Manufacturer/Model` → `parseDeviceID(deviceId)` → `parseDeviceURI(uri)`）→ **三态决策树**决定 `-m`（显式 `ppdUri` > `everywhere` > 自动 Top-1 > 报错）→ `uniquePrinterName` 去重队列名 → `lpadmin -p <name> -E -v <uri> [-m <ppd>]` → 默认 A4 → **验证**（`lpstat -p` + `lpoptions -l`，PPD 未生效时 `isNew` 队列回滚 `lpadmin -x`）。

## 异步任务模型（`driver_handlers.go`）

`install` / `remove` / `setup` 三个接口**必须是异步**的，不要"简化"回同步：

- `main.go` 的 `http.Server` 是全局 `WriteTimeout = 120s`，而编译型驱动（`canon-capt`、`foo2zjs-firmware`、arm64 上的 `escpr2`）在容器里现场 `apt-get install build-essential` + `make`，几分钟到十几分钟都正常。
- 同步实现里用 `exec.CommandContext(r.Context(), ...)`：连接一超时，请求 context 被 cancel，**`CommandContext` 会直接 kill 掉正在 `make` 的进程**，留下半编译产物（以及没被 EXIT trap 卸载干净的编译依赖），客户端还什么都拿不到。
- 现在的实现：handler 立刻 `202` 返回 `jobId`，真正的命令跑在 `context.Background()` 派生的 goroutine 里（硬超时常量 `driverJobTimeout = 30 * time.Minute`），前端轮询 `jobs/{id}` 拿增量日志。命令的 stdout/stderr 都写进加锁的 `safeBuffer`，所以轮询能看到"正在编译"的实时输出而不是等结束才一次性拿到。

**单飞（single-flight）**：`startDriverJob` 在锁内扫一遍任务表，**同一时刻只允许一个驱动任务在跑**——apt/dpkg 自身有全局锁，并发安装只会互相失败，报错还很难懂。已有任务运行中时接口返回 `409` 并带上正在跑的 `jobId`，前端可以直接切过去轮询：

```json
{ "error": "已有驱动任务正在执行，请等待其完成后重试", "jobId": "…" }
```

`.deb` 上传（同步执行 `dpkg -i`）也走同一把逻辑锁：`runningDriverJobID() != ""` 时直接 `409`，避免和后台任务抢 dpkg 锁。

**任务保留期**：`driverJobRetention = time.Hour`，`pruneDriverJobsLocked` 在每次新建任务时清掉完成超过 1 小时的旧任务，防止长期运行的进程无限累积（任务只存在内存里，进程重启即丢，前端已按此假设做超时提示）。`jobId` 是 `randomToken()` 生成的不透明大写 base32 串，路由约束因此放宽成 `{id:[A-Za-z0-9]+}`。
