package app

// 错误体与 JSON 输出（wire.md 第 1 节；错误码与文案沿用原型 errors.js）。

import (
	"encoding/json"
	"net/http"
	"time"
)

// APIError 统一错误
type APIError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string { return e.Code + "：" + e.Message }

var defaultMsg = map[string]struct {
	status int
	msg    string
}{
	"INVALID":             {400, "请求参数有误"},
	"REASON_REQUIRED":     {400, "请填写操作事由（至少 4 个字）"},
	"PAYEE_NAME_MISMATCH": {400, "收款账户须为本人实名账户，户名与实名信息不一致"},
	"NOT_LOGGED_IN":       {401, "请先授权登录"},
	"ADMIN_NOT_LOGGED_IN": {401, "登录已失效，请重新登录"},
	"UNAUTHORIZED":        {401, "服务凭证无效"},
	"FORBIDDEN":           {403, "你的账号没有该操作的权限"},
	"SCOPE_DENIED":        {403, "只能查看和操作本运营商的数据"},
	"READONLY":            {403, "运营商已退出，账号为只读，不能提交或修改"},
	"NOT_FOUND":           {404, "没有找到对应的记录"},
	"CONFLICT":            {409, "当前状态不允许该操作"},
	"ALREADY_DONE":        {409, "已经办理过了，无需重复操作"},
	"SERVER_BUSY":         {503, "服务繁忙，请稍后再试"},
	"INTERNAL":            {500, "服务内部异常，请稍后再试"},
}

// apiErr 生成错误；msg 为空用默认文案
func apiErr(code, msg string) *APIError {
	d := defaultMsg[code]
	if msg == "" {
		msg = d.msg
	}
	st := d.status
	if st == 0 {
		st = 400
	}
	return &APIError{Status: st, Code: code, Message: msg}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, e *APIError) {
	body := map[string]any{"code": e.Code, "message": e.Message}
	if e.Code == "NOT_LOGGED_IN" {
		body["charged"] = false
		body["next"] = map[string]string{"label": "一键授权", "action": "login"}
	}
	if e.Code == "ADMIN_NOT_LOGGED_IN" {
		body["next"] = map[string]string{"label": "去登录", "action": "login"}
	}
	if e.Code == "SERVER_BUSY" {
		body["next"] = map[string]string{"label": "重试", "action": "retry"}
	}
	writeJSON(w, e.Status, body)
}

var bj = time.FixedZone("CST", 8*3600)

func fmtTime(t time.Time) string { return t.In(bj).Format(time.RFC3339) }

func fmtTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := fmtTime(*t)
	return &s
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func dateOf(t time.Time) string { return t.In(bj).Format("2006-01-02") }

func dayStart(date string) (time.Time, error) { return time.ParseInLocation("2006-01-02", date, bj) }
