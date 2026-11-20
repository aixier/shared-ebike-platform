package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
)

// 测试数据取自原型 mockdata（operators.json、vehicles.json，SCHEMA.md 第 7 节）：
//   VH000004 青禾 SF86020226 已启用（有效政府码）
//   VH001061 简途 SF71009215 已失效（运营商已退出）
//   VH001151 市民骑行 SF27239795 待启用（联调中）
// 以 fleet 事件信封的形式给出，等同 fleet 的替身。

type evt struct {
	ID, Type, Key, At string
	Payload           map[string]any
}

func (v evt) envelope() []byte {
	b, _ := json.Marshal(map[string]any{
		"eventId": v.ID, "type": v.Type, "version": 1, "occurredAt": v.At, "key": v.Key,
		"actor": map[string]any{"kind": "系统", "id": "fleet", "name": nil}, "payload": v.Payload,
	})
	return b
}

func brandRegistered(id, brandID, opID, name, opName, opShort, access string) evt {
	return evt{ID: id, Type: "fleet.brand.registered", Key: brandID, At: "2026-07-01T09:00:00+08:00", Payload: map[string]any{
		"brandId": brandID, "operatorId": opID, "name": name, "displayName": "【" + name + "】电单车",
		"operatorName": opName, "operatorShortName": opShort, "operatorType": "运营商", "vehicleType": "电单车",
		"color": "#000000", "accessStatus": access,
	}}
}

func vehicleListed(id, vid, brandID, opID, plate, code, model, status string, enableAt any, listedAt string) evt {
	return evt{ID: id, Type: "fleet.vehicle.listed", Key: vid, At: listedAt, Payload: map[string]any{
		"vehicleId": vid, "brandId": brandID, "operatorId": opID, "plateNo": plate, "govCode": code, "model": model,
		"pileTagType": "RFID", "pileTag": "E280", "codeStatus": status, "codeEnableAt": enableAt, "listedAt": listedAt,
	}}
}

func codeChanged(id, vid, brandID, code, from, to, at string) evt {
	return evt{ID: id, Type: "fleet.code.status_changed", Key: vid, At: at, Payload: map[string]any{
		"vehicleId": vid, "brandId": brandID, "govCode": code, "from": from, "to": to, "changedAt": at,
	}}
}

func accessChanged(id, brandID, opID, from, to, at string) evt {
	return evt{ID: id, Type: "fleet.brand.access_changed", Key: brandID, At: at, Payload: map[string]any{
		"brandId": brandID, "operatorId": opID, "from": from, "to": to, "changedAt": at, "reason": nil,
	}}
}

func operatorExited(id, opID string, brands []string, at string) evt {
	return evt{ID: id, Type: "fleet.operator.exited", Key: opID, At: at, Payload: map[string]any{
		"operatorId": opID, "brandIds": brands, "exitedAt": at, "reason": "线下处置完成",
	}}
}

func vehicleRemoved(id, vid, brandID, opID, code, at string) evt {
	return evt{ID: id, Type: "fleet.vehicle.removed", Key: vid, At: at, Payload: map[string]any{
		"vehicleId": vid, "brandId": brandID, "operatorId": opID, "govCode": code, "removedAt": at,
	}}
}

func prototypeFleetEvents() []evt {
	return []evt{
		brandRegistered("e1", "BR01", "OP01", "青禾", "青禾出行科技有限公司", "青禾出行", "已接入"),
		brandRegistered("e2", "BR04", "OP03", "简途", "简途共享出行有限公司", "简途共享", "已接入"),
		brandRegistered("e3", "BR05", "OP04", "市民骑行", "示范市公共交通集团有限公司", "公交集团车队", "联调中"),
		brandRegistered("e4", "BR06", "OP05", "途迈", "途迈出行科技有限公司", "途迈出行", "未接入"),
		vehicleListed("e5", "VH000004", "BR01", "OP01", "QH464022", "SF86020226", "青禾 Q3 电单车", "已启用", nil, "2026-07-08T14:20:11+08:00"),
		vehicleListed("e6", "VH001061", "BR04", "OP03", "JT542568", "SF71009215", "简途 J1 电单车", "已启用", nil, "2026-07-03T16:41:09+08:00"),
		vehicleListed("e7", "VH001151", "BR05", "OP04", "GJ459524", "SF27239795", "公交 G1 电单车", "待启用", "2026-12-08T09:00:00+08:00", "2026-11-14T15:02:37+08:00"),
		accessChanged("e8", "BR04", "OP03", "已接入", "已退出", "2026-11-20T10:00:00+08:00"),
		operatorExited("e9", "OP03", []string{"BR04"}, "2026-11-20T10:00:00+08:00"),
		codeChanged("e10", "VH001061", "BR04", "SF71009215", "已启用", "已失效", "2026-11-20T10:00:00+08:00"),
	}
}

// seed 直接把事件交给事件处理入口（与总线消费走同一段代码）
func (e *env) seed(evs ...evt) {
	e.t.Helper()
	for _, v := range evs {
		if err := e.app.HandleEvent(context.Background(), v.envelope()); err != nil {
			e.t.Fatalf("处理事件 %s 失败：%v", v.ID, err)
		}
	}
}

func (e *env) seedPrototype() { e.seed(prototypeFleetEvents()...) }

// fleet 替身的流（由发布方 fleet 建，这里替它建）
func fleetStreamConfig() jetstream.StreamConfig {
	return jetstream.StreamConfig{Name: "EVT_FLEET", Subjects: []string{"fleet.>"}, Storage: jetstream.FileStorage}
}

func TestFixturesLoad(t *testing.T) {
	e := newEnv(t)
	e.seedPrototype()
	if n := e.count("SELECT count(*) FROM vehicles"); n != 3 {
		t.Fatalf("车辆投影应有 3 条，得到 %d", n)
	}
}
