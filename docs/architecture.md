# 架构与技术栈深度说明

> 本文档是 [AGENTS.md](../AGENTS.md) 的补充，收录技术栈选型理由与外部依赖的坑位说明。规则性速查表请回 AGENTS.md。

## 后端技术栈

| 组件 | 版本 / 说明 |
| --- | --- |
| Go | 1.26（见 `go.mod`） |
| HTTP 路由 | `github.com/gorilla/mux` |
| 会话管理 | `github.com/gorilla/securecookie` |
| 数据库 | `modernc.org/sqlite`（纯 Go，无 CGO） |
| 打印协议 | `github.com/OpenPrinting/goipp`（IPP） |
| PDF 解析 | `rsc.io/pdf`（页数读取）、`github.com/phpdave11/gofpdf`（PDF 生成） |
| 图像缩放 | `golang.org/x/image/draw`（CatmullRom，用于大图下采样） |
| 加密 | `golang.org/x/crypto/bcrypt` |

## 前端技术栈

| 组件 | 版本 / 说明 |
| --- | --- |
| 框架 | Vue 3.5 + Vue Router（hash 模式） |
| 构建 | Vite 7 |
| UI 库 | `@nuxt/ui` v4（含自带的 Tailwind 主题） |
| 样式 | Tailwind CSS v4 |
| 图标 | `@iconify-json/lucide` |
| PDF 处理 | `pdfjs-dist`（预览，PDF 生成统一交由后端 `/api/convert`） |
| HEIC 兼容 | `heic2any` |
| 包管理 | 本地开发推荐 Bun（`bun install` / `bun run dev`）；CI 与 Docker 镜像统一用 npm（`npm ci` + `npm run build`），以同时覆盖 `linux/arm/v7` 架构——Bun 官方不支持 32-bit ARM（见 [docker-build.md](docker-build.md)） |

## 外部依赖及坑位

### CUPS

打印服务，通过 IPP 通信。AIO 镜像里由 `scripts/build/install-cups.sh` 从 OpenPrinting 源码编译（当前 `CUPS_VERSION=2.4.19`）后 overlay 覆盖 apt 版。

### LibreOffice（headless）

Office 文档 → PDF；同时作为 PDF 标准化的兜底链路。

**⚠️ 依赖可写 `HOME`**：Dockerfile 显式 `ENV HOME=/root` + 预建 `~/.config/libreoffice`，拿不到可写 HOME 时 `--convert-to pdf` 会返回 0 但不产出 PDF（静默失败，极难排查）。

### Java 21 + `ofd-converter.jar`

OFD 文档 → PDF（基于 `ofdrw`）。构建期 `openjdk-21-jdk-headless`，运行期 `openjdk-21-jre`。

### Ghostscript (`gs`)

PDF 标准化首选链路：统一降级到 PDF 1.4 兼容性（主要面向 CUPS/老打印机对新版 PDF 解析能力弱的场景）。

**⚠️ `gs pdfwrite` 会对原 PDF 的每个字体对象强行加上 subset 前缀（`CCGWER+` 之类 6 位随机码）并重建字体字典**，对"空壳 Type0 字体 + `UniGB-UCS2-H` 外部 CMap"（Acrobat 导出的准考证/国标表格最常见的形态）是**破坏性改造**：原 PDF 的 `/BaseFont /#ba#da#cc#e5`（宋体 GBK 字节转义）会被改写成 `/BaseFont /BPCXJX+#cb#ce#cc#e5`，让 pdf.js 等渲染器误以为有内嵌字形可用、走内嵌路径却拿不到真实 FontFile，字宽表 vs 字形度量对不上导致"先正确一闪、再错位挤压"。因此该链路**不是 PDF 预览乱码的解药**，只在 CUPS 驱动确认无法解析原字体字典时才有收益。

本地 macOS 需要 `brew install ghostscript`；Docker 镜像里给 gs 配了**三层中文字体兜底**（见 `Dockerfile` 注释）：

1. `docker-fonts/cidfmap.local` 把 GBK 字节 BaseFont（宋/黑/楷/仿宋 × Regular/Bold，共 8 条）精准映射到 `arphic-uming` / `arphic-ukai` / `wqy-zenhei` 这三套**纯 TrueType** 字体（用户放了 `simsun.ttf` 等 Windows 字体时构建期 `sed` 换成真实字体），构建期同时装到 `/etc/ghostscript/cidfmap.local` 与 gs 的 `Resource/Init/cidfmap`（后者被 gs 启动时自动加载，详见 [pdf-pipeline.md](pdf-pipeline.md) cidfmap 小节）；
2. `fonts-droid-fallback` 作为 cidfmap 未命中时的 Adobe-GB1 CID 兜底（Debian 把 gs 依赖的 `DroidSansFallback.ttf` 剥离到独立包）；
3. `fonts-noto-cjk` 等 Unicode 字形包仅服务 LibreOffice 渲染 Office 文档，不参与 CIDFSubst 路径。

之所以只用 arphic/wqy 而不用 Noto CJK OTC，是因为 gs 10.x 对 CFF-based OpenType Collection 的 CIDFont 子字体索引偶有坑，纯 TrueType 最稳。

### `dpkg` / `apt-get`

运行时安装第三方打印驱动（见 [driver-management.md](driver-management.md)）。

**⚠️ runtime 镜像不含 `dpkg-dev`**，所以脚本里只能用 `dpkg --print-architecture`，不能用 `dpkg-architecture`。
