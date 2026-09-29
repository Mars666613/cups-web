# Docker 构建与部署深度说明

> 本文档是 [AGENTS.md](../AGENTS.md)「部署」章节的深度补充，收录五阶段构建设计理由、三架构基础镜像选型史、关键 `Dockerfile` 注释与 CI/CD 细节。docker-compose 配置速查表请回 AGENTS.md。

## Docker 多阶段构建

`Dockerfile`（**单容器 AIO：CUPS + Web 同镜像**）有**五个**构建阶段，**全部覆盖 `linux/amd64` + `linux/arm64` + `linux/arm/v7` 三架构**：

1. `frontend-build`（`node:20-slim` + `npm`）：`npm ci` + `npm run build` 出 Vite dist
2. `java-builder`（`--platform=$BUILDPLATFORM debian:trixie-slim` + apt `openjdk-21-jdk-headless` + Apache Maven tarball）：构建 `ofd-converter.jar`
3. `builder`（`golang:1.26`）：`go build` 输出二进制（`CGO_ENABLED=0`，`-ldflags -X main.Version=$VERSION`）
4. `cups-builder`（`debian:trixie-slim`）：跑 `scripts/build/install-cups.sh` 从 OpenPrinting/cups 源码编译（`CUPS_VERSION=2.4.19`，`--prefix=/usr` + `--libdir=/usr/lib/<multiarch>`），再把编译产物 `tar` 打包成 `/tmp/cups-compiled.tar`
5. `runtime`（`debian:trixie-slim`）：装齐 CUPS 生态（`cups-filters`/`cups-daemon`/`printer-driver-*`/`hplip`/`avahi-daemon`/`ipp-usb`…）+ LibreOffice（core/writer/calc/impress）+ `openjdk-21-jre` + Ghostscript + 中文字体（`fonts-noto-cjk`、`fonts-wqy-zenhei`、`fonts-arphic-*`、`fonts-droid-fallback`），然后解包 overlay 覆盖 apt 版 CUPS

**运行身份是 root，不是 `nonroot`**：容器内要跑 `cupsd`、`lpadmin`、`dpkg`（运行时安装驱动），还要往 `/usr/lib/cups`、`/usr/share/ppd`、`/lib/firmware` 等系统路径写驱动文件。`docker-compose.yml` 里显式写了 `user: root`。

### 为什么 `cups-builder` 要「apt 装一遍 cups 再用源码编译版覆盖」

`cups-filters` 会把 apt 版 `cups` 当依赖拉进来，由 Debian 包负责创建 `lp`/`lpadmin` 用户组、`/etc/cups` 目录骨架、systemd unit 等**集成脚手架**；随后 `make install`（同样 `--prefix=/usr`）用上游编译产物覆盖 `cupsd` / `libcups.so.2` / `cups-client` 等文件。这样既保留 Debian 侧的脚手架，又拿到 OpenPrinting 上游的最新版本，而 `libcups2` ABI 兼容让 `cups-filters` 和所有 `printer-driver-*` 继续可用。runtime 阶段的 overlay 就是 `tar xf /tmp/cups-compiled.tar -C / && ldconfig`；tar 的文件清单里 `libcups*` 路径用 `dpkg-architecture -qDEB_HOST_MULTIARCH`（**构建阶段装了 `dpkg-dev`，这里可以用**；运行时脚本里不行，见 [driver-management.md](driver-management.md) 架构探测约定）。

### 🚨 `cups-builder` 阶段的 `ca-certificates` 请勿删除

`scripts/build/install-cups.sh` 用 `wget` 从 GitHub Releases 下载 CUPS tarball，没有 CA 根证书时 wget 无法校验 TLS，**以退出码 5（SSL verification failure）失败**，而脚本是 `set -euo pipefail`，整个构建当场崩掉（CI 报 `install-cups.sh ... exit code: 5`）。`debian:trixie-slim` 默认不带 `ca-certificates`；旧的单阶段 `cups/Dockerfile` 因为运行时依赖里已经包含它才没暴露这个坑，拆成独立 builder 阶段后**必须显式声明**。这是真实修过的 CI 打包失败根因。

### `HOME` / LibreOffice profile

runtime 阶段显式 `ENV HOME=/root` + `XDG_CACHE_HOME` + `DCONF_USER_CONFIG_DIR` 并预建目录。原因有两条：

1. LibreOffice headless 必须有可写 HOME 来落 user profile，拿不到就**静默退出**、`--convert-to pdf` 返回 0 却不产出 PDF；
2. Docker 只在 `USER` 指向 `/etc/passwd` 里的用户时才隐式给 `HOME`，部署方用 k8s `securityContext.runAsUser` 或 `docker run -u` 换成任意 uid 时 `HOME` 会退化成 `/`，转换开始莫名失败。写死 ENV 后路径至少是确定的、故障可诊断。

## 三架构覆盖的基础镜像选型

历史决策，`bookworm` → `trixie`、JDK 17 → 21 之后结论不变。

最初 `frontend-build` 用 `oven/bun`、`java-builder` 用 `maven:3.9-eclipse-temurin-17`，但这两个基础镜像都不支持 32-bit ARM：

### `oven/bun` → `node:20-slim`

Bun 官方明确不支持 32-bit ARM（[oven-sh/bun#5060](https://github.com/oven-sh/bun/issues/5060) "Closed as not planned"，仅 arm64/x64）。**替代方案**：切到 `node:20-slim`（官方 manifest 覆盖 `amd64`/`arm32v7`/`arm64v8`），用 `npm ci` + `npm run build` 替换 `bun install` + `bun run build`；前端 `package.json` 里 scripts 全是标准 Vite/Node 命令，不依赖 bun 专有 API，迁移无业务代码改动。

代价是必须维护 `frontend/package-lock.json`（和 `bun.lock` 并存；`npm ci` 要求 lockfile 与 `package.json` 严格一致，开发时如果用 `bun add` / `bun remove` 改了依赖，需同步跑一次 `npm install` 更新 `package-lock.json` 再提交，否则 CI 会在 `npm ci` 阶段挂掉）。

### `maven:3.9-eclipse-temurin-17` → `debian:trixie-slim` + apt JDK + Maven tarball

Eclipse Temurin 对 "Linux ARM 32-bit Hard-Float" 仅 JDK 8/11 有二进制，JDK 17/21/25 [官方明确 Not Supported](https://adoptium.net/supported-platforms)；Maven 官方镜像同样没有 armhf manifest。

**现用方案**：`FROM --platform=$BUILDPLATFORM debian:trixie-slim AS java-builder`，把 java-builder 阶段**固定跑在 host 本地架构**（GitHub Actions 上永远是 amd64），apt 装 `openjdk-21-jdk-headless`，Maven 用 Apache 官方 tarball（`MAVEN_VERSION=3.9.9`）；产物 `ofd-converter.jar` 是纯 Java 字节码（`maven.compiler.source=1.8`），在 runtime 阶段被各架构的 JRE 直接 `COPY --from=java-builder` 过来复用，跨架构通吃。

**为什么必须锁 `BUILDPLATFORM`**：QEMU 用户态模拟 armhf 下现代 OpenJDK 不稳定——Maven 无论是用 Debian 的 `apt install maven` 还是用 Apache 官方 tarball 启动都会随机抛 `java.lang.ClassNotFoundException: org.apache.maven.cli.MavenCli`，堆栈完全一致（只差 classworlds 版本行号：Debian 包版的 `SelfFirstStrategy.java:50` vs tarball 版的 `:42`），说明问题在 JVM 层（QEMU 下的 ClassLoader / JIT 稳定性），不是 Maven 安装方式能救的；Adoptium 官方放弃 JDK 17+ armhf 二进制也印证了"ARM 32-bit 上的现代 JVM 本来就是薄弱环节"。让 java-builder 锁 amd64 就彻底绕开了这堵墙，也是 Docker 官方推荐的 multi-arch Java 最佳实践（纯字节码跨架构是 JVM 的第一性原理）。

**为什么顶部需要 `# syntax=docker/dockerfile:1`**：`BUILDPLATFORM` 是 BuildKit 前端注入的自动变量，旧 buildx 环境若缺失该声明会静默把它当成空，`--platform=$BUILDPLATFORM` 退化成默认 target，java-builder 又会落回 QEMU。

**为什么 `FROM debian:trixie-slim AS runtime`（以及 `cups-builder`）不加 `--platform`**：runtime 阶段要装 LibreOffice/JRE/中文字体/打印驱动并真正被各架构的 Docker 节点拉取运行，`cups-builder` 编出的是**架构相关的原生二进制**，两者都必须跟随 `TARGETPLATFORM` 生成三份；锁 amd64 会让 arm64/armhf 节点拉到 amd64 层、QEMU 模拟整个 runtime，完全跑偏。

**Maven 为什么仍用 tarball 而不是 `apt install maven`**：虽然 host amd64 上 `apt install maven` 不会触发 QEMU 坑，但 Debian 包依赖 `dpkg triggers + update-alternatives` 更新软链（[carlossg/docker-maven#213](https://github.com/carlossg/docker-maven/issues/213)），换 base 镜像或升级系统时偶有兼容性问题；Apache tarball 的 `lib/` 自包含所有 jar，不依赖任何 OS 打包细节，一劳永逸。tarball URL 走 dlcdn.apache.org → archive.apache.org 的 fallback 链（前者只保留 current release，后者永久归档），升级 Maven 时只需改 `Dockerfile` 里的 `MAVEN_VERSION`。

## docker-compose 配置理由

`docker-compose.yml` 现在只有**一个** `cups` 服务（原来是 `cups` + `web` 两个），`image: hanxi/cups-web:latest`，`network_mode: host`——CUPS 直接占用宿主 `631`，Web 监听地址由 `LISTEN_ADDR` 环境变量决定（compose 默认 `:1180`，与旧桥接时代 `1180:8080` 映射保持一致）。关键配置及其理由：

| 配置 | 为什么 |
| --- | --- |
| `network_mode: host` + `hostname: CUPS` | issue #107：mDNS/DNS-SD 依赖局域网组播（5353/udp），桥接模式下组播出不去也进不来——容器发现不了局域网网络打印机（`dnssd://…._ipp._tcp.local`），手机也搜不到 AirPrint。host 网络下容器内自启的 avahi 直接在局域网工作。代价：无端口映射、宿主自装 avahi 会抢 5353 |
| `LISTEN_ADDR=:1180` | host 网络无端口映射，让 Web 直接监听宿主 1180，老用户书签 / 反代不用改（`cmd/server/main.go` 原生支持该环境变量） |
| `user: root` | 要跑 cupsd / lpadmin / dpkg，还要往系统路径写驱动文件 |
| `security_opt: [apparmor:unconfined]` | issue #91：PVE LXC 等环境下 `apparmor="DENIED" … comm="jobs.cgi"` 会导致打印失败；合并单容器后它同时也保护 LibreOffice / OFD 转换子进程 |
| `./.etc:/etc/cups`、`./.data:/data`、`./.uploads:/uploads` | CUPS 配置 / 数据库 / 上传文件持久化 |
| **`./.drivers:/opt/cups-drivers/data`** | **驱动快照持久化**。删掉这个卷 = 重启后丢失所有手动安装的第三方驱动，需要在 Web「驱动」页面重装一遍（见 [driver-management.md](driver-management.md)） |
| `/dev/bus/usb:/dev/bus/usb` + `device_cgroup_rules: ['c 189:* rmw']` | issue #81：USB 打印机热插拔。`devices:` 是启动时一次性绑定，打印机"后开机"时宿主 udev 新建的节点不会传播进容器；改成目录 bind-mount 才能实时反映新节点 |
| `/run/udev:/run/udev:ro` | 让 libusb 读到设备属性，改善识别（宿主无 `/run/udev` 时该挂载可删） |

> ⚠️ **`/run/dbus/system_bus_socket` 挂载已删除**（issue #107，取代 issue #94 的"借宿主 avahi"方案）：宿主没装 avahi-daemon 时，挂进来的 socket 文件会占住路径，entrypoint 里容器自己的 `dbus-daemon --system --fork` 起不来（老代码被 `|| true` 静默吞掉），avahi 跟着失效。host 网络下容器内自启的 dbus + avahi 已能直接发现和广播，不需要宿主任何服务。

## CI/CD

两条 workflow：

- **`build-release.yml`**：push 到任何分支和 tag 时交叉编译 7 平台二进制（`linux/amd64`、`linux/arm64`、`linux/armv7`、`linux/loong64`、`darwin/amd64`、`darwin/arm64`、`windows/amd64`）。v* tag 推送时创建正式 Release；master 分支推送时创建滚动更新 `dev` prerelease（git 标签 `dev`，避免用分支名现 git 标签引来重名冲突）。CI 使用的 Go 版本（`setup-go` 的 `go-version`）与 `go.mod` 保持一致（当前 `1.26`），升级 `go.mod` 时请同步 CI。
- **`docker-publish.yml`**：push 到 `master` 或 `v*` tag 时构建并推送镜像。**合并单容器后这里只剩一个 `build` job**（原来是 cups 镜像 + cups-web 镜像两个 job），单份 `Dockerfile` 出 `linux/amd64,linux/arm64,linux/arm/v7` 三架构的 `hanxi/cups-web`。v* tag 推送时打 `vX.Y.Z` + `latest` 两个镜像 tag；master 分支推送时打 `dev` 镜像 tag（避免 `master` 与分支/标签引用重名），`VERSION` build-arg 同步为 `dev`。缓存 scope 为 `cups-web`。开头还有一步 `Free disk space`（删 dotnet/android/CodeQL 缓存）——AIO 镜像把 CUPS 编译 + LibreOffice + 驱动生态塞进一份镜像后体积很大，GitHub runner 默认磁盘不够。

补充说明：

- `linux/armv7` 使用 `GOARCH=arm` + `GOARM=7`，覆盖树莓派 2/3、主流 ARM SBC 等 32 位硬浮点设备；matrix 里通过 `goarm` 字段声明，Build 步骤已把 `GOARM` 透传到 `env`（其他非 arm 目标此字段为空不生效）。
- `linux/loong64` 依赖 `modernc.org/sqlite` ≥ `v1.34`（`v1.29.0` 尚未支持 loong64 架构）。
- 由于全仓严格 `CGO_ENABLED=0`，新增其他 modernc 已支持的架构（`riscv64` / `s390x` / `ppc64le` 等）只需往 `build-release.yml` 的 matrix 里加一行 `goos/goarch/suffix`，无需额外工具链。

## 版本管理

使用 `bump-version.sh` 打 tag：

```bash
./bump-version.sh patch    # 默认
./bump-version.sh minor
./bump-version.sh major
```
