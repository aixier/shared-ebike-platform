package app

import (
	"context"
	"testing"
	"time"
)

func TestProjectionFromFleetEvents(t *testing.T) {
	e := newEnv(t)
	e.seedPrototype()
	ctx := context.Background()

	var status string
	var invalidAt *time.Time
	if err := e.pool.QueryRow(ctx, "SELECT code_status, code_invalid_at FROM vehicles WHERE vehicle_id='VH001061'").Scan(&status, &invalidAt); err != nil {
		t.Fatal(err)
	}
	if status != "已失效" || invalidAt == nil {
		t.Fatalf("码状态变更未落投影：%s %v", status, invalidAt)
	}

	var access string
	if err := e.pool.QueryRow(ctx, "SELECT access_status FROM brands WHERE brand_id='BR04'").Scan(&access); err != nil || access != "已退出" {
		t.Fatalf("接入状态未落投影：%s %v", access, err)
	}
	var opStatus, opShort string
	if err := e.pool.QueryRow(ctx, "SELECT status, short_name FROM operators WHERE operator_id='OP03'").Scan(&opStatus, &opShort); err != nil || opStatus != "已退出" || opShort != "简途共享" {
		t.Fatalf("运营商投影不对：%s %s %v", opStatus, opShort, err)
	}
}

// 至少送达一次：同一 eventId 重复到达只处理一次
func TestDuplicateEventIsIgnored(t *testing.T) {
	e := newEnv(t)
	e.seedPrototype()
	// 重投最早的"车辆已入清单"（码状态为已启用），不得把已失效改回去
	e.seed(prototypeFleetEvents()[5])
	if n := e.count("SELECT count(*) FROM vehicles WHERE vehicle_id='VH001061' AND code_status='已失效'"); n != 1 {
		t.Fatal("重复事件被再次处理")
	}
	if n := e.count("SELECT count(*) FROM processed_events"); n != 10 {
		t.Fatalf("去重表应有 10 条，得到 %d", n)
	}
}

// 车辆移出清单后码随之失效：保留投影并标记已失效，扫码显示"码已失效"
func TestVehicleRemovedKeepsCodeAsInvalid(t *testing.T) {
	e := newEnv(t)
	e.seedPrototype()
	e.seed(vehicleRemoved("e11", "VH000004", "BR01", "OP01", "SF86020226", "2026-11-30T08:00:00+08:00"))
	if n := e.count("SELECT count(*) FROM vehicles WHERE vehicle_id='VH000004' AND removed AND code_status='已失效' AND code_invalid_at IS NOT NULL"); n != 1 {
		t.Fatal("移除车辆后投影不对")
	}
}

// 车辆先于品牌到达（不同 key 不保序）也能落库
func TestVehicleBeforeBrand(t *testing.T) {
	e := newEnv(t)
	e.seed(vehicleListed("x1", "VH000707", "BR03", "OP02", "CL000707", "SF11112222", "驰路", "已启用", nil, "2026-08-01T10:00:00+08:00"))
	e.seed(brandRegistered("x2", "BR03", "OP02", "驰路", "驰路智行科技有限公司", "驰路智行", "已接入"))
	if n := e.count("SELECT count(*) FROM vehicles v JOIN brands b USING (brand_id) WHERE v.vehicle_id='VH000707'"); n != 1 {
		t.Fatal("乱序事件未落库")
	}
}

// 未订阅的事件类型确认后忽略，不报错
func TestUnknownEventTypeIgnored(t *testing.T) {
	e := newEnv(t)
	e.seed(evt{ID: "u1", Type: "fleet.something.else", Key: "k", At: "2026-12-01T12:00:00+08:00", Payload: map[string]any{}})
	if err := e.app.HandleEvent(context.Background(), []byte("not json")); err == nil {
		t.Fatal("坏消息应报错")
	}
}

// 经事件总线消费：持久消费者名为 enforce，按主题过滤
func TestConsumeFleetEventsFromBus(t *testing.T) {
	e := newEnv(t)
	_, js := connectJS(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// fleet 的替身：建它的流并发布事件
	if _, err := js.CreateStream(ctx, fleetStreamConfig()); err != nil {
		t.Fatal(err)
	}
	go func() { _ = e.app.RunConsumer(ctx, js) }()

	for _, v := range prototypeFleetEvents() {
		if _, err := js.Publish(ctx, v.Type, v.envelope()); err != nil {
			t.Fatal(err)
		}
	}
	// 重复发同一事件（编号相同）
	dup := prototypeFleetEvents()[5]
	if _, err := js.Publish(ctx, dup.Type, dup.envelope()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "投影建好", func() bool {
		return e.count("SELECT count(*) FROM processed_events") == 10
	})
	if n := e.count("SELECT count(*) FROM vehicles WHERE vehicle_id='VH001061' AND code_status='已失效'"); n != 1 {
		t.Fatal("经总线消费后投影不对")
	}
	info, err := js.Consumer(ctx, "EVT_FLEET", "enforce")
	if err != nil {
		t.Fatalf("应建持久消费者 enforce：%v", err)
	}
	if info.CachedInfo().Config.Durable != "enforce" {
		t.Fatal("消费者名不对")
	}
}
