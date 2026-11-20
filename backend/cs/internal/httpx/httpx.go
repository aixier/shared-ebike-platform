// Package httpx 提供本服务的 HTTP 小工具：统一错误体、JSON 读写。
// trace: contracts/wire.md 第 1 节；contracts/openapi/_common.yaml Error
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

// Error 是对外的统一错误。Extra 中的字段平铺进错误体（如 blockers、next）。
type Error struct {
	Code    string
	Message string
	Extra   map[string]any
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// E 构造一个错误。
func E(code, msg string) *Error { return &Error{Code: code, Message: msg} }

// StatusOf 按 wire.md 把错误码映射到 HTTP 状态。
func StatusOf(code string) int {
	switch code {
	case "INVALID", "REASON_REQUIRED":
		return http.StatusBadRequest
	case "NOT_LOGGED_IN", "ADMIN_NOT_LOGGED_IN":
		return http.StatusUnauthorized
	case "FORBIDDEN", "SCOPE_DENIED", "READONLY", "ACCOUNT_DISABLED":
		return http.StatusForbidden
	case "NOT_FOUND":
		return http.StatusNotFound
	case "SERVER_BUSY", "INTERNAL":
		return http.StatusInternalServerError
	}
	return http.StatusConflict
}

// WriteJSON 写 JSON 响应。
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ErrorBody 把错误转成状态码与错误体；不是 *Error 的错误一律按内部异常处理，不外露细节。
func ErrorBody(err error) (int, map[string]any) {
	var e *Error
	if !errors.As(err, &e) {
		slog.Error("内部异常", "err", err)
		e = &Error{Code: "INTERNAL", Message: "服务暂时出了点问题，请稍后再试"}
	}
	body := map[string]any{}
	for k, v := range e.Extra {
		body[k] = v
	}
	body["code"] = e.Code
	body["message"] = e.Message
	return StatusOf(e.Code), body
}

// WriteError 写错误体。
func WriteError(w http.ResponseWriter, err error) {
	status, body := ErrorBody(err)
	WriteJSON(w, status, body)
}

// DecodeJSON 读请求体；格式不对返回 INVALID。
func DecodeJSON(r *http.Request, v any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return E("INVALID", "请求读取失败")
	}
	if !utf8.Valid(b) {
		return E("INVALID", "请求体不是 UTF-8 编码（命令行直传中文会被转成本地代码页，请改用文件传）")
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return E("INVALID", "请求体不能为空")
	}
	if err := json.Unmarshal(b, v); err != nil {
		return E("INVALID", "请求格式不对")
	}
	return nil
}
