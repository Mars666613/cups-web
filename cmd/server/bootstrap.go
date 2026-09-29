package main

import (
	"context"
	"database/sql"
	"errors"
	"log"

	"cups-web/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func ensureDefaultAdmin(ctx context.Context) error {
	return appStore.WithTx(ctx, false, func(tx *sql.Tx) error {
		user, err := store.GetUserByUsername(ctx, tx, "admin")
		if err == nil {
			if user.Role != store.RoleAdmin || !user.Protected {
				if _, err := tx.ExecContext(ctx, "UPDATE users SET role = ?, protected = 1 WHERE id = ?", store.RoleAdmin, user.ID); err != nil {
					return err
				}
			}
			// 若仍是默认密码，启动时醒目告警，提示尽快修改。
			if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte("admin")) == nil {
				log.Printf("[SECURITY WARNING] admin 账号仍在使用默认密码 admin，请立即登录并修改密码！")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		hash, err := bcrypt.GenerateFromPassword([]byte("admin"), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		if _, err := store.CreateUser(ctx, tx, store.CreateUserInput{
			Username:     "admin",
			PasswordHash: string(hash),
			Role:         store.RoleAdmin,
			Protected:    true,
		}); err != nil {
			return err
		}
		log.Printf("default admin created: admin")
		log.Printf("[SECURITY WARNING] 已创建默认管理员 admin/admin，请立即登录并修改密码！")
		return nil
	})
}
