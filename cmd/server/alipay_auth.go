package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"cups-web/internal/payment"
	"cups-web/internal/store"

	"golang.org/x/crypto/bcrypt"
)

// ─── 支付宝授权登录（OAuth）─────────────────────────────────────────────
//
// 顾客扫码 → 一键支付宝登录 → 传文件 → 支付。身份在建单【之前】就确定，
// 所以记录和余额从第一单起就是干净的。
//
// 与付款是两条独立的链路：这里只认人，一分钱都不动。
//
// ## 为什么流程是"绕一圈"的
//
// 官方要求：手机端 H5 的授权【必须】在支付宝客户端内进行；实测在系统浏览器里
// 直接开授权链接是个死路（「请在支付宝客户端打开链接」，无可点按钮）。
// 所以授权必然发生在支付宝的 webview 里。
//
// 但支付宝的 webview 干不了活：
//   · 它的文件选择器翻不到微信/QQ 的文件（浏览器用的是系统文件管理器，有快捷访问）
//   · pdf.js 的 worker 在里面起不来，PDF 预览一直转圈
//   · 支付流程在里面也没验过，而浏览器那条是跑了很久的
//
// 所以：**授权在支付宝里完成，会话交回浏览器**（微信 PC 扫码登录是同一套）：
//
//	浏览器  /start（建一行状态）→ 用 alipays:// 把授权页交给支付宝 → 开始轮询
//	支付宝  授权 → 回跳 …/#/print?auth_code&state → /exchange（把 user_id 写进那行）
//	浏览器  /poll 轮询到 user_id 非 0 → 给自己签发会话 → 删掉那行 → 继续上传/支付
//
// 状态为什么必须放服务端：浏览器和 webview 是【两个 cookie 罐】。

const (
	// alipayUsernamePrefix 是支付宝账号的登录名前缀。
	// username 是 NOT NULL UNIQUE 且不许为空，所以必须造一个合成名；
	// 带前缀同时把它和保留的 guest / admin 隔开（那些守卫都是字符串比较）。
	alipayUsernamePrefix = "alipay:"

	// alipayAuthReturnPath 是授权完成后回跳落到前端哪个页面。
	//
	// 沿用"/#/print"——这个地址已经配在支付宝后台的「授权回调地址」里了。
	// 它虽然落在受保护的 /print 上（守卫会先送到 /login 并把 URL 参数冲掉），
	// 但 auth_code 是在 App.vue 的 setup 阶段【同步】读走的，任何路由跳转都赶不上。
	alipayAuthReturnPath = "/print"

	// alipayAuthTimeout 是向支付宝换取令牌的超时。
	// ⚠️ 挂在 context.Background() 上而不是请求的 context：出站调用不该被浏览器
	// 的一次中止掐断（仓库里支付渠道也是这条规矩）。
	alipayAuthTimeout = 15 * time.Second

	// alipayLoginStateTTL 是登录状态行的存活时间。过了就当没用（会被清掉）。
	// 取 15 分钟：授权码本身最短 3 分钟就过期，留出"在支付宝里磨蹭一会儿"的余量。
	alipayLoginStateTTL = 15 * time.Minute
)

// alipayOAuthClient 把授权登录需要的两个能力抽出来，便于 handler 单测注入假实现
// （真实现要打支付宝网关，测试里不能依赖外网）。
type alipayOAuthClient interface {
	AuthorizeURL(state, redirectURI string) string
	ExchangeAuthCode(ctx context.Context, code string) (payment.OAuthToken, error)
}

// 是个变量而不是直接调用，测试里可以替换掉。
var buildAlipayOAuthClient = func() (alipayOAuthClient, error) {
	return buildAlipayClient()
}

// alipayLoginEnabled 报告支付宝授权登录是否可用。
//
// 判据与支付方式列表同源：凭据（ALIPAY_APP_ID / PRIVATE_KEY / PUBLIC_KEY）齐全。
// ⚠️ 它只说明"本实例配了支付宝"，不代表开放平台那边确实开通了「获取会员信息」
// 与 `openid 配置管理` —— 那两件事只能在真机上试出来。所以这个布尔只用来决定
// 登录页要不要显示按钮，不能当作"这条路一定通"的证据。
func alipayLoginEnabled() bool {
	_, ok := alipayFromEnv()
	return ok
}

// isAlipayUserAgent 判断请求来自支付宝的内置浏览器。
//
// 用来分辨"谁在发起授权"：普通浏览器发起的会有一个人在后面轮询等结果；
// 支付宝里直接打开的那种就地登录、没有轮询方。两种收尾方式不一样。
func isAlipayUserAgent(ua string) bool {
	return strings.Contains(strings.ToLower(ua), "alipayclient")
}

// alipayAuthStartHandler 处理 GET /api/auth/alipay/start。
//
// 建一行登录状态，返回拼好的授权页地址。
//
// 【为什么返回 JSON 而不是 302】端外那一步（把授权页交给支付宝打开）只能由前端
// 用 alipays:// 触发，后端套不了自定义 scheme；而且前端跳走之后还要留在原地轮询。
// 把地址交给前端，这两件事才有地方做。
func alipayAuthStartHandler(w http.ResponseWriter, r *http.Request) {
	client, err := buildAlipayOAuthClient()
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "支付宝登录未配置")
		return
	}

	redirect := paymentReturnURL(r.Context(), r, alipayAuthReturnPath)
	if redirect == "" {
		// 拿不到回跳地址就没法授权 —— 这属于配置问题，要说清楚怎么查，
		// 而不是丢一句"登录失败"。
		writeJSONError(w, http.StatusServiceUnavailable,
			"拿不到回跳地址：请检查系统设置里的 site_base_url，或换用正常的域名/IP 访问本站")
		return
	}

	state := randomToken()
	fromBrowser := !isAlipayUserAgent(r.UserAgent())

	err = appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
		// 顺手清掉过期的行。这张表只有"正在登录的人"在用，量极小，
		// 没必要为它单开一个定时任务；而留着过期行是有害的（state 可被重放）。
		cutoff := time.Now().UTC().Add(-alipayLoginStateTTL).Format(time.RFC3339)
		if _, err := store.DeleteExpiredAlipayLoginStates(r.Context(), tx, cutoff); err != nil {
			return err
		}
		return store.CreateAlipayLoginState(r.Context(), tx, state, fromBrowser)
	})
	if err != nil {
		log.Printf("[alipay-login] 建登录状态失败: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "登录失败，请稍后再试")
		return
	}

	writeJSON(w, map[string]interface{}{
		"authorizeUrl": client.AuthorizeURL(state, redirect),
		// state 回给调用方，是为了让【浏览器】知道该轮询哪一个。
		// 它就是发起方自己刚拿到的值，同源请求，不算泄露。
		"state":       state,
		"fromBrowser": fromBrowser,
	})
}

// alipayAuthExchangeHandler 处理 POST /api/auth/alipay/exchange。
//
// 由【支付宝 webview 里那一页】调用（回跳参数就在它的地址栏上）。
// 它把兑换出来的用户写进状态行，浏览器那边轮询到就能领走。
func alipayAuthExchangeHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code  string `json:"code"`
		State string `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "请求格式不正确")
		return
	}
	req.Code = strings.TrimSpace(req.Code)
	req.State = strings.TrimSpace(req.State)
	if req.Code == "" {
		writeJSONError(w, http.StatusBadRequest, "缺少授权码，请重新点击支付宝登录")
		return
	}
	if req.State == "" {
		writeJSONError(w, http.StatusBadRequest, "缺少登录状态标识，请重新点击支付宝登录")
		return
	}

	// 限流放在最前面：这个端点每次都会向支付宝发一次【出站请求】，失败还会走到
	// 建号/写库，是天然的放大点。（start 端点不限流：它只写一行小记录。）
	key := clientIP(r) + "|alipay"
	if ok, _ := loginAllowed(key); !ok {
		writeJSONError(w, http.StatusTooManyRequests, "尝试过于频繁，请稍后再试")
		return
	}

	// 官方要求：state 缺失、或与签发时记下的不一致，就必须拒绝服务。
	// 放在换取令牌【之前】，这样伪造的请求根本打不到支付宝（不放大）。
	//
	// ⚠️ 查的是服务端的行，不是 cookie —— 兑换发生在支付宝 webview，
	// 而发起可能发生在普通浏览器，两者 cookie 罐不通（真机测出来的）。
	var pending store.AlipayLoginState
	err := appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
		s, err := store.GetAlipayLoginState(r.Context(), tx, req.State)
		if err != nil {
			return err
		}
		pending = s
		return nil
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusBadRequest, "登录状态已失效，请重新点击支付宝登录")
			return
		}
		log.Printf("[alipay-login] 读登录状态失败: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "登录失败，请稍后再试")
		return
	}

	client, err := buildAlipayOAuthClient()
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "支付宝登录未配置")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), alipayAuthTimeout)
	defer cancel()

	tok, err := client.ExchangeAuthCode(ctx, req.Code)
	if err != nil {
		registerLoginFailure(key)
		// payment 层给的已经是【指向根因】的中文（授权码过期 / app_id 不匹配 /
		// openid 配置没启用…），直接透出去。🚫 不要在这里吞成一句"登录失败"。
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	clearLoginFailures(key)

	user, err := loadOrCreateAlipayUser(ctx, tok.OpenID)
	if err != nil {
		log.Printf("[alipay-login] 建号/查号失败: openid=%s err=%v", tok.OpenID, err)
		writeJSONError(w, http.StatusInternalServerError, "登录失败，请稍后再试")
		return
	}

	err = appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
		if err := store.MarkAlipayLoginStateDone(r.Context(), tx, req.State, user.ID); err != nil {
			return err
		}
		// 没人会来轮询的（支付宝扫一扫直接进来的那种），这行就没用了，当场删掉。
		if !pending.FromBrowser {
			return store.DeleteAlipayLoginState(r.Context(), tx, req.State)
		}
		return nil
	})
	if err != nil {
		log.Printf("[alipay-login] 写登录状态失败: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "登录失败，请稍后再试")
		return
	}

	// 只在【没有浏览器在等】的时候，才给调用方（支付宝 webview）签会话 ——
	// 也就是"用支付宝扫一扫直接进来"的那种，它就地继续用。
	//
	// 🚫 浏览器发起的那次【不签】。那一页只是个中转：活要在浏览器里干
	// （那边才翻得到微信/QQ 的文件、PDF 预览也正常）。给它签了会话，它就成了一个
	// 功能完整的入口 —— 能点进个人中心、能上传，而它恰恰是最不该干活的那个环境。
	// 不签，它就物理上到不了别的页面（所有受保护路由都会被守卫拦回去）。
	if !pending.FromBrowser {
		if _, err := issueSession(w, r, user); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "session error")
			return
		}
	}

	writeJSON(w, map[string]interface{}{
		"ok": true,
		// 告诉前端：这次是"浏览器发起、支付宝里完成"，该提示用户回浏览器继续 ——
		// 浏览器那边的文件选择器才翻得到微信/QQ 的文件，PDF 预览也才正常。
		"needBrowserReturn": pending.FromBrowser,
	})
}

// alipayAuthPollHandler 处理 GET /api/auth/alipay/poll?state=xxx。
//
// 浏览器在被送进支付宝之后就一直留在这里轮询（自定义 scheme 唤起 App 时
// 浏览器不会卸载当前页，所以这个页面还在），一旦那行状态里有了 user_id，
// 就把会话签给【浏览器自己】。这就是"会话交回浏览器"的那一步。
func alipayAuthPollHandler(w http.ResponseWriter, r *http.Request) {
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if state == "" {
		writeJSONError(w, http.StatusBadRequest, "缺少登录状态标识")
		return
	}

	var pending store.AlipayLoginState
	err := appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
		s, err := store.GetAlipayLoginState(r.Context(), tx, state)
		if err != nil {
			return err
		}
		pending = s
		return nil
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// 已经领过了、过期被清了、或者本来就是假的。
			// ⚠️ 用 200 + status 而不是 404：这是轮询，前端每次都要判断，
			// 走 4xx 会被 apiFetch 的全局兜底当成"会话失效"弹登出，
			// 而此刻用户根本还没登录。
			writeJSON(w, map[string]string{"status": "expired"})
			return
		}
		log.Printf("[alipay-login] 轮询读状态失败: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "登录失败，请稍后再试")
		return
	}

	if pending.UserID == 0 {
		writeJSON(w, map[string]string{"status": "pending"})
		return
	}

	var user store.User
	err = appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
		u, err := store.GetUserByID(r.Context(), tx, pending.UserID)
		if err != nil {
			return err
		}
		user = u
		return nil
	})
	if err != nil {
		log.Printf("[alipay-login] 轮询查用户失败: user_id=%d err=%v", pending.UserID, err)
		writeJSONError(w, http.StatusInternalServerError, "登录失败，请稍后再试")
		return
	}

	// 一次性：领走就把这行删掉，免得同一个结果被反复兑换成会话。
	if err := appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
		return store.DeleteAlipayLoginState(r.Context(), tx, state)
	}); err != nil {
		// 删不掉不算致命：下面照样把会话给出去，这行最多被多领一次，
		// 而下一次轮询就再也找不到它了（前端拿到会话就停了）。
		log.Printf("[alipay-login] 删登录状态失败: %v", err)
	}

	sess, err := issueSession(w, r, user)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "session error")
		return
	}
	writeJSON(w, sess)
}

// alipayUsernameSuffixLen 是合成用户名里随机部分的长度。
//
// 6 位 base32（rand.Text 的字母表是 A-Z + 2-7）≈ 10 亿种，够一个连锁用很久。
// 真撞了由 loadOrCreateAlipayUser 换一个重试。
const alipayUsernameSuffixLen = 6

// alipayUsername 生成支付宝账号的登录名，形如 alipay:8F3K2Q。
//
// ⚠️ 绝不要拿 open_id 当用户名。它是 128 个字符：界面上显示不全、没人念得出来，
// 而管理端的「用户名」筛选是【精确匹配】（`u.username = $1`，见 prints.go / wallet.go）
// —— 想查某个顾客的流水时，那种用户名根本没法用。
//
// 身份存在 `alipay_open_id` 列里，那才是真正的凭据；用户名只是个能念、能搜的短标签。
// 前缀 `alipay:` 用来把它和保留的 guest / admin 隔开（那些守卫都是字符串比较）。
func alipayUsername() string {
	t := randomToken()
	if len(t) > alipayUsernameSuffixLen {
		t = t[:alipayUsernameSuffixLen]
	}
	return alipayUsernamePrefix + t
}

// findUserByOpenID 按 open_id 读一次用户（不存在时返回 sql.ErrNoRows）。
func findUserByOpenID(ctx context.Context, openID string) (store.User, error) {
	var user store.User
	err := appStore.WithTx(ctx, true, func(tx *sql.Tx) error {
		found, err := store.GetUserByAlipayOpenID(ctx, tx, openID)
		if err != nil {
			return err
		}
		user = found
		return nil
	})
	return user, err
}

// loadOrCreateAlipayUser 按 open_id 找用户，找不到就建一个。
//
// 两处并发/冲突兜底：
//   - 两个几乎同时的回跳都会读不到，随后一侧 CreateUser 撞上 alipay_open_id 的唯一索引
//     → 每次失败都按 open_id 重读一次，让后到者复用同一行（与 loadOrCreateGuest 同构）。
//   - 短随机用户名本身也可能撞（概率很小但不为零）→ 换一个再试。
func loadOrCreateAlipayUser(ctx context.Context, openID string) (store.User, error) {
	if u, err := findUserByOpenID(ctx, openID); err == nil {
		return u, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return store.User{}, err
	}

	// 密码存一个【随机哈希】而不是空串 —— 照 loadOrCreateGuest 的既有做法。
	// 空串其实也登不进来（bcrypt 对空哈希返回 ErrHashTooShort，LoginHandler
	// 会干净地拒绝），但随机哈希与既有约定一致，也不会在将来被某条新的
	// 比较路径误当成"空密码放行"。
	hash, err := bcrypt.GenerateFromPassword([]byte(randomToken()), bcrypt.DefaultCost)
	if err != nil {
		return store.User{}, err
	}

	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		var created store.User
		lastErr = appStore.WithTx(ctx, false, func(tx *sql.Tx) error {
			u, err := store.CreateUser(ctx, tx, store.CreateUserInput{
				Username:     alipayUsername(),
				PasswordHash: string(hash),
				Role:         store.RoleUser,
				Protected:    false,
				AlipayOpenID: openID,
			})
			if err != nil {
				return err
			}
			created = u
			return nil
		})
		if lastErr == nil {
			return created, nil
		}
		// 失败先看看是不是"并发同一个人"—— 是的话直接复用那一行，别再建了。
		if u, rerr := findUserByOpenID(ctx, openID); rerr == nil {
			return u, nil
		}
		// 否则多半是用户名撞了，换一个再来。
	}
	return store.User{}, lastErr
}
