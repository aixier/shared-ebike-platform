package fakes

import (
	"net/http"
	"strings"
	"time"
)

// Ride 是 ride 替身。
func (w *World) Ride() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/ride/orders/{orderId}", func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if _, ok := w.guard("ride", rw, r); !ok {
			return
		}
		o, ok := w.Orders[r.PathValue("orderId")]
		if !ok {
			writeErr(rw, 404, "NOT_FOUND", "没有找到该订单", nil)
			return
		}
		writeJSON(rw, 200, o)
	})
	mux.HandleFunc("GET /internal/ride/orders", func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if _, ok := w.guard("ride", rw, r); !ok {
			return
		}
		plate := r.URL.Query().Get("plateNo")
		if plate == "" && r.URL.Query().Get("userId") == "" && r.URL.Query().Get("orderIds") == "" {
			writeErr(rw, 400, "INVALID", "至少传一个条件", nil)
			return
		}
		rows := []*Order{}
		for _, o := range w.Orders {
			if plate != "" && o.PlateNo == plate {
				rows = append(rows, o)
			}
		}
		writeJSON(rw, 200, map[string]any{"rows": rows, "total": len(rows), "page": 1, "pageSize": 20})
	})
	mux.HandleFunc("POST /internal/ride/orders/{orderId}/force-end", func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		body, ok := w.guard("ride", rw, r)
		if !ok {
			return
		}
		o, ok := w.Orders[r.PathValue("orderId")]
		if !ok {
			writeErr(rw, 404, "NOT_FOUND", "没有找到该订单", nil)
			return
		}
		if o.Status != "骑行中" {
			writeErr(rw, 409, "NOT_RIDING", "订单不在骑行中", nil)
			return
		}
		if s, _ := body["reason"].(string); len([]rune(s)) < 4 {
			writeErr(rw, 400, "REASON_REQUIRED", "请写明事由", nil)
			return
		}
		end := time.Now()
		if s, ok := body["billedEndAt"].(string); ok {
			t, err := time.Parse(time.RFC3339, s)
			if err != nil || (o.StartAt != nil && t.Before(*o.StartAt)) {
				writeErr(rw, 400, "INVALID", "计费终点不对", nil)
				return
			}
			end = t
		}
		o.Status = "已结束"
		o.EndAt = &end
		o.EndMethod = str("客服强制结束")
		res := map[string]any{"order": o, "lockRequest": "已受理"}
		w.remember("ride", r, res)
		writeJSON(rw, 200, res)
	})
	return mux
}

func (b *Bill) facts() map[string]any {
	m := map[string]any{
		"orderId": b.OrderID, "userId": b.UserID, "billedMinutes": b.BilledMinutes, "feeLater": false,
		"refundable": b.ChannelPaid - b.Refunded, "fee": nil, "payStatus": nil, "refundStatus": nil, "refundFailedPaymentId": nil,
	}
	if b.HasFee {
		m["fee"] = map[string]any{
			"receivable": b.Receivable, "payable": b.Payable, "channelPaid": b.ChannelPaid, "refunded": b.Refunded,
			"netReceived": b.ChannelPaid - b.Refunded,
		}
	}
	return m
}

// Bill 是 bill 替身。
func (w *World) Bill() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/bill/orders/{orderId}", func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if _, ok := w.guard("bill", rw, r); !ok {
			return
		}
		b, ok := w.Bills[r.PathValue("orderId")]
		if !ok {
			writeErr(rw, 404, "NOT_FOUND", "该订单还没有账单", nil)
			return
		}
		writeJSON(rw, 200, b.facts())
	})
	mux.HandleFunc("POST /internal/bill/refunds", func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		body, ok := w.guard("bill", rw, r)
		if !ok {
			return
		}
		oid, _ := body["orderId"].(string)
		b, ok := w.Bills[oid]
		if !ok {
			writeErr(rw, 404, "NOT_FOUND", "没有找到该订单的账单", nil)
			return
		}
		amt := int64(0)
		if f, ok := body["amount"].(float64); ok {
			amt = int64(f)
		}
		res := map[string]any{"orderId": oid, "amount": amt, "awaitingPayee": false, "failCode": nil, "failReason": nil, "paymentId": nil}
		switch {
		case b.ChannelPaid == 0:
			res["result"], res["failCode"], res["failReason"] = "失败", "NOTHING_TO_REFUND", "该单没有渠道收款"
		case amt > b.ChannelPaid-b.Refunded:
			res["result"], res["failCode"], res["failReason"] = "失败", "EXCEED_REFUNDABLE", "超过可退金额"
		case b.RefundMode == "channel":
			res["result"], res["failCode"], res["failReason"] = "失败", "CHANNEL_FAILED", "原支付账户已注销"
			res["paymentId"], res["awaitingPayee"], res["refundStatus"] = w.nextPaymentID(), true, "原路失败·待人工转账"
		case b.RefundMode == "unknown":
			res["result"], res["paymentId"] = "结果未知", w.nextPaymentID()
		default:
			b.Refunded += amt
			res["result"], res["paymentId"] = "成功", w.nextPaymentID()
		}
		res["refunded"] = b.Refunded
		w.remember("bill", r, res)
		writeJSON(rw, 200, res)
	})
	return mux
}

func (u *User) view() map[string]any {
	return map[string]any{"userId": u.UserID, "phoneMasked": "138****" + u.Phone[len(u.Phone)-4:], "channel": "微信",
		"account": map[string]any{"status": u.AccountStatus, "freeze": nil, "history": []any{}}}
}

// Acct 是 acct 替身。
func (w *World) Acct() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/acct/users/lookup", func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if _, ok := w.guard("acct", rw, r); !ok {
			return
		}
		phone, tail := r.URL.Query().Get("phone"), r.URL.Query().Get("phoneTail")
		if (phone == "") == (tail == "") {
			writeErr(rw, 400, "INVALID", "phone 与 phoneTail 二选一", nil)
			return
		}
		users := []map[string]any{}
		for _, u := range w.Users {
			if (phone != "" && u.Phone == phone) || (tail != "" && strings.HasSuffix(u.Phone, tail)) {
				users = append(users, map[string]any{"userId": u.UserID, "phoneMasked": "138****", "accountStatus": u.AccountStatus})
			}
		}
		writeJSON(rw, 200, map[string]any{"users": users})
	})
	mux.HandleFunc("GET /internal/acct/users/{userId}", func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if _, ok := w.guard("acct", rw, r); !ok {
			return
		}
		u, ok := w.Users[r.PathValue("userId")]
		if !ok {
			writeErr(rw, 404, "NOT_FOUND", "没有找到该用户", nil)
			return
		}
		writeJSON(rw, 200, u.view())
	})
	mux.HandleFunc("POST /internal/acct/users/{userId}/unfreeze", func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if _, ok := w.guard("acct", rw, r); !ok {
			return
		}
		u, ok := w.Users[r.PathValue("userId")]
		if !ok {
			writeErr(rw, 404, "NOT_FOUND", "没有找到该用户", nil)
			return
		}
		if u.AccountStatus != "冻结" {
			writeErr(rw, 409, "CONFLICT", "该账户未冻结", nil)
			return
		}
		u.AccountStatus = "正常"
		v := u.view()
		w.remember("acct", r, v)
		writeJSON(rw, 200, v)
	})
	mux.HandleFunc("POST /internal/acct/users/{userId}/cancel", func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if _, ok := w.guard("acct", rw, r); !ok {
			return
		}
		u, ok := w.Users[r.PathValue("userId")]
		if !ok {
			writeErr(rw, 404, "NOT_FOUND", "没有找到该用户", nil)
			return
		}
		if u.AccountStatus == "已注销" {
			writeErr(rw, 409, "ALREADY_DONE", "该账户已注销", nil)
			return
		}
		if len(u.CancelBlockers) > 0 {
			writeErr(rw, 409, "CANCEL_BLOCKED", "还有待处理的事项", map[string]any{"blockers": u.CancelBlockers})
			return
		}
		u.AccountStatus = "已注销"
		v := map[string]any{"ok": true, "userId": u.UserID, "cancelledAt": time.Now().Format(time.RFC3339)}
		w.remember("acct", r, v)
		writeJSON(rw, 200, v)
	})
	return mux
}

// Deposit 是 deposit 替身。
func (w *World) Deposit() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/deposit/assist-refunds", func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		body, ok := w.guard("deposit", rw, r)
		if !ok {
			return
		}
		uid, _ := body["userId"].(string)
		u, ok := w.Users[uid]
		if !ok {
			writeErr(rw, 404, "NOT_FOUND", "没有找到该用户", nil)
			return
		}
		var res map[string]any
		switch u.Deposit {
		case "ok":
			now := time.Now()
			res = map[string]any{"result": "成功", "userId": uid, "failCode": nil, "failReason": nil, "blockers": []any{},
				"refund": map[string]any{"appliedAt": now.Format(time.RFC3339), "stoppedAt": nil, "expectedAt": now.Add(5 * 24 * time.Hour).Format(time.RFC3339), "amount": u.DepositAmount, "paymentId": nil, "awaitingPayee": false},
				"text":   "已受理退押，预计 5 个工作日内原路退回"}
			u.Deposit = "refunding"
		case "blocked":
			res = map[string]any{"result": "失败", "userId": uid, "failCode": "REFUND_BLOCKED", "failReason": "暂不能退押：有一单正在骑行，还车后再处理",
				"blockers": []any{map[string]any{"code": "HAS_RIDING", "message": "有一单正在骑行"}}, "refund": nil, "text": "暂不能退押"}
		default:
			res = map[string]any{"result": "失败", "userId": uid, "failCode": "NOT_DEPOSIT_USER", "failReason": "该用户没有可退的押金", "blockers": []any{}, "refund": nil, "text": "没有可退的押金"}
		}
		w.remember("deposit", r, res)
		writeJSON(rw, 200, res)
	})
	return mux
}
