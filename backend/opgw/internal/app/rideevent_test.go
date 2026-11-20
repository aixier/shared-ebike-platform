package app

import (
	"context"
	"testing"
	"time"
)

func unlockEvent(id, order, plate string) map[string]any {
	return map[string]any{"eventId": id, "type": "开锁结果", "orderId": order, "plateNo": plate, "occurredAt": "2026-12-01T12:00:00+08:00", "unlock": map[string]any{"result": "成功"}}
}

func lockEvent(id, order, plate string) map[string]any {
	return map[string]any{"eventId": id, "type": "锁车", "orderId": order, "plateNo": plate, "occurredAt": "2026-12-01T12:18:20+08:00",
		"lock": map[string]any{"location": []float64{104.0657, 30.6595}, "inStation": true, "durationSec": 1100, "distanceKm": 3.2}}
}

// 任务 9：M-03 骑行事件入口

func TestRideEventDeliveredOnce(t *testing.T) {
	e := newEnv(t, nil)
	e.seedOperators()
	e.unlockOK("OD2026120100001", "OP01", "BR01", "QH000004")
	st, m := e.signed("OP01", "/open/v1/ride-events", unlockEvent("EVT-TEST-0001", "OD2026120100001", "QH000004"), nil)
	if st != 200 || m["received"] != true || m["duplicate"] != false || m["eventId"] != "EVT-TEST-0001" {
		t.Fatalf("%d %v", st, m)
	}
	st, m2 := e.signed("OP01", "/open/v1/ride-events", unlockEvent("EVT-TEST-0001", "OD2026120100001", "QH000004"), nil)
	if st != 200 || m2["duplicate"] != true || m2["receivedAt"] != m["receivedAt"] {
		t.Fatalf("重复到达：%v", m2)
	}
	if len(e.rideGot) != 1 {
		t.Fatalf("只送达 ride 一次，得 %d", len(e.rideGot))
	}
	d := e.rideGot[0]
	if d["operatorEventId"] != "EVT-TEST-0001" || d["orderId"] != "OD2026120100001" || d["brandId"] != "BR01" || d["operatorId"] != "OP01" || d["kind"] != "开锁结果" || d["late"] != false {
		t.Fatalf("送达报文：%v", d)
	}
	u := d["unlock"].(map[string]any)
	if u["result"] != "成功" || u["at"] != "2026-12-01T12:00:00+08:00" || d["lock"] != nil {
		t.Fatalf("%v", d)
	}
}

func TestRideEventLockManualAndForced(t *testing.T) {
	e := newEnv(t, nil)
	e.seedOperators()
	e.unlockOK("OD2026120100001", "OP01", "BR01", "QH000004")
	e.advance(20 * time.Minute)
	// 未发结束骑行就锁车：用户手动锁车
	if st, m := e.signed("OP01", "/open/v1/ride-events", lockEvent("EVT-TEST-0003", "OD2026120100001", "QH000004"), nil); st != 200 {
		t.Fatalf("%d %v", st, m)
	}
	l := e.rideGot[0]["lock"].(map[string]any)
	if l["manual"] != true || l["vehicleSideInStation"] != true || l["durationSec"].(float64) != 1100 || l["distanceKm"].(float64) != 3.2 || len(l["location"].([]any)) != 2 {
		t.Fatalf("%v", l)
	}
	// 平台发过结束骑行之后的锁车不是手动
	e.unlockOK("OD2026120100002", "OP01", "BR01", "QH000005")
	e.internal("/internal/opgw/end-ride", "end2", map[string]any{"orderId": "OD2026120100002", "operatorId": "OP01", "brandId": "BR01", "plateNo": "QH000005", "cause": "用户点还车"})
	e.signed("OP01", "/open/v1/ride-events", lockEvent("EVT-L-2", "OD2026120100002", "QH000005"), nil)
	if e.rideGot[1]["lock"].(map[string]any)["manual"] != false {
		t.Fatal("平台结束的锁车 manual=false")
	}
}

func TestRideEventFailReasonAndLate(t *testing.T) {
	e := newEnv(t, nil)
	e.seedOperators()
	e.unlockOK("OD2026120100002", "OP01", "BR01", "QH000015")
	ev := map[string]any{"eventId": "EVT-TEST-0002", "type": "开锁结果", "orderId": "OD2026120100002", "plateNo": "QH000015", "occurredAt": "2026-12-01T12:00:03+08:00", "unlock": map[string]any{"result": "失败", "failReason": "电量不足"}}
	if st, _ := e.signed("OP01", "/open/v1/ride-events", ev, nil); st != 200 {
		t.Fatal(st)
	}
	if e.rideGot[0]["unlock"].(map[string]any)["reason"] != "电量不足" {
		t.Fatalf("%v", e.rideGot[0])
	}
	// 超时后才报告开锁成功（BR-03）：时刻早于收到 30 秒以上，标补报
	e.unlockOK("OD2026091213579", "OP01", "BR01", "QH000020")
	e.advance(2 * time.Minute)
	ev2 := unlockEvent("EVT-LATE-1", "OD2026091213579", "QH000020")
	if st, _ := e.signed("OP01", "/open/v1/ride-events", ev2, nil); st != 200 {
		t.Fatal(st)
	}
	if e.rideGot[1]["late"] != true {
		t.Fatal("晚到应标 late")
	}
	// 车辆无响应：原样转送
	e.unlockOK("OD2026120100003", "OP01", "BR01", "QH000021")
	ev3 := map[string]any{"eventId": "EVT-NR", "type": "开锁结果", "orderId": "OD2026120100003", "plateNo": "QH000021", "occurredAt": "2026-12-01T12:02:00+08:00", "unlock": map[string]any{"result": "失败", "failReason": "车辆无响应"}}
	if st, _ := e.signed("OP01", "/open/v1/ride-events", ev3, nil); st != 200 {
		t.Fatal(st)
	}
	if e.rideGot[2]["unlock"].(map[string]any)["reason"] != "车辆无响应" {
		t.Fatal("车辆无响应应原样转送给 ride")
	}
}

func TestRideEventBackfillWindow(t *testing.T) {
	e := newEnv(t, nil)
	e.seedOperators()
	e.unlockOK("OD2026120100001", "OP01", "BR01", "QH000004")
	e.advance(71 * time.Hour)
	if st, m := e.signed("OP01", "/open/v1/ride-events", lockEvent("EVT-B1", "OD2026120100001", "QH000004"), nil); st != 200 || e.rideGot[0]["late"] != true {
		t.Fatalf("窗口内补报照收：%d %v", st, m)
	}
	// 平台参数把补报窗口改为 24 小时
	e.event("R1", "rule.params.changed", map[string]any{"backfillWindowHours": 24, "unlockReportTimeoutSec": 30})
	if st, m := e.signed("OP01", "/open/v1/ride-events", lockEvent("EVT-B2", "OD2026120100001", "QH000004"), nil); st != 400 || m["code"] != "INVALID" {
		t.Fatalf("超出窗口：%d %v", st, m)
	}
}

func TestRideEventRejections(t *testing.T) {
	e := newEnv(t, nil)
	e.seedOperators()
	e.unlockOK("OD2026120100001", "OP01", "BR01", "QH000004")
	cases := []struct {
		name string
		op   string
		body map[string]any
		st   int
		code string
	}{
		{"订单不存在", "OP01", unlockEvent("E1", "OD2026120199999", "QH000004"), 404, "NOT_FOUND"},
		{"别家订单", "OP04", unlockEvent("E2", "OD2026120100001", "QH000004"), 403, "SCOPE_DENIED"},
		{"车号不符", "OP01", unlockEvent("E3", "OD2026120100001", "QH000009"), 400, "INVALID"},
		{"缺类型", "OP01", map[string]any{"eventId": "E4", "orderId": "OD2026120100001", "plateNo": "QH000004", "occurredAt": "2026-12-01T12:00:00+08:00"}, 400, "INVALID"},
		{"失败缺原因", "OP01", map[string]any{"eventId": "E5", "type": "开锁结果", "orderId": "OD2026120100001", "plateNo": "QH000004", "occurredAt": "2026-12-01T12:00:00+08:00", "unlock": map[string]any{"result": "失败"}}, 400, "INVALID"},
		{"锁车缺在点", "OP01", map[string]any{"eventId": "E6", "type": "锁车", "orderId": "OD2026120100001", "plateNo": "QH000004", "occurredAt": "2026-12-01T12:00:00+08:00", "lock": map[string]any{"location": []float64{1, 2}}}, 400, "INVALID"},
		{"时刻无时区", "OP01", map[string]any{"eventId": "E7", "type": "开锁结果", "orderId": "OD2026120100001", "plateNo": "QH000004", "occurredAt": "2026-12-01 12:00:00", "unlock": map[string]any{"result": "成功"}}, 400, "INVALID"},
	}
	for _, c := range cases {
		st, m := e.signed(c.op, "/open/v1/ride-events", c.body, nil)
		if st != c.st || m["code"] != c.code {
			t.Errorf("%s：%d %v", c.name, st, m)
		}
	}
	if len(e.rideGot) != 0 {
		t.Fatal("被拒的不送达")
	}
}

func TestRideEventRetryUntilDelivered(t *testing.T) {
	e := newEnv(t, nil)
	e.seedOperators()
	e.unlockOK("OD2026120100001", "OP01", "BR01", "QH000004")
	e.rideStatus = 503
	st, m := e.signed("OP01", "/open/v1/ride-events", unlockEvent("EVT-R", "OD2026120100001", "QH000004"), nil)
	if st != 200 || m["received"] != true {
		t.Fatalf("ride 不可用也确认收到：%d %v", st, m)
	}
	ctx := context.Background()
	if n, _ := e.a.RetryDeliveries(ctx); n != 0 {
		t.Fatal("退避期内不重试")
	}
	e.advance(11 * time.Second)
	if n, _ := e.a.RetryDeliveries(ctx); n != 0 {
		t.Fatal("ride 仍不可用")
	}
	var attempts int
	var lastErr *string
	_ = testPool.QueryRow(ctx, `SELECT attempts, last_error FROM ride_events`).Scan(&attempts, &lastErr)
	if attempts != 2 || lastErr == nil {
		t.Fatal(attempts)
	}
	e.rideStatus = 200
	e.advance(10 * time.Second)
	if n, err := e.a.RetryDeliveries(ctx); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	if n, _ := e.a.RetryDeliveries(ctx); n != 0 {
		t.Fatal("送达后不再送")
	}
	if len(e.rideGot) != 1 || e.rideGot[0]["operatorEventId"] != "EVT-R" {
		t.Fatalf("%v", e.rideGot)
	}
}
