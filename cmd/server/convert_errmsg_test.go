package main

// 「底层错误 → 用户看得懂的一句话」这三个函数的测试。
//
// 它们存在的理由是：同一个失败在四条孪生流水线（print / reprint / order /
// scheduled）里过去会给出四种不同质量的提示，而只有指对方向的那些能省下
// 排查时间。所以这里盯的不是措辞好不好听，而是【几类根因有没有被区分开】：
//
//	缺外部工具 → 要说「没装什么」，让管理员去装包
//	磁盘满/权限 → 要说「服务器的问题」，别让用户反复重试或换文件
//	其它       → 原样带出底层错误，不吞
//
// 实测踩过：docx 数不出页数只报「failed to read pages」，让人反复怀疑自己的
// 文件，其实是服务器没装 LibreOffice。

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

func TestConvertErrMsgDistinguishesMissingTool(t *testing.T) {
	// LibreOffice 未安装：要点名工具，并告诉用户下一步找谁。
	msg := convertErrMsg(fmt.Errorf("libreoffice %w", errBinaryNotInstalled))
	if !strings.Contains(msg, "LibreOffice") || !strings.Contains(msg, "未安装") {
		t.Errorf("缺 LibreOffice 时应当点名并说明未安装，实际: %q", msg)
	}

	// 其它外部命令缺失（exec.ErrNotFound）：也要走「缺工具」这一档，
	// 不能回落到「文件转换失败」让人去怀疑文件。
	msg = convertErrMsg(fmt.Errorf("lpinfo: %w", exec.ErrNotFound))
	if !strings.Contains(msg, "缺少") {
		t.Errorf("缺外部命令时应当说明缺少工具，实际: %q", msg)
	}

	// 其它错误：原样带出，不吞。
	msg = convertErrMsg(errors.New("boom-detail"))
	if !strings.Contains(msg, "boom-detail") {
		t.Errorf("未知错误必须把原文带出去，实际: %q", msg)
	}
}

func TestCountPagesErrMsgDelegatesMissingTool(t *testing.T) {
	// docx / OFD 的页数要先转换成 PDF 才数得出来，所以「数不出页数」这条路上
	// 同样会撞见「缺工具」。必须复用 convertErrMsg 的判据 —— 直接报
	// 「读不出页数」会让用户去怀疑文件。
	msg := countPagesErrMsg(fmt.Errorf("libreoffice %w", errBinaryNotInstalled))
	if !strings.Contains(msg, "LibreOffice") {
		t.Errorf("数页时缺工具应当点名 LibreOffice，实际: %q", msg)
	}
	if strings.Contains(msg, "页数") {
		t.Errorf("缺工具时不该说成「读不出页数」（会把方向带偏），实际: %q", msg)
	}

	// 真正的文件问题：说清楚是页数读不出来，并把原文带出。
	msg = countPagesErrMsg(errors.New("boom-detail"))
	if !strings.Contains(msg, "页数") || !strings.Contains(msg, "boom-detail") {
		t.Errorf("应说明是页数问题并带出原文，实际: %q", msg)
	}
}

func TestSaveErrMsgDistinguishesServerSideFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		// 磁盘满最典型：用户重试一百次也不会好，得让管理员去 df -h。
		{"磁盘满", fmt.Errorf("write uploads/x: %w", syscall.ENOSPC), "磁盘"},
		{"权限不足", fmt.Errorf("open uploads/x: %w", syscall.EACCES), "权限"},
		{"只读文件系统", fmt.Errorf("create uploads/x: %w", syscall.EROFS), "只读"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := saveErrMsg(c.err)
			if !strings.Contains(msg, c.want) {
				t.Errorf("应当指出是服务器侧的问题（含 %q），实际: %q", c.want, msg)
			}
			// 这类错误的共同点是「不是用户能修的」，所以要点名找管理员。
			if !strings.Contains(msg, "管理员") {
				t.Errorf("应当告诉用户去找管理员，实际: %q", msg)
			}
		})
	}

	t.Run("其它错误原样带出", func(t *testing.T) {
		msg := saveErrMsg(errors.New("boom-detail"))
		if !strings.Contains(msg, "boom-detail") {
			t.Errorf("未知错误必须把原文带出去，实际: %q", msg)
		}
	})
}
