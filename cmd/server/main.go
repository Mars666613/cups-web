package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"cups-web/frontend"
	"cups-web/internal/auth"
	"cups-web/internal/ipp"
	"cups-web/internal/middleware"
	"cups-web/internal/server"
	"cups-web/internal/store"

	"github.com/gorilla/mux"
)

func main() {
	// ⚠️ 必须在读任何配置【之前】加载 .env。
	//
	// .env.example 让用户把密钥写进 .env，但那个文件只有 docker compose 会读
	// （它拿来做 ${VAR} 替换）；本地直接跑二进制时没人读，于是同一个文件在两种
	// 运行方式下行为不一致。实测踩过：生产密钥写进 .env 了，F5 调试用的却是
	// 别处的旧配置，请求打到沙箱网关 —— 界面上一切正常，真付款才失败。
	//
	// 优先级：真实环境变量 > .env（见 env_file.go）。
	loadDotEnv(".env")

	// 把支付渠道的实际落点打出来。见 alipayConfigSummary 的说明：
	// 「以为在用生产、实际打到沙箱」这种错配，界面上完全看不出来。
	log.Printf("[config] 支付宝: %s", alipayConfigSummary())
	// 微信这行现在恒为「未实现」。留着是因为它回答的是「我配了微信为什么
	// 支付方式里没有」—— 没有它，第一反应会去翻环境变量和 docker-compose。
	log.Printf("[config] 微信支付: %s", wechatConfigSummary())
	if paymentMockEnabled() {
		log.Printf("[config] ⚠️ PAYMENT_MOCK 已开启：模拟支付渠道可用，生产环境不应出现这一行")
	}

	// 命令行参数优先级高于环境变量。
	// 默认值留空以便区分"用户未指定"与"显式指定"，最终再回退到 :8080。
	listenFlag := flag.String("addr", "", "监听地址，如 :8080 或 0.0.0.0:8080 (优先级高于 LISTEN_ADDR 环境变量)")
	flag.Parse()

	addr := *listenFlag
	if addr == "" {
		addr = os.Getenv("LISTEN_ADDR")
	}
	if addr == "" {
		addr = ":8080"
	}

	// 数据库连接串。各店连同一台中心 PostgreSQL。
	//
	// ⚠️ 不要再往 DSN 上拼文件路径相关的处理：以前这里是 DB_PATH（SQLite 文件
	// 路径），还有一句 MkdirAll(filepath.Dir(dbPath))。如果照旧套到 DSN 上，
	// filepath.Dir("postgres://…") 会得到一个叫 `postgres:` 的目录。
	//
	// 🚫 也不要在 DSN 为空时回退到 SQLite —— 那套已经被彻底删除了。
	// 配置缺失时直接在启动时报出来，比跑起来之后连不上库好查得多。
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		log.Fatal("未配置 DATABASE_URL —— 本程序只支持 PostgreSQL，不再支持 SQLite。\n" +
			"  例：DATABASE_URL='postgres://cupsweb:密码@中心库地址:5432/cupsweb?sslmode=require'\n" +
			"  建表语句见 internal/store/schema.sql（启动时会自动执行，也可手动 psql -f）。")
	}
	var err error
	appStore, err = store.Open(context.Background(), dsn, dbMaxConns())
	if err != nil {
		log.Fatal("failed to open database: ", err)
	}
	log.Printf("[config] 数据库: %s  最大连接数 %d", redactDSN(dsn), dbMaxConns())
	if err := ensureDefaultAdmin(context.Background()); err != nil {
		log.Fatal("failed to ensure default admin: ", err)
	}

	uploadDir = os.Getenv("UPLOAD_DIR")
	if uploadDir == "" {
		uploadDir = "uploads"
	}
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		log.Fatal("failed to create uploads dir: ", err)
	}

	if err := auth.SetupSecureCookie(appStore.DB); err != nil {
		log.Fatal("failed to setup secure cookie: ", err)
	}

	r := mux.NewRouter()
	// 全局安全中间件：安全响应头 + 基于 Sec-Fetch-Site 的跨源 CSRF 防护
	// （Go 1.25 http.CrossOriginProtection，作为 double-submit 之外的纵深防御）。
	r.Use(middleware.SecurityHeaders)
	r.Use(middleware.CrossOriginProtection())

	api := r.PathPrefix("/api").Subrouter()
	api.HandleFunc("/login", LoginHandler).Methods("POST")
	api.HandleFunc("/logout", LogoutHandler).Methods("POST")
	api.HandleFunc("/csrf", CSRFHandler).Methods("GET")
	// session endpoint used by frontend to detect existing session on page load
	api.HandleFunc("/session", SessionHandler).Methods("GET")
	// 公开的版本接口：前端在登录页与主界面 footer 上展示，
	// 用户二进制覆盖升级后无需登录即可确认当前运行版本（Issue #26）。
	api.HandleFunc("/version", VersionHandler).Methods("GET")
	// 公开的前端可读设置：登录前也要用来渲染，比如管理员配置的自定义 CSS（Issue #57）。
	api.HandleFunc("/public-settings", publicSettingsHandler).Methods("GET")
	// 支付渠道的异步通知：无会话、无 CSRF —— 渠道服务器不可能带我们的 cookie。
	// 验签（Payment.VerifyCallback）是这里唯一的身份证明，
	// 所以绝不能因为「看起来像自家回调」就放行。
	api.HandleFunc("/payments/callback/{provider:[a-z]+}", paymentCallbackHandler).Methods("POST")

	// 支付宝授权登录。两条都【不挂会话守卫】—— 顾客此刻还没有会话，这正是
	// 登录的目的。跨源由全局 CrossOriginProtection 挡（它会拦跨源的 POST），
	// 回跳参数的真伪由 state 签名 cookie 保证（见 internal/auth）。
	//
	// ⚠️ 必须注册在下面的 protected 子路由【之前】：那个子路由的前缀是空串。
	api.HandleFunc("/auth/alipay/start", alipayAuthStartHandler).Methods("GET")
	api.HandleFunc("/auth/alipay/exchange", alipayAuthExchangeHandler).Methods("POST")
	api.HandleFunc("/auth/alipay/poll", alipayAuthPollHandler).Methods("GET")

	// 刷卡取件的终端口。同样【不挂会话守卫】—— 取件端是打印机旁一台没有
	// 屏幕、没有浏览器、没有 cookie 的树莓派，它拿的只是一张卡。
	// 身份靠 body 里的终端 token（见 admin_kiosk_handlers.go）。
	//
	// ⚠️ 同样必须注册在下面的 protected 子路由【之前】（理由同上）。
	api.HandleFunc("/kiosk/release", kioskReleaseHandler).Methods("POST")
	// 终端的自检口：只验 token，不碰任何状态（见 kioskPingHandler 的注释）。
	api.HandleFunc("/kiosk/ping", kioskPingHandler).Methods("POST")

	protected := api.PathPrefix("").Subrouter()
	protected.Use(middleware.RequireSession)
	protected.Use(middleware.ValidateCSRF)
	protected.HandleFunc("/me", MeHandler).Methods("GET")
	// 计费：用户可读的价目表、钱包、订单。
	// 下单口【只有一个】：单文件走 file、多文件走 files，与 /api/convert 同一套约定。
	// 单文件也分配批次号，支付/轮询/取消因此只有一条代码路径。
	protected.HandleFunc("/prices", priceViewHandler).Methods("GET")
	protected.HandleFunc("/wallet", walletHandler).Methods("GET")
	protected.HandleFunc("/orders", orderCreateHandler).Methods("POST")
	protected.HandleFunc("/orders", orderListHandler).Methods("GET")
	protected.HandleFunc("/orders/batch/{batchId}", orderBatchStatusHandler).Methods("GET")
	protected.HandleFunc("/orders/batch/{batchId}/pay", orderPayHandler).Methods("POST")
	protected.HandleFunc("/orders/batch/{batchId}/cancel", orderCancelHandler).Methods("POST")
	// 刷卡取件：提交（暂存，不打印）、查看自己的待取件、撤销。
	// 与计费互斥，两者都关时走 /api/print（见 pullModeEnabled）。
	protected.HandleFunc("/pull-orders", pullOrderCreateHandler).Methods("POST")
	protected.HandleFunc("/pull-orders", pullOrderListHandler).Methods("GET")
	protected.HandleFunc("/pull-orders/{id:[0-9]+}", pullOrderCancelHandler).Methods("DELETE")
	// 在线支付：充值发起、查单（"我已支付"）、mock 渠道的显式支付。
	// mock 的那条【必须】由环境变量 PAYMENT_MOCK=1 才能用，生产不会误开。
	protected.HandleFunc("/wallet/topup", walletTopupHandler).Methods("POST")
	// 余额原路退回：把充进来还没花掉的钱退回用户的支付宝。
	// 可退额度见 wallet_refund.go —— 不是看余额，而是看有没有对应的渠道交易。
	protected.HandleFunc("/wallet/refund", walletRefundHandler).Methods("POST")
	protected.HandleFunc("/payments/{outTradeNo}/sync", paymentSyncHandler).Methods("POST")
	protected.HandleFunc("/payments/mock/{outTradeNo}/pay", mockPayHandler).Methods("POST")
	protected.HandleFunc("/printers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		cupsHost := os.Getenv("CUPS_HOST")
		if cupsHost == "" {
			cupsHost = "localhost"
		}

		printers, err := ipp.ListPrinters(cupsHost)
		if err != nil {
			log.Printf("[printers] list failed: %v", err)
			http.Error(w, "failed to list printers", http.StatusInternalServerError)
			return
		}

		// HasKiosk 只在刷卡取件模式下被前端用到：那个模式里提交页只列
		// 【配有取件终端】的打印机 —— 员工选到没有终端的那台，任务提交后就
		// 再也取不出来，而且没有任何报错。
		//
		// ⚠️ 查不到时全部下发 false（宁可界面少列），不是全部 true：
		// 一次读库失败不该让员工把任务投进黑洞。
		// 后端 pull_handlers.go 里还有一道同样的校验兜底。
		kioskURIs := map[string]bool{}
		if err := appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
			var e error
			kioskURIs, e = store.PrinterURIsWithKiosk(r.Context(), tx)
			return e
		}); err != nil {
			log.Printf("[printers] 查询取件终端失败（hasKiosk 全按 false）: %v", err)
			kioskURIs = map[string]bool{}
		}

		type printerWithKiosk struct {
			ipp.Printer
			HasKiosk bool `json:"hasKiosk"`
		}
		out := make([]printerWithKiosk, 0, len(printers))
		for _, p := range printers {
			out = append(out, printerWithKiosk{Printer: p, HasKiosk: kioskURIs[p.URI]})
		}
		json.NewEncoder(w).Encode(out)
	}).Methods("GET")
	protected.HandleFunc("/print", printHandler).Methods("POST")
	protected.HandleFunc("/convert", convertHandler).Methods("POST")
	protected.HandleFunc("/compose", composeHandler).Methods("POST")
	protected.HandleFunc("/estimate", estimateHandler).Methods("POST")
	// 远程复印（issue：复印功能）：状态探测 + 提交复印。
	// 计费开启时提交返回的是待支付订单批次（与 /api/orders 同形状），
	// 免费与取件模式下直接扫描送印。
	protected.HandleFunc("/copy/status", copyStatusHandler).Methods("GET")
	protected.HandleFunc("/copy", copyHandler).Methods("POST")
	protected.HandleFunc("/print-records", printRecordsHandler).Methods("GET")
	protected.HandleFunc("/print-records/{id:[0-9]+}/file", printRecordFileHandler).Methods("GET")
	protected.HandleFunc("/print-records/{id:[0-9]+}/reprint", reprintHandler).Methods("POST")
	protected.HandleFunc("/printer-info", printerInfoHandler).Methods("GET")
	// 定时打印（issue #28）：用户上传文件 + 打印参数 + 触发策略，由后台调度器到点触发。
	protected.HandleFunc("/scheduled-prints", scheduledListHandler).Methods("GET")
	protected.HandleFunc("/scheduled-prints", scheduledCreateHandler).Methods("POST")
	protected.HandleFunc("/scheduled-prints/{id:[0-9]+}", scheduledUpdateHandler).Methods("PUT")
	protected.HandleFunc("/scheduled-prints/{id:[0-9]+}", scheduledDeleteHandler).Methods("DELETE")
	protected.HandleFunc("/scheduled-prints/{id:[0-9]+}/run", scheduledRunNowHandler).Methods("POST")
	// CUPS 任务列表 / 取消:任何登录用户可用,权限由 CUPS 按 owner 校验(issue #60)。
	protected.HandleFunc("/cups-jobs", cupsJobsHandler).Methods("GET")
	protected.HandleFunc("/cups-jobs/cancel", cupsCancelJobHandler).Methods("POST")

	admin := api.PathPrefix("/admin").Subrouter()
	admin.Use(middleware.RequireSession)
	admin.Use(middleware.RequireAdmin)
	admin.Use(middleware.ValidateCSRF)
	admin.HandleFunc("/users", adminListUsersHandler).Methods("GET")
	admin.HandleFunc("/users", adminCreateUserHandler).Methods("POST")
	admin.HandleFunc("/users/{id:[0-9]+}", adminUpdateUserHandler).Methods("PUT")
	admin.HandleFunc("/users/{id:[0-9]+}", adminDeleteUserHandler).Methods("DELETE")
	// 手工调额 = 线下收款入账 / 纠正误扣。会写审计流水，不是直接改余额列。
	admin.HandleFunc("/users/{id:[0-9]+}/balance", adminAdjustBalanceHandler).Methods("POST")
	// 工牌绑定。必须是【刷卡】绑，不是手输 —— 同一张卡换个读卡器读出来
	// 就不是同一个字符串，手输的号码和读卡器吐出来的对不上。
	admin.HandleFunc("/users/{id:[0-9]+}/card", adminBindCardHandler).Methods("POST")
	admin.HandleFunc("/users/{id:[0-9]+}/card", adminUnbindCardHandler).Methods("DELETE")
	// 「点绑卡 → 到终端刷卡」：开一个短命窗口，窗口期内的那次刷卡会被绑给
	// 指定用户（见 kiosk_handlers.go 的 tryBindCard）。
	// 浏览器轮询 GET 拿结果 —— 读卡器在终端上，浏览器够不着它。
	admin.HandleFunc("/users/{id:[0-9]+}/card-bind", adminCreateCardBindSessionHandler).Methods("POST")
	admin.HandleFunc("/card-bind-sessions/{id:[0-9]+}", adminGetCardBindSessionHandler).Methods("GET")
	admin.HandleFunc("/card-bind-sessions/{id:[0-9]+}", adminCancelCardBindSessionHandler).Methods("DELETE")
	// 取件终端（打印机旁那台树莓派）的管理。token 由这里生成，
	// 管理员填进 Pi 的配置文件。
	admin.HandleFunc("/kiosk-terminals", adminListKioskTerminalsHandler).Methods("GET")
	admin.HandleFunc("/kiosk-terminals", adminCreateKioskTerminalHandler).Methods("POST")
	admin.HandleFunc("/kiosk-terminals/{token}", adminUpdateKioskTerminalHandler).Methods("PUT")
	admin.HandleFunc("/kiosk-terminals/{token}", adminDeleteKioskTerminalHandler).Methods("DELETE")
	admin.HandleFunc("/print-records", adminPrintRecordsHandler).Methods("GET")
	admin.HandleFunc("/settings", adminGetSettingsHandler).Methods("GET")
	admin.HandleFunc("/settings", adminUpdateSettingsHandler).Methods("PUT")
	// 计费价格配置。开关与价目放在一起而不是塞进 /settings：
	// 两者必须同时生效，从同一个入口改才不容易出现「开了计费但没价目」的中间态。
	admin.HandleFunc("/prices", adminGetPricesHandler).Methods("GET")
	admin.HandleFunc("/prices", adminUpdatePricesHandler).Methods("PUT")
	// 计费对账：订单与余额流水查询、手工退款。
	admin.HandleFunc("/orders", adminListOrdersHandler).Methods("GET")
	admin.HandleFunc("/orders/{id:[0-9]+}/refund", adminRefundOrderHandler).Methods("POST")
	admin.HandleFunc("/wallet-transactions", adminListWalletTransactionsHandler).Methods("GET")
	// 管理操作审计：只读。写入没有 HTTP 入口 —— 它只能由业务改动顺带产生，
	// 能单独调用的审计等于可以伪造的审计。
	admin.HandleFunc("/audit", adminListAuditHandler).Methods("GET")
	// 设备异常告警：列表只读；resolve 是「已知悉，别再提醒这一类」，
	// test 用当前配置发一条测试消息（见 admin_device_alert_handlers.go）。
	admin.HandleFunc("/device-events", adminListDeviceEventsHandler).Methods("GET")
	admin.HandleFunc("/device-events/{id:[0-9]+}/resolve", adminResolveDeviceEventHandler).Methods("POST")
	admin.HandleFunc("/device-events/test", adminTestAlertHandler).Methods("POST")
	admin.HandleFunc("/cleanup", adminCleanupHandler).Methods("POST")
	admin.HandleFunc("/drivers", adminListDriversHandler).Methods("GET")
	admin.HandleFunc("/drivers/install", adminInstallDriverHandler).Methods("POST")
	admin.HandleFunc("/drivers/remove", adminRemoveDriverHandler).Methods("POST")
	admin.HandleFunc("/drivers/detect", adminDetectPrintersHandler).Methods("GET")
	admin.HandleFunc("/drivers/ppds", adminListPPDCandidatesHandler).Methods("GET")
	admin.HandleFunc("/drivers/upload", adminUploadDriverHandler).Methods("POST")
	admin.HandleFunc("/drivers/setup", adminSetupPrinterHandler).Methods("POST")
	// 驱动安装/卸载/一键设置改为异步任务（编译型驱动几分钟，会被全局
	// WriteTimeout=120s 掐断），这里提供任务状态与增量日志的轮询入口。
	// jobId 是 randomToken() 生成的不透明大写 base32 串，故约束放宽到字母数字。
	admin.HandleFunc("/drivers/jobs/{id:[A-Za-z0-9]+}", adminDriverJobHandler).Methods("GET")

	// Static files (embedded) - register after API routes so /api/* is matched first
	serverFS := server.NewEmbeddedServer(frontend.FS)
	r.PathPrefix("/").Handler(serverFS)

	// 超时放宽：打印 / 转换接口需要在服务端处理大图（下采样 + gofpdf 合成）+
	// 回传 PDF 到移动端，15s 在 4G 上传 10M+ 照片时很容易超时（Issue #22）。
	// 为简化起见这里统一放到 2 分钟，业务上限比 Ghostscript 标准化管线的超时还要长一些。
	srv := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  120 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	startMaintenance(appStore, uploadDir)
	startScheduler(appStore, uploadDir)
	// 订单巡检：补偿送印（已支付但没打成）、回收卡死的订单、
	// 释放超时未支付的批次并回收其文件。
	startOrderWorker(appStore, uploadDir)
	// 设备异常巡检：缺纸 / 卡纸 / 门没关 / 耗材耗尽 / 设备不可达。
	//
	// 它防的是那条最贵的故障链：半夜卡纸 → 之后每一单都送印失败 → 自动退款 →
	// 第二天才发现。默认关闭（alert_enabled=0），配置见系统设置。
	startDeviceAlertWorker(appStore)

	fmt.Println("listening on", addr)
	log.Fatal(srv.ListenAndServe())
}
