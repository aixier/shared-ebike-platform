package core

import (
	"context"
	"testing"
	"time"
)

var (
	inST1   = [2]float64{104.0005, 30.0005} // 有桩站 ST0001 内
	inST2   = [2]float64{104.0105, 30.0005} // 无桩站 ST0002 内
	outside = [2]float64{104.0005, 30.0025} // ST0001 北约 166 米
)

func (e *testEnv) lock(orderID string, loc [2]float64, side *bool, late bool, at time.Time) resp {
	e.t.Helper()
	ev := lockEvent(e.og.nextID(), orderID, "QH0004", at, loc, side)
	ev.Late = late
	ev.ReceivedAt = e.svc.now()
	return e.do("POST", "/internal/ride/ride-events", "internal:opgw", ev)
}

func TestLockReportEnds(t *testing.T) {
	cases := []struct {
		name   string
		loc    [2]float64
		side   *bool
		prep   func(e *testEnv)
		inSt   bool
		basis  string
		method string
	}{
		{"无桩站点（ST0002）", inST2, nil, nil, true, "运营商车端", "用户锁车"},
		{"有空桩、车端判定", inST1, bp(true), nil, true, "运营商车端", "用户锁车"},
		{"站外（BR-11）", outside, bp(false), nil, false, "站外", "用户锁车"},
		{"桩满围栏内（BR-10）", inST1, nil, func(e *testEnv) {
			e.ev("site.pile.occupancy", map[string]any{"pileId": "PL00001", "stationId": "ST0001", "vehicleId": "VH000901", "present": true, "at": time.Now(), "receivedAt": time.Now()})
			e.ev("site.pile.occupancy", map[string]any{"pileId": "PL00002", "stationId": "ST0001", "vehicleId": "VH000902", "present": true, "at": time.Now(), "receivedAt": time.Now()})
		}, true, "围栏", "用户锁车"},
		{"桩故障车端判定（BR-12）", inST1, bp(true), func(e *testEnv) {
			e.ev("site.pile.status_changed", map[string]any{"pileId": "PL00001", "stationId": "ST0001", "no": 1, "location": [2]float64{104.0002, 30.0002}, "status": "故障"})
			e.ev("site.pile.occupancy", map[string]any{"pileId": "PL00002", "stationId": "ST0001", "vehicleId": "VH000902", "present": true, "at": time.Now(), "receivedAt": time.Now()})
		}, true, "运营商车端", "用户锁车"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			if c.prep != nil {
				c.prep(e)
			}
			id := e.unlockOK("U100341", "SF86020226")
			time.Sleep(30 * time.Millisecond)
			r := e.lock(id, c.loc, c.side, false, time.Now())
			if r.status != 200 || r.body["orderId"] != id {
				t.Fatalf("送达锁车：%s", r.raw)
			}
			o := e.order(id)
			if o.Status != StEnded || *o.InStation != c.inSt || *o.InStationBasis != c.basis || *o.EndMethod != c.method {
				t.Fatalf("结束：status=%s inStation=%v basis=%s method=%s", o.Status, *o.InStation, *o.InStationBasis, *o.EndMethod)
			}
			if o.DurationSec == nil || o.LockReportAt == nil || o.ReportReceivedAt == nil || *o.DistanceKm != 1.3 {
				t.Fatalf("结束字段：%+v", o)
			}
			ev := e.outbox("ride.order.ended")
			if len(ev) != 1 || ev[0]["inStation"] != c.inSt || ev[0]["endMethod"] != c.method || ev[0]["lateReport"] != false {
				t.Fatalf("已结束事件：%v", ev)
			}
			if !c.inSt && ev[0]["distanceToStationM"] == nil {
				t.Fatal("站外应给距最近站点的米数")
			}
			// 车辆最后位置更新，可再被附近车列出
			var loc []byte
			_ = testPool.QueryRow(context.Background(), `SELECT doc->'location' FROM snap WHERE kind='vehicle' AND id='VH000004'`).Scan(&loc)
			if len(loc) == 0 {
				t.Fatal("车辆位置应更新")
			}
		})
	}
}

func TestPileInEnds(t *testing.T) {
	e := newEnv(t)
	id := e.unlockOK("U100341", "SF86020226")
	time.Sleep(30 * time.Millisecond)
	pileAt := time.Now()
	e.ev("site.pile.occupancy", map[string]any{"pileId": "PL00001", "stationId": "ST0001", "vehicleId": "VH000004", "present": true, "at": pileAt, "receivedAt": pileAt})
	// 锁车报告晚 2 分钟，车端说不在点（定位漂移）：终点取桩时刻、以桩为准（BR-01、BR-13）
	lockAt := pileAt.Add(2 * time.Minute)
	e.clock.advance(3 * time.Minute)
	r := e.lock(id, outside, bp(false), false, lockAt)
	if r.status != 200 {
		t.Fatal(string(r.raw))
	}
	o := e.order(id)
	if *o.EndMethod != EndPile || *o.InStationBasis != "桩识别" || !*o.InStation || *o.EndPileID != "PL00001" || *o.EndStationID != "ST0001" {
		t.Fatalf("入桩：%+v basis=%s", o, *o.InStationBasis)
	}
	if !o.EndAt.Equal(ts(pileAt)) || o.PileInAt == nil || o.VehicleSideInStation == nil || *o.VehicleSideInStation {
		t.Fatalf("终点应取桩在位时刻：end=%v pile=%v", o.EndAt, pileAt)
	}
}

func TestPileNoLock(t *testing.T) {
	e := newEnv(t)
	id := e.unlockOK("U100341", "SF86020226")
	time.Sleep(30 * time.Millisecond)
	at := time.Now()
	e.ev("site.pile.occupancy", map[string]any{"pileId": "PL00002", "stationId": "ST0001", "vehicleId": "VH000004", "present": true, "at": at, "receivedAt": at})
	e.svc.SweepPileNoLock(context.Background()) // 还没到等待时长
	for _, c := range e.og.callList() {
		if c == "/internal/opgw/end-ride|"+id+"|入桩未锁" {
			t.Fatal("等待时长内不应请求结束")
		}
	}
	e.clock.advance(2 * time.Second)
	e.og.set(func(f *fakeOpgw) { f.lockOnEnd = &lockSpec{loc: inST1} })
	e.svc.SweepPileNoLock(context.Background())
	eventually(t, 2*time.Second, func() bool { return e.order(id).Status == StEnded })
	o := e.order(id)
	if *o.EndMethod != EndPileNoLock || !o.EndAt.Equal(ts(at)) || *o.LockRequest != "已受理" {
		t.Fatalf("入桩未锁：method=%s end=%v", *o.EndMethod, o.EndAt)
	}
}

func TestPageReturnAndPreview(t *testing.T) {
	e := newEnv(t)
	if r := e.do("GET", "/api/ride/return-preview", "U100341", nil); r.status != 409 || r.body["code"] != "NO_RIDING" {
		t.Fatalf("没有骑行：%s", r.raw)
	}
	id := e.unlockOK("U100341", "SF86020226")
	r := e.do("GET", "/api/ride/return-preview?lng=104.0005&lat=30.0025", "U100341", nil)
	if r.status != 200 || r.body["inStation"] != false || r.body["dispatchFee"] != float64(500) || r.body["nearestStation"] == nil {
		t.Fatalf("站外预判：%s", r.raw)
	}
	if r.body["text"] != "不在站点内，还车将收取调度费 ¥5.0。最近站点「天府广场东」约 166 米" {
		t.Fatalf("预判文案：%v", r.body["text"])
	}
	r = e.do("GET", "/api/ride/return-preview?stationId=ST0002", "U100341", nil)
	if r.body["inStation"] != true || r.body["dispatchFee"] != float64(0) || r.body["basis"] != "运营商车端" {
		t.Fatalf("站内预判：%s", r.raw)
	}
	// 站外未确认：先提示调度费与最近站点，charged=true
	r = e.do("POST", "/api/ride/return", "U100341", map[string]any{"location": outside})
	if r.status != 409 || r.body["code"] != "DISPATCH_CONFIRM_REQUIRED" || r.body["charged"] != true || r.body["dispatchFee"] != float64(500) || r.body["nearestStation"] == nil {
		t.Fatalf("站外须确认：%s", r.raw)
	}
	// 确认后：请求运营商结束，锁车报告到了即结束
	e.og.set(func(f *fakeOpgw) { f.lockOnEnd = &lockSpec{loc: outside, side: bp(false)} })
	r = e.do("POST", "/api/ride/return", "U100341", map[string]any{"location": outside, "confirmDispatch": true}, "Idempotency-Key", "ret-1")
	if r.status != 200 || r.body["ended"] != true || r.body["deferred"] != false {
		t.Fatalf("页面还车：%s", r.raw)
	}
	o := e.order(id)
	if *o.EndMethod != EndPage || !o.DispatchPromptShown || *o.InStation {
		t.Fatalf("页面还车结果：%+v", o)
	}
	found := false
	for _, c := range e.og.callList() {
		if c == "/internal/opgw/end-ride|"+id+"|用户点还车" {
			found = true
		}
	}
	if !found {
		t.Fatal("应请求运营商结束骑行")
	}
	if r := e.do("POST", "/api/ride/return", "U100341", map[string]any{}); r.status != 409 || r.body["code"] != "NO_RIDING" {
		t.Fatalf("结束后再还：%s", r.raw)
	}
}

func TestPageReturnNoLockReportYet(t *testing.T) {
	e := newEnv(t)
	e.unlockOK("U100341", "SF86020226")
	r := e.do("POST", "/api/ride/return", "U100341", map[string]any{"stationId": "ST0002"})
	if r.status != 200 || r.body["ended"] != false || r.body["message"] != "还车确认中，你可以离开，按实际锁车时刻计费" {
		t.Fatalf("锁车报告未到：%s", r.raw)
	}
	// 桩位不可用、桩满
	e.ev("site.pile.status_changed", map[string]any{"pileId": "PL00001", "stationId": "ST0001", "no": 1, "location": [2]float64{104.0002, 30.0002}, "status": "离线"})
	e.ev("site.pile.occupancy", map[string]any{"pileId": "PL00002", "stationId": "ST0001", "vehicleId": "VH000999", "present": true, "at": time.Now(), "receivedAt": time.Now()})
	if r := e.do("POST", "/api/ride/return", "U100341", map[string]any{"pileId": "PL00001"}); r.status != 409 || r.body["code"] != "PILE_UNAVAILABLE" || r.body["stationId"] != "ST0001" {
		t.Fatalf("桩不可用：%s", r.raw)
	}
	if r := e.do("POST", "/api/ride/return", "U100341", map[string]any{"pileId": "PL00002"}); r.status != 409 || r.body["code"] != "PILE_FULL" {
		t.Fatalf("桩满：%s", r.raw)
	}
}

func TestLateReportAndManualLock(t *testing.T) {
	e := newEnv(t)
	id := e.unlockOK("U100341", "SF86020226")
	time.Sleep(30 * time.Millisecond)
	// 运营商未带订单号的手动锁车（补报）：按车号匹配骑行中订单
	lockAt := time.Now()
	e.clock.advance(5 * time.Hour)
	r := e.lock("", inST2, nil, true, lockAt)
	if r.status != 200 || r.body["orderId"] != id {
		t.Fatalf("补报匹配：%s", r.raw)
	}
	o := e.order(id)
	if !o.LateReport || !o.EndAt.Equal(ts(lockAt)) || o.ReportReceivedAt.Sub(*o.EndAt) < 4*time.Hour {
		t.Fatalf("补报：late=%v end=%v recv=%v", o.LateReport, o.EndAt, o.ReportReceivedAt)
	}
	// 匹配不到的保留待匹配
	r = e.lock("OD2099010100001", inST2, nil, true, time.Now())
	if r.status != 200 || r.body["accepted"] != true || r.body["orderId"] != nil {
		t.Fatalf("未匹配：%s", r.raw)
	}
	var unapplied int
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM ride_reports WHERE NOT applied`).Scan(&unapplied)
	if unapplied != 1 {
		t.Fatalf("未匹配的应保留：%d", unapplied)
	}
	if r := e.do("POST", "/internal/ride/ride-events", "internal:opgw", map[string]any{"kind": "锁车"}); r.status != 400 {
		t.Fatalf("缺字段：%s", r.raw)
	}
	if r := e.do("POST", "/internal/ride/ride-events", "internal:bill", map[string]any{}); r.status != 401 {
		t.Fatalf("不在名单的调用方：%d", r.status)
	}
}

func TestForceEnd(t *testing.T) {
	e := newEnv(t)
	id := e.unlockOK("U100341", "SF86020226")
	actor := map[string]any{"kind": "人", "id": "AC0301", "name": "苏*"}
	if r := e.do("POST", "/internal/ride/orders/"+id+"/force-end", "internal:cs", map[string]any{"reason": "无法锁车", "actor": actor}); r.status != 400 || r.body["code"] != "INVALID" {
		t.Fatalf("缺幂等键：%s", r.raw)
	}
	if r := e.do("POST", "/internal/ride/orders/"+id+"/force-end", "internal:cs", map[string]any{"reason": "锁", "actor": actor}, "Idempotency-Key", "fe-0"); r.status != 400 || r.body["code"] != "REASON_REQUIRED" {
		t.Fatalf("事由太短：%s", r.raw)
	}
	e.og.set(func(f *fakeOpgw) { f.endReply = "不可达" })
	billed := time.Now().Add(-time.Second)
	r := e.do("POST", "/internal/ride/orders/"+id+"/force-end", "internal:cs", map[string]any{"reason": "车辆无法锁车，用户已离开", "actor": actor,
		"billedEndAt": billed.Format(time.RFC3339Nano), "stationId": "ST0002", "ticketId": "TK202612010033"}, "Idempotency-Key", "fe-1")
	if r.status != 200 || r.body["lockRequest"] != "不可达" {
		t.Fatalf("强制结束：%s", r.raw)
	}
	ord := r.body["order"].(map[string]any)
	if ord["status"] != "已结束" || ord["endMethod"] != "客服强制结束" || ord["forceEnd"] == nil || ord["userId"] != "U100341" {
		t.Fatalf("强制结束订单：%v", ord)
	}
	// 幂等重复返回同样结果
	r2 := e.do("POST", "/internal/ride/orders/"+id+"/force-end", "internal:cs", map[string]any{"reason": "车辆无法锁车，用户已离开", "actor": actor}, "Idempotency-Key", "fe-1")
	if r2.status != 200 || string(r2.raw) != string(r.raw) {
		t.Fatalf("幂等：%s", r2.raw)
	}
	ev := e.outbox("ride.order.ended")
	if len(ev) != 1 || ev[0]["forceEnd"].(map[string]any)["ticketId"] != "TK202612010033" || ev[0]["_actor"].(map[string]any)["id"] != "AC0301" {
		t.Fatalf("已结束事件：%v", ev)
	}
	au := e.outbox("ride.audit.recorded")
	if len(au) != 1 || au[0]["action"] != "强制结束订单" || au[0]["targetType"] != "order" || au[0]["reason"] != "车辆无法锁车，用户已离开" {
		t.Fatalf("留痕：%v", au)
	}
	if r := e.do("POST", "/internal/ride/orders/"+id+"/force-end", "internal:cs", map[string]any{"reason": "再次强制结束", "actor": actor}, "Idempotency-Key", "fe-2"); r.status != 409 || r.body["code"] != "NOT_RIDING" {
		t.Fatalf("不在骑行中：%s", r.raw)
	}
	if r := e.do("POST", "/internal/ride/orders/OD2099010100001/force-end", "internal:cs", map[string]any{"reason": "再次强制结束", "actor": actor}, "Idempotency-Key", "fe-3"); r.status != 404 {
		t.Fatalf("找不到：%s", r.raw)
	}
}

func TestStaleSweepAndLockFact(t *testing.T) {
	e := newEnv(t)
	id := e.unlockOK("U100341", "SF86020226")
	e.clock.advance(73 * time.Hour)
	e.svc.SweepStale(context.Background())
	if o := e.order(id); o.Status != StManual {
		t.Fatalf("超过补报窗口应待人工处理：%s", o.Status)
	}
	pm := e.outbox("ride.order.pending_manual")
	if len(pm) != 1 || pm[0]["missing"].([]any)[0] != "锁车事实" {
		t.Fatalf("待人工处理事件：%v", pm)
	}
	actor := map[string]any{"kind": "人", "id": "AC0501", "name": "何*"}
	endAt := time.Now().Add(30 * time.Minute)
	r := e.do("POST", "/internal/ride/orders/"+id+"/lock-fact", "internal:ops", map[string]any{"endAt": endAt.Format(time.RFC3339Nano), "stationId": "ST0002", "reason": "运营商后台查到锁车", "actor": actor}, "Idempotency-Key", "lf-1")
	if r.status != 200 || r.body["status"] != "已结束" || r.body["inStation"] != true {
		t.Fatalf("补录：%s", r.raw)
	}
	if au := e.outbox("ride.audit.recorded"); len(au) != 1 || au[0]["action"] != "闭合订单" {
		t.Fatalf("补录留痕：%v", au)
	}
	if r := e.do("POST", "/internal/ride/orders/"+id+"/lock-fact", "internal:ops", map[string]any{"endAt": endAt.Format(time.RFC3339Nano), "reason": "运营商后台查到锁车", "actor": actor}, "Idempotency-Key", "lf-2"); r.status != 409 || r.body["code"] != "CONFLICT" {
		t.Fatalf("不是待人工处理：%s", r.raw)
	}
}

func TestStuckUnlockingSwept(t *testing.T) {
	e := newEnv(t)
	o := &Order{OrderID: "OD2026112000001", UserID: "U100341", VehicleID: "VH000004", BrandID: "BR01", PlateNo: "QH0004", OperatorID: "OP01",
		PricingVersionID: "PR0003", Status: StUnlocking, RequestAt: ts(time.Now().Add(-5 * time.Minute)), Rev: 1}
	if err := e.svc.saveOrder(context.Background(), o, true, nil); err != nil {
		t.Fatal(err)
	}
	e.svc.SweepStale(context.Background())
	if x := e.order(o.OrderID); x.Status != StFailed || *x.UnlockFailReason != ReasonNoResponse {
		t.Fatalf("卡在开锁中应判失败：%+v", x)
	}
}
