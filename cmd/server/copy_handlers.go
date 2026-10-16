package main

// 远程复印：扫描（eSCL）+ 打印（IPP/CUPS）的组合入口。
//
// 与 /api/print 的关系：
//   - 免费模式：扫描 → 直接送印（与 /api/print 的 PDF 分支同构）；
//   - 计费模式：扫描 → 走 createOneOrder 落成待支付订单（纸不出，等付款），
//     支付由现有订单链路接管，响应形状与 /api/orders 完全一致；
//   - 刷卡取件模式：复印【不走暂存】—— 取件模式管的是「打印」，而复印的
//     用户此刻就站在打印机旁边等纸，再要求刷卡是纯粹的重复劳动（需求原话：
//     员工用手机点复印，不需要再刷卡）。因此与免费模式同一条直接送印路径。
//
// 复印与打印的本质区别是「文件不是上传的，是扫描仪现场采的」：
// 扫描发生在请求内，页数在扣款前就完全确定，不存在「预估页数与实付不符」。
//
// 「原件到底放好了没有」有两道闸，都落在这个文件里：
//   - 下任务前：进稿器显式上报空 → 409 adf_empty（adfPreflight）。
//     它只是一条【提示】：设备可能误报（少数机型常年报 Empty），用户确认
//     后即可越过。
//   - 拿到件后：整份扫描件是空白 → 409 blank_scan（pdfLooksBlank）。
//     这一道才是权威判据 —— 空进稿器与空玻璃板都会交回一张全白页，设备
//     既不报错也不终止任务（见 pdf_blank.go 的实测记录）。

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"cups-web/internal/auth"
	"cups-web/internal/escl"
	"cups-web/internal/ipp"
	"cups-web/internal/store"
)

// scanOverallTimeout 是一次扫描的硬上限。服务端 WriteTimeout 是 120s，
// 扫描 + 落单必须整体压在它之下，否则响应还没写出去连接就被掐断，
// 用户看到的是浏览器超时而服务端还在傻等。
// 玻璃板单页 5-20s、ADF 双面十几页约 60-90s，110s 是够用的经验值。
const scanOverallTimeout = 110 * time.Second

// 放稿方式在 API 上的取值（小写）。与 escl 包里写进 XML 的 InputSource
// 枚举（Platen / ADF）刻意分开：前者是给前端用的接口约定，后者是协议值，
// 混用会让「接口里的大小写」变成协议实现细节的一部分。
const (
	copyInputPlaten = "platen"
	copyInputADF    = "adf"
)

// errAdfEmpty 表示「用户选了进稿器，但设备明确说里面没有原件」。
// 单独成一个哨兵值，是为了让 handler 能把它映射成带 code 的 409，
// 前端据此给出「仍然继续」这种可当场纠正的提示。
var errAdfEmpty = errors.New("adf empty")

// writeCopyError 在错误体里多带一个机器可读的 code。复印页要按错误种类
// 决定是弹提示、还是让用户确认后重试，光靠中文文案去匹配太脆。
func writeCopyError(w http.ResponseWriter, status int, code, msg string) {
	writeJSONStatus(w, status, map[string]string{"error": msg, "code": code})
}

// adfPreflight 决定要不要在创建扫描任务【之前】就把这一单拦下。
//
// 只在设备明确上报 AdfState=ScannerAdfEmpty 时才拦：往空进稿器下 ADF
// 任务，设备必然走一遍失败流程（多等十几秒、给一条不好懂的报错）。提前
// 拦下能把「纸没动但等了半天」换成点下去就有的明确提示。
//
// 设备【不】上报 AdfState 时一律放行 —— 有些机型常年报 Empty 或干脆不报，
// 拿它当依据会把正常复印误伤掉。force 是用户的确认（前端在提示后带上），
// 到了那一步仍要让用户能按自己的判断继续。
func adfPreflight(resolvedSource string, force bool, st *escl.Status) error {
	if force || resolvedSource != escl.SourceADF || st == nil || !st.AdfEmpty() {
		return nil
	}
	return errAdfEmpty
}

// copyStatusResponse 回给前端复印页的状态。
type copyStatusResponse struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Model     string `json:"model,omitempty"`
	State     string `json:"state,omitempty"`
	Idle      bool   `json:"idle"`
	HasPlaten bool   `json:"hasPlaten"`
	HasAdf    bool   `json:"hasAdf"`
	AdfDuplex bool   `json:"adfDuplex"`
	Color     bool   `json:"color"`
	// AdfLoaded 表示进稿器里检测到了原件。前端拿它把「放稿」的默认值
	// 定到原件实际所在的位置 —— 用户把纸放进进稿器却按玻璃板扫，会出来
	// 一张空白纸而且照样打印，这比报错更让人费解。
	AdfLoaded bool `json:"adfLoaded"`
	// AdfState 是设备上报的原始取值（ScannerAdfEmpty / Loaded / …）。
	// 前端不显示，排障时对着它能一眼分清「设备说没纸」与「设备没说」。
	AdfState string `json:"adfState,omitempty"`
	// ScanTarget 是实际探测的 eSCL 地址（如 http://192.168.1.100/eSCL）。
	// 前端不显示，只用于出问题时对着日志/接口响应排障 —— 队列地址与设备
	// 地址搞混是这一块最容易踩的坑。
	ScanTarget string `json:"scanTarget,omitempty"`
}

// copyStatusHandler 探测所选打印机的扫描能力。
// 每次进复印页 / 换打印机调一次，不做缓存 —— 设备就一台，探测很便宜，
// 缓存反而会让「管理员刚打开机器的扫描服务」迟到一轮才可见。
// 会话由 protected 路由组的中间件保证，这里无需再取。
func copyStatusHandler(w http.ResponseWriter, r *http.Request) {
	printer := r.URL.Query().Get("printer")
	resp := copyStatusResponse{}
	if printer == "" {
		resp.Reason = "未选择打印机"
		writeJSON(w, resp)
		return
	}
	// 前端下发的 printer 是 CUPS 队列地址，扫描要的是它背后的设备地址，
	// 交给 newScannerClient 解析（见 copy_scan_target.go）。
	client, target := newScannerClient(r.Context(), printer)
	if client == nil {
		resp.Reason = "这台打印机的连接方式无法使用远程复印（仅支持网络一体机）"
		resp.ScanTarget = target
		writeJSON(w, resp)
		return
	}
	resp.ScanTarget = client.BaseURL

	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	caps, capsErr := client.Capabilities(ctx)
	if capsErr != nil {
		log.Printf("[copy] 探测扫描能力失败 %s（eSCL %s）: %v", printer, client.BaseURL, capsErr)
		resp.Reason = "这台打印机不支持远程复印（未开放 eSCL/AirScan 网络扫描）"
		writeJSON(w, resp)
		return
	}
	st, stErr := client.Status(ctx)
	if stErr != nil {
		log.Printf("[copy] 查询扫描状态失败 %s: %v", printer, stErr)
		// 能查到能力却查不到状态：设备多半还是可用的，不因状态口失败而整体判死。
		st = &escl.Status{State: "Idle"}
	}

	resp.Available = true
	resp.Model = caps.MakeAndModel
	resp.State = st.State
	resp.Idle = st.Idle()
	resp.HasPlaten = caps.HasPlaten
	resp.HasAdf = caps.HasAdf
	resp.AdfDuplex = caps.AdfDuplex
	resp.Color = caps.Color
	resp.AdfLoaded = st.AdfLoaded()
	resp.AdfState = st.AdfState
	writeJSON(w, resp)
}

// copyRequest 是 POST /api/copy 的 JSON body。
type copyRequest struct {
	Printer string `json:"printer"`
	// Color 为真走彩色扫描（RGB24），否则灰度。
	Color bool `json:"color"`
	// InputSource 指定原件放在哪："platen"（玻璃板，一次一页）或
	// "adf"（进稿器，可多页）。必须由用户明确选择 —— 猜错代价很大：
	// 猜成玻璃板而原件在进稿器里，扫出来是空白页（还会照样打印出来）；
	// 猜成进稿器而原件在玻璃板上，任务会被设备直接终止。
	// 留空时退回旧行为（有进稿器就用进稿器）以兼容旧前端。
	InputSource string `json:"inputSource"`
	// AdfDuplex 为真表示原件是双面的（仅进稿器有意义）。
	AdfDuplex bool `json:"adfDuplex"`
	// DuplexOut 为真表示输出双面。
	DuplexOut bool `json:"duplexOut"`
	// Copies 复印份数，1-99。
	Copies int `json:"copies"`
	// PaperSize 计价与 IPP 用的纸张，默认 A4。
	PaperSize string `json:"paperSize"`
	// ForceInput 由前端在「设备说进稿器里没纸、但用户确认放好了」之后
	// 带上来，用来越过下面那道预检。设备偶尔会报错装载状态，用户明知道
	// 自己放了纸却被拦在门外，比放行更糟。
	ForceInput bool `json:"forceInput"`
	// ForceBlank 由前端在「扫描结果整页空白、但用户确认就是要印这张白纸」
	// 之后带上来。与 ForceInput 分开：两者对应的是界面上两条不同的提示，
	// 确认了「纸放好了」不等于确认了「我就要一张白纸」，各自只越过自己
	// 那一道（见 copyHandler 里的两道闸）。
	ForceBlank bool `json:"forceBlank"`
}

func (req *copyRequest) normalize() error {
	if req.Printer == "" {
		return fmt.Errorf("missing printer")
	}
	if req.Copies < 1 {
		req.Copies = 1
	}
	if req.Copies > 99 {
		return fmt.Errorf("复印份数最多 99")
	}
	if req.PaperSize == "" {
		req.PaperSize = "A4"
	}
	return nil
}

// copyHandler 提交一次复印。
func copyHandler(w http.ResponseWriter, r *http.Request) {
	var req copyRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := req.normalize(); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	sess, err := auth.GetSession(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	billing := billingEnabled(r.Context())
	// 计费模式的订单必须挂在真实账号上：访客余额是公摊钱包，
	// 与 orderCreateHandler 同一道防线。
	if billing && sess.Username == guestUsername {
		writeJSONError(w, http.StatusForbidden, "访客模式不支持计费复印")
		return
	}

	// ── 扫描 ──────────────────────────────────────────────
	pdfBytes, err := scanOriginal(r.Context(), req)
	if err != nil {
		log.Printf("[copy] 扫描失败 %s: %v", req.Printer, err)
		switch {
		case errors.Is(err, escl.ErrScannerBusy):
			writeCopyError(w, http.StatusConflict, "scanner_busy", err.Error())
		case errors.Is(err, escl.ErrUnsupportedFormat):
			writeJSONError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, errAdfEmpty):
			// 409 而非 400：请求本身没错，是现场还没准备好，改一下就能重试。
			writeCopyError(w, http.StatusConflict, "adf_empty",
				"进稿器里没有检测到原件。请把原件放进进稿器（正面朝上、两侧导板贴住纸边），或改选「玻璃板」后重新复印。")
		default:
			writeJSONError(w, http.StatusBadGateway, err.Error())
		}
		return
	}

	filename := "复印_" + time.Now().Format("20060102-150405") + ".pdf"

	// 扫描「成功」不等于扫到了东西：空进稿器与空玻璃板都会交回一张全白的
	// 页面，设备既不报错也不终止任务（详见 pdf_blank.go 开头的实测）。落单
	// 或送印之前先看一眼结果，否则用户会为一张白纸付钱、等一张白纸出纸。
	//
	// 免费与刷卡取件模式同样要拦：那两种模式虽然不涉及钱，但白出纸、白等
	// 一轮一样是坏体验，而且「没放原件也能印」本身就说明流程漏了。
	if !req.ForceBlank {
		if blank, known := pdfLooksBlank(bytes.NewReader(pdfBytes)); known && blank {
			log.Printf("[copy] 扫描件判定为空白，未落单：%s 纸张=%s 放稿=%s 字节=%d",
				req.Printer, req.PaperSize, req.InputSource, len(pdfBytes))
			writeCopyError(w, http.StatusConflict, "blank_scan",
				"这次扫描得到的是一张空白页：原件多半没有放好（放稿方式与原件实际位置不一致、盖板没压住、或纸没进进稿器）。请检查原件后重新复印。")
			return
		}
	}

	if billing {
		createCopyOrder(w, r, sess, req, pdfBytes, filename)
		return
	}
	directCopyPrint(w, r, sess, req, pdfBytes, filename)
}

// resolveScanInput 决定这次扫描的输入源，并校验设备确实具备该能力。
//
// 抽成纯函数是为了能被用例逐格覆盖 —— 这里判错一次，用户要么拿到一张
// 空白纸（还以为印出来了），要么收到一条与操作对不上的设备报错。
//
// requested 是 API 侧的小写取值（platen / adf），大小写不敏感；
// 为空时兼容未升级的前端：它的「原件 单面/双面」表达的本来就是进稿器
// 语义，所以有进稿器就按进稿器走。
func resolveScanInput(requested string, adfDuplex bool, caps *escl.Capabilities) (string, bool, error) {
	source := strings.ToLower(strings.TrimSpace(requested))
	if source == "" {
		if caps.HasAdf {
			source = copyInputADF
		} else {
			source = copyInputPlaten
		}
	}

	switch source {
	case copyInputPlaten:
		if !caps.HasPlaten {
			return "", false, fmt.Errorf("这台打印机没有玻璃板，请把原件放进进稿器")
		}
		if adfDuplex {
			return "", false, fmt.Errorf("玻璃板一次只能放一页，双面原件请改用进稿器")
		}
		// 玻璃板在物理上不存在双面，Duplex 恒为假。
		return escl.SourcePlaten, false, nil

	case copyInputADF:
		if !caps.HasAdf {
			return "", false, fmt.Errorf("这台打印机没有进稿器，请把原件放在玻璃板上")
		}
		// ADF 双面要设备真的支持，否则悄悄降级成单面 —— 复印出来缺半边
		// 内容比明确报错糟糕得多。
		if adfDuplex && !caps.AdfDuplex {
			return "", false, fmt.Errorf("这台打印机不支持双面扫描，请选择单面原件或翻面分两次复印")
		}
		return escl.SourceADF, adfDuplex, nil

	default:
		return "", false, fmt.Errorf("不认识的放稿方式 %q", requested)
	}
}

// scanOriginal 执行扫描，返回扫描件字节（多页 PDF）。
func scanOriginal(ctx context.Context, req copyRequest) ([]byte, error) {
	// 与状态探测同一条解析路径：队列地址 → 设备地址 → eSCL 端点。
	client, target := newScannerClient(ctx, req.Printer)
	if client == nil {
		return nil, fmt.Errorf("这台打印机的连接方式无法使用远程复印")
	}
	_ = target

	scanCtx, cancel := context.WithTimeout(ctx, scanOverallTimeout)
	defer cancel()

	caps, err := client.Capabilities(scanCtx)
	if err != nil {
		return nil, fmt.Errorf("无法连接打印机的扫描服务（%s），请确认打印机在线并已开启 eSCL/AirScan", client.BaseURL)
	}

	// 放稿方式以用户明确选择为准（见 copyRequest.InputSource 的说明）。
	source, duplex, err := resolveScanInput(req.InputSource, req.AdfDuplex, caps)
	if err != nil {
		return nil, err
	}

	colorMode := escl.ColorModeGray8
	if req.Color {
		if !caps.Color {
			return nil, fmt.Errorf("这台打印机不支持彩色扫描")
		}
		colorMode = escl.ColorModeRGB24
	}

	// 进稿器的空/满设备能当场告知，下任务前先问一句：往空的进稿器下 ADF
	// 任务，唯一的结果是白等一场（见 adfPreflight）。查不到状态就放行，
	// 由设备自己去判。
	if source == escl.SourceADF && !req.ForceInput {
		st, stErr := client.Status(scanCtx)
		if stErr != nil {
			log.Printf("[copy] 查询进稿器状态失败（不拦，交给设备判）%s: %v", req.Printer, stErr)
		} else if err := adfPreflight(source, req.ForceInput, st); err != nil {
			return nil, err
		}
	}

	// 扫描区域按纸张算，不用设备最大幅面 —— A4 原件被采成 A3 页面的话，
	// 打印端会为了适应 A4 把整页转 90° 缩小，复印出来是横着的。
	regionW, regionH := scanRegion(req.PaperSize, source, caps)
	log.Printf("[copy] 扫描 %s：纸张=%s 放稿=%s 区域=%d×%d(1/300in，约 %d×%dmm)",
		client.BaseURL, req.PaperSize, source, regionW, regionH,
		regionW*254/3000, regionH*254/3000)

	settings := escl.ScanSettings{
		InputSource: source,
		ColorMode:   colorMode,
		Duplex:      duplex,
		Format:      "application/pdf",
		XResolution: 300,
		YResolution: 300,
		// 区域 = 原件在扫描几何下的大小，页面才会与纸张同形（见 copy_scan_region.go）。
		Width:  regionW,
		Height: regionH,
		// 进稿器吃到空；玻璃板由 ScanSettings 强制成 1 页。
		ImagesToTransfer: 0,
	}
	return client.Scan(scanCtx, settings)
}

// createCopyOrder 计费模式：扫描件落成待支付订单。
// 响应形状与 /api/orders 一致，前端直接复用支付弹窗与轮询。
func createCopyOrder(w http.ResponseWriter, r *http.Request, sess auth.Session, req copyRequest, pdfBytes []byte, filename string) {
	maxPages, _ := getPrintLimits(r.Context())
	rules, fallback, err := loadPricingSnapshot(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load prices")
		return
	}

	form := orderForm{
		Printer:  req.Printer,
		IsColor:  req.Color,
		IsDuplex: req.DuplexOut,
		Copies:   req.Copies,
		// 与 /api/print 的表单缺省一致：不写缩放/页码等高级字段，
		// IPP 侧 auto 缩放会把扫描件按所选纸张自适应。
		PaperSize: req.PaperSize,
		// ⚠️ 纸张类型必须给值，不能留空。价目表的维度是
		// (是否彩色, 纸张尺寸, 纸张类型)，LookupUnitCents 按三个维度全等
		// 匹配：留空即 ""，与管理员配置的 "plain" 对不上，于是每一笔复印
		// 订单都会掉到兜底价（默认 0 分）—— 金额 0 元的订单连支付单都建不
		// 出来（createPayment 拒绝 AmountCents <= 0），用户点付款只会拿到
		// 一句「没有待支付订单」。
		//
		// 取 "plain" 而不是新造一个维度：这是打印页表单（PrintOptions.vue）
		// 的默认值，管理端新建价目行（AdminBillingView.vue）的默认值也是它，
		// 复印页没有纸张类型选择项，跟随同一个默认值才能命中同一行价目，
		// 也才能与打印的计费口径一致。
		PaperType: "plain",
	}

	expires := time.Now().UTC().Add(orderTTL(r.Context())).Format(time.RFC3339)
	batchID := newBatchID()

	order, amount, matched, perr := createOneOrder(r.Context(), sess.UserID, batchID,
		orderOrigin{Source: store.OrderSourceCopy}, form,
		io.NopCloser(bytes.NewReader(pdfBytes)), filename,
		maxPages, rules, fallback, expires)
	if perr != nil {
		writeClientError(w, perr)
		return
	}

	resp := orderBatchResponse{
		BatchID:          batchID,
		OrderIDs:         []int64{order.ID},
		Orders:           []orderResponse{mapOrder(order, matched)},
		TotalAmountCents: amount,
		ExpiresAt:        expires,
		Failed:           []orderFailedItem{},
	}
	if balance, berr := readUserBalance(r.Context(), sess.UserID); berr == nil {
		resp.BalanceCents = balance
	}
	writeJSON(w, resp)
}

// directCopyPrint 免费 / 取件模式：扫描件直接送印并落打印记录。
// 与 printHandler 的 PDF 分支同构（复印产物天然是 PDF，不需要转换管线）。
func directCopyPrint(w http.ResponseWriter, r *http.Request, sess auth.Session, req copyRequest, pdfBytes []byte, filename string) {
	storedRel, storedAbs, err := saveUploadedFile(bytes.NewReader(pdfBytes), filename, uploadDir)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, saveErrMsg(err))
		return
	}

	pages, cerr := countPDFPages(storedAbs)
	if cerr != nil {
		log.Printf("[copy] countPDFPages failed: %v", cerr)
		pages = 1
	}
	if pages < 1 {
		pages = 1
	}
	if maxPages, _ := getPrintLimits(r.Context()); maxPages > 0 && int64(pages) > maxPages {
		_ = remove(storedAbs)
		writeJSONError(w, http.StatusUnprocessableEntity, "复印页数超出管理员设置的上限")
		return
	}

	var recordID int64
	if err := appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
		user, err := store.GetUserByID(r.Context(), tx, sess.UserID)
		if err != nil {
			return err
		}
		rec := store.PrintRecord{
			UserID:     user.ID,
			PrinterURI: req.Printer,
			Filename:   filename,
			StoredPath: storedRel,
			Pages:      pages,
			Status:     "queued",
			IsDuplex:   req.DuplexOut,
			IsColor:    req.Color,
			Copies:     req.Copies,
			PaperSize:  req.PaperSize,
			CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		}
		id, err := store.InsertPrintRecord(r.Context(), tx, &rec)
		if err != nil {
			return err
		}
		recordID = id
		return nil
	}); err != nil {
		_ = remove(storedAbs)
		writeJSONError(w, http.StatusInternalServerError, "failed to create print record")
		return
	}

	f, err := os.Open(storedAbs)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to open file")
		return
	}
	defer f.Close()

	job, err := ipp.SendPrintJob(req.Printer, f, "application/pdf", sess.Username, filename, ipp.PrintJobOptions{
		IsDuplex:  req.DuplexOut,
		IsColor:   req.Color,
		Copies:    req.Copies,
		PaperSize: req.PaperSize,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "print error: "+err.Error())
		return
	}
	if recordID > 0 {
		if err := appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
			return store.UpdatePrintStatus(r.Context(), tx, recordID, "printed", job)
		}); err != nil {
			log.Printf("[copy] 回写打印记录状态失败（记录会停在 queued）: record=%d err=%v", recordID, err)
		}
	}

	writeJSON(w, printResp{
		JobID:    job,
		OK:       true,
		Pages:    pages,
		IsDuplex: req.DuplexOut,
		IsColor:  req.Color,
		Copies:   req.Copies,
	})
}

// remove 是 os.Remove 的局部别名，仅让上方错误路径一眼可读。
func remove(path string) error { return os.Remove(path) }
