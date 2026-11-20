package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"bike/enforce/internal/auth"
)

// apiErr 是返回给页面的业务错误，按 _common.yaml 的 Error 输出。
type apiErr struct {
	status int
	code   string
	msg    string
}

func (e *apiErr) Error() string { return e.code + "：" + e.msg }

func errInvalid(msg string) *apiErr { return &apiErr{http.StatusBadRequest, "INVALID", msg} }
func errNotFound(what string) *apiErr {
	return &apiErr{http.StatusNotFound, "NOT_FOUND", "没有找到该" + what}
}

var (
	errNotLoggedIn    = &apiErr{http.StatusUnauthorized, "ADMIN_NOT_LOGGED_IN", "登录已失效，请重新登录"}
	errLocked         = &apiErr{http.StatusConflict, "EVIDENCE_LOCKED", "证据提交后不可修改或删除"}
	errReadonly       = &apiErr{http.StatusForbidden, "READONLY", "运营商已退出，账号为只读，不能提交或修改"}
	errReasonRequired = &apiErr{http.StatusBadRequest, "REASON_REQUIRED", "请填写操作事由（至少 4 个字）"}
)

func errForbidden(role string) *apiErr {
	if role == "" {
		role = "未知岗位"
	}
	return &apiErr{http.StatusForbidden, "FORBIDDEN", "你的账号（" + role + "）没有该操作的权限"}
}

func errScopeDenied() *apiErr {
	return &apiErr{http.StatusForbidden, "SCOPE_DENIED", "只能查看和操作本运营商的数据"}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeRaw(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// wrap 把处理函数返回的错误转成统一错误体；未知错误记日志并返回 SERVER_BUSY。
func (a *App) wrap(f func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := f(w, r)
		if err == nil {
			return
		}
		var ae *apiErr
		if errors.As(err, &ae) {
			writeJSON(w, ae.status, map[string]string{"code": ae.code, "message": ae.msg})
			return
		}
		a.Log.Error("接口内部异常", "path", r.URL.Path, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"code": "SERVER_BUSY", "message": "服务繁忙，请稍后再试"})
	}
}

// ---------- 鉴权 ----------

const roleOfficer = "执法人员"

func (a *App) admin(r *http.Request) (auth.Admin, error) {
	h := r.Header.Get("Authorization")
	tok, ok := strings.CutPrefix(h, "Bearer ")
	if !ok {
		return auth.Admin{}, errNotLoggedIn
	}
	adm, err := a.Verifier.Verify(strings.TrimSpace(tok))
	if err != nil {
		return auth.Admin{}, errNotLoggedIn
	}
	return adm, nil
}

// officer 要求执法人员账号（执法端接口没有权限点，按岗位判断）。
func (a *App) officer(r *http.Request, write bool) (auth.Admin, error) {
	adm, err := a.admin(r)
	if err != nil {
		return adm, err
	}
	if adm.Role != roleOfficer {
		return adm, errForbidden(adm.Role)
	}
	if write {
		if err := a.checkWritable(r.Context(), adm); err != nil {
			return adm, err
		}
	}
	return adm, nil
}

// checkWritable 判只读：令牌 readonly 为真，或令牌 scope 对应的运营商已退出（以订阅 fleet.operator.exited 的投影为准，不等令牌刷新）。
func (a *App) checkWritable(ctx context.Context, adm auth.Admin) error {
	if adm.Readonly {
		return errReadonly
	}
	if adm.Scope != "" {
		var exited bool
		err := a.Pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM operators WHERE operator_id=$1 AND status='已退出')", adm.Scope).Scan(&exited)
		if err != nil {
			return err
		}
		if exited {
			return errReadonly
		}
	}
	return nil
}

// perm 要求持有任一权限点。
func (a *App) perm(r *http.Request, write bool, perms ...string) (auth.Admin, error) {
	adm, err := a.admin(r)
	if err != nil {
		return adm, err
	}
	if !adm.HasAnyPerm(perms...) {
		return adm, errForbidden(adm.Role)
	}
	if write {
		if err := a.checkWritable(r.Context(), adm); err != nil {
			return adm, err
		}
	}
	return adm, nil
}

// RequireCaller 校验 /internal 调用方的服务凭证（本服务目前没有 /internal 接口，供以后挂载）。
func (a *App) RequireCaller(next http.Handler) http.Handler {
	return a.wrap(func(w http.ResponseWriter, r *http.Request) error {
		key, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if a.Callers == nil || !a.Callers.Allow(r.Header.Get("X-Caller"), strings.TrimSpace(key)) {
			return &apiErr{http.StatusUnauthorized, "UNAUTHORIZED", "服务凭证不对"}
		}
		next.ServeHTTP(w, r)
		return nil
	})
}

func decodeJSON(r *http.Request, v any) error {
	b, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err != nil {
		return errInvalid("请求内容不是有效的 JSON")
	}
	if !utf8.Valid(b) {
		return errInvalid("请求体不是 UTF-8 编码")
	}
	if err := json.Unmarshal(b, v); err != nil {
		return errInvalid("请求内容不是有效的 JSON")
	}
	return nil
}
