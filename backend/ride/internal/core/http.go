package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"bike/ride/internal/auth"
	"bike/ride/internal/geo"
)

// adminPerms 订单查询所需权限点（x-permissions，有其一即可）。
var adminPerms = []string{"order.view", "settlement.manage", "ops.monitor", "degrade.closure.view", "arrears.view"}

// Handler 组装全部接口。
func (s *Service) Handler(v *auth.Verifier) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{"ok": true}) })

	// 用户端
	mux.HandleFunc("POST /api/ride/scan", s.userOpt(v, func(w http.ResponseWriter, r *http.Request, uid string) {
		var req struct{ Code string }
		if !decode(w, r, &req) {
			return
		}
		res, ae := s.Scan(r.Context(), uid, req.Code)
		reply(w, res, ae)
	}))
	mux.HandleFunc("GET /api/ride/nearby", s.userOpt(v, func(w http.ResponseWriter, r *http.Request, uid string) {
		q := r.URL.Query()
		center, ok := pointParam(w, q.Get("lng"), q.Get("lat"))
		if !ok {
			return
		}
		radius := 0
		if x := q.Get("radiusM"); x != "" {
			n, err := strconv.Atoi(x)
			if err != nil || n < 1 {
				writeErr(w, apiErr(400, "INVALID", "radiusM 应为正整数", nil))
				return
			}
			radius = n
		}
		res, ae := s.Nearby(r.Context(), q.Get("vehicleId"), center, radius)
		reply(w, res, ae)
	}))
	mux.HandleFunc("POST /api/ride/unlock", s.userOpt(v, func(w http.ResponseWriter, r *http.Request, uid string) {
		var req struct{ Code string }
		if !decode(w, r, &req) {
			return
		}
		s.idem(w, r, "user:"+uid+":unlock", false, func() (int, any) {
			res, ae := s.Unlock(r.Context(), uid, req.Code)
			if ae != nil {
				return ae.Status, ae.Body
			}
			return 200, res
		})
	}))
	mux.HandleFunc("GET /api/ride/current", s.userReq(v, func(w http.ResponseWriter, r *http.Request, uid string) {
		res, ae := s.Current(r.Context(), uid)
		if ae == nil && res == nil {
			w.WriteHeader(204)
			return
		}
		reply(w, res, ae)
	}))
	mux.HandleFunc("GET /api/ride/return-preview", s.userReq(v, func(w http.ResponseWriter, r *http.Request, uid string) {
		q := r.URL.Query()
		loc, ok := pointParam(w, q.Get("lng"), q.Get("lat"))
		if !ok {
			return
		}
		res, ae := s.ReturnPreview(r.Context(), uid, q.Get("stationId"), loc)
		reply(w, res, ae)
	}))
	mux.HandleFunc("POST /api/ride/return", s.userReq(v, func(w http.ResponseWriter, r *http.Request, uid string) {
		var req ReturnRequest
		if !decode(w, r, &req) {
			return
		}
		s.idem(w, r, "user:"+uid+":return", false, func() (int, any) {
			res, ae := s.Return(r.Context(), uid, req)
			if ae != nil {
				return ae.Status, ae.Body
			}
			return 200, res
		})
	}))
	mux.HandleFunc("GET /api/ride/orders", s.userReq(v, func(w http.ResponseWriter, r *http.Request, uid string) {
		f, ok := filterFrom(w, r)
		if !ok {
			return
		}
		f.UserID, f.SortKey, f.SortDir, f.Q, f.BrandID, f.OperatorID, f.Degraded, f.From, f.To = uid, "", "", "", "", "", nil, nil, nil
		s.listReply(w, r, f, func(o *Order) map[string]any { return o.userView() })
	}))
	mux.HandleFunc("GET /api/ride/orders/{orderId}", s.userReq(v, func(w http.ResponseWriter, r *http.Request, uid string) {
		o, err := s.GetOrder(r.Context(), r.PathValue("orderId"))
		if err == nil && o.UserID != uid {
			err = errNotFound
		}
		if !orderErr(w, err) {
			writeJSON(w, 200, o.userView())
		}
	}))

	// 管理端
	mux.HandleFunc("GET /api/ride/admin/orders", s.adminReq(v, func(w http.ResponseWriter, r *http.Request, a *auth.Admin) {
		f, ok := filterFrom(w, r)
		if !ok {
			return
		}
		f.Scope = a.Scope
		s.listReply(w, r, f, func(o *Order) map[string]any { return o.adminView() })
	}))
	mux.HandleFunc("GET /api/ride/admin/orders/{orderId}", s.adminReq(v, func(w http.ResponseWriter, r *http.Request, a *auth.Admin) {
		o, err := s.GetOrder(r.Context(), r.PathValue("orderId"))
		if orderErr(w, err) {
			return
		}
		if a.Scope != nil && *a.Scope != o.OperatorID {
			writeErr(w, apiErr(403, "SCOPE_DENIED", "只能查看和操作本运营商的数据", nil))
			return
		}
		writeJSON(w, 200, o.adminView())
	}))

	// 内部
	mux.HandleFunc("GET /internal/ride/orders", s.internalReq(v, func(w http.ResponseWriter, r *http.Request, caller string) {
		f, ok := filterFrom(w, r)
		if !ok {
			return
		}
		q := r.URL.Query()
		f.UserID, f.PlateNo = q.Get("userId"), q.Get("plateNo")
		if f.UserID == "" && f.PlateNo == "" && len(f.OrderIDs) == 0 {
			writeErr(w, apiErr(400, "INVALID", "userId、orderIds、plateNo 至少传一个", nil))
			return
		}
		f.Q, f.SortKey, f.SortDir = "", "", ""
		s.listReply(w, r, f, func(o *Order) map[string]any { return o.adminView() })
	}))
	mux.HandleFunc("GET /internal/ride/orders/{orderId}", s.internalReq(v, func(w http.ResponseWriter, r *http.Request, caller string) {
		o, err := s.GetOrder(r.Context(), r.PathValue("orderId"))
		if !orderErr(w, err) {
			writeJSON(w, 200, o.adminView())
		}
	}))
	mux.HandleFunc("POST /internal/ride/orders/{orderId}/force-end", s.internalReq(v, func(w http.ResponseWriter, r *http.Request, caller string) {
		var req ForceEndRequest
		if !decode(w, r, &req) {
			return
		}
		id := r.PathValue("orderId")
		s.idem(w, r, "internal:"+caller+":force-end:"+id, true, func() (int, any) {
			res, ae := s.ForceEnd(r.Context(), id, req)
			if ae != nil {
				return ae.Status, ae.Body
			}
			return 200, res
		})
	}))
	mux.HandleFunc("POST /internal/ride/orders/{orderId}/lock-fact", s.internalReq(v, func(w http.ResponseWriter, r *http.Request, caller string) {
		var req LockFactRequest
		if !decode(w, r, &req) {
			return
		}
		id := r.PathValue("orderId")
		s.idem(w, r, "internal:"+caller+":lock-fact:"+id, true, func() (int, any) {
			res, ae := s.BackfillLockFact(r.Context(), id, req)
			if ae != nil {
				return ae.Status, ae.Body
			}
			return 200, res
		})
	}))
	mux.HandleFunc("POST /internal/ride/ride-events", s.internalReq(v, func(w http.ResponseWriter, r *http.Request, caller string) {
		var req RideEvent
		if !decode(w, r, &req) {
			return
		}
		// 按运营商事件号去重，幂等键可选
		res, ae := s.DeliverRideEvent(r.Context(), &req)
		reply(w, res, ae)
	}))
	return mux
}

// ---------------- 鉴权包装 ----------------

func (s *Service) userOpt(v *auth.Verifier, h func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, err := v.User(r)
		if err != nil && !errors.Is(err, auth.ErrNoToken) {
			uid = "" // 令牌无效按未登录：扫码照常，check 里提示登录
		}
		h(w, r, uid)
	}
}

func (s *Service) userReq(v *auth.Verifier, h func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, err := v.User(r)
		if err != nil {
			writeErr(w, apiErr(401, "NOT_LOGGED_IN", "", nil))
			return
		}
		h(w, r, uid)
	}
}

func (s *Service) adminReq(v *auth.Verifier, h func(http.ResponseWriter, *http.Request, *auth.Admin)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, err := v.Admin(r)
		if err != nil {
			writeErr(w, apiErr(401, "ADMIN_NOT_LOGGED_IN", "登录已失效，请重新登录", nil))
			return
		}
		if !a.HasAny(adminPerms) {
			role := a.Role
			if role == "" {
				role = "当前岗位"
			}
			writeErr(w, apiErr(403, "FORBIDDEN", "你的账号（"+role+"）没有该操作的权限", nil))
			return
		}
		if writeMethod(r.Method) && a.Readonly {
			writeErr(w, apiErr(403, "READONLY", "该运营商已退出，账号只读，不能做修改操作", nil))
			return
		}
		h(w, r, a)
	}
}

// writeMethod 写操作的 HTTP 方法。管理端写接口一律拒绝 readonly 令牌（本服务目前没有管理端写接口，护栏先就位）。
func writeMethod(m string) bool {
	return m != http.MethodGet && m != http.MethodHead && m != http.MethodOptions
}

func (s *Service) internalReq(v *auth.Verifier, h func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller, err := v.Internal(r)
		if err != nil {
			writeErr(w, &APIError{Status: 401, Body: map[string]any{"code": "UNAUTHORIZED", "message": "服务凭证不正确"}})
			return
		}
		h(w, r, caller)
	}
}

// ---------------- 幂等 ----------------

// idem 同一幂等键重复到达返回第一次的结果，不重复执行；同时到达的第二个请求等第一个完成。
func (s *Service) idem(w http.ResponseWriter, r *http.Request, scope string, required bool, fn func() (int, any)) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		if required {
			writeErr(w, apiErr(400, "INVALID", "缺少 Idempotency-Key", nil))
			return
		}
		st, body := fn()
		writeJSON(w, st, body)
		return
	}
	if len(key) > 64 {
		writeErr(w, apiErr(400, "INVALID", "Idempotency-Key 不能超过 64 个字符", nil))
		return
	}
	ctx := r.Context()
	if !s.dbOK() {
		s.idemMem(w, scope+"|"+key, fn)
		return
	}
	tag, err := s.store.pool.Exec(ctx, `INSERT INTO idem(scope, key) VALUES ($1,$2) ON CONFLICT DO NOTHING`, scope, key)
	if err != nil {
		if isConnErr(err) {
			s.breakers.Trip("数据库")
			s.idemMem(w, scope+"|"+key, fn)
			return
		}
		writeErr(w, busy(err))
		return
	}
	if tag.RowsAffected() == 0 {
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			var done bool
			var st int
			var body []byte
			err := s.store.pool.QueryRow(ctx, `SELECT done, status, body FROM idem WHERE scope=$1 AND key=$2`, scope, key).Scan(&done, &st, &body)
			if errors.Is(err, pgx.ErrNoRows) {
				break // 第一次执行失败被删除，重新执行
			}
			if err == nil && done {
				writeRaw(w, st, body)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
		if _, err := s.store.pool.Exec(ctx, `INSERT INTO idem(scope, key) VALUES ($1,$2) ON CONFLICT DO NOTHING`, scope, key); err != nil {
			writeErr(w, busy(err))
			return
		}
	}
	st, body := fn()
	b, _ := json.Marshal(body)
	bg := context.Background()
	if st >= 500 {
		_, _ = s.store.pool.Exec(bg, `DELETE FROM idem WHERE scope=$1 AND key=$2`, scope, key)
	} else {
		_, _ = s.store.pool.Exec(bg, `UPDATE idem SET done=true, status=$3, body=$4 WHERE scope=$1 AND key=$2`, scope, key, st, b)
	}
	writeRaw(w, st, b)
}

func (s *Service) idemMem(w http.ResponseWriter, k string, fn func() (int, any)) {
	s.mu.Lock()
	if b, ok := s.memIdem[k]; ok {
		s.mu.Unlock()
		st, _ := strconv.Atoi(string(b[:3]))
		writeRaw(w, st, b[3:])
		return
	}
	s.mu.Unlock()
	st, body := fn()
	b, _ := json.Marshal(body)
	if st < 500 {
		s.mu.Lock()
		s.memIdem[k] = append([]byte(strconv.Itoa(st)), b...)
		s.mu.Unlock()
	}
	writeRaw(w, st, b)
}

// ---------------- 小工具 ----------------

func (s *Service) listReply(w http.ResponseWriter, r *http.Request, f OrderFilter, view func(*Order) map[string]any) {
	os, total, err := s.ListOrders(r.Context(), f)
	if err != nil {
		writeErr(w, busy(err))
		return
	}
	rows := make([]map[string]any, 0, len(os))
	for _, o := range os {
		rows = append(rows, view(o))
	}
	page, size := f.Page, f.PageSize
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	writeJSON(w, 200, map[string]any{"rows": rows, "total": total, "page": page, "pageSize": size})
}

var statusEnum = map[string]bool{StUnlocking: true, StFailed: true, StRiding: true, StEnded: true, StManual: true}

func filterFrom(w http.ResponseWriter, r *http.Request) (OrderFilter, bool) {
	q := r.URL.Query()
	f := OrderFilter{Q: q.Get("q"), UserID: q.Get("userId"), Status: q.Get("status"), BrandID: q.Get("brandId"), OperatorID: q.Get("operatorId"),
		SortKey: q.Get("sortKey"), SortDir: q.Get("sortDir")}
	bad := func(msg string) (OrderFilter, bool) { writeErr(w, apiErr(400, "INVALID", msg, nil)); return f, false }
	if f.Status != "" && !statusEnum[f.Status] {
		return bad("订单状态不正确")
	}
	for k, dst := range map[string]*int{"page": &f.Page, "pageSize": &f.PageSize} {
		if x := q.Get(k); x != "" {
			n, err := strconv.Atoi(x)
			if err != nil || n < 1 || (k == "pageSize" && n > 200) {
				return bad(k + " 不正确")
			}
			*dst = n
		}
	}
	if x := q.Get("degraded"); x != "" {
		b, err := strconv.ParseBool(x)
		if err != nil {
			return bad("degraded 应为 true 或 false")
		}
		f.Degraded = &b
	}
	for k, dst := range map[string]**time.Time{"from": &f.From, "to": &f.To} {
		if x := q.Get(k); x != "" {
			t, err := time.Parse(time.RFC3339, x)
			if err != nil {
				return bad(k + " 应为 RFC 3339 时刻")
			}
			*dst = &t
		}
	}
	if x := q.Get("orderIds"); x != "" {
		for _, id := range strings.Split(x, ",") {
			if id = strings.TrimSpace(id); id != "" {
				f.OrderIDs = append(f.OrderIDs, id)
			}
		}
		if len(f.OrderIDs) > 100 {
			return bad("orderIds 最多 100 个")
		}
	}
	return f, true
}

func pointParam(w http.ResponseWriter, lng, lat string) (*geo.Pt, bool) {
	if lng == "" && lat == "" {
		return nil, true
	}
	x, err1 := strconv.ParseFloat(lng, 64)
	y, err2 := strconv.ParseFloat(lat, 64)
	if err1 != nil || err2 != nil {
		writeErr(w, apiErr(400, "INVALID", "lng 与 lat 须同时传数字", nil))
		return nil, false
	}
	return &geo.Pt{x, y}, true
}

func orderErr(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if isNotFound(err) {
		writeErr(w, apiErr(404, "NOT_FOUND", "没有找到该订单", nil))
		return true
	}
	writeErr(w, busy(err))
	return true
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeErr(w, apiErr(400, "INVALID", "请求内容不是有效的 JSON", nil))
		return false
	}
	if !utf8.Valid(body) {
		writeErr(w, apiErr(400, "INVALID", "请求体不是 UTF-8 编码", nil))
		return false
	}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(v); err != nil {
		writeErr(w, apiErr(400, "INVALID", "请求内容不是有效的 JSON", nil))
		return false
	}
	return true
}

func reply(w http.ResponseWriter, res any, ae *APIError) {
	if ae != nil {
		writeErr(w, ae)
		return
	}
	writeJSON(w, 200, res)
}

func writeErr(w http.ResponseWriter, e *APIError) { writeJSON(w, e.Status, e.Body) }

func writeJSON(w http.ResponseWriter, st int, v any) {
	b, _ := json.Marshal(v)
	writeRaw(w, st, b)
}

func writeRaw(w http.ResponseWriter, st int, b []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(st)
	_, _ = w.Write(b)
}
