package app

import (
	"encoding/json"
	"errors"
	"net/http"
)

// apiError 统一错误体 {code, message, ...}（_common.yaml Error）。
type apiError struct {
	Status  int
	Code    string
	Message string
	Extra   map[string]any
}

func (e *apiError) Error() string { return e.Code + "：" + e.Message }

func newErr(status int, code, msg string) *apiError {
	return &apiError{Status: status, Code: code, Message: msg}
}

func (e *apiError) with(k string, v any) *apiError {
	if e.Extra == nil {
		e.Extra = map[string]any{}
	}
	e.Extra[k] = v
	return e
}

var (
	errNotLoggedIn = func() *apiError {
		return newErr(401, "NOT_LOGGED_IN", "请先授权登录").with("next", next("一键授权", "login"))
	}
	errAdminNotLoggedIn = func() *apiError {
		return newErr(401, "ADMIN_NOT_LOGGED_IN", "登录已失效，请重新登录").with("next", next("去登录", "login"))
	}
	errForbidden   = func() *apiError { return newErr(403, "FORBIDDEN", "你的账号没有该操作的权限") }
	errScopeDenied = func() *apiError { return newErr(403, "SCOPE_DENIED", "只能查看和操作本运营商的数据") }
	errBusy        = func(msg string) *apiError {
		return newErr(503, "SERVER_BUSY", msg).with("next", next("重试", "retry"))
	}
	errInvalid       = func(msg string) *apiError { return newErr(400, "INVALID", msg) }
	errResultUnknown = func() *apiError {
		return newErr(409, "CONFLICT", "支付结果还没有返回，请稍后查看押金状态").with("next", next("刷新", "retry"))
	}
	errInternal = func() *apiError {
		return newErr(500, "INTERNAL", "服务出了点问题，请稍后再试").with("next", next("重试", "retry"))
	}
)

// writeJSON 写 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr 写错误体；userSide 为真时带 charged:false（用户侧失败必有）。
func writeErr(w http.ResponseWriter, err error, userSide bool) {
	var e *apiError
	if !errors.As(err, &e) {
		e = errInternal()
	}
	body := map[string]any{}
	for k, v := range e.Extra {
		body[k] = v
	}
	body["code"], body["message"] = e.Code, e.Message
	if userSide {
		if _, ok := body["charged"]; !ok {
			body["charged"] = false
		}
	}
	writeJSON(w, e.Status, body)
}
