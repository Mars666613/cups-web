package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cups-web/internal/store"
)

func startMaintenance(s *store.Store, uploads string) {
	go func() {
		for {
			if err := cleanupOldPrints(context.Background(), s, uploads, time.Now()); err != nil {
				log.Println("cleanup failed:", err)
			}
			time.Sleep(1 * time.Hour)
		}
	}()
}

func cleanupAllPrints(ctx context.Context, s *store.Store, uploads string) (int, error) {
	// 拿不到保护名单就【取消本次清理】。宁可留着孤儿文件，
	// 也不能删掉用户已付费、正在等待打印的订单文件。
	protected, err := protectedRels(ctx, s)
	if err != nil {
		return 0, fmt.Errorf("加载文件保护名单失败，取消本次清理: %w", err)
	}

	var paths []string
	err = s.WithTx(ctx, false, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT stored_path FROM print_jobs")
		if err != nil {
			return err
		}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				return err
			}
			paths = append(paths, p)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		_, err = tx.ExecContext(ctx, "DELETE FROM print_jobs")
		return err
	})
	if err != nil {
		return 0, err
	}

	for _, rel := range paths {
		// 保护定时任务源文件与未终结订单的文件；"清空全部打印历史"
		// 只清 print_jobs 表与不再被订单持有的普通上传。
		removeUnprotectedFiles(uploads, rel, protected)
	}

	// 终态订单的文件也一并回收：save_history=0 的部署不写 print_jobs，
	// 那些订单的产物不会被上面的循环扫到，会永久残留。订单行保留。
	orderRemoved, oerr := cleanupOrderArtifacts(ctx, s, uploads, "")
	if oerr != nil {
		log.Printf("[maintenance] 回收终态订单文件失败: %v", oerr)
	}

	if len(paths) > 0 || orderRemoved > 0 {
		if _, err := s.DB.ExecContext(ctx, "VACUUM"); err != nil {
			return len(paths), err
		}
		if _, err := s.DB.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			return len(paths), err
		}
	}
	return len(paths), nil
}

func cleanupOldPrints(ctx context.Context, s *store.Store, uploads string, now time.Time) error {
	var retentionDays int64
	err := s.WithTx(ctx, true, func(tx *sql.Tx) error {
		val, err := store.GetSettingInt(ctx, tx, store.SettingRetentionDays, 0)
		if err != nil {
			return err
		}
		retentionDays = val
		return nil
	})
	if err != nil {
		return err
	}
	if retentionDays <= 0 {
		return nil
	}

	cutoff := now.AddDate(0, 0, -int(retentionDays)).UTC().Format(time.RFC3339)

	// 拿不到保护名单就【整轮跳过】。宁可留着孤儿文件，也不能删掉
	// 用户已付费、正在等待打印的订单文件。
	protected, err := protectedRels(ctx, s)
	if err != nil {
		return fmt.Errorf("加载文件保护名单失败，跳过本轮清理: %w", err)
	}

	var paths []string
	err = s.WithTx(ctx, false, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT stored_path FROM print_jobs WHERE created_at < ?", cutoff)
		if err != nil {
			return err
		}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				return err
			}
			paths = append(paths, p)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		_, err = tx.ExecContext(ctx, "DELETE FROM print_jobs WHERE created_at < ?", cutoff)
		return err
	})
	if err != nil {
		return err
	}

	for _, rel := range paths {
		// scheduled/ 前缀的文件属于定时任务源文件（issue #28），是共享资源：
		// print_jobs 里可能有很多条历史指向同一份源文件，删掉会打断后续触发。
		// 这里连同「未终结订单持有的文件」一起由 removeUnprotectedFiles 跳过；
		// 定时任务的清理由 DeleteScheduledPrint 负责。
		removeUnprotectedFiles(uploads, rel, protected)
	}

	// 终态订单的文件一并回收（订单行是财务记录，保留）。
	if _, oerr := cleanupOrderArtifacts(ctx, s, uploads, cutoff); oerr != nil {
		log.Printf("[maintenance] 回收终态订单文件失败: %v", oerr)
	}

	if len(paths) > 0 {
		if _, err := s.DB.ExecContext(ctx, "VACUUM"); err != nil {
			return err
		}
		if _, err := s.DB.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			return err
		}
	}
	return nil
}

// isScheduledRel 判断一个 stored_path 是否指向定时任务的源文件。
// 前缀与 scheduledSubDir 保持一致，全路径归一化为斜杠再判断，避免跨平台差异。
func isScheduledRel(rel string) bool {
	if rel == "" {
		return false
	}
	rel = filepath.ToSlash(rel)
	return rel == scheduledSubDir ||
		strings.HasPrefix(rel, scheduledSubDir+"/")
}

// protectedRels 一次性返回所有【仍需保护】的文件路径（归一化为斜杠）。
//
// 目前有两类：
//  1. 定时任务源文件 —— 由 isScheduledRel 判断，多份记录共享，删了会打断后续触发
//  2. 未终结订单持有的文件（pending_payment / paid / printing）
//
// 第 2 类的必要性：本函数所在的两个清理入口都是按 print_jobs 的保留期删文件的，
// 而它们【完全不知道订单的存在】。一笔「已支付、正在打印」的订单，其文件
// 很可能因为对应的 print_jobs 行过期而被删掉 —— 用户付了钱却打不出来。
//
// 把两类判断集中在一处，将来新增第三种「共享文件」场景时只有这一个改动点。
func protectedRels(ctx context.Context, s *store.Store) (map[string]bool, error) {
	protected := map[string]bool{}
	err := s.WithTx(ctx, true, func(tx *sql.Tx) error {
		refs, err := store.ListActiveOrderFiles(ctx, tx)
		if err != nil {
			return err
		}
		for _, r := range refs {
			if r.StoredPath != "" {
				protected[filepath.ToSlash(r.StoredPath)] = true
			}
			if r.PrintPath != "" {
				protected[filepath.ToSlash(r.PrintPath)] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return protected, nil
}

// removeUnprotectedFiles 删除一条 stored_path 及其转换产物，跳过受保护的路径。
//
// 两个清理入口（按保留期 / 清空全部）共用它，同时也消除了它们之间
// 原本各自一份的删除逻辑。
func removeUnprotectedFiles(uploads string, rel string, protected map[string]bool) {
	rel = filepath.ToSlash(rel)
	if isScheduledRel(rel) {
		return
	}
	if !protected[rel] {
		removeFileLogged(uploads, rel)
	}
	// 转换产物单独判断：订单的 print_path 有可能正好是它，
	// 而 stored_path 不在保护名单里（例如订单只持有转换后的版本）。
	if cRel := filepath.ToSlash(convertedRelPath(rel)); cRel != "" && !protected[cRel] {
		removeFileLogged(uploads, cRel)
	}
}

// cleanupOrderArtifacts 回收【终态】订单持有的文件，返回删除的文件数。
//
// 订单行本身是财务记录，永不删除；这里只回收文件。
// 必须做这一步的原因：save_history=0 的部署不写 print_jobs，
// 那些订单的产物不会被上面按 print_jobs 的清理扫到，会永久残留在 uploads 里。
//
// cutoff 为空表示「全部终态订单」（管理员点「立即清理」时）。
func cleanupOrderArtifacts(ctx context.Context, s *store.Store, uploads, cutoff string) (int, error) {
	var refs []store.OrderFileRef
	err := s.WithTx(ctx, true, func(tx *sql.Tx) error {
		var err error
		refs, err = store.ListTerminalOrderFiles(ctx, tx, cutoff)
		return err
	})
	if err != nil {
		return 0, err
	}

	removed := 0
	for _, r := range refs {
		// 定时任务源文件是共享资源，绝不删 —— 与上面两处清理同一个理由。
		if r.Source == store.OrderSourceScheduled || isScheduledRel(r.StoredPath) {
			continue
		}
		removed += removeFileLogged(uploads, r.StoredPath)
		if r.PrintPath != r.StoredPath {
			removed += removeFileLogged(uploads, r.PrintPath)
		}
	}
	return removed, nil
}

// removeFileLogged 删除一个文件，失败时重试并在最终失败时记日志。
//
// 🚫 不要退回成 `_ = os.Remove(...)`：文件删不掉是最难发现的一类问题 ——
// 磁盘慢慢涨满，而所有日志都显示清理"成功"。实测就撞到过：送印 goroutine
// 收尾（走退款事务）时文件句柄尚未关闭，此刻删除在 Windows 上直接失败。
// 生产是 Linux + 每小时清理，撞上的概率极低，但重试的代价可以忽略。
//
// 已不存在的文件不算失败：重复清理是正常操作。
func removeFileLogged(uploads, rel string) int {
	if rel == "" {
		return 0
	}
	abs := filepath.Join(uploads, filepath.FromSlash(rel))

	const attempts = 3
	var err error
	for i := 0; i < attempts; i++ {
		err = os.Remove(abs)
		if err == nil {
			return 1
		}
		if os.IsNotExist(err) {
			return 0
		}
		if i < attempts-1 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	log.Printf("[maintenance] ⚠️ 删除文件失败 %s: %v", abs, err)
	return 0
}
