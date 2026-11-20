package app

// HTTP 接口：按 contracts/openapi/pay.yaml 逐个实现。
// trace: pay.yaml 全部 operationId

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"bike/pay/internal/auth"
	"bike/pay/internal/channel"
)

// Handler 路由
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]bool{"ok": true}) })

	mux.HandleFunc("GET /api/pay/me/payments", a.user(a.listMyOrderPayments))
	mux.HandleFunc("POST /api/pay/me/payments/{paymentId}/payee", a.user(a.submitPayeeH))

	mux.HandleFunc("GET /api/pay/admin/recon", a.admin([]string{"recon.manage"}, false, a.listReconH))
	mux.HandleFunc("GET /api/pay/admin/recon/{dateOrId}", a.admin([]string{"recon.manage"}, false, a.getReconH))
	mux.HandleFunc("POST /api/pay/admin/recon/{reconId}/diff-items/{index}/handle", a.admin([]string{"recon.manage"}, true, a.handleDiffH))
	mux.HandleFunc("GET /api/pay/admin/refund-failures", a.admin([]string{"refund.manualTransfer"}, false, a.listRefundFailuresH))
	mux.HandleFunc("POST /api/pay/admin/refund-failures/{paymentId}/transfer", a.admin([]string{"refund.manualTransfer"}, true, a.transferH))
	mux.HandleFunc("GET /api/pay/admin/orders/{orderId}/payments", a.admin([]string{"order.view", "settlement.manage", "ops.monitor", "degrade.closure.view", "arrears.view"}, false, a.listOrderPaymentsH))
	mux.HandleFunc("GET /api/pay/admin/users/{userId}/payments", a.admin([]string{"user.view"}, false, a.listUserPaymentsH))
	mux.HandleFunc("GET /api/pay/admin/todos", a.admin(nil, false, a.todosH))
	mux.HandleFunc("GET /api/pay/admin/income-summary", a.admin([]string{"deposit.view", "recon.manage"}, false, a.incomeSummaryH))

	mux.HandleFunc("POST /internal/pay/credit-charges", a.internal(a.creditChargeH))
	mux.HandleFunc("POST /internal/pay/payments", a.internal(a.oneTapH))
	mux.HandleFunc("POST /internal/pay/one-tap-payments", a.internal(a.oneTapH)) // 旧路径，兼容
	mux.HandleFunc("POST /internal/pay/refunds", a.internal(a.refundH))
	mux.HandleFunc("POST /internal/pay/deposit-charges", a.internal(a.depositChargeH))
	mux.HandleFunc("POST /internal/pay/deposit-refunds", a.internal(a.depositRefundH))
	mux.HandleFunc("POST /internal/pay/deposit-transfers", a.internal(a.depositTransferH))
	mux.HandleFunc("POST /internal/pay/payments/{paymentId}/confirm", a.internal(a.confirmH))
	return mux
}

func (a *App) fail(w http.ResponseWriter, err error) {
	a.Log.Error("内部异常", "err", err)
	writeErr(w, apiErr("INTERNAL", ""))
}

// ---------- 鉴权中间层 ----------

type userHandler func(w http.ResponseWriter, r *http.Request, userID string)

func (a *App) user(h userHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, err := a.Verifier.User(r)
		if err != nil {
			writeErr(w, apiErr("NOT_LOGGED_IN", ""))
			return
		}
		h(w, r, uid)
	}
}

type adminHandler func(w http.ResponseWriter, r *http.Request, ad *auth.Admin)

// admin 验管理令牌；perms 有其一即可（nil 不查）；write 为真时只读账号拒绝。
// pay 的数据不按运营商划分，带数据范围的运营商账号一律越界。
func (a *App) admin(perms []string, write bool, h adminHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ad, err := a.Verifier.Admin(r)
		if err != nil {
			writeErr(w, apiErr("ADMIN_NOT_LOGGED_IN", ""))
			return
		}
		if perms != nil && !ad.HasAny(perms...) {
			writeErr(w, apiErr("FORBIDDEN", ""))
			return
		}
		if ad.Scope != nil {
			writeErr(w, apiErr("SCOPE_DENIED", ""))
			return
		}
		if write && ad.Readonly {
			writeErr(w, apiErr("READONLY", ""))
			return
		}
		h(w, r, ad)
	}
}

type internalHandler func(w http.ResponseWriter, r *http.Request, scope, key string)

// internal 验服务凭证；幂等键必带
func (a *App) internal(h internalHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller, ok := a.Callers.Check(r)
		if !ok {
			writeErr(w, apiErr("UNAUTHORIZED", ""))
			return
		}
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if key == "" || len(key) > 64 {
			writeErr(w, apiErr("INVALID", "缺少幂等键 Idempotency-Key"))
			return
		}
		h(w, r, caller+" "+r.Pattern, key)
	}
}

func decode(r *http.Request, v any) *APIError {
	b, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, 1<<20))
	if err != nil {
		return apiErr("INVALID", "请求体不是有效的 JSON")
	}
	if !utf8.Valid(b) {
		return apiErr("INVALID", "请求体不是 UTF-8 编码")
	}
	if err := json.NewDecoder(bytes.NewReader(b)).Decode(v); err != nil {
		return apiErr("INVALID", "请求体不是有效的 JSON")
	}
	return nil
}

// apiIdem 页面写接口的幂等：带了键且曾成功的，返回首次结果
func (a *App) apiIdem(w http.ResponseWriter, r *http.Request, scope string, fn func() (any, *APIError, error)) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	ctx := r.Context()
	if key != "" {
		h, err := a.idemGet(ctx, a.Pool, scope, key)
		if err != nil {
			a.fail(w, err)
			return
		}
		if h != nil && h.body != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(h.status)
			w.Write(h.body)
			return
		}
	}
	out, ae, err := fn()
	if err != nil {
		a.fail(w, err)
		return
	}
	if ae != nil {
		writeErr(w, ae)
		return
	}
	if key != "" {
		if tx, err := a.Pool.Begin(ctx); err == nil {
			_ = a.idemPut(ctx, tx, scope, key, nil, 200, out)
			_ = tx.Commit(ctx)
		}
	}
	writeJSON(w, 200, out)
}

// ---------- 用户端 ----------

func (a *App) listMyOrderPayments(w http.ResponseWriter, r *http.Request, uid string) {
	oid := strings.TrimSpace(r.URL.Query().Get("orderId"))
	if oid == "" {
		writeErr(w, apiErr("INVALID", "缺少订单号"))
		return
	}
	ps, err := a.queryPayments(r.Context(), a.Pool, `WHERE c.order_id = $1 AND c.user_id = $2 ORDER BY c.created_at, c.seq`, oid, uid)
	if err != nil {
		a.fail(w, err)
		return
	}
	out := make([]UserPayment, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.userView())
	}
	writeJSON(w, 200, out)
}

func (a *App) submitPayeeH(w http.ResponseWriter, r *http.Request, uid string) {
	var req submitPayeeReq
	if ae := decode(r, &req); ae != nil {
		writeErr(w, ae)
		return
	}
	id := r.PathValue("paymentId")
	a.apiIdem(w, r, "user:"+uid+" submitPayee "+id, func() (any, *APIError, error) {
		return a.submitPayee(r.Context(), uid, id, req)
	})
}

// ---------- 财务、客服、公共 ----------

func adminActor(ad *auth.Admin) Actor {
	kind := "人"
	if ad.Role == "智能体" {
		kind = "智能体"
	}
	return Actor{Kind: kind, ID: ad.AccountID, Name: ad.Name}
}

func pageParams(r *http.Request) (int, int, *APIError) {
	q := r.URL.Query()
	page, size := 1, 20
	if s := q.Get("page"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return 0, 0, apiErr("INVALID", "page 应为大于 0 的整数")
		}
		page = n
	}
	if s := q.Get("pageSize"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 200 {
			return 0, 0, apiErr("INVALID", "pageSize 应为 1 至 200")
		}
		size = n
	}
	return page, size, nil
}

func (a *App) listReconH(w http.ResponseWriter, r *http.Request, ad *auth.Admin) {
	page, size, ae := pageParams(r)
	if ae != nil {
		writeErr(w, ae)
		return
	}
	q := r.URL.Query()
	out, err := a.listRecon(r.Context(), reconFilter{from: q.Get("from"), to: q.Get("to"), status: q.Get("status"), page: page, pageSize: size, asc: q.Get("sortDir") == "asc"})
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (a *App) getReconH(w http.ResponseWriter, r *http.Request, ad *auth.Admin) {
	out, ae, err := a.getRecon(r.Context(), r.PathValue("dateOrId"))
	if err != nil {
		a.fail(w, err)
		return
	}
	if ae != nil {
		writeErr(w, ae)
		return
	}
	writeJSON(w, 200, out)
}

func (a *App) handleDiffH(w http.ResponseWriter, r *http.Request, ad *auth.Admin) {
	idx, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || idx < 0 {
		writeErr(w, apiErr("INVALID", "差异项序号应为非负整数"))
		return
	}
	var req struct {
		Note string `json:"note"`
	}
	if ae := decode(r, &req); ae != nil {
		writeErr(w, ae)
		return
	}
	rid := r.PathValue("reconId")
	a.apiIdem(w, r, "admin:"+ad.AccountID+" handleDiff "+rid, func() (any, *APIError, error) {
		v, ae, err := a.handleDiff(r.Context(), adminActor(ad), rid, idx, strings.TrimSpace(req.Note))
		if v == nil {
			return nil, ae, err
		}
		return v, ae, err
	})
}

func (a *App) listRefundFailuresH(w http.ResponseWriter, r *http.Request, ad *auth.Admin) {
	out, err := a.refundFailures(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (a *App) transferH(w http.ResponseWriter, r *http.Request, ad *auth.Admin) {
	var req transferReq
	if ae := decode(r, &req); ae != nil {
		writeErr(w, ae)
		return
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if utf8.RuneCountInString(req.Reason) < 4 {
		writeErr(w, apiErr("REASON_REQUIRED", ""))
		return
	}
	id := r.PathValue("paymentId")
	a.apiIdem(w, r, "admin:"+ad.AccountID+" transfer "+id, func() (any, *APIError, error) {
		p, ae, err := a.transfer(r.Context(), adminActor(ad), id, req)
		if p == nil {
			return nil, ae, err
		}
		return p, nil, nil
	})
}

func (a *App) listOrderPaymentsH(w http.ResponseWriter, r *http.Request, ad *auth.Admin) {
	ps, err := a.queryPayments(r.Context(), a.Pool, `WHERE c.order_id = $1 ORDER BY c.created_at, c.seq`, r.PathValue("orderId"))
	if err != nil {
		a.fail(w, err)
		return
	}
	if ps == nil {
		ps = []*Payment{}
	}
	writeJSON(w, 200, ps)
}

func (a *App) listUserPaymentsH(w http.ResponseWriter, r *http.Request, ad *auth.Admin) {
	page, size, ae := pageParams(r)
	if ae != nil {
		writeErr(w, ae)
		return
	}
	q := r.URL.Query()
	where := `WHERE c.user_id = $1`
	args := []any{r.PathValue("userId")}
	for _, f := range []struct{ col, val string }{{"c.type", q.Get("type")}, {"c.status", q.Get("status")}, {"c.account", q.Get("account")}} {
		if f.val != "" {
			args = append(args, f.val)
			where += " AND " + f.col + " = $" + strconv.Itoa(len(args))
		}
	}
	ctx := r.Context()
	var total int
	if err := a.Pool.QueryRow(ctx, `SELECT count(*) FROM payment_current c `+where, args...).Scan(&total); err != nil {
		a.fail(w, err)
		return
	}
	args = append(args, size, (page-1)*size)
	ps, err := a.queryPayments(ctx, a.Pool, where+` ORDER BY c.created_at DESC, c.seq DESC LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		a.fail(w, err)
		return
	}
	if ps == nil {
		ps = []*Payment{}
	}
	writeJSON(w, 200, map[string]any{"rows": ps, "total": total, "page": page, "pageSize": size})
}

// Todo 契约 Todo
type Todo struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Count int    `json:"count"`
	Menu  string `json:"menu"`
}

func (a *App) todosH(w http.ResponseWriter, r *http.Request, ad *auth.Admin) {
	ctx := r.Context()
	out := []Todo{}
	if ad.HasAny("recon.manage") {
		n, err := a.pendingDiffCount(ctx)
		if err != nil {
			a.fail(w, err)
			return
		}
		if n > 0 {
			out = append(out, Todo{Key: "recon", Label: "待处理对账差异", Count: n, Menu: "收单对账"})
		}
	}
	if ad.HasAny("refund.manualTransfer") {
		fs, err := a.refundFailures(ctx)
		if err != nil {
			a.fail(w, err)
			return
		}
		if len(fs) > 0 {
			out = append(out, Todo{Key: "refundFail", Label: "退款失败待转账", Count: len(fs), Menu: "欠费与退款失败"})
		}
	}
	writeJSON(w, 200, out)
}

func (a *App) incomeSummaryH(w http.ResponseWriter, r *http.Request, ad *auth.Admin) {
	var received, refunded, card int64
	err := a.Pool.QueryRow(r.Context(), `SELECT
		coalesce(sum(amount) FILTER (WHERE type IN ('订单扣款','订单支付','欠费补付')),0),
		coalesce(sum(amount) FILTER (WHERE type IN ('退款','人工转账退款')),0),
		coalesce(sum(amount) FILTER (WHERE type = '月卡购买'),0)
		FROM payment_current WHERE account = '骑行收入户' AND status = '成功'`).Scan(&received, &refunded, &card)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"rideIncomeNet": received + card - refunded, "received": received, "refunded": refunded,
		"monthCardIncome": card, "asOf": fmtTime(a.Now())})
}

// ---------- 内部命令 ----------

func validCollect(ch string) bool { return ch == channel.WeChat || ch == channel.Alipay }

func (a *App) writeCmd(w http.ResponseWriter, res CommandResult, err error) {
	switch {
	case errors.Is(err, errNoCollection):
		writeErr(w, apiErr("NOT_FOUND", "该订单没有可退的收款"))
	case errors.Is(err, errAlreadyRefunded):
		writeErr(w, apiErr("CONFLICT", "该订单已全额退款，不能再退"))
	case errors.Is(err, errOriginalNotFound):
		writeErr(w, apiErr("NOT_FOUND", "原押金收款流水不存在"))
	case errors.Is(err, errPaymentNotFound):
		writeErr(w, apiErr("NOT_FOUND", "没有找到该流水"))
	case err != nil:
		a.fail(w, err)
	default:
		writeJSON(w, 200, res)
	}
}

func (a *App) creditChargeH(w http.ResponseWriter, r *http.Request, scope, key string) {
	var c struct {
		BizNo, UserID, OrderID, Channel string
		Amount                          int64
	}
	if ae := decode(r, &c); ae != nil {
		writeErr(w, ae)
		return
	}
	if c.BizNo == "" || c.UserID == "" || c.OrderID == "" || c.Amount <= 0 || !validCollect(c.Channel) {
		writeErr(w, apiErr("INVALID", "bizNo、userId、orderId、amount（大于 0）、channel（微信支付｜支付宝）必填"))
		return
	}
	res, err := a.runCollect(r.Context(), scope, key, collectSpec{merchant: channel.IncomeMerchant, np: newPayment{
		BizNo: c.BizNo, Type: "订单扣款", Account: "骑行收入户", Direction: "收", Channel: c.Channel, Credit: true,
		UserID: &c.UserID, OrderID: &c.OrderID, Amount: c.Amount}})
	a.writeCmd(w, res, err)
}

func (a *App) oneTapH(w http.ResponseWriter, r *http.Request, scope, key string) {
	var c struct {
		BizNo, Type, UserID, OrderID, CardID, Channel string
		PayerOpenID                                   string `json:"payerOpenId"`
		Amount                                        int64
	}
	if ae := decode(r, &c); ae != nil {
		writeErr(w, ae)
		return
	}
	okType := c.Type == "订单支付" || c.Type == "欠费补付" || c.Type == "月卡购买"
	if c.BizNo == "" || c.UserID == "" || c.Amount <= 0 || !okType || !validCollect(c.Channel) {
		writeErr(w, apiErr("INVALID", "bizNo、type、userId、amount（大于 0）、channel 必填且取值合法"))
		return
	}
	if (c.Type != "月卡购买" && c.OrderID == "") || (c.Type == "月卡购买" && c.CardID == "") {
		writeErr(w, apiErr("INVALID", "订单支付、欠费补付须带 orderId；月卡购买须带 cardId"))
		return
	}
	res, err := a.runCollect(r.Context(), scope, key, collectSpec{merchant: channel.IncomeMerchant, intent: true, openID: c.PayerOpenID, np: newPayment{
		BizNo: c.BizNo, Type: c.Type, Account: "骑行收入户", Direction: "收", Channel: c.Channel,
		UserID: &c.UserID, OrderID: strPtr(c.OrderID), CardID: strPtr(c.CardID), Amount: c.Amount}})
	a.writeCmd(w, res, err)
}

func (a *App) refundH(w http.ResponseWriter, r *http.Request, scope, key string) {
	var c RefundCmd
	if ae := decode(r, &c); ae != nil {
		writeErr(w, ae)
		return
	}
	if c.UserID == "" || c.OrderID == "" || c.Amount <= 0 {
		writeErr(w, apiErr("INVALID", "userId、orderId、amount（大于 0）必填"))
		return
	}
	res, err := a.runRefund(r.Context(), scope, key, c)
	a.writeCmd(w, res, err)
}

func (a *App) depositChargeH(w http.ResponseWriter, r *http.Request, scope, key string) {
	var c struct {
		BizNo, Type, UserID, Channel string
		Amount                       int64
	}
	if ae := decode(r, &c); ae != nil {
		writeErr(w, ae)
		return
	}
	if c.BizNo == "" || c.UserID == "" || c.Amount <= 0 || (c.Type != "押金缴纳" && c.Type != "押金补缴") || !validCollect(c.Channel) {
		writeErr(w, apiErr("INVALID", "bizNo、type（押金缴纳｜押金补缴）、userId、amount、channel 必填"))
		return
	}
	res, err := a.runCollect(r.Context(), scope, key, collectSpec{merchant: channel.DepositMerchant, np: newPayment{
		BizNo: c.BizNo, Type: c.Type, Account: "押金专户", Direction: "收", Channel: c.Channel, UserID: &c.UserID, Amount: c.Amount}})
	a.writeCmd(w, res, err)
}

func (a *App) depositRefundH(w http.ResponseWriter, r *http.Request, scope, key string) {
	var c DepositRefundCmd
	if ae := decode(r, &c); ae != nil {
		writeErr(w, ae)
		return
	}
	if c.BizNo == "" || c.UserID == "" || c.Amount <= 0 || len(c.OriginalPaymentIDs) == 0 {
		writeErr(w, apiErr("INVALID", "bizNo、userId、amount（大于 0）、originalPaymentIds 必填"))
		return
	}
	res, err := a.runDepositRefund(r.Context(), scope, key, c)
	a.writeCmd(w, res, err)
}

func (a *App) depositTransferH(w http.ResponseWriter, r *http.Request, scope, key string) {
	var c DepositTransferCmd
	if ae := decode(r, &c); ae != nil {
		writeErr(w, ae)
		return
	}
	if c.BizNo == "" || c.UserID == "" || c.OrderID == "" || c.Amount <= 0 {
		writeErr(w, apiErr("INVALID", "bizNo、userId、orderId、amount（大于 0）必填"))
		return
	}
	res, err := a.runDepositTransfer(r.Context(), scope, key, c)
	a.writeCmd(w, res, err)
}

func (a *App) confirmH(w http.ResponseWriter, r *http.Request, scope, key string) {
	var c ConfirmCmd
	if ae := decode(r, &c); ae != nil {
		writeErr(w, ae)
		return
	}
	if c.Result != "已收款" && c.Result != "未收款" {
		writeErr(w, apiErr("INVALID", "result 只能是 已收款 或 未收款"))
		return
	}
	if c.Actor == nil || c.Actor.ID == "" || c.Actor.Kind == "" {
		writeErr(w, apiErr("INVALID", "缺少操作人 actor"))
		return
	}
	c.Reason = strings.TrimSpace(c.Reason)
	if utf8.RuneCountInString(c.Reason) < 4 {
		writeErr(w, apiErr("REASON_REQUIRED", ""))
		return
	}
	res, err := a.runConfirm(r.Context(), scope, key, r.PathValue("paymentId"), c)
	a.writeCmd(w, res, err)
}
