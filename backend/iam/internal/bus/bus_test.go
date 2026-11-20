package bus

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func startNATS(t *testing.T) string {
	t.Helper()
	dir, _ := filepath.Abs(filepath.Join("..", "..", ".tmp", "nats-bus-"+t.Name()))
	_ = os.RemoveAll(dir)
	ns, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: dir, NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats 未就绪")
	}
	t.Cleanup(func() { ns.Shutdown(); ns.WaitForShutdown(); _ = os.RemoveAll(dir) })
	return ns.ClientURL()
}

func connect(t *testing.T, url string) jetstream.JetStream {
	t.Helper()
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	return js
}

type recorder struct {
	mu   sync.Mutex
	got  []string
	fail map[string]int // 某条消息先失败几次
}

func (r *recorder) handle(_ context.Context, subject string, data []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail[string(data)] > 0 {
		r.fail[string(data)]--
		return context.DeadlineExceeded
	}
	r.got = append(r.got, subject+"|"+string(data))
	return nil
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.got...)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("等待超时")
}

// 流不存在时订阅不报错，流出现后自动接上，且按顺序、失败重投。
func TestSubscriberToleratesMissingStreamAndRedelivers(t *testing.T) {
	url := startNATS(t)
	js := connect(t, url)
	rec := &recorder{fail: map[string]int{"m1": 1}}
	sub := NewSubscriber(js, "iam", 100*time.Millisecond, rec.handle, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sub.Run(ctx, []Subscription{{Stream: "EVT_RIDE", Filters: []string{"ride.audit.recorded"}}})

	time.Sleep(300 * time.Millisecond)
	if sub.Active("EVT_RIDE") {
		t.Fatal("流还不存在，不应处于已订阅状态")
	}
	if err := EnsureStream(ctx, js, "EVT_RIDE", []string{"ride.>"}, time.Hour); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"m1", "m2", "m3"} {
		if err := Publish(ctx, js, "ride.audit.recorded", "id-"+m, []byte(m)); err != nil {
			t.Fatal(err)
		}
	}
	// 不在过滤范围内的主题不送达
	_ = Publish(ctx, js, "ride.order.ended", "id-x", []byte("x"))
	waitFor(t, func() bool { return len(rec.list()) == 3 })
	got := rec.list()
	want := []string{"ride.audit.recorded|m1", "ride.audit.recorded|m2", "ride.audit.recorded|m3"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("顺序或内容不对: %v", got)
		}
	}
	if !sub.Active("EVT_RIDE") {
		t.Fatal("应处于已订阅状态")
	}
}

// 同一消息编号重复发布，总线侧去重。
func TestPublishDedupByMsgID(t *testing.T) {
	url := startNATS(t)
	js := connect(t, url)
	ctx := context.Background()
	if err := EnsureStream(ctx, js, "EVT_IAM", []string{"iam.>"}, time.Hour); err != nil {
		t.Fatal(err)
	}
	// 再次确保不报错
	if err := EnsureStream(ctx, js, "EVT_IAM", []string{"iam.>"}, time.Hour); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := Publish(ctx, js, "iam.account.changed", "E1", []byte("{}")); err != nil {
			t.Fatal(err)
		}
	}
	s, _ := js.Stream(ctx, "EVT_IAM")
	info, _ := s.Info(ctx)
	if info.State.Msgs != 1 {
		t.Fatalf("应只有 1 条，实际 %d", info.State.Msgs)
	}
}

// 多个过滤主题（fleet 的留痕与运营商退出）用一个消费者。
func TestMultipleFilters(t *testing.T) {
	url := startNATS(t)
	js := connect(t, url)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = EnsureStream(ctx, js, "EVT_FLEET", []string{"fleet.>"}, time.Hour)
	rec := &recorder{fail: map[string]int{}}
	sub := NewSubscriber(js, "iam", 100*time.Millisecond, rec.handle, nil)
	go sub.Run(ctx, []Subscription{{Stream: "EVT_FLEET", Filters: []string{"fleet.audit.recorded", "fleet.operator.exited"}}})
	_ = Publish(ctx, js, "fleet.operator.exited", "a", []byte("a"))
	_ = Publish(ctx, js, "fleet.brand.registered", "b", []byte("b"))
	_ = Publish(ctx, js, "fleet.audit.recorded", "c", []byte("c"))
	waitFor(t, func() bool { return len(rec.list()) == 2 })
	got := rec.list()
	if got[0] != "fleet.operator.exited|a" || got[1] != "fleet.audit.recorded|c" {
		t.Fatalf("内容不对: %v", got)
	}
}
