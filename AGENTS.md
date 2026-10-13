# CUPS Web 开发者指南

本文档面向开发者，介绍项目架构、API、开发流程与扩展方式。用户文档请参阅 [README.md](README.md)。

> 📚 **深度文档**：原理说明、故障案例与历史决策已移至 [docs/](docs/README.md)，本文件只保留可快速扫读的规则与契约。

## 📦 项目概述

- **项目定位**：基于 CUPS 的 Web 打印管理工具，前后端分离
- **技术栈**：Go 1.26（后端）+ Vue 3（前端）+ PostgreSQL（存储）+ IPP（打印协议）
- **部署形态**：单二进制（前端 `go:embed`）连接外部 CUPS；**单容器（AIO）Docker 镜像**（`cupsd` + `cups-web` 同容器，内置 LibreOffice + Java 21 + OFD 转换器 + Ghostscript + 打印驱动生态）

> ⚠️ **历史形态提示**：仓库曾经是「cups 镜像 + cups-web 镜像」双容器（`cups/` 目录），已在合并提交里删除。现在只有根目录的一份 `Dockerfile` / `entrypoint.sh`，构建脚本在 `scripts/build/`、驱动脚本在 `scripts/driver/`。

## 🛠️ 技术栈

### 后端

| 组件 | 说明 |
| --- | --- |
| Go 1.26 | 见 `go.mod` |
| `gorilla/mux` | HTTP 路由 |
| `gorilla/securecookie` | 会话管理 |
| `pgx/v5`（`stdlib` 层） | PostgreSQL 驱动。**用它的 `database/sql` 兼容层**，于是 `*sql.Tx` / `sql.ErrNoRows` / `RowsAffected` 语义全不变 |
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
│   ├── pull_handlers.go           # 刷卡取件：提交 / 待取件 / 撤销
│   ├── kiosk_handlers.go          # 刷卡取件：终端口（唯一的无会话写入口）
│   ├── admin_kiosk_handlers.go    # 取件终端管理 + 工牌绑定
│   ├── payment.go                 # 渠道注册表 + 支付宝配置（微信只留了锚点，见 docs/wechat-pay.md）
│   ├── payment_handlers.go        # settlePayment / 回调 / 查单
│   ├── billing_handlers.go        # /api/prices、钱包
│   ├── admin_billing_handlers.go  # 管理端价目 / 订单 / 退款 / 调额
│   └── version.go                 # 构建期版本号
├── internal/
│   ├── auth/session.go            # securecookie 会话 + CSRF
│   ├── pricing/                   # 计价内核（纯计算，无 DB/HTTP）
│   ├── payment/                   # 支付渠道抽象 + mock + 支付宝（wap.pay / page.pay / 当面付）；微信未实现
│   ├── middleware/                 # csrf / security 中间件
│   ├── ipp/                       # IPP 客户端 + URI 校验
│   ├── server/static.go           # 静态资源嵌入（SPA fallback）
│   └── store/                     # 数据层（users / prints / settings / kiosk）
├── cmd/kiosk/                     # 树莓派端取件终端（evdev 读卡 + 声光反馈）
├── frontend/                      # Vue 3 前端（go:embed dist）
├── ofd-converter/                 # Java OFD → PDF
├── scripts/
│   ├── build/install-cups.sh      # 源码编译 CUPS
│   ├── driver/                    # 驱动管理命令 + 安装脚本 + capture-debs.sh（apt 钩子）
│   └── kiosk/                     # 取件终端的 systemd 单元 + 树莓派安装脚本
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
| GET | `/api/public-settings` | 登录前也要用的设置（自定义 CSS、访客模式、`alipayLogin`） |
| GET | `/api/auth/alipay/start` | 支付宝授权登录：取授权页地址 |
| POST | `/api/auth/alipay/exchange` | 支付宝授权登录：用 auth_code 换会话 |
| POST | `/api/kiosk/release` | **刷卡取件终端专用**。body `{"token","cardNo"}`，认证靠终端 token（见下）。🚫 一律 200，状态放 body —— 取件端没有屏幕，走 4xx 它只能笼统报「失败」。⚠️ 绑定窗口开着时，这次刷卡的含义是**绑定**而不是取件 |
| POST | `/api/kiosk/ping` | 终端自检：只验 token，**不碰任何状态**。🚫 自检不能用假卡号打 `/release` —— 那一刻正好开着绑定窗口的话，假卡会被绑给一个真人 |

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
| POST | `/api/pull-orders` | 刷卡取件：提交（**只暂存，不打印**） |
| GET | `/api/pull-orders` | 刷卡取件：我的待取件任务 |
| DELETE | `/api/pull-orders/{id}` | 刷卡取件：撤销（仅限本人、仅限 `held`） |
| POST | `/api/payments/{outTradeNo}/sync` | 主动查单（「我已支付」） |
| POST | `/api/payments/mock/{outTradeNo}/pay` | mock 渠道专用，需 `PAYMENT_MOCK=1` |

> ⚠️ `/api/print` 与 `/api/orders` 是**互斥**的两条通道：计费关闭时前者可用、后者 404；开启时反之。

#### `/api/printers` 返回形状

每项：`name` / `uri` / `info` / `location` / `makeAndModel` / `hasKiosk`。`info` 即 CUPS Web 界面里的「描述」（`printer-info`），前端下拉靠它区分同型号队列（issue #101）。

- `hasKiosk` 表示这台打印机配有**启用的**取件终端。刷卡取件模式下，提交页只列它的项为真的打印机 —— 选到没有终端的那台，任务会进一个**取不出来也不报错的黑洞**。查库失败时全部下发 `false`（宁可界面少列），后端下单处还有一道同样的校验。

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
| POST/DELETE | `/api/admin/users/{id}/card` | 绑定 / 解绑工牌（body `{"cardNo"}`）。冲突返回 **409 并带上占用者用户名** |
| POST | `/api/admin/users/{id}/card-bind` | 开一个**刷卡绑定窗口**（120 秒，全局单飞）。之后到终端刷卡即完成绑定 |
| GET/DELETE | `/api/admin/card-bind-sessions/{id}` | 轮询结果（`waiting`/`bound`/`closed` + 失败原因）/ 取消 |
| GET/POST | `/api/admin/kiosk-terminals` | 取件终端列表 / 新建（返回 token） |
| PUT/DELETE | `/api/admin/kiosk-terminals/{token}` | 改（名称/打印机/启停）/ 删 |
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

PostgreSQL，连接串走 `DATABASE_URL`（**必填，没有 SQLite 回退**）。表结构在 [`internal/store/schema.sql`](internal/store/schema.sql)，程序启动时 `//go:embed` 整份执行（全是 `IF NOT EXISTS`，可重复跑），也可以手动 `psql -f`。

### 几条踩过的坑，改之前先读

- **建库必须指定 `LC_COLLATE 'C'`**（`CREATE DATABASE ... LC_COLLATE 'C' LC_CTYPE 'C' TEMPLATE template0`）。PG 默认若是 ICU 非确定性 collation，`users.username` 的唯一约束会把 `Admin` 与 `admin` 判为冲突，而且 `WHERE username = ?` 变成大小写不敏感 —— **能用 `ADMIN` 登录**。`C` 是字节序，与原先 SQLite 的 BINARY 一致。
- 🚫 **时间列是 `TEXT` 存 RFC3339 UTC，不要改成 `timestamptz`**。UTC 的字典序等于时间序，所以排序与 `created_at < ?` 全部正确；换 `timestamptz` 要把 Go 侧 25 个时间字段改成 `time.Time`、连带约 120 处改动，而本仓库恰好有「时间差 8 小时、开发机永远复现不了」的教训。理由与代价写在 `schema.sql` 顶部。
- 🚫 **唯一约束冲突必须按 SQLSTATE 判断**（`isUniqueViolation` 用 `pgconn.PgError.Code == "23505"`）。原先匹配 SQLite 的 `"UNIQUE constraint failed"` 文本，在 PG 下永远为假 —— 调用方拿不到 `ErrDuplicateTransaction`，会把「重复入账、应当忽略」当成真失败而整笔回滚。
- 🚫 **`SUM(bigint)` 返回 `numeric`，必须 `::bigint`**（见 `sumPendingAmountSQL`）。扫进 `sql.NullInt64` 会失败，而那是余额支付的主路径。
- 🚫 **布尔列不能在 SQL 里跟 `1`/`0` 比**，写 `TRUE` / `FALSE`。`protected = 1` 是类型错误。
- 🚫 **`LastInsertId` 不存在**，自增主键一律 `INSERT ... RETURNING id` + `QueryRowContext`。
- 🚫 **`INSERT OR IGNORE/REPLACE` 不存在**。`settings` 的两处 upsert 是 `ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value` —— 写成 `DO NOTHING` 会让设置项**静默存不进去**（界面看着保存成功、刷新就回退）。
- **多语句的 schema.sql 必须整份交给 PG 解析**：pgx 默认走扩展协议，一次 `Exec` 多语句会报 `cannot insert multiple commands into a prepared statement`。`applySchema` 用 `Raw` 拿到底层 pgx 连接走简单协议，并包在一个事务里（PG 支持事务性 DDL）。

### 并发：SQLite 的全局写锁一直在替我们兜底

这是迁移时最重要的一条认知。SQLite 下「一个事务在写，别的事务连读都排队」，于是代码里大量**先 SELECT 再 UPDATE** 的写法*看起来*是原子的。PG 默认 READ COMMITTED 不是：两条语句拿到不同快照，中间可以插进别人的提交。**这些缺陷一直都在，是换库把它们揭开的。**

已经堵上的几处（都靠 `FOR UPDATE`）：

| 位置 | 不加锁的后果 |
| --- | --- |
| `GetPendingOrdersByBatch` | 返回的是 SELECT 那批；并发付款插在中间 → **去删一份已付款订单的文件** |
| `sumPendingAmountSQL`（余额支付用的那条） | 读完成与扣款之间有人取消订单 → **按取消前的总额扣钱** |
| `listOrderIDsByStatus` / `MarkOrdersPaidByPayment` | 返回的 id 集合与实际改到的行不一致 → 给已取消的订单送印 |

- ⚠️ **`FOR UPDATE` 不能在只读事务里跑**（PG 直接报错）。所以余额支付那条合计拆成了两个函数：`SumPendingAmountByBatch`（只读，`startOnlinePayment` 用）与 `SumPendingAmountByBatchLocked`（加锁，扣款路径用）。加锁前先确认调用方是 `WithTx(..., false)`。
- ⚠️ **多实例隐患（尚未处理）**：`userRefundLock` 是**进程内** `sync.Mutex`，而可退额度是一次非原子求和。单进程没问题，一旦跑第二个实例就会出现超额退款。真要多实例时得换成数据库层的锁或原子校验。同样地，「一个店一个实例」是当前的部署前提。

### `users`

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` | INTEGER PK | 自增 |
| `username` | TEXT UNIQUE | 登录名 |
| `password_hash` | TEXT | bcrypt |
| `role` | TEXT | `admin` / `user` |
| `protected` | INTEGER | `1` = 受保护 |
| `contact_name` / `phone` / `email` | TEXT | 联系信息 |
| `alipay_open_id` | TEXT | 支付宝身份，`''` = 不是支付宝账号。**部分唯一索引**（空串不参与） |
| `card_no` | TEXT | NFC 工牌号，`''` = 未绑。同样是部分唯一索引。🚫 存读卡器吐出的**原样字符串**，绝不归一 |
| `balance_cents` | BIGINT | 余额（分），只能经 `ApplyWalletDelta` 变动 |
| `created_at` / `updated_at` | TEXT | RFC3339 UTC |

> `alipay_open_id` 与 `card_no` 都**不在 `UpdateUserInput` 里** —— 它们是身份凭据，
> 只能经各自的专门函数改（`SetUserCardNo` 等），管理员改资料碰不到。

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

KV 表。当前键：`retention_days`（`0` = 永久）、`save_history`、`max_pages_per_job`、`max_upload_bytes`、`custom_css`、`guest_mode`、`session_hash_key` / `session_block_key`，计费相关的 `billing_enabled`（`0` 默认关闭）/ `order_ttl_minutes`（`15`）/ `price_fallback_cents`（`0`）/ `payment_methods`（空 = 不限制）/ `site_base_url`（空 = 渠道不会回调，靠主动查单），以及刷卡取件的 `pull_mode_enabled`（`0`）/ `pull_ttl_hours`（`72`）。

> `billing_enabled` / `pull_mode_enabled` / `guest_mode` **三方两两互斥**，两个方向各校验一次（见 [刷卡取件](#-刷卡取件拉式打印)）。

### 计费相关表

`price_rules`（单价）、`print_orders`（订单，自带**完整参数快照**——支付后按这些列重放 IPP，不引用 `print_jobs`，因为 `save_history=0` 时那张表根本没有记录）、`payments`（支付单，对订单**一对多**：靠 `print_orders.payment_id` 反向引用）、`wallet_transactions`（余额账本，`idempotency_key` UNIQUE）。详见 [计费与支付](#-计费与支付)。

刷卡取件复用 `print_orders`（状态 `held`、来源 `pull`、金额 0），另加一张 `kiosk_terminals`（取件终端：`token` 主键 / `name` / `printer_uri` / `enabled` / `last_seen_at`）。**没有**为取件另起任务表 —— 理由见 [docs/pull-printing.md](docs/pull-printing.md#为什么不新开一张表)。

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

### 支付宝授权登录（`alipay_auth.go`）

顾客扫码 → 一键登录 → 传文件 → 支付。顾客是陌生人、只来一次，**任何要填表的注册
都会被放弃** —— 这也是为什么没用短信验证码（要钱），以及为什么微信登录走不通
（要企业资质 + 300 元/年 + **ICP 备案域名**，微信明确不接受内网 IP）。

- 两条公开路由（`main.go` 的 `api` 段，**不挂** `RequireSession` —— 顾客此刻还没有会话）。
  `start` 只签发 state 并返回授权页地址；`exchange` 换 open_id 后签发会话。
  **返回 JSON 而不是 302**：端外唤起要靠前端的 `alipays://` 自定义 scheme，后端套不了。
- **scope 用 `auth_base`**：静默授权，用户看不到确认页。代价是拿不到昵称头像 —— 登录只要一个稳定身份，够用。
- **身份用 `open_id` 而不是 `user_id`**：官方明说 `user_id` 未来会逐步回收，新商户应当用
  `open_id`（需在应用里启用「openid 配置管理」）。只拿到 `user_id` 时专门报这一条 ——
  否则现象是「登录成功但没身份」，极难查。
- **`state` 存在服务端的 `alipay_login_states` 表里**，一次性消费，官方要求回跳时校验、
  不一致必须拒绝服务。🚫 不要改回 cookie（第一版就是那么写的，被真机推翻，理由见下节）。
- **授权页主机与网关不是同一台**（`openauth.alipay.com` vs `openapi.alipay.com`），
  规则在 `payment.AuthorizeURLFor`，`NewAlipay` 与启动日志共用同一份。
- **授权链接不需要签名**（只有 app_id/scope/state/redirect_uri 四个参数）。🚫 别照抄 `cashierURL`。
- **回调地址允许内网 IP + http + 非标准端口** —— 实测配 `http://192.168.1.22:5173/#/print`
  成功，支付宝**不校验域名备案**（这点与微信完全不同，网上大量说法是错的）。
- 账号懒创建：`username = alipay:` + **6 位随机 base32**（🚫 不是 `alipay:<open_id>` ——
  128 字符的 open_id 铺在界面上认不出谁是谁），唯一的 `open_id` 落在 `alipay_open_id`
  列上（部分唯一索引）。密码是随机 bcrypt（照 `loadOrCreateGuest`）。
  `LoginHandler` 与管理员建/改都**拒绝 `alipay:` 前缀**，把命名空间留出来。
- 🚫 **个人中心必须显示【原始用户名】+ 复制按钮。** 曾经美化显示成「支付宝用户」，
  结果是顾客要退款、联系管理员时报不出自己是谁 —— 管理员查不到该退哪一单。
  **这个字符串就是顾客唯一的身份凭据，不能替他藏起来。**（管理端列表则可以截断 + 角标，
  见 `userLabel()`；那页支持按用户名子串查询。）

#### 授权在支付宝里做，会话交回浏览器（真机结论，别推翻）

三条真机事实，是这套绕法的全部理由：

| 做法 | 结果 |
| --- | --- |
| 系统浏览器里直接开授权链接 | ❌ **死路** —— 「抱歉，出错了 / 请在支付宝客户端打开链接」，连可点的按钮都没有 |
| 系统浏览器里 `alipays://` 唤起 | 能授权，但结果落在**支付宝的 webview** 里 |
| 支付宝内打开页面再授权 | ✅ 正常 |

官方文档也这么写：「如果网站支付宝登录在手机端 H5 页面进行授权，**必须要在支付宝客户端内去打开 H5 页面**」。
所以端外没有第三条路 —— 授权只能在支付宝里完成。

**但支付宝的 webview 干不了活**（同样是真机测的）：

- 它的文件选择器**翻不到微信/QQ 里发来的文件**。浏览器用的是系统文件管理器，直接列出
  「快捷访问：微信 / QQ / 钉钉」。
- `pdf.js` 的 worker 在里面起不来，**PDF 预览一直转圈**（浏览器里正常）。

所以最终形态是「**授权在支付宝里做，会话交回浏览器**」（微信 PC 扫码登录是同一套）：

```
浏览器  GET /api/auth/alipay/start（建一行状态）→ alipays:// 把授权页交给支付宝 → 开始轮询
支付宝  静默授权 → 回跳 …/#/print?auth_code&state → POST /exchange（把 user_id 写进那行）
浏览器  GET /api/auth/alipay/poll?state=… 轮询到 user_id → 给自己签发会话 → 继续上传/支付
```

- **`state` 必须在服务端**（`alipay_login_states` 表），不能放 cookie：浏览器和 webview 是
  **两个 cookie 罐**，cookie 过不去。这是真机测出来的，不是设计偏好。
- 轮询之所以可能：自定义 scheme 唤起 App 时浏览器**不会卸载当前页**（见
  `PaymentPanel.openAlipayApp` 的注释），那一页还在，能继续发请求。
- 状态行 `user_id = 0` 表示还没兑换；`from_browser` 表示"发起方是浏览器、会有人来轮询"。
  支付宝扫一扫直接进来的那种没有轮询方，就地登录 —— `exchange` 返回 `needBrowserReturn`
  区分这两种，前端据此决定要不要提示"请回到浏览器"。
- `/poll` 用 **200 + `status`** 而不是 4xx：它是轮询，前端每次都要判断；走 4xx 会被
  `apiFetch` 的全局兜底当成会话失效弹登出，而此刻用户根本还没登录。
- `/start` 里顺手清过期的状态行（不塞进 maintenance）—— 这张表只有正在登录的人在用，
  量极小，而过期行留着是有害的（state 可被重放）。
- 离页与轮询的逻辑全收在 `frontend/src/utils/alipayAuth.js` 一个文件里，换唤起方式只改它。

> ✅ **已确认**：`alipay_system_oauth_token_response` **带签名**，严格验签照常走。
> （证据：真机那次报的是"业务节点里没有 code"，而这只可能发生在 `rawResponseNode`
> 与 `verifyResponse` **都成功**之后 —— 否则会报"响应没 sign"或"验签失败"。）
> 🚫 所以别为了省事放开验签，那个口子没有理由开。

#### 🚨 iOS 必须 HTTPS —— 上线前的硬前提

**支付宝 iOS 客户端是 WKWebView，iOS 的 ATS 默认禁止加载 http 明文**（App 得在
`Info.plist` 里显式放开，支付宝没为普通网页放开）。真机实测：

| 设备 | 支付宝内置浏览器加载 `http://192.168.1.22:5173` |
| --- | --- |
| 安卓 / 鸿蒙 | ✅ 正常 |
| **iOS** | ❌ **拦截** → 白屏 |

现象分两段：先弹「**是否前往外部网站**」（支付宝跳离自有页面的安全确认，提示里显示的
正是我们的回调地址 → 说明授权其实**已经做完**，卡在最后把 `auth_code` 交回来），
用户点确认后**一直空白加载**。

🚫 **改代码救不了**，这是渠道客户端的限制。**生产的公网地址必须是 `https://`**，
否则 iOS 顾客一路白屏。（已与用户确认生产就是 https。）

- 开发期内网 http 验证不了 iOS，可临时用内网穿透拿个 `https://` 地址在 iPhone 上跑一遍。
- 次要嫌疑：**回调地址里的 `#`**，支付宝社区有"带 `#` 在 iOS 端会造成 bug"的反馈。
  若生产 HTTPS 下 iOS 仍失败，**第一件事就是把回调换成无 `#` 的路径**
  （如 `https://<域名>/alipay-callback`）。后端不用改 —— SPA fallback 会吐 `index.html`，
  而 `readAlipayAuthReturn()` 本来就同时读 hash 内和 hash 外两种落点（支付回跳的经验：
  支付宝会丢 fragment）。只需改后台那处配置 + `paymentReturnURL` 的调用点。

#### `alipay.system.oauth.token` 和别的接口不一样（两处，都栽过）

这个接口属于支付宝的**老式形态**，请求和响应两边都跟主流不同。
🚫 别照抄其它接口的写法：

| | 其它接口 | 这个接口 |
| --- | --- | --- |
| 业务参数位置 | JSON 塞进 `biz_content` | **顶级表单项**（`-F 'grant_type=...'`）→ 用 `callFlat()` |
| 成功响应 | 带 `code: "10000"` | **根本没有 `code` 字段**，成功信号是 `open_id` 有值 |

两处都是真机踩出来的，而且错误信息都**指向错误的方向**：

- 参数放错位置 → `isv.isv.grant-type-invalid`，只说"grant_type 参数不正确"
- 用 `code != "10000"` 判失败 → 把**每一次成功**都判成失败，错误信息还是空的（`支付宝授权失败: /`）

`alipay_oauth_test.go` 里有对应的回归断言。⚠️ 那里的**成功夹具必须与官方文档示例同形** ——
我第一版是照自己的印象写的（带 `code:"10000"`），结果测试和代码犯了同一个错、一起绿了。

#### 顾客的文件常常在微信/QQ 里 —— 这是"会话必须交回浏览器"的另一个理由

⚠️ 我一度以为"微信/QQ 的文件在应用沙箱里、系统选择器看不到、必须先保存到手机"。
那是按**通用 Android 规则**推的，被真机证伪：国产 ROM 的文件管理器会把微信/QQ 的目录
直接列出来（实测「快捷访问」里有「微信 606 项 / QQ 679 项」），**点进去就能选**。

但**支付宝 webview 里的选择器没有这些入口** —— 它自己拉了一个原生选择器，只有
最近/图片/视频/文档。这由宿主 App 决定，我们的代码左右不了。

所以：**上传这一步要放在浏览器里做**，顾客选微信发来的文件才是顺手的。这条和"PDF 预览
在 webview 里转圈"一起，构成了把会话交回浏览器的理由。

- 登录页那块「要打印的文件在微信 / QQ 里？」的折叠指引（安卓 / iPhone / QQ 三种说法）
  **仍然保留**：浏览器里虽然选得到，但顾客未必知道去哪儿找，讲一句没坏处。
- ⚠️ 它是**设备相关**的：换一台限制更严的机器（或 Android 进一步收紧 `Android/data`
  访问）就可能翻不到。那段指引是兜底，🚫 别删。

> 完整的已核实事实（为什么支付宝免资质免备案、`open_id` 取代 `user_id` 的官方原文、
> 授权链接为何不需要签名、三个 handler 的分工、iOS HTTPS 前提）见
> [docs/alipay-login.md](docs/alipay-login.md)。
>
> 💳 **上线换支付宝账号照 [docs/支付宝正式环境配置手册.md](docs/支付宝正式环境配置手册.md) 做**
> —— 要签哪几个产品、密钥怎么生成上传、环境变量、上线检查清单、**换账号的分步流程**。
> 改动支付配置（新增环境变量、换网关、改回调）时记得同步那份手册。
>
> ⚠️ **个人实名账号就能签「手机网站支付 + 电脑网站支付 + 获取会员信息」，实测过。**
> 网上「支付产品必须企业资质 / 个人只能做沙箱」的说法是过时的，别往文档里抄 ——
> 这类市场规则随平台政策变，**以开放平台控制台当前的提示为准**。

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

## 🖥️ 前端信息架构

导航只有**两项**，手机与电脑共用同一套（`App.vue`）：

| 导航项 | 路由 | 内容 |
| --- | --- | --- |
| 在线打印 | `/print` | 落地页：打印机 + 四个模式按钮，选完才进具体打印界面 |
| 个人中心 | `/me` | 入口列表：打印记录 / 钱包 / 定时 / 管理 / 驱动 / 登出 |

另有 `/done`（**打印/支付完成后的中间页**，见 `ResultView.vue`）。它不在导航里，**两条支付路径都收敛到这里**：

- 手机网站支付：付完款支付宝把浏览器送回 `return_url`，页面是**重新加载**的 —— 直接落回「在线打印」的落地页（四个按钮）的话，用户既看不到刚才打的是什么，也没有收尾感。批次号由查单接口带回（`?batch=`）。
- 余额支付：弹窗的成功页结束（倒计时走完或点「返回」）后跳过来 —— 见 `PaymentModal` 的 `onSuccessDone`。🚫 不要停在打印页：弹窗一关，「刚才那笔」在界面上就没有痕迹了。

**为什么必须有它**：用户刚付完钱，最想知道的是「我的任务在哪」。弱反馈会让人怀疑钱花了、任务没下。

- 🚫 **不要用「最近一条订单 / 最近一条打印记录」去猜是哪一笔** —— 多设备或连着打几次时必然猜错，用户看到的是别人的单子。批次号必须从查单接口或内存里的 `orderBatch` 带过去。
- ⚠️ **代价**：跳到 `/done` 会让打印页卸载（`onUnmounted` 清文件），所以「同样一份再打一次」需要重新上传。这是有意的取舍 —— 页面的收尾感优先。要照顾重复打印的话，得在中间页加「再打一份」，而不是取消这个页面。
- 订单状态标签用 `orderStatusText/orderStatusColor`，🚫 不是 `statusText/statusColor`（那对是打印记录的）—— 详见下面那节。

### 两个「名称很像但语义不同」的坑

- 🚫 **`statusText/statusColor`（`utils/format`）映射的是【打印记录】的状态**（`queued/printed/failed/cancelled`）；**订单**状态是另一套（`printing/refunded/expired/...`），要用 `orderStatusText/orderStatusColor`。拿错的那对去渲染订单，`printing` 会落不到映射里、被原样透出英文。实测踩过：中间页的订单标签显示成 `printing`。四个使用者（`PaymentModal` / `WalletView` / `ResultView` / `AdminOrdersView`）共用这一份，🚫 不要再各写一份。同理，订单/流水的筛选项「全部」哨兵值走 `utils/adminFilters.js` 的 `FILTER_ALL`。
- ⚠️ **`App.vue::loadSession` 里读 `route.path` 之前必须 `await router.isReady()`。** 路由守卫里有一次异步的 session 请求，首次导航在 `onMounted` 时还没落定，此刻 `route.path` 仍是初始的 `/` —— 那个「根路径就兜底跳 /print」的判断会命中，把用户真正想去的那一页冲掉。实测：刷新 `/#/records`、`/#/wallet` 都会被送到打印页。

**打印页只留操作，不留历史与运维信息**：

- **打印记录搬到了个人中心的 `/records`**（`RecordsView.vue`）。它带 5 秒轮询与重打（计费开启时重打转订单、要弹支付窗），所以那个页面把 `PrintRecordList` + `PaymentModal` 整套都接了过去。打印页不再持有记录列表。
- **CUPS 任务按钮与「打印机状态」卡片只有管理员看得到**（`v-if="isAdmin"`，`PrintView` 从 `session` prop 取角色）。墨量、纸盒、队列是运维信息，普通用户看到只会以为要自己处理。
- **普通用户看的是打印机名字下面那个连接状态灯**（`PrintView` 的 `deviceState`）。判据取 `/api/printer-info` 的 `device.probed` / `device.reachable`，🚫 不能用 cupsd 的 `printer-state`（那是队列状态，打印机拔电照样报 idle）。⚠️ `probed=false`（USB 等探不了）要显示中性态，**不能**和「连不上」混为一谈。
- ⚠️ `PrintRecordList` 会 emit `reprint-open`（带上目标打印机 URI）。进纸盒可选项 `mediaSourceSupported` 得由父组件按这个 URI 现查 —— 记录页没有「当前打印机」这个概念，而打印页要传的是它当下选中的那台，两边需求不同。

🚫 **不要再往顶栏加导航项**。手机顶栏放不下，而且两套导航必然各自漂移。
新页面一律挂到个人中心下面（`ProfileView.vue` 的 `entries`），**子页面再往下加一层**
（见下面「系统管理」）。顶栏只在 `/me` 及其子页面上高亮「个人中心」，
见 `App.vue` 的 `PROFILE_PATHS`。

#### 系统管理是「落地菜单 + 子页」，🚫 不要再堆回单页

`/admin` 只是一个落地菜单（`AdminView.vue`），六个板块各是一个子路由：

```
/admin            落地菜单（AdminView.vue）
/admin/users      用户管理      AdminUsersView.vue
/admin/records    打印记录      AdminRecordsView.vue
/admin/orders     打印订单      AdminOrdersView.vue
/admin/wallet     余额流水      AdminWalletView.vue
/admin/billing    计费设置      AdminBillingView.vue
/admin/kiosk      取件终端      AdminKioskView.vue
/admin/settings   系统设置      AdminSettingsView.vue
```

- **为什么拆**：原先六块同页，`onMounted` 会**并行拉四个「全站」列表**（用户 / 打印记录 / 订单 / 流水，后端都无上限）。拆开后每个子页只加载自己那一份，负载降到 1/4；六个子页也都改成了懒加载，管理端代码不再进首屏包。
- ⚠️ **子页清单只有一份**：`utils/adminSections.js` 的 `ADMIN_SECTIONS`。落地菜单与子页标签栏（`components/admin/AdminTabs.vue`）都从它取 —— 各写一份的表现是「菜单里点得到、标签栏里切不过去」。新增子页只需改这一处 + `router/index.js` 注册路由。
- ⚠️ **`App.vue` 的 `PROFILE_PATHS` 必须保持【前缀匹配】**（`startsWith(p + '/')`）。改回 `includes(route.path)` 的精确匹配，所有 `/admin/xxx` 子页上顶栏就都不高亮了，而表现是「看起来像导航坏了」。
- ⚠️ **子页之间不要互相重载数据**。原先「退款后顺带刷新用户与流水列表」「清理后顺带刷新打印记录」这类跨板块刷新，在拆页后指向的是没挂载的页面，必须删掉 —— 每个子页每次进入都会重新挂载并自己拉取。
- ⚠️ 子页的 loader **失败必须有可见错误态**，且 `billing` / `settings` 两页在加载失败时要**禁用保存按钮** —— 否则表单停在初值，管理员照着「默认的」界面点保存，就把真实配置覆盖掉了。

- **打印落地页是刻意的**：`PrintView.vue` 的 `entered` 为假时只渲染打印机与四个模式按钮。手机屏小，一上来就把上传区、十几个参数、预览、打印记录全堆出来，用户不知道从哪儿下手。
- ⚠️ `entered` **不用** localStorage 里的 `print_mode` 决定 —— 每次进这个页面都先看落地页。存下来的模式只用来让 `switchMode` 在「选的还是同一个模式」时保留已填好的内容。
- ⚠️ 打印页的**手机**顺序是「上传 → 参数 → 预览 → 费用 → 按钮 → 记录 → 状态」，靠外层栅格 `gap-4` + 两个 column 包装器在手机上 `contents` 拆盒 + 四个 `order-N` 分组实现。桌面仍是两栏、左栏内部顺序不变（`order-*` 在块布局里不生效）。🚫 别把 `contents` 当成多余的类删掉。
- ⚠️ **`emit('logout')` 必须接到 `logout()`，不是 `onLogout()`**：后者只清内存里的 session，服务端 cookie 还在，刷新一下又自动登回去。早先只有顶栏的登出按钮走 `logout()`，视图 emit 仅用于 401 自动登出，所以这个坑一直没露头。
- 钱包页的**打印订单 / 余额流水默认收起**（最多 100 / 50 条，展开着要划很久），折叠写法照抄 `PrintRecordList.vue`：可点的标题 + chevron，内容是 `overflow-hidden` + `maxHeight` 过渡。
- **管理端各子页（`views/admin/*.vue`）的表格分页**走 `utils/tablePage.js` 的 `usePagedRows`：默认 20 行，配 `UPagination`，并显示「共 N 条」。后端那几个 `/api/admin/*` 列表**没有任何上限**（`PrintFilter` 有 `Limit` 字段，但管理端 handler 不传），有多少返回多少 —— 所以前端必须自己收口。⚠️ 拆成子页只把「一次拉四个全站列表」降成了「一次拉一个」，**没有解决单表本身无上限的问题**；真到了百万行量级，得后端支持 `limit`/`offset` 才算解决。
  - 🚫 **不要以为给 `UTable` 传 `:pagination` 就会分页。** @nuxt/ui 4.7.1 的 `UTable` 只注册了 `getCoreRowModel` / `getFilteredRowModel` / `getSortedRowModel` / `getExpandedRowModel` —— **没有 `getPaginationRowModel`**。那个 `pagination` 属性只是状态透传（配 `onPaginationChange` 用），传进去**不会切片，表格照常渲染全部行**。看着完全正确、构建和静态检查全绿，但页面长度一点没变 —— 只有拿真数据渲染出来才看得见。要真分页就得自己 `slice`（现在的做法）。
  - ⚠️ **空表有两个方向，两个都要堵**：①重新查询时页码要归零（在第 5 页搜出 3 条 → 空白）；②数据变少时页码要往回夹（在最后一页删掉唯一一条 → 空白）。`usePagedRows` 各有一条，删任何一条都会出现「明明有数据但表格是空的」。
  - ⚠️ 归零只放在**「查询」按钮**上（`searchAdminOrders` 等独立无参函数），🚫 不要塞进 loader —— 退款/调额之后也会重跑 loader，那时用户在第 3 页操作完被弹回第 1 页很难受。也不要把这些 `search*` 写成带可选参数的（模板里 `@click` 会传 `MouseEvent`，理由见下面那条）。

### 页面被手机浏览器回收后要能接回去

实测：传好文件、预览正常 → 切到微信回消息 → 手机内存紧张，浏览器把**标签页整个回收** → 切回来时页面重新加载，文件没了，一切从头。

回收本身拦不住 —— 那是浏览器的内存策略，网页没有任何 API 能阻止。但文件是我们自己持有的对象，可以提前存一份。

| 存什么 | 存哪 | 为什么 |
| --- | --- | --- |
| 模式、打印参数、速印文字 | `sessionStorage` | 小数据，同步写、便宜。`PrintView` 里一条 watch 每次变化就写 |
| 用户上传的原始文件 | IndexedDB（`utils/pendingUpload.js`） | 二进制、几十 MB。localStorage 只能存字符串且 5MB 上限，一张手机照片就超 |

- ⚠️ **只在确认是浏览器重载时恢复**（Navigation Timing 的 `reload` / `back_forward`）。从导航进这个页面时看到落地页是刻意的入口设计，不能被自动填上上次留下的文件。
- ⚠️ **恢复走同一个入口函数**（`processFile` / `processMultipleImages` / `batch` / `convertQuickText`…），不要自己去拼内部状态（`previewUrl` / `pdfBlob` / `converted` / `imageThumbnails`…）—— 那样迟早漏掉一个 ref，表现为「预览看着对但某个参数没生效」，极难查。
- ⚠️ **不要直接把 `File` 存进 IndexedDB**：个别浏览器（尤其老 WebView）克隆回来只剩 `Blob`，`name` / `type` 丢了 —— 而 `processFile` 全靠这两个字段认文件类型。`packValue` / `unpackValue` 负责包装与还原（导出是为了能被单独验证）。
- ⚠️ 文件集合的同步用**一条 deep watch** 覆盖全部入口，🚫 别往 `processFile` / `removeImage` / `removeInvoiceFile` 里逐个插调用 —— 漏一个的表现是「某种方式选的文件丢了」，几乎复现不出来。
- ⚠️ **暂存的是用户的原始文档，属隐私数据**：打印成功、登出都要 `clearPendingUpload()`，TTL 6 小时兜底。
- 诊断入口：`PrintView` 挂载时会打一行 `[print] 页面加载类型: …`。手机才能复现的问题，离线判断不了浏览器的加载类型，这行不是调试残留，🚫 别删。

### PDF 预览在手机上失败 → 先怀疑「缺新 JS API」

`pdfjs-dist` 5.x 大量直接调用很新的 JS 内置方法，**没有任何 feature-detect 兜底**。旧内核浏览器（国产浏览器、老 Android）上整份 PDF 在加载阶段就抛异常，而图片走另一条渲染路径不受影响 —— 所以「PC 上能预览、手机上不能」是它的典型表现，**根因是浏览器版本，不是文件、不是网络**。

已经踩过三次，都在 `frontend/src/polyfills/` 下补掉了：

| 缺失的 API | 报错 |
| --- | --- |
| `Uint8Array.prototype.toHex` / `toBase64`、`fromBase64` | `a.toHex is not a function` |
| `Promise.withResolvers` | `Promise.withResolvers is not a function` |
| `Map`/`WeakMap.prototype.getOrInsert` / `getOrInsertComputed` | `this[#methodPromises].getOrInsertComputed is not a function` |

🚫 **补的时候必须在【两个地方】各加一次 import**：`main.js`（主线程）+ `polyfills/pdf-worker.js`（worker）。二者是相互独立的 JS 执行环境，主线程补上了 worker 里照样缺。加完 `grep -l` 一下 `dist/assets/pdf-worker-*.js` 确认进了 worker 包。

⚠️ 预览失败时界面会显示 pdf.js 的**原始报错**（`PrintPreview.vue` 的 `pdfPreviewError`）。别把它改成「预览失败」这种笼统文案 —— 那一行原文就是诊断结论，翻译一遍反而丢掉线索。

## 🧹 维护任务

`maintenance.go` 每小时：读 `retention_days`（`0` 跳过）→ 删过期 `print_jobs` + 文件 → `VACUUM` + `wal_checkpoint`。管理员可 `POST /api/admin/cleanup` 手动触发。

> ⚠️ 清理必须跳过**未终结订单持有的文件**（`protectedRels`，来自 `ListActiveOrderFiles`），否则已付款未打印的订单文件会被误删。
>
> 🚨 **这份名单里必须包含 `held`（刷卡取件的待取件）** —— 少了它的表现是
> 「员工刷卡时文件不见了」，而根因是一小时前的这次清理。加订单状态时，
> 要改的是这条 SQL 里硬编码的 `IN` 列表，**不是**那个无人引用的
> `OrderStatusesActive`（已删）。见 [docs/pull-printing.md](docs/pull-printing.md)。

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
- 重打转订单后前端**不需要分支**：靠响应里有没有 `batchId` 区分（[PrintView.vue](frontend/src/views/PrintView.vue) 的 `handleReprint`）；取件模式则靠 `held`。

**刷卡取件模式下**这四个入口的对应关系：`/api/orders` 与 `/api/print` 都 **403**，唯一合法通道是 `POST /api/pull-orders`；重打同样转订单但落 `held`；**`scheduler.go` 的定时打印被显式拒绝**（报失败并写进 `last_error`）—— 它绕过订单链路直接送 IPP，在取件模式里就是「保密文档无人值守地躺在出纸口」，而那正是拉式打印要消灭的场景。

### 金额与计价

金额一律以**整数分**存储与传输，前端 `formatCents` 换算展示。

⚠️ **任何把「分」换成「元」的地方都必须保留到分，且往返可逆。** 实测踩过（退款弹窗的「全部退款」）：

- `Math.floor(cents / 100)` —— 在**元**这一级取整，把不足 1 元的部分整个抹掉。可退 1 分时填出来是 `0`，按钮因 `refundCents <= 0` 被禁用，**那笔钱在界面上永远退不出去**。正确写法 `(cents / 100).toFixed(2)`。

判据很简单：**「可退 X 分」→ 填进输入框 → 换算回分，必须还等于 X**。改动金额相关的 UI 时照这个走一遍。

> 🚫 **排查金额问题时别被 `step` / `min` 带偏。** 它们**只**影响步进器与浏览器的校验 API，根本拦不住用户直接输入的值 —— 充值输入框当时也写着 `min="1"`，用户手输 `0.01` 照样充得进去（后端只校验 `> 0` 且 ≤ 100 万元，没有起充门槛）。把 `min` 当成根因会修错地方：真正的门在它之后 —— 值先被 `Math.floor` 算成 `0`，按钮才禁用的。两个输入框现在都统一成 `step="0.01" min="0.01"`，但那是**为了步进器行为正确**（一次一分而非整元跳），不是因为它能拦住谁。

- 计价内核在 `internal/pricing`，前端镜像在 `frontend/src/utils/pricing.js`，**两边跑同一套用例**（`testdata/pricing_cases.json`，`go test` + `npm run check:pricing`）。改一边必须同步改另一边。
- 计费口径 = `|page_range ∩ [1..N]|` 过滤 `page_set` 后 × 单价 × 份数。**N-up 不折算**，补白页不计费。
- 🚫 **计费用重排前的 `DocPages`**，不是 `PrintPages`——even-reverse 会补一张空白页，用后者会多收钱。

### 订单状态机

```
pending_payment ─┬─ 余额/在线支付到账 ──► paid ──(原子领取)──► printing ─┬─► printed
                 ├─ 用户取消 ──► cancelled                              └─► failed（自动全额退款）
                 └─ 超时 ──► expired（删文件）
printing ──(30 分钟无终态)──► failed（结果未知，**不自动退款**，交管理员判断）

                                   【刷卡取件，与计费互斥】
held ──(刷卡，原子领取)──► printing ─┬─► printed
  │                                  └─► failed（**不退款**，取件压根没收钱）
  └─ 超过 pull_ttl_hours ──► expired（删文件）
                 └─ 送印失败 ──► 退回 held（可原地重刷）
```

`ClaimOrderForPrint`（`UPDATE ... WHERE status='paid'`）是**付一次印十次**的唯一门控；
取件侧的对等物是 `ClaimHeldOrdersForPrinter`（`WHERE user_id + printer_uri + held`）。

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

> ⚠️ **微信有意不在这个列表里**（`availableProviders` 里没有它的分支），不是漏了。
> 它是 `/api/wallet` 里 `paymentMethods` 的唯一来源，驱动钱包充值的渠道下拉与
> 打印弹窗的支付方式列表，而这些入口最终都走到 `buildProvider("wechat")` ——
> 那个分支现在恒返回 `ErrUnavailable`。列出来就等于在三个界面上摆一个必定 400
> 的选项，正是上面这条规则要防的事。接入时给微信的 configured 判定要与支付宝
> **同构**，别无脑 `append`。见 [docs/wechat-pay.md](docs/wechat-pay.md)。

#### 支付宝：两种收款方式，按设备分流

这个程序主要给手机用，但电脑上也要能用。两种设备拿什么付款完全不同，所以同一个渠道下备了两条路，由前端判断设备后在支付请求里带 `channel`：

| channel | 接口 | 谁用 | 拿到什么 |
| --- | --- | --- | --- |
| `wap` | `alipay.trade.wap.pay`（手机网站支付） | 手机 | 收银台地址，**当前标签**跳过去；收银台自动唤起 App，付完支付宝把浏览器送回 `return_url` |
| `page` | `alipay.trade.page.pay`（电脑网站支付） | 电脑 | 同上，跳到收银台；电脑上收银台页面显示二维码可扫 |
| `qrcode` | `alipay.trade.precreate`（当面付） | — | 一串 `qr_code`，渲染成二维码让手机扫。**前端现在不选它**，见下 |

⚠️ **`qrcode` 这条路前端不再主动选**。当面付（FaceToFace）是**线下收银场景**的产品（它的另一个 API `alipay.trade.pay` 是商家用扫码枪扫用户的付款码），网页应用一般签不下来 —— 官方给网页应用的就是上面两个「页面跳转类」产品。保留这条分支是因为它的体验确实更好（二维码直接渲染在我们页面上、不跳转），哪天签到了当面付，把前端 `utils/device.js` 的 `payChannel()` 改回 `'qrcode'` 即可。

> 注意文档口径不统一：`precreate` 在支付宝官方路由表里常被归为「订单码支付」，EasySDK 里才列在「当面付」下。别被术语绕进去——**看你的应用类型能签哪个产品**。

- 🚫 **`return_url` 用内网地址完全可以**（`http://192.168.1.22:1180/#/wallet`）。回跳是**手机浏览器**去访问的，支付宝服务器不需要可达它 —— 不需要公网域名、不需要备案。真正需要公网可达的只有 `notify_url`，而本项目压根不发（到账一律靠 `alipay.trade.query` 主动查）。
- 🚫 **`wap` 拿不到回跳地址时必须降级成 `qrcode`**（`createPayment` 里做）。没有 `return_url` 时用户付完会停在支付宝自己的结果页上，还得自己找回来 —— 那正是改用官方方案要解决的问题本身。
- 🚫 **`wap` 这一步不发任何 HTTP 请求**：手机网站支付是页面跳转类接口，服务端只是把签名好的参数拼进收银台地址。于是**签名错误、没签产品、app_id 不对都不会在本机报错**，只会在用户手机上变成一个支付宝错误页。改这段必须真机联调，`alipay_test.go` 的 `TestAlipayCreateWap` 会自己验一遍签名兜住。
- ⚠️ **`product_code` 必须是 `QUICK_WAP_WAY`**，传错被网关直接拒。
- ⚠️ 前端渲染依据是**服务端返回了哪种凭据**（`qrCodeText` 是否为空），不是再看一遍设备 —— 服务端降级时界面要跟着降级。空字符串的二维码 value 会让 Reka UI 抛错打崩组件树。
- ⚠️ **回跳后页面是重新加载的**，内存里什么都没了。`App.vue` 的 `handlePaymentReturn` 凭 URL 上的 `out_trade_no` 补一次查单 —— 少了它，用户付完回来看到的是一个毫无反应的页面。为此 `loadSession` 不能无条件 `push('/print')`（会把查询参数冲掉）。
- 🚫 **结算完必须 `announcePaymentSettled()`（`utils/paymentSignal.js`），不能只改数据。** Vue 是子组件先挂载、`App.vue` 的 `onMounted` 最后跑，所以回跳到钱包页时 WalletView 那次 `load()` 拉到的还是**到账前**的余额；而 `router.replace('/wallet')` 因为已经在这一页是个空操作，不会重新挂载。少了这个广播，用户看着「充值成功」的提示、余额却纹丝不动，得手动点刷新才变。WalletView 与 PrintView（价格条上的余额）各 watch 一次。

配置全走**环境变量**（不进数据库——私钥落库会跟着备份一起流出去）：

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
  - 🚫 查单是**出站请求，必须在事务外**做：放进 `WithTx` 会占着数据库事务等网络往返
- 「我已支付」按钮**不加节流**：它是认证过的人类点击，加了反而时灵时不灵；节流只约束自动轮询

微信支付**尚未实现** —— 代码里只有锚点（`buildProvider` 的 `wechat` 分支、
启动日志、前端 logo）和两条绊线测试，**没有协议代码**，这是有意的。
接手前先读 [docs/wechat-pay.md](docs/wechat-pay.md)：产品选型（Native/H5/JSAPI）、
备案与资质门槛、内网下 H5 为何不可行、以及「为什么先不写签名」。

> ⚠️ 这里原先写的是「需平台证书轮换 + 回调 AES-GCM 解密」。那是**未经核实**的
> 技术描述（我凭印象写的），已删。真实工作量取决于产品选型，而定选型之前
> 谈协议细节没有意义。别再凭印象往这行补技术名词。

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

## 🎫 刷卡取件（拉式打印）

企业内网形态：员工在电脑上提交 → 任务**暂存在服务器**（不打印）→ 走到打印机旁
刷工牌 → 那台打印机才出纸。与便利店的「扫码 + 计费」是两个部署形态。

`pull_mode_enabled` 默认 **0**。与 `billing_enabled`、`guest_mode` **三方两两互斥** ——
它们回答的都是「谁是这个部署的打印入口」，互斥在 `adminUpdateSettingsHandler`
与 `adminUpdatePricesHandler` 里**两个方向各校验一次**。

**复用**「先提交、后送印」那套机器，不另起炉灶：`prepareOrderPrint` → `print_orders`
（新状态 `held`、来源 `pull`、金额 0）→ 刷卡时 `ClaimHeldOrdersForPrinter` 原子领取 →
`startPullPrintForOrder` 重放 IPP。核对链路（`confirmPrintResults` /
`decidePrintAction`）完全共用。

### 🚨 三条最容易踩的

1. **`held` 必须在 `ListActiveOrderFiles` 的保护名单里**（`internal/store/orders.go`）。
   少了它，maintenance 每小时会把「员工还没取件」的文件当过期文件删掉 ——
   表现是**员工刷卡时提示文件缺失**，根因却在一小时前的例行清理。
   `internal/store/pull_test.go` 的 `TestListActiveOrderFilesIncludesHeld` 钉着它。
   - ⚠️ **别去找 `OrderStatusesActive`** —— 那个变量自称是同一件事，但**全仓无人引用**，
     已删除。照着死代码改会以为保护到位了。
2. **释放失败绝不走退款路径**。`failOrderAndRefund` 即便金额为 0 不会真退钱，
   它也已经先把订单标成 `failed` 了 —— 员工的任务照样丢。取件走 `revertPullOrder`
   （退回 `held`，可原地重刷）与 `failPullOrder`（标失败，不退款）。
3. **孤儿恢复的判据是 `error = PullClaimMarker`，不是「`job_id` 为空」**。
   「送印成功但任务号回写失败」的订单形状一模一样，回退它会让员工下次刷卡
   **重复出纸**。

### 其余要点

- **释放到哪台 = 提交时选的那台**（`printer_uri` 硬约束）。代价是员工要
  走回那台，换来的是参数绝不会与目标机不匹配。
- **送印之后的失败不退回 `held`**：任务已被 CUPS 收下、纸有没有出来存疑，
  自动重打会**重复出纸**。`reconcilePrintingOrder` 里对 `source='pull'` 做一次
  动作翻译（`pullReconcileAction`），🚫 **不要改 `decidePrintAction` 本身** ——
  它是被逐分支覆盖的纯函数，是唯一决定「已付的钱要不要退」的地方。
- **卡号在读写两侧都过 `store.NormalizeCardNo`**：去空白与 `:` `-` `_`、转大写。
  规则对称，所以不会把两张卡弄成一张；而它解决的差异是必然发生的 ——
  PC/SC 读卡器（ACR122U 等）吐十六进制 UID，管理员从终端上抄进管理界面时
  大小写几乎一定会有抄错的时候，各家的分隔符写法也不统一。
  🚫 归一**不**去前导零、不删数字（那会改变卡的身份）；也救不了
  「两台不同型号读卡器读出不同 UID」（Wiegand 号 vs 卡面号、字节序不同）——
  那是物理层差异，只能靠统一型号，换型号就要重新绑。
- **读卡器分两类，抓的是同一个卡号但路径完全不同**：PC/SC（ACR122U，走 pcscd，
  吐规范化 UID）与键盘模拟（HID，当键盘敲一串字符）。**键盘布局只影响后者** ——
  HID 读卡器按键码输出，机器布局不同会导致同一张卡读出不同字符串。
  推荐 PC/SC：没有这一整类问题。
- **绑卡用的读卡器插在哪儿都行** —— UID 是芯片固化的，同一张卡在任何
  一台 PC/SC 读卡器上读出来都一样。所以给管理员电脑配一台
  `printer_uri = ''` 的**仅用于绑卡终端**（新建终端时勾「仅用于绑卡」），
  绑卡就不必走到打印机那边去。这种终端在没有等待中的绑定窗口时回
  `bind_idle`（「请先在管理界面点开始等待刷卡」），而不是含糊的 `no_jobs`。
- **`/api/kiosk/release` 是本系统唯一的无会话写入口**，认证靠终端 token。
  必须限流 + 审计日志（无屏幕时日志是唯一排障线索），日志里**只打 token 前缀**。
- **提交页只列 `hasKiosk` 的打印机**（`/api/printers` 新增字段）。少了它，
  员工能选一台没装终端的机器，任务会进一个**取不出来也不报错的黑洞**。
  后端 `pullOrderCreateHandler` 有一道同样的校验兜底。
- **提交成功的文案必须是「已暂存，请到打印机刷卡取件」**。沿用「打印任务已提交」
  在取件模式下是**谎言**，员工会以为纸已经出来了。凡是提交路径上会出现这类
  文案的地方（`PrintView` 单/批量、`RecordsView` 重打）都要靠响应里的 `held` 分流。
- 「待取件」列表以 `print_orders` 为准（`GET /api/pull-orders`），
  🚫 **不要改成查 `print_jobs`** —— 后者受 `save_history` 影响，关掉时压根不写记录。

> 硬件（PC/SC 与键盘模拟两类读卡器的取舍、无屏幕的声码表）、`user_cards`
> 多卡升级路径，以及上面每条的完整论证见 [docs/pull-printing.md](docs/pull-printing.md)。
>
> 🧰 **去现场装机器的人照着 [docs/刷卡取件终端部署手册.md](docs/刷卡取件终端部署手册.md) 做**
> —— 从「买读卡器前先验卡型」到「开机自启 + 绑卡 + 验收」的完整步骤。
> 改终端程序（`scripts/kiosk/kiosk.py`）时记得同步那份手册。

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

## ⚠️ 错误信息必须指向根因

一次真实排查换来的教训：docx 上传失败只报 `failed to read pages`，让人反复怀疑自己的文件，而真实原因是服务器没装 LibreOffice。**错误信息指错方向，浪费的是真人的时间** —— 这是本仓库返工最多的一类问题。

### 后端

四条孪生流水线（`print_handlers.go` / `print_records_handlers.go` / `order_prepare.go` / 定时任务）是同一段逻辑的复制，过去各写各的硬编码英文兜底，只有一两条接了分层处理 —— 于是**用户拿到的提示取决于他点的是哪个入口**，而根因被同一句话吞掉。

🚫 不要再在这些地方写 `"failed to read pages"` / `"conversion failed"` / `"failed to save file"` 这类硬编码兜底，一律走 `convert_handler.go` 里的三个出口：

| 函数 | 用途 |
| --- | --- |
| `convertErrMsg(err)` | 转换失败 |
| `countPagesErrMsg(err)` | 数不出页数 |
| `saveErrMsg(err)` | 落盘失败（区分磁盘满 / 权限 / 只读） |

判据都是 `errors.Is(err, errBinaryNotInstalled)`；加新分支只改这一处，四条路径同时受益。`convert_errmsg_test.go` 钉着它们。同一段逻辑的孪生文件改动时要一起改。

- ⚠️ **外部命令要区分「没装」和「执行失败」**。`exec.Command(...).Output()` 的 `exec.ErrNotFound` 必须单独分支，否则「没装 lpinfo」会被报成「探测打印机失败」，让人去查打印机电源和防火墙。
- ⚠️ **不要把哨兵错误降级成普通字符串**（如 `fmt.Errorf("ghostscript not found")`）：那样 `errors.Is` 永远为假，上层所有分层处理失效。写成 `fmt.Errorf("ghostscript %w", errBinaryNotInstalled)`。
- ⚠️ **静默丢弃的回写要留日志**（`_ = appStore.WithTx(...)`）。纸已经出来了所以不该判失败，但记录会停在 `queued`，用户会以为没打出去而重复重打 —— 日志里必须查得到。

### 前端

- 🚫 **`if (!resp.ok) return` 是一种撒谎**。拉取失败时组件保持初值，而初值往往是个看起来合理的数字或空态：钱包显示 `¥0.00`（用户以为钱没了）、记录显示「暂无打印记录」（用户以为被清空）、价格条显示「尚未配置价目，请联系管理员配置」（管理员白翻一遍价目表）。**失败必须有可见的错误态。**
- ⚠️ **模板里用到的状态必须在 props 或脚本里真的存在**。踩过两次：`PrintPreview` 的 `pdfPreviewFailed` 被赋值但模板从没用过；`PriceSummary` 的 `error` 在模板里用了却从没声明 —— 于是那条分支永远渲染。
- ⚠️ **401 靠 `utils/api.js` 的全局兜底**（`setUnauthorizedHandler`，由 `App.vue` 注册）。原先每个调用点各传各的 `() => emit('logout')`，实测支付链路上 9 处全漏。新代码不必再传第三个参数；🚫 也不要传 `() => {}` 把它关掉。
- ⚠️ **兜底文案不能指错方向**。`catch { printerInfoError = '无法连接到打印机' }` 把「调接口失败」说成「打印机连不上」，把排查从服务/网络带到了硬件。

## 🎯 常见开发任务 / 调试 / 代码风格

> 新增 API、修改 DB、新增前端页面、新增文件类型、新增驱动的步骤模板，以及调试命令、Go/Vue 风格、Git 提交约定，见 [docs/conventions.md](docs/conventions.md)。
>
> 🚫 **commit message 禁止 `Co-Authored-By` 及任何 AI 署名行**，中文撰写。

**维护者**：涵曦（<im.hanxi@gmail.com>）
