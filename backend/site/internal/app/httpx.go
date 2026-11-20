package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"bike/site/internal/auth"
)

// apiErr 业务错误，按 contracts/openapi/_common.yaml 的 Error 输出
type apiErr struct {
	Status  int
	Code    string
	Message string
	Extra   map[string]any
}

func (e *apiErr) Error() string { return e.Code + ": " + e.Message }

// 错误码的缺省状态码与说明（沿用原型 errors.js）
var errDefaults = map[string]struct {
	status int
	msg    string
}{
	"INVALID":             {400, "填写的内容有误，请检查后再提交"},
	"REASON_REQUIRED":     {400, "请填写操作事由（至少 4 个字）"},
	"ADMIN_NOT_LOGGED_IN": {401, "登录已失效，请重新登录"},
	"DEVICE_UNAUTHORIZED": {401, "设备密钥无效"},
	"FORBIDDEN":           {403, "你的账号没有该操作的权限"},
	"SCOPE_DENIED":        {403, "只能查看和操作本运营商的数据"},
	"READONLY":            {403, "运营商已退出，账号为只读，不能提交或修改"},
	"NOT_FOUND":           {404, "没有找到对应的记录"},
	"CONFLICT":            {409, "记录状态已变化，请刷新后再操作"},
	"ALREADY_DONE":        {409, "已经办理过了，无需重复操作"},
	"STATION_IN_USE":      {409, "站点仍在使用，暂不能删除"},
	"SERVER_BUSY":         {500, "服务繁忙，请稍后再试"},
}

// fail 按错误码构造；msg 为空用缺省说明
func fail(code, msg string, extra ...map[string]any) *apiErr {
	d, ok := errDefaults[code]
	if !ok {
		d.status = 409
	}
	if msg == "" {
		msg = d.msg
	}
	e := &apiErr{Status: d.status, Code: code, Message: msg}
	if len(extra) > 0 {
		e.Extra = extra[0]
	}
	return e
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr 非 2xx 一律返回 Error 结构；内部异常不暴露细节
func (a *App) writeErr(w http.ResponseWriter, err error) {
	var e *apiErr
	if !errors.As(err, &e) {
		a.log.Error("内部异常", "err", err)
		e = fail("SERVER_BUSY", "")
		e.Extra = map[string]any{"next": map[string]any{"label": "重试", "action": "retry"}}
	}
	body := map[string]any{}
	for k, v := range e.Extra {
		body[k] = v
	}
	body["code"] = e.Code
	body["message"] = e.Message
	writeJSON(w, e.Status, body)
}

// readBody 读请求体（上限 1 MiB）；不是 UTF-8 编码的返回 INVALID
func readBody(r *http.Request) ([]byte, error) {
	raw, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err != nil {
		return nil, fail("INVALID", "请求体读取失败或超过 1 MiB")
	}
	if !utf8.Valid(raw) {
		return nil, fail("INVALID", "请求体不是 UTF-8 编码")
	}
	return raw, nil
}

// decode 读 JSON 请求体
func decode(r *http.Request, v any) error {
	raw, err := readBody(r)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fail("INVALID", "请求体不是合法的 JSON")
	}
	return nil
}

// checkReason 事由至少 4 个字
func checkReason(r string) (string, error) {
	r = strings.TrimSpace(r)
	if utf8.RuneCountInString(r) < 4 {
		return "", fail("REASON_REQUIRED", "")
	}
	return r, nil
}

type adminCtxKey struct{}

func adminFrom(ctx context.Context) auth.Admin {
	a, _ := ctx.Value(adminCtxKey{}).(auth.Admin)
	return a
}

func actorOf(adm auth.Admin) Actor { return Actor{Kind: "人", ID: adm.AccountID, Name: adm.Name} }

// adminOnly 验管理令牌与权限点；write 为真时拒绝只读与运营商数据范围账号，并按幂等键去重
func (a *App) adminOnly(perms []string, write bool, h http.HandlerFunc) http.HandlerFunc {
	return a.adminGuard(perms, write, false, h)
}

// adminGuard 同 adminOnly；noReplay 为真时不做幂等重放（响应里有一次性密钥，不能留在幂等记录里）
func (a *App) adminGuard(perms []string, write, noReplay bool, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		adm, err := a.admin.Verify(r.Header.Get("Authorization"))
		if err != nil {
			a.writeErr(w, fail("ADMIN_NOT_LOGGED_IN", ""))
			return
		}
		if !adm.HasAny(perms...) {
			a.writeErr(w, fail("FORBIDDEN", ""))
			return
		}
		if write && adm.Readonly {
			a.writeErr(w, fail("READONLY", ""))
			return
		}
		// 站点与桩不属于任何运营商，带数据范围的账号不能改
		if write && adm.Scope != "" {
			a.writeErr(w, fail("SCOPE_DENIED", ""))
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), adminCtxKey{}, adm))
		if write && !noReplay {
			a.idempotent(adm.AccountID, h)(w, r)
			return
		}
		h(w, r)
	}
}

// recorder 记下响应以便幂等重放
type recorder struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (rc *recorder) WriteHeader(s int)           { rc.status = s; rc.ResponseWriter.WriteHeader(s) }
func (rc *recorder) Write(b []byte) (int, error) { rc.buf.Write(b); return rc.ResponseWriter.Write(b) }

// idempotent 同一个 Idempotency-Key 重复到达返回第一次的结果；内部异常（5xx）不记，允许重试
func (a *App) idempotent(who string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if key == "" {
			h(w, r)
			return
		}
		if len(key) > 64 {
			a.writeErr(w, fail("INVALID", "Idempotency-Key 不能超过 64 个字符"))
			return
		}
		scope := who + " " + r.Method + " " + r.URL.Path
		var status int
		var body []byte
		err := a.db.QueryRow(r.Context(), `SELECT status, body FROM idempotency WHERE scope=$1 AND key=$2`, scope, key).Scan(&status, &body)
		if err == nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Idempotent-Replay", "true")
			w.WriteHeader(status)
			_, _ = w.Write(body)
			return
		}
		rc := &recorder{ResponseWriter: w, status: 200}
		h(rc, r)
		if rc.status < 500 {
			if _, err := a.db.Exec(context.WithoutCancel(r.Context()), `INSERT INTO idempotency(scope, key, status, body) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`, scope, key, rc.status, rc.buf.Bytes()); err != nil {
				a.log.Error("幂等记录写入失败", "err", err)
			}
		}
	}
}
