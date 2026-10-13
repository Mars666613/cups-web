package store

import (
	"context"
	"database/sql"
	"strconv"
)

// ⚠️ 下面两条 upsert 必须用 DO UPDATE，不能写成 DO NOTHING。
//
// 这里原先写的是 SQLite 的 `INSERT OR REPLACE`，语义是「有则覆盖」。
// 当成 `ON CONFLICT (key) DO NOTHING` 翻译过来的话，设置项会**静默存不进去** ——
// 管理员在界面上改完点保存，看着一切正常，刷新一下就回到旧值，且没有任何报错。
// 这是本次迁移里最容易顺手写错的一处。
const upsertSettingSQL = `INSERT INTO settings(key, value) VALUES ($1, $2)
	ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`

func GetSettingInt(ctx context.Context, tx *sql.Tx, key string, defaultVal int64) (int64, error) {
	var value string
	err := tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = $1", key).Scan(&value)
	if err == sql.ErrNoRows {
		return defaultVal, nil
	}
	if err != nil {
		return 0, err
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, err
	}
	return parsed, nil
}

func SetSettingInt(ctx context.Context, tx *sql.Tx, key string, value int64) error {
	_, err := tx.ExecContext(ctx, upsertSettingSQL, key, strconv.FormatInt(value, 10))
	return err
}

func GetSettingString(ctx context.Context, tx *sql.Tx, key string, defaultVal string) (string, error) {
	var value string
	err := tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = $1", key).Scan(&value)
	if err == sql.ErrNoRows {
		return defaultVal, nil
	}
	if err != nil {
		return "", err
	}
	return value, nil
}

func SetSettingString(ctx context.Context, tx *sql.Tx, key string, value string) error {
	_, err := tx.ExecContext(ctx, upsertSettingSQL, key, value)
	return err
}
