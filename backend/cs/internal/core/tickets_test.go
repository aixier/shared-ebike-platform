package core

import (
	"context"
	"regexp"
	"testing"
)

// trace: 任务列表 5 工单与对话；cs.yaml openTicket、getMyTicket、sendTicketMessage、listMyOrderTickets；SCHEMA.md 5.9
func TestOpenTicketWithOrderSnapshotsFacts(t *testing.T) {
	e := newEnv(t)
	r := e.do("POST", "/api/cs/tickets", userTok("U100484"), map[string]any{
		"orderId": "OD2026090110600", "entry": "我的订单", "text": "我明明停在站点里，为什么还收了调度费？", "category": "调度费异议",
	}, "")
	e.expect(r, 201, "")
	id := r.body["ticketId"].(string)
	if !regexp.MustCompile(`^TK20261201\d{4}$`).MatchString(id) {
		t.Fatalf("工单号格式不对：%s", id)
	}
	if r.body["status"] != "待处理" || r.body["category"] != "调度费异议" || r.body["entry"] != "我的订单" || r.body["awaitingPayee"] != false {
		t.Fatalf("%s", r.raw)
	}
	msgs := r.body["messages"].([]any)
	if len(msgs) != 1 || mustMap(t, msgs[0])["from"] != "用户" {
		t.Fatalf("智能体未接入时不应自动回复：%v", msgs)
	}
	if _, ok := r.body["userId"]; ok {
		t.Fatal("用户视图不应含 userId 等客服字段")
	}
	// 客服看到的快照与订单一致（原型 TK202609010031 的 facts，金额换算为分）
	a := e.do("GET", "/api/cs/admin/tickets/"+id, staff(), nil, "")
	e.expect(a, 200, "")
	f := mustMap(t, a.body["facts"])
	want := map[string]any{"orderId": "OD2026090110600", "startAt": "2026-09-01T06:38:42+08:00", "endAt": "2026-09-01T06:57:50+08:00",
		"billedMinutes": 20.0, "distanceKm": 3.3, "receivable": 1130.0, "payable": 930.0, "netReceived": 930.0, "inStation": false, "inStationBasis": "站外"}
	for k, v := range want {
		if f[k] != v {
			t.Errorf("facts.%s 想要 %v，得到 %v", k, v, f[k])
		}
	}
	if a.body["refundLimit"] != 2000.0 || a.body["assignee"] != nil {
		t.Fatalf("%s", a.raw)
	}
	evs := e.outbox("cs.ticket.changed")
	if len(evs) != 1 || payload(evs[0])["change"] != "新建" || payload(evs[0])["ticketId"] != id || evs[0]["key"] != id || mustMap(t, evs[0]["actor"])["kind"] != "用户" {
		t.Fatalf("工单变更事件不对：%v", evs)
	}
}

func TestOpenTicketRidingOrderHasNullAmounts(t *testing.T) {
	e := newEnv(t)
	id := e.open("U100123", "OD2026120111070", "骑行中页", "早上 8 点就把车锁了，现在页面还在计时")
	a := e.do("GET", "/api/cs/admin/tickets/"+id, staff(), nil, "")
	f := mustMap(t, a.body["facts"])
	if f["endAt"] != nil || f["receivable"] != nil || f["billedMinutes"] != nil || f["startAt"] != "2026-12-01T07:41:18+08:00" {
		t.Fatalf("骑行中订单的快照不对：%v", f)
	}
	if a.body["category"] != "费用咨询" {
		t.Fatalf("未预选类别时默认费用咨询，得到 %v", a.body["category"])
	}
}

func TestOpenTicketValidation(t *testing.T) {
	e := newEnv(t)
	u := userTok("U100004")
	e.expect(e.do("POST", "/api/cs/tickets", u, map[string]any{"entry": "我的", "text": "  "}, ""), 400, "INVALID")
	e.expect(e.do("POST", "/api/cs/tickets", u, map[string]any{"entry": "首页", "text": "你好"}, ""), 400, "INVALID")
	e.expect(e.do("POST", "/api/cs/tickets", u, map[string]any{"entry": "我的", "text": "你好", "category": "投诉"}, ""), 400, "INVALID")
	// 别人的订单与不存在的订单都按找不到
	e.expect(e.do("POST", "/api/cs/tickets", u, map[string]any{"entry": "我的订单", "text": "你好", "orderId": "OD2026090110600"}, ""), 404, "NOT_FOUND")
	e.expect(e.do("POST", "/api/cs/tickets", u, map[string]any{"entry": "我的订单", "text": "你好", "orderId": "OD2026010100001"}, ""), 404, "NOT_FOUND")
	// 不带订单号
	r := e.do("POST", "/api/cs/tickets", u, map[string]any{"entry": "押金", "text": "旧押金怎么退"}, "")
	e.expect(r, 201, "")
	if r.body["orderId"] != nil {
		t.Fatal("orderId 应为 null")
	}
	a := e.do("GET", "/api/cs/admin/tickets/"+r.body["ticketId"].(string), staff(), nil, "")
	if a.body["facts"] != nil {
		t.Fatal("没有关联订单时 facts 为 null")
	}
}

func TestUserTicketAccessAndMessages(t *testing.T) {
	e := newEnv(t)
	id := e.open("U100484", "OD2026090110600", "我的订单", "调度费不对")
	e.expect(e.do("GET", "/api/cs/tickets/"+id, userTok("U100004"), nil, ""), 404, "NOT_FOUND")
	e.expect(e.do("POST", "/api/cs/tickets/"+id+"/messages", userTok("U100004"), map[string]any{"text": "x"}, ""), 404, "NOT_FOUND")
	e.expect(e.do("POST", "/api/cs/tickets/"+id+"/messages", userTok("U100484"), map[string]any{"text": ""}, ""), 400, "INVALID")
	r := e.do("POST", "/api/cs/tickets/"+id+"/messages", userTok("U100484"), map[string]any{"text": "还在吗"}, "")
	e.expect(r, 200, "")
	if len(r.body["messages"].([]any)) != 2 || r.body["status"] != "待处理" {
		t.Fatalf("%s", r.raw)
	}
	// 结案后用户再发消息回到处理中
	e.expect(e.do("POST", "/api/cs/admin/tickets/"+id+"/close", staff(), map[string]any{"reason": "已说明计费规则"}, ""), 200, "")
	r = e.do("POST", "/api/cs/tickets/"+id+"/messages", userTok("U100484"), map[string]any{"text": "还是不明白"}, "")
	if r.body["status"] != "处理中" || r.body["closedAt"] != nil {
		t.Fatalf("已解决的对话收到新消息应回到处理中：%s", r.raw)
	}
	// 列表与订单摘要
	e.open("U100484", "", "我的", "怎么开发票")
	l := e.do("GET", "/api/cs/tickets", userTok("U100484"), nil, "")
	if len(l.list) != 2 {
		t.Fatalf("应有 2 个对话：%s", l.raw)
	}
	b := e.do("GET", "/api/cs/orders/OD2026090110600/tickets", userTok("U100484"), nil, "")
	if len(b.list) != 1 || mustMap(t, b.list[0])["ticketId"] != id {
		t.Fatalf("%s", b.raw)
	}
	b = e.do("GET", "/api/cs/orders/OD2026090110600/tickets", userTok("U100004"), nil, "")
	if b.status != 200 || len(b.list) != 0 {
		t.Fatalf("别人的订单返回空列表：%s", b.raw)
	}
}

func TestReplyTakesOverAndClose(t *testing.T) {
	e := newEnv(t)
	id := e.open("U100484", "OD2026090110600", "我的订单", "调度费不对")
	e.expect(e.do("POST", "/api/cs/admin/tickets/"+id+"/reply", staff(), map[string]any{"text": " "}, ""), 400, "INVALID")
	r := e.do("POST", "/api/cs/admin/tickets/"+id+"/reply", staff(), map[string]any{"text": "正在核实"}, "")
	e.expect(r, 200, "")
	if r.body["status"] != "处理中" || r.body["assignee"] != "AC0301" || r.body["assigneeName"] != "客服甲" {
		t.Fatalf("%s", r.raw)
	}
	// 第二个客服回复不改接手人
	r = e.do("POST", "/api/cs/admin/tickets/"+id+"/reply", supervisor(), map[string]any{"text": "补充"}, "")
	if r.body["assignee"] != "AC0301" {
		t.Fatalf("接手人不应变：%v", r.body["assignee"])
	}
	e.expect(e.do("POST", "/api/cs/admin/tickets/"+id+"/close", staff(), map[string]any{"reason": "好"}, ""), 400, "REASON_REQUIRED")
	r = e.do("POST", "/api/cs/admin/tickets/"+id+"/close", staff(), map[string]any{"reason": "已解释站外调度费规则"}, "")
	e.expect(r, 200, "")
	msgs := r.body["messages"].([]any)
	last := mustMap(t, msgs[len(msgs)-1])
	if r.body["status"] != "已解决" || r.body["closedAt"] == nil || last["from"] != "客服" || last["text"] != "已解释站外调度费规则" {
		t.Fatalf("%s", r.raw)
	}
	e.expect(e.do("POST", "/api/cs/admin/tickets/"+id+"/close", staff(), map[string]any{"reason": "再结一次案"}, ""), 409, "ALREADY_DONE")
	e.expect(e.do("POST", "/api/cs/admin/tickets/TK202601019999/close", staff(), map[string]any{"reason": "不存在的工单"}, ""), 404, "NOT_FOUND")
	var changes []string
	for _, ev := range e.outbox("cs.ticket.changed") {
		changes = append(changes, payload(ev)["change"].(string))
	}
	if len(changes) != 4 || changes[1] != "客服回复" || changes[3] != "结案" {
		t.Fatalf("事件序列不对：%v", changes)
	}
}

func TestTicketIDDailySequence(t *testing.T) {
	e := newEnv(t)
	a := e.open("U100004", "", "我的", "一")
	b := e.open("U100004", "", "我的", "二")
	if a != "TK202612010001" || b != "TK202612010002" {
		t.Fatalf("日序号不对：%s %s", a, b)
	}
	var n int
	_ = pool.QueryRow(context.Background(), `SELECT n FROM ticket_day_seq WHERE day = '20261201'`).Scan(&n)
	if n != 2 {
		t.Fatal(n)
	}
}
