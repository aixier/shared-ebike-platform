package app

import (
	"context"
	"testing"
	"time"
)

// trace: README 订阅事件——品牌已登记、车辆已入清单、品牌接入状态变更、运营商已退出、平台参数变更

func TestBrandRegisteredCreatesIntegration(t *testing.T) {
	e := newEnv(t)
	e.registerBR05()
	st, _, raw := e.call(techOP04, "GET", "/api/onboard/integrations", nil)
	if st != 200 {
		t.Fatalf("%d %s", st, raw)
	}
	var list []IntegrationView
	mustJSON(t, raw, &list)
	if len(list) != 1 {
		t.Fatalf("应有 1 条接入记录: %s", raw)
	}
	in := list[0]
	if in.IntegrationID != "IN-BR05" || in.OperatorID != "OP04" || in.Status != "未接入" || in.AccessVia != "自行接入" || in.TargetDays != 21 {
		t.Fatalf("接入记录不对: %+v", in.Integration)
	}
	if in.Credentials.Status != "有效" || in.Credentials.SandboxAppID == "" || in.Credentials.SandboxKeyMasked[:7] != "sk_****" {
		t.Fatalf("凭证不对: %+v", in.Credentials)
	}
	if len(in.StatusHistory) != 1 || in.StatusHistory[0].By != "AC0404" || in.LatestRun != nil || in.Acceptance == nil || in.Acceptance.Result != "未开始" {
		t.Fatalf("历史或验收不对: %+v", in)
	}
	// 别家运营商看不到
	_, _, raw = e.call(techOP01, "GET", "/api/onboard/integrations", nil)
	if string(raw) != "[]\n" {
		t.Fatalf("OP01 不应看到 OP04 的接入记录: %s", raw)
	}
}

func TestAccessChangedAndVehicles(t *testing.T) {
	e := newEnv(t)
	e.registerBR05()
	reason := "接入负责人发放联调凭证"
	e.event("fleet.brand.access_changed", Actor{Kind: "人", ID: "AC0403"}, map[string]any{"brandId": "BR05", "operatorId": "OP04", "from": "未接入", "to": "联调中", "changedAt": "2026-11-10T09:30:00+08:00", "reason": reason})
	// 原型样例：VH001151 市民骑行 SF27239795 码未启用（联调中）
	enable := "2026-12-08T09:00:00+08:00"
	e.event("fleet.vehicle.listed", Actor{Kind: "人", ID: "AC0114"}, map[string]any{"vehicleId": "VH001151", "brandId": "BR05", "operatorId": "OP04", "plateNo": "SM000001",
		"govCode": "SF27239795", "model": "市民 S1", "pileTagType": "RFID", "pileTag": "E28011B05000000000000001", "codeStatus": "待启用", "codeEnableAt": enable, "listedAt": "2026-11-14T15:02:37+08:00"})
	e.event("fleet.vehicle.listed", Actor{Kind: "人", ID: "AC0114"}, map[string]any{"vehicleId": "VH001152", "brandId": "BR05", "operatorId": "OP04", "plateNo": "SM000002",
		"govCode": "SF27239796", "model": "市民 S1", "pileTagType": "RFID", "pileTag": "E28011B05000000000000002", "codeStatus": "待启用", "codeEnableAt": enable, "listedAt": "2026-11-14T15:02:37+08:00"})
	in := e.integ(techOP04, "BR05")
	if in.Status != "联调中" || len(in.StatusHistory) != 2 || *in.StatusHistory[1].Note != reason {
		t.Fatalf("状态投影不对: %+v", in.StatusHistory)
	}
	vl := in.VehicleList
	if vl == nil || vl.VehicleCount != 2 || vl.CodesIssued != 2 || *vl.SubmittedBy != "AC0114" || *vl.CodeStatus != "待启用（计划 2026-12-08 09:00 随上线启用）" {
		t.Fatalf("清单摘要不对: %+v", vl)
	}
	e.event("fleet.vehicle.removed", acAccess, map[string]any{"vehicleId": "VH001152", "brandId": "BR05", "operatorId": "OP04", "govCode": "SF27239796", "removedAt": time.Now()})
	if in := e.integ(techOP04, "BR05"); in.VehicleList.VehicleCount != 1 {
		t.Fatalf("移除后应剩 1 辆: %+v", in.VehicleList)
	}
}

// trace: README 不变量——运营商退出即停用联调凭证
func TestOperatorExitedDisablesCredential(t *testing.T) {
	e := newEnv(t)
	e.registerBR05()
	e.event("fleet.operator.exited", Actor{Kind: "人", ID: "AC0001"}, map[string]any{"operatorId": "OP04", "brandIds": []string{"BR05"}, "exitedAt": time.Now(), "reason": "按线下结果设置退出"})
	in := e.integ(techOP04, "BR05")
	if in.Credentials.Status != "停用" || in.Credentials.DisabledAt == nil || *in.Credentials.DisabledBy != "AC0001" {
		t.Fatalf("凭证应停用: %+v", in.Credentials)
	}
	if a := lastAudit(t); a["actor"].(map[string]any)["kind"] != "系统" || a["payload"].(map[string]any)["targetType"] != "credential" {
		t.Fatalf("应留痕: %v", a)
	}
}

func TestParamsChanged(t *testing.T) {
	e := newEnv(t)
	e.event("rule.params.changed", acAccess, map[string]any{"unlockReportTimeoutSec": 25, "backfillWindowHours": 48, "acceptance": map[string]any{"realVehicles": 10, "realOrders": 30, "targetDays": 14}})
	p, err := e.app.params(context.Background())
	if err != nil || p.RealVehicles != 10 || p.RealOrders != 30 || p.TargetDays != 14 || p.UnlockTimeout != 25*time.Second || p.BackfillWindowHours != 48 {
		t.Fatalf("参数投影不对: %+v %v", p, err)
	}
}
