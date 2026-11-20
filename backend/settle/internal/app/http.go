package app

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"bike/settle/internal/auth"
	"bike/settle/internal/calc"
)

// ---------------- 基础件：错误体、令牌、权限、幂等 ----------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func errorBody(e *ApiError) map[string]any {
	b := map[string]any{"code": e.Code, "message": e.Message}
	for k, v := range e.Extra {
		b[k] = v
	}
	return b
}

func (a *App) writeErr(w http.ResponseWriter, err error) {
	if ae, ok := asAPI(err); ok {
		writeJSON(w, ae.Status, errorBody(ae))
		return
	}
	a.Log.Error("内部错误", "err", err)
	writeJSON(w, 500, map[string]any{"code": "SERVER_ERROR", "message": "服务暂时出了点问题，请稍后重试"})
}

// csvFile 是导出文件。
type csvFile struct {
	name  string
	count int
	data  []byte
}

type handler func(r *http.Request, c *auth.Claims) (any, error)

type route struct {
	perms []string // 有其一即可；空表示只要登录
	write bool
	op    string
}

// guard 验令牌、查权限与只读，再交给 h；写操作走幂等。
func (a *App) guard(rt route, h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || a.Verifier == nil {
			a.writeErr(w, apiErr(401, "ADMIN_NOT_LOGGED_IN", "登录已失效，请重新登录"))
			return
		}
		c, err := a.Verifier.Verify(strings.TrimSpace(tok), a.Now())
		if err != nil {
			a.writeErr(w, apiErr(401, "ADMIN_NOT_LOGGED_IN", "登录已失效，请重新登录"))
			return
		}
		if len(rt.perms) > 0 && !c.HasAny(rt.perms) {
			role := c.Role
			if role == "" {
				role = "当前岗位"
			}
			a.writeErr(w, apiErr(403, "FORBIDDEN", fmt.Sprintf("你的账号（%s）没有该操作的权限", role)))
			return
		}
		if rt.write && c.Readonly {
			a.writeErr(w, errReadonly)
			return
		}
		// 运营商退出（fleet.operator.exited）后，对该运营商 scope 的写操作一律拒绝，不依赖令牌里 readonly 何时刷新
		if rt.write && c.Scope != "" {
			var exited bool
			if err := a.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM operator_exits WHERE operator_id = $1)`, c.Scope).Scan(&exited); err != nil {
				a.writeErr(w, err)
				return
			}
			if exited {
				a.writeErr(w, errReadonly)
				return
			}
		}
		if rt.write {
			a.idempotent(w, r, rt.op, c, h)
			return
		}
		res, err := h(r, c)
		if err != nil {
			a.writeErr(w, err)
			return
		}
		if f, ok := res.(*csvFile); ok {
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(f.name))
			w.Header().Set("X-Total-Count", strconv.Itoa(f.count))
			w.WriteHeader(200)
			_, _ = w.Write(f.data)
			return
		}
		writeJSON(w, 200, res)
	}
}

// idempotent 同一个 Idempotency-Key（同操作、同账号）重复到达返回第一次的结果，不重复执行。
func (a *App) idempotent(w http.ResponseWriter, r *http.Request, op string, c *auth.Claims, h handler) {
	key := r.Header.Get("Idempotency-Key")
	if len(key) > 64 {
		a.writeErr(w, errInvalid("Idempotency-Key 最长 64 个字符"))
		return
	}
	ctx := r.Context()
	scoped := op + " " + r.URL.Path
	if key != "" {
		tag, err := a.DB.Exec(ctx, `INSERT INTO idempotency(idem_key, op, account_id, status, body) VALUES ($1,$2,$3,0,'') ON CONFLICT DO NOTHING`, key, scoped, c.AccountID)
		if err != nil {
			a.writeErr(w, err)
			return
		}
		if tag.RowsAffected() == 0 {
			var status int
			var body []byte
			if err := a.DB.QueryRow(ctx, `SELECT status, body FROM idempotency WHERE idem_key = $1 AND op = $2 AND account_id = $3`, key, scoped, c.AccountID).Scan(&status, &body); err != nil {
				a.writeErr(w, err)
				return
			}
			if status == 0 {
				a.writeErr(w, apiErr(409, "IN_PROGRESS", "同一请求正在处理，请稍后刷新查看结果"))
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(status)
			_, _ = w.Write(body)
			return
		}
	}
	res, err := h(r, c)
	status, payload := 200, any(res)
	if err != nil {
		ae, ok := asAPI(err)
		if !ok {
			if key != "" {
				_, _ = a.DB.Exec(context.Background(), `DELETE FROM idempotency WHERE idem_key = $1 AND op = $2 AND account_id = $3`, key, scoped, c.AccountID)
			}
			a.writeErr(w, err)
			return
		}
		status, payload = ae.Status, errorBody(ae)
	}
	body, _ := json.Marshal(payload)
	body = append(body, byte(10))
	if key != "" {
		_, _ = a.DB.Exec(context.Background(), `UPDATE idempotency SET status = $4, body = $5 WHERE idem_key = $1 AND op = $2 AND account_id = $3`, key, scoped, c.AccountID, status, body)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func actorOf(c *auth.Claims) Actor { return Actor{Kind: "人", ID: c.AccountID, Name: c.Name} }

func decode(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	b := new(bytes.Buffer)
	if _, err := b.ReadFrom(http.MaxBytesReader(nil, r.Body, 1<<20)); err != nil {
		return errInvalid("请求体过大或读取失败")
	}
	if b.Len() == 0 {
		return nil
	}
	if !utf8.Valid(b.Bytes()) {
		return errInvalid("请求体不是 UTF-8 编码")
	}
	if err := json.Unmarshal(b.Bytes(), v); err != nil {
		return errInvalid("请求体不是有效的 JSON")
	}
	return nil
}

func paging(q url.Values) (int, int, error) {
	page, size := 1, 20
	if v := q.Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return 0, 0, errInvalid("page 应为从 1 起的整数")
		}
		page = n
	}
	if v := q.Get("pageSize"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			return 0, 0, errInvalid("pageSize 应在 1～200 之间")
		}
		size = n
	}
	return page, size, nil
}

func requireScope(c *auth.Claims) (string, error) {
	if c.Scope == "" {
		return "", apiErr(403, "FORBIDDEN", "该页面只对运营商账号开放")
	}
	return c.Scope, nil
}

var settlementStatuses = map[string]bool{"待审核": true, "已审核": true, "已发送": true, "已到账": true}
var disputeStatuses = map[string]bool{"待核实": true, "已采纳": true, "已驳回": true, "无需调整": true}

// ---------------- 路由 ----------------

// Handler 返回全部 HTTP 接口。
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]bool{"ok": true}) })

	manage := []string{"settlement.manage"}
	adjust := []string{"settlement.adjust"}
	decide := []string{"dispute.decide"}
	pre := "/api/settle"

	// 财务：结算单
	mux.HandleFunc("GET "+pre+"/finance/settlements", a.guard(route{perms: manage}, func(r *http.Request, c *auth.Claims) (any, error) {
		q := r.URL.Query()
		page, size, err := paging(q)
		if err != nil {
			return nil, err
		}
		f := SettlementFilter{Month: q.Get("month"), OperatorID: q.Get("operatorId"), Status: q.Get("status"), Page: page, PageSize: size}
		if f.Month != "" && !calc.ValidMonth(f.Month) {
			return nil, errInvalid("结算月格式为 YYYY-MM")
		}
		if f.Status != "" && !settlementStatuses[f.Status] {
			return nil, errInvalid("结算单状态不正确")
		}
		if c.Scope != "" {
			if f.OperatorID != "" && f.OperatorID != c.Scope {
				return nil, errScope
			}
			f.OperatorID = c.Scope
		}
		return a.ListSettlements(r.Context(), f)
	}))
	mux.HandleFunc("GET "+pre+"/finance/settlements/preview", a.guard(route{perms: manage}, func(r *http.Request, c *auth.Claims) (any, error) {
		op := r.URL.Query().Get("operatorId")
		if op == "" {
			return nil, errNotFound("该运营商")
		}
		if c.Scope != "" && c.Scope != op {
			return nil, errScope
		}
		return a.Preview(r.Context(), op)
	}))
	mux.HandleFunc("POST "+pre+"/finance/settlements/generate", a.guard(route{perms: manage, write: true, op: "generateSettlements"}, func(r *http.Request, c *auth.Claims) (any, error) {
		var body struct {
			Month string `json:"month"`
		}
		if err := decode(r, &body); err != nil {
			return nil, err
		}
		return a.Generate(r.Context(), body.Month, "财务手动", actorOf(c))
	}))
	mux.HandleFunc("GET "+pre+"/finance/settlements/{settlementId}", a.guard(route{perms: manage}, func(r *http.Request, c *auth.Claims) (any, error) {
		return a.GetSettlement(r.Context(), c.Scope, r.PathValue("settlementId"), true)
	}))
	mux.HandleFunc("GET "+pre+"/finance/settlements/{settlementId}/export", a.guard(route{perms: manage}, func(r *http.Request, c *auth.Claims) (any, error) {
		v, err := a.GetSettlement(r.Context(), c.Scope, r.PathValue("settlementId"), true)
		if err != nil {
			return nil, err
		}
		return financeCSV(v), nil
	}))
	mux.HandleFunc("POST "+pre+"/finance/settlements/{settlementId}/adjustments", a.guard(route{perms: adjust, write: true, op: "addSettlementAdjustment"}, func(r *http.Request, c *auth.Claims) (any, error) {
		var body struct {
			BrandID  string   `json:"brandId"`
			FeeDelta *int64   `json:"feeDelta"`
			OrderIDs []string `json:"orderIds"`
			Reason   string   `json:"reason"`
		}
		if err := decode(r, &body); err != nil {
			return nil, err
		}
		sid := r.PathValue("settlementId")
		if _, err := a.GetSettlement(r.Context(), c.Scope, sid, false); err != nil {
			return nil, err
		}
		if body.FeeDelta == nil {
			return nil, errInvalid("请填写服务费增减金额")
		}
		return a.AddAdjustment(r.Context(), c.Scope, sid, body.BrandID, *body.FeeDelta, body.OrderIDs, body.Reason, actorOf(c))
	}))
	mux.HandleFunc("POST "+pre+"/finance/settlements/{settlementId}/adjustments/{adjustmentId}/confirm", a.guard(route{perms: adjust, write: true, op: "confirmSettlementAdjustment"}, func(r *http.Request, c *auth.Claims) (any, error) {
		var body struct {
			Note string `json:"note"`
		}
		if err := decode(r, &body); err != nil {
			return nil, err
		}
		return a.ConfirmAdjustment(r.Context(), c.Scope, r.PathValue("settlementId"), r.PathValue("adjustmentId"), body.Note, actorOf(c))
	}))
	mux.HandleFunc("POST "+pre+"/finance/settlements/{settlementId}/review", a.guard(route{perms: manage, write: true, op: "reviewSettlement"}, func(r *http.Request, c *auth.Claims) (any, error) {
		var body struct {
			Note string `json:"note"`
		}
		if err := decode(r, &body); err != nil {
			return nil, err
		}
		return a.Review(r.Context(), c.Scope, r.PathValue("settlementId"), body.Note, actorOf(c))
	}))
	mux.HandleFunc("POST "+pre+"/finance/settlements/{settlementId}/send", a.guard(route{perms: manage, write: true, op: "sendSettlement"}, func(r *http.Request, c *auth.Claims) (any, error) {
		return a.Send(r.Context(), c.Scope, r.PathValue("settlementId"), actorOf(c))
	}))
	mux.HandleFunc("POST "+pre+"/finance/settlements/{settlementId}/payment", a.guard(route{perms: manage, write: true, op: "registerSettlementPayment"}, func(r *http.Request, c *auth.Claims) (any, error) {
		var body struct {
			BankTradeNo string     `json:"bankTradeNo"`
			PaidAt      *time.Time `json:"paidAt"`
		}
		if err := decode(r, &body); err != nil {
			return nil, err
		}
		return a.RegisterPayment(r.Context(), c.Scope, r.PathValue("settlementId"), body.BankTradeNo, body.PaidAt, actorOf(c))
	}))

	// 财务：异议
	mux.HandleFunc("GET "+pre+"/finance/disputes", a.guard(route{perms: decide}, func(r *http.Request, c *auth.Claims) (any, error) {
		q := r.URL.Query()
		page, size, err := paging(q)
		if err != nil {
			return nil, err
		}
		st, op := q.Get("status"), q.Get("operatorId")
		if st != "" && !disputeStatuses[st] {
			return nil, errInvalid("异议状态不正确")
		}
		if c.Scope != "" {
			if op != "" && op != c.Scope {
				return nil, errScope
			}
			op = c.Scope
		}
		return a.ListDisputes(r.Context(), op, st, page, size)
	}))
	mux.HandleFunc("GET "+pre+"/finance/disputes/{disputeId}", a.guard(route{perms: decide}, func(r *http.Request, c *auth.Claims) (any, error) {
		return a.GetDispute(r.Context(), c.Scope, r.PathValue("disputeId"))
	}))
	mux.HandleFunc("POST "+pre+"/finance/disputes/{disputeId}/decide", a.guard(route{perms: decide, write: true, op: "decideDispute"}, func(r *http.Request, c *auth.Claims) (any, error) {
		var body struct {
			Decision    string            `json:"decision"`
			Corrections []CorrectionInput `json:"corrections"`
			Reason      string            `json:"reason"`
		}
		if err := decode(r, &body); err != nil {
			return nil, err
		}
		return a.DecideDispute(r.Context(), c.Scope, r.PathValue("disputeId"), body.Decision, body.Corrections, body.Reason, actorOf(c))
	}))

	// 运营商：订单明细
	mux.HandleFunc("GET "+pre+"/operator/orders", a.guard(route{perms: []string{"order.view.own"}}, func(r *http.Request, c *auth.Claims) (any, error) {
		f, err := orderFilter(r, c)
		if err != nil {
			return nil, err
		}
		return a.ListOperatorOrders(r.Context(), f)
	}))
	mux.HandleFunc("GET "+pre+"/operator/orders/export", a.guard(route{perms: []string{"order.export.own"}}, func(r *http.Request, c *auth.Claims) (any, error) {
		f, err := orderFilter(r, c)
		if err != nil {
			return nil, err
		}
		rows, err := a.ExportOperatorOrders(r.Context(), f)
		if err != nil {
			return nil, err
		}
		name := operatorName(r.Context(), a.DB, f.OperatorID)
		if name == "" {
			name = f.OperatorID
		}
		q := r.URL.Query()
		span := q.Get("date")
		if span == "" {
			span = q.Get("from") + "_" + q.Get("to")
		}
		return ordersCSV(fmt.Sprintf("%s_订单明细_%s.csv", name, span), rows), nil
	}))
	mux.HandleFunc("GET "+pre+"/operator/orders/{orderId}", a.guard(route{perms: []string{"order.view.own"}}, func(r *http.Request, c *auth.Claims) (any, error) {
		scope, err := requireScope(c)
		if err != nil {
			return nil, err
		}
		return a.GetOperatorOrder(r.Context(), scope, r.PathValue("orderId"))
	}))

	// 运营商：结算单与异议
	viewOwn := []string{"settlement.view.own"}
	mux.HandleFunc("GET "+pre+"/operator/settlements", a.guard(route{perms: viewOwn}, func(r *http.Request, c *auth.Claims) (any, error) {
		scope, err := requireScope(c)
		if err != nil {
			return nil, err
		}
		page, size, err := paging(r.URL.Query())
		if err != nil {
			return nil, err
		}
		return a.ListSettlements(r.Context(), SettlementFilter{OperatorID: scope, Page: page, PageSize: size})
	}))
	mux.HandleFunc("GET "+pre+"/operator/settlements/current", a.guard(route{perms: viewOwn}, func(r *http.Request, c *auth.Claims) (any, error) {
		scope, err := requireScope(c)
		if err != nil {
			return nil, err
		}
		return a.Preview(r.Context(), scope)
	}))
	mux.HandleFunc("GET "+pre+"/operator/settlements/{settlementId}", a.guard(route{perms: viewOwn}, func(r *http.Request, c *auth.Claims) (any, error) {
		scope, err := requireScope(c)
		if err != nil {
			return nil, err
		}
		return a.GetSettlement(r.Context(), scope, r.PathValue("settlementId"), true)
	}))
	mux.HandleFunc("GET "+pre+"/operator/settlements/{settlementId}/export", a.guard(route{perms: []string{"settlement.export.own"}}, func(r *http.Request, c *auth.Claims) (any, error) {
		scope, err := requireScope(c)
		if err != nil {
			return nil, err
		}
		v, err := a.GetSettlement(r.Context(), scope, r.PathValue("settlementId"), true)
		if err != nil {
			return nil, err
		}
		return operatorSettlementCSV(v), nil
	}))
	mux.HandleFunc("GET "+pre+"/operator/disputes", a.guard(route{perms: []string{"dispute.submit.own", "settlement.view.own"}}, func(r *http.Request, c *auth.Claims) (any, error) {
		scope, err := requireScope(c)
		if err != nil {
			return nil, err
		}
		page, size, err := paging(r.URL.Query())
		if err != nil {
			return nil, err
		}
		return a.ListDisputes(r.Context(), scope, "", page, size)
	}))
	mux.HandleFunc("POST "+pre+"/operator/disputes", a.guard(route{perms: []string{"dispute.submit.own"}, write: true, op: "submitOperatorDispute"}, func(r *http.Request, c *auth.Claims) (any, error) {
		var body struct {
			SettlementID string   `json:"settlementId"`
			OrderIDs     []string `json:"orderIds"`
			Field        string   `json:"field"`
			Claim        string   `json:"claim"`
		}
		if err := decode(r, &body); err != nil {
			return nil, err
		}
		return a.SubmitDispute(r.Context(), c.Scope, body.SettlementID, body.OrderIDs, body.Field, body.Claim, actorOf(c))
	}))

	// 公共
	orderPerms := []string{"order.view", "settlement.manage", "ops.monitor", "degrade.closure.view", "arrears.view", "order.view.own"}
	mux.HandleFunc("GET "+pre+"/orders/{orderId}/settlement", a.guard(route{perms: orderPerms}, func(r *http.Request, c *auth.Claims) (any, error) {
		return a.OrderSettlement(r.Context(), c.Scope, r.PathValue("orderId"))
	}))
	mux.HandleFunc("GET "+pre+"/order-settlements", a.guard(route{perms: orderPerms}, func(r *http.Request, c *auth.Claims) (any, error) {
		raw := r.URL.Query().Get("orderIds")
		var ids []string
		for _, s := range strings.Split(raw, ",") {
			if s = strings.TrimSpace(s); s != "" {
				ids = append(ids, s)
			}
		}
		if len(ids) == 0 || len(ids) > 200 {
			return nil, errInvalid("orderIds 应为 1～200 个订单号，逗号分隔")
		}
		return a.OrderSettlements(r.Context(), c.Scope, ids)
	}))
	mux.HandleFunc("GET "+pre+"/todos", a.guard(route{}, func(r *http.Request, c *auth.Claims) (any, error) {
		todos, err := a.Todos(r.Context(), c.Has, c.Scope)
		if err != nil {
			return nil, err
		}
		return map[string]any{"todos": todos}, nil
	}))
	return mux
}

func orderFilter(r *http.Request, c *auth.Claims) (OrderFilter, error) {
	scope, err := requireScope(c)
	if err != nil {
		return OrderFilter{}, err
	}
	q := r.URL.Query()
	page, size, err := paging(q)
	if err != nil {
		return OrderFilter{}, err
	}
	f := OrderFilter{OperatorID: scope, Date: q.Get("date"), BrandID: q.Get("brandId"), Status: q.Get("status"), Q: strings.TrimSpace(q.Get("q")), Page: page, Size: size, SortKey: q.Get("sortKey"), Desc: true}
	if f.Date != "" {
		if _, err := time.Parse("2006-01-02", f.Date); err != nil {
			return f, errInvalid("日期格式为 YYYY-MM-DD")
		}
	}
	for _, k := range []string{"from", "to"} {
		if v := q.Get(k); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return f, errInvalid(k + " 应为带时区的 RFC 3339 时刻")
			}
			if k == "from" {
				f.From = &t
			} else {
				f.To = &t
			}
		}
	}
	if v := q.Get("degraded"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return f, errInvalid("degraded 应为 true 或 false")
		}
		f.Degraded = &b
	}
	if f.Status != "" && f.Status != "开锁失败" && f.Status != "骑行中" && f.Status != "已结束" && f.Status != "待人工处理" {
		return f, errInvalid("订单状态不正确")
	}
	if f.SortKey != "" {
		if _, ok := orderSortCols[f.SortKey]; !ok {
			return f, errInvalid("不支持按 " + f.SortKey + " 排序")
		}
	}
	switch q.Get("sortDir") {
	case "", "desc":
	case "asc":
		f.Desc = false
	default:
		return f, errInvalid("sortDir 应为 asc 或 desc")
	}
	return f, nil
}

// ---------------- CSV ----------------

func ts(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.In(calc.Loc).Format("2006-01-02 15:04:05")
}

func optInt(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}

func optYuan(p *int64) string {
	if p == nil {
		return ""
	}
	return yuan(*p)
}

func optKm(p *float64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatFloat(*p, 'f', 1, 64)
}

func yesNo(b bool) string {
	if b {
		return "是"
	}
	return "否"
}

func buildCSV(name string, header []string, rows [][]string) *csvFile {
	var buf bytes.Buffer
	buf.Write([]byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM
	w := csv.NewWriter(&buf)
	_ = w.Write(header)
	_ = w.WriteAll(rows)
	return &csvFile{name: name, count: len(rows), data: buf.Bytes()}
}

func brandNames(v SettlementView) map[string]string {
	m := map[string]string{}
	for _, b := range v.Brands {
		m[b.BrandID] = b.BrandName
	}
	for _, a := range v.Adjustments {
		m[a.BrandID] = a.BrandName
	}
	return m
}

func financeCSV(v SettlementView) *csvFile {
	names := brandNames(v)
	var rows [][]string
	for _, d := range *v.Details {
		bm := d.BilledMinutes
		end := d.EndAt
		rows = append(rows, []string{d.OrderID, names[d.BrandID], ts(&end), optInt(&bm), yuan(d.Receivable), yuan(d.Base), d.Ratio.String(), yuan(d.Fee), deref(d.Remark)})
	}
	return buildCSV(v.SettlementID+".csv", []string{"订单号", "品牌", "结束", "计费分钟", "订单应收", "服务费基数", "比例", "服务费", "备注"}, rows)
}

func operatorSettlementCSV(v SettlementView) *csvFile {
	names := brandNames(v)
	var rows [][]string
	for _, d := range *v.Details {
		bm := d.BilledMinutes
		end := d.EndAt
		rows = append(rows, []string{d.OrderID, names[d.BrandID], ts(d.StartAt), ts(&end), optInt(&bm), optKm(d.DistanceKm), yesNo(d.Degraded), d.PricingVersionID,
			yuan(d.Receivable), yuan(d.RefundedAtGeneration), yuan(d.Base), d.Ratio.String(), yuan(d.Fee), deref(d.Remark)})
	}
	for _, a := range v.Adjustments {
		for _, it := range a.Items {
			end := it.EndAt
			rows = append(rows, []string{it.OrderID, a.BrandName, "", ts(&end), "", "", "", "", "", "", yuan(it.NewBase), it.Ratio.String(), yuan(it.FeeDelta),
				fmt.Sprintf("调整项 %s（%s）", a.AdjustmentID, a.Type)})
		}
	}
	return buildCSV(v.SettlementID+".csv", []string{"订单号", "品牌", "开始", "结束", "计费分钟", "里程", "降级单", "价格版本", "订单应收", "出单时已退款", "服务费基数", "比例", "服务费", "备注"}, rows)
}

func ordersCSV(name string, list []OperatorOrderView) *csvFile {
	var rows [][]string
	for _, o := range list {
		rows = append(rows, []string{o.OrderID, deref(o.PlateNo), o.BrandName, o.Status, ts(o.RequestAt), deref(o.UnlockResult), ts(o.StartAt), ts(o.EndAt),
			ts(o.LockReportAt), ts(o.PileInAt), deref(o.EndMethod), deref(o.InStationBasis), optInt(o.DurationSec), optInt(o.BilledMinutes), optKm(o.DistanceKm),
			optYuan(o.Receivable), yesNo(o.Degraded), yesNo(o.LateReport), deref(o.SettleMonth)})
	}
	return buildCSV(name, []string{"订单号", "车号", "品牌", "状态", "开锁请求时刻", "开锁结果", "计费起点", "计费终点", "锁车报告时刻", "桩在位时刻", "结束方式", "在点依据",
		"时长（秒）", "计费分钟", "里程（公里）", "订单应收（元）", "降级单", "补报", "结算月"}, rows)
}
