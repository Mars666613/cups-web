package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cups-web/internal/auth"
	"cups-web/internal/pricing"
	"cups-web/internal/store"

	"github.com/gorilla/mux"
)

// scheduledSubDir 保存定时任务源文件的相对目录。放在 uploads/scheduled/ 下
// 是为了让 cleanupOldPrints 天然扫不到（它只查 print_jobs.stored_path），
// 避免用户设的保留期把仍在使用的源文件误删。
const scheduledSubDir = "scheduled"

type scheduledPrintResponse struct {
	ID         int64  `json:"id"`
	UserID     int64  `json:"userId"`
	Username   string `json:"username"`
	Name       string `json:"name"`
	PrinterURI string `json:"printerUri"`
	Filename   string `json:"filename"`

	IsDuplex       bool   `json:"isDuplex"`
	IsColor        bool   `json:"isColor"`
	Copies         int    `json:"copies"`
	Orientation    string `json:"orientation"`
	PaperSize      string `json:"paperSize"`
	PaperType      string `json:"paperType"`
	MediaSource    string `json:"mediaSource"`
	PrintScaling   string `json:"printScaling"`
	PageRange      string `json:"pageRange"`
	PageSet        string `json:"pageSet"`
	Mirror         bool   `json:"mirror"`
	WatermarkText  string `json:"watermarkText"`
	NumberUp       int    `json:"numberUp"`
	NumberUpLayout string `json:"numberUpLayout"`
	PageBorder     string `json:"pageBorder"`

	ScheduleType    string `json:"scheduleType"`
	ScheduleTime    string `json:"scheduleTime"`
	ScheduleWeekday int    `json:"scheduleWeekday"`
	ScheduleDay     int    `json:"scheduleDay"`
	RunAt           string `json:"runAt"`
	NextRunAt       string `json:"nextRunAt"`
	LastRunAt       string `json:"lastRunAt"`
	LastStatus      string `json:"lastStatus"`
	LastError       string `json:"lastError"`
	Enabled         bool   `json:"enabled"`

	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

func mapScheduledPrint(rec store.ScheduledPrint) scheduledPrintResponse {
	return scheduledPrintResponse{
		ID:         rec.ID,
		UserID:     rec.UserID,
		Username:   rec.Username,
		Name:       rec.Name,
		PrinterURI: rec.PrinterURI,
		Filename:   rec.Filename,

		IsDuplex:       rec.IsDuplex,
		IsColor:        rec.IsColor,
		Copies:         rec.Copies,
		Orientation:    rec.Orientation,
		PaperSize:      rec.PaperSize,
		PaperType:      rec.PaperType,
		MediaSource:    rec.MediaSource,
		PrintScaling:   rec.PrintScaling,
		PageRange:      rec.PageRange,
		PageSet:        rec.PageSet,
		Mirror:         rec.Mirror,
		WatermarkText:  rec.WatermarkText,
		NumberUp:       rec.NumberUp,
		NumberUpLayout: rec.NumberUpLayout,
		PageBorder:     rec.PageBorder,

		ScheduleType:    rec.ScheduleType,
		ScheduleTime:    rec.ScheduleTime,
		ScheduleWeekday: rec.ScheduleWeekday,
		ScheduleDay:     rec.ScheduleDay,
		RunAt:           rec.RunAt,
		NextRunAt:       rec.NextRunAt,
		LastRunAt:       rec.LastRunAt,
		LastStatus:      rec.LastStatus,
		LastError:       rec.LastError,
		Enabled:         rec.Enabled,

		CreatedAt: rec.CreatedAt,
		UpdatedAt: rec.UpdatedAt,
	}
}

func scheduledListHandler(w http.ResponseWriter, r *http.Request) {
	sess, err := auth.GetSession(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var records []store.ScheduledPrint
	err = appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
		list, err := store.ListScheduledPrints(r.Context(), tx, store.ScheduledPrintFilter{
			Username: sess.Username,
		})
		if err != nil {
			return err
		}
		records = list
		return nil
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load scheduled prints")
		return
	}
	resp := make([]scheduledPrintResponse, 0, len(records))
	for _, rec := range records {
		resp = append(resp, mapScheduledPrint(rec))
	}
	writeJSON(w, resp)
}

// scheduledCreateHandler 上传文件 + 打印参数 + 触发策略。走 multipart form；
// 文件独立存放到 uploads/scheduled/<userId>/ 避开保留期清理。
func scheduledCreateHandler(w http.ResponseWriter, r *http.Request) {
	sess, err := auth.GetSession(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	applyUploadLimit(w, r)
	if err := r.ParseMultipartForm(512 << 20); err != nil {
		if isMaxBytesError(err) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "文件超出管理员设置的大小上限")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "invalid multipart form")
		return
	}
	file, fh, err := r.FormFile("file")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "missing file field")
		return
	}
	defer file.Close()

	baseDir := scheduledBaseDir(uploadDir, sess.UserID)
	storedRel, _, err := saveUploadedFile(file, fh.Filename, baseDir)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to save file")
		return
	}
	relInUploads := filepath.ToSlash(filepath.Join(scheduledSubDir, strconv.FormatInt(sess.UserID, 10), storedRel))

	rec, err := buildScheduledRecordFromForm(r, sess.UserID, fh.Filename, relInUploads)
	if err != nil {
		_ = os.Remove(filepath.Join(baseDir, filepath.FromSlash(storedRel)))
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	var newID int64
	err = appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
		id, err := store.InsertScheduledPrint(r.Context(), tx, &rec)
		if err != nil {
			return err
		}
		newID = id
		return nil
	})
	if err != nil {
		_ = os.Remove(filepath.Join(baseDir, filepath.FromSlash(storedRel)))
		writeJSONError(w, http.StatusInternalServerError, "failed to save scheduled print")
		return
	}

	var created store.ScheduledPrint
	if err := appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
		c, err := store.GetScheduledPrintByID(r.Context(), tx, newID)
		if err != nil {
			return err
		}
		created = c
		return nil
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to reload scheduled print")
		return
	}
	writeJSON(w, mapScheduledPrint(created))
}

// scheduledUpdateHandler 只改调度参数与打印参数，不允许换文件；换文件请重新创建任务。
func scheduledUpdateHandler(w http.ResponseWriter, r *http.Request) {
	sess, err := auth.GetSession(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
		return
	}

	var payload scheduledUpdatePayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid payload")
		return
	}

	err = appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
		rec, err := store.GetScheduledPrintByID(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if sess.Role != store.RoleAdmin && rec.UserID != sess.UserID {
			return errForbidden
		}
		if err := applyScheduledPayload(&rec, payload); err != nil {
			return err
		}
		if err := ensureScheduleDefaults(&rec); err != nil {
			return err
		}
		next, ok := computeNextRun(rec, time.Now())
		if !ok {
			return errors.New("无法计算下一次执行时间")
		}
		rec.NextRunAt = next.UTC().Format(time.RFC3339)
		if rec.ScheduleType == store.ScheduleOnce {
			rec.RunAt = rec.NextRunAt
		} else {
			rec.RunAt = ""
		}
		rec.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		return store.UpdateScheduledPrint(r.Context(), tx, &rec)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "not found")
			return
		}
		if errors.Is(err, errForbidden) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	var updated store.ScheduledPrint
	if err := appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
		u, err := store.GetScheduledPrintByID(r.Context(), tx, id)
		if err != nil {
			return err
		}
		updated = u
		return nil
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to reload scheduled print")
		return
	}
	writeJSON(w, mapScheduledPrint(updated))
}

func scheduledDeleteHandler(w http.ResponseWriter, r *http.Request) {
	sess, err := auth.GetSession(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
		return
	}

	var stored string
	err = appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
		rec, err := store.GetScheduledPrintByID(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if sess.Role != store.RoleAdmin && rec.UserID != sess.UserID {
			return errForbidden
		}
		stored = rec.StoredPath
		return store.DeleteScheduledPrint(r.Context(), tx, id)
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "not found")
			return
		}
		if errors.Is(err, errForbidden) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to delete scheduled print")
		return
	}
	if stored != "" {
		abs := filepath.Join(uploadDir, filepath.FromSlash(stored))
		_ = os.Remove(abs)
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// scheduledRunNowHandler 触发一次立即执行。执行是同步的，避免用户以为"没反应"，
// 但会带 5 分钟超时（executeScheduled 内部）。
func scheduledRunNowHandler(w http.ResponseWriter, r *http.Request) {
	sess, err := auth.GetSession(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
		return
	}

	var task store.ScheduledPrint
	err = appStore.WithTx(r.Context(), true, func(tx *sql.Tx) error {
		rec, err := store.GetScheduledPrintByID(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if sess.Role != store.RoleAdmin && rec.UserID != sess.UserID {
			return errForbidden
		}
		task = rec
		return nil
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "not found")
			return
		}
		if errors.Is(err, errForbidden) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to load scheduled print")
		return
	}

	status, execErr := executeScheduled(appStore, uploadDir, task)
	errMsg := ""
	if execErr != nil {
		errMsg = execErr.Error()
	}
	_ = appStore.WithTx(r.Context(), false, func(tx *sql.Tx) error {
		return store.UpdateScheduledPrintAfterRun(r.Context(), tx, id,
			time.Now().UTC().Format(time.RFC3339), status, errMsg, task.NextRunAt, task.Enabled)
	})
	if execErr != nil {
		writeJSONError(w, http.StatusInternalServerError, execErr.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "status": status})
}

// scheduledUpdatePayload 是 PUT 允许覆盖的字段。文件与用户不变。
type scheduledUpdatePayload struct {
	Name       string `json:"name"`
	PrinterURI string `json:"printerUri"`
	Enabled    *bool  `json:"enabled"`

	IsDuplex       bool   `json:"isDuplex"`
	IsColor        bool   `json:"isColor"`
	Copies         int    `json:"copies"`
	Orientation    string `json:"orientation"`
	PaperSize      string `json:"paperSize"`
	PaperType      string `json:"paperType"`
	MediaSource    string `json:"mediaSource"`
	PrintScaling   string `json:"printScaling"`
	PageRange      string `json:"pageRange"`
	PageSet        string `json:"pageSet"`
	Mirror         bool   `json:"mirror"`
	WatermarkText  string `json:"watermarkText"`
	NumberUp       int    `json:"numberUp"`
	NumberUpLayout string `json:"numberUpLayout"`
	PageBorder     string `json:"pageBorder"`

	ScheduleType    string `json:"scheduleType"`
	ScheduleTime    string `json:"scheduleTime"`
	ScheduleWeekday int    `json:"scheduleWeekday"`
	ScheduleDay     int    `json:"scheduleDay"`
	RunAt           string `json:"runAt"`
}

// applyScheduledPayload 把更新请求写入记录，并顺带规范化页码范围。
// 页码非法时返回错误，由调用方转成 400 + 中文提示。
func applyScheduledPayload(rec *store.ScheduledPrint, p scheduledUpdatePayload) error {
	// 与创建路径同样的校验：非法页码会让定时任务在后台静默打印整个文档。
	normalizedPageRange, err := pricing.NormalizePageRange(p.PageRange)
	if err != nil {
		return errors.New(msgInvalidPageRange)
	}

	rec.Name = strings.TrimSpace(p.Name)
	rec.PrinterURI = p.PrinterURI
	rec.IsDuplex = p.IsDuplex
	rec.IsColor = p.IsColor
	rec.Copies = p.Copies
	rec.Orientation = p.Orientation
	rec.PaperSize = p.PaperSize
	rec.PaperType = p.PaperType
	rec.MediaSource = p.MediaSource
	rec.PrintScaling = p.PrintScaling
	rec.PageRange = normalizedPageRange
	rec.PageSet = p.PageSet
	rec.Mirror = p.Mirror
	rec.WatermarkText = strings.TrimSpace(p.WatermarkText)
	rec.NumberUp = p.NumberUp
	rec.NumberUpLayout = p.NumberUpLayout
	rec.PageBorder = p.PageBorder
	rec.ScheduleType = p.ScheduleType
	rec.ScheduleTime = p.ScheduleTime
	rec.ScheduleWeekday = p.ScheduleWeekday
	rec.ScheduleDay = p.ScheduleDay
	rec.RunAt = p.RunAt
	if p.Enabled != nil {
		rec.Enabled = *p.Enabled
	}
	return nil
}

func buildScheduledRecordFromForm(r *http.Request, userID int64, filename, storedPath string) (store.ScheduledPrint, error) {
	f := r.MultipartForm.Value
	getStr := func(key string) string {
		if vs, ok := f[key]; ok && len(vs) > 0 {
			return vs[0]
		}
		return ""
	}
	getInt := func(key string, def int) int {
		if s := getStr(key); s != "" {
			if n, err := strconv.Atoi(s); err == nil {
				return n
			}
		}
		return def
	}
	getBool := func(key string) bool {
		return getStr(key) == "true"
	}

	normalizedPageRange, err := pricing.NormalizePageRange(strings.TrimSpace(getStr("page_range")))
	if err != nil {
		return store.ScheduledPrint{}, errors.New(msgInvalidPageRange)
	}

	rec := store.ScheduledPrint{
		UserID:     userID,
		Name:       strings.TrimSpace(getStr("name")),
		PrinterURI: getStr("printer"),
		Filename:   filename,
		StoredPath: storedPath,

		IsDuplex:       getBool("duplex"),
		IsColor:        getBool("color"),
		Copies:         getInt("copies", 1),
		Orientation:    getStr("orientation"),
		PaperSize:      getStr("paper_size"),
		PaperType:      getStr("paper_type"),
		MediaSource:    getStr("media_source"),
		PrintScaling:   getStr("print_scaling"),
		PageRange:      normalizedPageRange,
		PageSet:        getStr("page_set"),
		Mirror:         getBool("mirror"),
		WatermarkText:  strings.TrimSpace(getStr("watermark_text")),
		NumberUp:       getInt("number_up", 1),
		NumberUpLayout: getStr("number_up_layout"),
		PageBorder:     getStr("page_border"),

		ScheduleType:    getStr("schedule_type"),
		ScheduleTime:    getStr("schedule_time"),
		ScheduleWeekday: getInt("schedule_weekday", 0),
		ScheduleDay:     getInt("schedule_day", 1),
		RunAt:           getStr("run_at"),
		Enabled:         true,
	}

	if err := ensureScheduleDefaults(&rec); err != nil {
		return store.ScheduledPrint{}, err
	}

	next, ok := computeNextRun(rec, time.Now())
	if !ok {
		return store.ScheduledPrint{}, fmt.Errorf("无法计算下一次执行时间")
	}
	rec.NextRunAt = next.UTC().Format(time.RFC3339)
	if rec.ScheduleType == store.ScheduleOnce {
		rec.RunAt = rec.NextRunAt
	} else {
		rec.RunAt = ""
	}
	now := time.Now().UTC().Format(time.RFC3339)
	rec.CreatedAt = now
	rec.UpdatedAt = now
	return rec, nil
}

func scheduledBaseDir(base string, userID int64) string {
	return filepath.Join(base, scheduledSubDir, strconv.FormatInt(userID, 10))
}

var errForbidden = errors.New("forbidden")
