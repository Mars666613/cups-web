package main

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cups-web/internal/store"
)

// testSchemaDSN 建一个随机 schema 并把 search_path 指过去，返回可用的 DSN。
//
// 与 internal/store 里的测试同一套做法（那里是 testSchema）：需要
// TEST_DATABASE_URL，未设置就跳过；每个测试一个独立 schema，结束 drop。
// 两个包各自实现一份是无奈 —— 测试辅助函数没法跨包共享。
func testSchemaDSN(t *testing.T) string {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL 未设置，跳过需要 PostgreSQL 的测试")
	}
	schema := fmt.Sprintf("m%d_%d", time.Now().UnixNano()%1e9, rand.Intn(10000))

	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		_ = admin.Close()
		t.Fatalf("建 schema 失败: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE")
		_ = admin.Close()
	})

	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	// 空格编成 %20，不能是 QueryEscape 默认的 '+'（pgx 会当成参数名的一部分）。
	opt := strings.ReplaceAll(url.QueryEscape("-c search_path="+schema), "+", "%20")
	return base + sep + "options=" + opt
}

// newTestEnv 建一个临时库（独立 schema）+ 临时 uploads 目录，并把包级 uploadDir
// 指过去（cleanupOrderArtifacts 内部会用到 uploadDir 相关的路径拼接）。
func newTestEnv(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	uploads := filepath.Join(dir, "uploads")
	if err := os.MkdirAll(uploads, 0o755); err != nil {
		t.Fatalf("建 uploads 失败: %v", err)
	}
	s, err := store.Open(context.Background(), testSchemaDSN(t), 4)
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, uploads
}

// writeUpload 在 uploads 下造一个文件，返回相对路径。
func writeUpload(t *testing.T, uploads, rel string) string {
	t.Helper()
	abs := filepath.Join(uploads, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.WriteFile(abs, []byte("x"), 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
	return abs
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// insertOrder 直接插一条订单（绕过完整下单流程，只关心文件与状态）。
func insertOrder(t *testing.T, s *store.Store, userID int64, source, storedRel, printRel, status string) int64 {
	t.Helper()
	var id int64
	err := s.WithTx(context.Background(), false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), `INSERT INTO print_orders (
			user_id, source, batch_id, printer_uri, filename, stored_path, print_path,
			pages, billed_pages, status, created_at, updated_at
		) VALUES ($1, $2, 'B1', 'http://x/', 'a.pdf', $3, $4, 1, 1, $5, $6, $7)
		RETURNING id`,
			userID, source, storedRel, printRel, status,
			time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339)).Scan(&id)
	})
	if err != nil {
		t.Fatalf("插订单失败: %v", err)
	}
	return id
}

func insertUser(t *testing.T, s *store.Store, name string) int64 {
	t.Helper()
	var id int64
	err := s.WithTx(context.Background(), false, func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), `INSERT INTO users
			(username, password_hash, role, protected, contact_name, phone, email,
			 balance_cents, created_at, updated_at)
			VALUES ($1, 'x', 'user', false, '', '', '', 0, $2, $3)
		RETURNING id`,
			name, time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339)).Scan(&id)
	})
	if err != nil {
		t.Fatalf("插用户失败: %v", err)
	}
	return id
}

// TestCleanupOrderArtifactsRemovesTerminalFiles 终态订单的文件应当被回收。
func TestCleanupOrderArtifactsRemovesTerminalFiles(t *testing.T) {
	s, uploads := newTestEnv(t)
	uid := insertUser(t, s, "u1")

	rel := "20260921/a.pdf"
	abs := writeUpload(t, uploads, rel)
	insertOrder(t, s, uid, store.OrderSourceWeb, rel, rel, store.OrderStatusFailed)

	if !exists(abs) {
		t.Fatal("前置条件不成立：文件应当存在")
	}

	removed, err := cleanupOrderArtifacts(context.Background(), s, uploads, "")
	if err != nil {
		t.Fatalf("cleanupOrderArtifacts 报错: %v", err)
	}
	if removed != 1 {
		t.Errorf("删除文件数 = %d，期望 1", removed)
	}
	if exists(abs) {
		t.Error("终态订单的文件没有被删除")
	}

	// 订单行必须保留 —— 它是财务记录。
	var count int
	if err := s.WithTx(context.Background(), true, func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), "SELECT COUNT(1) FROM print_orders").Scan(&count)
	}); err != nil {
		t.Fatalf("查订单失败: %v", err)
	}
	if count != 1 {
		t.Errorf("订单行数 = %d，期望 1（订单是财务记录，永不删除）", count)
	}
}

// TestProtectedRelsCoversActiveOrders 未终结订单的文件必须在保护名单里。
//
// 这是本阶段最要紧的一条：maintenance 按 print_jobs 的保留期删文件，
// 它完全不知道订单的存在 —— 少了这层保护，一笔「已支付、正在打印」的
// 订单其文件会被删掉，用户付了钱却打不出来。
func TestProtectedRelsCoversActiveOrders(t *testing.T) {
	s, uploads := newTestEnv(t)
	uid := insertUser(t, s, "u2")

	activeRels := []string{"20260921/active1.pdf", "20260921/active2.pdf"}
	for _, rel := range activeRels {
		writeUpload(t, uploads, rel)
	}
	// 三种未终结状态各来一条
	insertOrder(t, s, uid, store.OrderSourceWeb, activeRels[0], activeRels[0], store.OrderStatusPaid)
	insertOrder(t, s, uid, store.OrderSourceWeb, activeRels[1], activeRels[1], store.OrderStatusPrinting)
	// 再加一条待支付的，用转换产物路径区别于原文件
	convRel := "20260921/pending.pdf"
	convPrint := convertedRelPath(convRel)
	writeUpload(t, uploads, convRel)
	writeUpload(t, uploads, convPrint)
	insertOrder(t, s, uid, store.OrderSourceWeb, convRel, convPrint, store.OrderStatusPendingPayment)

	protected, err := protectedRels(context.Background(), s)
	if err != nil {
		t.Fatalf("protectedRels 报错: %v", err)
	}

	for _, want := range []string{activeRels[0], activeRels[1], convRel, convPrint} {
		if !protected[want] {
			t.Errorf("未终结订单的路径 %q 不在保护名单里", want)
		}
	}

	// 终态订单不应占用保护名额（它的文件该由 cleanupOrderArtifacts 回收）
	doneRel := "20260921/done.pdf"
	writeUpload(t, uploads, doneRel)
	insertOrder(t, s, uid, store.OrderSourceWeb, doneRel, doneRel, store.OrderStatusFailed)
	protected2, err := protectedRels(context.Background(), s)
	if err != nil {
		t.Fatalf("protectedRels 报错: %v", err)
	}
	if protected2[doneRel] {
		t.Error("终态订单不应当占用保护名额")
	}
}

// TestRemoveUnprotectedFilesRespectsProtection 保护名单内的路径一个都不能删。
func TestRemoveUnprotectedFilesRespectsProtection(t *testing.T) {
	_, uploads := newTestEnv(t)

	freeRel := "20260921/free.pdf"
	protectedRel := "20260921/keep.pdf"
	freeAbs := writeUpload(t, uploads, freeRel)
	protectedAbs := writeUpload(t, uploads, protectedRel)

	protected := map[string]bool{protectedRel: true}
	removeUnprotectedFiles(uploads, freeRel, protected)
	removeUnprotectedFiles(uploads, protectedRel, protected)

	if exists(freeAbs) {
		t.Error("未被保护的文件应当被删除")
	}
	if !exists(protectedAbs) {
		t.Error("受保护的文件被误删了")
	}
}

// TestRemoveUnprotectedFilesSkipsScheduled 定时任务源文件永远不删（多份记录共享）。
func TestRemoveUnprotectedFilesSkipsScheduled(t *testing.T) {
	_, uploads := newTestEnv(t)

	rel := scheduledSubDir + "/1/20260921/src.pdf"
	abs := writeUpload(t, uploads, rel)

	removeUnprotectedFiles(uploads, rel, map[string]bool{})
	if !exists(abs) {
		t.Error("定时任务源文件被误删了 —— 它是共享资源，删了会打断后续触发")
	}
}
