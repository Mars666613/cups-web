package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cups-web/internal/auth"
	"cups-web/internal/store"

	"github.com/gorilla/mux"
)

// ─── 管理端：取件终端与工牌绑定 ───────────────────────────────────────
//
// 都在 admin 子路由下（RequireSession + RequireAdmin + ValidateCSRF）。
//
// ⚠️ 终端 token 是【等价于密码的凭据】：拿到它就能冒充那台取件终端。
// 所以它只在这个文件里出现，只发给管理员，日志里一律只打前缀
// （见 kiosk_handlers.go 的 shortToken）。

type kioskTerminalResponse struct {
	Token      string `json:"token"`
	Name       string `json:"name"`
	PrinterURI string `json:"printerUri"`
	Enabled    bool   `json:"enabled"`
	CreatedAt  string `json:"createdAt"`
	// LastSeenAt 为空表示这台终端从未上报过。管理员据此判断
	// 「现场那台到底配好没有」——无屏幕方案里这是唯一的在线线索。
	LastSeenAt string `json:"lastSeenAt"`
}

func mapKioskTerminal(t store.KioskTerminal) kioskTerminalResponse {
	return kioskTerminalResponse{
		Token:      t.Token,
		Name:       t.Name,
		PrinterURI: t.PrinterURI,
		Enabled:    t.Enabled,
		CreatedAt:  t.CreatedAt,
		LastSeenAt: t.LastSeenAt,
	}
}

type kioskTerminalPayload struct {
	Name       string `json:"name"`
	PrinterURI string `json:"printerUri"`
	Enabled    *bool  `json:"enabled"`
}

func adminListKioskTerminalsHandler(w http.ResponseWriter, r *http.Request) {
	var list []store.KioskTerminal
	err := appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
		var err error
		list, err = store.ListKioskTerminals(r.Context(), tx)
		return err
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to list kiosk terminals")
		return
	}

	resp := make([]kioskTerminalResponse, 0, len(list))
	for _, t := range list {
		resp = append(resp, mapKioskTerminal(t))
	}
	writeJSON(w, resp)
}

func adminCreateKioskTerminalHandler(w http.ResponseWriter, r *http.Request) {
	var payload kioskTerminalPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}
	payload.Name = strings.TrimSpace(payload.Name)
	payload.PrinterURI = strings.TrimSpace(payload.PrinterURI)
	// ⚠️ printerUri 允许为空 —— 那是【仅用于绑卡】的终端（通常就是管理员
	// 自己电脑上插的那个读卡器）。绑卡只需要读一次 UID，而 UID 是芯片固化的，
	// 读卡器插在哪台机器上都一样，所以没必要为了绑卡走到打印机那边去。
	// 前端用一个「仅用于绑卡」的勾选框表达这个意图。
	if payload.Name == "" {
		// 名字是给管理员自己认的（"3楼东侧"）。空名会让列表里一排终端
		// 无从分辨，而现场排障时正是靠它定位。
		payload.Name = payload.PrinterURI
	}
	if payload.Name == "" {
		payload.Name = "绑卡终端"
	}

	term := store.KioskTerminal{
		Token:      randomToken(),
		Name:       payload.Name,
		PrinterURI: payload.PrinterURI,
		Enabled:    true,
	}
	if err := appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
		return store.CreateKioskTerminal(r.Context(), tx, term)
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to create terminal")
		return
	}

	log.Printf("[kiosk] 新建取件终端「%s」→ %s", term.Name, term.PrinterURI)
	// token 只在这里返回一次是没必要的（列表接口也返回），但创建时返回
	// 能让前端直接弹出「把它填进树莓派配置」的引导。
	writeJSON(w, mapKioskTerminal(term))
}

func adminUpdateKioskTerminalHandler(w http.ResponseWriter, r *http.Request) {
	token := mux.Vars(r)["token"]
	var payload kioskTerminalPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}
	payload.Name = strings.TrimSpace(payload.Name)
	payload.PrinterURI = strings.TrimSpace(payload.PrinterURI)
	// printerUri 为空 = 改成「仅用于绑卡」的终端（见 create 处的说明）。

	// enabled 是可选字段：不传就保持原值，避免「只改名字」把终端意外停用。
	enabled := true
	if payload.Enabled != nil {
		enabled = *payload.Enabled
	} else {
		var current store.KioskTerminal
		if err := appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
			var err error
			current, err = store.GetKioskTerminal(r.Context(), tx, token)
			return err
		}); err != nil {
			writeJSONError(w, http.StatusNotFound, "终端不存在")
			return
		}
		enabled = current.Enabled
	}

	err := appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
		return store.UpdateKioskTerminal(r.Context(), tx, token, payload.Name, payload.PrinterURI, enabled)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "终端不存在")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to update terminal")
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func adminDeleteKioskTerminalHandler(w http.ResponseWriter, r *http.Request) {
	token := mux.Vars(r)["token"]
	err := appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
		return store.DeleteKioskTerminal(r.Context(), tx, token)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "终端不存在")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to delete terminal")
		return
	}
	log.Printf("[kiosk] 删除取件终端 %s", shortToken(token))
	writeJSON(w, map[string]any{"ok": true})
}

// ─── 刷卡绑定窗口（「点绑卡 → 到终端刷卡」）────────────────────────────
//
// 读卡器挂在打印机旁的终端上，**浏览器访问不到它** —— 所以「在哪点按钮」和
// 「在哪刷卡」天生是两个地方，中间必须由服务端记住「现在这次刷卡是给谁绑的」。
// 这张表就是那个中间人（见 internal/store/schema.sql 的 card_bind_sessions）。
//
// 它替代不了的那条路是 kiosk.py --read：到终端读出 UID、抄进管理界面。
// 两条都留着 —— 批量发卡时抄号更快（不用来回走），单张绑定时刷卡更省事。

// cardBindWindow 是窗口有效期。
//
// 定这个值的取舍：太短则管理员还没走到终端就失效了；太长则
// 「窗口开着的时候别人来刷卡」的概率变大。两分钟够从工位走到打印机。
const cardBindWindow = 120 * time.Second

type cardBindSessionResponse struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"userId"`
	Username  string `json:"username"`
	ExpiresAt string `json:"expiresAt"`
	// Status ∈ waiting（等刷卡）/ bound（绑上了）/ closed（超时或已取消）
	Status string `json:"status"`
	CardNo string `json:"cardNo,omitempty"`
	// Error 是「这次刷卡为什么没绑上」（比如卡已绑给别人）。
	// 窗口【不】因此关闭 —— 管理员可以直接换一张卡再刷。
	Error string `json:"error,omitempty"`
}

func toCardBindResponse(b store.CardBindSession) cardBindSessionResponse {
	status := "waiting"
	switch {
	case b.Done && b.CardNo != "":
		status = "bound"
	case b.Done:
		status = "closed"
	}
	return cardBindSessionResponse{
		ID:        b.ID,
		UserID:    b.UserID,
		Username:  b.Username,
		ExpiresAt: b.ExpiresAt,
		Status:    status,
		CardNo:    b.CardNo,
		Error:     b.Error,
	}
}

func adminCreateCardBindSessionHandler(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	sess, err := auth.GetSession(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var created store.CardBindSession
	expiresAt := time.Now().UTC().Add(cardBindWindow).Format(time.RFC3339)
	err = appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
		if _, err := store.GetUserByID(r.Context(), tx, userID); err != nil {
			return err
		}
		id, err := store.CreateCardBindSession(r.Context(), tx, userID, sess.Username, expiresAt)
		if err != nil {
			return err
		}
		created, err = store.GetCardBindSession(r.Context(), tx, id)
		return err
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "用户不存在")
			return
		}
		if errors.Is(err, store.ErrBindSessionActive) {
			// 明确说「已经有一个在等着了」，而不是「操作失败」——
			// 管理员多半是在另一个标签页里点过一次，或者上一次没取消。
			writeJSONError(w, http.StatusConflict,
				"已经有一个绑卡窗口开着了。请在那边刷完卡，或先取消它。")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to open bind session")
		return
	}

	log.Printf("[kiosk] 管理员 %s 为「%s」打开绑卡窗口（%ds 内到终端刷卡）",
		sess.Username, created.Username, int(cardBindWindow.Seconds()))
	writeJSON(w, toCardBindResponse(created))
}

func adminGetCardBindSessionHandler(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var bind store.CardBindSession
	err = appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
		var err error
		bind, err = store.GetCardBindSession(r.Context(), tx, id)
		return err
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "绑定窗口不存在")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to load bind session")
		return
	}
	writeJSON(w, toCardBindResponse(bind))
}

func adminCancelCardBindSessionHandler(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
		_, err := store.CancelCardBindSession(r.Context(), tx, id)
		return err
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to cancel bind session")
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// ─── 工牌绑定 ─────────────────────────────────────────────────────────
//
// 绑定的方式必须是【刷卡】，不是手输。原因见 schema.sql 里 card_no 那段：
// 同一张卡换个读卡器读出来就不是同一个字符串（频率不同、进制不同、字节序
// 不同、Wiegand 格式号与卡面号不同）。手输的号码和读卡器吐出来的对不上时，
// 现象是「员工刷卡没反应」—— 而根因在键盘上，不在系统里。
//
// 所以前端那个输入框只是一个「读卡器捕获框」：它不接收手打，只接收
// 键盘模拟读卡器的一串输入 + 回车，捕获到后显示原文让管理员确认再提交。

type bindCardPayload struct {
	CardNo string `json:"cardNo"`
}

func adminBindCardHandler(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	var payload bindCardPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}
	// 与刷卡时同样的处理：只 trim。任何其它归一都会让两边对不上。
	cardNo := strings.TrimSpace(payload.CardNo)
	if cardNo == "" {
		writeJSONError(w, http.StatusBadRequest, "没有读到卡号，请重新刷卡")
		return
	}
	if len(cardNo) < 4 {
		// 太短的极可能是误触（管理员随手敲了几个键就回车）。真卡号
		// 几乎不可能短于 4 位，而误绑一张卡要先去解绑才能纠正。
		writeJSONError(w, http.StatusBadRequest, "读到的内容太短，可能没刷上。请重新刷卡")
		return
	}

	err = appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
		return store.SetUserCardNo(r.Context(), tx, userID, cardNo)
	})
	if err != nil {
		if errors.Is(err, store.ErrCardTaken) {
			// ⚠️ 必须告诉管理员【这张卡现在绑给了谁】。
			// 只说「绑定失败」，他会以为是系统故障，然后反复重试；
			// 而真实情况通常是「卡发重了」或「员工换卡后老卡没解绑」——
			// 这两种的处理方式完全不同。
			owner := ""
			if berr := appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
				u, uerr := store.GetUserByCardNo(r.Context(), tx, cardNo)
				if uerr != nil {
					return uerr
				}
				owner = u.Username
				return nil
			}); berr != nil {
				log.Printf("[kiosk] 查询卡号占用者失败: %v", berr)
			}
			if owner != "" {
				writeJSONError(w, http.StatusConflict, "这张卡已经绑定给「"+owner+"」了，请先在那边解绑")
			} else {
				writeJSONError(w, http.StatusConflict, "这张卡已经绑定给别的账号了")
			}
			return
		}
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "用户不存在")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to bind card")
		return
	}

	log.Printf("[kiosk] 用户 #%d 绑定工牌 %q", userID, cardNo)
	writeJSON(w, map[string]any{"ok": true, "cardNo": cardNo})
}

func adminUnbindCardHandler(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	// 解绑 = 置空串。空串不参与那条部分唯一索引，所以必定成功，
	// 也不会和任何别人冲突 —— 换卡就是「先解绑旧的、再刷一张新的」。
	if err := appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
		return store.SetUserCardNo(r.Context(), tx, userID, "")
	}); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "用户不存在")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to unbind card")
		return
	}
	log.Printf("[kiosk] 用户 #%d 解绑工牌", userID)
	writeJSON(w, map[string]any{"ok": true})
}
