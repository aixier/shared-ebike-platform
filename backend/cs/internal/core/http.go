package core

import (
	"net/http"
	"strconv"

	"bike/cs/internal/auth"
	"bike/cs/internal/httpx"
)

// Handler 返回本服务的全部 HTTP 路由（contracts/openapi/cs.yaml）。
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, 200, map[string]bool{"ok": true})
	})

	// 用户端
	mux.HandleFunc("GET /api/cs/tickets", a.user(func(w http.ResponseWriter, r *http.Request, uid string) {
		ts, err := a.ListUserTickets(r.Context(), uid)
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		out := make([]UserTicket, 0, len(ts))
		for _, t := range ts {
			out = append(out, t.User())
		}
		httpx.WriteJSON(w, 200, out)
	}))
	mux.HandleFunc("POST /api/cs/tickets", a.user(func(w http.ResponseWriter, r *http.Request, uid string) {
		a.runIdem(w, r, "user:"+uid, func() (int, any, error) {
			var req OpenTicketRequest
			if err := httpx.DecodeJSON(r, &req); err != nil {
				return 0, nil, err
			}
			t, err := a.OpenTicket(r.Context(), uid, req)
			if err != nil {
				return 0, nil, err
			}
			return 201, t.User(), nil
		})
	}))
	mux.HandleFunc("GET /api/cs/tickets/{ticketId}", a.user(func(w http.ResponseWriter, r *http.Request, uid string) {
		t, err := a.GetUserTicket(r.Context(), uid, r.PathValue("ticketId"))
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		httpx.WriteJSON(w, 200, t.User())
	}))
	mux.HandleFunc("POST /api/cs/tickets/{ticketId}/messages", a.user(func(w http.ResponseWriter, r *http.Request, uid string) {
		a.runIdem(w, r, "user:"+uid, func() (int, any, error) {
			var req struct {
				Text string `json:"text"`
			}
			if err := httpx.DecodeJSON(r, &req); err != nil {
				return 0, nil, err
			}
			t, err := a.SendUserMessage(r.Context(), uid, r.PathValue("ticketId"), req.Text)
			if err != nil {
				return 0, nil, err
			}
			return 200, t.User(), nil
		})
	}))
	mux.HandleFunc("GET /api/cs/orders/{orderId}/tickets", a.user(func(w http.ResponseWriter, r *http.Request, uid string) {
		ts, err := a.ListUserOrderTickets(r.Context(), uid, r.PathValue("orderId"))
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		out := make([]TicketBrief, 0, len(ts))
		for _, t := range ts {
			out = append(out, t.Brief())
		}
		httpx.WriteJSON(w, 200, out)
	}))

	// 管理端
	mux.HandleFunc("GET /api/cs/admin/search", a.admin([]string{"user.view"}, false, func(w http.ResponseWriter, r *http.Request, ad *auth.Admin) {
		res, err := a.Search(r.Context(), ad, r.URL.Query().Get("q"))
		writeResult(w, 200, res, err)
	}))
	mux.HandleFunc("GET /api/cs/admin/tickets", a.admin([]string{"order.view"}, false, func(w http.ResponseWriter, r *http.Request, ad *auth.Admin) {
		q := r.URL.Query()
		page, _ := strconv.Atoi(q.Get("page"))
		size, _ := strconv.Atoi(q.Get("pageSize"))
		res, err := a.ListTickets(r.Context(), ad, TicketFilter{Status: q["status"], Category: q.Get("category"), UserID: q.Get("userId"),
			OrderID: q.Get("orderId"), Assignee: q.Get("assignee"), Q: q.Get("q"), Page: page, PageSize: size, SortDir: q.Get("sortDir")})
		writeResult(w, 200, res, err)
	}))
	mux.HandleFunc("GET /api/cs/admin/tickets/{ticketId}", a.admin([]string{"order.view"}, false, func(w http.ResponseWriter, r *http.Request, ad *auth.Admin) {
		t, err := a.GetTicket(r.Context(), ad, r.PathValue("ticketId"))
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		a.writeTicket(w, r, 200, t)
	}))
	mux.HandleFunc("POST /api/cs/admin/tickets/{ticketId}/reply", a.adminWrite([]string{"order.view"}, func(r *http.Request, ad *auth.Admin) (int, any, error) {
		var req struct {
			Text string `json:"text"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			return 0, nil, err
		}
		t, err := a.Reply(r.Context(), ad, r.PathValue("ticketId"), req.Text)
		return a.ticketResult(r, t, err)
	}))
	mux.HandleFunc("POST /api/cs/admin/tickets/{ticketId}/close", a.adminWrite([]string{"order.view"}, func(r *http.Request, ad *auth.Admin) (int, any, error) {
		var req struct {
			Reason string `json:"reason"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			return 0, nil, err
		}
		t, err := a.Close(r.Context(), ad, r.PathValue("ticketId"), req.Reason)
		return a.ticketResult(r, t, err)
	}))
	mux.HandleFunc("POST /api/cs/admin/tickets/{ticketId}/refund/confirm", a.adminWrite([]string{"refund.confirm"}, func(r *http.Request, ad *auth.Admin) (int, any, error) {
		var req struct {
			Amount *int64 `json:"amount"`
			Reason string `json:"reason"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			return 0, nil, err
		}
		pending, res, t, err := a.ConfirmRefund(r.Context(), ad, r.PathValue("ticketId"), req.Amount, req.Reason)
		if err != nil {
			return 0, nil, err
		}
		limit, err := a.refundLimit(r.Context(), a.DB)
		if err != nil {
			return 0, nil, err
		}
		return 200, map[string]any{"pendingSupervisor": pending, "refund": res, "ticket": t.Admin(limit)}, nil
	}))
	mux.HandleFunc("POST /api/cs/admin/tickets/{ticketId}/refund/approve-over-limit", a.adminWrite([]string{"refund.confirm.overLimit"}, func(r *http.Request, ad *auth.Admin) (int, any, error) {
		var req struct {
			Reason string `json:"reason"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			return 0, nil, err
		}
		res, t, err := a.ApproveOverLimit(r.Context(), ad, r.PathValue("ticketId"), req.Reason)
		if err != nil {
			return 0, nil, err
		}
		limit, err := a.refundLimit(r.Context(), a.DB)
		if err != nil {
			return 0, nil, err
		}
		return 200, map[string]any{"refund": res, "ticket": t.Admin(limit)}, nil
	}))
	mux.HandleFunc("POST /api/cs/admin/tickets/{ticketId}/refund/reject", a.adminWrite([]string{"refund.confirm"}, func(r *http.Request, ad *auth.Admin) (int, any, error) {
		var req struct {
			Reason string `json:"reason"`
		}
		if err := httpx.DecodeJSON(r, &req); err != nil {
			return 0, nil, err
		}
		t, err := a.RejectRefund(r.Context(), ad, r.PathValue("ticketId"), req.Reason)
		return a.ticketResult(r, t, err)
	}))
	mux.HandleFunc("POST /api/cs/admin/orders/{orderId}/force-end", a.adminWrite([]string{"order.forceEnd"}, func(r *http.Request, ad *auth.Admin) (int, any, error) {
		var req ForceEndRequest
		if err := httpx.DecodeJSON(r, &req); err != nil {
			return 0, nil, err
		}
		res, err := a.ForceEnd(r.Context(), ad, r.PathValue("orderId"), req)
		if err != nil {
			return 0, nil, err
		}
		if res.ticket != nil {
			limit, err := a.refundLimit(r.Context(), a.DB)
			if err != nil {
				return 0, nil, err
			}
			v := res.ticket.Admin(limit)
			res.Ticket = &v
		}
		return 200, res, nil
	}))
	userAction := func(perm string, fn func(r *http.Request, ad *auth.Admin, uid string, req UserActionRequest, key string) (any, error)) http.HandlerFunc {
		return a.adminWrite([]string{perm}, func(r *http.Request, ad *auth.Admin) (int, any, error) {
			var req UserActionRequest
			if err := httpx.DecodeJSON(r, &req); err != nil {
				return 0, nil, err
			}
			res, err := fn(r, ad, r.PathValue("userId"), req, r.Header.Get("Idempotency-Key"))
			return 200, res, err
		})
	}
	mux.HandleFunc("POST /api/cs/admin/users/{userId}/unfreeze", userAction("account.unfreeze", func(r *http.Request, ad *auth.Admin, uid string, req UserActionRequest, key string) (any, error) {
		return a.Unfreeze(r.Context(), ad, uid, req, key)
	}))
	mux.HandleFunc("POST /api/cs/admin/users/{userId}/assist-refund-deposit", userAction("deposit.assist", func(r *http.Request, ad *auth.Admin, uid string, req UserActionRequest, key string) (any, error) {
		return a.AssistRefundDeposit(r.Context(), ad, uid, req, key)
	}))
	mux.HandleFunc("POST /api/cs/admin/users/{userId}/assist-cancel", userAction("account.cancel.assist", func(r *http.Request, ad *auth.Admin, uid string, req UserActionRequest, key string) (any, error) {
		return a.AssistCancel(r.Context(), ad, uid, req, key)
	}))
	mux.HandleFunc("GET /api/cs/admin/users/{userId}/tickets", a.admin([]string{"user.view"}, false, func(w http.ResponseWriter, r *http.Request, ad *auth.Admin) {
		res, err := a.ListTicketsBy(r.Context(), ad, "user_id", r.PathValue("userId"))
		writeResult(w, 200, res, err)
	}))
	mux.HandleFunc("GET /api/cs/admin/orders/{orderId}/tickets", a.admin([]string{"order.view", "settlement.manage", "ops.monitor", "degrade.closure.view", "arrears.view"}, false, func(w http.ResponseWriter, r *http.Request, ad *auth.Admin) {
		res, err := a.ListTicketsBy(r.Context(), ad, "order_id", r.PathValue("orderId"))
		writeResult(w, 200, res, err)
	}))
	mux.HandleFunc("GET /api/cs/admin/todos", a.admin(nil, false, func(w http.ResponseWriter, r *http.Request, ad *auth.Admin) {
		res, err := a.Todos(r.Context(), ad)
		writeResult(w, 200, res, err)
	}))
	return mux
}

func writeResult(w http.ResponseWriter, status int, v any, err error) {
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, status, v)
}

func (a *App) writeTicket(w http.ResponseWriter, r *http.Request, status int, t *Ticket) {
	limit, err := a.refundLimit(r.Context(), a.DB)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	httpx.WriteJSON(w, status, t.Admin(limit))
}

func (a *App) ticketResult(r *http.Request, t *Ticket, err error) (int, any, error) {
	if err != nil {
		return 0, nil, err
	}
	limit, err := a.refundLimit(r.Context(), a.DB)
	if err != nil {
		return 0, nil, err
	}
	return 200, t.Admin(limit), nil
}

// user 校验用户令牌。
func (a *App) user(fn func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, err := a.UserKeys.VerifyUser(auth.Bearer(r))
		if err != nil {
			httpx.WriteError(w, httpx.E("NOT_LOGGED_IN", "请先登录"))
			return
		}
		fn(w, r, uid)
	}
}

// admin 校验管理令牌与权限点（列出多个时有其一即可；为空表示登录即可）。
func (a *App) admin(perms []string, write bool, fn func(http.ResponseWriter, *http.Request, *auth.Admin)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ad, err := a.AdminKeys.VerifyAdmin(auth.Bearer(r))
		if err != nil {
			httpx.WriteError(w, httpx.E("ADMIN_NOT_LOGGED_IN", "请先登录管理端"))
			return
		}
		if len(perms) > 0 && !ad.HasAny(perms...) {
			httpx.WriteError(w, httpx.E("FORBIDDEN", "你没有这项操作的权限"))
			return
		}
		if write && ad.ReadOnly {
			httpx.WriteError(w, httpx.E("READONLY", "当前账号为只读，不能操作"))
			return
		}
		if write && ad.Scope != "" {
			// 运营商退出（fleet.operator.exited）后，不等令牌刷新，其账号的写操作一律拒绝
			var exited bool
			if err := a.DB.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM exited_operators WHERE operator_id = $1)`, ad.Scope).Scan(&exited); err != nil {
				httpx.WriteError(w, err)
				return
			}
			if exited {
				httpx.WriteError(w, httpx.E("READONLY", "该运营商已退出，账号为只读，不能操作"))
				return
			}
		}
		fn(w, r, ad)
	}
}

// adminWrite 是带幂等的管理端写操作。
func (a *App) adminWrite(perms []string, fn func(*http.Request, *auth.Admin) (int, any, error)) http.HandlerFunc {
	return a.admin(perms, true, func(w http.ResponseWriter, r *http.Request, ad *auth.Admin) {
		a.runIdem(w, r, "admin:"+ad.AccountID, func() (int, any, error) { return fn(r, ad) })
	})
}

// internal 校验服务凭证（本服务目前没有被其他模块调用的接口，留作 /internal 路由的统一入口）。
func (a *App) internal(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := a.Callers.Check(r); !ok {
			httpx.WriteJSON(w, 401, map[string]any{"code": "UNAUTHORIZED", "message": "服务凭证不对"})
			return
		}
		fn(w, r)
	}
}
