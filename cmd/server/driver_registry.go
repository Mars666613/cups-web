package main

import (
	"regexp"
	"runtime"
	"strings"
)

// DriverMeta holds static metadata about a known printer driver.
type DriverMeta struct {
	Name          string   `json:"name"`
	DisplayName   string   `json:"displayName"`
	Description   string   `json:"description"`
	Arch          []string `json:"arch"`
	NeedCompile   bool     `json:"needCompile"`
	MatchPatterns []string `json:"-"` // not exposed to API
}

// DriverStatus extends DriverMeta with installation state.
type DriverStatus struct {
	DriverMeta
	Installed bool `json:"installed"`
	// InstalledAt 取自 metadata.txt 的 installed_at=，缺失时退回 manifest.txt 的 mtime。
	InstalledAt string `json:"installedAt,omitempty"`
	// InstalledArch 取自 metadata.txt 的 arch=：驱动数据目录是挂载卷，
	// 用户可能把 amd64 上装好的卷搬到 arm64 机器上，架构不一致时前端要提示重装。
	InstalledArch string `json:"installedArch,omitempty"`
	// Supported 表示该驱动是否支持当前运行架构（Arch 含 "all" 或含当前架构）。
	// 前端据此禁用「安装」按钮，避免用户在 armhf 上点了必然失败的 amd64 专有驱动。
	Supported bool `json:"supported"`
	// HasScript 表示镜像里确实存在 install-<name>.sh；镜像瘦身或裁剪脚本时
	// 注册表可能领先于实际脚本，提前暴露出来比让 driver-install 报错更友好。
	HasScript bool `json:"hasScript"`
	// RestoreMode 取自 metadata.txt 的 restore_mode=，取值 package / files / hybrid。
	// 它决定容器重启后驱动能否被**完整**恢复：厂商 deb 普遍把产物装在 /opt、/usr/bin、
	// /usr/share/<vendor> 等文件级路径白名单之外的位置，只有归档 .deb 原件
	// （package / hybrid）才能完整装回来。
	// 老快照（v1，没跑过新版 driver-install）没有这个键，此时为空串 —— 前端据此提示
	// 用户"重装一次以启用包级恢复"。
	RestoreMode string `json:"restoreMode,omitempty"`
	// PackageCount 取自 metadata.txt 的 package_count=：本驱动归档了几个 .deb。
	PackageCount int `json:"packageCount,omitempty"`
	// ManifestVersion 取自 metadata.txt 的 manifest_version=（v2 起才写）。
	ManifestVersion int `json:"manifestVersion,omitempty"`
}

// CustomDebPackage 描述一个通过 /api/admin/drivers/upload 上传的 .deb 包。
// 归档在 custom-deb/packages/ 下，容器启动时由 restore-drivers 用 dpkg -i 自动重装
// （幂等：已装且版本不更旧就跳过）。
type CustomDebPackage struct {
	Filename    string `json:"filename"`
	InstalledAt string `json:"installedAt,omitempty"`
	Arch        string `json:"installedArch,omitempty"`
	SizeBytes   int64  `json:"sizeBytes,omitempty"`
}

// DetectedPrinter represents a printer discovered via lpinfo.
type DetectedPrinter struct {
	DeviceURI    string      `json:"deviceUri"`
	Manufacturer string      `json:"manufacturer"`
	Model        string      `json:"model"`
	Connection   string      `json:"connection"` // "usb", "network", "direct"
	DriverMatch  *DriverMeta `json:"driverMatch"`
	HasDriver    bool        `json:"hasDriver"` // CUPS already has a matching PPD

	// 以下为 PPD 候选匹配引擎新增字段（全部 omitempty，保持向后兼容）。

	// DeviceID 是原始 IEEE-1284 串（lpinfo 的 device-id 字段）。
	// 历史实现解析出 MFG/MDL 后就丢弃了原串，但它是 cups-driverd 指纹匹配的唯一输入，
	// 也是排障时的金矿——管理员截图就能看出设备自报了什么。
	DeviceID string `json:"deviceId,omitempty"`
	// MakeAndModel 是 lpinfo 的 make-and-model 原文，供人工核对。
	MakeAndModel string `json:"makeAndModel,omitempty"`
	// Info 是 lpinfo 的 info 字段。
	Info string `json:"info,omitempty"`
	// Location 是 lpinfo 的 location 字段，多机场景辨认设备用。
	Location string `json:"location,omitempty"`
	// Scheme 是 device-uri 的 scheme（usb / ipp / ipps / dnssd / socket / lpd）。
	Scheme string `json:"scheme,omitempty"`
	// DriverState 是四态驱动状态：ready / driverless / needsVendorDriver / unmatched。
	DriverState string `json:"driverState"`
	// TopCandidate 是本地打分的 Top-1 候选（detect 内联，零额外 fork）。
	TopCandidate *PPDCandidate `json:"topCandidate,omitempty"`
	// CandidateCount 是候选总数（含 generic 兜底）。
	CandidateCount int `json:"candidateCount"`
	// ExistingQueue 是该 device-uri 已有的 CUPS 队列名（lpstat -v 查出）。
	ExistingQueue string `json:"existingQueue,omitempty"`
	// SuggestedName 是去重后的建议队列名。
	SuggestedName string `json:"suggestedName,omitempty"`
}

var driversRegistry = []DriverMeta{
	{
		Name: "canon-ufr2", DisplayName: "Canon UFR II",
		Description: "Canon imageCLASS / i-SENSYS / imageRUNNER 系列激光打印机",
		Arch:        []string{"amd64", "arm64"}, NeedCompile: false,
		MatchPatterns: []string{`Canon iR`, `Canon imageCLASS`, `Canon i-SENSYS`, `Canon MF`, `Canon LBP[3-9]`, `Canon imageRUNNER`},
	},
	{
		Name: "canon-capt", DisplayName: "Canon CAPT",
		Description: "Canon LBP2900 / LBP2900B",
		Arch:        []string{"all"}, NeedCompile: true,
		MatchPatterns: []string{`Canon LBP2900`, `Canon LBP-2900`},
	},
	{
		Name: "hp-laserjet1020", DisplayName: "HP LaserJet 1020",
		Description: "HP LaserJet 1020 固件 + A4 默认 PPD",
		Arch:        []string{"all"}, NeedCompile: false,
		MatchPatterns: []string{`HP LaserJet 1020`},
	},
	{
		Name: "foo2zjs-firmware", DisplayName: "HP foo2zjs Firmware",
		Description: "HP LaserJet 1000/1005/1018/P1005/P1006/P1505 固件",
		Arch:        []string{"all"}, NeedCompile: true,
		MatchPatterns: []string{`HP LaserJet 1000`, `HP LaserJet 1005`, `HP LaserJet 1018`, `HP LaserJet P100`, `HP LaserJet P1505`},
	},
	{
		Name: "escpr2", DisplayName: "Epson ESC/P-R 2",
		Description: "Epson ET-18100, L8050, L8160, WF-7840 等新款喷墨打印机",
		Arch:        []string{"amd64", "armhf", "arm64"}, NeedCompile: false,
		MatchPatterns: []string{`Epson ET-`, `Epson L[0-9]`, `Epson WF-`, `Epson XP-`},
	},
	{
		Name: "epson-cn", DisplayName: "Epson 国行驱动",
		Description: "Epson L380, L455 等国行喷墨打印机",
		Arch:        []string{"amd64"}, NeedCompile: false,
		MatchPatterns: []string{`Epson L380`, `Epson L455`},
	},
	{
		Name: "konica-bizhub", DisplayName: "Konica Minolta bizhub",
		Description: "Konica Minolta bizhub 3000MF 黑白激光打印机",
		Arch:        []string{"amd64", "arm64"}, NeedCompile: false,
		MatchPatterns: []string{`KONICA MINOLTA`, `Konica Minolta`, `bizhub`},
	},
	{
		Name: "sharp", DisplayName: "Sharp PostScript",
		Description: "Sharp MX-C2622R 等 PostScript 打印机",
		Arch:        []string{"all"}, NeedCompile: false,
		MatchPatterns: []string{`Sharp MX`, `SHARP MX`},
	},
	{
		Name: "gutenprint", DisplayName: "Gutenprint",
		Description: "Gutenprint 高质量打印驱动（支持大量打印机型号）",
		Arch:        []string{"amd64", "arm64"}, NeedCompile: false,
		MatchPatterns: []string{}, // Generic, matches many printers
	},
}

// matchDriverForPrinter finds the best matching driver for a printer model string.
func matchDriverForPrinter(modelStr string) *DriverMeta {
	upper := strings.ToUpper(modelStr)
	for i := range driversRegistry {
		d := &driversRegistry[i]
		for _, pattern := range d.MatchPatterns {
			re, err := regexp.Compile("(?i)" + pattern)
			if err != nil {
				// Fall back to simple substring match if regex is invalid.
				if strings.Contains(upper, strings.ToUpper(pattern)) {
					return d
				}
				continue
			}
			if re.MatchString(modelStr) {
				return d
			}
		}
	}
	return nil
}

// currentDebArch 把 Go 的 GOARCH 映射成 Debian 架构名，好和注册表里
// DriverMeta.Arch（写的是 amd64 / arm64 / armhf 这套 Debian 命名）直接比对。
// 二进制是 CGO_ENABLED=0 交叉编译的，GOARCH 就是运行架构；未知架构原样返回，
// 让前端至少能显示出来而不是伪装成 amd64。
func currentDebArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "amd64"
	case "arm64":
		return "arm64"
	case "arm":
		return "armhf"
	case "386":
		return "i386"
	default:
		return runtime.GOARCH
	}
}

// driverSupportsArch 判断驱动是否支持给定架构。Arch 为空视为通用（不做限制），
// 含 "all" 表示纯脚本/固件类驱动与架构无关。
func driverSupportsArch(d DriverMeta, arch string) bool {
	if len(d.Arch) == 0 {
		return true
	}
	for _, a := range d.Arch {
		if a == "all" || strings.EqualFold(a, arch) {
			return true
		}
	}
	return false
}

// findDriverByName looks up a driver by its canonical name.
func findDriverByName(name string) *DriverMeta {
	for i := range driversRegistry {
		if driversRegistry[i].Name == name {
			return &driversRegistry[i]
		}
	}
	return nil
}
