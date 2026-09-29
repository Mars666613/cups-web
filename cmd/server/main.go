package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
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

	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = filepath.Join("data", "cups-web.db")
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		log.Fatal("failed to create data dir: ", err)
	}
	var err error
	appStore, err = store.Open(context.Background(), dbPath)
	if err != nil {
		log.Fatal("failed to open database: ", err)
	}
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

		json.NewEncoder(w).Encode(printers)
	}).Methods("GET")
	protected.HandleFunc("/print", printHandler).Methods("POST")
	protected.HandleFunc("/convert", convertHandler).Methods("POST")
	protected.HandleFunc("/compose", composeHandler).Methods("POST")
	protected.HandleFunc("/estimate", estimateHandler).Methods("POST")
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

	fmt.Println("listening on", addr)
	log.Fatal(srv.ListenAndServe())
}
