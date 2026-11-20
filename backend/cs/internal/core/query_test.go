package core

import (
	"testing"
)

// trace: 任务列表 11 查询；cs.yaml searchUsersAndOrders
func TestSearch(t *testing.T) {
	e := newEnv(t)
	id := e.open("U100484", "OD2026112714351", "我的订单", "退款")
	e.expect(e.do("GET", "/api/cs/admin/search?q=", staff(), nil, ""), 400, "INVALID")
	e.expect(e.do("GET", "/api/cs/admin/search?q=448", staff(), nil, ""), 400, "INVALID")
	e.expect(e.do("GET", "/api/cs/admin/search?q=13900004484", adminTok("AC0501", "财务", "order.view"), nil, ""), 403, "FORBIDDEN")

	check := func(q, matched string, users, orders int) resp {
		t.Helper()
		r := e.do("GET", "/api/cs/admin/search?q="+q, staff(), nil, "")
		e.expect(r, 200, "")
		if r.body["matchedBy"] != matched || len(r.body["users"].([]any)) != users || len(r.body["orders"].([]any)) != orders {
			t.Fatalf("q=%s：%s", q, r.raw)
		}
		return r
	}
	r := check("13900004484", "手机号", 1, 0)
	u := mustMap(t, r.body["users"].([]any)[0])
	if u["userId"] != "U100484" || u["ticketCount"] != 1.0 || u["openTicketCount"] != 1.0 {
		t.Fatalf("%v", u)
	}
	check("4484", "手机尾号", 1, 0)
	check("U100484", "用户编号", 1, 0)
	check("u100484", "用户编号", 1, 0)
	check("U999999", "用户编号", 0, 0)
	r = check("OD2026112714351", "订单号", 1, 1)
	o := mustMap(t, r.body["orders"].([]any)[0])
	if o["userId"] != "U100484" || len(o["ticketIds"].([]any)) != 1 || o["ticketIds"].([]any)[0] != id {
		t.Fatalf("%v", o)
	}
	check("OD2026010100001", "订单号", 0, 0)
	check("qh0592", "车号", 1, 1)
	check("你好", "无", 0, 0)
}

// trace: cs.yaml listTickets、listTicketsByUser、listTicketsByOrder、getCsTodos
func TestTicketListsAndTodos(t *testing.T) {
	e := newEnv(t)
	t1 := e.open("U100484", "OD2026090110600", "我的订单", "调度费")
	t2 := e.open("U100484", "OD2026112714351", "我的订单", "退款")
	t3 := e.open("U100123", "OD2026120111070", "骑行中页", "还不了车")
	e.expect(e.do("POST", "/api/cs/admin/tickets/"+t1+"/reply", staff(), map[string]any{"text": "核实中"}, ""), 200, "")

	r := e.do("GET", "/api/cs/admin/tickets", staff(), nil, "")
	e.expect(r, 200, "")
	rows := r.body["rows"].([]any)
	if r.body["total"] != 3.0 || len(rows) != 3 || mustMap(t, rows[0])["ticketId"] != t3 || r.body["page"] != 1.0 || r.body["pageSize"] != 20.0 {
		t.Fatalf("%s", r.raw)
	}
	r = e.do("GET", "/api/cs/admin/tickets?status=处理中", staff(), nil, "")
	if r.body["total"] != 1.0 || mustMap(t, r.body["rows"].([]any)[0])["ticketId"] != t1 {
		t.Fatalf("%s", r.raw)
	}
	r = e.do("GET", "/api/cs/admin/tickets?status=处理中&status=待处理", staff(), nil, "")
	if r.body["total"] != 3.0 {
		t.Fatalf("多选状态：%s", r.raw)
	}
	e.expect(e.do("GET", "/api/cs/admin/tickets?status=其他", staff(), nil, ""), 400, "INVALID")
	r = e.do("GET", "/api/cs/admin/tickets?userId=U100484&page=2&pageSize=1", staff(), nil, "")
	if r.body["total"] != 2.0 || len(r.body["rows"].([]any)) != 1 || mustMap(t, r.body["rows"].([]any)[0])["ticketId"] != t1 {
		t.Fatalf("分页：%s", r.raw)
	}
	r = e.do("GET", "/api/cs/admin/tickets?q="+t2, staff(), nil, "")
	if r.body["total"] != 1.0 {
		t.Fatalf("%s", r.raw)
	}
	r = e.do("GET", "/api/cs/admin/tickets?assignee=AC0301", staff(), nil, "")
	if r.body["total"] != 1.0 {
		t.Fatalf("%s", r.raw)
	}
	r = e.do("GET", "/api/cs/admin/tickets?sortDir=asc", staff(), nil, "")
	if mustMap(t, r.body["rows"].([]any)[0])["ticketId"] != t1 {
		t.Fatalf("%s", r.raw)
	}
	// 运营商账号只看到本运营商订单的工单（OD2026090110600、OD2026112714351 属 OP01）
	r = e.do("GET", "/api/cs/admin/tickets", adminTokWith("AC0201", "OP01", true, "order.view"), nil, "")
	if r.body["total"] != 2.0 {
		t.Fatalf("数据范围：%s", r.raw)
	}
	e.expect(e.do("GET", "/api/cs/admin/tickets/"+t3, adminTokWith("AC0201", "OP01", true, "order.view"), nil, ""), 403, "SCOPE_DENIED")

	l := e.do("GET", "/api/cs/admin/users/U100484/tickets", staff(), nil, "")
	if len(l.list) != 2 || mustMap(t, l.list[0])["ticketId"] != t2 || mustMap(t, l.list[0])["refundLimit"] != 2000.0 {
		t.Fatalf("%s", l.raw)
	}
	l = e.do("GET", "/api/cs/admin/orders/OD2026120111070/tickets", staff(), nil, "")
	if len(l.list) != 1 {
		t.Fatalf("%s", l.raw)
	}

	td := e.do("GET", "/api/cs/admin/todos", staff(), nil, "")
	if len(td.list) != 1 || mustMap(t, td.list[0])["key"] != "tickets" || mustMap(t, td.list[0])["count"] != 3.0 || mustMap(t, td.list[0])["label"] != "待处理工单" {
		t.Fatalf("%s", td.raw)
	}
	td = e.do("GET", "/api/cs/admin/todos", adminTok("AC0501", "财务", "settlement.manage"), nil, "")
	if td.status != 200 || len(td.list) != 0 {
		t.Fatalf("没有相关权限点时返回空：%s", td.raw)
	}
}
