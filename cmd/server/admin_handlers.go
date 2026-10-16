package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"golang.org/x/crypto/bcrypt"

	"cups-web/internal/auth"
	"cups-web/internal/store"
)

var (
	errDeleteDefaultAdmin = errors.New("default admin cannot be deleted")
	errProtectedRole      = errors.New("protected admin role cannot change")
	errAdminRename        = errors.New("admin username cannot change")
	errDeleteGuest        = errors.New("guest user cannot be deleted")
	errGuestModify        = errors.New("guest user is managed automatically")
	// alipay: 前缀是支付宝授权登录自动建号的命名空间。改名【进】这个空间会让
	// 管理端把普通账号误显示成"支付宝顾客"，所以挡住；改出去不挡。
	errAlipayNamespace = errors.New("alipay: prefix is reserved for wechat login accounts")
	// 财务记录守卫：删除用户会级联抹掉他的账本与订单。
	errUserHasBalance      = errors.New("user still has balance")
	errUserHasTransactions = errors.New("user has wallet transactions")
)

type adminUserPayload struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	Role        string `json:"role"`
	ContactName string `json:"contactName"`
	Phone       string `json:"phone"`
	Email       string `json:"email"`
}

type adminUserResponse struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	Role        string `json:"role"`
	Protected   bool   `json:"protected"`
	ContactName string `json:"contactName"`
	Phone       string `json:"phone"`
	Email       string `json:"email"`
	// IsAlipay 为真表示这是支付宝授权登录自动创建的账号。
	// 它的 username 是一长串 alipay:<open_id>，直接铺在表格里没法看 ——
	// 前端据此显示成「支付宝顾客」。
	IsAlipay bool `json:"isAlipay"`
	// CardNo 是绑定的 NFC 工牌号，'' = 没绑。刷卡取件模式下它就是员工的取件凭据。
	//
	// 原样返回（不截断、不打码）：管理员要能核对「这张卡绑对了没有」，
	// 而且卡号本身就是读卡器读出来的任意字符串，打码反而看不出格式对不对。
	// 前端负责把它显示的短一些（像用户名那样）。
	CardNo string `json:"cardNo"`
	// 单位「分」，前端用 formatCents 换算展示。
	// 只读：余额的变动一律走 /api/admin/users/{id}/balance，那里会写审计流水。
	BalanceCents int64  `json:"balanceCents"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
}

type settingsPayload struct {
	RetentionDays  *int64  `json:"retentionDays"`
	SaveHistory    *bool   `json:"saveHistory"`
	MaxPagesPerJob *int64  `json:"maxPagesPerJob"`
	MaxUploadBytes *int64  `json:"maxUploadBytes"`
	CustomCSS      *string `json:"customCss"`
	GuestMode      *bool   `json:"guestMode"`
	// 待支付订单的有效期。到期后订单连同其上传文件会被后台回收，
	// 所以它同时是「用户可以在支付页停留多久」。
	OrderTTLMinutes *int64 `json:"orderTtlMinutes"`
	// ── 刷卡取件 ──
	// PullModeEnabled 与计费互斥（反方向在 PUT /api/admin/prices 里校验）。
	PullModeEnabled *bool `json:"pullModeEnabled"`
	// PullTTLHours 是「员工有多久来取纸」。它与 retention_days 是两回事：
	// 后者按打印记录的时间删文件，前者按取件时限。
	PullTTLHours *int64 `json:"pullTtlHours"`

	// ── 设备异常告警 ──
	// AlertEnabled 是【总开关】：关掉等于不巡检、不记事件、不通知。
	AlertEnabled *bool `json:"alertEnabled"`
	// AlertWebhookURL 为空表示只记事件不外发（管理端页面上仍然看得到）。
	AlertWebhookURL *string `json:"alertWebhookUrl"`
	// AlertWebhookKind 取 auto / wechat / dingtalk / feishu / generic。
	AlertWebhookKind *string `json:"alertWebhookKind"`
	// AlertRepeatMinutes 是同一异常持续存在时的重发间隔；0 = 只提醒一次。
	AlertRepeatMinutes *int64 `json:"alertRepeatMinutes"`
	// AlertSupplyLowPercent 是「耗材不足」的判定阈值（百分比）。
	AlertSupplyLowPercent *int64 `json:"alertSupplyLowPercent"`
}

// customCSSMaxLen 是自定义 CSS 的字节上限（32 KiB）。
// 管理员错手贴入巨量数据（比如把整个 tailwind.css 复制进来）会拖累每次登录页
// 的公开设置接口，也会撑大 SQLite 单行，这里给一个宽松但不无限的上限。
const customCSSMaxLen = 32 * 1024

func adminListUsersHandler(w http.ResponseWriter, r *http.Request) {
	var resp []adminUserResponse
	err := appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
		users, err := store.ListUsers(r.Context(), tx, store.UserFilter{
			Username: strings.TrimSpace(r.URL.Query().Get("username")),
		})
		if err != nil {
			return err
		}
		resp = mapAdminUsers(users)
		return nil
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list users")
		return
	}
	writeJSON(w, resp)
}

func adminCreateUserHandler(w http.ResponseWriter, r *http.Request) {
	var payload adminUserPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	payload.Username = strings.TrimSpace(payload.Username)
	if payload.Username == "" || payload.Password == "" {
		writeJSONError(w, http.StatusBadRequest, "username and password required")
		return
	}
	// guest 是访客模式（Issue #55）保留账号名，禁止管理员手工新建同名普通账号，
	// 否则会与 SessionHandler 的惰性创建路径分裂成两份语义不同的 guest。
	if strings.EqualFold(payload.Username, guestUsername) {
		writeJSONError(w, http.StatusBadRequest, "guest 是访客模式保留账号")
		return
	}
	// alipay: 前缀是支付宝授权登录自动建号的命名空间（见 loadOrCreateAlipayUser），
	// 保留出来 —— 管理员手工建一个同前缀的账号，会让「哪些是支付宝顾客」这个
	// 判断（以及登录接口对它的拦截）失准。
	if strings.HasPrefix(payload.Username, alipayUsernamePrefix) {
		writeJSONError(w, http.StatusBadRequest, "alipay: 前缀是支付宝授权登录保留的命名空间")
		return
	}
	role := normalizeRole(payload.Role)
	if role == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid role")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(payload.Password), bcrypt.DefaultCost)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to hash password")
		return
	}

	var created store.User
	err = appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
		user, err := store.CreateUser(r.Context(), tx, store.CreateUserInput{
			Username:     payload.Username,
			PasswordHash: string(hash),
			Role:         role,
			Protected:    false,
			ContactName:  payload.ContactName,
			Phone:        payload.Phone,
			Email:        payload.Email,
		})
		if err != nil {
			return err
		}
		created = user
		// 「这个账号是谁建的」——多个管理员时，这是排查「怎么多了一个 admin」
		// 的第一条线索。摘要里不写口令，一个字都不写。
		return recordAudit(r.Context(), tx, sessionUsername(r), store.AuditUserCreate,
			fmt.Sprintf("user#%d", user.ID),
			fmt.Sprintf("新建用户 %s（角色 %s）", user.Username, roleText(role)))
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to create user")
		return
	}
	writeJSON(w, mapAdminUser(created))
}

func adminUpdateUserHandler(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	var payload adminUserPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	payload.Username = strings.TrimSpace(payload.Username)
	if payload.Username == "" {
		writeJSONError(w, http.StatusBadRequest, "username required")
		return
	}
	role := normalizeRole(payload.Role)
	if role == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid role")
		return
	}

	var pwdHash *string
	if strings.TrimSpace(payload.Password) != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(payload.Password), bcrypt.DefaultCost)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to hash password")
			return
		}
		h := string(hash)
		pwdHash = &h
	}

	var updated store.User
	err = appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
		current, err := store.GetUserByID(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if current.Username == "admin" && payload.Username != "admin" {
			return errAdminRename
		}
		if current.Username == "admin" && role != store.RoleAdmin {
			return errProtectedRole
		}
		if current.Username == "admin" {
			role = store.RoleAdmin
		}
		// guest 是访客模式（Issue #55）用的托管账号，不能改用户名/角色/密码——
		// 让管理员误升为 admin 就等于给公网开了后台，改密码则打破「访客登录不走口令」的
		// 前提。要停用直接关掉访客模式开关。
		if current.Username == guestUsername {
			return errGuestModify
		}
		// 与新建同一条规则：不许把账号改名【进】 alipay: 命名空间 —— 那会让一个
		// 普通账号在管理端被误显示成"支付宝顾客"。
		// 改出去是允许的：支付宝身份存在 alipay_open_id 列里，改名不影响登录。
		if payload.Username != current.Username &&
			strings.HasPrefix(payload.Username, alipayUsernamePrefix) {
			return errAlipayNamespace
		}

		user, err := store.UpdateUser(r.Context(), tx, store.UpdateUserInput{
			ID:           id,
			Username:     payload.Username,
			PasswordHash: pwdHash,
			Role:         role,
			ContactName:  payload.ContactName,
			Phone:        payload.Phone,
			Email:        payload.Email,
		})
		if err != nil {
			return err
		}
		updated = user

		// 审计的是【变更项】，不是整份表单。前端每次提交都会带上全部字段，
		// 若照单记下来，「改了电话」和「什么都没改」在审计里长得一模一样。
		//
		// ⚠️ 口令只记「重置过」，绝不记内容，也不记哈希 —— 审计表是要给人
		// 看的，而哈希泄露等于把离线爆破的时间成本白送出去。
		changes := []auditChange{}
		if user.Username != current.Username {
			changes = append(changes, auditChange{
				Field: "username", Text: fmt.Sprintf("用户名 %s → %s", current.Username, user.Username),
				Before: current.Username, After: user.Username,
			})
		}
		if user.Role != current.Role {
			changes = append(changes, auditChange{
				Field: "role", Text: fmt.Sprintf("角色 %s → %s", roleText(current.Role), roleText(user.Role)),
				Before: current.Role, After: user.Role,
			})
		}
		if pwdHash != nil {
			changes = append(changes, auditChange{
				Field: "passwordReset", Text: "重置了登录密码", Before: false, After: true,
			})
		}
		for _, c := range []struct {
			field, label, before, after string
		}{
			{"contactName", "联系人", current.ContactName, user.ContactName},
			{"phone", "手机号", current.Phone, user.Phone},
			{"email", "邮箱", current.Email, user.Email},
		} {
			if c.before != c.after {
				changes = append(changes, auditChange{
					Field: c.field, Text: fmt.Sprintf("%s %s → %s", c.label, dashIfEmpty(c.before), dashIfEmpty(c.after)),
					Before: c.before, After: c.after,
				})
			}
		}
		return recordAudit(r.Context(), tx, sessionUsername(r), store.AuditUserUpdate,
			fmt.Sprintf("user#%d", id), "", changes...)
	})
	if err != nil {
		if errors.Is(err, errAdminRename) {
			writeJSONError(w, http.StatusBadRequest, errAdminRename.Error())
			return
		}
		if errors.Is(err, errProtectedRole) {
			writeJSONError(w, http.StatusBadRequest, "admin role cannot change")
			return
		}
		if errors.Is(err, errGuestModify) {
			writeJSONError(w, http.StatusBadRequest, "guest 账号由访客模式自动管理，不能编辑")
			return
		}
		if errors.Is(err, errAlipayNamespace) {
			writeJSONError(w, http.StatusBadRequest, "alipay: 前缀是支付宝授权登录保留的命名空间")
			return
		}
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "user not found")
		} else {
			writeJSONError(w, http.StatusInternalServerError, "failed to update user")
		}
		return
	}
	writeJSON(w, mapAdminUser(updated))
}

func adminDeleteUserHandler(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDParam(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	sess, _ := auth.GetSession(r)
	if sess.UserID == id {
		writeJSONError(w, http.StatusBadRequest, "cannot delete current user")
		return
	}
	err = appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
		user, err := store.GetUserByID(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if user.Username == "admin" {
			return errDeleteDefaultAdmin
		}
		if user.Username == guestUsername {
			return errDeleteGuest
		}
		// 财务记录守卫：users 的删除对 wallet_transactions 与 print_orders
		// 都是 ON DELETE CASCADE —— 删用户会把他的账本与订单一起抹掉，
		// 而那是【审计记录】，删了就再也查不回来，也没法对账。
		//
		// 余额不为 0 或有任何流水的用户一律拒绝：管理员应当先手工清零余额、
		// 确认过账目之后再删。这不是麻烦，是账目完整性的底线。
		if user.BalanceCents != 0 {
			return errUserHasBalance
		}
		hasTx, err := store.HasWalletTransactions(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if hasTx {
			return errUserHasTransactions
		}
		// 先记审计再删（同一个事务，顺序不影响结果）：此刻 user 还在手上，
		// 用户名与余额是「这个人是谁」的最后一份证据。
		if err := recordAudit(r.Context(), tx, sessionUsername(r), store.AuditUserDelete,
			fmt.Sprintf("user#%d", id),
			fmt.Sprintf("删除用户 %s（角色 %s，余额 %d 分）", user.Username, roleText(user.Role), user.BalanceCents)); err != nil {
			return err
		}
		return store.DeleteUser(r.Context(), tx, id)
	})
	if err != nil {
		if errors.Is(err, errDeleteDefaultAdmin) {
			writeJSONError(w, http.StatusBadRequest, "admin cannot be deleted")
			return
		}
		if errors.Is(err, errDeleteGuest) {
			writeJSONError(w, http.StatusBadRequest, "guest 账号由访客模式自动管理，请通过设置关闭访客模式")
			return
		}
		// 财务记录守卫：提示要具体到「该怎么做」，否则管理员只会反复点删除。
		if errors.Is(err, errUserHasBalance) {
			writeJSONError(w, http.StatusBadRequest,
				"该用户账户仍有余额，请先在上方「余额」处调整为 0 再删除")
			return
		}
		if errors.Is(err, errUserHasTransactions) {
			writeJSONError(w, http.StatusBadRequest,
				"该用户有余额流水（对账依据），删除会一并抹掉账本。如确需删除，请先手工留档")
			return
		}
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "user not found")
		} else {
			writeJSONError(w, http.StatusInternalServerError, "failed to delete user")
		}
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// settingsSnapshot 是系统设置的完整快照。
//
// 抽出来是为了让【同一组字段】有两个用途时只有一份读取代码：GET 的响应，
// 以及 PUT 的「改动前 / 改动后」对比。原先 GET 里是一串逐个 GetSettingInt，
// 若审计再写一份，将来新增设置项必然漏掉一处 —— 而漏在审计那一侧的症状
// 是「界面上能改、审计里看不到」，几乎不会被发现。
type settingsSnapshot struct {
	RetentionDays   int64
	SaveHistory     bool
	MaxPagesPerJob  int64
	MaxUploadBytes  int64
	CustomCSS       string
	GuestMode       bool
	OrderTTLMinutes int64
	PullModeEnabled bool
	PullTTLHours    int64

	// 设备异常告警（见 cmd/server/device_alert.go）。
	AlertEnabled          bool
	AlertWebhookURL       string
	AlertWebhookKind      string
	AlertRepeatMinutes    int64
	AlertSupplyLowPercent int64
}

func loadSettingsSnapshot(ctx context.Context, tx *sql.Tx) (settingsSnapshot, error) {
	var s settingsSnapshot
	var err error

	if s.RetentionDays, err = store.GetSettingInt(ctx, tx, store.SettingRetentionDays, 0); err != nil {
		return s, err
	}
	saveHistory, err := store.GetSettingInt(ctx, tx, store.SettingSaveHistory, 1)
	if err != nil {
		return s, err
	}
	s.SaveHistory = saveHistory != 0

	if s.MaxPagesPerJob, err = store.GetSettingInt(ctx, tx, store.SettingMaxPagesPerJob, 0); err != nil {
		return s, err
	}
	if s.MaxUploadBytes, err = store.GetSettingInt(ctx, tx, store.SettingMaxUploadBytes, 0); err != nil {
		return s, err
	}
	if s.CustomCSS, err = store.GetSettingString(ctx, tx, store.SettingCustomCSS, ""); err != nil {
		return s, err
	}
	guestMode, err := store.GetSettingInt(ctx, tx, store.SettingGuestMode, 0)
	if err != nil {
		return s, err
	}
	s.GuestMode = guestMode != 0

	if s.OrderTTLMinutes, err = store.GetSettingInt(ctx, tx, store.SettingOrderTTLMinutes, 15); err != nil {
		return s, err
	}
	pullMode, err := store.GetSettingInt(ctx, tx, store.SettingPullModeEnabled, 0)
	if err != nil {
		return s, err
	}
	s.PullModeEnabled = pullMode != 0

	if s.PullTTLHours, err = store.GetSettingInt(ctx, tx, store.SettingPullTTLHours, 72); err != nil {
		return s, err
	}

	alertEnabled, err := store.GetSettingInt(ctx, tx, store.SettingAlertEnabled, 0)
	if err != nil {
		return s, err
	}
	s.AlertEnabled = alertEnabled != 0

	if s.AlertWebhookURL, err = store.GetSettingString(ctx, tx, store.SettingAlertWebhookURL, ""); err != nil {
		return s, err
	}
	if s.AlertWebhookKind, err = store.GetSettingString(ctx, tx, store.SettingAlertWebhookKind, "auto"); err != nil {
		return s, err
	}
	if s.AlertRepeatMinutes, err = store.GetSettingInt(ctx, tx, store.SettingAlertRepeatMinutes, 60); err != nil {
		return s, err
	}
	if s.AlertSupplyLowPercent, err = store.GetSettingInt(ctx, tx, store.SettingAlertSupplyLowPercent, 10); err != nil {
		return s, err
	}
	return s, nil
}

// settingsChanges 比较前后两个快照，返回变更项。
//
// 中文标签与单位写在这里（而不是做成通用 diff）：只有这里知道
// max_upload_bytes 要显示成 MB、orderTtlMinutes 的单位是分钟。
func settingsChanges(before, after settingsSnapshot) []auditChange {
	var out []auditChange
	add := func(field, text string, b, a interface{}) {
		out = append(out, auditChange{Field: field, Text: text, Before: b, After: a})
	}

	if before.RetentionDays != after.RetentionDays {
		add("retentionDays", fmt.Sprintf("自动清理 %s → %s",
			retentionText(before.RetentionDays), retentionText(after.RetentionDays)),
			before.RetentionDays, after.RetentionDays)
	}
	if before.SaveHistory != after.SaveHistory {
		add("saveHistory", fmt.Sprintf("保存打印历史 %s → %s",
			onOff(before.SaveHistory), onOff(after.SaveHistory)),
			before.SaveHistory, after.SaveHistory)
	}
	if before.MaxPagesPerJob != after.MaxPagesPerJob {
		add("maxPagesPerJob", fmt.Sprintf("单任务最大页数 %s → %s",
			limitText(before.MaxPagesPerJob), limitText(after.MaxPagesPerJob)),
			before.MaxPagesPerJob, after.MaxPagesPerJob)
	}
	if before.MaxUploadBytes != after.MaxUploadBytes {
		add("maxUploadBytes", fmt.Sprintf("单文件最大 %s → %s",
			uploadLimitText(before.MaxUploadBytes), uploadLimitText(after.MaxUploadBytes)),
			before.MaxUploadBytes, after.MaxUploadBytes)
	}
	// ⚠️ 自定义 CSS 只记【字节数】，不记内容。它上限 32KB，整段塞进审计行会
	// 把列表页撑爆；而管理员真正要确认的是「有人动过它」。需要内容时去库里
	// 看 settings 现值 —— 审计回答的是「什么时候变的」，不是「变成长什么样」。
	if before.CustomCSS != after.CustomCSS {
		add("customCssBytes", fmt.Sprintf("自定义 CSS %d → %d 字节",
			len(before.CustomCSS), len(after.CustomCSS)),
			len(before.CustomCSS), len(after.CustomCSS))
	}
	if before.GuestMode != after.GuestMode {
		add("guestMode", fmt.Sprintf("访客模式 %s → %s",
			onOff(before.GuestMode), onOff(after.GuestMode)),
			before.GuestMode, after.GuestMode)
	}
	if before.OrderTTLMinutes != after.OrderTTLMinutes {
		add("orderTtlMinutes", fmt.Sprintf("订单支付有效期 %d → %d 分钟",
			before.OrderTTLMinutes, after.OrderTTLMinutes),
			before.OrderTTLMinutes, after.OrderTTLMinutes)
	}
	if before.PullModeEnabled != after.PullModeEnabled {
		add("pullModeEnabled", fmt.Sprintf("刷卡取件 %s → %s",
			onOff(before.PullModeEnabled), onOff(after.PullModeEnabled)),
			before.PullModeEnabled, after.PullModeEnabled)
	}
	if before.PullTTLHours != after.PullTTLHours {
		add("pullTtlHours", fmt.Sprintf("取件时限 %d → %d 小时",
			before.PullTTLHours, after.PullTTLHours),
			before.PullTTLHours, after.PullTTLHours)
	}
	if before.AlertEnabled != after.AlertEnabled {
		add("alertEnabled", fmt.Sprintf("设备告警 %s → %s",
			onOff(before.AlertEnabled), onOff(after.AlertEnabled)),
			before.AlertEnabled, after.AlertEnabled)
	}
	// ⚠️ webhook 地址里通常带 token（企业微信机器人的 key、钉钉的 access_token），
	// 也就是【等同于口令】。审计里只记主机名，不记完整 URL —— 与「用户口令只记
	// 重置过、不记内容」同一条理由。要看完整值就去设置页（管理员本来就能看到）。
	if before.AlertWebhookURL != after.AlertWebhookURL {
		add("alertWebhookHost", fmt.Sprintf("告警通知地址 %s → %s",
			webhookHostText(before.AlertWebhookURL), webhookHostText(after.AlertWebhookURL)),
			webhookHostText(before.AlertWebhookURL), webhookHostText(after.AlertWebhookURL))
	}
	if before.AlertWebhookKind != after.AlertWebhookKind {
		add("alertWebhookKind", fmt.Sprintf("通知渠道 %s → %s",
			before.AlertWebhookKind, after.AlertWebhookKind),
			before.AlertWebhookKind, after.AlertWebhookKind)
	}
	if before.AlertRepeatMinutes != after.AlertRepeatMinutes {
		add("alertRepeatMinutes", fmt.Sprintf("告警重发间隔 %s → %s",
			repeatText(before.AlertRepeatMinutes), repeatText(after.AlertRepeatMinutes)),
			before.AlertRepeatMinutes, after.AlertRepeatMinutes)
	}
	if before.AlertSupplyLowPercent != after.AlertSupplyLowPercent {
		add("alertSupplyLowPercent", fmt.Sprintf("耗材不足阈值 %d%% → %d%%",
			before.AlertSupplyLowPercent, after.AlertSupplyLowPercent),
			before.AlertSupplyLowPercent, after.AlertSupplyLowPercent)
	}
	return out
}

// webhookHostText 只留主机名，去掉路径与查询串。
//
// 机器人的 webhook 把密钥放在【路径或 query】里（企业微信是 key=…，
// 钉钉是 access_token=…），完整 URL 写进审计等于把密钥抄了一份到人人都能看的
// 列表页上。主机名足够回答「原来发到企业微信、现在发到自建服务了」。
func webhookHostText(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "（未配置）"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "（无法解析）"
	}
	return u.Hostname()
}

// repeatText 把「重发间隔」的分钟数翻成话。0 是一个特殊值（只提醒一次），
// 直接显示「0 分钟」会让人以为是「不停地提醒」。
func repeatText(minutes int64) string {
	if minutes <= 0 {
		return "只提醒一次"
	}
	return fmt.Sprintf("%d 分钟", minutes)
}

func onOff(b bool) string {
	if b {
		return "开启"
	}
	return "关闭"
}

func retentionText(days int64) string {
	if days <= 0 {
		return "不自动清理"
	}
	return fmt.Sprintf("%d 天", days)
}

func limitText(n int64) string {
	if n <= 0 {
		return "不限"
	}
	return fmt.Sprintf("%d 页", n)
}

func uploadLimitText(n int64) string {
	if n <= 0 {
		return "不限"
	}
	return fmt.Sprintf("%d MB", n/(1024*1024))
}

// dashIfEmpty 把空串显示成「空」，否则审计摘要里会出现「手机号  → 138…」
// 这种两个空格并排、看不出改了什么的一句话。
func dashIfEmpty(s string) string {
	if strings.TrimSpace(s) == "" {
		return "（空）"
	}
	return s
}

func adminGetSettingsHandler(w http.ResponseWriter, r *http.Request) {
	var snap settingsSnapshot
	err := appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
		var err error
		snap, err = loadSettingsSnapshot(r.Context(), tx)
		return err
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load settings")
		return
	}
	writeJSON(w, map[string]interface{}{
		"retentionDays":   snap.RetentionDays,
		"saveHistory":     snap.SaveHistory,
		"maxPagesPerJob":  snap.MaxPagesPerJob,
		"maxUploadBytes":  snap.MaxUploadBytes,
		"customCss":       snap.CustomCSS,
		"guestMode":       snap.GuestMode,
		"orderTtlMinutes": snap.OrderTTLMinutes,
		"pullModeEnabled": snap.PullModeEnabled,
		"pullTtlHours":    snap.PullTTLHours,
		// 设备异常告警。webhook 地址会回给前端（管理员本来就要能看见并改它），
		// 但审计里只记主机名 —— 理由见 settingsChanges。
		"alertEnabled":          snap.AlertEnabled,
		"alertWebhookUrl":       snap.AlertWebhookURL,
		"alertWebhookKind":      snap.AlertWebhookKind,
		"alertRepeatMinutes":    snap.AlertRepeatMinutes,
		"alertSupplyLowPercent": snap.AlertSupplyLowPercent,
	})
}

// publicSettingsHandler 返回登录前也需要展示的少量前端可读设置：
//   - customCss：登录页 / 主界面都会应用（Issue #57）。
//   - guestMode：前端据此决定登录页是否自动登入访客（Issue #55）。
//
// 未登录用户也会命中这些设置，所以必须走公开接口而不是 /api/admin/settings。
func publicSettingsHandler(w http.ResponseWriter, r *http.Request) {
	var customCSS string
	var guestMode int64
	_ = appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
		if v, err := store.GetSettingString(r.Context(), tx, store.SettingCustomCSS, ""); err == nil {
			customCSS = v
		}
		if v, err := store.GetSettingInt(r.Context(), tx, store.SettingGuestMode, 0); err == nil {
			guestMode = v
		}
		return nil
	})
	writeJSON(w, map[string]interface{}{
		"customCss": customCSS,
		"guestMode": guestMode != 0,
		// 计费是否开启决定前端要不要展示价格 / 钱包相关 UI。
		// 只暴露这一个布尔，不含价目明细 —— 明细只走登录后的 /api/prices。
		"billingEnabled": billingEnabled(r.Context()),
		// 取件模式是否开启：前端据此决定提交走哪条路，以及把成功文案换成
		// 「已暂存，请到打印机刷卡取件」。这两个模式互斥，不会同时为真。
		"pullModeEnabled": pullModeEnabled(r.Context()),
		// 取件时限（小时），用于「请在 N 小时内取件」这类提示。
		"pullTtlHours": pullTTLHours(r.Context()),
		// 支付宝授权登录是否可用（判据是凭据齐全，与支付方式列表同一套）。
		// 前端据此决定登录页要不要显示「支付宝登录」按钮：没配置却显示一个
		// 点了必然失败的按钮，比不显示更糟；反过来它也是个不用改代码、不用
		// 重新部署前端的降级开关。
		"alipayLogin": alipayLoginEnabled(),
		"currency":    CurrencyCNY,
	})
}

func adminUpdateSettingsHandler(w http.ResponseWriter, r *http.Request) {
	var payload settingsPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	err := appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
		// 「改之前」必须是【本事务内读到的一致快照】，不能在事务外先读一遍：
		// 两个管理员同时保存时，事务外读到的值可能已经是别人改完之后的了 ——
		// 那样审计会记成「B 把 30 改成了 60」，而真实过程是「A 改 30→60、
		// B 改 60→90」。记错前值比不记更糟，因为它看起来是确定的。
		before, err := loadSettingsSnapshot(r.Context(), tx)
		if err != nil {
			return err
		}

		if payload.OrderTTLMinutes != nil {
			// 下界 1 分钟：0 或负数会让订单在创建瞬间就过期，
			// 用户根本来不及付款。上界一天，避免误填出一个月。
			if *payload.OrderTTLMinutes < 1 || *payload.OrderTTLMinutes > 1440 {
				return errors.New("orderTtlMinutes 必须在 1 到 1440 之间（分钟）")
			}
			if err := store.SetSettingInt(r.Context(), tx, store.SettingOrderTTLMinutes, *payload.OrderTTLMinutes); err != nil {
				return err
			}
		}
		if payload.RetentionDays != nil {
			if *payload.RetentionDays < 0 {
				return errors.New("invalid retentionDays")
			}
			if err := store.SetSettingInt(r.Context(), tx, store.SettingRetentionDays, *payload.RetentionDays); err != nil {
				return err
			}
		}
		if payload.SaveHistory != nil {
			var v int64
			if *payload.SaveHistory {
				v = 1
			}
			if err := store.SetSettingInt(r.Context(), tx, store.SettingSaveHistory, v); err != nil {
				return err
			}
		}
		if payload.MaxPagesPerJob != nil {
			if *payload.MaxPagesPerJob < 0 {
				return errors.New("invalid maxPagesPerJob")
			}
			if err := store.SetSettingInt(r.Context(), tx, store.SettingMaxPagesPerJob, *payload.MaxPagesPerJob); err != nil {
				return err
			}
		}
		if payload.MaxUploadBytes != nil {
			if *payload.MaxUploadBytes < 0 {
				return errors.New("invalid maxUploadBytes")
			}
			if err := store.SetSettingInt(r.Context(), tx, store.SettingMaxUploadBytes, *payload.MaxUploadBytes); err != nil {
				return err
			}
		}
		if payload.CustomCSS != nil {
			css := *payload.CustomCSS
			if len(css) > customCSSMaxLen {
				return errors.New("customCss too long")
			}
			if err := store.SetSettingString(r.Context(), tx, store.SettingCustomCSS, css); err != nil {
				return err
			}
		}
		if payload.GuestMode != nil {
			// 访客模式与计费互斥（反方向在 PUT /api/admin/prices 里校验）。
			// guest 是所有未登录用户共用的一个账号，它的余额就是「公摊钱包」——
			// 任何人都能花光，且无法追责、无法对账。
			if *payload.GuestMode {
				billing, err := store.GetSettingInt(r.Context(), tx, store.SettingBillingEnabled, 0)
				if err != nil {
					return err
				}
				if billing != 0 {
					return errors.New("请先关闭计费再开启访客模式")
				}
			}
			if err := store.SetSettingInt(r.Context(), tx, store.SettingGuestMode, boolToInt(*payload.GuestMode)); err != nil {
				return err
			}
		}
		if payload.PullModeEnabled != nil {
			// ── 取件模式的三方互斥 ──
			//
			// 计费 / 取件 / 访客 三者都在描述「谁是这个部署的打印入口」，
			// 两两都不兼容：
			//   计费 × 取件：一个要求先付钱，一个压根不收费，
			//                /api/print 同时该 403 又该暂存。
			//   取件 × 访客：访客是所有未登录用户共用的账号，而取件模式的
			//                全部意义就是「任务认人」—— 共用账号下所有人的
			//                任务会混在一起，刷卡时统统打出来。
			if *payload.PullModeEnabled {
				billing, err := store.GetSettingInt(r.Context(), tx, store.SettingBillingEnabled, 0)
				if err != nil {
					return err
				}
				if billing != 0 {
					return errors.New("取件模式与计费互斥，请先关闭计费")
				}
				guest, err := store.GetSettingInt(r.Context(), tx, store.SettingGuestMode, 0)
				if err != nil {
					return err
				}
				if guest != 0 {
					return errors.New("取件模式与访客模式互斥，请先关闭访客模式")
				}
			}
			if err := store.SetSettingInt(r.Context(), tx, store.SettingPullModeEnabled, boolToInt(*payload.PullModeEnabled)); err != nil {
				return err
			}
		}
		if payload.PullTTLHours != nil {
			// 下界 1 小时：更短会让员工来不及走到打印机。
			// 上界 30 天：再长就是「文件在服务器上无限期躺着」，
			// 而这是企业文档，不是公共资源。
			if *payload.PullTTLHours < 1 || *payload.PullTTLHours > 720 {
				return errors.New("pullTtlHours 必须在 1 到 720 之间（小时）")
			}
			if err := store.SetSettingInt(r.Context(), tx, store.SettingPullTTLHours, *payload.PullTTLHours); err != nil {
				return err
			}
		}

		// ── 设备异常告警 ──
		if payload.AlertEnabled != nil {
			// 开之前必须有通知地址，否则就是「开关打开、消息发不出去」——
			// 那种状态比不告警更危险：管理员以为有人在看着。
			//
			// 判据用【保存后的最终值】：一次保存里同时改开关与地址是常态，
			// 只看 payload 里的地址会在「地址没改、开关打开」时误判。
			if *payload.AlertEnabled {
				// 一次保存里同时改开关与地址是常态，所以要看「保存后的最终值」：
				// 有请求里的地址就用请求里的，没有才回退到库里的现值。
				url := strings.TrimSpace(before.AlertWebhookURL)
				if payload.AlertWebhookURL != nil {
					url = strings.TrimSpace(*payload.AlertWebhookURL)
				}
				if url == "" {
					return errors.New("请先填写告警通知地址再开启设备告警")
				}
			}
			if err := store.SetSettingInt(r.Context(), tx, store.SettingAlertEnabled, boolToInt(*payload.AlertEnabled)); err != nil {
				return err
			}
		}
		if payload.AlertWebhookURL != nil {
			u := strings.TrimSpace(*payload.AlertWebhookURL)
			// 必须是 http(s)。这里不做「能不能连通」的校验 —— 那是
			// POST /api/admin/device-events/test 的活，而写得过于严格的
			// 校验会把自建转发服务（内网 IP、非标准端口、带 query 的 token）
			// 挡在外面。
			if u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
				return errors.New("告警通知地址必须以 http:// 或 https:// 开头")
			}
			if len(u) > 2048 {
				return errors.New("告警通知地址过长")
			}
			if err := store.SetSettingString(r.Context(), tx, store.SettingAlertWebhookURL, u); err != nil {
				return err
			}
		}
		if payload.AlertWebhookKind != nil {
			kind := strings.ToLower(strings.TrimSpace(*payload.AlertWebhookKind))
			switch kind {
			case "auto", "wechat", "dingtalk", "feishu", "generic":
			default:
				return errors.New("alertWebhookKind 取值不合法")
			}
			if err := store.SetSettingString(r.Context(), tx, store.SettingAlertWebhookKind, kind); err != nil {
				return err
			}
		}
		if payload.AlertRepeatMinutes != nil {
			// 上界一天：再长的「重发」已经不属于提醒，而属于「忘了这件事」。
			if *payload.AlertRepeatMinutes < 0 || *payload.AlertRepeatMinutes > 1440 {
				return errors.New("alertRepeatMinutes 必须在 0 到 1440 之间（分钟，0 表示只提醒一次）")
			}
			if err := store.SetSettingInt(r.Context(), tx, store.SettingAlertRepeatMinutes, *payload.AlertRepeatMinutes); err != nil {
				return err
			}
		}
		if payload.AlertSupplyLowPercent != nil {
			if *payload.AlertSupplyLowPercent < 0 || *payload.AlertSupplyLowPercent > 100 {
				return errors.New("alertSupplyLowPercent 必须在 0 到 100 之间")
			}
			if err := store.SetSettingInt(r.Context(), tx, store.SettingAlertSupplyLowPercent, *payload.AlertSupplyLowPercent); err != nil {
				return err
			}
		}

		after, err := loadSettingsSnapshot(r.Context(), tx)
		if err != nil {
			return err
		}
		// 只记真正变了的字段（理由见 recordAudit 的第 3 条约定）。
		return recordAudit(r.Context(), tx, sessionUsername(r), store.AuditSettingsUpdate,
			"全局", "", settingsChanges(before, after)...)
	})
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func adminCleanupHandler(w http.ResponseWriter, r *http.Request) {
	count, err := cleanupAllPrints(r.Context(), appStore, uploadDir)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "cleanup failed: "+err.Error())
		return
	}

	// ⚠️ 这一条审计【不能】和业务改动同事务 —— 清理动作的主体是【删文件】，
	// 而文件系统不参与数据库事务。删都删完了，这里再回滚也换不回文件。
	// 所以它是「事后补记」：写不进去时只能大声报错，不能假装没发生。
	//
	// 之所以仍然要记：这是全站唯一会把所有人的打印历史一次性抹掉的操作，
	// 出了事（「昨天的记录怎么没了」）必须能查到是谁在几点点的。
	if err := appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
		return recordAudit(r.Context(), tx, sessionUsername(r), store.AuditCleanupRun,
			"全局", fmt.Sprintf("清空打印历史，删除 %d 个文件", count))
	}); err != nil {
		log.Printf("[audit] ⚠️ 清理已执行但审计未能写入（删了 %d 个文件）：%v", count, err)
	}

	writeJSON(w, map[string]interface{}{"ok": true, "deleted": count})
}

func normalizeRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "":
		return store.RoleUser
	case store.RoleUser:
		return store.RoleUser
	case store.RoleAdmin:
		return store.RoleAdmin
	default:
		return ""
	}
}

func parseIDParam(r *http.Request) (int64, error) {
	idStr := mux.Vars(r)["id"]
	return strconv.ParseInt(idStr, 10, 64)
}

func mapAdminUsers(users []store.User) []adminUserResponse {
	resp := make([]adminUserResponse, 0, len(users))
	for _, user := range users {
		resp = append(resp, mapAdminUser(user))
	}
	return resp
}

func mapAdminUser(user store.User) adminUserResponse {
	return adminUserResponse{
		ID:          user.ID,
		Username:    user.Username,
		Role:        user.Role,
		Protected:   user.Username == "admin" || user.Username == guestUsername,
		ContactName: user.ContactName,
		Phone:       user.Phone,
		Email:       user.Email,
		// 判据是【列】而不是用户名前缀：改名不该让它失去这个标记。
		IsAlipay:     user.AlipayOpenID != "",
		CardNo:       user.CardNo,
		BalanceCents: user.BalanceCents,
		CreatedAt:    user.CreatedAt,
		UpdatedAt:    user.UpdatedAt,
	}
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}
