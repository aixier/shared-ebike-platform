package app

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"bike/ops/internal/auth"
	"bike/ops/internal/httpx"
)

type ctxKey struct{}

func adminFrom(r *http.Request) *auth.Admin { a, _ := r.Context().Value(ctxKey{}).(*auth.Admin); return a }

func actorOf(a *auth.Admin) Actor { return Actor{Kind: "人", ID: a.AccountID, Name: a.Name} }

// guard 验管理令牌与权限点（有其一即可）；write 为真时只读账号拒绝。
func (a *App) guard(perms []string, write bool, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || a.Verifier == nil {
			httpx.WriteError(w, httpx.New(401, "ADMIN_NOT_LOGGED_IN", "请先登录"))
			return
		}
		ad, err := a.Verifier.Verify(strings.TrimSpace(tok))
		if err != nil {
			httpx.WriteError(w, httpx.New(401, "ADMIN_NOT_LOGGED_IN", "登录已失效，请重新登录"))
			return
		}
		if len(perms) > 0 && !ad.HasAny(perms...) {
			httpx.WriteError(w, httpx.Forbidden("没有该操作的权限"))
			return
		}
		if write && ad.Readonly {
			httpx.WriteError(w, httpx.New(403, "READONLY", "当前账号为只读，不能执行该操作"))
			return
		}
		if write && ad.Scope != "" && a.operatorExited(r.Context(), ad.Scope) {
			httpx.WriteError(w, httpx.New(403, "READONLY", "该运营商已退出，只能查看"))
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, ad)))
	}
}

// operatorExited 该运营商名下品牌全部已退出（订阅 fleet.operator.exited 的投影）。
func (a *App) operatorExited(ctx context.Context, operatorID string) bool {
	var total, active int
	if err := a.DB.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE access_status <> '已退出') FROM brands WHERE operator_id = $1`, operatorID).Scan(&total, &active); err != nil {
		return false
	}
	return total > 0 && active == 0
}

// recorder 记下响应以便幂等重放。
type recorder struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (r *recorder) WriteHeader(s int) { r.status = s; r.ResponseWriter.WriteHeader(s) }
func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = 200
	}
	r.buf.Write(b)
	return r.ResponseWriter.Write(b)
}

// idempotent 同一登录人、同一接口、同一 Idempotency-Key 返回第一次的结果，不重复执行（5xx 不记）。
func (a *App) idempotent(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			h(w, r)
			return
		}
		if len(key) > 64 {
			httpx.WriteError(w, httpx.Invalid("幂等键过长"))
			return
		}
		scope := r.Method + " " + r.URL.Path
		if ad := adminFrom(r); ad != nil {
			scope = ad.AccountID + " " + scope
		}
		var status int
		var body []byte
		err := a.DB.QueryRow(r.Context(), `SELECT status, body FROM idempotency WHERE scope = $1 AND key = $2`, scope, key).Scan(&status, &body)
		if err == nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(status)
			_, _ = w.Write(body)
			return
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, err)
			return
		}
		rec := &recorder{ResponseWriter: w}
		h(rec, r)
		if rec.status != 0 && rec.status < 500 {
			_, _ = a.DB.Exec(context.WithoutCancel(r.Context()), `INSERT INTO idempotency (scope, key, status, body) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
				scope, key, rec.status, rec.buf.Bytes())
		}
	}
}

func reply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, 200, v)
}

func intParam(r *http.Request, name string, def, min, max int) (int, error) {
	s := r.URL.Query().Get(name)
	if s == "" {
		return def, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < min || v > max {
		return 0, httpx.Invalid(name + " 取值超出范围")
	}
	return v, nil
}

type reasonBody struct {
	Reason string `json:"reason"`
}

// Handler 全部路由。
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	const (
		monitor   = "ops.monitor"
		control   = "degrade.control"
		clView    = "degrade.closure.view"
		clHandle  = "degrade.closure.handle"
		pausePerm = "access.pause"
	)
	p := func(ps ...string) []string { return ps }

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { httpx.WriteJSON(w, 200, map[string]bool{"ok": true}) })

	mux.HandleFunc("GET /api/ops/monitor", a.guard(p(monitor), false, func(w http.ResponseWriter, r *http.Request) {
		hours, err := intParam(r, "hours", 72, 1, 720)
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		days, err := intParam(r, "days", 30, 1, 366)
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		m, err := a.Monitor(r.Context(), hours, days)
		reply(w, m, err)
	}))
	mux.HandleFunc("GET /api/ops/monitor/unlock-rate", a.guard(p("metrics.unlockRate.view", monitor), false, func(w http.ResponseWriter, r *http.Request) {
		hours, err := intParam(r, "hours", 72, 1, 720)
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		m, err := a.MonitorLimitedView(r.Context(), adminFrom(r).Scope, hours)
		reply(w, m, err)
	}))
	mux.HandleFunc("GET /api/ops/brands/unlock-rate", a.guard(p("access.view", "access.view.own", monitor, "metrics.unlockRate.view"), false, func(w http.ResponseWriter, r *http.Request) {
		var ids []string
		for _, s := range strings.Split(r.URL.Query().Get("brandIds"), ",") {
			if s = strings.TrimSpace(s); s != "" {
				ids = append(ids, s)
			}
		}
		v, err := a.BrandUnlockRates(r.Context(), adminFrom(r).Scope, ids)
		reply(w, v, err)
	}))
	mux.HandleFunc("GET /api/ops/degrade/public", func(w http.ResponseWriter, r *http.Request) {
		v, err := a.Public(r.Context())
		if err != nil {
			httpx.WriteError(w, httpx.ServerBusy(""))
			return
		}
		httpx.WriteJSON(w, 200, v)
	})
	mux.HandleFunc("GET /api/ops/degrade/status", a.guard(p(monitor, clView), false, func(w http.ResponseWriter, r *http.Request) {
		v, err := a.Status(r.Context())
		reply(w, v, err)
	}))
	mux.HandleFunc("GET /api/ops/degrade/events", a.guard(p(monitor, clView), false, func(w http.ResponseWriter, r *http.Request) {
		v, err := a.ListEvents(r.Context())
		reply(w, v, err)
	}))
	mux.HandleFunc("GET /api/ops/degrade/events/{eventId}", a.guard(p(monitor, clView), false, func(w http.ResponseWriter, r *http.Request) {
		v, err := a.GetEvent(r.Context(), r.PathValue("eventId"))
		reply(w, v, err)
	}))
	mux.HandleFunc("POST /api/ops/degrade/enter", a.guard(p(control), true, a.idempotent(func(w http.ResponseWriter, r *http.Request) {
		var req EnterRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.WriteError(w, err)
			return
		}
		v, err := a.Enter(r.Context(), actorOf(adminFrom(r)), req)
		reply(w, v, err)
	})))
	mux.HandleFunc("POST /api/ops/degrade/exit", a.guard(p(control), true, a.idempotent(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Reason *string `json:"reason"`
		}
		if err := httpx.Decode(r, &req); err != nil {
			httpx.WriteError(w, err)
			return
		}
		v, err := a.Exit(r.Context(), actorOf(adminFrom(r)), req.Reason)
		reply(w, v, err)
	})))
	for _, kind := range []string{"tighten", "stop", "relax"} {
		kind := kind
		mux.HandleFunc("POST /api/ops/degrade/"+kind, a.guard(p(control), true, a.idempotent(func(w http.ResponseWriter, r *http.Request) {
			var req reasonBody
			if err := httpx.Decode(r, &req); err != nil {
				httpx.WriteError(w, err)
				return
			}
			v, err := a.Control(r.Context(), actorOf(adminFrom(r)), kind, req.Reason)
			reply(w, v, err)
		})))
	}
	mux.HandleFunc("GET /api/ops/closures", a.guard(p(clView, clHandle), false, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		st := q.Get("status")
		if st != "" && !contains([]string{"骑行中", "待闭合", "待人工", "已闭合"}, st) {
			httpx.WriteError(w, httpx.Invalid("闭合状态取值不对"))
			return
		}
		v, err := a.ListClosures(r.Context(), q.Get("eventId"), st)
		reply(w, v, err)
	}))
	mux.HandleFunc("POST /api/ops/closures/{orderId}/handle", a.guard(p(clHandle), true, a.idempotent(func(w http.ResponseWriter, r *http.Request) {
		var req HandleClosureRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.WriteError(w, err)
			return
		}
		v, err := a.HandleClosure(r.Context(), actorOf(adminFrom(r)), r.PathValue("orderId"), req, r.Header.Get("Idempotency-Key"))
		reply(w, v, err)
	})))
	mux.HandleFunc("GET /api/ops/alerts", a.guard(p(monitor), false, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f := AlertFilter{Level: q.Get("level"), SortKey: q.Get("sortKey"), SortDir: q.Get("sortDir")}
		if f.Level != "" && !contains([]string{"严重", "警告", "提示"}, f.Level) {
			httpx.WriteError(w, httpx.Invalid("告警级别取值不对"))
			return
		}
		if s := q.Get("acked"); s != "" {
			b, err := strconv.ParseBool(s)
			if err != nil {
				httpx.WriteError(w, httpx.Invalid("acked 取值不对"))
				return
			}
			f.Acked = &b
		}
		var err error
		if f.Page, err = intParam(r, "page", 1, 1, 1<<30); err != nil {
			httpx.WriteError(w, err)
			return
		}
		if f.PageSize, err = intParam(r, "pageSize", 20, 1, 200); err != nil {
			httpx.WriteError(w, err)
			return
		}
		v, err := a.ListAlerts(r.Context(), f)
		reply(w, v, err)
	}))
	mux.HandleFunc("POST /api/ops/alerts/{alertId}/ack", a.guard(p(monitor), true, a.idempotent(func(w http.ResponseWriter, r *http.Request) {
		v, err := a.AckAlert(r.Context(), actorOf(adminFrom(r)), r.PathValue("alertId"))
		reply(w, v, err)
	})))
	mux.HandleFunc("GET /api/ops/pauses", a.guard(p(monitor, "access.view", "access.view.own"), false, func(w http.ResponseWriter, r *http.Request) {
		v, err := a.ListPauses(r.Context(), adminFrom(r).Scope)
		reply(w, v, err)
	}))
	mux.HandleFunc("GET /api/ops/suspend-suggestions", a.guard(p(monitor, pausePerm), false, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		v, err := a.ListSuggestions(r.Context(), q.Get("status"), q.Get("brandId"))
		reply(w, v, err)
	}))
	mux.HandleFunc("POST /api/ops/suspend-suggestions/{suggestionId}/confirm", a.guard(p(pausePerm), true, a.idempotent(func(w http.ResponseWriter, r *http.Request) {
		var req reasonBody
		if err := httpx.Decode(r, &req); err != nil {
			httpx.WriteError(w, err)
			return
		}
		v, err := a.ConfirmSuspend(r.Context(), actorOf(adminFrom(r)), r.PathValue("suggestionId"), req.Reason, r.Header.Get("Idempotency-Key"))
		reply(w, v, err)
	})))
	mux.HandleFunc("POST /api/ops/suspend-suggestions/{suggestionId}/dismiss", a.guard(p(pausePerm), true, a.idempotent(func(w http.ResponseWriter, r *http.Request) {
		var req reasonBody
		if err := httpx.Decode(r, &req); err != nil {
			httpx.WriteError(w, err)
			return
		}
		v, err := a.DismissSuspend(r.Context(), actorOf(adminFrom(r)), r.PathValue("suggestionId"), req.Reason)
		reply(w, v, err)
	})))
	mux.HandleFunc("PUT /api/ops/auto-suspend", a.guard(p(pausePerm), true, a.idempotent(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			On     *bool  `json:"on"`
			Reason string `json:"reason"`
		}
		if err := httpx.Decode(r, &req); err != nil {
			httpx.WriteError(w, err)
			return
		}
		if req.On == nil {
			httpx.WriteError(w, httpx.Invalid("请选择开启或关闭"))
			return
		}
		on, err := a.SetAutoSuspend(r.Context(), actorOf(adminFrom(r)), *req.On, req.Reason)
		reply(w, map[string]bool{"autoPauseByAgent": on}, err)
	})))
	mux.HandleFunc("POST /api/ops/brands/{brandId}/pause", a.guard(p(pausePerm), true, a.idempotent(func(w http.ResponseWriter, r *http.Request) {
		var req reasonBody
		if err := httpx.Decode(r, &req); err != nil {
			httpx.WriteError(w, err)
			return
		}
		v, err := a.PauseBrand(r.Context(), actorOf(adminFrom(r)), r.PathValue("brandId"), req.Reason, r.Header.Get("Idempotency-Key"))
		reply(w, v, err)
	})))
	mux.HandleFunc("POST /api/ops/brands/{brandId}/resume", a.guard(p(pausePerm), true, a.idempotent(func(w http.ResponseWriter, r *http.Request) {
		var req reasonBody
		if err := httpx.Decode(r, &req); err != nil {
			httpx.WriteError(w, err)
			return
		}
		v, err := a.ResumeBrand(r.Context(), actorOf(adminFrom(r)), r.PathValue("brandId"), req.Reason, r.Header.Get("Idempotency-Key"))
		reply(w, v, err)
	})))
	mux.HandleFunc("GET /api/ops/todos", a.guard(nil, false, func(w http.ResponseWriter, r *http.Request) {
		v, err := a.Todos(r.Context(), adminFrom(r).HasAny)
		reply(w, v, err)
	}))
	return mux
}
