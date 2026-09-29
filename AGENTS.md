# CUPS Web 开发者指南

本文档面向开发者，介绍项目架构、API、开发流程与扩展方式。用户文档请参阅 [README.md](README.md)。

> 📚 **深度文档**：原理说明、故障案例与历史决策已移至 [docs/](docs/README.md)，本文件只保留可快速扫读的规则与契约。

## 📦 项目概述

- **项目定位**：基于 CUPS 的 Web 打印管理工具，前后端分离
- **技术栈**：Go 1.26（后端）+ Vue 3（前端）+ SQLite（存储）+ IPP（打印协议）
- **部署形态**：单二进制（前端 `go:embed`）连接外部 CUPS；**单容器（AIO）Docker 镜像**（`cupsd` + `cups-web` 同容器，内置 LibreOffice + Java 21 + OFD 转换器 + Ghostscript + 打印驱动生态）

> ⚠️ **历史形态提示**：仓库曾经是「cups 镜像 + cups-web 镜像」双容器（`cups/` 目录），已在合并提交里删除。现在只有根目录的一份 `Dockerfile` / `entrypoint.sh`，构建脚本在 `scripts/build/`、驱动脚本在 `scripts/driver/`。

## 🛠️ 技术栈

### 后端

| 组件 | 说明 |
| --- | --- |
| Go 1.26 | 见 `go.mod` |
| `gorilla/mux` | HTTP 路由 |
| `gorilla/securecookie` | 会话管理 |
| `modernc.org/sqlite` | 纯 Go SQLite，无 CGO |
| `OpenPrinting/goipp` | IPP 协议 |
| `rsc.io/pdf` + `phpdave11/gofpdf` | PDF 解析 / 生成 |
| `golang.org/x/image/draw` | 大图下采样（CatmullRom） |
| `golang.org/x/crypto/bcrypt` | 密码哈希 |

### 前端

| 组件 | 说明 |
| --- | --- |
| Vue 3.5 + Vue Router | hash 模式 |
| Vite 7 | 构建 |
| `@nuxt/ui` v4 + Tailwind CSS v4 | UI / 样式 |
| `pdfjs-dist` | 预览（PDF 生成由后端 `/api/convert` 负责） |
| Bun（本地）/ npm（CI + Docker） | 包管理。npm 用于覆盖 `linux/arm/v7`（Bun 不支持 32-bit ARM） |

### 外部依赖

CUPS（IPP 通信）、LibreOffice（Office → PDF）、Java 21 + `ofd-converter.jar`（OFD → PDF）、Ghostscript（PDF 标准化）、`dpkg`/`apt-get`（运行时驱动安装）。

> 各依赖的坑位说明（LibreOffice 可写 HOME、gs 字体破坏性改造、runtime 无 `dpkg-dev`）见 [docs/architecture.md](docs/architecture.md)。

## 📁 项目结构

```text
cups-web/
├── cmd/server/                    # 后端主程序
│   ├── main.go                    # 入口与路由注册
│   ├── app.go                     # 全局变量
│   ├── bootstrap.go               # 默认 admin 初始化
│   ├── auth_handlers.go           # 登录 / 登出 / session / csrf
│   ├── login_limiter.go           # 登录失败限流
│   ├── admin_handlers.go          # 管理员：用户 / 设置 / 清理
│   ├── user_handlers.go           # /api/me
│   ├── print_handlers.go          # /api/print（主打印入口）
│   ├── print_records_handlers.go  # 打印记录查询 / 下载 / 重打
│   ├── printer_info_handler.go    # 打印机属性查询
│   ├── convert_handler.go         # /api/convert
│   ├── convert_utils.go           # LibreOffice / OFD 转换工具
│   ├── compose_handler.go         # /api/compose（多页拼版）
│   ├── estimate_handler.go        # /api/estimate（预估页数）
│   ├── driver_handlers.go         # /api/admin/drivers/* + 后台任务
│   ├── driver_registry.go         # 驱动注册表
│   ├── file_utils.go              # 文件保存 / 类型识别 / 页数
│   ├── pdf_utils.go               # 图片 / 文本 → PDF
│   ├── pdf_compose.go             # 多页拼版
│   ├── pdf_reorder.go             # 页序重排 + 测试
│   ├── watermark.go               # 水印
│   ├── pdf_normalize.go           # PDF 标准化管线
│   ├── fonts.go                   # 中文字体加载
│   ├── maintenance.go             # 后台维护任务
│   ├── billing.go                 # 计费开关 / 价目加载
│   ├── order_handlers.go          # 下单（单/多文件）+ 订单查询 / 取消
│   ├── order_prepare.go           # 订单专用转换流水线（printHandler 的孪生）
│   ├── order_reprint.go           # 重打转订单
│   ├── order_print.go             # startPrintForOrder / 失败退款
│   ├── order_worker.go            # 订单巡检 + 主动查单轮询
│   ├── payment.go                 # 渠道注册表 + 支付宝配置
│   ├── payment_handlers.go        # settlePayment / 回调 / 查单
│   ├── billing_handlers.go        # /api/prices、钱包
│   ├── admin_billing_handlers.go  # 管理端价目 / 订单 / 退款 / 调额
│   └── version.go                 # 构建期版本号
├── internal/
│   ├── auth/session.go            # securecookie 会话 + CSRF
│   ├── pricing/                   # 计价内核（纯计算，无 DB/HTTP）
│   ├── payment/                   # 支付渠道抽象 + mock + 支付宝（wap.pay / 当面付）
│   ├── middleware/                 # csrf / security 中间件
│   ├── ipp/                       # IPP 客户端 + URI 校验
│   ├── server/static.go           # 静态资源嵌入（SPA fallback）
│   └── store/                     # 数据层（users / prints / settings）
├── frontend/                      # Vue 3 前端（go:embed dist）
├── ofd-converter/                 # Java OFD → PDF
├── scripts/
│   ├── build/install-cups.sh      # 源码编译 CUPS
│   └── driver/                    # 驱动管理命令 + 安装脚本 + capture-debs.sh（apt 钩子）
├── docker-fonts/                  # 构建期字体与 gs/fontconfig 配置
├── entrypoint.sh                  # AIO 容器启动脚本
├── Dockerfile                     # 五阶段构建
├── docker-compose.yml             # 单服务 AIO
└── Makefile                       # 构建脚本
```

## 🔌 HTTP API

所有接口以 `/api` 为前缀。除登录/登出/csrf/session 外均需 `RequireSession` + `ValidateCSRF`；管理员接口再叠加 `RequireAdmin`。

> **CSRF 约定**：登录成功后下发 `csrf_token` Cookie（非 HttpOnly）；前端非 GET 请求带 `X-CSRF-Token` 头。

### 公开接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/api/login` | 登录 |
| POST | `/api/logout` | 登出 |
| GET | `/api/csrf` | 刷新 csrf token |
| GET | `/api/session` | 查询会话 |
| GET | `/api/version` | 构建版本号 |

### 已登录用户接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/me` | 当前用户信息 |
| GET | `/api/printers` | 列出打印机 |
| GET | `/api/printer-info?uri=<uri>` | 打印机属性 |
| POST | `/api/estimate` | 估算页数 |
| POST | `/api/convert` | 文档 → PDF（单文件 `file` / 多图 `files`） |
| POST | `/api/print` | 提交打印 |
| POST | `/api/compose` | 多页拼版 |
| GET | `/api/print-records` | 打印记录 |
| GET | `/api/print-records/{id}/file` | 下载原始文件 |
| POST | `/api/print-records/{id}/reprint` | 重打（计费开启时转订单，见 [计费与支付](#-计费与支付)） |
| GET | `/api/prices` | 价目表（用户侧只读） |
| GET | `/api/wallet` | 余额 + 流水 + 可用支付方式 |
| POST | `/api/wallet/topup` | 发起充值 |
| POST | `/api/wallet/refund` | 余额原路退款（body `{"amountCents":N}`） |
| POST | `/api/orders` | 下单（单文件 `file` / 多文件 `files`） |
| GET | `/api/orders` | 我的订单 |
| GET | `/api/orders/batch/{batchId}` | 批次状态（支付弹窗轮询） |
| POST | `/api/orders/batch/{batchId}/pay` | 支付整批，body `{"method":"balance"\|"alipay"}` |
| POST | `/api/orders/batch/{batchId}/cancel` | 取消未支付订单 |
| POST | `/api/payments/{outTradeNo}/sync` | 主动查单（「我已支付」） |
| POST | `/api/payments/mock/{outTradeNo}/pay` | mock 渠道专用，需 `PAYMENT_MOCK=1` |

> ⚠️ `/api/print` 与 `/api/orders` 是**互斥**的两条通道：计费关闭时前者可用、后者 404；开启时反之。

#### `/api/printers` 返回形状

每项：`name` / `uri` / `info` / `location` / `makeAndModel`。`info` 即 CUPS Web 界面里的「描述」（`printer-info`），前端下拉靠它区分同型号队列（issue #101）。

- `ipp.ListPrinters` 优先走 IPP `CUPS-Get-Printers`（只点名 4 个 `requested-attributes`，否则每台机器回上百个属性）；操作被拒或对端不是 CUPS 时退回抓 `/printers` HTML 页面，此时 `info`/`location`/`makeAndModel` 为空串。
- 🚫 必须遍历 `rsp.Groups` 里的 `TagPrinterGroup`：`goipp` 的 `Message.Printer` 只是被压平的第一组，直接用它会在多队列时静默只返回一台。
- 🚫 URI 一律按 `CUPS_HOST` + 队列名拼，**不要**用响应里的 `printer-uri-supported` —— 那是 cupsd 自报的主机名，容器/跨网段场景下浏览器与服务端未必解析得到。
- CUPS 建队列不带 `-D` 时 `printer-info` 默认等于队列名，前端 `printerLabel()` 会在两者相同时省略描述，不重复显示。

### 管理员接口（`/api/admin/*`）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET/POST | `/api/admin/users` | 用户列表 / 创建 |
| PUT/DELETE | `/api/admin/users/{id}` | 更新 / 删除（`admin` 禁止；**有余额或流水时禁止删除**，见下） |
| POST | `/api/admin/users/{id}/balance` | 调整余额（可负），写 `admin_adjust` 流水 |
| GET/PUT | `/api/admin/prices` | 价格配置全量读写 |
| GET | `/api/admin/orders` | 全站订单 |
| POST | `/api/admin/orders/{id}/refund` | 手工退款到余额 |
| GET | `/api/admin/wallet-transactions` | 全站流水 |
| GET | `/api/admin/print-records` | 全站记录 |
| GET/PUT | `/api/admin/settings` | 系统设置 |
| POST | `/api/admin/cleanup` | 手动清理 |
| GET | `/api/admin/drivers` | 驱动列表 + 状态 |
| POST | `/api/admin/drivers/install` | **异步**安装，`202` + `jobId` |
| POST | `/api/admin/drivers/remove` | **异步**卸载，`202` + `jobId` |
| GET | `/api/admin/drivers/detect` | 扫描打印机推荐驱动 |
| GET | `/api/admin/drivers/ppds` | 候选 PPD 列表（`?deviceUri=&deviceId=&manufacturer=&model=&limit=8`） |
| POST | `/api/admin/drivers/upload` | 上传 `.ppd` / `.deb`（**同步**，64MB 上限） |
| POST | `/api/admin/drivers/setup` | **异步**一键设置，`202` + `jobId` |
| GET | `/api/admin/drivers/jobs/{id}` | 轮询任务状态 |

#### 驱动异步任务要点

- `install` / `remove` / `setup` **必须异步**（编译型驱动耗时可达十几分钟，全局 `WriteTimeout=120s` 会 kill 同步进程）。handler 立刻 `202` + `jobId`，命令跑在 `context.Background()` goroutine（硬超时 30min），前端轮询 `jobs/{id}`。
- **单飞**：同一时刻只允许一个驱动任务（apt/dpkg 全局锁），已有任务时返回 `409` + 正在跑的 `jobId`。
- 任务只存内存，保留 1 小时，进程重启即丢。

> 异步模型的完整设计理由与请求/响应形状见 [docs/driver-management.md](docs/driver-management.md#异步任务模型driver_handlersgo)。

`drivers[]` 每项是 `DriverStatus`：`name` / `displayName` / `description` / `arch` / `needCompile` / `installed` / `installedAt` / `installedArch` / `supported` / `hasScript`。`installed` 以 `manifest.txt` 是否存在为唯一判据。

`customDebs[]` 每项是 `CustomDebPackage`：`filename` / `installedAt` / `installedArch` / `sizeBytes`。纯信息性条目。

### `/api/print` 表单字段

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `file` | file | 待打印文件 |
| `printer` | string | 打印机 URI |
| `duplex` | `"true"` / `"false"` | 双面 |
| `color` | `"true"` / `"false"` | 彩色 |
| `copies` | int | 份数 |
| `orientation` | `portrait` / `landscape` | 方向 |
| `paper_size` | `A4` / `A3` / `5inch`…`10inch` | 纸张尺寸 |
| `paper_type` | `plain` / `photo` / … / `auto` | 纸张类型 |
| `media_source` | string | 进纸盒（`auto` 不发送） |
| `print_scaling` | `auto` / `auto-fit` / `fit` / `fill` / `none` / 纯数字 `10`–`400` | 缩放；纯数字＝自定义百分比 |
| `page_range` | string | 页码范围 |
| `page_set` | `all` / `odd` / `even` | 页面子集（`all` 不发送） |
| `mirror` | `"true"` / `"false"` | 镜像 |
| `number_up` | `1`–`16` | N-up（`1` 不发送） |
| `number_up_layout` | `lrtb` / `rltb` / `tblr` / `tbrl` | N-up 排布 |
| `page_border` | `single` / `none` | N-up 边框 |

## 🗄️ 数据库

SQLite，`WAL` + `foreign_keys`；迁移在 `store.go::migrate()` 中用幂等 SQL + `addColumnIfMissing` 增量加列。

### `users`

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` | INTEGER PK | 自增 |
| `username` | TEXT UNIQUE | 登录名 |
| `password_hash` | TEXT | bcrypt |
| `role` | TEXT | `admin` / `user` |
| `protected` | INTEGER | `1` = 受保护 |
| `contact_name` / `phone` / `email` | TEXT | 联系信息 |
| `created_at` / `updated_at` | TEXT | RFC3339 UTC |

### `print_jobs`

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` | INTEGER PK | 自增 |
| `user_id` | INTEGER FK | 提交者 |
| `printer_uri` / `filename` / `stored_path` | TEXT | 打印机 / 文件 |
| `pages` | INTEGER | 页数 |
| `job_id` | TEXT | IPP Job ID |
| `status` | TEXT | `queued` / `printed` |
| `is_duplex` / `is_color` / `mirror` | INTEGER | 布尔参数 |
| `copies` / `number_up` | INTEGER | 数值参数 |
| `orientation` / `paper_size` / `paper_type` / `media_source` | TEXT | 纸张参数 |
| `print_scaling` / `page_range` / `page_set` | TEXT | 页面参数 |
| `watermark_text` | TEXT | 水印 |
| `number_up_layout` / `page_border` | TEXT | N-up 参数 |
| `created_at` | TEXT | RFC3339 UTC |

> 除 `is_duplex` / `is_color` 外，其余打印参数列为 Issue #68 新增（完整参数快照落库，供「重新打印」预填）。`page_set` 存用户原始选择（`even-reverse` 等），不是重排后的值。

### `settings`

KV 表。当前键：`retention_days`（`0` = 永久）、`session_hash_key` / `session_block_key`，以及计费相关的 `billing_enabled`（`0` 默认关闭）/ `order_ttl_minutes`（`15`）/ `price_fallback_cents`（`0`）/ `payment_methods`（空 = 不限制）/ `site_base_url`（空 = 渠道不会回调，靠主动查单）。

### 计费相关表

`price_rules`（单价）、`print_orders`（订单，自带**完整参数快照**——支付后按这些列重放 IPP，不引用 `print_jobs`，因为 `save_history=0` 时那张表根本没有记录）、`payments`（支付单，对订单**一对多**：靠 `print_orders.payment_id` 反向引用）、`wallet_transactions`（余额账本，`idempotency_key` UNIQUE）。详见 [计费与支付](#-计费与支付)。

## 🔐 认证与安全

1. **启动**：`auth.SetupSecureCookie` 从 settings 读取/生成密钥
2. **登录**：写 `session`（HttpOnly，加密+签名）+ `csrf_token`（非 HttpOnly）
3. **鉴权链**：`RequireSession` → `RequireAdmin`（管理员）→ `ValidateCSRF`（非 GET）
4. **登出**：两条 cookie `MaxAge=-1`
5. **默认管理员**：`bootstrap.go` 保证 `admin/admin` 存在且 `protected=1`；`Username == "admin"` 判定保护（禁止改名/改角色/删除）

### Cookie 属性

两条 cookie 统一 `Path=/`、`SameSite=Lax`、`MaxAge=86400`，`Domain` 不设（host-only）。
签发与清除都必须走 `auth.NewCSRFCookie()` / `auth.SetSession()` / `auth.ClearSession()`，
🚫 不要在 handler 里手搓 `http.Cookie` —— 属性漂移过一次（旧的登出路径漏了 `Secure`/`SameSite`）。

`Secure` 由 `COOKIE_SECURE` 三态控制：缺省 `auto`（逐请求看 `r.TLS` 或 `X-Forwarded-Proto`
第一跳）/ `true` 恒开 / `false` 恒关。因此 `CookieSecure(r)` 带 `*http.Request` 参数，
不能缓存成进程级常量。

### 跨源防护（`middleware.CrossOriginProtection`）

包 Go 标准库 `http.CrossOriginProtection`（基于 `Sec-Fetch-Site`，缺失时退化为
`Origin` vs `r.Host`），外加两处项目特有的适配：

- **`X-Forwarded-Host` 比对**：反代不写 `proxy_set_header Host $host` 时 `r.Host` 是上游
  地址，Origin 与它天然不等 → 整站 POST 被 403。`proxyOriginAllowed()` 只在
  `Sec-Fetch-Site` **缺失**时补一次 XFH 比对救回这类请求。
  🚫 不要把这条放行扩大到 `Sec-Fetch-Site` 有值的情况 —— 那才是真正的跨源判定。
  默认信任 XFH 不削弱安全（能伪造头的客户端本来就能让标准库 fail-open），论证见 docs。
- **拒绝响应必须是 JSON**：标准库默认吐 `text/plain`，而前端把「响应不是合法 JSON」的
  登录失败兜底显示成「用户名或密码错误」，导致反代配错被谎报成密码错误（issue #99）。
  `denyCrossOrigin` 返回 `{"error","code":"cross_origin_blocked","origin","host"}` 并打
  含全部判定依据的日志。同理，前端 `LoginView.vue` 的失败文案按状态码分级，
  🚫 不要再加「解析失败就当密码错误」的兜底。

`TRUSTED_ORIGINS`（逗号分隔完整 origin）是浏览器判定 `same-site`/`cross-site` 时的唯一
逃生阀 —— 那种情况改请求头没用。

### 登录限流（`login_limiter.go`）

键 `clientIP|username`，5 次失败锁 15 分钟，仅进程内存。`clientIP()` 取
`X-Forwarded-For` 第一跳且**无条件信任**，所以后端端口不应直接暴露公网。

> 反代下的完整排查表、两类 403 的分别修法、Caddy/Traefik 配置、子路径挂载限制见
> [docs/reverse-proxy.md](docs/reverse-proxy.md)。

## 🖨️ 打印流水

`printHandler` 流程：接收 multipart → 落盘 `uploads/YYYYMMDD/` → 类型识别 & 转换 → 页数统计 → 插入 `queued` 记录 → IPP 提交 → 回写 `printed`。

- `pdf` → `normalizePDF`（gs → LibreOffice → passthrough）
- `office` → LibreOffice `--convert-to pdf`
- `ofd` → `java -jar /ofd-converter.jar`
- `image` → `gofpdf`（大图先下采样到 3000px）
- `text` → `gofpdf` + 内嵌中文字体

> ⚠️ 标准化管线只解决 CUPS 老驱动兼容性问题，gs 会破坏空壳 CJK 字体导致 pdf.js 预览错位。管线原理与 cidfmap 机制见 [docs/pdf-pipeline.md](docs/pdf-pipeline.md)。

### 送印结果的确认（`order_worker.go`）

🚫 **IPP 的 `Print-Job` 是异步的：CUPS 回 `successful-ok` 只代表任务入了队列，跟纸有没有出来毫无关系。**

早先 `startPrintForOrder` 拿到 `successful-ok` 就 `MarkOrderPrinted`，于是打印机离线时会出现「记录显示已打印、CUPS 里任务还在排队重试、钱不退」—— 订单早早离开 `printing`，连卡死回收都够不着。

**主要防线是下单时拦截**（`orderCreateHandler` 里的 `printerUnavailableReason`）：打印机不可用直接 422 不接单，用户压根不会付钱。送印前再查一次（下单到送印之间打印机可能掉线）。

### 打印机的真实状态必须直连探测（`printer_device.go` / `ipp/probe.go`）

🚫 **cupsd 报的「打印机状态」大多不是打印机的状态。**

| 字段 | 真实来源 | 打印机离线时 |
| --- | --- | --- |
| `printer-state` | **队列**状态 | 没活儿干就是 `idle` —— cupsd 不主动探打印机 |
| `printer-state-message` | 只有送过任务且在重试时才有 | 空闲时为空 |
| `marker-levels`（墨量） | 上次成功连上时的**缓存** | 照常端出旧值 |
| `media-ready` / `media-source-supported` | **PPD**（驱动的能力声明） | 照常显示 |

实测：一台离线的打印机，cupsd 仍报 `idle`、四个墨盒 70/60/50/70、五个纸盒 —— 全是它脑子里的印象。照着这些判断，用户照样能把钱付出去。

`device-uri`（如 `ipp://192.168.0.243/ipp/print`）是 cupsd 唯一给出的真实线索。`ipp.ProbeDevice` 拿它**直连设备**：

- `ipp://` `ipps://` → 发一次真正的 IPP `Get-Printer-Attributes`，拿到**打印机自报**的状态与**实时**墨量
- `socket://` `lpd://` `http(s)://` → TCP 连一下端口
- 其余（`usb://` 等）→ `Probed=false`

- 🚫 **`Probed` 与 `Reachable` 必须分开**：`Probed=false`（探不了）**不等于不可达**。混为一谈会把 USB 打印机的用户永久挡在门外。`deviceUnavailableReason` 只在 `Probed && !Reachable` 时返回原因。
- 探测走 `newSafeClient`（IP 层校验，防 DNS rebinding）；`socket://` 那条走裸 `net.DialTimeout`，**必须先自己 `isBlockedIP` 一遍**。
- 结果缓存 5 秒（`deviceProbeTTL`）：不可达时要等满超时，而打印页展示与下单闸门都会用到。探测本身不持锁。
- 前端 `PrinterStatus.vue` 把「队列状态」与「设备状态」**分开显示**，不可达时顶上一条红色警告，并把墨量标成「缓存值 · 打印机离线」。
- 🚫 别把两者合并成一个「状态」——它们回答的是两个不同的问题：「队列怎么想」与「打印机在不在」。

提交成功后订单**停在 `printing`**，由 `confirmPrintResults` / `confirmPrintResultsForBatch` 核对真实结果，`decidePrintAction` 裁决：

| 情形 | 动作 |
| --- | --- |
| `completed`(9) | 转 `printed`，钱不退 |
| `canceled`(7) / `aborted`(8) | 失败 + **全额退款** |
| `processing` 且**印数长时间不增长** | 后端在空转重试 → **先取消任务、再退款** |
| `processing` 且印数在增长 | **继续等**（正在出纸，6 分钟也好） |
| `pending`(3) / `held`(4) / `stopped`(6) | **继续等**（前面有任务排队） |
| 任务查不到 | 超过 `orderStuckPrintingAfter` 才转人工，**不自动退款** |

#### ⚠️ `job_id` 存的是 URI，不是数字

`SendPrintJob` 优先返回 `job-uri`，所以 `print_orders.job_id` 里实际是
`ipp://localhost:631/jobs/15`。要拿它查任务必须先 `ipp.JobIDFromURI` 取出末尾的数字。

🚫 **不要「从字符串开头往后解析数字」**：URI 以 `'i'` 开头，那样直接得到 **0**，于是每次核对都去问「0 号任务」——永远查不到。后果是**四个现象同时出现，指向同一个根因**：

- 订单永远停在 `printing`（界面显示「打印中」）
- 打印记录永远停在 `queued`（界面显示「排队中」）
- 支付弹窗永远不切到成功页（它等的是订单变 `printed`）
- 卡满 `orderStuckPrintingAfter` 后还被判成「打印结果无法确认」

实测踩过，而且覆盖 4 个现象的排查花了很久 —— 见到「订单卡在打印中」先查这个。
`client_test.go` 的 `TestJobIDFromURI` 盯着它（含一条「与 SendPrintJob 返回值对得上」的断言）。

#### 判据是「印数动不动」，不是状态词

⚠️ 实测（CUPS 2.4 + 网络打印机，打印机连不上时）CUPS 报的是：

```
job-state                 = 5  (processing)   ← 一派「正在打印」的样子
job-state-reasons         = [job-printing]    ← 没有任何异常词
job-printer-state-message = "The printer is unreachable at this time."
job-impressions-completed = 1                 ← 然后就再也不动了
```

**状态词和原因词全都看不出问题。** 所以 `progressTracker` 记的是 `job-impressions-completed` 有没有增长 —— 数字不依赖语言、不依赖 CUPS 的措辞。

同理，下单时的可用性判断也只能读 `printer-state-message` 里的 CUPS 原话（`unreachableStateMessages`）：那时 `printer-state` 是 `processing`、reasons 是 `[media-empty-warning]`，同样指望不上。

- 🚫 **不能按「等了多久」一刀切**：用户打印几百页要 6 分钟、或者前面排了别的任务，`pending` 十几分钟完全正常。按时间切会把正常任务取消掉，用户付了钱却因为「等得久」被退款 —— 而他只需要多等一会儿。
- 🚫 **「先取消再退款」的顺序不能反**：直接退款而任务还在队列里，等打印机恢复就会把纸打出来 —— 变成「钱退了、纸也出了」。
- 🚫 任务查不到时**不自动退款**：cupsd 清掉记录最常见的原因就是任务已经打完了。
- `decidePrintAction` 抽成纯函数（`order_worker_test.go` 覆盖每个分支）。它是整个计费系统里唯一决定「已付的钱要不要退」的地方，错了只有两种后果：白送钱，或用户付了钱拿不到东西。
- 前端轮询批次状态时顺带核对（`printCheckGate` 节流 2 秒），打完到界面变样不必等后台巡检的 30 秒。

### 打印记录的可见性

打印记录在**提交到 CUPS 的那一刻**就建（状态 `queued`），确认打完后再改成 `printed`，失败则改成 `failed`。

🚫 不要改成「确认打完才建记录」：用户点完「重新打印」在记录页什么都看不到，会以为没生效 —— 而任务其实已经在队列里了。状态如实标 `queued` 既不骗人也不让人干等。

### 自定义百分比缩放（`pdf_scale.go`）

`print_scaling` 为纯数字时走 `resolveCustomScaling`：gs 预缩放 PDF 内容 → 发给 CUPS 的 `print-scaling` 换成 `none`。
打印与重打（`reprintHandler`）两条路径都必须调用它。

- 🚫 **纯数字绝不能透传给 IPP**：`print-scaling` 是 keyword，`"40"` 不是合法取值。gs 失败/非 PDF 时退回空串（打印机默认），不是原样下发。
- 🚫 gs 缩放**不要**用 `-dFIXEDMEDIA` + 固定 `-dDEVICEWIDTHPOINTS`：横向页会被塞进纵向纸并偏移。现在的 `Install` 过程逐页读 `currentpagedevice /PageSize`，纸张尺寸原样保留。
- ⚠️ `-sOutputFile` 必须排在 `-f` **之前**——`-f` 之后的参数会被 gs 当成输入文件名，放末尾会直接报 `requires an output file`（Issue #98 的根因）。
- 前端预览用等比 CSS transform 同步这个百分比（`PrintPreview.vue` / `PdfCanvas.vue`），其余缩放模式交给 CUPS，预览不模拟。

## 🧹 维护任务

`maintenance.go` 每小时：读 `retention_days`（`0` 跳过）→ 删过期 `print_jobs` + 文件 → `VACUUM` + `wal_checkpoint`。管理员可 `POST /api/admin/cleanup` 手动触发。

> ⚠️ 清理必须跳过**未终结订单持有的文件**（`protectedRels`），否则已付款未打印的订单文件会被误删。

## 💰 计费与支付

`billing_enabled` 默认 **0（关闭）**。关闭时所有入口与改造前**完全一致**，升级既有部署不受影响。开启后订单成为唯一的打印通道。

### 四个打印入口与闸门

| 入口 | 计费开启时 |
| --- | --- |
| `POST /api/orders`（单/多文件） | ✅ 唯一合法通道 |
| `POST /api/print` | 🚫 **403**「计费已开启，请通过打印订单提交」 |
| `POST /api/print-records/{id}/reprint` | 转订单（`source='reprint'`），返回与下单**相同**的响应体 |
| `scheduler.go` 定时打印 | ⚠️ **暂未接入计费**（该功能可能被移除） |

- 🚫 **`/api/print` 的闸门是防直连的兜底**，不是给前端看的。前端在计费开启时本来就走订单，但少了这道判断，一次 `curl` 就能绕过下单与扣款。
- 重打转订单后前端**不需要分支**：靠响应里有没有 `batchId` 区分（[PrintView.vue](frontend/src/views/PrintView.vue) 的 `handleReprint`）。

### 金额与计价

金额一律以**整数分**存储与传输，前端 `formatCents` 换算展示。

- 计价内核在 `internal/pricing`，前端镜像在 `frontend/src/utils/pricing.js`，**两边跑同一套用例**（`testdata/pricing_cases.json`，`go test` + `npm run check:pricing`）。改一边必须同步改另一边。
- 计费口径 = `|page_range ∩ [1..N]|` 过滤 `page_set` 后 × 单价 × 份数。**N-up 不折算**，补白页不计费。
- 🚫 **计费用重排前的 `DocPages`**，不是 `PrintPages`——even-reverse 会补一张空白页，用后者会多收钱。

### 订单状态机

```
pending_payment ─┬─ 余额/在线支付到账 ──► paid ──(原子领取)──► printing ─┬─► printed
                 ├─ 用户取消 ──► cancelled                              └─► failed（自动全额退款）
                 └─ 超时 ──► expired（删文件）
printing ──(30 分钟无终态)──► failed（结果未知，**不自动退款**，交管理员判断）
```

`ClaimOrderForPrint`（`UPDATE ... WHERE status='paid'`）是**付一次印十次**的唯一门控。

### 结算与幂等

`settlePayment` 是「钱已到账」的**唯一**入口，mock、渠道回调、查单轮询三条路都汇到它。五层幂等：`payments.out_trade_no` UNIQUE → `MarkPaymentPaid` → `wallet_transactions.idempotency_key` UNIQUE → `ClaimOrderForPrint` → `refund:<order_id>`。

- 🚫 **余额只能经 `store.ApplyWalletDelta` 变动**。任何地方直接 `UPDATE users SET balance_cents` 都会让余额与账本分叉。
- 🚫 **回调必须校验 `res.Paid`**：验签通过 ≠ 已收款。支付宝的 `TRADE_CLOSED` 也是一条签名合法的正常通知，直接送去 `settlePayment` 会把订单标成已支付并发货。
- 🚫 **回调必须以「支付单」为锚**（`out_trade_no` → `payments`），不是订单；且金额必须与库里记的一致，不符则不入账不打印，只留证告警。

### 支付渠道

渠道实现 `payment.Provider`（`Name` / `Create` / `Query` / `Refund` / `Close` / `VerifyCallback` / `CallbackAck`），差异被封在 `internal/payment` 里，`cmd/server` 只拿结论。

### 余额原路退款（`wallet_refund.go`）

`POST /api/wallet/refund`，把充进来还没花掉的钱退回用户的支付宝。与「打印失败退款」是两件事：那个退回**余额**（钱没离开系统），这个把钱真的**退出去了**。

**可退额度 = min(余额, 未退完的充值单之和)**，不是看余额：

| 余额里的钱 | 能不能原路退 | 为什么 |
| --- | --- | --- |
| 充值进来的（`purpose='topup'` 且 `status='paid'`） | ✅ | 有对应的渠道交易 |
| 打印花掉的 | ❌ | 已经变成纸了 |
| 管理员手工调额的 | ❌ | **没有渠道交易**，支付宝那边不存在这笔钱 |
| 打印失败退回来的（`kind='refund'`） | ❌ | 同样没有渠道交易 |

- 🚫 最后两行是**资金安全的关键**：若可退额度只看余额，用户就能把"凭空调出来的钱"套现走。`ListRefundableTopups` 的查询条件与它一一对应，改之前先想清楚。
- **先扣余额、再去渠道退**。两个失败方向不对称：先扣后失败 → 用户余额少了、钱还在账上，**对用户可逆**；先退后失败 → 用户既拿到钱余额又还在，**对我们不可逆**且无从自动发现。退款失败时把没退成的部分补回余额（`withdraw_rollback`）。
- 一笔退款可能横跨多张充值单（按时间从早退起），`RefundNo = <批次号>-<序号>`：同批内各不相同、整批重试沿用同一批次号，渠道据此判重。
- 同一用户的退款请求**单飞**（`userRefundLock`）：「可退额度」依赖一次非原子的求和，并发下两个请求会各自通过校验。
- 每次渠道退款成功后立刻单独提交本地记账；若记账失败会把 `payment`/`outTradeNo`/金额/退款单号打进日志 —— 钱已经出去了，必须留痕可人工补。
- `kind` 区分清楚：`refund` = 退回余额，`withdraw` = 原路退回支付宝，`withdraw_rollback` = 退款失败回滚。前端文案也分开（「退回余额」/「原路退款」），否则用户对不上账。

### 二维码生命周期

- 二维码有效期 **3 分钟**（`paymentQRTTL`），与订单 TTL（`order_ttl_minutes`，默认 15 分钟）是**两回事**：前者决定眼前这个码能扫多久，后者决定用户能在页面上待多久。取两者中较短的那个。
- 到期后前端**自动刷新**：再调一次 `/api/orders/batch/{id}/pay`，服务端新建一笔支付单并把订单改挂过去，同时调 `Provider.Close` 关掉旧的。
- 🚫 **旧单必须在渠道侧关掉**。少了这一步，用户若扫了旧码付款，钱会到账但那笔支付单下面已经没有订单了（订单被重新挂到新单上）—— 结果是「付了钱什么都没打出来」，而钱在渠道侧，我们无法自动原路退回。
- 🚫 关单失败**不阻断**刷新（用户此刻要的是一个能扫的新码），只记日志。

### 支付界面约定（`PaymentPanel.vue`）

打印支付与余额充值**共用**这个组件，两处行为必须一致：二维码 + 倒计时 + 自动刷新 + `我已支付` + 成功页。

- **支付成功后不立刻关闭**：显示「支付成功」+ 5 秒倒计时自动返回，同时提供手动「返回」按钮 —— 两条路都给。
- ⚠️ **「钱到了」与「纸出来了」必须分开，成功页绑在【钱到了】上。** 判据是批次离开 `pending_payment`（`processing` / `printed` 都算到账；`expired` / `cancelled` 是没付款的终态，不算）。
  - 早先把成功页绑在 `status === 'printed'` 上，结果是用户付完款还要**盯着二维码等打印机把纸打完**（实测等了近一分钟）才看到「支付成功」。付款是即时到账的，反馈也必须即时。
  - 到账后**锁定**（`paymentSettled`），后续打印结果只改文案（「支付成功」→「打印完成」），不会把已经展示的成功页翻回去。
  - 唯独打印确实失败/部分失败时不报成功 —— 那时给用户看原因与退款（`printFailed`）。
- ⚠️ 所有会写进界面的错误提示**必须自动消失**（`flashError` / `clearTopupErrorLater`）。一句永远不走的「尚未收到支付结果」既像故障，又挡住后面的状态变化。
- 🚫 **公开入口函数不要带可选参数**。模板里写 `@click="startTopup"` 时 Vue 会把 `MouseEvent` 传进来，签名若是 `startTopup(silent = false)` 那事件对象就是真值，手动点击会静默退化成「自动刷新」模式。实测踩过两次。现在公开入口一律无参，静默行为拆成 `runTopup` / `runSyncTopup` 等内部函数。
- 🚫 对渠道的**出站**调用（`Create` / `Close`）绝不挂 HTTP 请求的 ctx —— 浏览器一次中止就会掐断调用，表现为「第一次点报错、第二次成功」。用 `paymentChannelTimeout` 独立超时。

`availableProviders()` 的判据是**能构建出实例**（配置齐全），不是管理员勾选了什么——避免前端出现「点了必然失败」的按钮。

#### 支付宝：两种收款方式，按设备分流

这个程序主要给手机用，但电脑上也要能用。两种设备拿什么付款完全不同，所以同一个渠道下备了两条路，由前端判断设备后在支付请求里带 `channel`：

| channel | 接口 | 谁用 | 拿到什么 |
| --- | --- | --- | --- |
| `wap` | `alipay.trade.wap.pay`（手机网站支付） | 手机 | 收银台地址，**当前标签**跳过去；收银台自动唤起 App，付完支付宝把浏览器送回 `return_url` |
| `qrcode` | `alipay.trade.precreate`（当面付） | 电脑 | 一串 `qr_code`，渲染成二维码让手机扫 |

- 🚫 **`return_url` 用内网地址完全可以**（`http://192.168.1.22:1180/#/wallet`）。回跳是**手机浏览器**去访问的，支付宝服务器不需要可达它 —— 不需要公网域名、不需要备案。真正需要公网可达的只有 `notify_url`，而本项目压根不发（到账一律靠 `alipay.trade.query` 主动查）。
- 🚫 **`wap` 拿不到回跳地址时必须降级成 `qrcode`**（`createPayment` 里做）。没有 `return_url` 时用户付完会停在支付宝自己的结果页上，还得自己找回来 —— 那正是改用官方方案要解决的问题本身。
- 🚫 **`wap` 这一步不发任何 HTTP 请求**：手机网站支付是页面跳转类接口，服务端只是把签名好的参数拼进收银台地址。于是**签名错误、没签产品、app_id 不对都不会在本机报错**，只会在用户手机上变成一个支付宝错误页。改这段必须真机联调，`alipay_test.go` 的 `TestAlipayCreateWap` 会自己验一遍签名兜住。
- ⚠️ **`product_code` 必须是 `QUICK_WAP_WAY`**，传错被网关直接拒。
- ⚠️ 前端渲染依据是**服务端返回了哪种凭据**（`qrCodeText` 是否为空），不是再看一遍设备 —— 服务端降级时界面要跟着降级。空字符串的二维码 value 会让 Reka UI 抛错打崩组件树。
- ⚠️ **回跳后页面是重新加载的**，内存里什么都没了。`App.vue` 的 `handlePaymentReturn` 凭 URL 上的 `out_trade_no` 补一次查单 —— 少了它，用户付完回来看到的是一个毫无反应的页面。为此 `loadSession` 不能无条件 `push('/print')`（会把查询参数冲掉）。

配置全走**环境变量**（不进 SQLite——私钥落库会跟着 `data/*.db` 进备份）：

| 变量 | 说明 |
| --- | --- |
| `ALIPAY_APP_ID` | 开放平台应用的 AppID |
| `ALIPAY_PRIVATE_KEY` | 应用私钥（PKCS8/PKCS1、PEM 或裸 base64 均可） |
| `ALIPAY_PUBLIC_KEY` | ⚠️ **支付宝公钥**，不是应用公钥 |
| `ALIPAY_GATEWAY` | 可选，沙箱用 `https://openapi-sandbox.dl.alipaydev.com/gateway.do` |

- ⚠️ **生产要签两个产品**：「当面付」+「手机网站支付」。沙箱两个都能调，且沙箱**只支持余额支付**（旧 MAPI 接口 `alipay.wap.create.direct.pay.by.user` 不支持沙箱，必须用新的 `alipay.trade.wap.pay`）。
- 🚫 **响应节点必须按原始字节验签**：`sign` 是对响应 JSON 节点**原文**的签名，`Unmarshal` 再 `Marshal` 回去（键序/空格/转义都可能变）必然验签失败。用 `rawResponseNode` 从 body 里切。
- 🚫 **`timestamp` 必须用东八区**（`alipayLocation()`）。容器默认 UTC，用本地时间会差 8 小时——这个 bug 在开发机上永远复现不了。
- 🚫 **金额换算不能用 float**：`centsToYuan` / `yuanToCents` 走定点字符串解析，多出的位数只接受全零。
- ⚠️ 回调验签通过后还要核对 `app_id`：同一开放平台账号下其它应用签发的通知也能通过验签。
- 没有公网回调时的结算是**主动查单**，两条路都走 `syncPaymentByOutTradeNo`：
  - **按需**（主路径）：前端轮询批次状态（支付弹窗每 2 秒）/ 充值状态时顺带查一次，用户扫完码**几秒内**界面就变
  - **后台巡检**：`pollUnsettledPayments` 每 30 秒兜底，覆盖用户付完就关页面的情况
  - 两者共用 `paymentQueryGate` 节流（同一支付单 5 秒内只查一次）——支付宝对查单接口有频率限制，不节流的话 2 秒轮询会打爆配额
  - 🚫 查单是**出站请求，必须在事务外**做：放进 `WithTx` 会占着 SQLite 事务等网络往返
- 「我已支付」按钮**不加节流**：它是认证过的人类点击，加了反而时灵时不灵；节流只约束自动轮询

微信支付 v3 尚未实现（需平台证书轮换 + 回调 AES-GCM 解密）。

- 🚫 只支持**公钥模式**，不支持证书模式（需额外 `app_cert_sn` / `alipay_root_cert_sn`）。
- ⚠️ 沙箱版支付宝**只有 Android**，且与日常支付宝**不能混用**（互相扫码提示「码已失效」）。
- ⚠️ `precreate` 只生成二维码，用户扫码进收银台支付宝才真正建单 —— 此前 query 报「交易不存在」是正常的。

### 本地验证

`verify-*.sh`（仓库外一层）覆盖计费全链路。**在 Git Bash 里跑**，不是 WSL——WSL 里没装 node：

```bash
bash verify-billing-gates.sh   # /api/print 闸门 + 重打转订单
bash verify-alipay.sh          # 支付宝全链路（本地假网关，无需商户资质）
```

`fake-alipay-gateway.js` 照着真协议实现签名/验签/两个接口，`ALIPAY_GATEWAY` 指过去即可端到端跑通，只有「对面是支付宝」这件事是假的。

## 🧩 驱动管理

相关文件：`driver_handlers.go`、`driver_registry.go`、`scripts/driver/*.sh`、`DriversView.vue`。

### 目录约定

| 路径 | 内容 |
| --- | --- |
| `/opt/cups-drivers/scripts/install-<name>.sh` | 安装脚本（构建期 COPY，不执行） |
| `/usr/local/bin/{driver-install,driver-list,driver-remove,restore-drivers}` | 管理命令 |
| `/opt/cups-drivers/libexec/capture-debs` | apt `DPkg::Pre-Install-Pkgs` 钩子（归档 apt 要装的 `.deb`） |
| `/opt/cups-drivers/baseline-packages.txt` | **构建期**生成的镜像自带包名快照（镜像层，**不在挂载卷内**） |
| `/opt/cups-drivers/data/<driver>/manifest.txt` | 文件清单 = **"已安装"唯一标记**（纯路径列表，包级模式下可能为空文件） |
| `/opt/cups-drivers/data/<driver>/packages.txt` | 存在即包级/混合模式。`<pkg> <version> <arch> <相对路径>` |
| `/opt/cups-drivers/data/<driver>/packages/*.deb` | 归档的 `.deb` 原件 |
| `/opt/cups-drivers/data/<driver>/metadata.txt` | `driver=` / `installed_at=` / `file_count=` / `arch=` / `manifest_version=` / `restore_mode=` / `package_count=` / `package_bytes=` |
| `/opt/cups-drivers/data/<driver>/<绝对路径镜像>` | 文件级产物副本 |
| `/opt/cups-drivers/data/custom-ppd/` | 上传 `.ppd`（写 manifest，可恢复） |
| `/opt/cups-drivers/data/custom-deb/packages/` | 上传 `.deb`（写 `packages.txt`，**重启自动重装**） |

`docker-compose.yml` 把 `./.drivers` 挂到 `/opt/cups-drivers/data`。**不挂卷 = 重启丢驱动。**

### 两条持久化通道（按驱动来源分流）

| 通道 | 适用 | 恢复 | 卸载 |
| --- | --- | --- | --- |
| **包级** | dpkg 包来源（`escpr2`(deb) / `canon-ufr2` / `konica-bizhub` / `epson-cn` / `gutenprint` / `custom-deb`） | `dpkg -i` 归档的 `.deb` | `apt-get purge`（dry-run 校验后）或 `dpkg -P --force-depends` |
| **文件级** | 非包来源（`canon-capt` / `hp-laserjet1020` / `sharp` / `foo2zjs-firmware` / `custom-ppd` / `escpr2`(源码分支)） | 按 manifest `cp -aT` | 按 manifest `rm -f` |

**为什么必须分流**：厂商 deb 普遍把产物装在 `/opt/<vendor>/`（Epson escpr2 的 298 个文件全在 `/opt`、Konica 的 filter 真身在 `/opt/km`）、`/usr/bin`（Canon UFR II 的渲染引擎 `cnrsdrvufr2`）、`/usr/share/<vendor>/`（gutenprint 的 370 个机型 XML、Canon 的 356 个 ICC），这些全在文件级路径白名单之外 —— 只靠文件级快照会得到空 manifest（`exit 1`，但文件其实已进容器）或残缺快照（UI 显示"已安装"而重启后驱动失效）。

`.deb` 原件的捕获有四层，前面命中就不用后面：apt 钩子（`capture-debs`，含全部传递依赖）→ 厂商脚本用 `DRIVER_PKG_DIR` 主动交接 → `/var/cache/apt/archives/` 打捞 → `apt-get download`。

> 🚫 `capture-debs` **必须永远 `exit 0`** —— `DPkg::Pre-Install-Pkgs` 返回非零会让 apt 中止**整个安装事务**。
>
> 🚫 厂商脚本的交接行必须 `|| true` 且不新增 `trap`，以免破坏"退出码 0/3/其他"和"只允许一个 EXIT trap"两条约定。

### 退出码约定

| 退出码 | 含义 | 行为 |
| --- | --- | --- |
| `0` | 成功 | 写 manifest |
| `3` | 架构不支持 | 不写 manifest，`exit 3` |
| 其他非零 | 真失败 | 不写 manifest，透传 |

**退出码 0 但文件与包归档同时为空才判失败**（拒绝写 manifest）。只要归档到了 `.deb`，即使所有文件都落在白名单外也算成功 —— 那种驱动照样能完整恢复。

### 架构探测

统一用 `dpkg --print-architecture`（**不要用 `dpkg-architecture`**，runtime 无 `dpkg-dev`）。Go 侧 `currentDebArch()` 映射 `GOARCH` → Debian 命名。multiarch 用 `detect_multiarch_libdir()`（glob `/usr/lib/*-linux-gnu*`，拿不到返回空串）。

### manifest 白名单速查

**ALLOW**：`/usr/lib/cups`、`/usr/share/cups`、`/usr/share/ppd`、`/usr/share/foomatic`、`/lib/firmware`、`/usr/lib/firmware`、`/usr/lib/<multiarch>`。

**DENY**：`/usr/bin/*`、`/usr/sbin/*`、`/bin/*`、`/sbin/*`、`/usr/local/{bin,sbin}/*`、`/etc/*`、`/var/*`、`/usr/include/*`、`/opt/cups-drivers/*`、`/tmp/*`、`/usr/share/{doc,man,locale,info}/*`、`/usr/share/cups/doc-root/*`、`*/pkgconfig/*`、`*.a`、`*.o`、`*.la`。

> ⚠️ **白名单在 `driver-install.sh` / `driver-remove.sh` / `restore-drivers.sh` 三处各有一份，必须永久保留全部三份**——remove/restore 侧是给存量被污染快照兜底的。🚫 不要因为 install 侧已过滤就删掉另外两处。详见 [docs/driver-management.md](docs/driver-management.md#-manifest-白名单为什么必须存在且三处都要有)。

### baseline 归属守卫（路径白名单的结构性补丁）

路径白名单有个**按路径分不开**的盲区：multiarch 目录（`/usr/lib/<triplet>`）既是驱动共享库的家、也是系统库的家。厂商 deb 的依赖被 apt 解析时会把无关系统库拖进来（老 `install-epson-cn.sh` 的 `apt-get -f install` 会拉进整套 Qt5/X11/GL —— 那只是 GUI 工具的依赖），它们正好落在白名单**内** → 被当成驱动产物写进 manifest → `driver-remove` 逐条 `rm` 就把系统库删了，`restore-drivers` 每次开机还用旧副本覆盖回去。

按**包属主**就分得干干净净：构建期把当时已安装的全部包名存进 `baseline-packages.txt`，三个脚本据此把"镜像自带包拥有的文件"一律排除（install 侧不记录，remove 侧不删，restore 侧不覆盖）。

实现要点：包名读进 bash 关联数组做 O(1) 判断（避免对两千多个 `.list` 文件各 fork 一次 grep）→ 只 `cat` baseline 包自己的 `/var/lib/dpkg/info/*.list`（multiarch 包的清单名是 `<pkg>:<arch>.list`，比对前要去掉 `:arch`）→ 与 manifest 求一次 `comm -12` 交集，之后逐条查是 O(1)。

> ⚠️ 这份守卫同样**三处各有一份**，理由与三份路径白名单相同。
>
> ⚠️ `baseline-packages.txt` 必须留在**镜像层**且生成于**所有 apt 安装之后**。放进 `/opt/cups-drivers/data` 会被 volume 挂载覆盖，守卫就降级成"只做路径白名单"。
>
> 🚫 **CUPS 自身的包绝不能进驱动快照**（`driver-install.sh` 的 `PKG_NAME_DENY_PATTERNS` 里列了 `cups` / `cups-core-drivers` / `cups-ppdc` / `libcups*` 等）。本镜像的 CUPS 是源码编译的（2.4.19，overlay tar 解包进 `/usr`），apt 侧只装了 `cups-daemon` / `cups-client` / `cups-filters`，**故意没有 `cups` 元包**。某些驱动包（`printer-driver-gutenprint`、Konica 的 deb）硬依赖 `cups` 元包，一旦让 apt 去满足就会装上 Debian 的 `cups-core-drivers` —— 那里面正是 `/usr/lib/cups/backend/{usb,socket,lpd,...}` 和一批 filter，**直接覆盖源码编译的同名文件**。实测 Konica 那条路径会让 564 个 CUPS 自身的文件被算进驱动快照。各 `install-*.sh` 用 `dpkg -i --force-depends` 从根上避免，DENY 再兜一层。

### AIO 编译脚本约定

> ⚠️ 编译型脚本**只允许一个 `trap _cleanup EXIT`**（bash 同信号只保留最后注册的 handler）。AIO 模式下只 `apt-get clean`，**绝不 `rm -rf /var/lib/apt/lists/*`**。详见 [docs/driver-management.md](docs/driver-management.md#-aio-编译脚本的单一-exit-trap约定)。

### 上传自定义驱动

- **`.ppd`**：校验 → 装到 `/usr/share/cups/model/custom/` → 写 manifest（可恢复）
- **`.deb`**：`dpkg -i`（失败补依赖后**必须再 `dpkg -i` 一次**）→ 归档到 `custom-deb/packages/` **并登记 `packages.txt`** → 容器启动时由 `restore-drivers` 幂等 `dpkg -i` 自动重装。仍**不写 manifest**（文件级恢复对 `.deb` 无意义，真正的安装动作在 maintainer script 里）
  - 存量只归档未登记的 `.deb` 会被 restore 按 glob **自动收养**，无需用户操作
  - 装不上的坏包会每次开机重试 → 出口是删掉宿主 `./.drivers/custom-deb/packages/` 下的对应文件
- 🔐 上传 `.deb` = 容器内 root RCE（dpkg maintainer script）。接口受三重鉴权保护，**管理员密码等同容器 root 凭据**
- 大小上限必须用 `http.MaxBytesReader`（`ParseMultipartForm(n)` 的 `n` 是 maxMemory 不是 body 上限）

> 上传机制完整说明见 [docs/driver-management.md](docs/driver-management.md#上传自定义驱动)。

### `lpinfo` 检测与一键设置

- 用 `lpinfo -l -v` **长格式**（短格式无厂商型号）；按 caps 加 `--timeout`/`--include-schemes`；独立超时 context（不挂 `r.Context()`）
- 型号优先级（修正）：`req.Manufacturer/Model`（lpinfo make-and-model，最可信）→ `device-id` MFG/MDL → URI 路径。🚫 不要把 URI 解析排最前（usb URI 的厂商常是裸 "HP" 甚至 "Unknown"）
- PPD 匹配走**打分引擎**（`ppd_match.go` 纯函数 + `ppd_query.go` 副作用层）：型号归一化 → 分层 tier 打分 → 来源偏好（custom > vendor > hplip > everywhere > foomatic > gutenprint > generic）→ cups-driverd 指纹加分 → 稳定排序 Top-N
- `GET /api/admin/drivers/ppds` 返回候选列表（不走后台 job，不占单飞锁，并发闸 4）
- `setup` 三态决策树：显式 `ppdUri` > `everywhere`（driverless）> 自动 Top-1 > **报错**（绝不静默建 raw）
- ⚠️ **`lpadmin` 不传 `-m` 建的是 raw 队列（无 PPD），不是 IPP Everywhere。** 真正的 driverless 要显式 `-m everywhere`。raw 队列拿不到 PPD 选项 → `/api/printer-info` 的 `mediaSourceSupported` 为空 → 前端进纸盒下拉消失
- 队列名去重（`uniquePrinterName`，`-2`…`-50` 后缀）；同 device-uri 已有队列时拒绝覆盖
- `lpadmin` 后验证（`lpstat -p` + `lpoptions -l`），PPD 未生效时 `isNew` 队列回滚 `lpadmin -x`

> 解析细节与历史翻车见 [docs/driver-management.md](docs/driver-management.md#lpinfo-检测格式假设与型号解析优先级)。

## 🚀 容器启动流程（`entrypoint.sh`）

1. `restore-drivers`（恢复驱动快照：先包级 `dpkg -i` 归档的 `.deb`，再文件级 `cp -a`）
2. CUPS 管理员用户 + tzdata
3. CUPS 配置还原（空卷时从 `/etc/cups-bak/` 复制）+ **对存量卷幂等补** `ssl/` 目录与 `ReadyPaperSizes`
4. HP 1020 PPD Letter → A4 修补（issue #48）
5. HP host-based 固件上传（后台）
6. dbus + avahi + ipp-usb（后台，允许失败）
7. cupsd + watchdog
8. 等 cupsd 就绪（`lpstat -r`，30 × 1s）
9. HP 1020 队列 `media-default=A4`（后台）
10. `exec /cups-web`（PID 1）

> ⚠️ cupsd 必须在 watchdog 子 shell **内部前台**启动（`wait` 只能等自己的子进程，否则 127 重启风暴）。🚫 不要把 `cupsd -f` 挪到子 shell 外面。
>
> ⚠️ `restore-drivers` 必须永远 `exit 0`（驱动恢复是尽力而为，不能阻塞启动，否则用户连 Web UI 都进不去无法自救）。它现在会跑 `dpkg -i`，因此必须 `DEBIAN_FRONTEND=noninteractive` + `--force-confold` + `timeout` + 临时 `policy-rc.d`（详见 docs）。
>
> ⚠️ **第 3 步的两条补丁必须在 `if [ ! -f cupsd.conf ]` 之外**：那个 if 只看 `cupsd.conf` 一个文件，存量 `./.etc` 卷里只要有它，整块还原就被跳过，新基线里加的东西存量用户永远拿不到。
>
> 🚫 **`lpadmin -o media=` 设的是 `media-default`，不是 `media-ready`。** iPhone AirPrint 面板的纸张**列表**读 `media-ready`，而 CUPS 的 `media-ready` 只由 cupsd.conf 的 `ReadyPaperSizes` ∩ PPD 尺寸决定（`scheduler/printers.c` 里 `load_ppd` 是唯一写入点），跟 `media-default` 无关。第 9 步只负责"面板默认勾选 A4"，issue #82 的真正修法是 `ReadyPaperSizes A4,A3,A5,A6,EnvDL`（值用 **PPD 尺寸名**，不是 PWG 名 `iso_a4_210x297mm`）。不配它时 cupsd 按 locale 兜底成 `Letter,Legal,Tabloid,4x6,Env10`（容器无 `LANG` → locale 为 C → 走 Letter 分支），A4 永远不出现。
>
> 完整设计理由见 [docs/container-startup.md](docs/container-startup.md)。

## 🔧 开发环境

### 本地搭建

```bash
# 前端
cd frontend && bun install && bun run dev    # :5173，代理 /api → :8090

# 后端
go mod download
go build -o bin/cups-web ./cmd/server && ./bin/cups-web    # :8080
```

### Makefile

```bash
make all            # 前端 dist + Go 二进制（必须先前端再后端）
make frontend       # 仅前端
make build          # 仅后端（禁止裸 go build ./cmd/server）
make docker-build   # AIO 镜像
```

### Vite 开发代理

`/api → http://localhost:8090`。本地调试：后端 `LISTEN_ADDR=:8090 go run ./cmd/server`，前端 `bun run dev`。

### 构建产物分包

`vue-vendor`（vue/router）、`ui-vendor`（nuxt-ui/reka-ui/vueuse）、`pdf-vendor`（pdfjs-dist）。

## 🚢 部署

### docker-compose

单服务 `cups`（AIO），`image: hanxi/cups-web:latest`，`network_mode: host`（mDNS 组播需要，CUPS 直接占用宿主 `631`，Web 由 `LISTEN_ADDR` 决定、默认 `:1180`，issue #107）。

| 配置 | 为什么 |
| --- | --- |
| `network_mode: host` + `hostname: CUPS` | mDNS/DNS-SD 组播（5353/udp）桥接下出不去进不来：发现不了局域网网络打印机、AirPrint 广播不出去（issue #107）。avahi 以 `CUPS.local` 广播 |
| `LISTEN_ADDR=:1180` | host 网络无端口映射，Web 直接监听宿主 1180，与旧桥接时代一致 |
| `user: root` | cupsd / lpadmin / dpkg / 写系统路径 |
| `security_opt: [apparmor:unconfined]` | PVE LXC AppArmor DENIED（issue #91） |
| `./.etc:/etc/cups`、`./.data:/data`、`./.uploads:/uploads` | 持久化 |
| **`./.drivers:/opt/cups-drivers/data`** | **驱动快照持久化**（删 = 重启丢驱动） |
| `/dev/bus/usb:/dev/bus/usb` + `device_cgroup_rules` | USB 热插拔（issue #81） |
| `/run/udev:/run/udev:ro` | libusb 设备属性（可选） |

> ⚠️ **不要挂载宿主 `/run/dbus/system_bus_socket`**：旧版为借宿主 avahi 广播 AirPrint 挂过它（issue #94），但宿主没装 avahi 时容器内自己的 dbus-daemon 会因 socket 路径被占而起不来，avahi 跟着失效（issue #107）。现已移除，host 网络下容器内自启 dbus + avahi 即可。

### Docker 构建

五阶段：`frontend-build`（node:20-slim）→ `java-builder`（BUILDPLATFORM 锁 amd64）→ `builder`（golang:1.26）→ `cups-builder`（源码编译 CUPS）→ `runtime`（trixie-slim）。覆盖 `linux/amd64` + `arm64` + `arm/v7`。

> 🚨 `cups-builder` 的 `ca-certificates` 请勿删除（wget TLS 校验，删了 CI 直接崩）。
>
> ⚠️ **`/etc/cups/*` 来自 apt 的 `cups-daemon`（trixie 2.4.10），不是源码编译那份。** `cups-builder` 打的 `cups-compiled.tar` 只含 `/usr` 路径，整个 `/etc` 都不在里面。所以：读 `cupsd.conf` 的 Location/Policy 出厂内容要按 **Debian 版**理解；而 `/usr/lib/cups/**`（filter、backend、`daemon/cups-driverd`）是**源码编译的 2.4.19**。这也是为什么绝不能让 apt 装 `cups-core-drivers` —— 它会用 Debian 版覆盖 overlay 解包的那批文件。
>
> ⚠️ `cupsd` **不会自己创建** `/etc/cups/ssl`（源码里那处 `cupsdCheckPermissions` 的 `create_dir` 传的是 0，`lstat` 失败时既不 mkdir 也不报错），缺了它要到第一次 ipps 握手才炸 `Unable to create server credentials`，而 AirPrint 客户端优先挑 `_ipps._tcp`。以前它之所以存在纯粹是因为 Debian 的 `cups-daemon` 把它作为 package-owned 空目录发布 —— 那是别人的实现细节，现在 Dockerfile 与 entrypoint 各显式建一次。
>
> 五阶段设计理由、三架构镜像选型史、HOME/LibreOffice profile 见 [docs/docker-build.md](docs/docker-build.md)。

### CI/CD

- **`build-release.yml`**：7 平台交叉编译。v* tag → 正式 Release；master 分支 → 滚动 `dev` prerelease（git 标签 `dev`，不用分支名，避免重名冲突）
- **`docker-publish.yml`**：`master` / `v*` tag → 三架构镜像。v* tag → `vX.Y.Z` + `latest`；master 分支 → `dev`（`VERSION` 同步）。开头有 `Free disk space` 步骤。

### 版本管理

`./bump-version.sh patch|minor|major`

## 🎯 常见开发任务 / 调试 / 代码风格

> 新增 API、修改 DB、新增前端页面、新增文件类型、新增驱动的步骤模板，以及调试命令、Go/Vue 风格、Git 提交约定，见 [docs/conventions.md](docs/conventions.md)。
>
> 🚫 **commit message 禁止 `Co-Authored-By` 及任何 AI 署名行**，中文撰写。

**维护者**：涵曦（<im.hanxi@gmail.com>）
