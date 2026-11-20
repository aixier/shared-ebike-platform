package core

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// trace: 任务列表 4 发件表与事件收发；wire.md 第 4 节
func TestOutboxPublishAndConsume(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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

	id := e.open("U100484", "OD2026090110600", "我的订单", "调度费")
	n, err := bus.PublishPending(ctx)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if n, _ := bus.PublishPending(ctx); n != 0 {
		t.Fatal("已发的不再发")
	}
	s, err := bus.JS.Stream(ctx, "EVT_CS")
	if err != nil {
		t.Fatal(err)
	}
	msg, err := s.GetLastMsgForSubject(ctx, "cs.ticket.changed")
	if err != nil {
		t.Fatal(err)
	}
	var env Envelope
	_ = json.Unmarshal(msg.Data, &env)
	var p map[string]any
	_ = json.Unmarshal(env.Payload, &p)
	if env.Type != "cs.ticket.changed" || env.Version != 1 || env.Key != id || p["change"] != "新建" || msg.Header.Get(nats.MsgIdHdr) != env.EventID {
		t.Fatalf("发出的事件不对：%+v %v", env, msg.Header)
	}
	for _, k := range []string{"ticketId", "userId", "orderId", "category", "status", "change", "at"} {
		if _, ok := p[k]; !ok {
			t.Fatalf("载荷缺 %s", k)
		}
	}

	// 订阅方：发布方的流不存在时报错，稍后重试
	if err := bus.StartConsumers(ctx); err == nil {
		t.Fatal("pay、rule、fleet 的流还不存在时应报错")
	}
	for _, sc := range []jetstream.StreamConfig{{Name: "EVT_PAY", Subjects: []string{"pay.>"}}, {Name: "EVT_RULE", Subjects: []string{"rule.>"}}, {Name: "EVT_FLEET", Subjects: []string{"fleet.>"}}} {
		if _, err := bus.JS.CreateOrUpdateStream(ctx, sc); err != nil {
			t.Fatal(err)
		}
	}
	if err := bus.StartConsumers(ctx); err != nil {
		t.Fatal(err)
	}
	ev := mustJSON(map[string]any{"eventId": "rule-ev-1", "type": "rule.params.changed", "version": 1, "occurredAt": "2026-12-01T10:00:00+08:00", "key": "params",
		"actor": map[string]any{"kind": "人", "id": "AC0101"}, "payload": map[string]any{"csRefundLimit": 5000, "changedAt": "2026-12-01T10:00:00+08:00"}})
	for i := 0; i < 2; i++ {
		m := &nats.Msg{Subject: "rule.params.changed", Data: ev, Header: nats.Header{}}
		if _, err := bus.JS.PublishMsg(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	// pay 的无关主题不应被本服务消费
	if _, err := bus.JS.Publish(ctx, "pay.payment.succeeded", []byte(`{"eventId":"x","type":"pay.payment.succeeded"}`)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		l, _ := e.app.RefundLimit(ctx)
		if l == 5000 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("事件未被消费，额度=%d", l)
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	var cnt int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM inbox`).Scan(&cnt)
	if cnt != 1 {
		t.Fatalf("同一 eventId 只处理一次，且不消费未订阅主题；inbox=%d", cnt)
	}
}

func TestPublishWhenBusDownKeepsOutbox(t *testing.T) {
	e := newEnv(t)
	e.open("U100004", "", "我的", "你好")
	nc, err := nats.Connect(natsSrv.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	bus, _ := NewBus(e.app, nc)
	nc.Close() // 总线不可用
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := bus.PublishPending(ctx); err == nil {
		t.Fatal("总线不可用时应报错")
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&n)
	if n != 1 {
		t.Fatalf("事件应留在发件表：%d", n)
	}
}

// 客服智能体接入点：接入后由它回复；未接入时不自动回复（其余测试已覆盖）
type stubAgent struct{ app *App }

func (s stubAgent) OnUserMessage(ctx context.Context, ticketID string) error {
	_, err := s.app.AgentReply(ctx, ticketID, "（测试替身）收到")
	return err
}

func TestAgentHookWiring(t *testing.T) {
	e := newEnv(t)
	e.app.Agent = stubAgent{e.app}
	r := e.do("POST", "/api/cs/tickets", userTok("U100004"), map[string]any{"entry": "我的", "text": "你好"}, "")
	e.expect(r, 201, "")
	msgs := r.body["messages"].([]any)
	if len(msgs) != 2 || mustMap(t, msgs[1])["from"] != "客服智能体" {
		t.Fatalf("接入后首条回复应在响应里：%s", r.raw)
	}
	v, err := e.app.AgentReadTicket(context.Background(), r.body["ticketId"].(string))
	if err != nil || len(v.Messages) != 2 || v.RefundLimit != 2000 {
		t.Fatalf("%+v %v", v, err)
	}
}
