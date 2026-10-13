package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
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
		return nil
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
		return nil
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

func adminGetSettingsHandler(w http.ResponseWriter, r *http.Request) {
	var retention int64
	var saveHistory int64
	var maxPages int64
	var maxBytes int64
	var customCSS string
	var guestMode int64
	var orderTTLMinutes int64
	var pullMode int64
	var pullTTLVal int64
	err := appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
		val, err := store.GetSettingInt(r.Context(), tx, store.SettingRetentionDays, 0)
		if err != nil {
			return err
		}
		retention = val
		sh, err := store.GetSettingInt(r.Context(), tx, store.SettingSaveHistory, 1)
		if err != nil {
			return err
		}
		saveHistory = sh
		mp, err := store.GetSettingInt(r.Context(), tx, store.SettingMaxPagesPerJob, 0)
		if err != nil {
			return err
		}
		maxPages = mp
		mb, err := store.GetSettingInt(r.Context(), tx, store.SettingMaxUploadBytes, 0)
		if err != nil {
			return err
		}
		maxBytes = mb
		css, err := store.GetSettingString(r.Context(), tx, store.SettingCustomCSS, "")
		if err != nil {
			return err
		}
		customCSS = css
		gm, err := store.GetSettingInt(r.Context(), tx, store.SettingGuestMode, 0)
		if err != nil {
			return err
		}
		guestMode = gm
		ttl, err := store.GetSettingInt(r.Context(), tx, store.SettingOrderTTLMinutes, 15)
		if err != nil {
			return err
		}
		orderTTLMinutes = ttl

		pm, err := store.GetSettingInt(r.Context(), tx, store.SettingPullModeEnabled, 0)
		if err != nil {
			return err
		}
		pullMode = pm

		pt, err := store.GetSettingInt(r.Context(), tx, store.SettingPullTTLHours, 72)
		if err != nil {
			return err
		}
		pullTTLVal = pt
		return nil
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load settings")
		return
	}
	writeJSON(w, map[string]interface{}{
		"retentionDays":   retention,
		"saveHistory":     saveHistory != 0,
		"maxPagesPerJob":  maxPages,
		"maxUploadBytes":  maxBytes,
		"customCss":       customCSS,
		"guestMode":       guestMode != 0,
		"orderTtlMinutes": orderTTLMinutes,
		"pullModeEnabled": pullMode != 0,
		"pullTtlHours":    pullTTLVal,
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
		return nil
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
