package core

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// trace: 第二轮变更单 全体 1（只读与退出）
func TestReadonlyTokenRejectedOnAdminWrites(t *testing.T) {
	e := newEnv(t)
	id := e.open("U100484", "OD2026090110600", "我的订单", "调度费")
	ro := adminTokWith("AC0399", nil, true, staffPerms...)
	roSup := adminTokWith("AC0398", nil, true, append(append([]string{}, staffPerms...), "refund.confirm.overLimit")...)
	cases := []struct {
		path string
		tok  string
		body map[string]any
	}{
		{"/api/cs/admin/tickets/" + id + "/reply", ro, map[string]any{"text": "您好"}},
		{"/api/cs/admin/tickets/" + id + "/close", ro, map[string]any{"reason": "已电话说明"}},
		{"/api/cs/admin/tickets/" + id + "/refund/confirm", ro, map[string]any{"reason": "核实退款"}},
		{"/api/cs/admin/tickets/" + id + "/refund/approve-over-limit", roSup, map[string]any{"reason": "核实退款"}},
		{"/api/cs/admin/tickets/" + id + "/refund/reject", ro, map[string]any{"reason": "不予退款"}},
		{"/api/cs/admin/orders/OD2026120111070/force-end", ro, map[string]any{"reason": "无法正常结束"}},
		{"/api/cs/admin/users/U100712/unfreeze", ro, map[string]any{"reason": "核实后解冻"}},
		{"/api/cs/admin/users/U100004/assist-refund-deposit", ro, map[string]any{"reason": "电话申请退押"}},
		{"/api/cs/admin/users/U100484/assist-cancel", ro, map[string]any{"reason": "用户要求注销"}},
	}
	for _, c := range cases {
		e.expect(e.do("POST", c.path, c.tok, c.body, ""), 403, "READONLY")
	}
	if n := len(e.world.CallsTo("/internal/bill/refunds")) + len(e.world.CallsTo("/internal/acct/users/U100712/unfreeze")); n != 0 {
		t.Fatalf("只读账号不得触发下游命令：%d", n)
	}
	// 只读账号仍可读
	e.expect(e.do("GET", "/api/cs/admin/tickets/"+id, ro, nil, ""), 200, "")
}

func exitedEvent(id, operatorID string) []byte {
	b, _ := json.Marshal(map[string]any{"eventId": id, "type": "fleet.operator.exited", "version": 1, "occurredAt": testNow.Format(time.RFC3339), "key": operatorID,
		"actor":   map[string]any{"kind": "人", "id": "AC0001", "name": "管理员"},
		"payload": map[string]any{"operatorId": operatorID, "brandIds": []string{"BR01"}, "exitedAt": testNow.Format(time.RFC3339), "readonlyUntil": nil, "reason": "线下结果"}})
	return b
}

// trace: 第二轮变更单 全体 1；events/fleet.md fleet.operator.exited
func TestOperatorExitedMakesScopedWritesReadonly(t *testing.T) {
	e := newEnv(t)
	id := e.open("U100484", "OD2026090110600", "我的订单", "调度费") // 订单属 OP01
	op1 := func() string { return adminTokWith("AC0201", "OP01", false, "order.view") }
	if err := e.app.HandleEvent(context.Background(), exitedEvent("fe-1", "OP02")); err != nil {
		t.Fatal(err)
	}
	// 退出的是 OP02，OP01 不受影响
	e.expect(e.do("POST", "/api/cs/admin/tickets/"+id+"/reply", op1(), map[string]any{"text": "您好"}, ""), 200, "")
	if err := e.app.HandleEvent(context.Background(), exitedEvent("fe-2", "OP01")); err != nil {
		t.Fatal(err)
	}
	e.expect(e.do("POST", "/api/cs/admin/tickets/"+id+"/reply", op1(), map[string]any{"text": "再回复"}, ""), 403, "READONLY")
	e.expect(e.do("POST", "/api/cs/admin/tickets/"+id+"/close", op1(), map[string]any{"reason": "结案试试"}, ""), 403, "READONLY")
	// 读保留；平台客服（无 scope）不受影响
	e.expect(e.do("GET", "/api/cs/admin/tickets/"+id, op1(), nil, ""), 200, "")
	e.expect(e.do("POST", "/api/cs/admin/tickets/"+id+"/reply", staff(), map[string]any{"text": "平台客服回复"}, ""), 200, "")
	// 重复送达不报错
	if err := e.app.HandleEvent(context.Background(), exitedEvent("fe-2", "OP01")); err != nil {
		t.Fatal(err)
	}
}

func TestSubscriptionsIncludeFleetExited(t *testing.T) {
	found := false
	for _, s := range Subscriptions["EVT_FLEET"] {
		if s == "fleet.operator.exited" {
			found = true
		}
	}
	if !found {
		t.Fatal("应订阅 fleet.operator.exited")
	}
}

// trace: 第二轮变更单 全体 3（UTF-8）
func TestNonUTF8BodyRejected(t *testing.T) {
	e := newEnv(t)
	gbk := []byte("{\"entry\":\"我的订单\",\"text\":\"\xb5\xf7\xb6\xc8\xb7\xd1\"}") // "调度费" 的 GBK 字节
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/cs/tickets", bytes.NewReader(gbk))
	req.Header.Set("Authorization", "Bearer "+userTok("U100484"))
	req.Header.Set("Content-Type", "application/json")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var b map[string]any
	_ = json.NewDecoder(r.Body).Decode(&b)
	if r.StatusCode != 400 || b["code"] != "INVALID" || !strings.Contains(b["message"].(string), "请求体不是 UTF-8 编码") {
		t.Fatalf("%d %v", r.StatusCode, b)
	}
}

// trace: 第二轮变更单 cs（查看用户信息只记 cs 自己的查看）
func TestSearchRecordsUserView(t *testing.T) {
	e := newEnv(t)
	e.expect(e.do("GET", "/api/cs/admin/search?q=U100484", staff(), nil, ""), 200, "")
	au := e.outbox("cs.audit.recorded")
	if len(au) != 1 {
		t.Fatalf("%v", au)
	}
	p := payload(au[0])
	if p["action"] != "查看用户信息" || p["targetType"] != "user" || p["targetId"] != "U100484" || au[0]["key"] != "user:U100484" || mustMap(t, au[0]["actor"])["id"] != "AC0301" {
		t.Fatalf("%v", au[0])
	}
	// 按订单号查到订单与其用户，同样记；没有命中不记
	e.expect(e.do("GET", "/api/cs/admin/search?q=OD2026113010347", staff(), nil, ""), 200, "")
	e.expect(e.do("GET", "/api/cs/admin/search?q=U999999", staff(), nil, ""), 200, "")
	au = e.outbox("cs.audit.recorded")
	if len(au) != 2 || payload(au[1])["targetId"] != "U101601" {
		t.Fatalf("%v", au)
	}
}

// trace: 第二轮变更单 全体 2（EVENT_RETENTION_DAYS）
func TestStreamRetentionFromApp(t *testing.T) {
	e := newEnv(t)
	e.app.EventRetentionDays = 7
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	nc, err := nats.Connect(natsSrv.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	bus, err := NewBus(e.app, nc)
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Stop()
	if err := bus.EnsureStream(ctx); err != nil {
		t.Fatal(err)
	}
	s, _ := bus.JS.Stream(ctx, "EVT_CS")
	info, _ := s.Info(ctx)
	if info.Config.MaxAge != 7*24*time.Hour {
		t.Fatalf("MaxAge=%v", info.Config.MaxAge)
	}
}

// trace: 第二轮变更单 cs（同步调用对方一律带 actor）
func TestDownstreamCommandsCarryActor(t *testing.T) {
	e := newEnv(t)
	e.expect(e.do("POST", "/api/cs/admin/users/U100712/unfreeze", staff(), map[string]any{"reason": "核实后解冻"}, ""), 200, "")
	e.expect(e.do("POST", "/api/cs/admin/users/U100004/assist-refund-deposit", staff(), map[string]any{"reason": "电话申请退押"}, ""), 200, "")
	e.expect(e.do("POST", "/api/cs/admin/users/U100484/assist-cancel", staff(), map[string]any{"reason": "用户要求注销"}, ""), 200, "")
	for _, path := range []string{"/internal/acct/users/U100712/unfreeze", "/internal/acct/users/U100484/cancel"} {
		calls := e.world.CallsTo(path)
		if len(calls) != 1 {
			t.Fatalf("%s %d", path, len(calls))
		}
		a := mustMap(t, calls[0].Body["actor"])
		if a["kind"] != "人" || a["id"] != "AC0301" {
			t.Fatalf("%s %v", path, a)
		}
	}
	dc := e.world.CallsTo("/internal/deposit/")
	if len(dc) != 1 || mustMap(t, dc[0].Body["actor"])["id"] != "AC0301" {
		t.Fatalf("%+v", dc)
	}
}
