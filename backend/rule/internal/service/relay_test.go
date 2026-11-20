package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// trace: contracts/wire.md 第 4 节、contracts/events/README.md 第 4 节 任务 4 10

func TestRelay_发件表按序发到事件总线(t *testing.T) {
	e := newEnv(t, true)
	e.do("POST", "/api/rule/pricing/versions", editor, map[string]any{"name": "发布测试", "reason": "政府调价通知", "dispatchFee": 600}, nil)
	e.do("POST", "/api/rule/pricing/versions/PR0004/revoke", editor, map[string]any{"reason": "门槛另行研究"}, nil)

	nc, err := nats.Connect(testNats.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	relay, err := NewRelay(testPool, nc, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	js, _ := jetstream.New(nc)
	_ = js.DeleteStream(ctx, streamName)

	n, err := relay.Flush(ctx)
	if err != nil || n != 4 {
		t.Fatalf("应发出 4 条，实得 %d %v", n, err)
	}
	if n, _ := relay.Flush(ctx); n != 0 {
		t.Errorf("已发布的不应重发：%d", n)
	}
	var left int
	_ = testPool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&left)
	if left != 0 {
		t.Errorf("发件表应全部标记已发布：剩 %d", left)
	}

	st, err := js.Stream(ctx, streamName)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := st.Info(ctx)
	if info.Config.Storage != jetstream.FileStorage || info.Config.Subjects[0] != "rule.>" || info.State.Msgs != 4 {
		t.Errorf("流配置或条数不对：%+v %+v", info.Config, info.State)
	}
	wantSubj := []string{"rule.version.published", "rule.audit.recorded", "rule.version.revoked", "rule.audit.recorded"}
	for i, subj := range wantSubj {
		m, err := st.GetMsg(ctx, uint64(i+1))
		if err != nil {
			t.Fatal(err)
		}
		var env map[string]any
		_ = json.Unmarshal(m.Data, &env)
		if m.Subject != subj || env["type"] != subj {
			t.Errorf("第 %d 条应为 %s，实得 %s", i+1, subj, m.Subject)
		}
		if m.Header.Get("Nats-Msg-Id") != env["eventId"] || env["eventId"] == "" {
			t.Errorf("消息编号应为 eventId：%v %v", m.Header, env["eventId"])
		}
	}
}

func TestRelay_总线不可用时事件留在发件表(t *testing.T) {
	e := newEnv(t, true)
	nc, err := nats.Connect(testNats.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	relay, _ := NewRelay(testPool, nc, 0, nil)
	nc.Close() // 总线断开

	r := e.do("POST", "/api/rule/pricing/versions", editor, map[string]any{"name": "断线测试", "reason": "政府调价通知", "dispatchFee": 700}, nil)
	expect(t, r, 200, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := relay.Flush(ctx); err == nil {
		t.Error("总线断开时发布应失败")
	}
	var left int
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&left)
	if left != 2 {
		t.Errorf("事件应留在发件表等待补发：%d", left)
	}
}
