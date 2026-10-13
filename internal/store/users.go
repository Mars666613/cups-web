package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode"
)

// ErrCardTaken 这张工牌已经绑在别人身上了。
//
// 调用方应当拿它去提示「这张卡已绑定给 XXX」——那是管理员唯一能据此判断
// 「是绑错了还是卡发重了」的信息。只说「绑定失败」会让人以为是系统故障。
var ErrCardTaken = errors.New("store: card already bound to another user")

// NormalizeCardNo 把卡号归一成可以稳定比对的形式。
//
// 绑定与刷卡【两侧都必须过它】—— 这也是它安全的前提。只要规则对称，
// 归一就不会把两张不同的卡弄成一张；而它对下面这些几乎必然发生的书写差异
// 是必需的：
//
//	① 大小写     PC/SC 读卡器（ACR122U 等）吐的是十六进制 UID。管理员
//	             把终端上看到的 UID 抄进管理界面时，`04A23B1C` 与 `04a23b1c`
//	             手抄错一个几乎一定会发生。
//	② 分隔符     同一个 UID 各家写法不同：`04:A2:3B:1C` / `04-A2-3B-1C` / `04A23B1C`。
//
// 🚫 【不要】去掉前导零、不要只留数字 —— 那会改变卡的身份。
// 🚫 也不要指望它能救「两台不同型号读卡器读出不同 UID」：那是物理层差异
//
//	（Wiegand 号 vs 卡面号、字节序不同），归一化救不了，只能靠统一读卡器型号。
func NormalizeCardNo(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case ' ', '\t', '\r', '\n', ':', '-', '_':
			continue
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}

type User struct {
	ID           int64
	Username     string
	PasswordHash string
	Role         string
	Protected    bool
	ContactName  string
	Phone        string
	Email        string
	// AlipayOpenID 是支付宝授权登录拿到的用户唯一标识（open_id，128 字符）。
	// 空串 = 不是支付宝账号。它是身份凭据，建号后不再变动 ——
	// 所以只在 CreateUser 里写，UpdateUser 碰不到它。
	AlipayOpenID string
	// CardNo 是 NFC 工牌号，刷卡取件用。空串 = 没绑卡。
	//
	// ⚠️ 存的是【读卡器实际吐出的字符串】原样，不做任何归一（见 schema.sql
	// 那段注释）。绑定与刷卡必须用同型号读卡器，否则读出来对不上。
	// 与 AlipayOpenID 一样是身份凭据：只能经 SetUserCardNo 改，
	// UpdateUser（管理员改资料）碰不到它。
	CardNo string
	// BalanceCents 是账户余额，单位「分」。只能经 ApplyWalletDelta 变动，
	// 每笔变动都会在 wallet_transactions 留一条流水。
	BalanceCents int64
	CreatedAt    string
	UpdatedAt    string
}

type CreateUserInput struct {
	Username     string
	PasswordHash string
	Role         string
	Protected    bool
	ContactName  string
	Phone        string
	Email        string
	// AlipayOpenID 只有支付宝授权登录那条路会填，其余一律留空。
	AlipayOpenID string
}

type UpdateUserInput struct {
	ID           int64
	Username     string
	PasswordHash *string
	Role         string
	ContactName  string
	Phone        string
	Email        string
}

func CountUsers(ctx context.Context, tx *sql.Tx) (int, error) {
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(1) FROM users").Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// userColumns 是 users 表的列清单，SELECT 与 scanUser 必须与它同序。
//
// 之前这段清单在三个 SELECT 里各内联一份，加一列要改三处 —— 漏一处不会编译
// 失败，只会在运行时「某一列串位」。prints.go / orders.go 早就是共享常量的写法，
// 这里跟上。
const userColumns = `id, username, password_hash, role, protected, contact_name, phone, email,
	alipay_open_id, card_no, balance_cents, created_at, updated_at`

func GetUserByUsername(ctx context.Context, tx *sql.Tx, username string) (User, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE username = $1`, username)
	return scanUser(row)
}

func GetUserByAlipayOpenID(ctx context.Context, tx *sql.Tx, openID string) (User, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE alipay_open_id = $1`, openID)
	return scanUser(row)
}

// GetUserByCardNo 按工牌号找人。刷卡取件这条路上【每一步】都要它。
//
// 比对用的归一形式（NormalizeCardNo）而不是原样字符串 —— 规则与
// SetUserCardNo 完全一致，所以管理员手抄的 UID 能与读卡器吐出来的对上。
//
// 传入空串会直接返回 sql.ErrNoRows（而不是「匹配到某个没绑卡的人」）：
// 空串是「没绑卡」的哨兵，不是一张合法的卡。归一后变空串同理。
func GetUserByCardNo(ctx context.Context, tx *sql.Tx, cardNo string) (User, error) {
	normalized := NormalizeCardNo(cardNo)
	if normalized == "" {
		return User{}, sql.ErrNoRows
	}
	row := tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE card_no = $1`, normalized)
	return scanUser(row)
}

// SetUserCardNo 绑定/解绑工牌。传 "" 即解绑。
//
// 解绑（空串）不参与部分唯一索引，所以必定成功、也不影响别人。
// 绑定时若这张卡已在别人名下，返回 ErrCardTaken —— 调用方据此去查
// GetUserByCardNo 拿到占用者的用户名，提示管理员。
//
// ⚠️ 这里【不】做「先查再改」的两段式：查与改之间插进另一个绑定就会双写。
// 靠唯一索引拒绝，再把 23505 翻译成哨兵错误，才是原子的。
func SetUserCardNo(ctx context.Context, tx *sql.Tx, userID int64, cardNo string) error {
	// 存归一形式，与 GetUserByCardNo 的查法对称 —— 这是手抄 UID 能对上的前提。
	res, err := tx.ExecContext(ctx,
		`UPDATE users SET card_no = $1, updated_at = $2 WHERE id = $3`,
		NormalizeCardNo(cardNo), nowUTC(), userID)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrCardTaken
		}
		return err
	}
	// ⚠️ 必须检查影响行数。用户不存在时 UPDATE 影响 0 行且【不报错】，
	// 调用方会收到「绑定成功」而实际上什么都没写进去 ——
	// 管理员以为绑好了，员工刷卡却提示「工牌未登记」，两边都查不出所以然。
	affected, err := res.RowsAffected()
	if err == nil && affected == 0 {
		return sql.ErrNoRows
	}
	return err
}

func GetUserByID(ctx context.Context, tx *sql.Tx, id int64) (User, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id)
	return scanUser(row)
}

// UserFilter 是用户列表的筛选条件。零值 = 不过滤。
type UserFilter struct {
	// Username 按登录名做【子串】匹配（不区分大小写）。
	//
	// 用子串而不是精确匹配，因为这张列表的典型用法是"管理员手里有个用户名，
	// 要把那个人找出来"：顾客报过来的是完整的 `alipay:8F3K2Q`，粘进去能命中；
	// 只打一半 `8F3K` 也能命中，省得管理员去记前缀。
	Username string
}

func ListUsers(ctx context.Context, tx *sql.Tx, filter UserFilter) ([]User, error) {
	query := `SELECT ` + userColumns + ` FROM users`
	var args []interface{}
	if filter.Username != "" {
		// ⚠️ 用 position() 而不是 LIKE：LIKE 里的 % 和 _ 是通配符，管理员在搜索框
		// 里打一个 % 就会"查出所有人"，而反斜杠转义特别容易被漏掉。
		// position(x in y) 就是纯子串查找，没有这层语义。
		query += ` WHERE position(lower($1) in lower(username)) > 0`
		args = append(args, filter.Username)
	}
	query += ` ORDER BY id`

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	users := []User{}
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func CreateUser(ctx context.Context, tx *sql.Tx, input CreateUserInput) (User, error) {
	now := nowUTC()
	// ⚠️ RETURNING id 是必须的：PostgreSQL 没有 LastInsertId，
	// pgx 会在调用时直接报错。所有自增主键的 INSERT 都得这么写。
	var id int64
	err := tx.QueryRowContext(ctx, `INSERT INTO users (
		username, password_hash, role, protected, contact_name, phone, email, alipay_open_id,
		created_at, updated_at
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
		input.Username, input.PasswordHash, input.Role, input.Protected, input.ContactName, input.Phone, input.Email, input.AlipayOpenID,
		now, now,
	).Scan(&id)
	if err != nil {
		return User{}, err
	}
	return GetUserByID(ctx, tx, id)
}

func UpdateUser(ctx context.Context, tx *sql.Tx, input UpdateUserInput) (User, error) {
	now := nowUTC()
	if input.PasswordHash != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET
			username = $1, password_hash = $2, role = $3, contact_name = $4, phone = $5, email = $6,
			updated_at = $7
			WHERE id = $8`,
			input.Username, *input.PasswordHash, input.Role, input.ContactName, input.Phone, input.Email,
			now, input.ID,
		); err != nil {
			return User{}, err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET
			username = $1, role = $2, contact_name = $3, phone = $4, email = $5,
			updated_at = $6
			WHERE id = $7`,
			input.Username, input.Role, input.ContactName, input.Phone, input.Email,
			now, input.ID,
		); err != nil {
			return User{}, err
		}
	}
	return GetUserByID(ctx, tx, input.ID)
}

func DeleteUser(ctx context.Context, tx *sql.Tx, id int64) error {
	res, err := tx.ExecContext(ctx, "DELETE FROM users WHERE id = $1", id)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err == nil && affected == 0 {
		return sql.ErrNoRows
	}
	return err
}

type scanner interface {
	Scan(dest ...interface{}) error
}

func scanUser(s scanner) (User, error) {
	var user User
	err := s.Scan(
		&user.ID, &user.Username, &user.PasswordHash, &user.Role, &user.Protected, &user.ContactName, &user.Phone, &user.Email,
		&user.AlipayOpenID, &user.CardNo, &user.BalanceCents, &user.CreatedAt, &user.UpdatedAt,
	)
	return user, err
}
