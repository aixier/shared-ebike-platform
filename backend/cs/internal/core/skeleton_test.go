package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// trace: 任务列表 4 工程骨架
func TestHealthz(t *testing.T) {
	e := newEnv(t)
	r := e.do("GET", "/healthz", "", nil, "")
	if r.status != 200 || r.body["ok"] != true {
		t.Fatalf("%d %s", r.status, r.raw)
	}
}

func TestAuthRequired(t *testing.T) {
	e := newEnv(t)
	e.expect(e.do("GET", "/api/cs/tickets", "", nil, ""), 401, "NOT_LOGGED_IN")
	e.expect(e.do("GET", "/api/cs/tickets", staff(), nil, ""), 401, "NOT_LOGGED_IN") // 管理令牌不能当用户令牌
	e.expect(e.do("GET", "/api/cs/admin/tickets", "", nil, ""), 401, "ADMIN_NOT_LOGGED_IN")
	e.expect(e.do("GET", "/api/cs/admin/tickets", userTok("U100004"), nil, ""), 401, "ADMIN_NOT_LOGGED_IN")
	e.expect(e.do("GET", "/api/cs/admin/tickets", adminTok("AC0501", "财务", "settlement.manage"), nil, ""), 403, "FORBIDDEN")
	// 按订单号查工单：列出的权限点有其一即可
	e.expect(e.do("GET", "/api/cs/admin/orders/OD2026090110600/tickets", adminTok("AC0501", "财务", "settlement.manage"), nil, ""), 200, "")
	// 只读账号不能写
	ro := adminTokWith("AC0303", nil, true, staffPerms...)
	id := e.open("U100484", "OD2026090110600", "我的订单", "调度费不对")
	e.expect(e.do("POST", "/api/cs/admin/tickets/"+id+"/reply", ro, map[string]any{"text": "你好"}, ""), 403, "READONLY")
	e.expect(e.do("GET", "/api/cs/admin/tickets/"+id, ro, nil, ""), 200, "")
}

func TestFactsSnapshotImmutable(t *testing.T) {
	e := newEnv(t)
	id := e.open("U100484", "OD2026090110600", "我的订单", "调度费不对")
	_, err := pool.Exec(context.Background(), `UPDATE tickets SET facts = '{"orderId":"X"}' WHERE ticket_id = $1`, id)
	if err == nil || !strings.Contains(err.Error(), "不可修改") {
		t.Fatalf("快照应不可修改：%v", err)
	}
}

func TestIdempotentOpen(t *testing.T) {
	e := newEnv(t)
	body := map[string]any{"entry": "我的", "text": "怎么开发票"}
	r1 := e.do("POST", "/api/cs/tickets", userTok("U100004"), body, "k-1")
	r2 := e.do("POST", "/api/cs/tickets", userTok("U100004"), body, "k-1")
	e.expect(r1, 201, "")
	e.expect(r2, 201, "")
	if r1.body["ticketId"] != r2.body["ticketId"] {
		t.Fatalf("同一幂等键应返回同一工单：%v %v", r1.body["ticketId"], r2.body["ticketId"])
	}
	// 另一个用户用同一个键是另一件事
	r3 := e.do("POST", "/api/cs/tickets", userTok("U100484"), body, "k-1")
	if r3.body["ticketId"] == r1.body["ticketId"] {
		t.Fatal("不同主体的同名幂等键不应互相回放")
	}
	var n int
	_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM tickets`).Scan(&n)
	if n != 2 {
		t.Fatalf("应只有 2 张工单，得到 %d", n)
	}
	e.expect(e.do("POST", "/api/cs/tickets", userTok("U100004"), body, strings.Repeat("k", 65)), 400, "INVALID")
}

func TestDownstreamBusyIsNotCachedByIdempotency(t *testing.T) {
	e := newEnv(t)
	e.world.Down["ride"] = true
	body := map[string]any{"entry": "我的订单", "text": "调度费", "orderId": "OD2026090110600"}
	e.expect(e.do("POST", "/api/cs/tickets", userTok("U100484"), body, "k-busy"), 500, "SERVER_BUSY")
	e.world.Down["ride"] = false
	e.expect(e.do("POST", "/api/cs/tickets", userTok("U100484"), body, "k-busy"), 201, "")
}

func TestInternalCallerCheck(t *testing.T) {
	e := newEnv(t)
	h := e.app.internal(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	req := httptest.NewRequest("GET", "/internal/cs/x", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != 401 {
		t.Fatalf("没有凭证应 401，得到 %d", rec.Code)
	}
	req.Header.Set("Authorization", "Bearer ops-key")
	req.Header.Set("X-Caller", "ops")
	rec = httptest.NewRecorder()
	h(rec, req)
	if rec.Code != 204 {
		t.Fatalf("凭证正确应放行，得到 %d", rec.Code)
	}
}

// trace: 任务列表 15 留痕
func TestWritesEmitAudit(t *testing.T) {
	e := newEnv(t)
	id := e.open("U100484", "OD2026090110600", "我的订单", "调度费不对")
	e.expect(e.do("POST", "/api/cs/admin/tickets/"+id+"/close", staff(), map[string]any{"reason": "已电话说明清楚"}, ""), 200, "")
	evs := e.outbox("cs.audit.recorded")
	if len(evs) != 1 {
		t.Fatalf("应有 1 条留痕，得到 %d", len(evs))
	}
	ev := evs[0]
	p := payload(ev)
	actor := mustMap(t, ev["actor"])
	if p["action"] != "结案工单" || p["targetType"] != "ticket" || p["targetId"] != id || p["reason"] != "已电话说明清楚" || actor["id"] != "AC0301" || actor["kind"] != "人" || ev["key"] != "ticket:"+id {
		t.Fatalf("留痕不对：%v", ev)
	}
	for _, k := range []string{"before", "after", "summary"} {
		if _, ok := p[k]; !ok {
			t.Fatalf("留痕缺字段 %s", k)
		}
	}
}
