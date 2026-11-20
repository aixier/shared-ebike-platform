package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"bike/deposit/internal/auth"
	"bike/deposit/internal/workday"
)

// Handler 全部 HTTP 接口（contracts/openapi/deposit.yaml）与 /healthz。
func (a *App) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]any{"ok": true}) })

	m.HandleFunc("GET /api/deposit/me/state", a.userAuth(a.getMyState))
	m.HandleFunc("GET /api/deposit/me", a.userAuth(a.getMyPage))
	m.HandleFunc("POST /api/deposit/me/credit-free", a.userAuth(a.idem(false, a.postCreditFree)))
	m.HandleFunc("POST /api/deposit/me/payment", a.userAuth(a.idem(false, a.postPayment)))
	m.HandleFunc("POST /api/deposit/me/topup", a.userAuth(a.idem(false, a.postTopup)))
	m.HandleFunc("POST /api/deposit/me/refund", a.userAuth(a.idem(false, a.postRefund)))

	m.HandleFunc("GET /api/deposit/admin/summary", a.adminAuth([]string{"deposit.view"}, false, a.getSummary))
	m.HandleFunc("GET /api/deposit/admin/flows", a.adminAuth([]string{"deposit.view"}, false, a.getFlows))
	m.HandleFunc("GET /api/deposit/admin/users/{userId}", a.adminAuth([]string{"user.view"}, false, a.getUserDeposit))

	m.HandleFunc("POST /internal/deposit/deductions", a.internalAuth([]string{"bill"}, a.idem(true, a.postDeduction)))
	m.HandleFunc("POST /internal/deposit/assist-refunds", a.internalAuth([]string{"cs"}, a.idem(true, a.postAssistRefund)))
	return utf8Body(m)
}

// utf8Body 请求体不是 UTF-8 一律 400 INVALID（在鉴权之前判，错误体与各接口一致）。
func utf8Body(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Body != http.NoBody && (r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch) {
			b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
			if err != nil {
				writeErr(w, errInvalid("请求体读取失败或过大"), strings.HasPrefix(r.URL.Path, "/api/deposit/me"))
				return
			}
			if !utf8.Valid(b) {
				writeErr(w, errInvalid("请求体不是 UTF-8 编码"), strings.HasPrefix(r.URL.Path, "/api/deposit/me"))
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(b))
		}
		next.ServeHTTP(w, r)
	})
}

type ctxKey int

const (
	keyPrincipal ctxKey = iota // 用户编号、管理账号或调用方模块名
	keyUserSide
)

func principal(r *http.Request) string { s, _ := r.Context().Value(keyPrincipal).(string); return s }
func userSide(r *http.Request) bool    { b, _ := r.Context().Value(keyUserSide).(bool); return b }

func (a *App) fail(w http.ResponseWriter, r *http.Request, err error) {
	var e *apiError
	if !errors.As(err, &e) {
		a.Log.Error("内部错误", "path", r.URL.Path, "err", err)
	}
	writeErr(w, err, userSide(r))
}

// ---- 鉴权 ----

func (a *App) userAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), keyUserSide, true)
		uid, err := a.UserVerifier.VerifyUser(auth.Bearer(r))
		if err != nil {
			writeErr(w, errNotLoggedIn(), true)
			return
		}
		h(w, r.WithContext(context.WithValue(ctx, keyPrincipal, uid)))
	}
}

// adminAuth 管理端鉴权。write 为真的是写接口：只读令牌（运营商已退出）一律 READONLY。
func (a *App) adminAuth(perms []string, write bool, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ad, err := a.AdminVerifier.VerifyAdmin(auth.Bearer(r))
		if err != nil {
			writeErr(w, errAdminNotLoggedIn(), false)
			return
		}
		if !ad.HasAny(perms...) {
			writeErr(w, errForbidden(), false)
			return
		}
		if write && ad.Readonly {
			writeErr(w, newErr(403, "READONLY", "当前账号为只读，不能执行写操作"), false)
			return
		}
		// 押金数据都是骑行用户与平台专户的数据，不属于任何运营商：带数据范围的账号一律越界
		if ad.Scope != nil {
			writeErr(w, errScopeDenied(), false)
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), keyPrincipal, ad.AccountID)))
	}
}

func (a *App) internalAuth(callers []string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller, ok := a.Callers.Check(r)
		if !ok {
			writeErr(w, newErr(401, "UNAUTHORIZED", "服务凭证无效"), false)
			return
		}
		if !slices.Contains(callers, caller) {
			writeErr(w, newErr(403, "FORBIDDEN", "该模块不能调用此命令"), false)
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), keyPrincipal, caller)))
	}
}

// ---- 幂等 ----

type recorder struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (r *recorder) WriteHeader(s int) { r.status = s }
func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = 200
	}
	return r.buf.Write(b)
}

// noStore 处理函数通过它声明本次结果不入幂等表（结果未知，重试时要取最新结果）。
const noStoreHeader = "X-Deposit-No-Store"

// idem 写接口幂等：同一主体同一幂等键重复到达返回第一次的结果。required 为真时没带键拒绝（/internal 命令）。
func (a *App) idem(required bool, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			if required {
				writeErr(w, errInvalid("缺少请求头 Idempotency-Key"), false)
				return
			}
			h(w, r)
			return
		}
		if len(key) > 64 {
			writeErr(w, errInvalid("Idempotency-Key 不能超过 64 个字符"), userSide(r))
			return
		}
		ctx := r.Context()
		scope := principal(r) + " " + r.Method + " " + r.URL.Path
		tag, err := a.DB.Exec(ctx, `INSERT INTO idempotency (scope, key, created_at) VALUES ($1,$2,now()) ON CONFLICT DO NOTHING`, scope, key)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if tag.RowsAffected() == 0 {
			var status *int
			var body []byte
			if err := a.DB.QueryRow(ctx, `SELECT status, body FROM idempotency WHERE scope=$1 AND key=$2`, scope, key).Scan(&status, &body); err != nil {
				a.fail(w, r, err)
				return
			}
			if status == nil {
				writeErr(w, newErr(409, "CONFLICT", "同一请求正在处理，请稍后再试").with("next", next("刷新", "retry")), userSide(r))
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(*status)
			_, _ = w.Write(body)
			return
		}
		rec := &recorder{ResponseWriter: w}
		h(rec, r)
		if rec.status == 0 {
			rec.status = 200
		}
		store := rec.status < 500 && rec.Header().Get(noStoreHeader) == ""
		if store {
			var e struct {
				Code string `json:"code"`
			}
			_ = json.Unmarshal(rec.buf.Bytes(), &e)
			store = e.Code != "CONFLICT" && e.Code != "SERVER_BUSY"
		}
		bg := context.WithoutCancel(ctx)
		if store {
			_, _ = a.DB.Exec(bg, `UPDATE idempotency SET status=$3, body=$4 WHERE scope=$1 AND key=$2`, scope, key, rec.status, rec.buf.Bytes())
		} else {
			_, _ = a.DB.Exec(bg, `DELETE FROM idempotency WHERE scope=$1 AND key=$2`, scope, key)
		}
		w.Header().Del(noStoreHeader)
		w.WriteHeader(rec.status)
		_, _ = w.Write(rec.buf.Bytes())
	}
}

// ---- 用户端 ----

func (a *App) getMyState(w http.ResponseWriter, r *http.Request) {
	acc, err := readAccount(r.Context(), a.DB, principal(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	v, _, err := a.stateView(r.Context(), a.DB, acc)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (a *App) getMyPage(w http.ResponseWriter, r *http.Request) {
	ctx, uid := r.Context(), principal(r)
	rl, err := a.currentRule(ctx, a.DB)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if rl == nil {
		a.fail(w, r, errBusy("押金规则尚未同步，请稍后再试"))
		return
	}
	acc, err := readAccount(ctx, a.DB, uid)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	state, d, err := a.stateView(ctx, a.DB, acc)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	prev, prevRefund, err := a.previousView(ctx, a.DB, acc)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	days := a.refundWorkdays(ctx, a.DB)
	page := map[string]any{"mode": state["mode"], "deposit": nil, "creditFree": nil, "previousDeposit": prev,
		"rule":      map[string]any{"depositAmount": rl.Amount, "creditFreeThreshold": rl.Threshold, "refundWorkdays": days},
		"canRefund": false, "blockers": []blocker{}, "refundAmount": int64(0), "expectedAtIfApplyNow": a.ts(workday.Add(a.now(), days)),
		"awaitingPayee": false, "refundFailedPaymentId": nil}
	var awaiting *refundReq
	if d != nil {
		page["deposit"] = map[string]any{"amountPaid": d.AmountPaid, "balance": d.Balance, "status": d.Status, "paidAt": a.ts(d.PaidAt), "refund": state["refund"]}
		page["refundAmount"] = d.Balance
		if refundable(d) {
			bl, err := refundBlockers(ctx, a.DB, uid)
			if err != nil {
				a.fail(w, r, err)
				return
			}
			page["blockers"], page["canRefund"] = bl, len(bl) == 0
		}
		if cur, err := latestRefund(ctx, a.DB, d.ID); err == nil && cur != nil && cur.AwaitingPayee {
			awaiting = cur
		}
	}
	if acc.Mode == "免押" {
		page["creditFree"] = map[string]any{"provider": acc.CreditProvider, "openedAt": a.tsp(acc.CreditOpenedAt)}
	}
	if awaiting == nil && prevRefund != nil && prevRefund.AwaitingPayee {
		awaiting = prevRefund
	}
	if awaiting != nil {
		page["awaitingPayee"], page["refundFailedPaymentId"] = true, awaiting.PaymentID
	}
	writeJSON(w, 200, page)
}

func (a *App) postCreditFree(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider string `json:"provider"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err.Error() != "EOF" {
			a.fail(w, r, errInvalid("请求体不是合法 JSON"))
			return
		}
	}
	out, err := a.openCreditFree(r.Context(), principal(r), body.Provider)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}

func (a *App) postCharge(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			key = "auto-" + newEventID() // 没带幂等键：每次都是新请求
		}
		out, err := a.charge(r.Context(), principal(r), key, kind)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		writeJSON(w, 200, out)
	}
}

func (a *App) postPayment(w http.ResponseWriter, r *http.Request) { a.postCharge("押金缴纳")(w, r) }
func (a *App) postTopup(w http.ResponseWriter, r *http.Request)   { a.postCharge("押金补缴")(w, r) }

func (a *App) postRefund(w http.ResponseWriter, r *http.Request) {
	uid := principal(r)
	rf, err := a.requestRefund(r.Context(), uid, userActor(uid), nil, nil)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"refund": a.refundView(rf), "text": a.refundText(rf)})
}

// ---- 财务与客服 ----

func (a *App) getSummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	l := map[string]int64{}
	rows, err := a.DB.Query(ctx, `SELECT type, status, COALESCE(sum(amount),0) FROM ledger GROUP BY type, status`)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	for rows.Next() {
		var t, s string
		var v int64
		if err := rows.Scan(&t, &s, &v); err != nil {
			rows.Close()
			a.fail(w, r, err)
			return
		}
		l[t+"/"+s] = v
	}
	rows.Close()
	paid, top, ded, ref, man := l["押金缴纳/成功"], l["押金补缴/成功"], l["押金抵扣/成功"], l["退押/成功"], l["人工转账退押/成功"]
	users := map[string]int{"已缴": 0, "押金不足": 0, "退押中": 0, "已退": 0}
	urows, err := a.DB.Query(ctx, `SELECT d.status, count(*) FROM accounts a JOIN deposits d ON d.deposit_id = a.deposit_id WHERE a.mode='押金' GROUP BY d.status`)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	for urows.Next() {
		var s string
		var n int
		_ = urows.Scan(&s, &n)
		users[s] = n
	}
	urows.Close()
	var sum int64
	if err := a.DB.QueryRow(ctx, `SELECT COALESCE(sum(balance),0) FROM deposits`).Scan(&sum); err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"ledger": map[string]any{"paidIn": paid, "toppedUp": top, "deducted": ded, "refunded": ref, "manualRefunded": man,
			"balance": paid + top - ded - ref - man, "refundingAmount": l["退押/处理中"]},
		"users": users, "userBalanceSum": sum,
		"note": "押金专户与骑行收入户分开记账；押金只抵扣本人未付骑行费，不用于支付服务费",
	})
}

var flowTypes = []string{"押金缴纳", "押金补缴", "押金抵扣", "退押", "人工转账退押"}
var flowStatuses = []string{"成功", "失败", "处理中"}

func (a *App) getFlows(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	where, args := []string{"true"}, []any{}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(args))))
	}
	if t := q.Get("type"); t != "" {
		if !slices.Contains(flowTypes, t) {
			a.fail(w, r, errInvalid("流水类型不对"))
			return
		}
		add("type = ?", t)
	}
	if s := q.Get("status"); s != "" {
		if !slices.Contains(flowStatuses, s) {
			a.fail(w, r, errInvalid("流水状态不对"))
			return
		}
		add("status = ?", s)
	}
	if u := q.Get("userId"); u != "" {
		add("user_id = ?", u)
	}
	for _, p := range []struct{ name, cond string }{{"from", "created_at >= ?"}, {"to", "created_at <= ?"}} {
		if v := q.Get(p.name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				a.fail(w, r, errInvalid(p.name+" 应为 RFC 3339 时间"))
				return
			}
			add(p.cond, t)
		}
	}
	page, size := atoiDefault(q.Get("page"), 1), atoiDefault(q.Get("pageSize"), 20)
	if page < 1 || size < 1 || size > 200 {
		a.fail(w, r, errInvalid("分页参数不对"))
		return
	}
	sortCol := map[string]string{"": "created_at", "createdAt": "created_at", "amount": "amount", "completedAt": "completed_at"}[q.Get("sortKey")]
	if sortCol == "" {
		a.fail(w, r, errInvalid("排序字段不对"))
		return
	}
	dir := "DESC"
	if q.Get("sortDir") == "asc" {
		dir = "ASC"
	}
	cond := strings.Join(where, " AND ")
	ctx := r.Context()
	var total int
	if err := a.DB.QueryRow(ctx, `SELECT count(*) FROM ledger WHERE `+cond, args...).Scan(&total); err != nil {
		a.fail(w, r, err)
		return
	}
	rows, err := a.DB.Query(ctx, `SELECT payment_id, type, direction, user_id, order_id, amount, channel, status, fail_reason, created_at, completed_at, related_payment_id
		FROM ledger WHERE `+cond+` ORDER BY `+sortCol+` `+dir+`, payment_id `+dir+` LIMIT `+strconv.Itoa(size)+` OFFSET `+strconv.Itoa((page-1)*size), args...)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var pid, typ, dirn, uid, st string
		var order, ch, fr, rel *string
		var amt int64
		var created time.Time
		var completed *time.Time
		if err := rows.Scan(&pid, &typ, &dirn, &uid, &order, &amt, &ch, &st, &fr, &created, &completed, &rel); err != nil {
			a.fail(w, r, err)
			return
		}
		out = append(out, map[string]any{"paymentId": pid, "type": typ, "direction": dirn, "userId": uid, "orderId": order, "amount": amt, "channel": ch,
			"status": st, "failReason": fr, "createdAt": a.ts(created), "completedAt": a.tsp(completed), "relatedPaymentId": rel})
	}
	writeJSON(w, 200, map[string]any{"rows": out, "total": total, "page": page, "pageSize": size})
}

func atoiDefault(s string, d int) int {
	if s == "" {
		return d
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return -1
	}
	return n
}

func (a *App) getUserDeposit(w http.ResponseWriter, r *http.Request) {
	ctx, uid := r.Context(), r.PathValue("userId")
	acc, err := readAccount(ctx, a.DB, uid)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if !acc.exists {
		f, err := loadFlags(ctx, a.DB, uid)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if !f.known {
			a.fail(w, r, newErr(404, "NOT_FOUND", "没有找到该用户的押金记录"))
			return
		}
	}
	state, d, err := a.stateView(ctx, a.DB, acc)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	prev, _, err := a.previousView(ctx, a.DB, acc)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	bl := []blocker{}
	if refundable(d) {
		if bl, err = refundBlockers(ctx, a.DB, uid); err != nil {
			a.fail(w, r, err)
			return
		}
	}
	writeJSON(w, 200, map[string]any{"userId": uid, "deposit": state, "previousDeposit": prev, "canRefund": refundable(d) && len(bl) == 0, "refundBlockers": bl})
}

// ---- 模块间命令 ----

func (a *App) postDeduction(w http.ResponseWriter, r *http.Request) {
	var cmd deductCmd
	if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
		a.fail(w, r, errInvalid("请求体不是合法 JSON"))
		return
	}
	if cmd.UserID == "" || cmd.OrderID == "" || cmd.Amount <= 0 {
		a.fail(w, r, errInvalid("userId、orderId 必填，amount 须大于 0"))
		return
	}
	out, err := a.deduct(r.Context(), principal(r), r.Header.Get("Idempotency-Key"), cmd)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if out["result"] == "结果未知" {
		w.Header().Set(noStoreHeader, "1")
	}
	writeJSON(w, 200, out)
}

func (a *App) postAssistRefund(w http.ResponseWriter, r *http.Request) {
	var cmd struct {
		UserID   string  `json:"userId"`
		Reason   string  `json:"reason"`
		Actor    *Actor  `json:"actor"`
		TicketID *string `json:"ticketId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
		a.fail(w, r, errInvalid("请求体不是合法 JSON"))
		return
	}
	if cmd.UserID == "" || cmd.Actor == nil || cmd.Actor.ID == "" || cmd.Actor.Kind == "" {
		a.fail(w, r, errInvalid("userId、actor 必填"))
		return
	}
	if utf8.RuneCountInString(strings.TrimSpace(cmd.Reason)) < 4 {
		a.fail(w, r, newErr(400, "REASON_REQUIRED", "请填写事由，至少 4 个字"))
		return
	}
	reason := strings.TrimSpace(cmd.Reason)
	rf, err := a.requestRefund(r.Context(), cmd.UserID, *cmd.Actor, &reason, cmd.TicketID)
	var e *apiError
	switch {
	case err == nil:
		writeJSON(w, 200, map[string]any{"result": "成功", "userId": cmd.UserID, "failCode": nil, "failReason": nil, "blockers": []blocker{},
			"refund": a.refundView(rf), "text": a.refundText(rf)})
	case errors.As(err, &e) && (e.Code == "REFUND_BLOCKED" || e.Code == "NOT_DEPOSIT_USER"):
		bl, _ := e.Extra["blockers"].([]blocker)
		if bl == nil {
			bl = []blocker{}
		}
		writeJSON(w, 200, map[string]any{"result": "失败", "userId": cmd.UserID, "failCode": e.Code, "failReason": e.Message, "blockers": bl, "refund": nil, "text": e.Message})
	case errors.As(err, &e) && e.Code == "CONFLICT":
		w.Header().Set(noStoreHeader, "1")
		writeJSON(w, 200, map[string]any{"result": "结果未知", "userId": cmd.UserID, "failCode": nil, "failReason": e.Message, "blockers": []blocker{}, "refund": nil, "text": e.Message})
	default:
		a.fail(w, r, err)
	}
}
