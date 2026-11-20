package core

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// trace: 任务列表 9 强制结束；R-13；原型 TK202612010033（建议按 08:02 计费）
func TestForceEndUsesAgentProposal(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.open("U100123", "OD2026120111070", "骑行中页", "早上 8 点就在路口把车锁了，现在页面还在计时")
	end := time.Date(2026, 12, 1, 8, 2, 0, 0, CST)
	st := "ST0007"
	if _, err := e.app.AgentProposeForceEnd(ctx, id, ForceEndProposalInput{BilledEndAt: end, StationID: &st}); err != nil {
		t.Fatal(err)
	}
	if len(e.world.CallsTo("/internal/ride/orders/OD2026120111070/force-end")) != 0 {
		t.Fatal("没有人确认不得发强制结束命令")
	}
	td := e.do("GET", "/api/cs/admin/todos", staff(), nil, "")
	hasForce := false
	for _, x := range td.list {
		if m := x.(map[string]any); m["key"] == "forceEnd" {
			hasForce = true
		}
	}
	if hasForce {
		t.Fatal("类别为费用咨询的工单不算待强制结束")
	}
	if _, err := e.app.AgentSetCategory(ctx, id, "订单无法结束"); err != nil {
		t.Fatal(err)
	}
	td = e.do("GET", "/api/cs/admin/todos", staff(), nil, "")
	hasForce = false
	for _, x := range td.list {
		if m := x.(map[string]any); m["key"] == "forceEnd" && m["count"] == 1.0 && m["menu"] == "强制结束订单" {
			hasForce = true
		}
	}
	if !hasForce {
		t.Fatalf("应有待强制结束：%s", td.raw)
	}

	path := "/api/cs/admin/orders/OD2026120111070/force-end"
	e.expect(e.do("POST", path, staff(), map[string]any{"reason": "短"}, ""), 400, "REASON_REQUIRED")
	e.expect(e.do("POST", path, adminTok("AC0501", "财务", "order.view"), map[string]any{"reason": "无权限强制结束"}, ""), 403, "FORBIDDEN")
	r := e.do("POST", path, staff(), map[string]any{"reason": "车辆自 08:02 停在站点内未移动，未收到锁车报告"}, "")
	e.expect(r, 200, "")
	fe := mustMap(t, r.body["forceEnd"])
	if r.body["orderId"] != "OD2026120111070" || fe["billedEndAt"] != "2026-12-01T08:02:00+08:00" || fe["by"] != "AC0301" || fe["at"] != "2026-12-01T11:40:00+08:00" {
		t.Fatalf("%s", r.raw)
	}
	tk := mustMap(t, r.body["ticket"])
	acts := tk["actions"].([]any)
	if tk["status"] != "已解决" || tk["ticketId"] != id || mustMap(t, acts[0])["type"] != "强制结束" || tk["forceEnd"] == nil {
		t.Fatalf("%v", tk)
	}
	calls := e.world.CallsTo("/internal/ride/orders/OD2026120111070/force-end")
	if len(calls) != 1 {
		t.Fatalf("%d", len(calls))
	}
	b := calls[0].Body
	if b["billedEndAt"] != "2026-12-01T08:02:00+08:00" || b["stationId"] != "ST0007" || b["ticketId"] != id || b["reason"] == nil || b["actor"].(map[string]any)["id"] != "AC0301" {
		t.Fatalf("强制结束命令不对：%v", b)
	}
	// 已结束的订单
	e.expect(e.do("POST", path, staff(), map[string]any{"reason": "再结束一次试试"}, ""), 409, "NOT_RIDING")
	au := e.outbox("cs.audit.recorded")
	if len(au) != 0 { // 强制结束由 ride 执行并记留痕
		t.Fatalf("%v", au)
	}
}

func TestForceEndManualTimeAndValidation(t *testing.T) {
	e := newEnv(t)
	path := "/api/cs/admin/orders/OD2026120111070/force-end"
	e.expect(e.do("POST", path, staff(), map[string]any{"reason": "早于开锁的计费终点", "billedEndAt": "2026-12-01T07:00:00+08:00"}, ""), 400, "INVALID")
	e.expect(e.do("POST", path, staff(), map[string]any{"reason": "晚于现在的计费终点", "billedEndAt": "2026-12-01T12:00:00+08:00"}, ""), 400, "INVALID")
	e.expect(e.do("POST", "/api/cs/admin/orders/OD2026010100001/force-end", staff(), map[string]any{"reason": "不存在的订单"}, ""), 404, "NOT_FOUND")
	e.expect(e.do("POST", "/api/cs/admin/orders/OD2026090110600/force-end", staff(), map[string]any{"reason": "已结束的订单"}, ""), 409, "NOT_RIDING")
	other := e.open("U100484", "OD2026090110600", "我的订单", "调度费")
	e.expect(e.do("POST", path, staff(), map[string]any{"reason": "工单不是这一单", "ticketId": other}, ""), 400, "INVALID")
	// 运营商账号只能动本运营商的订单（该单属于 OP02）
	e.expect(e.do("POST", path, adminTokWith("AC0201", "OP01", false, "order.forceEnd"), map[string]any{"reason": "越界强制结束"}, ""), 403, "SCOPE_DENIED")
	// 没有工单、客服手工给计费终点、stationId 传 null 按车辆当前位置
	r := e.do("POST", path, staff(), map[string]any{"reason": "用户电话反映无法还车", "billedEndAt": "2026-12-01T08:30:00+08:00", "stationId": nil}, "")
	e.expect(r, 200, "")
	if r.body["ticket"] != nil || mustMap(t, r.body["forceEnd"])["billedEndAt"] != "2026-12-01T08:30:00+08:00" {
		t.Fatalf("%s", r.raw)
	}
	b := e.world.CallsTo("/internal/ride/orders/OD2026120111070/force-end")[0].Body
	if v, ok := b["stationId"]; !ok || v != nil {
		t.Fatalf("stationId 应为 null：%v", b)
	}
}

func TestForceEndDefaultsToNowWithoutProposal(t *testing.T) {
	e := newEnv(t)
	id := e.open("U100123", "OD2026120111070", "骑行中页", "还不了车")
	r := e.do("POST", "/api/cs/admin/orders/OD2026120111070/force-end", staff(), map[string]any{"reason": "用户无法还车按当前时刻结束"}, "")
	e.expect(r, 200, "")
	if mustMap(t, r.body["forceEnd"])["billedEndAt"] != "2026-12-01T11:40:00+08:00" || mustMap(t, r.body["ticket"])["ticketId"] != id {
		t.Fatalf("没有提议取当前时刻，且自动关联未解决的工单：%s", r.raw)
	}
}

func TestAgentProposalValidation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.open("U100484", "OD2026090110600", "我的订单", "调度费")
	bad := []RefundProposalInput{
		{Amount: 300, Kind: "退一半", Basis: "x"},
		{Amount: 300, Kind: "部分退款", Basis: " "},
		{Amount: 100, Kind: "不退款", Basis: "x"},
		{Amount: 0, Kind: "部分退款", Basis: "x"},
		{Amount: 931, Kind: "全额退款", Basis: "超过实收"},
	}
	for _, in := range bad {
		if _, err := e.app.AgentProposeRefund(ctx, id, in); err == nil {
			t.Errorf("应拒绝：%+v", in)
		}
	}
	if _, err := e.app.AgentProposeRefund(ctx, id, RefundProposalInput{Amount: 0, Kind: "不退款", Basis: "车端与桩均判定站外"}); err != nil {
		t.Fatal(err)
	}
	noOrder := e.open("U100004", "", "押金", "旧押金怎么退")
	if _, err := e.app.AgentProposeRefund(ctx, noOrder, RefundProposalInput{Amount: 100, Kind: "部分退款", Basis: "x"}); err == nil {
		t.Fatal("没有订单的工单不能提议退款")
	}
	if _, err := e.app.AgentProposeForceEnd(ctx, id, ForceEndProposalInput{BilledEndAt: time.Date(2026, 9, 1, 6, 0, 0, 0, CST)}); err == nil {
		t.Fatal("计费终点早于开锁应拒绝")
	}
	if _, err := e.app.AgentSetCategory(ctx, id, "投诉"); err == nil {
		t.Fatal("类别须在枚举内")
	}
	if _, err := e.app.AgentReply(ctx, id, " "); err == nil {
		t.Fatal("空回复应拒绝")
	}
}
