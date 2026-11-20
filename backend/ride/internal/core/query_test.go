package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func TestOrderQueries(t *testing.T) {
	e := newEnv(t)
	a := e.unlockOK("U100341", "SF86020226")
	e.lock(a, inST2, nil, false, time.Now())
	b := e.unlockOK("U100004", "SF86020227")
	// 我的订单：只看本人，新到旧，行程部分
	r := e.do("GET", "/api/ride/orders", "U100341", nil)
	rows := r.body["rows"].([]any)
	if r.status != 200 || r.body["total"] != float64(1) || rows[0].(map[string]any)["orderId"] != a {
		t.Fatalf("我的订单：%s", r.raw)
	}
	if _, ok := rows[0].(map[string]any)["userId"]; ok {
		t.Fatal("用户端不返回 userId")
	}
	if r := e.do("GET", "/api/ride/orders?orderIds="+a+","+b, "U100341", nil); r.body["total"] != float64(1) {
		t.Fatalf("按订单号批量查只返回本人的：%s", r.raw)
	}
	if r := e.do("GET", "/api/ride/orders?status=骑行中", "U100004", nil); r.body["total"] != float64(1) {
		t.Fatalf("按状态：%s", r.raw)
	}
	if r := e.do("GET", "/api/ride/orders?status=乱写", "U100004", nil); r.status != 400 {
		t.Fatalf("状态不对：%s", r.raw)
	}
	if r := e.do("GET", "/api/ride/orders/"+b, "U100341", nil); r.status != 404 {
		t.Fatalf("不能看别人的订单：%d", r.status)
	}
	if r := e.do("GET", "/api/ride/orders/"+a, "U100341", nil); r.status != 200 || r.body["status"] != "已结束" {
		t.Fatalf("详情：%s", r.raw)
	}
	// 管理端：权限、数据范围、q 按订单号/用户编号/车号
	tok := e.adminToken([]string{"order.view"}, nil)
	r = e.do("GET", "/api/ride/admin/orders", "admin:"+tok, nil)
	if r.status != 200 || r.body["total"] != float64(2) {
		t.Fatalf("管理端列表：%s", r.raw)
	}
	for q, want := range map[string]float64{"U100004": 1, "QH0005": 1, a[:10]: 2, a: 1} {
		r = e.do("GET", "/api/ride/admin/orders?q="+q, "admin:"+tok, nil)
		if r.body["total"] != want {
			t.Errorf("q=%s 期望 %v：%s", q, want, r.raw)
		}
	}
	r = e.do("GET", "/api/ride/admin/orders?sortKey=requestAt&sortDir=asc&pageSize=1&page=2", "admin:"+tok, nil)
	if rows := r.body["rows"].([]any); len(rows) != 1 || rows[0].(map[string]any)["orderId"] != b {
		t.Fatalf("排序分页：%s", r.raw)
	}
	if r := e.do("GET", "/api/ride/admin/orders/"+a, "admin:"+tok, nil); r.status != 200 || r.body["userId"] != "U100341" || r.body["lockReportAt"] == nil {
		t.Fatalf("管理端详情：%s", r.raw)
	}
	if r := e.do("GET", "/api/ride/admin/orders", "admin:"+e.adminToken([]string{"refund.confirm"}, nil), nil); r.status != 403 || r.body["code"] != "FORBIDDEN" {
		t.Fatalf("无权限：%s", r.raw)
	}
	if r := e.do("GET", "/api/ride/admin/orders", "", nil); r.status != 401 || r.body["code"] != "ADMIN_NOT_LOGGED_IN" {
		t.Fatalf("未登录：%s", r.raw)
	}
	op2 := "OP02"
	scoped := e.adminToken([]string{"order.view"}, &op2)
	if r := e.do("GET", "/api/ride/admin/orders", "admin:"+scoped, nil); r.body["total"] != float64(0) {
		t.Fatalf("数据范围：%s", r.raw)
	}
	if r := e.do("GET", "/api/ride/admin/orders/"+a, "admin:"+scoped, nil); r.status != 403 || r.body["code"] != "SCOPE_DENIED" {
		t.Fatalf("越界：%s", r.raw)
	}
	// 内部：读订单事实
	if r := e.do("GET", "/internal/ride/orders?plateNo=QH0004", "internal:cs", nil); r.body["total"] != float64(1) {
		t.Fatalf("按车号：%s", r.raw)
	}
	if r := e.do("GET", "/internal/ride/orders", "internal:cs", nil); r.status != 400 {
		t.Fatalf("至少一个条件：%s", r.raw)
	}
	if r := e.do("GET", "/internal/ride/orders/"+b, "internal:cs", nil); r.status != 200 || r.body["userId"] != "U100004" {
		t.Fatalf("内部详情：%s", r.raw)
	}
	if r := e.do("GET", "/internal/ride/orders/"+b, "", nil); r.status != 401 {
		t.Fatalf("内部接口要服务凭证：%d", r.status)
	}
}

func TestSnapshotEvents(t *testing.T) {
	e := newEnv(t)
	// 重复事件按 eventId 去重
	b, _ := json.Marshal(map[string]any{"userId": "U100341", "reason": "欠费追缴", "frozenAt": time.Now(), "unfreezeHow": "补付"})
	env := Envelope{EventID: "EV-DUP", Type: "acct.account.frozen", Version: 1, Payload: b}
	_ = e.svc.HandleEvent(context.Background(), env)
	if u := e.svc.snap.user("U100341"); !u.Frozen {
		t.Fatal("冻结应生效")
	}
	e.ev("acct.account.unfrozen", map[string]any{"userId": "U100341", "mode": "人工解冻", "unfrozenAt": time.Now()})
	_ = e.svc.HandleEvent(context.Background(), env)
	if u := e.svc.snap.user("U100341"); u.Frozen {
		t.Fatal("重复的冻结事件不应再生效")
	}
	// 一键支付期限内未付 → 待支付；付清后解除
	e.ev("bill.bill.issued", map[string]any{"orderId": "OD2026113010514", "userId": "U100004", "collectBy": "一键支付", "payable": 300, "payDeadline": time.Now().Add(24 * time.Hour)})
	if r := e.do("POST", "/api/ride/scan", "U100004", map[string]any{"code": "SF86020226"}); r.body["check"].(map[string]any)["code"] != "UNPAID_ORDER" {
		t.Fatalf("待支付：%s", r.raw)
	}
	e.ev("bill.bill.paid", map[string]any{"orderId": "OD2026113010514", "userId": "U100004", "payStatus": "已支付"})
	if r := e.do("POST", "/api/ride/scan", "U100004", map[string]any{"code": "SF86020226"}); r.body["check"].(map[string]any)["ok"] != true {
		t.Fatalf("付清后可骑：%s", r.raw)
	}
	e.ev("bill.arrears.cleared", map[string]any{"userId": "U100036", "clearedAt": time.Now()})
	if u := e.svc.snap.user("U100036"); u.ArrearsTotal != 0 {
		t.Fatal("欠费结清")
	}
	// 品牌暂停与恢复、码失效、车辆移除
	e.ev("fleet.brand.access_changed", map[string]any{"brandId": "BR01", "operatorId": "OP01", "from": "已接入", "to": "暂停可租", "changedAt": time.Now()})
	if r := e.do("POST", "/api/ride/scan", "U100341", map[string]any{"code": "SF86020226"}); r.body["check"].(map[string]any)["code"] != "BRAND_PAUSED" {
		t.Fatalf("暂停可租：%s", r.raw)
	}
	e.ev("fleet.brand.access_changed", map[string]any{"brandId": "BR01", "operatorId": "OP01", "from": "暂停可租", "to": "已接入", "changedAt": time.Now()})
	e.ev("fleet.vehicle.removed", map[string]any{"vehicleId": "VH000005", "brandId": "BR01", "operatorId": "OP01", "govCode": "SF86020227", "removedAt": time.Now()})
	if r := e.do("POST", "/api/ride/scan", "U100341", map[string]any{"code": "SF86020227"}); r.body["check"].(map[string]any)["code"] != "CODE_INVALID" {
		t.Fatalf("车辆移除：%s", r.raw)
	}
	// 平台参数与规则版本
	e.ev("rule.params.changed", map[string]any{"unlockReportTimeoutSec": 20, "nearbyRadiusM": 800, "backfillWindowHours": 48})
	if p := e.svc.snap.getParams(); p.UnlockReportTimeoutSec != 20 || p.NearbyRadiusM != 800 || p.BackfillWindowHours != 48 {
		t.Fatalf("平台参数：%+v", p)
	}
	e.ev("rule.version.published", map[string]any{"versionId": "PR0004", "effectiveAt": time.Now().Add(time.Hour), "ride": map[string]any{"startFee": 200, "startMinutes": 3, "perMinute": 60}})
	if v := e.svc.snap.versionAt(time.Now()); v.VersionID != "PR0003" {
		t.Fatal("待生效版本不参与取值")
	}
	if v := e.svc.snap.versionAt(time.Now().Add(2 * time.Hour)); v.VersionID != "PR0004" {
		t.Fatal("生效后取新版本")
	}
	e.ev("rule.version.revoked", map[string]any{"versionId": "PR0004", "revokedAt": time.Now()})
	if v := e.svc.snap.versionAt(time.Now().Add(2 * time.Hour)); v.VersionID != "PR0003" {
		t.Fatal("撤销的版本不参与取值")
	}
	// 另一个实例从数据库刷新到同样的快照
	other := NewSnapshot()
	if err := other.refresh(context.Background(), e.svc.store, true); err != nil {
		t.Fatal(err)
	}
	if v := other.findVehicle("SF86020226"); v == nil || v.VehicleID != "VH000004" {
		t.Fatal("别的实例刷新快照")
	}
}

func TestRefundRequestedBlocks(t *testing.T) {
	e := newEnv(t)
	id := e.unlockOK("U100004", "SF86020226")
	e.ev("deposit.refund.requested", map[string]any{"requestId": "RF202612010001", "userId": "U100004", "amount": 9900, "appliedAt": time.Now()})
	ev := e.outbox("ride.admission.blocked")
	if len(ev) != 1 || ev[0]["requestId"] != "RF202612010001" || ev[0]["ridingOrderId"] != id {
		t.Fatalf("已阻止开单回执：%v", ev)
	}
	var nb, created time.Time
	_ = testPool.QueryRow(context.Background(), `SELECT not_before, created_at FROM outbox WHERE subject='ride.admission.blocked'`).Scan(&nb, &created)
	if !nb.After(created) {
		t.Fatal("回执应在所有实例生效后才发出")
	}
	e.lock(id, inST2, nil, false, time.Now())
	if r := e.do("POST", "/api/ride/scan", "U100004", map[string]any{"code": "SF86020226"}); r.body["check"].(map[string]any)["code"] != "DEPOSIT_REFUNDING" {
		t.Fatalf("退押中不能开：%s", r.raw)
	}
}

// TestEventBus 发件表发到总线、从总线消费订阅事件（进程内 nats-server，只监听 127.0.0.1）。
func TestEventBus(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(tmpRoot, "nats-"+strings.ReplaceAll(time.Now().Format("150405.000000"), ".", ""))
	defer os.RemoveAll(dir)
	ns, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: dir, NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go ns.Start()
	defer ns.Shutdown()
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats 未就绪")
	}
	nc, err := nats.Connect(ns.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, _ := jetstream.New(nc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := EnsureStream(ctx, js, time.Hour); err != nil {
		t.Fatal(err)
	}
	// 发布：开一单，发件表里的事件发到 EVT_RIDE
	id := e.unlockOK("U100341", "SF86020226")
	n, err := e.svc.PublishOutbox(ctx, js)
	if err != nil || n < 2 {
		t.Fatalf("发布：%d %v", n, err)
	}
	cons, _ := js.CreateOrUpdateConsumer(ctx, "EVT_RIDE", jetstream.ConsumerConfig{Durable: "probe", FilterSubject: "ride.order.unlocked"})
	msg, err := cons.Next(jetstream.FetchMaxWait(3 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var env Envelope
	_ = json.Unmarshal(msg.Data(), &env)
	if env.Type != "ride.order.unlocked" || env.Key != id || msg.Headers().Get("Nats-Msg-Id") != env.EventID {
		t.Fatalf("总线上的事件：%+v", env)
	}
	if n, _ := e.svc.PublishOutbox(ctx, js); n != 0 {
		t.Fatal("已发的不重发")
	}
	// 消费：fleet 的流先由发布方建好，ride 建持久消费者 ride
	_, _ = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{Name: "EVT_FLEET", Subjects: []string{"fleet.>"}})
	e.svc.StartConsumers(ctx, js)
	pub := func(typ string, p map[string]any) {
		b, _ := json.Marshal(p)
		eb, _ := json.Marshal(Envelope{EventID: newEventID(), Type: typ, Version: 1, OccurredAt: time.Now(), Key: "BR01", Actor: systemActor, Payload: b})
		if _, err := js.Publish(ctx, typ, eb); err != nil {
			t.Fatal(err)
		}
	}
	pub("fleet.brand.access_changed", map[string]any{"brandId": "BR01", "operatorId": "OP01", "from": "已接入", "to": "暂停可租", "changedAt": time.Now()})
	eventually(t, 10*time.Second, func() bool { b := e.svc.snap.brand("BR01"); return b != nil && b.AccessStatus == "暂停可租" })
	info, err := js.Consumer(ctx, "EVT_FLEET", "ride")
	if err != nil {
		t.Fatal(err)
	}
	if ci, _ := info.Info(ctx); ci.Config.MaxAckPending != 1 {
		t.Fatal("一次只处理一条以保序")
	}
}
