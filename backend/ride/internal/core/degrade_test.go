package core

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"bike/ride/internal/clients/opgw"
	"bike/ride/internal/journal"
	"bike/ride/internal/rules"
)

func TestDegradeByBreaker(t *testing.T) {
	e := newEnv(t)
	// 支付熔断：老用户照常开，只多一行"费用将稍后收取"，订单带降级标记
	e.svc.breakers.Trip(rules.CompPay)
	if ev := e.outbox("ride.dependency.state_changed"); len(ev) != 1 || ev[0]["component"] != "支付通道" || ev[0]["state"] != "熔断" || ev[0]["instance"] != "test-1" {
		t.Fatalf("熔断事件：%v", ev)
	}
	r := e.do("POST", "/api/ride/scan", "U100341", map[string]any{"code": "SF86020226"})
	dg := r.body["degrade"].(map[string]any)
	adm := dg["admission"].(map[string]any)
	if dg["active"] != true || dg["line"] != "费用将稍后收取" || adm["category"] != "老用户" || adm["notice"] != nil {
		t.Fatalf("降级扫码：%s", r.raw)
	}
	id := e.unlockOK("U100341", "SF86020226")
	o := e.order(id)
	if o.Degraded == nil || o.Degraded.Admission != "老用户" || o.Degraded.LimitMinutes != 120 || o.Degraded.EventID != nil || !o.Degraded.FeeLater {
		t.Fatalf("降级标记：%+v", o.Degraded)
	}
	if cr := e.outbox("ride.order.created"); cr[0]["degraded"] == nil {
		t.Fatal("订单已创建事件应带降级标记")
	}
	// 已知欠费的不能开；先骑后验新用户可开紧档
	if r := e.do("POST", "/api/ride/unlock", "U100036", map[string]any{"code": "SF86020227"}); r.body["code"] != "DEGRADE_REJECT_ARREARS" {
		t.Fatalf("降级欠费：%s", r.raw)
	}
	if r := e.do("POST", "/api/ride/unlock", "U101097", map[string]any{"code": "SF86020227"}); r.body["code"] != "DEGRADE_REJECT_REFUNDING" {
		t.Fatalf("降级退押中：%s", r.raw)
	}
	r = e.do("POST", "/api/ride/scan", "U100026", map[string]any{"code": "SF86020227"})
	adm = r.body["degrade"].(map[string]any)["admission"].(map[string]any)
	if adm["category"] != "先骑后验新用户" || adm["notice"] == nil || adm["limitMinutes"] != float64(30) {
		t.Fatalf("先骑后验：%s", r.raw)
	}
	// 老用户还车后，费用未收取前不能再开降级单（每用户未结降级单 1）
	e.lock(id, inST2, nil, false, time.Now())
	if r := e.do("POST", "/api/ride/unlock", "U100341", map[string]any{"code": "SF86020227"}); r.body["code"] != "DEGRADE_USER_LIMIT" {
		t.Fatalf("每用户未结降级单：%s", r.raw)
	}
	e.ev("bill.bill.paid", map[string]any{"orderId": id, "userId": "U100341", "payStatus": "已支付", "channelPaid": 300, "depositDeducted": 0, "paidAt": time.Now(), "degraded": true})
	e.unlockOK("U100341", "SF86020227")
	// 恢复后不再降级
	e.svc.breakers.Report(rules.CompPay, nil)
	e.svc.breakers.Report(rules.CompPay, nil)
	if r := e.do("POST", "/api/ride/scan", "U100004", map[string]any{"code": "SF86020226"}); r.body["degrade"].(map[string]any)["active"] != false {
		t.Fatalf("恢复后：%s", r.raw)
	}
}

func TestDegradeOpsControls(t *testing.T) {
	e := newEnv(t)
	e.ev("ops.degrade.entered", map[string]any{"eventId": "DG202612010001", "cause": "押金查询不可用", "components": []string{"押金与免押查询"}, "enteredAt": time.Now(), "limitsVersionId": "PR0003"})
	// 押金状态未知的老用户：紧档并带说明
	r := e.do("POST", "/api/ride/scan", "U100004", map[string]any{"code": "SF86020226"})
	adm := r.body["degrade"].(map[string]any)["admission"].(map[string]any)
	if adm["category"] != "押金状态未知的老用户" || adm["limitAmount"] != float64(1500) || adm["notice"] == nil {
		t.Fatalf("押金未知：%s", r.raw)
	}
	id := e.unlockOK("U100004", "SF86020226")
	if o := e.order(id); o.Degraded == nil || o.Degraded.EventID == nil || *o.Degraded.EventID != "DG202612010001" {
		t.Fatalf("应打上降级事件编号：%+v", o.Degraded)
	}
	// 值班人收紧：紧档暂停，老用户不受影响（这里所有老用户都按押金未知）
	e.ev("ops.degrade.tightened", map[string]any{"eventId": "DG202612010001", "at": time.Now(), "reason": "接近每小时上限"})
	if r := e.do("POST", "/api/ride/unlock", "U100341", map[string]any{"code": "SF86020227"}); r.body["code"] != "DEGRADE_PAUSED_TIGHT" {
		t.Fatalf("收紧：%s", r.raw)
	}
	if r := e.do("POST", "/api/ride/unlock", "U100026", map[string]any{"code": "SF86020227"}); r.body["code"] != "DEGRADE_NEW_USER_UNAVAILABLE" {
		t.Fatalf("收紧新用户：%s", r.raw)
	}
	e.ev("ops.degrade.stopped", map[string]any{"eventId": "DG202612010001", "at": time.Now(), "reason": "恢复中"})
	if r := e.do("POST", "/api/ride/unlock", "U100341", map[string]any{"code": "SF86020227"}); r.body["code"] != "DEGRADE_STOPPED" {
		t.Fatalf("停止：%s", r.raw)
	}
	e.ev("ops.degrade.relaxed", map[string]any{"eventId": "DG202612010001", "at": time.Now(), "reason": "放宽", "to": "正常"})
	e.unlockOK("U100341", "SF86020227")
	// 超限只提醒不结束（6.7）
	e.clock.advance(31 * time.Minute)
	e.svc.SweepOverLimit(context.Background())
	o := e.order(id)
	if o.Status != StRiding || o.Degraded.OverLimitRemindedAt == nil {
		t.Fatalf("超限提醒：%+v", o)
	}
	if ev := e.outbox("ride.order.over_limit_reminded"); len(ev) != 2 {
		t.Fatalf("两单都超紧档 30 分钟，应各提醒一次：%v", ev)
	}
	e.svc.SweepOverLimit(context.Background())
	if ev := e.outbox("ride.order.over_limit_reminded"); len(ev) != 2 {
		t.Fatal("只提醒一次")
	}
	cur := e.do("GET", "/api/ride/current", "U100004", nil)
	if ol := cur.body["overLimit"].(map[string]any); ol["reached"] != true {
		t.Fatalf("骑行页超限：%s", cur.raw)
	}
	e.ev("ops.degrade.exited", map[string]any{"eventId": "DG202612010001", "exitedAt": time.Now(), "durationMinutes": 40})
	if d := e.svc.snap.getDegrade(); d.Active {
		t.Fatal("退出后不降级")
	}
}

// TestDatabaseDownJournalReplay 数据库不可用：订单与骑行事件先写本地日志再开锁；恢复后按订单号去重入库并补发事件（R-34、NFR-08）。
func TestDatabaseDownJournalReplay(t *testing.T) {
	e := newEnv(t)
	cfg, _ := pgxpool.ParseConfig("postgres://postgres:postgres@127.0.0.1:1/postgres?sslmode=disable&connect_timeout=1")
	deadPool, _ := pgxpool.NewWithConfig(context.Background(), cfg)
	defer deadPool.Close()
	j, _ := journal.Open(e.jdir)
	down := New(deadPool, e.svc.snap, j, opgw.New(e.og.srv.URL, "key-ride"), Options{InstanceID: "test-2", InstanceNo: 1, PollInterval: 20 * time.Millisecond,
		UnlockTimeoutMin: 800 * time.Millisecond}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	down.breakers.Trip(rules.CompDB)
	e.og.svc = down
	ctx := context.Background()
	res, ae := down.Unlock(ctx, "U100341", "SF86020226")
	if ae != nil {
		t.Fatalf("数据库不可用时照常开单：%v", ae)
	}
	id := res["orderId"].(string)
	if id[10:] != "55001" {
		t.Fatalf("本地序号段：%s", id)
	}
	if res["degraded"] == nil {
		t.Fatal("数据库熔断时新单带降级标记")
	}
	// 同一人再开：内存里守一人一单
	if _, ae := down.Unlock(ctx, "U100341", "SF86020227"); ae == nil || ae.Body["code"] != "HAS_RIDING" {
		t.Fatalf("一人一单：%v", ae)
	}
	// 锁车报告也写日志并对内存订单生效
	ev := lockEvent("JE-1", id, "QH0004", time.Now(), inST2, nil)
	if r, ae := down.DeliverRideEvent(ctx, ev); ae != nil || r["accepted"] != true {
		t.Fatalf("送达：%v %v", r, ae)
	}
	if j.Pending() < 3 {
		t.Fatalf("日志条数：%d", j.Pending())
	}
	var n int
	_ = testPool.QueryRow(ctx, `SELECT count(*) FROM orders`).Scan(&n)
	if n != 0 {
		t.Fatal("数据库里还不该有")
	}
	// 恢复：回放两次，结果一样（按订单号去重）
	e.og.svc = e.svc
	if _, err := e.svc.Replay(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Replay(ctx); err != nil {
		t.Fatal(err)
	}
	o := e.order(id)
	if o.Status != StEnded || *o.InStationBasis != "运营商车端" || o.Degraded == nil {
		t.Fatalf("回放后订单：%+v", o)
	}
	_ = testPool.QueryRow(ctx, `SELECT count(*) FROM orders`).Scan(&n)
	if n != 1 {
		t.Fatalf("不丢不重：%d", n)
	}
	for _, typ := range []string{"ride.order.created", "ride.order.unlocked", "ride.order.ended"} {
		if len(e.outbox(typ)) != 1 {
			t.Fatalf("应补发 %s 一条：%v", typ, e.outbox(typ))
		}
	}
	var rep int
	_ = testPool.QueryRow(ctx, `SELECT count(*) FROM ride_reports WHERE applied`).Scan(&rep)
	if rep < 2 || j.Pending() != 0 {
		t.Fatalf("报告入库：%d，日志剩 %d", rep, j.Pending())
	}
}
