package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"unicode/utf8"
)

// apiError 统一错误体 {code, message, ...}（contracts/openapi/_common.yaml Error）。
type apiError struct {
	Status  int
	Code    string
	Message string
	Extra   map[string]any
}

func (e *apiError) Error() string { return e.Code + ": " + e.Message }

func errf(status int, code, msg string) *apiError {
	return &apiError{Status: status, Code: code, Message: msg}
}

func (e *apiError) with(k string, v any) *apiError {
	if e.Extra == nil {
		e.Extra = map[string]any{}
	}
	e.Extra[k] = v
	return e
}

func (e *apiError) body() map[string]any {
	m := map[string]any{"code": e.Code, "message": e.Message}
	for k, v := range e.Extra {
		m[k] = v
	}
	return m
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// toResponse 把处理结果或错误转成状态码与报文。未知错误不暴露内部细节。
func toResponse(v any, err error) (int, any) {
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) {
			return ae.Status, ae.body()
		}
		return http.StatusInternalServerError, map[string]any{"code": "SERVER_BUSY", "message": "服务暂时不可用，请稍后重试"}
	}
	return http.StatusOK, v
}

// checkReason 事由至少 4 个字。
func checkReason(r string) error {
	if utf8.RuneCountInString(r) < 4 {
		return errf(400, "REASON_REQUIRED", "请填写事由，至少 4 个字")
	}
	return nil
}

var (
	errNotUTF8     = errf(400, "INVALID", "请求体不是 UTF-8 编码")
	errNotLoggedIn = errf(401, "ADMIN_NOT_LOGGED_IN", "登录已失效，请重新登录")
	errForbidden   = errf(403, "FORBIDDEN", "当前账号没有这项操作的权限")
	errReadonly    = errf(403, "READONLY", "运营商已退出，账号只读，不能做这项操作")
	errScope       = errf(403, "SCOPE_DENIED", "只能查看和操作本运营商的数据")
)
