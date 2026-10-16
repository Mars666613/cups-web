# 🖨️ CUPS Web — 网页打印管理

<div align="center">

[![GitHub Release](https://img.shields.io/github/v/release/Mars666613/cups-web?style=flat-square&logo=github&color=blue)](https://github.com/Mars666613/cups-web/releases)
[![Docker Pulls](https://img.shields.io/docker/pulls/Mars666613/cups-web?style=flat-square&logo=docker)](https://hub.docker.com/r/Mars666613/cups-web)
[![Docker Image Size](https://img.shields.io/docker/image-size/Mars666613/cups-web/latest?style=flat-square&logo=docker&color=066da5)](https://hub.docker.com/r/Mars666613/cups-web)
[![GitHub Stars](https://img.shields.io/github/stars/Mars666613/cups-web?style=flat-square&logo=github)](https://github.com/Mars666613/cups-web/stargazers)
[![GitHub Forks](https://img.shields.io/github/forks/Mars666613/cups-web?style=flat-square&logo=github)](https://github.com/Mars666613/cups-web/network/members)
[![GitHub Issues](https://img.shields.io/github/issues/Mars666613/cups-web?style=flat-square&logo=github)](https://github.com/Mars666613/cups-web/issues)
[![GitHub Last Commit](https://img.shields.io/github/last-commit/Mars666613/cups-web?style=flat-square&logo=github)](https://github.com/Mars666613/cups-web/commits)
[![GitHub Downloads](https://img.shields.io/github/downloads/Mars666613/cups-web/total?style=flat-square&logo=github&color=success)](https://github.com/Mars666613/cups-web/releases)
[![License](https://img.shields.io/github/license/Mars666613/cups-web?style=flat-square&color=blue)](LICENSE)

[![Go Version](https://img.shields.io/github/go-mod/go-version/Mars666613/cups-web?style=flat-square&logo=go)](https://golang.org)
[![Vue 3](https://img.shields.io/badge/Vue-3.5-4FC08D?style=flat-square&logo=vue.js)](https://vuejs.org)
[![Vite](https://img.shields.io/badge/Vite-7-646CFF?style=flat-square&logo=vite)](https://vitejs.dev)
[![Nuxt UI](https://img.shields.io/badge/Nuxt%20UI-v4-00DC82?style=flat-square&logo=nuxt.js)](https://ui.nuxt.com)
[![Tailwind CSS](https://img.shields.io/badge/Tailwind-v4-38B2AC?style=flat-square&logo=tailwind-css)](https://tailwindcss.com)
[![CUPS](https://img.shields.io/badge/CUPS-IPP-orange?style=flat-square)](https://www.cups.org)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-16-4169E1?style=flat-square&logo=postgresql)](https://www.postgresql.org)

🏠 [GitHub](https://github.com/Mars666613/cups-web) • 🐳 [Docker Hub](https://hub.docker.com/r/Mars666613/cups-web) • 📖 [开发文档](AGENTS.md) • 💬 [上游微信群](https://github.com/hanxi/cups-web/issues/36)

</div>

> ### 关于本分支
>
> 本项目基于 **[hanxi/cups-web](https://github.com/hanxi/cups-web)** 二次开发。原项目由 [涵曦](https://github.com/hanxi) 创作，采用 MIT 许可证 —— 感谢原作者。
>
> 原项目面向「家用 / 小型办公室，把一台 USB 打印机变成网络打印服务」；本分支在其之上做了**较大改造**，功能方向转向**自助打印门店**：
>
> - **数据层**：SQLite → **PostgreSQL**，多台终端共享一个中心库
> - **计费与支付**：价目表、兜底单价、余额账本（只增不改的流水）、计费开关；支付宝授权登录与扫码支付
> - **自助终端**：刷卡取件机（`cmd/kiosk`，树莓派端）
> - **运维**：设备异常告警（缺纸 / 卡纸 / 门未开 / 耗材不足 / 设备不可达，推送到企业微信、钉钉、飞书）与管理员操作审计
> - **复印**：eSCL 扫描链路、PDF 空白页检测、驱动管理页
>
> 如果你要的是原项目那种「家用打印机共享」场景，请直接前往 [hanxi/cups-web](https://github.com/hanxi/cups-web)。

基于 CUPS 的网页版打印管理工具。通过浏览器上传文件、远程提交打印任务，支持多用户管理与打印记录追踪 —— 既能用于家庭和小型办公室，也支持计费、支付与自助取件终端，可直接投放到自助打印门店。

## 📸 界面预览

> 📷 **截图待补** —— 早先那一批已经删除：它们是**旧版界面**（管理端与打印记录页
> 后来都重构过），而且用的是**真实使用中的文件名与打印机型号**，不适合放进公开仓库。
>
> 想自己看效果的话直接部署一个（下面有 Docker 一行命令），比截图准。

<!-- 补好截图后替换掉上面这段：
<div align="center">
<table>
  <tr>
    <td align="center"><img src="screenshots/print.png" width="400" alt="在线打印"><br/><b>在线打印</b></td>
    <td align="center"><img src="screenshots/preview.png" width="400" alt="实时预览"><br/><b>实时预览</b></td>
  </tr>
  <tr>
    <td align="center"><img src="screenshots/admin.png" width="400" alt="管理后台"><br/><b>管理后台</b></td>
    <td align="center"><img src="screenshots/result.png" width="400" alt="取件提示"><br/><b>取件提示</b></td>
  </tr>
</table>
</div>
-->

## ✨ 功能特性

### 打印能力

- **多格式支持**：PDF、图片（JPG/PNG/GIF/HEIC）、Office 文档（doc/docx/xls/xlsx/ppt/pptx）、OFD、纯文本
- **自动转换**：Office 文档通过 LibreOffice 转 PDF；OFD 通过内置 Java 转换器（基于 ofdrw）转 PDF；文本/图片在服务端渲染为 PDF
- **多图片合并打印**：一次选择多张图片自动合并为一份 PDF
- **打印选项**：份数、单双面、彩色/黑白、纸张大小、纸张类型、页面方向、页码范围、缩放、镜像打印
- **实时预览**：支持 PDF 预览、纸张方向的可视化预览、页数估算

### 打印机驱动

镜像内预装了 Debian `printer-driver-all` 等通用驱动包，覆盖大部分常见打印机。对于特定品牌打印机，提供**按需手动安装**的驱动脚本和 Web 管理界面：

**预装通用驱动**（开箱即用）：

- `printer-driver-all`：Debian 维护的驱动 meta 包，包含 splix、c2050、m2300w、ptouch 等
- `printer-driver-cups-pdf`：虚拟 PDF 打印机
- `printer-driver-escpr`：Epson ESC/P-R 标准款（大部分 Epson 喷墨老机型）
- `printer-driver-foo2zjs`：ZjStream / Hiperc / OAKT 协议（部分 HP / Konica / Minolta 老款激光机）
- `printer-driver-brlaser`：Brother 老款激光机
- `foomatic-db-compressed-ppds` + `openprinting-ppds`：海量 PPD 库
- `hplip` + `hpijs-ppds` + `hp-ppd`：HP 全系打印套件
- `ipp-usb` + CUPS 内置 driverless：IPP Everywhere / AirPrint / Mopria 自动识别

**可选厂商驱动**（通过 Web 界面或命令行按需安装，安装后自动持久化）：

| 驱动 | 命令名 | 架构 | 适用机型 |
| --- | --- | --- | --- |
| Canon UFR II | `canon-ufr2` | amd64 / arm64 | i-SENSYS LBP/MF、imageCLASS、imageRUNNER 等 |
| Canon CAPT | `canon-capt` | 全架构 🔧 | LBP2900 / LBP2900B |
| HP LaserJet 1020 固件 | `hp-laserjet1020` | 全架构 | HP LaserJet 1020 / 1020 Plus |
| HP foo2zjs 固件 | `foo2zjs-firmware` | 全架构 🔧 | HP LaserJet 1000/1005/1018/P1005/P1006/P1505 |
| Epson ESC/P-R 2 | `escpr2` | amd64 / armhf / arm64 | ET-18100, L8050, L8160, WF-7840 等 |
| Epson 国行驱动 | `epson-cn` | 仅 amd64 | L380, L455 等国行机型 |
| Konica Minolta bizhub | `konica-bizhub` | amd64 / arm64 | bizhub 3000MF |
| Sharp PostScript | `sharp` | 全架构 | MX-C2622R 等 PostScript 打印机 |
| Gutenprint | `gutenprint` | amd64 / arm64 | 大量 Epson/Canon/HP 老机型 |

> 🔧 = 需要在容器内现场编译，耗时较长（几分钟到十几分钟，ARM 小主机更慢）。其他驱动是下载 + 解包安装，通常几十秒内完成。
>
> Epson ESC/P-R 2 在 amd64 / armhf 上直接安装预编译包，在 **arm64 上会回退到源码编译**，也需要几分钟。
>
> 「架构」列就是实际的硬限制：厂商没有提供对应架构的二进制时，Web 界面会把该驱动的「安装」按钮**禁用**并提示原因，不会让你点一个必然失败的按钮。

### 驱动管理（Web 界面）

驱动管理页面**仅管理员可见**（登录后导航栏的「驱动」入口）：

- **自动检测**：扫描 USB / 网络打印机，自动匹配推荐驱动
- **一键安装**：检测到打印机后一键安装驱动，并自动 `lpadmin` 添加到 CUPS（默认纸张设为 A4）
- **驱动列表**：查看所有可用驱动的安装状态、安装时间与支持架构，一键安装 / 卸载
- **上传自定义驱动**：支持上传 PPD 文件（`.ppd`）或 Debian 包（`.deb`），仅这两种扩展名
- **驱动持久化**：安装的驱动文件自动快照到 `.drivers` 持久卷，容器重建后自动恢复

#### 安装过程是异步的，请耐心等待

点击「安装」后接口会**立刻返回**，真正的安装在后台执行，页面上会出现一个进度卡片，每 2 秒刷新一次并**实时展示编译 / 安装日志**：

- 需要编译的驱动（Canon CAPT、HP foo2zjs 固件、arm64 上的 Epson ESC/P-R 2）可能要**几分钟到十几分钟**，页面一直显示滚动日志是正常的，**不是卡住了**
- 这期间请**不要刷新页面、不要重复点击**。刷新虽然不会中断后台安装，但页面上的实时日志会丢失，只能改用 `docker compose logs -f cups` 观察
- 后台任务的硬超时是 30 分钟，页面等待上限 35 分钟

#### 同一时刻只能跑一个驱动任务

apt / dpkg 本身持有全局锁，并发安装只会互相失败。因此后端限制**同时只允许一个驱动任务**（安装 / 卸载 / 一键安装并添加），任务进行中再发起第二个会被直接拒绝，并提示「已有驱动任务正在执行，请等待其完成后重试」；有任务在跑时上传 `.deb` 也会被同样拒绝。

#### `.ppd` 与 `.deb` 都会自动恢复

| 上传类型 | 容器重启后 |
| --- | --- |
| `.ppd` | ✅ 自动恢复（文件被快照到 `.drivers`，启动时还原到 `/usr/share/cups/model/custom`） |
| `.deb` | ✅ 自动重新安装（原件归档到 `.drivers/custom-deb/packages/`，启动时用 `dpkg -i` 装回来） |

`.deb` 的真正安装动作发生在包内的安装脚本里，光把文件拷回来并不能让驱动生效 —— 所以它走的是"归档整个包、重启时重新安装一遍"的路子，而不是逐个文件还原。重复安装是安全的：已经装上且版本不更旧的包会被跳过。

> 💡 如果某个上传的 `.deb` 始终装不上，它会在每次容器启动时重试一遍（日志里能看到告警）。想彻底移除它，删掉宿主 `./.drivers/custom-deb/packages/` 下对应的文件即可。

> ⚠️ **上传 `.deb` 的安全风险**：安装 `.deb` 时 dpkg 会以 **root 身份执行包内的安装脚本**，等价于在容器里执行任意代码，并且会改动容器的系统状态。这是有意保留给管理员的能力，但也意味着**管理员账号密码等同于容器的 root 凭据**。请只上传来源可信的 `.deb`，并且**不要在不可信的多人环境下开放管理员账号**（普通 `user` 角色看不到也调不到驱动接口）。每次上传都会把上传者用户名写进容器日志，便于事后审计。

> ⚠️ **驱动持久化依赖 `./.drivers` 卷**：手动安装的第三方驱动全部快照在这个目录里。**删掉它（或忘记挂这个卷）= 丢失所有手动安装的驱动**，重启后需要在「驱动」页面重新装一遍。备份时别漏了它。

也可以通过命令行安装驱动（命令行是同步执行的，会一直占用终端直到结束）：

```bash
# 查看可用驱动
docker exec cups driver-list

# 安装驱动
docker exec cups driver-install canon-ufr2

# 查看已安装驱动
docker exec cups driver-list --installed

# 卸载驱动
docker exec cups driver-remove canon-ufr2
```

### 用户与权限

- **多用户系统**：支持 `admin` / `user` 两种角色
- **默认管理员**：首次启动自动创建 `admin/admin`，`admin` 账号受保护无法被删除或重命名
- **打印记录**：完整保存每次打印的文件、页数、份数、双面/彩色选项、状态等

### 管理后台

- **用户管理**：创建、编辑、删除用户；修改角色与联系信息
- **打印记录查询**：可按用户名、时间范围过滤
- **数据保留策略**：按天数自动清理过期打印记录和对应文件（每小时巡检一次）

### 安全

- **Session 认证**：基于 Gorilla `securecookie`（加密 + 签名），密钥自动生成并持久化到数据库
- **CSRF 防护**：对所有非 GET/HEAD/OPTIONS 请求校验 `X-CSRF-Token`
- **密码安全**：bcrypt 加密存储

## 🛠️ 技术栈

- **后端**：Go 1.26 · Gorilla Mux · PostgreSQL（`pgx/v5` 的 `database/sql` 兼容层）
- **打印协议**：[OpenPrinting/goipp](https://github.com/OpenPrinting/goipp)（IPP）
- **前端**：Vue 3 · Vite 7 · [Nuxt UI v4](https://ui.nuxt.com/) · Tailwind CSS v4 · Vue Router（hash 模式）
- **文档转换**：LibreOffice（Office → PDF）· [ofdrw](https://github.com/ofdrw/ofdrw)（OFD → PDF，Java 21）
- **打印服务**：[CUPS](https://www.cups.org/)（源码编译 2.4.x，覆盖 apt 版本）

## 🚀 快速开始

提供两种部署方式：

- [Docker 部署](#docker-部署)（推荐，一键拉起 CUPS + Web）
- [二进制部署](#二进制部署)（适合已有 CUPS 服务的场景）

---

## Docker 部署

### 前置要求

- Docker 与 Docker Compose
- USB 打印机（若使用本地打印机）

### 1. 创建 `docker-compose.yml`

```yaml
services:
  cups:
    image: Mars666613/cups-web:latest
    container_name: cups
    user: root
    hostname: CUPS
    # mDNS/DNS-SD 依赖局域网组播，桥接模式下发现不了网络打印机、
    # AirPrint 也广播不出去，必须用 host 网络（issue #107）
    network_mode: host
    security_opt:
      - apparmor:unconfined
    environment:
      - CUPSADMIN=${CUPSADMIN:-print}
      - CUPSPASSWORD=${CUPSPASSWORD:-print}
      - TZ=${TZ:-Asia/Shanghai}
      # host 网络下 Web 直接监听宿主端口，默认 1180（与旧版端口映射一致）
      - LISTEN_ADDR=${LISTEN_ADDR:-:1180}
    volumes:
      - ./.etc:/etc/cups
      - ./.uploads:/uploads
      - ./.drivers:/opt/cups-drivers/data
      - /dev/bus/usb:/dev/bus/usb
      - /run/udev:/run/udev:ro
    device_cgroup_rules:
      - 'c 189:* rmw'
    # 特权模式：ARM / 嵌入式平台上 libusb 需要完整 sysfs 访问才能枚举 USB
    # 打印机（issue #110）。如果你确认 x86 平台不需要，可注释掉这行。
    privileged: true
    restart: unless-stopped
```

也可直接下载仓库内的 `docker-compose.yml`：

```bash
wget https://raw.githubusercontent.com/Mars666613/cups-web/main/docker-compose.yml
```

> 💡 镜像标签：`latest` = 最新正式发行版（v* tag）；`dev` = main 分支最新构建（每次 push 更新，尝鲜用）。`docker-compose.yml` 默认用 `latest`，想试 dev 版本时把 `image: Mars666613/cups-web:latest` 改成 `image: Mars666613/cups-web:dev` 即可。

### 2. 配置环境变量（可选）

在同目录创建 `.env`：

```bash
CUPSADMIN=print
CUPSPASSWORD=your_cups_password
TZ=Asia/Shanghai
```

### 3. 启动服务

```bash
docker compose up -d
```

### 4. 安装打印机驱动（按需）

大部分打印机靠镜像预装的通用驱动即可直接使用，只有特定品牌机型才需要这一步。

通过 Web 界面安装（推荐）：
1. 访问 <http://localhost:1180>，使用 `admin/admin` 登录
2. 点击导航栏「驱动」进入驱动管理页面（仅管理员可见）
3. 点击「扫描打印机」自动检测已连接的打印机
4. 对检测到的打印机点击「一键安装并添加」

> ⏳ 安装是**后台异步**执行的，页面上会实时滚动安装日志。需要编译的驱动可能要十几分钟，请不要刷新页面或重复点击；同一时刻只能有一个驱动任务在跑。详见 [驱动管理（Web 界面）](#驱动管理web-界面)。

或通过命令行安装：
```bash
docker exec cups driver-install canon-ufr2
```

### 5. 配置打印机

如果没有通过上面的自动检测添加打印机，也可以手动配置：

访问 CUPS 管理界面：<http://localhost:631>，使用 CUPS 管理员账号登录并添加打印机。

> ⚠️ **重要**：添加打印机后，必须在 CUPS 管理后台将其设为 **Shared（共享）** 状态，否则 Web 端无法发现该打印机。

### 6. 访问 Web

浏览器打开 <http://localhost:1180>，使用默认账号登录：

- 用户名：`admin`
- 密码：`admin`

> ⚠️ **首次登录请立即修改默认密码**。

---

## 二进制部署

适合已有 CUPS 服务的场景。

> ⚠️ **裸二进制部署的能力边界**：二进制里只有 Web 服务本身，**CUPS 需要你自己在宿主机上安装并配置好**（含打印机驱动）。镜像特有的功能在这里都不可用：
>
> - **驱动管理页面用不了**：驱动的安装 / 卸载脚本（`driver-install` 等）和持久化目录 `/opt/cups-drivers` 是**镜像内置**的。裸二进制环境下这些文件不存在，「驱动」页面里所有驱动都会显示为未安装、「安装」按钮被禁用并提示「当前镜像缺少该驱动的安装脚本」；如果强行调用接口，任务会以 `no such file or directory` 失败。请直接用宿主机的包管理器（`apt install printer-driver-*`）或 CUPS 管理界面装驱动。
> - **自动检测打印机**依赖 `lpinfo`（`cups-client` 包），缺失时「扫描打印机」会报 `failed to detect printers`。
> - **Office / OFD / PDF 标准化**依赖 LibreOffice、Java、Ghostscript，需要自行安装（见下文）。

### 1. 下载二进制

从 [GitHub Releases](https://github.com/Mars666613/cups-web/releases) 下载对应平台的二进制：

| 平台 | 架构 | 文件名 |
| --- | --- | --- |
| Linux | amd64 | `cups-web-linux-amd64` |
| Linux | arm64 | `cups-web-linux-arm64` |
| Linux | armv7 | `cups-web-linux-armv7` |
| Linux | loong64 | `cups-web-linux-loong64` |
| macOS | amd64 | `cups-web-darwin-amd64` |
| macOS | arm64 | `cups-web-darwin-arm64` |
| Windows | amd64 | `cups-web-windows-amd64.exe` |

```bash
wget https://github.com/Mars666613/cups-web/releases/latest/download/cups-web-linux-amd64
chmod +x cups-web-linux-amd64
```

### 2. 配置并运行

```bash
export CUPS_HOST=localhost:631
export DATABASE_URL='postgres://cupsweb:密码@127.0.0.1:5432/cupsweb?sslmode=disable'
export UPLOAD_DIR=./uploads
export LISTEN_ADDR=:8080

./cups-web-linux-amd64
```

或使用命令行参数（优先级高于环境变量）：

```bash
./cups-web-linux-amd64 -addr :8080
```

> ⚠️ **OFD 打印仅在 Docker 镜像中开箱即用**。二进制部署若需支持 OFD，需要另行安装 Java 运行时（镜像内为 Java 21）并把 `ofd-converter.jar` 放到 `/ofd-converter.jar`（或手动改源码中的路径）。

### 3. 访问 Web

浏览器打开 <http://localhost:8080>，使用 `admin/admin` 登录。

---

## ⚙️ 配置说明

### 环境变量

| 变量名 | 说明 | 默认值 |
| --- | --- | --- |
| `LISTEN_ADDR` | Web 服务监听地址 | `:8080` |
| `DATABASE_URL` | PostgreSQL 连接串，**必填**（没有 SQLite 回退） | 无 |
| `DB_MAX_CONNS` | 每个实例向数据库开多少条连接。所有实例加起来不能超过服务端 `max_connections` | `10` |
| `UPLOAD_DIR` | 上传文件目录 | `uploads` |
| `CUPS_HOST` | CUPS 服务地址（Docker 内默认 `localhost`） | `localhost` |
| `CUPSADMIN` | CUPS 管理员用户名 | `print` |
| `CUPSPASSWORD` | CUPS 管理员密码 | `print` |
| `TZ` | 时区 | `Asia/Shanghai` |
| `COOKIE_SECURE` | 会话 / CSRF cookie 的 `Secure` 属性：`auto`（缺省，按每个请求的实际协议判定）/ `true` 恒开 / `false` 恒关 | `auto` |
| `TRUSTED_ORIGINS` | 额外放行的来源，逗号分隔（形如 `https://print.example.com`）。仅在跨源防护误拦时需要 | 空 |
| `PRINTER_HOST_ALLOWLIST` | 打印机 URI 的主机白名单，逗号分隔。收紧 SSRF 面 | 空（不限制） |
| `PRINTER_BLOCK_PRIVATE` | `true` 时拒绝指向私网地址的打印机 URI | `false` |
| `OFD_CONVERTER_JAR` | OFD → PDF 转换器 jar 路径 | `/ofd-converter.jar` |

> 💡 `.env.example` 里带注释的是数据库与支付宝配置。`CUPSADMIN` / `CUPSPASSWORD` / `TZ` 在镜像里都已有内置默认值，不写也能启动。
>
> ⚠️ **`DATABASE_URL` 是必填的** —— 数据库已改为外部的 PostgreSQL，程序不再自带 SQLite。没配这一项容器会直接启动失败并在日志里打印格式示例。
>
> 💡 **建库时要指定 `C` collation**：`CREATE DATABASE cupsweb LC_COLLATE 'C' LC_CTYPE 'C' TEMPLATE template0;`。不指定的话，若集群默认是 ICU 非确定性 collation，用户名会变成大小写不敏感（能用 `ADMIN` 登录）。表结构不用手动建，程序启动时会自动执行。
>
> 💡 `UPLOAD_DIR` 的默认值是**相对路径**，镜像内工作目录是 `/`，所以容器里实际落在 `/uploads/`，正好对应下面的卷映射，通常不需要显式设置。
>
> 💡 `CUPS_HOST` 单容器化后默认就是 `localhost`（cupsd 与 Web 同容器），一般无需设置；只有把 Web 指向**另一台机器**上的 CUPS 时才需要，可写 `host` 或 `host:port`（省略端口时自动补 `631`）。
>
> 💡 `COOKIE_SECURE` 缺省的 `auto` 会在直连 HTTPS（`r.TLS`）或反代上报 `X-Forwarded-Proto: https` 时自动给 cookie 加 `Secure`，HTTP 内网部署行为不变。若代理错误地上报了 `https` 而浏览器实际走 HTTP，会出现「登录后立刻掉线」，此时显式设 `COOKIE_SECURE=false`。
>
> 💡 `TRUSTED_ORIGINS` 通常**不需要**设置。反向代理下的跨源误拦已由 `X-Forwarded-Host` 自动处理，详见 [docs/reverse-proxy.md](docs/reverse-proxy.md)。

### 命令行参数

| 参数 | 说明 |
| --- | --- |
| `-addr` | 监听地址，优先级高于 `LISTEN_ADDR` |

### 默认端口

- CUPS：`631`（管理界面 + IPP 协议）。Docker 部署使用 host 网络模式（[Issue #107](https://github.com/hanxi/cups-web/issues/107)），cupsd 直接占用宿主机 `631`
- Web：二进制部署默认监听 `8080`；Docker 部署由 `LISTEN_ADDR` 环境变量决定，`docker-compose.yml` 默认 `:1180`（与旧版桥接时代的端口映射保持一致）

### 数据持久化目录

Docker 默认卷映射：

| 宿主机路径 | 容器路径 | 说明 |
| --- | --- | --- |
| `./.etc` | `/etc/cups` | CUPS 配置（打印机、PPD 等） |
| `./.uploads` | `/uploads` | 上传的原始文件与转换后 PDF |
| `./.drivers` | `/opt/cups-drivers/data` | 手动安装的打印机驱动快照（⚠️ 删除即丢失全部手动装的驱动） |

> ⚠️ **数据库不在容器里。** 自 v0.2.15 起改为连接外部 PostgreSQL（`DATABASE_URL`），
> 原先的 `./.data:/data` 卷已删除 —— 容器里不再有任何东西写 `/data`。
> 宿主机上的 `./.data` 目录可以留着不管（里面只有旧的 `cups-web.db`），
> 确认不需要回滚之后再删。

此外还有两个**非数据类**的挂载，用于 USB 打印机识别与热插拔：

| 宿主机路径 | 容器路径 | 说明 |
| --- | --- | --- |
| `/dev/bus/usb` | `/dev/bus/usb` | 以**目录**方式挂载（而不是 `devices:`），这样打印机后开机时新建的设备节点能实时传播进容器 |
| `/run/udev` | `/run/udev`（只读） | 让 libusb 读到设备属性，改善识别；宿主机没有该目录时可以删掉这一行 |

> ⚠️ **从旧版升级注意**：旧版 compose 还挂载了 `/run/dbus/system_bus_socket`（[Issue #94](https://github.com/hanxi/cups-web/issues/94)，借用宿主机 avahi 广播 AirPrint），新版已**移除**（[Issue #107](https://github.com/hanxi/cups-web/issues/107)）：宿主机没装 avahi-daemon 时，该挂载会让容器内自己的 dbus/avahi 起不来，网络打印机发现与 AirPrint 广播全部失效。host 网络模式下容器内自启的 avahi 已能直接在局域网发现和广播，无需依赖宿主机任何服务。请同步删除你本地 compose 里的这一行。

### 其他 compose 选项说明

| 选项 | 为什么需要 |
| --- | --- |
| `network_mode: host` | mDNS/DNS-SD 依赖局域网组播（5353/udp），桥接模式下组播出不去也进不来：容器发现不了局域网网络打印机，手机也搜不到 AirPrint（[Issue #107](https://github.com/hanxi/cups-web/issues/107)）。代价是无法自定义端口映射，CUPS 固定占用宿主 `631`、Web 由 `LISTEN_ADDR` 决定；宿主若自装 avahi-daemon 会与容器内 avahi 抢 5353 端口，且宿主 avahi 无法替容器广播（两条独立 D-Bus 总线），需停掉宿主侧 |
| `hostname: CUPS` | avahi 以 `CUPS.local` 在局域网广播 AirPrint / IPP Everywhere 服务 |
| `LISTEN_ADDR=:1180` | host 网络下没有端口映射，让 Web 直接监听宿主 `1180`，与旧桥接时代一致，书签 / 反代配置无需改动 |
| `user: root` | 容器内要运行 cupsd、`lpadmin`、`dpkg`（驱动安装），还要往 `/usr/lib/cups`、`/usr/share/ppd` 等系统路径写驱动文件 |
| `security_opt: [apparmor:unconfined]` | 解除 AppArmor 限制（[Issue #91](https://github.com/hanxi/cups-web/issues/91)）。PVE (Proxmox VE) LXC 等环境下会出现 `apparmor="DENIED"` 导致打印失败；单容器化后它同时也保护 LibreOffice / OFD 转换子进程不被拦截 |
| `device_cgroup_rules: ['c 189:* rmw']` | 放开 USB 字符设备（major 189）的 cgroup 权限（[Issue #81](https://github.com/hanxi/cups-web/issues/81)） |
| `privileged: true` | 特权模式。ARM / 嵌入式平台（树莓派、Amlogic、各类 TV Box 跑 Armbian）上 libusb 需要完整 sysfs 才能枚举 USB 打印机（[Issue #110](https://github.com/hanxi/cups-web/issues/110)）。x86 平台若介意安全隔离可注释掉，仅靠上方 `device_cgroup_rules` 通常已足够 |

---

## 📖 使用指南

### 支持的文件格式

| 类型 | 扩展名 | 处理方式 |
| --- | --- | --- |
| PDF | `.pdf` | 直接打印 |
| 图片 | `.jpg` `.jpeg` `.png` `.gif` `.heic` | 转换为 PDF（支持多张合并） |
| Office | `.doc` `.docx` `.xls` `.xlsx` `.ppt` `.pptx` | 通过 LibreOffice 转换 |
| OFD | `.ofd` | 通过 ofdrw 转换 |
| 文本 | `.txt` `.md` `.html` | 服务端渲染为 PDF |

### 打印流程

1. 选择打印机
2. 上传文件（支持多图）
3. 预览转换后的 PDF、调整打印参数
4. 确认提交，系统自动落库并下发到 CUPS

### 管理员功能

- **用户管理**：创建、编辑、删除；默认 `admin` 账号不可删除、不可改名、角色固定
- **打印记录**：查看全站记录，按用户名/日期过滤，下载原始文件
- **系统设置**：数据保留天数（`0` 表示永久保留）
- **驱动管理**：自动检测打印机、安装/卸载驱动、上传自定义 PPD/deb（后台异步执行 + 实时日志，同时只跑一个任务）

---

## 🔧 进阶配置

### 反向代理与 HTTPS

通过反向代理（例如 Nginx）对外提供服务：

```nginx
server {
    listen 443 ssl;
    server_name example.com;

    ssl_certificate     /path/to/cert.pem;
    ssl_certificate_key /path/to/key.pem;

    # 打印文件可能很大，不放开会 413 Request Entity Too Large
    client_max_body_size 200m;

    location / {
        proxy_pass http://localhost:1180;

        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-Host  $host;

        # 文档转换与驱动安装耗时较长，默认 60s 会中途断开
        proxy_read_timeout    300s;
        proxy_send_timeout    300s;
        proxy_request_buffering off;
    }
}
```

> ⚠️ **上面五个 `proxy_set_header` 都不能省。** 漏掉 `Host` / `X-Forwarded-Host` 会让跨源防护把登录请求当成跨站请求拒掉，页面上表现为**登录提示密码错误**（issue #99）；漏掉 `X-Forwarded-Proto` 会让 HTTPS 部署下的 cookie 拿不到 `Secure` 标记。
>
> 📖 反代下的登录失败排查、Caddy / Traefik 配置、子路径挂载的限制，见 [docs/reverse-proxy.md](docs/reverse-proxy.md)。

### 修改端口

新版默认 host 网络模式（[Issue #107](https://github.com/hanxi/cups-web/issues/107)），**没有端口映射可改**：

- **Web 端口**：改 compose 里的 `LISTEN_ADDR`（默认 `:1180`），或在 `.env` 中设置：
  ```bash
  LISTEN_ADDR=:你的Web端口
  ```
- **CUPS 端口**：cupsd 固定监听 `631`。确需修改时编辑 `./.etc/cupsd.conf` 中的 `Listen 631` 后重启容器（不推荐，IPP/AirPrint 生态默认按 631 工作）
- 若坚持桥接模式（放弃网络打印机发现与 AirPrint 广播），可改回 `ports: ["你的CUPS端口:631", "你的Web端口:8080"]` 并删除 `network_mode: host` 与 `LISTEN_ADDR`

### 数据备份

数据库现在是**外部 PostgreSQL**，用 `pg_dump` 备份，不再有可以 `cp` 的库文件：

```bash
pg_dump "$DATABASE_URL" -Fc -f cupsweb-$(date +%F).dump     # 数据库
tar -czf uploads-backup.tar.gz ./.uploads/                   # 上传的文件
tar -czf cups-config-backup.tar.gz ./.etc/                   # CUPS 配置
tar -czf drivers-backup.tar.gz ./.drivers/                   # 驱动快照
```

> ⚠️ **上传的文件仍在本机**，不在数据库里 —— 只备份数据库的话，历史订单的原始文件与转换后的 PDF 会丢。两边都要备。

> 💡 `.drivers` 一定要一起备份——它是所有手动安装的第三方驱动的唯一副本。注意它是**按架构**快照的：把 amd64 上备份的 `.drivers` 恢复到 arm64 机器上，驱动列表会提示「安装于 amd64，与当前架构不符，建议卸载重装」。

---

## ❓ 常见问题

### 忘记管理员密码怎么办？

以前的办法是删掉 SQLite 文件让程序重建默认账号 —— 数据库搬到 PostgreSQL 之后那个办法没有了（删库可不是删一个文件那么轻）。用 SQL 直接重置该账号的密码哈希：

```bash
# 1. 生成一个 bcrypt 哈希（把 你的新密码 换掉）
htpasswd -bnBC 10 "" '你的新密码' | tr -d ':\n' | sed 's/^\$2y\$/\$2a\$/'

# 2. 写进数据库
psql "$DATABASE_URL" -c \
  "UPDATE users SET password_hash='<上一步的输出>' WHERE username='admin';"
```

> 💡 `$2y$` 要改成 `$2a$`：Go 的 bcrypt 只认 `$2a$`，而 `htpasswd` 输出 `$2y$`，两者哈希算法一致、只是前缀不同。
>
> ⚠️ `admin` 是受保护账号，不能删也不能改名，但**可以改密码**（走管理界面或上面这条 SQL 都行）。

### Web 端看不到打印机？

1. 检查打印机是否在 CUPS 中正常列出（<http://localhost:631>）
2. 确认打印机设置为 **Shared**
3. 重启容器：`docker compose restart cups`

### 打印机后开机就识别不到？（USB 热插拔）

使用最新的 `docker-compose.yml` 即可支持热插拔（volume 目录挂载 `/dev/bus/usb` + `device_cgroup_rules` + `privileged: true`）。如果在 x86 平台介意安全隔离，可注释掉 `privileged: true`，仅靠 `device_cgroup_rules` 通常已足够。

### AirPrint 搜不到打印机 / 发现不了局域网网络打印机？

手机 / iPad 的 AirPrint 广播、以及 CUPS 发现局域网网络打印机（`dnssd://xxx._ipp._tcp.local`），都依赖 mDNS 组播（5353/udp）。Docker 默认的 bridge 网络下组播出不去也进不来，因此新版 `docker-compose.yml` 默认使用 `network_mode: host`，由容器内自启的 avahi-daemon 直接在局域网广播和发现（[Issue #107](https://github.com/hanxi/cups-web/issues/107)）。

排查步骤：

1. **确认使用新版 compose**：必须有 `network_mode: host`、且**没有** `ports:` 和 `/run/dbus/system_bus_socket` 挂载。
   > ⚠️ 旧版为借用宿主机 avahi 广播而挂载了宿主机 D-Bus socket（[Issue #94](https://github.com/hanxi/cups-web/issues/94)）。宿主机没装 avahi-daemon 时，该挂载反而让容器内自己的 dbus/avahi 起不来，发现与广播全部失效——升级时务必删掉这一行。
2. **看容器日志**：`docker logs cups | grep -i 'dbus\|avahi'`。新版 entrypoint 在 dbus/avahi 启动失败时会打 `WARN`，按提示排查。
3. **宿主机自装了 avahi-daemon？** 容器内 avahi 会与它抢 5353 端口而起不来。注意宿主机 avahi **无法**替容器内的 CUPS 广播或发现（容器与宿主机是两条独立的 D-Bus 总线），必须停掉宿主机侧：`sudo systemctl disable --now avahi-daemon`，然后重启容器。
4. **路由器/交换机屏蔽组播**：部分 AP 开了「组播增强/IGMP 隔离」会丢 mDNS 包，尝试关闭或在同一 AP 下复测。

验证容器内 avahi 是否正常工作：

```bash
# 应能列出局域网打印机的 _ipp._tcp / _pdl-datastream._tcp 等服务
docker exec cups avahi-browse -art
# 应能解析出 .local 域名（依赖镜像内的 libnss-mdns）
docker exec cups getent hosts CUPS.local
```

### 安装的驱动丢失了？

确认 `docker-compose.yml` 中已配置驱动持久化卷：
```yaml
volumes:
  - ./.drivers:/opt/cups-drivers/data
```

驱动数据保存在 `.drivers` 目录中，容器重建后自动恢复。**删掉该目录就等于丢失全部手动安装的驱动**，需要在「驱动」页面重新安装。

通过「上传自定义驱动」装的 `.ppd` 与 `.deb` 都会自动恢复：`.ppd` 按文件还原，`.deb` 则归档整个包、启动时用 `dpkg -i` 重新安装一遍（已装且版本不更旧的会跳过）。界面上会列出装过哪些包。

### 驱动安装一直在转圈，是卡住了吗？

大概率没有。安装是后台异步执行的，页面上的进度卡片每 2 秒刷新一次并实时显示日志：

- 需要编译的驱动（Canon CAPT、HP foo2zjs 固件、arm64 上的 Epson ESC/P-R 2）在 ARM 小主机上十几分钟都属正常
- 只要日志还在增长就说明在正常推进；**不要刷新页面或重复点击**
- 如果不放心，另开终端看容器日志：`docker compose logs -f cups`
- 后台任务硬超时 30 分钟，页面等待上限 35 分钟；真的超时会明确报错

### 提示「已有驱动任务正在执行」？

apt / dpkg 有全局锁，所以同一时刻只允许一个驱动任务（安装 / 卸载 / 一键安装并添加）；有任务在跑时上传 `.deb` 也会被拒绝。等当前任务在页面上显示完成后再操作即可。

### 某个驱动的「安装」按钮是灰的？

两种原因，鼠标悬停在按钮上会显示具体提示：

- **当前架构 xxx 不支持**：厂商没有提供该 CPU 架构的二进制（例如 Epson 国行驱动仅 amd64、Canon UFR II 仅 amd64/arm64、Gutenprint 在 armhf 上无包）。请改用预装的通用驱动或 driverless/IPP Everywhere
- **当前镜像缺少该驱动的安装脚本**：通常出现在裸二进制部署下（驱动脚本是镜像内置的），或使用了裁剪过的镜像

### 在 PVE LXC 容器中部署时失败？（AppArmor DENIED）

在服务中添加 `security_opt: [apparmor:unconfined]`（最新 `docker-compose.yml` 已包含）。PVE LXC 容器还需开启 `Nesting` 和 `Keyctl` 功能。

### Office / OFD 转换失败？

- 转换有 **60 秒超时**，复杂文档可能超时
- 确认文档本身未损坏；可尝试本地先另存为 PDF 再上传
- 查看日志：`docker compose logs -f cups`

### 如何查看日志？

```bash
docker compose logs -f cups
```

---

## 🤝 贡献

欢迎提 Issue 和 Pull Request。开发者文档请参阅 [AGENTS.md](AGENTS.md)。

## 📈 Star History

<div align="center">

[![Star History Chart](https://star-history.dera.page/svg?repos=Mars666613/cups-web&type=Date)](https://star-history.dera.page/#Mars666613/cups-web&Date)

如果这个项目对你有帮助，欢迎点击右上角的 ⭐ **Star** 让更多人发现它！

</div>

## ⭐ Star 项目

如果这个项目对你有帮助，欢迎点击右上角的 ⭐ Star 按钮，让更多人发现这个项目。

## 📄 许可证

本项目采用 MIT 许可证，详见 [LICENSE](LICENSE)。

原项目 [hanxi/cups-web](https://github.com/hanxi/cups-web) 由涵曦创作，同样以 MIT 许可证发布。
本分支在其基础上的修改部分，版权归 xinghuo（GitHub: [@Mars666613](https://github.com/Mars666613)）所有。按 MIT 许可证的要求，
[LICENSE](LICENSE) 中同时保留了原作者与本分支的版权声明。
