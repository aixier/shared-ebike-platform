// Package apperr 定义本服务的统一错误：HTTP 状态 + 错误码 + 一句可直接展示的原因。
// 线上格式照 contracts/wire.md 第 1 节与 _common.yaml 的 Error。
package apperr

import "net/http"

// RowError 是整批校验中的一行错误，row 从 1 起。
type RowError struct {
	Row     int    `json:"row"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Error 是业务错误。Extra 里的字段随错误体一起返回（附加字段）。
type Error struct {
	Status  int
	Code    string
	Message string
	Errors  []RowError
	Extra   map[string]any
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Body 生成线上错误体。
func (e *Error) Body() map[string]any {
	b := map[string]any{"code": e.Code, "message": e.Message}
	for k, v := range e.Extra {
		b[k] = v
	}
	if len(e.Errors) > 0 {
		b["errors"] = e.Errors
	}
	return b
}

func New(status int, code, msg string) *Error {
	return &Error{Status: status, Code: code, Message: msg}
}

func Invalid(msg string) *Error { return New(http.StatusBadRequest, "INVALID", msg) }
func ReasonRequired() *Error {
	return New(http.StatusBadRequest, "REASON_REQUIRED", "请填写操作事由（至少 4 个字）")
}
func NotLoggedIn() *Error {
	return New(http.StatusUnauthorized, "ADMIN_NOT_LOGGED_IN", "登录已失效，请重新登录")
}
func Unauthorized(msg string) *Error { return New(http.StatusUnauthorized, "UNAUTHORIZED", msg) }
func Forbidden() *Error {
	return New(http.StatusForbidden, "FORBIDDEN", "当前账号没有这项操作的权限")
}
func ScopeDenied() *Error {
	return New(http.StatusForbidden, "SCOPE_DENIED", "只能查看和操作本运营商的数据")
}
func Readonly() *Error {
	return New(http.StatusForbidden, "READONLY", "运营商已退出，账号只读，不能修改")
}
func NotFound(what string) *Error {
	return New(http.StatusNotFound, "NOT_FOUND", "没有找到该"+what)
}
func Conflict(msg string) *Error { return New(http.StatusConflict, "CONFLICT", msg) }
func AlreadyDone(msg string) *Error {
	return New(http.StatusConflict, "ALREADY_DONE", msg)
}
func Internal() *Error {
	return New(http.StatusInternalServerError, "SERVER_BUSY", "服务繁忙，请稍后再试")
}
