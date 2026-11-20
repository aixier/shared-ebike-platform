package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"bike/rule/internal/auth"
	"bike/rule/internal/domain"
)

// apiError 非 2xx 的统一错误体（contracts/openapi/_common.yaml Error）。
type apiError struct {
	Status  int                 `json:"-"`
	Code    string              `json:"code"`
	Message string              `json:"message"`
	Errors  []domain.FieldError `json:"errors,omitempty"`
}

func (e *apiError) Error() string { return e.Code + ": " + e.Message }

func errNotLoggedIn() *apiError {
	return &apiError{Status: 401, Code: "ADMIN_NOT_LOGGED_IN", Message: "登录已失效，请重新登录"}
}

func errForbidden(role string) *apiError {
	if role == "" {
		role = "当前岗位"
	}
	return &apiError{Status: 403, Code: "FORBIDDEN", Message: fmt.Sprintf("你的账号（%s）没有该操作的权限", role)}
}

func errReadonly() *apiError {
	return &apiError{Status: 403, Code: "READONLY", Message: "账号为只读，不能提交或修改"}
}

func errNotFound(what string) *apiError {
	return &apiError{Status: 404, Code: "NOT_FOUND", Message: "没有找到该" + what}
}

func errReasonRequired() *apiError {
	return &apiError{Status: 400, Code: "REASON_REQUIRED", Message: "请填写操作事由（至少 4 个字）"}
}

func errConflict(msg string) *apiError {
	return &apiError{Status: 409, Code: "CONFLICT", Message: msg}
}

func errInvalid(errs []domain.FieldError) *apiError {
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		msgs = append(msgs, e.Message)
	}
	return &apiError{Status: 400, Code: "INVALID", Message: strings.Join(msgs, "；"), Errors: errs}
}

func errInvalidMsg(field, msg string) *apiError {
	return errInvalid([]domain.FieldError{{Field: field, Message: msg}})
}

func errCaller() *apiError {
	return &apiError{Status: 401, Code: "UNAUTHORIZED", Message: "调用方凭证无效"}
}

var errBusy = &apiError{Status: 500, Code: "SERVER_BUSY", Message: "服务繁忙，请稍后再试"}

func writeJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// writeErr 写错误体；非 apiError 记日志后按 SERVER_BUSY 返回，不暴露内部细节。
func writeErr(w http.ResponseWriter, log *slog.Logger, err error) {
	var ae *apiError
	if !errors.As(err, &ae) {
		if log != nil {
			log.Error("内部错误", "err", err)
		}
		ae = errBusy
	}
	b, _ := json.Marshal(ae)
	writeJSON(w, ae.Status, b)
}

// maxBody 请求体上限。
const maxBody = 1 << 20

// decodeBody 把请求体解成对象，数字保留为 json.Number 以便校验小数位。
func decodeBody(r *http.Request) (map[string]any, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		return nil, errInvalidMsg("", "请求格式不正确")
	}
	if !utf8.Valid(b) {
		return nil, errInvalidMsg("", "请求体不是 UTF-8 编码")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, errInvalidMsg("", "请求格式不正确，应为 JSON 对象")
	}
	return m, nil
}

// adminGuard 校验管理令牌与权限点。perms 为空表示登录即可；列出多个时有其一即可；write 为真时拒绝只读账号。
func (s *Service) adminGuard(r *http.Request, perms []string, write bool) (*auth.Admin, error) {
	tok, ok := auth.Bearer(r)
	if !ok {
		return nil, errNotLoggedIn()
	}
	a, err := s.Admin.Verify(tok)
	if err != nil {
		return nil, errNotLoggedIn()
	}
	if len(perms) > 0 {
		allowed := false
		for _, p := range perms {
			if a.Has(p) {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, errForbidden(a.Role)
		}
	}
	if write && a.Readonly {
		return nil, errReadonly()
	}
	return a, nil
}

// maxIdemKey 幂等键长度上限（_common.yaml IdempotencyKey）。
const maxIdemKey = 64

func idemKey(r *http.Request) (string, error) {
	k := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(k) > maxIdemKey {
		return "", errInvalidMsg("Idempotency-Key", "幂等键不能超过 64 个字符")
	}
	return k, nil
}
