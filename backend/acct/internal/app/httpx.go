package app

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
	"unicode/utf8"
)

// hfn 接口处理函数：返回响应体或错误（*apiError 为业务错误）
type hfn func(r *http.Request) (any, error)

// apiError 统一错误体，见 _common.yaml Error
type apiError struct {
	status   int
	Code     string    `json:"code"`
	Message  string    `json:"message"`
	Next     *next     `json:"next,omitempty"`
	Blockers []blocker `json:"blockers,omitempty"`
}

func (e *apiError) Error() string { return e.Code + ": " + e.Message }

type next struct {
	Label  string         `json:"label"`
	Action string         `json:"action"`
	Params map[string]any `json:"params,omitempty"`
}

// 错误码的默认原因（原型 API.md 第 2 节）
var defaultMessages = map[string]string{
	"INVALID":                "填写的内容有误",
	"REASON_REQUIRED":        "请填写操作事由（至少 4 个字）",
	"NOT_FOUND":              "没有找到该用户",
	"CONFLICT":               "记录状态已变化",
	"ALREADY_DONE":           "已经办理过了",
	"SERVER_BUSY":            "服务繁忙，请稍后再试",
	"NOT_LOGGED_IN":          "请先授权登录",
	"AUTH_UNAVAILABLE":       "微信或支付宝授权暂时不可用",
	"ACCOUNT_CANCELLED":      "该账户已注销",
	"REALNAME_INVALID_ID":    "证件号码格式不正确",
	"REALNAME_NAME_MISMATCH": "姓名与证件信息不一致",
	"UNDER_AGE":              "未满 16 周岁，不能骑行电动自行车",
	"CANCEL_BLOCKED":         "注销前还需处理待办事项",
	"ADMIN_NOT_LOGGED_IN":    "登录已失效，请重新登录",
	"ACCOUNT_DISABLED":       "该账号已停用",
	"FORBIDDEN":              "你的账号没有该操作的权限",
}

// HTTP 状态：见 acct.yaml 说明；其余业务错误码为 409
func statusOf(code string) int {
	switch code {
	case "INVALID", "REASON_REQUIRED":
		return 400
	case "NOT_LOGGED_IN", "ADMIN_NOT_LOGGED_IN":
		return 401
	case "FORBIDDEN", "READONLY", "ACCOUNT_DISABLED", "SCOPE_DENIED":
		return 403
	case "NOT_FOUND":
		return 404
	}
	return 409
}

func errf(code, msg string) *apiError {
	if msg == "" {
		msg = defaultMessages[code]
	}
	return &apiError{status: statusOf(code), Code: code, Message: msg}
}

// raw 已渲染好的响应（幂等重放用）
type raw struct {
	status int
	body   []byte
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, _ := json.Marshal(v)
	writeRaw(w, status, b)
}

func writeRaw(w http.ResponseWriter, status int, b []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func (a *App) render(res any, err error) (int, []byte) {
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) {
			b, _ := json.Marshal(ae)
			return ae.status, b
		}
		a.Log.Error("内部异常", "err", err)
		b, _ := json.Marshal(errf("SERVER_BUSY", ""))
		return 500, b
	}
	if r, ok := res.(raw); ok {
		return r.status, r.body
	}
	b, _ := json.Marshal(res)
	return 200, b
}

func (a *App) serve(h hfn) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st, b := a.render(h(r))
		writeRaw(w, st, b)
	}
}

// decode 读 JSON 请求体；optional 时允许空体
func decode(r *http.Request, v any, optional bool) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return errf("INVALID", "请求体读取失败")
	}
	if !utf8.Valid(b) {
		return errf("INVALID", "请求体不是 UTF-8 编码")
	}
	if len(b) == 0 {
		if optional {
			return nil
		}
		return errf("INVALID", "缺少请求体")
	}
	if err := json.Unmarshal(b, v); err != nil {
		return errf("INVALID", "请求体不是合法的 JSON")
	}
	return nil
}

// withIdem 幂等键：同一个键重复到达返回第一次的结果，不重复执行。
// 先占位再执行，并发重复到达时后到者返回 CONFLICT；服务内部异常不保存结果，可用同一键重试。
func (a *App) withIdem(op string, scope func(*http.Request) string, required bool, h hfn) hfn {
	return func(r *http.Request) (any, error) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			if required {
				return nil, errf("INVALID", "命令须带幂等键 Idempotency-Key")
			}
			return h(r)
		}
		if len(key) > 64 {
			return nil, errf("INVALID", "幂等键不能超过 64 个字符")
		}
		ctx := r.Context()
		sc := op + ":" + scope(r)
		tag, err := a.Pool.Exec(ctx, `INSERT INTO idempotency (scope, key, created_at) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, sc, key, a.Now())
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			var st int
			var body []byte
			if err := a.Pool.QueryRow(ctx, `SELECT status, body FROM idempotency WHERE scope=$1 AND key=$2`, sc, key).Scan(&st, &body); err != nil {
				return nil, err
			}
			if st == 0 {
				return nil, errf("CONFLICT", "同一请求正在处理，请稍后查看结果")
			}
			return raw{status: st, body: body}, nil
		}
		st, body := a.render(h(r))
		if st >= 500 {
			_, _ = a.Pool.Exec(ctx, `DELETE FROM idempotency WHERE scope=$1 AND key=$2`, sc, key)
		} else {
			_, _ = a.Pool.Exec(ctx, `UPDATE idempotency SET status=$3, body=$4 WHERE scope=$1 AND key=$2`, sc, key, st, body)
		}
		return raw{status: st, body: body}, nil
	}
}

func (a *App) idempotent(op string, scope func(*http.Request) string, h hfn) http.HandlerFunc {
	return a.serve(a.withIdem(op, scope, false, h))
}

func (a *App) idempotentUser(op string, h hfn) hfn {
	return a.withIdem(op, func(r *http.Request) string { return userIDFrom(r) }, false, h)
}

// fmtTime RFC 3339，北京时间
func fmtTime(t time.Time) string { return t.In(cst).Format(time.RFC3339) }

func fmtTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return fmtTime(*t)
}
