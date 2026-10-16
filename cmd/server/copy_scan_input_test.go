package main

import (
	"errors"
	"strings"
	"testing"

	"cups-web/internal/escl"
)

// 现场机器的能力：玻璃板 + 进稿器（单/双面都支持）。
var bothInputs = &escl.Capabilities{HasPlaten: true, HasAdf: true, AdfDuplex: true}

func TestResolveScanInput(t *testing.T) {
	cases := []struct {
		name      string
		requested string
		adfDuplex bool
		caps      *escl.Capabilities
		wantSrc   string
		wantDup   bool
		wantErr   string
	}{
		{
			name: "玻璃板单页", requested: "platen", caps: bothInputs,
			wantSrc: escl.SourcePlaten,
		},
		{
			name: "进稿器单面", requested: "adf", caps: bothInputs,
			wantSrc: escl.SourceADF,
		},
		{
			name: "进稿器双面", requested: "adf", adfDuplex: true, caps: bothInputs,
			wantSrc: escl.SourceADF, wantDup: true,
		},
		{
			// 玻璃板没有双面这回事：宁可报错，也不能把 duplex 悄悄带下去。
			name: "玻璃板不接受双面原件", requested: "platen", adfDuplex: true, caps: bothInputs,
			wantErr: "玻璃板一次只能放一页",
		},
		{
			name: "只有玻璃板的机器选进稿器", requested: "adf",
			caps:    &escl.Capabilities{HasPlaten: true},
			wantErr: "没有进稿器",
		},
		{
			name: "只有进稿器的机器选玻璃板", requested: "platen",
			caps:    &escl.Capabilities{HasAdf: true, AdfDuplex: true},
			wantErr: "没有玻璃板",
		},
		{
			name: "设备不支持 ADF 双面", requested: "adf", adfDuplex: true,
			caps:    &escl.Capabilities{HasPlaten: true, HasAdf: true},
			wantErr: "不支持双面扫描",
		},
		{
			name: "不认识的值直接拒绝", requested: "feeder",
			caps:    bothInputs,
			wantErr: "不认识的放稿方式",
		},
		{
			// 兼容未升级的前端：留空 = 旧行为（有进稿器就走进稿器）。
			name: "留空且有进稿器", caps: bothInputs,
			wantSrc: escl.SourceADF,
		},
		{
			name: "留空且只有玻璃板", caps: &escl.Capabilities{HasPlaten: true},
			wantSrc: escl.SourcePlaten,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src, dup, err := resolveScanInput(c.requested, c.adfDuplex, c.caps)
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("应报错（%s）", c.wantErr)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("错误信息应包含 %q，实际: %v", c.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("不该报错: %v", err)
			}
			if src != c.wantSrc || dup != c.wantDup {
				t.Fatalf("source/duplex = %q/%v, want %q/%v", src, dup, c.wantSrc, c.wantDup)
			}
		})
	}
}

// 进稿器预检：只有设备【明确】说「空」才拦，其余一律放行。
//
// 两边的错都要避免：该拦不拦 → 用户等十几秒换来一条看不懂的设备报错；
// 不该拦却拦了 → 有些机型常年报 Empty，会把正常复印彻底锁死。
func TestAdfPreflight(t *testing.T) {
	cases := []struct {
		name    string
		source  string
		force   bool
		status  *escl.Status
		wantErr bool
	}{
		{
			name: "进稿器明确为空 → 拦下", source: escl.SourceADF,
			status: &escl.Status{AdfState: "ScannerAdfEmpty"}, wantErr: true,
		},
		{
			name: "进稿器里有原件 → 放行", source: escl.SourceADF,
			status: &escl.Status{AdfState: "ScannerAdfLoaded"},
		},
		{
			name: "进稿器正在走纸 → 放行", source: escl.SourceADF,
			status: &escl.Status{AdfState: "ScannerAdfProcessing"},
		},
		{
			// 设备插槽老/固件不上报：拿「不知道」当「空」会误伤。
			name: "设备不上报 AdfState → 放行", source: escl.SourceADF,
			status: &escl.Status{},
		},
		{
			name: "设备报卡纸（非装载状态）→ 放行", source: escl.SourceADF,
			status: &escl.Status{AdfState: "ScannerAdfJam"},
		},
		{
			name: "状态查询失败拿不到状态 → 放行", source: escl.SourceADF, status: nil,
		},
		{
			name: "玻璃板不受进稿器状态影响", source: escl.SourcePlaten,
			status: &escl.Status{AdfState: "ScannerAdfEmpty"},
		},
		{
			// 用户已经确认放好了：设备可能报错了装载状态，不能再挡他。
			name: "用户确认后放行", source: escl.SourceADF, force: true,
			status: &escl.Status{AdfState: "ScannerAdfEmpty"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := adfPreflight(c.source, c.force, c.status)
			if c.wantErr && !errors.Is(err, errAdfEmpty) {
				t.Fatalf("应返回 errAdfEmpty, got %v", err)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("不该拦: %v", err)
			}
		})
	}
}
