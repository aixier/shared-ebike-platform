package app

// 依赖模块的替身（任务 13）：本模块没有同步调用，依赖只有 fleet 与 ride 发布的事件。
// 这里按 contracts/events/fleet.md、ride.md 的信封与载荷构造事件，测试里直接喂给 HandleEvent，
// 或发到测试进程内的 nats-server（流 EVT_FLEET、EVT_RIDE 由替身建）。

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
)

var standinSeq atomic.Int64

func envelope(typ, key string, payload map[string]any) []byte {
	id := fmt.Sprintf("EVT-standin-%d", standinSeq.Add(1))
	b, _ := json.Marshal(map[string]any{
		"eventId": id, "type": typ, "version": 1, "occurredAt": "2026-12-01T12:00:00+08:00", "key": key,
		"actor": map[string]any{"kind": "系统", "id": "standin", "name": "替身"}, "payload": payload,
	})
	return b
}

// fleet.vehicle.listed
func fleetVehicleListed(vehicleID, plateNo, tagType, tag string) []byte {
	return envelope("fleet.vehicle.listed", vehicleID, map[string]any{
		"vehicleId": vehicleID, "brandId": "BR01", "operatorId": "OP01", "plateNo": plateNo, "govCode": "SF00000000",
		"model": "青禾 Q3 电单车", "pileTagType": tagType, "pileTag": tag, "codeStatus": "已启用", "codeEnableAt": nil,
		"listedAt": "2026-12-01T12:00:00+08:00",
	})
}

// fleet.vehicle.removed
func fleetVehicleRemoved(vehicleID string) []byte {
	return envelope("fleet.vehicle.removed", vehicleID, map[string]any{
		"vehicleId": vehicleID, "brandId": "BR01", "operatorId": "OP01", "govCode": "SF00000000", "removedAt": "2026-12-01T12:00:00+08:00",
	})
}

func orderHead(orderID, vehicleID string) map[string]any {
	return map[string]any{"orderId": orderID, "userId": "U100004", "vehicleId": vehicleID, "plateNo": "QH000000", "brandId": "BR01",
		"operatorId": "OP01", "channel": "微信", "pricingVersionId": "PR0003", "requestAt": "2026-12-01T11:00:00+08:00"}
}

// ride.order.unlocked
func rideUnlocked(orderID, vehicleID string, startStation any) []byte {
	p := orderHead(orderID, vehicleID)
	p["startAt"], p["startStationId"], p["startLocation"], p["degraded"] = "2026-12-01T11:00:05+08:00", startStation, nil, nil
	return envelope("ride.order.unlocked", orderID, p)
}

// ride.order.ended
func rideEnded(orderID, vehicleID string, startStation, endStation any, inStation bool) []byte {
	p := orderHead(orderID, vehicleID)
	for k, v := range map[string]any{
		"unlockResult": "成功", "startAt": "2026-12-01T11:00:05+08:00", "endAt": "2026-12-01T11:20:05+08:00", "durationSec": 1200,
		"distanceKm": 3.2, "startLocation": nil, "endLocation": nil, "startStationId": startStation, "endStationId": endStation,
		"endStationName": nil, "endPileId": nil, "endMethod": "用户锁车", "inStation": inStation, "inStationBasis": "车端判定",
		"distanceToStationM": nil, "dispatchPromptShown": false, "lockReportAt": "2026-12-01T11:20:05+08:00", "pileInAt": nil,
		"reportReceivedAt": "2026-12-01T11:20:06+08:00", "lateReport": false, "degraded": nil, "forceEnd": nil,
	} {
		p[k] = v
	}
	return envelope("ride.order.ended", orderID, p)
}
