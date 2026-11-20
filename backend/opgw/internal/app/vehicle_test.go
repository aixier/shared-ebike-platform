package app

import (
	"net/http"
	"testing"
)

// 任务 10：M-04 车辆清单入口

func vehicleRows(rows ...[]string) map[string]any {
	var out []map[string]any
	for _, r := range rows {
		out = append(out, map[string]any{"brandId": r[0], "plateNo": r[1], "model": "QH-E2", "pileTag": r[2], "pileTagType": "RFID"})
	}
	return map[string]any{"rows": out}
}

func TestVehicleListAccepted(t *testing.T) {
	e := newEnv(t, nil)
	e.seedOperators()
	e.fleetFn = func(w http.ResponseWriter, body map[string]any) {
		_, _ = w.Write([]byte(`{"added":1,"vehicles":[{"vehicleId":"VH000004","brandId":"BR01","operatorId":"OP01","plateNo":"QH000004","govCode":"SF86020226","pileTag":"E2000017","codeStatus":"已启用","rentable":true}]}`))
	}
	st, m := e.signed("OP01", "/open/v1/vehicle-lists", vehicleRows([]string{"BR01", "QH000004", "E2000017"}), map[string]string{"Idempotency-Key": "vl-1"})
	if st != 200 || m["accepted"].(float64) != 1 {
		t.Fatalf("%d %v", st, m)
	}
	v := m["vehicles"].([]any)[0].(map[string]any)
	if v["govCode"] != "SF86020226" || v["vehicleId"] != "VH000004" || v["codeStatus"] != "已启用" || len(v) != 4 {
		t.Fatalf("%v", v)
	}
	got := e.fleetGot[0]
	if got["operatorId"] != "OP01" || got["actor"].(map[string]any)["kind"] != "系统" || len(got["rows"].([]any)) != 1 {
		t.Fatalf("转交 fleet：%v", got)
	}
	// 同一幂等键重复到达不再转交
	st, m2 := e.signed("OP01", "/open/v1/vehicle-lists", vehicleRows([]string{"BR01", "QH000004", "E2000017"}), map[string]string{"Idempotency-Key": "vl-1"})
	if st != 200 || len(e.fleetGot) != 1 || m2["accepted"].(float64) != 1 {
		t.Fatalf("幂等：%d %d", st, len(e.fleetGot))
	}
}

func TestVehicleListRowErrorsPassThrough(t *testing.T) {
	e := newEnv(t, nil)
	e.seedOperators()
	body := `{"code":"VEHICLE_LIST_INVALID","message":"清单有 2 处错误，整批未入库","errors":[{"row":1,"field":"plateNo","message":"车号格式不对"},{"row":2,"field":"pileTag","message":"车辆标识重复"}]}`
	e.fleetFn = func(w http.ResponseWriter, _ map[string]any) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(body))
	}
	st, m := e.signed("OP01", "/open/v1/vehicle-lists", vehicleRows([]string{"BR01", "Q1", "A"}, []string{"BR02", "MM000001", "A"}), nil)
	if st != 400 || m["code"] != "VEHICLE_LIST_INVALID" || len(m["errors"].([]any)) != 2 {
		t.Fatalf("%d %v", st, m)
	}
	if m["errors"].([]any)[1].(map[string]any)["field"] != "pileTag" {
		t.Fatal("逐行错误原样返回")
	}
}

func TestVehicleListScopeAndValidation(t *testing.T) {
	e := newEnv(t, nil)
	e.seedOperators()
	if st, m := e.signed("OP01", "/open/v1/vehicle-lists", vehicleRows([]string{"BR03", "CL000001", "X"}), nil); st != 403 || m["code"] != "SCOPE_DENIED" {
		t.Fatalf("别家品牌：%d %v", st, m)
	}
	if st, m := e.signed("OP01", "/open/v1/vehicle-lists", vehicleRows([]string{"BR99", "ZZ000001", "X"}), nil); st != 403 || m["code"] != "SCOPE_DENIED" {
		t.Fatalf("未知品牌：%d %v", st, m)
	}
	if st, m := e.signed("OP01", "/open/v1/vehicle-lists", map[string]any{"rows": []any{}}, nil); st != 400 || m["code"] != "INVALID" {
		t.Fatalf("空清单：%d %v", st, m)
	}
	if st, _ := e.signed("OP01", "/open/v1/vehicle-lists", []byte(`not json`), nil); st != 400 {
		t.Fatal(st)
	}
	if len(e.fleetGot) != 0 {
		t.Fatal("被拒的不转交")
	}
	// fleet 不可用
	e.fleetSrv.Close()
	if st, m := e.signed("OP01", "/open/v1/vehicle-lists", vehicleRows([]string{"BR01", "QH000004", "X"}), nil); st != 500 || m["code"] != "SERVER_BUSY" {
		t.Fatalf("%d %v", st, m)
	}
}

func TestVehicleListProviderOneOperatorPerBatch(t *testing.T) {
	e := newEnv(t, nil)
	e.accessChanged("P1", "BR03", "OP02", "已接入")
	e.accessChanged("P2", "BR07", "OP06", "已接入")
	if st, m := e.signed("OP02", "/open/v1/vehicle-lists", vehicleRows([]string{"BR03", "CL000001", "X"}, []string{"BR07", "XX000001", "Y"}), nil); st != 400 || m["code"] != "INVALID" {
		t.Fatalf("%d %v", st, m)
	}
	if st, _ := e.signed("OP02", "/open/v1/vehicle-lists", vehicleRows([]string{"BR07", "XX000001", "Y"}), nil); st != 200 {
		t.Fatal(st)
	}
	if e.fleetGot[0]["operatorId"] != "OP06" {
		t.Fatalf("按品牌找到名下运营商：%v", e.fleetGot[0])
	}
}
