// Package httpx 是错误体、JSON 读写与事由校验（contracts/wire.md 第 1 节）。
package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"
)

// Error 统一错误：状态码 + code + message + 附加字段。
type Error struct {
	Status  int
	Code    string
	Message string
	Extra   map[string]any
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// New 构造错误。
func New(status int, code, msg string) *Error {
	return &Error{Status: status, Code: code, Message: msg}
}

// With 加一个附加字段。
func (e *Error) With(k string, v any) *Error {
	if e.Extra == nil {
		e.Extra = map[string]any{}
	}
	e.Extra[k] = v
	return e
}

// 常用错误。
func Invalid(msg string) *Error   { return New(400, "INVALID", msg) }
func NotFound(msg string) *Error  { return New(404, "NOT_FOUND", msg) }
func Conflict(msg string) *Error  { return New(409, "CONFLICT", msg) }
func Forbidden(msg string) *Error { return New(403, "FORBIDDEN", msg) }
func ReasonRequired() *Error      { return New(400, "REASON_REQUIRED", "请填写事由，至少 4 个字") }
func ServerBusy(msg string) *Error {
	if msg == "" {
		msg = "服务繁忙，请稍后重试"
	}
	// wire.md：除 400/401/403/404/500 外业务上不允许的一律 409；swagger 也只给了 409 响应
	return New(409, "SERVER_BUSY", msg)
}

// WriteJSON 写 JSON 响应。
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError 写错误体；非 *Error 的一律 500，不暴露内部细节。
func WriteError(w http.ResponseWriter, err error) {
	var e *Error
	if !errors.As(err, &e) {
		slog.Error("内部错误", "err", err)
		e = New(500, "INTERNAL", "服务暂时不可用，请稍后重试")
	}
	body := map[string]any{}
	for k, v := range e.Extra {
		body[k] = v
	}
	body["code"] = e.Code
	body["message"] = e.Message
	WriteJSON(w, e.Status, body)
}

// Decode 读请求体 JSON；空请求体当作空对象。
func Decode(r *http.Request, v any) *Error {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return Invalid("请求体读取失败")
	}
	if !utf8.Valid(b) {
		return Invalid("请求体不是 UTF-8 编码")
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, v); err != nil {
		return Invalid("请求格式不对，请检查后重试")
	}
	return nil
}

// ValidReason 事由至少 4 个字（去掉首尾空白后按字符数）。
func ValidReason(s string) bool { return utf8.RuneCountInString(strings.TrimSpace(s)) >= 4 }
