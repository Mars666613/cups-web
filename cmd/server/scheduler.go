package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cups-web/internal/ipp"
	"cups-web/internal/pricing"
	"cups-web/internal/store"
)

// schedulerTickInterval 是调度器扫描到期任务的心跳间隔。
// 30s 已足够精细：定时打印属于分钟级业务，扫描频次再高只会加重 SQLite 压力。
const schedulerTickInterval = 30 * time.Second

// scheduleTimeLayout 是 HH:MM 的解析格式，与前端表单一致。
const scheduleTimeLayout = "15:04"

// startScheduler 启动后台调度器 goroutine。与 startMaintenance 并列，进程
// 生命周期内常驻。执行串行以避免多个任务并发抢 CUPS 导致队列错乱。
func startScheduler(s *store.Store, uploads string) {
	go func() {
		// 首次立即扫一次：进程重启后马上处理任何过期任务（会被判为 skipped）。
		schedulerTick(s, uploads)
		ticker := time.NewTicker(schedulerTickInterval)
		defer ticker.Stop()
		for range ticker.C {
			schedulerTick(s, uploads)
		}
	}()
}

func schedulerTick(s *store.Store, uploads string) {
	nowLocal := time.Now()
	nowUTC := nowLocal.UTC().Format(time.RFC3339)

	var due []store.ScheduledPrint
	err := s.WithTx(context.Background(), true, func(tx *sql.Tx) error {
		list, err := store.ListDueScheduledPrints(context.Background(), tx, nowUTC)
		if err != nil {
			return err
		}
		due = list
		return nil
	})
	if err != nil {
		log.Printf("[scheduler] list due failed: %v", err)
		return
	}

	for _, task := range due {
		runScheduledTask(s, uploads, task, nowLocal)
	}
}

// runScheduledTask 执行一条到期任务：先算下一次 next_run_at 并落库（避免失败反复重跑），
// 再实际发送到 IPP。过期任务（比进程启动时早于 now 的执行点）按 skipped 处理，不补跑。
func runScheduledTask(s *store.Store, uploads string, task store.ScheduledPrint, now time.Time) {
	// 判定是否为"错过窗口"：任务 next_run_at 早于 now - 2 分钟视为宕机期间错过。
	// 2 分钟窗口足够容忍 30s 扫描心跳的抖动，同时避免"下班后自动打印上周报表"。
	nextRunTime, parseErr := time.Parse(time.RFC3339, task.NextRunAt)
	missed := parseErr == nil && nextRunTime.Before(now.Add(-2*time.Minute))

	runAtUTC := now.UTC().Format(time.RFC3339)

	if missed {
		nextRunAt, stillEnabled := advanceNextRun(task, now)
		if err := persistScheduledResult(s, task.ID, runAtUTC, "skipped", "错过执行窗口，已跳过", nextRunAt, stillEnabled); err != nil {
			log.Printf("[scheduler] persist skipped failed: id=%d err=%v", task.ID, err)
		}
		log.Printf("[scheduler] task #%d skipped (missed window, next=%s)", task.ID, nextRunAt)
		return
	}

	// 先推进 next_run_at 并落库，再执行——避免长任务过程中被再次扫到；也让失败
	// 不至于每 30s 反复重跑，等到下一个自然触发点再重试。
	nextRunAt, stillEnabled := advanceNextRun(task, now)
	if err := persistScheduledResult(s, task.ID, task.LastRunAt, task.LastStatus, task.LastError, nextRunAt, stillEnabled); err != nil {
		log.Printf("[scheduler] persist next_run_at failed: id=%d err=%v", task.ID, err)
		return
	}

	status, execErr := executeScheduled(s, uploads, task)
	errMsg := ""
	if execErr != nil {
		errMsg = execErr.Error()
		log.Printf("[scheduler] task #%d failed: %v", task.ID, execErr)
	} else {
		log.Printf("[scheduler] task #%d printed (next=%s enabled=%v)", task.ID, nextRunAt, stillEnabled)
	}
	if err := persistScheduledResult(s, task.ID, runAtUTC, status, errMsg, nextRunAt, stillEnabled); err != nil {
		log.Printf("[scheduler] persist result failed: id=%d err=%v", task.ID, err)
	}
}

func persistScheduledResult(s *store.Store, id int64, lastRunAt, lastStatus, lastError, nextRunAt string, enabled bool) error {
	return s.WithTx(context.Background(), false, func(tx *sql.Tx) error {
		return store.UpdateScheduledPrintAfterRun(context.Background(), tx, id, lastRunAt, lastStatus, lastError, nextRunAt, enabled)
	})
}

// executeScheduled 复用 reprint 相同的转换/水印/缩放/IPP 提交链路，但独立成一个函数
// 避免依赖 HTTP request 上下文。返回值 status ∈ {printed, failed}。
func executeScheduled(s *store.Store, uploads string, task store.ScheduledPrint) (string, error) {
	ctx := context.Background()

	// 刷卡取件模式下【拒绝】定时直印。
	//
	// 取件模式的全部意义是「纸只在员工刷卡时才出来」。而这个函数绕过订单链路
	// 直接送 IPP（这条路上连计费也绕过了，见 AGENTS.md 的已知缺口），
	// 让它在取件模式下继续跑，等于企业的保密文档会无人值守地躺在出纸口 ——
	// 那正是拉式打印要消灭的场景。
	//
	// ⚠️ 如实报失败并写进 last_error，让用户在「定时打印」页看得到原因。
	// 静默跳过会让任务看起来像跑过了，比失败更糟。
	if pullModeEnabled(ctx) {
		return "failed", errors.New("刷卡取件模式下不支持定时打印：任务需要到打印机旁刷工牌才会出纸")
	}

	origPath, err := os.OpenInRoot(uploads, filepath.FromSlash(task.StoredPath))
	if err != nil {
		return "failed", fmt.Errorf("原始文件缺失：%w", err)
	}
	_ = origPath.Close()
	absPath := filepath.Join(uploads, filepath.FromSlash(task.StoredPath))

	// 独立超时：转换 + 打印链路加起来预留 5 分钟。
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	// 页码规范化。任务是后台跑的，没有人盯着屏幕 —— 非法页码在 IPP 层会让
	// page-ranges 属性压根不下发，结果是静默打印【整个文档】，而用户第二天
	// 才发现多了一沓纸。
	//
	// 放在转换之前是刻意的：库里可能存着规范化之前写下的 "5-3"（旧前端的
	// 正则放行过它），这种任务应当立刻失败并把原因写进 last_error，
	// 而不是白跑一趟可能几分钟的 LibreOffice/gs 转换再失败。
	normalizedPageRange, err := pricing.NormalizePageRange(task.PageRange)
	if err != nil {
		return "failed", errors.New(msgInvalidPageRange)
	}
	task.PageRange = normalizedPageRange

	printPath := absPath
	var printCleanup func()
	printMime := ""
	var pages int
	kind := detectFileKind(absPath, task.Filename)
	switch kind {
	case fileKindPDF:
		p, cerr := countPDFPages(absPath)
		if cerr != nil {
			log.Printf("[scheduler] countPDFPages failed: %v", cerr)
			p = 1
			printMime = "application/octet-stream"
		} else {
			printMime = "application/pdf"
		}
		pages = p
	case fileKindOffice:
		outPath, cleanup, err := convertOfficeToPDF(ctx, absPath)
		if err != nil {
			return "failed", fmt.Errorf("office 转换失败：%w", err)
		}
		printCleanup = cleanup
		printPath = outPath
		printMime = "application/pdf"
		pages, err = countPDFPages(outPath)
		if err != nil {
			return "failed", fmt.Errorf("解析页数失败：%w", err)
		}
	case fileKindOFD:
		outPath, cleanup, err := convertOFDToPDF(ctx, absPath)
		if err != nil {
			return "failed", fmt.Errorf("ofd 转换失败：%w", err)
		}
		printCleanup = cleanup
		printPath = outPath
		printMime = "application/pdf"
		pages, err = countPDFPages(outPath)
		if err != nil {
			return "failed", fmt.Errorf("解析页数失败：%w", err)
		}
	case fileKindImage:
		outPath, cleanup, err := convertImageToPDF(absPath, task.Orientation, task.PaperSize, false)
		if err != nil {
			return "failed", fmt.Errorf("图片转换失败：%w", err)
		}
		printCleanup = cleanup
		printPath = outPath
		printMime = "application/pdf"
		pages = 1
	case fileKindText:
		p, err := estimateTextPages(absPath)
		if err != nil {
			return "failed", fmt.Errorf("读取页数失败：%w", err)
		}
		outPath, cleanup, err := convertTextToPDF(absPath, task.Orientation, task.PaperSize)
		if err != nil {
			return "failed", fmt.Errorf("文本转换失败：%w", err)
		}
		printCleanup = cleanup
		printPath = outPath
		printMime = "application/pdf"
		pages = p
	default:
		p, _, err := countPages(ctx, absPath, task.Filename)
		if err != nil {
			return "failed", fmt.Errorf("解析页数失败：%w", err)
		}
		pages = p
	}
	if pages < 1 {
		pages = 1
	}
	if printCleanup != nil {
		defer printCleanup()
	}

	if watermark := strings.TrimSpace(task.WatermarkText); watermark != "" && printMime == "application/pdf" {
		wmPath, wmCleanup, wmErr := applyWatermarkToPDF(printPath, watermark)
		if wmErr != nil {
			log.Printf("[scheduler] watermark failed: %v", wmErr)
		} else {
			defer wmCleanup()
			printPath = wmPath
		}
	}

	scaledPath, scaleCleanup, effectiveScaling := resolveCustomScaling(printPath, task.PrintScaling, printMime, "scheduler")
	printPath = scaledPath
	if scaleCleanup != nil {
		defer scaleCleanup()
	}

	pageSet := task.PageSet
	if pageSet == "even-reverse" && printMime == "application/pdf" && pages > 1 {
		reorderedPath, reorderCleanup, rerr := reorderPDFForManualDuplex(printPath, pages, task.PaperSize)
		if rerr != nil {
			log.Printf("[scheduler] even-reverse reorder failed: %v", rerr)
			pageSet = "even"
		} else {
			defer reorderCleanup()
			printPath = reorderedPath
			if rp, _ := countPDFPages(reorderedPath); rp > 0 {
				pages = rp
			}
			pageSet = ""
		}
	}

	isDuplex := task.IsDuplex
	if task.PageSet == "odd" || task.PageSet == "even" || task.PageSet == "even-reverse" {
		isDuplex = false
	}

	// 打印前先在 print_jobs 里落一条历史，让用户在打印历史页看到"来自定时任务"的记录。
	var recordID int64
	if err := s.WithTx(ctx, false, func(tx *sql.Tx) error {
		rec := store.PrintRecord{
			UserID:     task.UserID,
			PrinterURI: task.PrinterURI,
			Filename:   task.Filename,
			StoredPath: task.StoredPath,
			Pages:      pages,
			Status:     "queued",
			IsDuplex:   isDuplex,
			IsColor:    task.IsColor,

			Copies:         task.Copies,
			Orientation:    task.Orientation,
			PaperSize:      task.PaperSize,
			PaperType:      task.PaperType,
			MediaSource:    task.MediaSource,
			PrintScaling:   task.PrintScaling,
			PageRange:      task.PageRange,
			PageSet:        task.PageSet,
			Mirror:         task.Mirror,
			WatermarkText:  task.WatermarkText,
			NumberUp:       task.NumberUp,
			NumberUpLayout: task.NumberUpLayout,
			PageBorder:     task.PageBorder,

			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}
		id, err := store.InsertPrintRecord(ctx, tx, &rec)
		if err != nil {
			return err
		}
		recordID = id
		return nil
	}); err != nil {
		return "failed", fmt.Errorf("落打印历史失败：%w", err)
	}

	f, err := os.Open(printPath)
	if err != nil {
		return "failed", fmt.Errorf("打开待打印文件失败：%w", err)
	}
	defer f.Close()

	mimeType := printMime
	if mimeType == "" {
		buf := make([]byte, 512)
		if n, _ := f.Read(buf); n > 0 {
			mimeType = http.DetectContentType(buf[:n])
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return "failed", fmt.Errorf("回退文件读取失败：%w", err)
			}
		}
	}

	printOpts := ipp.PrintJobOptions{
		IsDuplex:     isDuplex,
		IsColor:      task.IsColor,
		Copies:       task.Copies,
		Orientation:  task.Orientation,
		PaperSize:    task.PaperSize,
		PaperType:    task.PaperType,
		MediaSource:  task.MediaSource,
		PrintScaling: effectiveScaling,
		PageRange:    task.PageRange,
		PageSet:      pageSet,
		Mirror:       task.Mirror,

		NumberUp:       task.NumberUp,
		NumberUpLayout: task.NumberUpLayout,
		PageBorder:     task.PageBorder,
	}

	job, err := ipp.SendPrintJob(task.PrinterURI, f, mimeType, task.Username, task.Filename, printOpts)
	if err != nil {
		_ = s.WithTx(ctx, false, func(tx *sql.Tx) error {
			return store.UpdatePrintStatus(ctx, tx, recordID, "failed", "")
		})
		return "failed", fmt.Errorf("IPP 提交失败：%w", err)
	}
	_ = s.WithTx(ctx, false, func(tx *sql.Tx) error {
		return store.UpdatePrintStatus(ctx, tx, recordID, "printed", job)
	})
	return "printed", nil
}

// advanceNextRun 根据调度类型计算下一次触发时间；对 once 任务归档（enabled=false，
// next_run_at 保留最后一次执行点用于展示）。返回值均使用 UTC RFC3339。
func advanceNextRun(task store.ScheduledPrint, from time.Time) (string, bool) {
	if task.ScheduleType == store.ScheduleOnce {
		return from.UTC().Format(time.RFC3339), false
	}
	next, ok := computeNextRun(task, from.Add(time.Minute))
	if !ok {
		return from.UTC().Format(time.RFC3339), false
	}
	return next.UTC().Format(time.RFC3339), true
}

// computeNextRun 从 after（本地时间）之后寻找下一个触发时间点。
// 返回本地时间；调用方负责转 UTC 落库。
func computeNextRun(task store.ScheduledPrint, after time.Time) (time.Time, bool) {
	loc := time.Local
	after = after.In(loc)

	switch task.ScheduleType {
	case store.ScheduleOnce:
		if task.RunAt == "" {
			return time.Time{}, false
		}
		t, err := time.Parse(time.RFC3339, task.RunAt)
		if err != nil {
			return time.Time{}, false
		}
		return t.In(loc), true

	case store.ScheduleDaily:
		h, m, ok := parseScheduleTime(task.ScheduleTime)
		if !ok {
			return time.Time{}, false
		}
		cand := time.Date(after.Year(), after.Month(), after.Day(), h, m, 0, 0, loc)
		if !cand.After(after) {
			cand = cand.AddDate(0, 0, 1)
		}
		return cand, true

	case store.ScheduleWeekly:
		h, m, ok := parseScheduleTime(task.ScheduleTime)
		if !ok {
			return time.Time{}, false
		}
		target := (task.ScheduleWeekday%7 + 7) % 7
		cand := time.Date(after.Year(), after.Month(), after.Day(), h, m, 0, 0, loc)
		daysAhead := (target - int(cand.Weekday()) + 7) % 7
		cand = cand.AddDate(0, 0, daysAhead)
		if !cand.After(after) {
			cand = cand.AddDate(0, 0, 7)
		}
		return cand, true

	case store.ScheduleMonthly:
		h, m, ok := parseScheduleTime(task.ScheduleTime)
		if !ok {
			return time.Time{}, false
		}
		day := task.ScheduleDay
		if day < 1 {
			day = 1
		}
		if day > 31 {
			day = 31
		}
		year, month := after.Year(), after.Month()
		// 尝试当月对应的日期；若当月不存在该日（比如 2 月 30 日），跳到下一个存在
		// 的月份，避免"某月不触发"和 Go 时间归一化把 2/30 变成 3/2 的坑。
		for i := 0; i < 12; i++ {
			cand := makeMonthDay(year, month, day, h, m, loc)
			if cand.After(after) {
				return cand, true
			}
			// 推进到下个月
			month++
			if month > 12 {
				month = 1
				year++
			}
		}
		return time.Time{}, false
	}
	return time.Time{}, false
}

// makeMonthDay 返回 year-month 里第 day 天 h:m；若 day 超过月末则回退到月末。
func makeMonthDay(year int, month time.Month, day, h, m int, loc *time.Location) time.Time {
	// 用 day=0 拿到 month 的最后一天：Go 会把 (year, month, 0) 归一化为上月末日。
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, loc).Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(year, month, day, h, m, 0, 0, loc)
}

func parseScheduleTime(s string) (int, int, bool) {
	t, err := time.Parse(scheduleTimeLayout, strings.TrimSpace(s))
	if err != nil {
		return 0, 0, false
	}
	return t.Hour(), t.Minute(), true
}

// ensureScheduleDefaults 修剪 store.ScheduledPrint 字段，保证枚举合法。
// 保持业务逻辑集中在一处，handler 与 scheduler 都可以调用。
func ensureScheduleDefaults(rec *store.ScheduledPrint) error {
	if rec.PrinterURI == "" {
		return errors.New("printer 不能为空")
	}
	if rec.Filename == "" || rec.StoredPath == "" {
		return errors.New("文件缺失")
	}
	if rec.Copies < 1 {
		rec.Copies = 1
	}
	switch rec.NumberUp {
	case 1, 2, 4, 6, 9, 16:
	default:
		rec.NumberUp = 1
	}
	if rec.Orientation == "" {
		rec.Orientation = "portrait"
	}
	if rec.PaperSize == "" {
		rec.PaperSize = "A4"
	}
	if rec.PaperType == "" {
		rec.PaperType = "plain"
	}
	if rec.MediaSource == "" {
		rec.MediaSource = "auto"
	}
	if rec.PrintScaling == "" {
		rec.PrintScaling = "fit"
	}
	if rec.PageSet == "" {
		rec.PageSet = "all"
	}
	if rec.NumberUpLayout == "" {
		rec.NumberUpLayout = "lrtb"
	}
	if rec.PageBorder == "" {
		rec.PageBorder = "none"
	}
	switch rec.ScheduleType {
	case store.ScheduleOnce, store.ScheduleDaily, store.ScheduleWeekly, store.ScheduleMonthly:
	default:
		return fmt.Errorf("非法的 schedule_type: %q", rec.ScheduleType)
	}
	if rec.ScheduleType != store.ScheduleOnce {
		if _, _, ok := parseScheduleTime(rec.ScheduleTime); !ok {
			return errors.New("schedule_time 必须是 HH:MM")
		}
	}
	if rec.ScheduleType == store.ScheduleWeekly {
		if rec.ScheduleWeekday < 0 || rec.ScheduleWeekday > 6 {
			return errors.New("schedule_weekday 必须在 0-6")
		}
	}
	if rec.ScheduleType == store.ScheduleMonthly {
		if rec.ScheduleDay < 1 || rec.ScheduleDay > 31 {
			return errors.New("schedule_day 必须在 1-31")
		}
	}
	return nil
}
