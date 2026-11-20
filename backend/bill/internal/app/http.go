package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"bike/bill/internal/auth"
)

/* ---------------- 线上格式：JSON 与错误体（contracts/wire.md 第 1 节） ---------------- */

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type next struct {
	Label  string `json:"label"`
	Action string `json:"action"`
}

// apiError 统一错误体
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Charged *bool  `json:"charged,omitempty"`
	Next    *next  `json:"next,omitempty"`
	status  int
}

var errNext = map[string]*next{
	"NOT_LOGGED_IN":       {Label: "一键授权", Action: "login"},
	"ADMIN_NOT_LOGGED_IN": {Label: "去登录", Action: "login"},
	"ACCOUNT_FROZEN":      {Label: "查看解冻方法", Action: "unfreezeGuide"},
	"NEED_ONBOARDING":     {Label: "去完成", Action: "onboarding"},
	"PAY_DECLINED":        {Label: "换个方式重新支付", Action: "payOrder"},
	"CONFLICT":            {Label: "刷新", Action: "retry"},
}

// 用户端的失败必带 charged（这些失败都不产生费用）
var userCodes = map[string]bool{"NOT_LOGGED_IN": true, "ACCOUNT_FROZEN": true, "NEED_ONBOARDING": true, "PAY_DECLINED": true}

func fail(w http.ResponseWriter, status int, code, msg string) {
	e := apiError{Code: code, Message: msg, Next: errNext[code]}
	if userCodes[code] {
		f := false
		e.Charged = &f
	}
	writeJSON(w, status, e)
}

func (a *App) failErr(w http.ResponseWriter, err error) {
	var be *bizError
	var pe *payError
	switch {
	case errors.As(err, &be):
		fail(w, be.status, be.code, be.msg)
	case errors.As(err, &pe):
		fail(w, 409, pe.code, pe.msg)
	case errors.Is(err, errNotFound):
		fail(w, 404, "NOT_FOUND", "没有找到对应的记录")
	default:
		a.Log.Error("内部错误", "err", err)
		fail(w, 500, "SERVER_ERROR", "服务暂时出了点问题，请稍后再试")
	}
}

/* ---------------- 鉴权 ---------------- */

func (a *App) user(w http.ResponseWriter, r *http.Request) (string, bool) {
	u, err := a.Verifier.User(r)
	if err != nil {
		fail(w, 401, "NOT_LOGGED_IN", "请先授权登录")
		return "", false
	}
	return u, true
}

func (a *App) admin(w http.ResponseWriter, r *http.Request, perms ...string) (*auth.Admin, bool) {
	ad, err := a.Verifier.Admin(r)
	if err != nil {
		fail(w, 401, "ADMIN_NOT_LOGGED_IN", "登录已失效，请重新登录")
		return nil, false
	}
	if len(perms) > 0 && !ad.HasAny(perms...) {
		fail(w, 403, "FORBIDDEN", "你的账号没有该操作的权限")
		return nil, false
	}
	return ad, true
}

// noScope 用户数据只给平台公司账号；运营商账号越界
func noScope(w http.ResponseWriter, ad *auth.Admin) bool {
	if ad.Scope != "" {
		fail(w, 403, "SCOPE_DENIED", "只能查看和操作本运营商的数据")
		return false
	}
	return true
}

func (a *App) internal(w http.ResponseWriter, r *http.Request) (string, bool) {
	name, ok := a.Callers.Check(r)
	if !ok {
		fail(w, 401, "UNAUTHORIZED", "服务凭证无效")
		return "", false
	}
	return name, true
}

/* ---------------- 幂等键 ---------------- */

type recorder struct {
	status int
	buf    bytes.Buffer
	h      http.Header
}

func (r *recorder) Header() http.Header         { return r.h }
func (r *recorder) Write(b []byte) (int, error) { return r.buf.Write(b) }
func (r *recorder) WriteHeader(s int)           { r.status = s }

// idem 同一个键重复到达返回第一次的结果；required 为真时没带键拒绝
func (a *App) idem(w http.ResponseWriter, r *http.Request, scope string, required bool, fn func(w http.ResponseWriter)) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		if required {
			fail(w, 400, "INVALID", "缺少幂等键 Idempotency-Key")
			return
		}
		fn(w)
		return
	}
	if len(key) > 64 {
		fail(w, 400, "INVALID", "幂等键过长")
		return
	}
	ctx := r.Context()
	tag, err := a.Pool.Exec(ctx, `INSERT INTO idempotency (scope, key) VALUES ($1,$2) ON CONFLICT DO NOTHING`, scope, key)
	if err != nil {
		a.failErr(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		var st *int
		var body []byte
		if err := a.Pool.QueryRow(ctx, `SELECT status, body FROM idempotency WHERE scope=$1 AND key=$2`, scope, key).Scan(&st, &body); err != nil {
			a.failErr(w, err)
			return
		}
		if st == nil {
			fail(w, 409, "CONFLICT", "同一请求正在处理，请稍后再试")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(*st)
		_, _ = w.Write(body)
		return
	}
	rec := &recorder{status: 200, h: http.Header{}}
	fn(rec)
	if rec.status >= 500 {
		_, _ = a.Pool.Exec(context.WithoutCancel(ctx), `DELETE FROM idempotency WHERE scope=$1 AND key=$2`, scope, key)
	} else {
		_, _ = a.Pool.Exec(context.WithoutCancel(ctx), `UPDATE idempotency SET status=$3, body=$4 WHERE scope=$1 AND key=$2`, scope, key, rec.status, rec.buf.Bytes())
	}
	for k, v := range rec.h {
		w.Header()[k] = v
	}
	w.WriteHeader(rec.status)
	_, _ = w.Write(rec.buf.Bytes())
}

/* ---------------- 路由 ---------------- */

// Handler 全部接口
func (a *App) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]bool{"ok": true}) })

	m.HandleFunc("GET /api/bill/me/summary", a.hMySummary)
	m.HandleFunc("GET /api/bill/me/orders", a.hMyOrders)
	m.HandleFunc("GET /api/bill/me/orders/{orderId}", a.hMyOrder)
	m.HandleFunc("GET /api/bill/me/orders/{orderId}/estimate", a.hEstimate)
	m.HandleFunc("POST /api/bill/me/orders/{orderId}/pay-intent", a.hPayOrder)
	m.HandleFunc("POST /api/bill/me/orders/{orderId}/pay", a.hPayOrder) // 旧路径，同 pay-intent
	m.HandleFunc("POST /api/bill/me/arrears/pay-intent", a.hPayArrears)
	m.HandleFunc("POST /api/bill/me/arrears/pay", a.hPayArrears) // 旧路径，同 pay-intent
	m.HandleFunc("GET /api/bill/me/month-cards", a.hMyCards)
	m.HandleFunc("POST /api/bill/me/month-cards", a.hBuyCard)
	m.HandleFunc("GET /api/bill/me/coupons", a.hMyCoupons)
	m.HandleFunc("POST /api/bill/me/coupons", a.hClaimCoupon)
	m.HandleFunc("GET /api/bill/coupon-templates", a.hTemplates)

	m.HandleFunc("GET /api/bill/admin/arrears", a.hArrears)
	m.HandleFunc("GET /api/bill/admin/orders", a.hAdminOrders)
	m.HandleFunc("GET /api/bill/admin/orders/{orderId}", a.hAdminOrder)
	m.HandleFunc("GET /api/bill/admin/users/{userId}/summary", a.hUserSummary)
	m.HandleFunc("GET /api/bill/admin/users/{userId}/bills", a.hUserBills)
	m.HandleFunc("GET /api/bill/admin/users/{userId}/month-cards", a.hUserCards)
	m.HandleFunc("GET /api/bill/admin/users/{userId}/coupons", a.hUserCoupons)
	m.HandleFunc("GET /api/bill/admin/todos", a.hTodos)
	m.HandleFunc("GET /api/bill/admin/bills", a.hBillsByStatus)

	m.HandleFunc("POST /internal/bill/refunds", a.hRefund)
	m.HandleFunc("GET /internal/bill/orders/{orderId}", a.hFacts)
	return a.guard(m)
}

func isWrite(m string) bool {
	return m == http.MethodPost || m == http.MethodPut || m == http.MethodPatch || m == http.MethodDelete
}

// guard 全部接口的前置检查：请求体必须是 UTF-8（wire.md 第 1 节）；管理端写接口一律拒绝只读令牌与已退出运营商的数据范围（READONLY）
func (a *App) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isWrite(r.Method) && r.Body != nil && r.Body != http.NoBody {
			raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
			if err != nil {
				fail(w, 400, "INVALID", "请求体过大或读取失败")
				return
			}
			if !utf8.Valid(raw) {
				fail(w, 400, "INVALID", "请求体不是 UTF-8 编码")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		if isWrite(r.Method) && strings.HasPrefix(r.URL.Path, "/api/bill/admin/") && !a.adminWritable(w, r) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

// adminWritable 管理端写操作的只读判断：令牌 readonly 为真，或 scope 属于已退出的运营商，返回 403 READONLY
func (a *App) adminWritable(w http.ResponseWriter, r *http.Request) bool {
	ad, err := a.Verifier.Admin(r)
	if err != nil {
		fail(w, 401, "ADMIN_NOT_LOGGED_IN", "登录已失效，请重新登录")
		return false
	}
	if ad.Readonly {
		fail(w, 403, "READONLY", "账号为只读，不能执行写操作")
		return false
	}
	if ad.Scope != "" {
		exited, err := a.operatorExited(r.Context(), ad.Scope)
		if err != nil {
			a.failErr(w, err)
			return false
		}
		if exited {
			fail(w, 403, "READONLY", "该运营商已退出，只能查看，不能执行写操作")
			return false
		}
	}
	return true
}

// operatorExited 运营商是否已退出（来自 fleet.operator.exited）
func (a *App) operatorExited(ctx context.Context, operatorID string) (bool, error) {
	var n int
	err := a.Pool.QueryRow(ctx, `SELECT count(*) FROM exited_operators WHERE operator_id=$1`, operatorID).Scan(&n)
	return n > 0, err
}

func parseOrderIDs(r *http.Request) ([]string, bool) {
	raw := r.URL.Query().Get("orderIds")
	var ids []string
	seen := map[string]bool{}
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			ids = append(ids, s)
		}
	}
	return ids, len(ids) >= 1 && len(ids) <= 200
}

type batch struct {
	Rows    []OrderBill `json:"rows"`
	Missing []string    `json:"missing"`
}

func (a *App) batch(ctx context.Context, ids []string, admin bool, filter func(*Bill) bool) (batch, error) {
	bills, err := listBills(ctx, a.Pool, `order_id = ANY($1)`, ids)
	if err != nil {
		return batch{}, err
	}
	by := map[string]*Bill{}
	for _, b := range bills {
		if filter(b) {
			by[b.OrderID] = b
		}
	}
	out := batch{Rows: []OrderBill{}, Missing: []string{}}
	for _, id := range ids {
		if b, ok := by[id]; ok {
			out.Rows = append(out.Rows, a.orderBill(b, admin))
		} else {
			out.Missing = append(out.Missing, id)
		}
	}
	return out, nil
}

/* ---------------- 用户端 ---------------- */

func (a *App) hMySummary(w http.ResponseWriter, r *http.Request) {
	u, ok := a.user(w, r)
	if !ok {
		return
	}
	s, err := a.summary(r.Context(), u)
	if err != nil {
		a.failErr(w, err)
		return
	}
	writeJSON(w, 200, s)
}

func (a *App) hMyOrders(w http.ResponseWriter, r *http.Request) {
	u, ok := a.user(w, r)
	if !ok {
		return
	}
	ids, ok := parseOrderIDs(r)
	if !ok {
		fail(w, 400, "INVALID", "订单号列表应为 1～200 个")
		return
	}
	out, err := a.batch(r.Context(), ids, false, func(b *Bill) bool { return b.UserID == u })
	if err != nil {
		a.failErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (a *App) myBill(w http.ResponseWriter, r *http.Request, u string) (*Bill, bool) {
	b, err := getBill(r.Context(), a.Pool, `order_id=$1`, r.PathValue("orderId"))
	if err == nil && b.UserID != u {
		err = errNotFound
	}
	if err != nil {
		if errors.Is(err, errNotFound) {
			fail(w, 404, "NOT_FOUND", "没有找到该订单的账单")
		} else {
			a.failErr(w, err)
		}
		return nil, false
	}
	return b, true
}

func (a *App) hMyOrder(w http.ResponseWriter, r *http.Request) {
	u, ok := a.user(w, r)
	if !ok {
		return
	}
	if b, ok := a.myBill(w, r, u); ok {
		writeJSON(w, 200, a.orderBill(b, false))
	}
}

func (a *App) hEstimate(w http.ResponseWriter, r *http.Request) {
	u, ok := a.user(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	start, err := time.Parse(time.RFC3339, q.Get("startAt"))
	vid := q.Get("pricingVersionId")
	if err != nil || vid == "" {
		fail(w, 400, "INVALID", "缺少计费起点或规则版本号")
		return
	}
	est, err := a.estimate(r.Context(), u, r.PathValue("orderId"), start, vid)
	if err != nil {
		a.failErr(w, err)
		return
	}
	writeJSON(w, 200, est)
}

func attemptKey(r *http.Request) string {
	if k := r.Header.Get("Idempotency-Key"); k != "" {
		return k
	}
	return newID()
}

func rawOrNil(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return raw
}

func (a *App) hPayOrder(w http.ResponseWriter, r *http.Request) {
	u, ok := a.user(w, r)
	if !ok {
		return
	}
	orderID := r.PathValue("orderId")
	a.idem(w, r, "pay:"+u+":"+orderID, false, func(w http.ResponseWriter) {
		pi, err := a.payOne(r.Context(), u, orderID, attemptKey(r))
		if err != nil {
			if errors.Is(err, errNotFound) {
				fail(w, 404, "NOT_FOUND", "没有找到该订单的账单")
				return
			}
			a.failErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"orderId": pi.OrderID, "bill": a.orderBill(pi.Bill, false), "paymentId": nullStr(pi.PaymentID),
			"amount": pi.Amount, "payParams": rawOrNil(pi.PayParams), "paid": pi.Paid})
	})
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (a *App) hPayArrears(w http.ResponseWriter, r *http.Request) {
	u, ok := a.user(w, r)
	if !ok {
		return
	}
	a.idem(w, r, "arrears:"+u, false, func(w http.ResponseWriter) {
		ctx := r.Context()
		list, err := listBills(ctx, a.Pool, `user_id=$1 AND pay_status=$2 AND (pending_kind IS NULL OR (pending_kind=$3 AND pending_params IS NOT NULL)) ORDER BY bill_pushed_at`,
			u, stArrears, pendOneTap)
		if err != nil {
			a.failErr(w, err)
			return
		}
		if len(list) == 0 {
			fail(w, 409, "NOTHING_TO_PAY", "没有需要补付的欠费")
			return
		}
		attempt := attemptKey(r)
		var paid int64
		bills := []OrderBill{}
		intents := []map[string]any{}
		for _, b := range list {
			pi, err := a.payOne(ctx, u, b.OrderID, attempt)
			if err != nil {
				var pe *payError
				if errors.As(err, &pe) && pe.code == "CONFLICT" {
					continue // 结果未知的单跳过
				}
				if len(bills)+len(intents) == 0 {
					a.failErr(w, err)
					return
				}
				break // 已补付与已发起的部分照常返回
			}
			if pi.Paid {
				paid += pi.Amount
				bills = append(bills, a.orderBill(pi.Bill, false))
			} else {
				intents = append(intents, map[string]any{"orderId": pi.OrderID, "paymentId": nullStr(pi.PaymentID), "amount": pi.Amount, "payParams": rawOrNil(pi.PayParams)})
			}
		}
		if len(bills)+len(intents) == 0 {
			fail(w, 409, "CONFLICT", "支付结果尚未返回，稍后再试")
			return
		}
		s, err := a.summary(ctx, u)
		if err != nil {
			a.failErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"paid": paid, "bills": bills, "intents": intents, "summary": s})
	})
}

func (a *App) hMyCards(w http.ResponseWriter, r *http.Request) {
	u, ok := a.user(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	v, err := currentVersion(ctx, a.Pool, a.Now())
	if err != nil && !errors.Is(err, errNotFound) {
		a.failErr(w, err)
		return
	}
	cards, err := a.userCards(ctx, u)
	if err != nil {
		a.failErr(w, err)
		return
	}
	var rule any
	if v != nil {
		rule = cardRuleView(v)
	}
	writeJSON(w, 200, map[string]any{"rule": rule, "cards": cards})
}

func (a *App) hBuyCard(w http.ResponseWriter, r *http.Request) {
	u, ok := a.user(w, r)
	if !ok {
		return
	}
	a.idem(w, r, "card:"+u, false, func(w http.ResponseWriter) {
		c, err := a.BuyMonthCard(r.Context(), u)
		if err != nil {
			a.failErr(w, err)
			return
		}
		writeJSON(w, 200, c)
	})
}

var couponStatuses = map[string]bool{"": true, "未使用": true, "已使用": true, "已过期": true}

func (a *App) hMyCoupons(w http.ResponseWriter, r *http.Request) {
	u, ok := a.user(w, r)
	if !ok {
		return
	}
	a.writeCoupons(w, r, u)
}

func (a *App) writeCoupons(w http.ResponseWriter, r *http.Request, u string) {
	st := r.URL.Query().Get("status")
	if !couponStatuses[st] {
		fail(w, 400, "INVALID", "券状态不正确")
		return
	}
	cs, err := a.listCoupons(r.Context(), u, st)
	if err != nil {
		a.failErr(w, err)
		return
	}
	writeJSON(w, 200, cs)
}

func (a *App) hClaimCoupon(w http.ResponseWriter, r *http.Request) {
	u, ok := a.user(w, r)
	if !ok {
		return
	}
	var body struct {
		TemplateID string `json:"templateId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.TemplateID == "" {
		fail(w, 400, "INVALID", "缺少券模板编号")
		return
	}
	a.idem(w, r, "coupon:"+u, false, func(w http.ResponseWriter) {
		c, err := a.ClaimCoupon(r.Context(), u, body.TemplateID)
		if err != nil {
			a.failErr(w, err)
			return
		}
		writeJSON(w, 200, c)
	})
}

func (a *App) hTemplates(w http.ResponseWriter, r *http.Request) {
	u, _ := a.Verifier.User(r) // 可不登录
	ts, err := a.CouponTemplates(r.Context(), u)
	if err != nil {
		a.failErr(w, err)
		return
	}
	writeJSON(w, 200, ts)
}

/* ---------------- 管理端 ---------------- */

func pageParams(r *http.Request) (int, int) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("pageSize"))
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	if size > 200 {
		size = 200
	}
	return page, size
}

// ArrearsRow 欠费清单的一行
type ArrearsRow struct {
	UserID         string     `json:"userId"`
	Arrears        int64      `json:"arrears"`
	OrderCount     int        `json:"orderCount"`
	OrderIDs       []string   `json:"orderIds"`
	OldestAt       *time.Time `json:"oldestAt"`
	Days           int        `json:"days"`
	DegradedOrders int        `json:"degradedOrders"`
}

func (a *App) arrearsRows(ctx context.Context) ([]ArrearsRow, error) {
	bills, err := listBills(ctx, a.Pool, `pay_status=$1 ORDER BY bill_pushed_at`, stArrears)
	if err != nil {
		return nil, err
	}
	now := a.Now()
	idx := map[string]*ArrearsRow{}
	var order []string
	for _, b := range bills {
		row, ok := idx[b.UserID]
		if !ok {
			t := a.t(b.BillPushedAt)
			row = &ArrearsRow{UserID: b.UserID, OrderIDs: []string{}, OldestAt: &t, Days: int(now.Sub(b.BillPushedAt).Hours() / 24)}
			idx[b.UserID] = row
			order = append(order, b.UserID)
		}
		row.Arrears += b.unpaid()
		row.OrderCount++
		row.OrderIDs = append(row.OrderIDs, b.OrderID)
		if b.Degraded {
			row.DegradedOrders++
		}
	}
	out := make([]ArrearsRow, 0, len(order))
	for _, u := range order {
		out = append(out, *idx[u])
	}
	return out, nil
}

func (a *App) hArrears(w http.ResponseWriter, r *http.Request) {
	ad, ok := a.admin(w, r, "arrears.view")
	if !ok || !noScope(w, ad) {
		return
	}
	rows, err := a.arrearsRows(r.Context())
	if err != nil {
		a.failErr(w, err)
		return
	}
	q := r.URL.Query()
	key, desc := q.Get("sortKey"), q.Get("sortDir") != "asc"
	val := func(x ArrearsRow) int64 {
		switch key {
		case "arrears":
			return x.Arrears
		case "orderCount":
			return int64(x.OrderCount)
		default:
			return int64(x.Days)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if desc {
			return val(rows[i]) > val(rows[j])
		}
		return val(rows[i]) < val(rows[j])
	})
	var total int64
	for _, x := range rows {
		total += x.Arrears
	}
	page, size := pageParams(r)
	writeJSON(w, 200, map[string]any{"rows": pageSlice(rows, page, size), "total": len(rows), "page": page, "pageSize": size,
		"totals": map[string]any{"users": len(rows), "amount": total}})
}

func pageSlice[T any](xs []T, page, size int) []T {
	from := (page - 1) * size
	if from >= len(xs) {
		return []T{}
	}
	to := from + size
	if to > len(xs) {
		to = len(xs)
	}
	return xs[from:to]
}

var orderPerms = []string{"order.view", "settlement.manage", "ops.monitor", "degrade.closure.view", "arrears.view"}

func scopeOK(ad *auth.Admin, b *Bill) bool { return ad.Scope == "" || ad.Scope == b.OperatorID }

func (a *App) hAdminOrders(w http.ResponseWriter, r *http.Request) {
	ad, ok := a.admin(w, r, orderPerms...)
	if !ok {
		return
	}
	ids, ok := parseOrderIDs(r)
	if !ok {
		fail(w, 400, "INVALID", "订单号列表应为 1～200 个")
		return
	}
	out, err := a.batch(r.Context(), ids, true, func(b *Bill) bool { return scopeOK(ad, b) })
	if err != nil {
		a.failErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (a *App) hAdminOrder(w http.ResponseWriter, r *http.Request) {
	ad, ok := a.admin(w, r, orderPerms...)
	if !ok {
		return
	}
	b, err := getBill(r.Context(), a.Pool, `order_id=$1`, r.PathValue("orderId"))
	if err != nil {
		a.failErr(w, err)
		return
	}
	if !scopeOK(ad, b) {
		fail(w, 403, "SCOPE_DENIED", "只能查看和操作本运营商的数据")
		return
	}
	writeJSON(w, 200, a.orderBill(b, true))
}

func (a *App) csUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	ad, ok := a.admin(w, r, "user.view")
	if !ok || !noScope(w, ad) {
		return "", false
	}
	u := r.PathValue("userId")
	known, err := a.userKnown(r.Context(), u)
	if err != nil {
		a.failErr(w, err)
		return "", false
	}
	if !known {
		fail(w, 404, "NOT_FOUND", "没有该用户的计费记录")
		return "", false
	}
	return u, true
}

func (a *App) hUserSummary(w http.ResponseWriter, r *http.Request) {
	u, ok := a.csUser(w, r)
	if !ok {
		return
	}
	s, err := a.summary(r.Context(), u)
	if err != nil {
		a.failErr(w, err)
		return
	}
	writeJSON(w, 200, s)
}

var payStatuses = map[string]bool{"": true, stNone: true, stPaid: true, stPending: true, stDeposit: true, stArrears: true, stArrearsPaid: true}

func (a *App) hUserBills(w http.ResponseWriter, r *http.Request) {
	u, ok := a.csUser(w, r)
	if !ok {
		return
	}
	st := r.URL.Query().Get("payStatus")
	if !payStatuses[st] {
		fail(w, 400, "INVALID", "支付状态不正确")
		return
	}
	page, size := pageParams(r)
	ctx := r.Context()
	var total int
	if err := a.Pool.QueryRow(ctx, `SELECT count(*) FROM bills WHERE user_id=$1 AND ($2='' OR pay_status=$2)`, u, st).Scan(&total); err != nil {
		a.failErr(w, err)
		return
	}
	bills, err := listBills(ctx, a.Pool, `user_id=$1 AND ($2='' OR pay_status=$2) ORDER BY bill_pushed_at DESC, order_id LIMIT $3 OFFSET $4`, u, st, size, (page-1)*size)
	if err != nil {
		a.failErr(w, err)
		return
	}
	rows := []OrderBill{}
	for _, b := range bills {
		rows = append(rows, a.orderBill(b, true))
	}
	writeJSON(w, 200, map[string]any{"rows": rows, "total": total, "page": page, "pageSize": size})
}

func (a *App) hUserCards(w http.ResponseWriter, r *http.Request) {
	u, ok := a.csUser(w, r)
	if !ok {
		return
	}
	cs, err := a.userCards(r.Context(), u)
	if err != nil {
		a.failErr(w, err)
		return
	}
	writeJSON(w, 200, cs)
}

func (a *App) hUserCoupons(w http.ResponseWriter, r *http.Request) {
	u, ok := a.csUser(w, r)
	if !ok {
		return
	}
	a.writeCoupons(w, r, u)
}

func (a *App) hTodos(w http.ResponseWriter, r *http.Request) {
	ad, ok := a.admin(w, r)
	if !ok {
		return
	}
	out := []map[string]any{}
	if ad.Has("arrears.view") && ad.Scope == "" {
		var n int
		if err := a.Pool.QueryRow(r.Context(), `SELECT count(DISTINCT user_id) FROM bills WHERE pay_status=$1`, stArrears).Scan(&n); err != nil {
			a.failErr(w, err)
			return
		}
		if n > 0 {
			out = append(out, map[string]any{"key": "arrears", "label": "欠费用户", "count": n, "menu": "欠费与退款失败"})
		}
	}
	writeJSON(w, 200, out)
}

func (a *App) hBillsByStatus(w http.ResponseWriter, r *http.Request) {
	ad, ok := a.admin(w, r, "order.view", "settlement.manage", "arrears.view")
	if !ok {
		return
	}
	q := r.URL.Query()
	st := q.Get("payStatus")
	if !payStatuses[st] {
		fail(w, 400, "INVALID", "支付状态不正确")
		return
	}
	var from, to *time.Time
	for _, p := range []struct {
		k   string
		dst **time.Time
	}{{"from", &from}, {"to", &to}} {
		if s := q.Get(p.k); s != "" {
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				fail(w, 400, "INVALID", "时间格式应为 RFC 3339")
				return
			}
			*p.dst = &t
		}
	}
	page, size := pageParams(r)
	ctx := r.Context()
	where := `($1='' OR pay_status=$1) AND ($2::timestamptz IS NULL OR bill_pushed_at >= $2) AND ($3::timestamptz IS NULL OR bill_pushed_at <= $3) AND ($4='' OR operator_id=$4)`
	var total int
	if err := a.Pool.QueryRow(ctx, `SELECT count(*) FROM bills WHERE `+where, st, from, to, ad.Scope).Scan(&total); err != nil {
		a.failErr(w, err)
		return
	}
	bills, err := listBills(ctx, a.Pool, where+` ORDER BY bill_pushed_at DESC, order_id LIMIT $5 OFFSET $6`, st, from, to, ad.Scope, size, (page-1)*size)
	if err != nil {
		a.failErr(w, err)
		return
	}
	rows := []map[string]any{}
	for _, b := range bills {
		rows = append(rows, map[string]any{"orderId": b.OrderID, "userId": b.UserID, "payStatus": b.PayStatus, "payable": b.Payable, "unpaid": b.unpaid(), "billPushedAt": a.t(b.BillPushedAt)})
	}
	writeJSON(w, 200, map[string]any{"rows": rows, "total": total, "page": page, "pageSize": size})
}

/* ---------------- 内部 ---------------- */

func (a *App) hRefund(w http.ResponseWriter, r *http.Request) {
	caller, ok := a.internal(w, r)
	if !ok {
		return
	}
	var cmd RefundCommand
	raw, _ := readAll(r)
	if err := json.Unmarshal(raw, &cmd); err != nil {
		fail(w, 400, "INVALID", "请求体不是合法的 JSON")
		return
	}
	a.idem(w, r, "refund:"+caller, true, func(w http.ResponseWriter) {
		if utf8.RuneCountInString(strings.TrimSpace(cmd.Reason)) < 4 {
			fail(w, 400, "REASON_REQUIRED", "请填写操作事由（至少 4 个字）")
			return
		}
		if cmd.OrderID == "" || cmd.Amount <= 0 || cmd.Actor == nil || cmd.Actor.ID == "" {
			fail(w, 400, "INVALID", "订单号、退款金额（大于 0）与操作人必填")
			return
		}
		res, err := a.Refund(r.Context(), caller+":"+r.Header.Get("Idempotency-Key"), cmd)
		if err != nil {
			if errors.Is(err, errNotFound) {
				fail(w, 404, "NOT_FOUND", "没有找到该订单的账单")
				return
			}
			a.failErr(w, err)
			return
		}
		writeJSON(w, 200, res)
	})
}

func readAll(r *http.Request) ([]byte, error) {
	var b bytes.Buffer
	_, err := b.ReadFrom(http.MaxBytesReader(nil, r.Body, 1<<20))
	return b.Bytes(), err
}

func (a *App) hFacts(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.internal(w, r); !ok {
		return
	}
	b, err := getBill(r.Context(), a.Pool, `order_id=$1`, r.PathValue("orderId"))
	if err != nil {
		a.failErr(w, err)
		return
	}
	f, err := a.billFacts(r.Context(), b)
	if err != nil {
		a.failErr(w, err)
		return
	}
	writeJSON(w, 200, f)
}
