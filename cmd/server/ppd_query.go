package main

// PPD 查询层（有副作用的一侧）。
//
// 纯函数打分器在 ppd_match.go；本文件负责所有需要 fork 进程或访问网络的操作：
// lpinfo -m 缓存、lpinfo 选项能力探测、cups-driverd 委托匹配、
// IPP Everywhere 判定（复用 internal/ipp 的 goipp 客户端）、队列快照。
//
// 设计不变量：
//   - 候选查询不占 startDriverJob 的全局单飞锁（否则"看候选"和"装驱动"互斥）。
//   - 对 lpinfo/lpadmin 选项的依赖一律「能力探测 + 降级」：探测失败 = 该能力不存在。
//   - 缓存 fork 用独立 context（context.WithoutCancel），绝不挂 r.Context()。

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"cups-web/internal/ipp"
)

// ── lpinfo -m 缓存 ─────────────────────────────────────────────────────────────

const ppdModelCacheTTL = 10 * time.Minute

type ppdModelCache struct {
	mu       sync.Mutex
	entries  []PPDEntry
	lines    int
	loadedAt time.Time
}

var ppdModels ppdModelCache

// cachedPPDEntries 返回缓存的 PPD 条目列表。
// 首次调用或 TTL 过期时 fork `lpinfo -m`（独立 20s 超时，不挂请求 context）。
// 全程持锁串行化——lpinfo -m 是秒级操作，串行化正好避免并发 fork。
func cachedPPDEntries(ctx context.Context) ([]PPDEntry, error) {
	ppdModels.mu.Lock()
	defer ppdModels.mu.Unlock()

	if ppdModels.entries != nil && time.Since(ppdModels.loadedAt) < ppdModelCacheTTL {
		return ppdModels.entries, nil
	}
	return refreshPPDModelsLocked(ctx)
}

// refreshPPDModelsLocked 强制重新 fork lpinfo -m 并更新缓存。调用方必须持有 ppdModels.mu。
func refreshPPDModelsLocked(ctx context.Context) ([]PPDEntry, error) {
	// 独立 context：前端取消 / 120s WriteTimeout 不该把缓存填成半成品。
	forkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()

	output, err := exec.CommandContext(forkCtx, "lpinfo", "-m").Output()
	if err != nil {
		return nil, fmt.Errorf("lpinfo -m: %w", err)
	}
	lines := strings.Split(string(output), "\n")
	entries := ParsePPDLines(lines)
	ppdModels.entries = entries
	ppdModels.lines = len(lines)
	ppdModels.loadedAt = time.Now()
	return entries, nil
}

// invalidatePPDModels 显式使缓存失效。
// 失效点清单（漏一条就是回归）：
//  1. adminInstallDriverHandler 的 job 成功后
//  2. adminRemoveDriverHandler 的 job 成功后
//  3. adminSetupPrinterHandler job 内 driver-install 成功后
//  4. adminUploadDriverHandler 的 .ppd 安装成功后
//  5. adminUploadDriverHandler 的 .deb 安装成功后
//  6. TTL 10 分钟兜底（覆盖 restore-drivers 与容器内手工 apt install）
func invalidatePPDModels() {
	ppdModels.mu.Lock()
	defer ppdModels.mu.Unlock()
	ppdModels.entries = nil
	ppdModels.lines = 0
}

// refreshPPDEntriesAfterInstall 在驱动安装后刷新 PPD 缓存。
//
// ppds.dat 是 cupsd/cups-driverd 自己的缓存，driver-install 之后立刻 lpinfo -m
// 可能仍是旧列表。做法：失效 → 取一次记录行数 → 若行数与安装前相同，
// sleep 1s 重试，最多 3 次，每次把行数写进 job log（可观测、无依赖）。
func refreshPPDEntriesAfterInstall(ctx context.Context, logBuf *safeBuffer) ([]PPDEntry, error) {
	ppdModels.mu.Lock()
	beforeLines := ppdModels.lines
	ppdModels.entries = nil
	ppdModels.mu.Unlock()

	var entries []PPDEntry
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		ppdModels.mu.Lock()
		entries, err = refreshPPDModelsLocked(ctx)
		afterLines := ppdModels.lines
		ppdModels.mu.Unlock()

		if err != nil {
			return nil, err
		}
		fmt.Fprintf(logBuf, "PPD 列表: %d → %d 行\n", beforeLines, afterLines)
		if afterLines != beforeLines || attempt == 2 {
			break
		}
		// 行数没变，ppds.dat 可能还没重建，等一下再试。
		select {
		case <-ctx.Done():
			return entries, ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
	return entries, nil
}

// ppdCacheAgeSeconds 返回缓存年龄（秒），供接口的 matcher 字段排障用。
func ppdCacheAgeSeconds() int {
	ppdModels.mu.Lock()
	defer ppdModels.mu.Unlock()
	if ppdModels.entries == nil {
		return -1
	}
	return int(time.Since(ppdModels.loadedAt).Seconds())
}

// ── lpinfo 能力探测 ────────────────────────────────────────────────────────────

// lpinfoCaps 记录 lpinfo 支持的选项。
// 本环境无法联网核实 CUPS 各版本的选项差异，所以一律运行时探测。
type lpinfoCaps struct {
	DeviceID     bool   // --device-id
	MakeAndModel bool   // --make-and-model
	Language     bool   // --language
	Timeout      bool   // --timeout
	Schemes      bool   // --include-schemes / --exclude-schemes
	Raw          string // --help 原文片段，写进 matcher 字段便于排障
}

var (
	lpinfoCapsOnce  sync.Once
	lpinfoCapsValue lpinfoCaps
)

// lpinfoCapabilities 探测 lpinfo 支持的选项，结果进程内缓存。
func lpinfoCapabilities(ctx context.Context) lpinfoCaps {
	lpinfoCapsOnce.Do(func() {
		lpinfoCapsValue = probeLpinfoCaps(ctx)
	})
	return lpinfoCapsValue
}

func probeLpinfoCaps(ctx context.Context) lpinfoCaps {
	caps := lpinfoCaps{}

	// 第一层：lpinfo --help（合并 stdout+stderr，忽略退出码——CUPS 常以 1 退出）。
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(probeCtx, "lpinfo", "--help")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	_ = cmd.Run() // 忽略退出码
	help := buf.String()
	caps.Raw = help

	if help != "" {
		caps.DeviceID = strings.Contains(help, "--device-id")
		caps.MakeAndModel = strings.Contains(help, "--make-and-model")
		caps.Language = strings.Contains(help, "--language")
		caps.Timeout = strings.Contains(help, "--timeout")
		caps.Schemes = strings.Contains(help, "--include-schemes") || strings.Contains(help, "--exclude-schemes")
		return caps
	}

	// 第二层：--help 输出为空时，行为探测。
	caps.DeviceID = probeLpinfoOption(ctx, "--device-id", "MFG:CupsWebProbe;MDL:Probe;")
	caps.MakeAndModel = probeLpinfoOption(ctx, "--make-and-model", "CupsWeb Probe")
	return caps
}

// probeLpinfoOption 用一次实际调用探测某个 lpinfo 选项是否可用。
func probeLpinfoOption(ctx context.Context, flag, value string) bool {
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, "lpinfo", "-m", flag, value)
	// 退出码 0 = 支持（输出可以为空）；非 0 = 不支持。
	return cmd.Run() == nil
}

// ── cups-driverd 委托 ──────────────────────────────────────────────────────────

// driverdRanks 调用 lpinfo -m --device-id（或 --make-and-model）获取 cups-driverd 的排名。
//
// 把 driverd 当「加分器 / 标注器」，不当替代品：
// --device-id 匹配的是 PPD 里的 *1284DeviceID 指纹，大量 foomatic PPD 没有这个字段，
// 纯委托会对本地打分能搞定的机型返回空集；反过来本地 substring 抓不到指纹级精确匹配。
// 两者相加，任一侧失效只降级不失败。
//
// 返回 ppd-name → 排名（1-based）的 map、匹配到的条目数、错误。
func driverdRanks(ctx context.Context, deviceID, makeAndModel string) (map[string]int, int, error) {
	caps := lpinfoCapabilities(ctx)
	ranks := make(map[string]int)

	tryParse := func(args ...string) (int, error) {
		rankCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
		defer cancel()
		output, err := exec.CommandContext(rankCtx, "lpinfo", args...).Output()
		if err != nil {
			return 0, err
		}
		count := 0
		for i, line := range strings.Split(string(output), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			ppd, _, ok := strings.Cut(line, " ")
			if !ok || ppd == "" {
				continue
			}
			count++
			if _, exists := ranks[ppd]; !exists {
				ranks[ppd] = i + 1 // 1-based 排名
			}
		}
		return count, nil
	}

	// 优先 --device-id（指纹级匹配）。
	if caps.DeviceID && deviceID != "" {
		n, err := tryParse("-m", "--device-id", deviceID)
		if err == nil && n > 0 {
			return ranks, n, nil
		}
	}
	// 退一步 --make-and-model。
	if caps.MakeAndModel && makeAndModel != "" {
		n, err := tryParse("-m", "--make-and-model", makeAndModel)
		if err == nil && n > 0 {
			return ranks, n, nil
		}
	}
	return ranks, 0, nil
}

// ── driverless / IPP Everywhere 判定 ───────────────────────────────────────────

// detectDriverless 完整判定一台打印机的 IPP Everywhere 可用性。
// 三层：① scheme 白名单 ② CUPS 侧 everywhere 条目 ③ 打印机 IPP 属性确认。
func detectDriverless(ctx context.Context, uri string, entries []PPDEntry) driverlessInfo {
	// 第 1+2 层：纯函数。
	info := driverlessSchemeCheck(uri, entries)
	if !info.Available {
		return info
	}

	// 第 3 层：用 goipp 查打印机 IPP 属性（纯 Go，已有 SSRF 防护 + 超时，无需 fork）。
	// dnssd:// 给不了 goipp（scheme 不在 allowedSchemes 里），停在 level 1。
	scheme := uriScheme(uri)
	if scheme == "dnssd" {
		info.Reason = "IPP 连接（DNS-SD 发现），CUPS 支持免驱动模式"
		return info
	}

	pinfo, err := ipp.GetPrinterAttributes(uri)
	if err != nil {
		// 探测失败只降级，不报错（部署方可能设了 PRINTER_BLOCK_PRIVATE=true 封了 loopback）。
		info.Evidence = fmt.Sprintf("IPP 属性查询失败: %v", err)
		return info
	}

	// 检查 document-format-supported 是否含 image/urf 或 application/pdf。
	if pinfo != nil {
		fmts := pinfo.Attributes["document-format-supported"]
		if strings.Contains(fmts, "image/urf") || strings.Contains(fmts, "application/pdf") {
			info.Level = 2
			info.Reason = "打印机确认支持 IPP Everywhere（IPP 属性已验证）"
			info.Evidence = "document-format-supported: " + fmts
		}
	}
	return info
}

// ── 队列快照 ───────────────────────────────────────────────────────────────────

// listExistingQueues 解析 `lpstat -v` 输出，返回 队列名 → device-uri 的映射。
// 每行形如 "device for LaserJet_1020: usb://HP/LaserJet%201020?serial=XXX"。
func listExistingQueues(ctx context.Context) (map[string]string, error) {
	qctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	output, err := exec.CommandContext(qctx, "lpstat", "-v").Output()
	if err != nil {
		// lpstat 在没有队列时可能返回非零退出码，但 stdout 仍有内容。
		// 如果 stdout 为空才视为真正的错误。
		if len(bytes.TrimSpace(output)) == 0 {
			return make(map[string]string), nil
		}
	}

	queues := make(map[string]string)
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		// "device for NAME: URI"
		rest, ok := strings.CutPrefix(line, "device for ")
		if !ok {
			continue
		}
		name, uri, ok := strings.Cut(rest, ": ")
		if !ok {
			continue
		}
		queues[strings.TrimSpace(name)] = strings.TrimSpace(uri)
	}
	return queues, nil
}

// findQueueByURI 在队列快照里找某个 device-uri 对应的队列名。
func findQueueByURI(queues map[string]string, uri string) string {
	for name, u := range queues {
		if u == uri {
			return name
		}
	}
	return ""
}

// ── 命令执行辅助 ───────────────────────────────────────────────────────────────

// runDriverCommandOutput 与 runDriverCommand 行为一致（$ cmd 回显 + CUPS_AIO=1 + stderr 进 logBuf），
// 额外把 stdout 作为字符串返回。
//
// 抽这个 helper 是因为 detect（lpinfo -l -v）、listCUPSModels（lpinfo -m）、
// 验证（lpstat/lpoptions）三处都需要「跑命令 + 拿 stdout + 日志可观测」，
// 之前各自绕过 runDriverCommand 直接 .Output()，复制了三份。
func runDriverCommandOutput(ctx context.Context, logBuf *safeBuffer, name string, args ...string) (string, error) {
	fmt.Fprintf(logBuf, "$ %s %s\n", name, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "CUPS_AIO=1")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = logBuf
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(logBuf, "!! 命令失败: %v\n", err)
		return stdout.String(), err
	}
	return stdout.String(), nil
}

// ── 匹配编排 ───────────────────────────────────────────────────────────────────

// matchPPDCandidates 是一次完整匹配的编排入口：
// 缓存取 PPD 列表 → 可选 driverd 委托 → driverless 判定 → 打分排序。
//
// withDriverd 控制是否做 cups-driverd 委托（detect 里传 false 避免 N 台 × fork）。
func matchPPDCandidates(ctx context.Context, logBuf *safeBuffer,
	manufacturer, model, deviceID, uri string, withDriverd bool, topN int,
) ([]PPDCandidate, driverlessInfo, int, error) {

	entries, err := cachedPPDEntries(ctx)
	if err != nil {
		return nil, driverlessInfo{}, 0, fmt.Errorf("获取 PPD 列表失败: %w", err)
	}

	scheme := uriScheme(uri)

	// driverd 委托（可选）。
	var ranks map[string]int
	driverdMatched := 0
	if withDriverd {
		makeAndModel := strings.TrimSpace(manufacturer + " " + model)
		ranks, driverdMatched, _ = driverdRanks(ctx, deviceID, makeAndModel)
		if logBuf != nil && driverdMatched > 0 {
			fmt.Fprintf(logBuf, "cups-driverd 指纹匹配命中 %d 条\n", driverdMatched)
		}
	}

	// driverless 判定。
	dlInfo := detectDriverless(ctx, uri, entries)
	evidence := 0
	if dlInfo.Available {
		evidence = dlInfo.Level
	}

	in := MatchInput{
		Manufacturer:       manufacturer,
		Model:              model,
		DeviceID:           deviceID,
		Scheme:             scheme,
		PreferLang:         "zh",
		DriverdRanks:       ranks,
		EverywhereEvidence: evidence,
	}
	cands := ScorePPDCandidates(entries, in, topN)
	return cands, dlInfo, driverdMatched, nil
}

// bestPPDFromCandidates 从候选列表里取 Top-1 的 ppd-name（非 generic、非 low confidence）。
// 返回空串表示没有可用的自动匹配。
func bestPPDFromCandidates(cands []PPDCandidate) string {
	if len(cands) == 0 {
		return ""
	}
	top := cands[0]
	if top.Source == PPDSourceGeneric || top.Confidence == ppdConfidenceLow {
		return ""
	}
	return top.PPD
}

// ── 队列验证 ───────────────────────────────────────────────────────────────────

// verifyPrinterQueue 在 lpadmin 之后验证队列是否真正生效。
// 返回选项数、media-source 数、告警列表。
func verifyPrinterQueue(ctx context.Context, logBuf *safeBuffer, printerName string, expectPPD bool) (optionCount int, mediaSourceCount int, warnings []string) {
	// lpstat -p：队列存在性。
	if _, err := runDriverCommandOutput(ctx, logBuf, "lpstat", "-p", printerName); err != nil {
		warnings = append(warnings, fmt.Sprintf("lpstat -p %s 失败: %v", printerName, err))
		return
	}

	// lpoptions -p -l：PPD 选项列表。传了 -m 时必须非空。
	output, err := runDriverCommandOutput(ctx, logBuf, "lpoptions", "-p", printerName, "-l")
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("lpoptions -l 失败: %v", err))
		return
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			optionCount++
		}
		if strings.Contains(line, "InputSlot") || strings.Contains(line, "media-source") {
			mediaSourceCount++
		}
	}
	if expectPPD && optionCount == 0 {
		warnings = append(warnings, "lpoptions -l 输出为空——PPD 可能未真正生效（raw 队列）")
	}
	// raw 队列的 lpoptions -l 不是空输出，而是 "Unable to get PPD file"。
	if expectPPD && strings.Contains(output, "Unable to get PPD") {
		optionCount = 0
		warnings = append(warnings, "lpoptions 报告 PPD 不存在——队列可能是 raw 模式")
	}

	// /etc/cups/ppd/<name>.ppd 存在性（仅告警）。
	if expectPPD {
		ppdPath := fmt.Sprintf("/etc/cups/ppd/%s.ppd", printerName)
		if _, err := os.Stat(ppdPath); os.IsNotExist(err) {
			warnings = append(warnings, fmt.Sprintf("PPD 文件 %s 不存在", ppdPath))
		}
	}

	// IPP 属性查询（只告警，部署方可能封了 loopback）。
	// IPP 数据比 lpoptions 更权威，成功时覆盖；失败时保留 lpoptions 的统计值。
	localURI := fmt.Sprintf("http://localhost:631/printers/%s", printerName)
	if pinfo, err := ipp.GetPrinterAttributes(localURI); err == nil && pinfo != nil {
		if len(pinfo.MediaSourceSupported) > 0 {
			mediaSourceCount = len(pinfo.MediaSourceSupported)
		}
	} else if err != nil {
		warnings = append(warnings, fmt.Sprintf("IPP 属性查询失败（不影响使用）: %v", err))
	}

	return
}
