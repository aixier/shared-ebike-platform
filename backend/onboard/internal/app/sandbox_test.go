package app

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"bike/onboard/internal/sign"
)

// trace: 任务 8 联调入口；operator-api.yaml 签名与运营商侧错误码

func TestSandboxSignature(t *testing.T) {
	e := newEnv(t)
	appID, secret := e.readyBR05()
	o, err := e.app.createOrder(context.Background(), "BR05", "符合性", "", "SBX10001", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(fmt.Sprintf(`{"eventId":"EVT-T-1","type":"开锁结果","orderId":"%s","plateNo":"SBX10001","occurredAt":"%s","unlock":{"result":"成功"}}`, o.ID, time.Now().Format(time.RFC3339)))
	path := "/sandbox/open/v1/ride-events"
	cases := []struct {
		name, app, key, code string
		at                   time.Time
		status               int
	}{
		{"未登记应用标识", "sbx_nobody_000000", secret, "SIGNATURE_INVALID", time.Now(), 401},
		{"签名错误", appID, "wrong", "SIGNATURE_INVALID", time.Now(), 401},
		{"时间戳过期", appID, secret, "TIMESTAMP_EXPIRED", time.Now().Add(-301 * time.Second), 401},
	}
	for _, c := range cases {
		if st, m := signedPost(e.srv.URL, path, c.app, c.key, body, c.at, sign.NewNonce()); st != c.status || m["code"] != c.code {
			t.Errorf("%s: %d %v", c.name, st, m)
		}
	}
	nonce := sign.NewNonce()
	st, m := signedPost(e.srv.URL, path, appID, secret, body, time.Now(), nonce)
	if st != 200 || m["received"] != true || m["duplicate"] != false || m["eventId"] != "EVT-T-1" {
		t.Fatalf("合法请求: %d %v", st, m)
	}
	if st, m := signedPost(e.srv.URL, path, appID, secret, body, time.Now(), nonce); st != 401 || m["code"] != "NONCE_REPLAYED" {
		t.Fatalf("随机数重放: %d %v", st, m)
	}
	// 同一 eventId 重复到达：已收到并标 duplicate
	if st, m := signedPost(e.srv.URL, path, appID, secret, body, time.Now(), sign.NewNonce()); st != 200 || m["duplicate"] != true {
		t.Fatalf("重复事件: %d %v", st, m)
	}
	// 吊销后一律拒绝
	e.call(techOP04, "POST", "/api/onboard/integrations/BR05/credential/revoke", map[string]any{"reason": "测试吊销凭证"})
	if st, m := signedPost(e.srv.URL, path, appID, secret, body, time.Now(), sign.NewNonce()); st != 403 || m["code"] != "CREDENTIAL_DISABLED" {
		t.Fatalf("吊销后: %d %v", st, m)
	}
}

func TestSandboxRideEventValidation(t *testing.T) {
	e := newEnv(t)
	appID, secret := e.readyBR05()
	o, _ := e.app.createOrder(context.Background(), "BR05", "符合性", "", "SBX10001", "", false, false)
	now := time.Now().Format(time.RFC3339)
	path := "/sandbox/open/v1/ride-events"
	for name, c := range map[string]struct {
		body   string
		status int
		code   string
	}{
		"订单不存在": {`{"eventId":"E1","type":"开锁结果","orderId":"OD2026010199999","plateNo":"SBX10001","occurredAt":"` + now + `","unlock":{"result":"成功"}}`, 404, "NOT_FOUND"},
		"车号不符":  {`{"eventId":"E2","type":"开锁结果","orderId":"` + o.ID + `","plateNo":"SBX10002","occurredAt":"` + now + `","unlock":{"result":"成功"}}`, 400, "INVALID"},
		"失败缺原因": {`{"eventId":"E3","type":"开锁结果","orderId":"` + o.ID + `","plateNo":"SBX10001","occurredAt":"` + now + `","unlock":{"result":"失败"}}`, 400, "INVALID"},
		"锁车缺位置": {`{"eventId":"E4","type":"锁车","orderId":"` + o.ID + `","plateNo":"SBX10001","occurredAt":"` + now + `","lock":{"inStation":true}}`, 400, "INVALID"},
		"类型不对":  {`{"eventId":"E5","type":"还车","orderId":"` + o.ID + `","plateNo":"SBX10001","occurredAt":"` + now + `"}`, 400, "INVALID"},
	} {
		if st, m := signedPost(e.srv.URL, path, appID, secret, []byte(c.body), time.Now(), sign.NewNonce()); st != c.status || m["code"] != c.code {
			t.Errorf("%s: %d %v", name, st, m)
		}
	}
}

// trace: 模拟支付（R-38）——锁车后按运营商的模拟支付模式记结果，不进生产资金
func TestSandboxMockPayment(t *testing.T) {
	e := newEnv(t)
	appID, secret := e.readyBR05()
	e.call(techOP04, "PUT", "/api/onboard/sandbox/payment-mode", map[string]any{"mode": "余额不足", "reason": "测试欠费路径"})
	o, _ := e.app.createOrder(context.Background(), "BR05", "符合性", "", "SBX10001", "", false, false)
	body := fmt.Sprintf(`{"eventId":"EL1","type":"锁车","orderId":"%s","plateNo":"SBX10001","occurredAt":"%s","lock":{"location":[104.06,30.65],"inStation":true,"durationSec":600,"distanceKm":1.5}}`, o.ID, time.Now().Format(time.RFC3339))
	if st, _ := signedPost(e.srv.URL, "/sandbox/open/v1/ride-events", appID, secret, []byte(body), time.Now(), sign.NewNonce()); st != 200 {
		t.Fatal(st)
	}
	var pay string
	_ = testPool.QueryRow(context.Background(), `SELECT pay_result FROM sandbox_orders WHERE order_id=$1`, o.ID).Scan(&pay)
	if pay != "模拟扣款失败（余额不足），订单转欠费" {
		t.Fatalf("模拟支付结果: %q", pay)
	}
}

// trace: M-04 联调入口——整批校验、逐行错误、不入生产清单
func TestSandboxVehicleList(t *testing.T) {
	e := newEnv(t)
	appID, secret := e.readyBR05()
	// 生产清单里已有别家车辆用了这个标识
	e.event("fleet.vehicle.listed", acAccess, map[string]any{"vehicleId": "VH000004", "brandId": "BR01", "operatorId": "OP01", "plateNo": "QH000004",
		"govCode": "SF86020226", "model": "青禾 Q1", "pileTagType": "RFID", "pileTag": "E28011TAKEN", "codeStatus": "已启用", "codeEnableAt": nil, "listedAt": time.Now()})
	path := "/sandbox/open/v1/vehicle-lists"
	post := func(rows []map[string]any) (int, map[string]any) {
		b, _ := json.Marshal(map[string]any{"rows": rows})
		return signedPost(e.srv.URL, path, appID, secret, b, time.Now(), sign.NewNonce())
	}
	st, m := post([]map[string]any{
		{"brandId": "BR05", "plateNo": "SM000101", "model": "市民 S1", "pileTag": "E28011A3F2"},
		{"brandId": "BR05", "plateNo": "SM000102", "model": "", "pileTag": "E28011A3F2"},
		{"brandId": "BR05", "plateNo": "SM000103", "model": "市民 S1", "pileTag": "E28011TAKEN"},
		{"brandId": "BR05", "plateNo": "SM000104", "model": "市民 S1", "pileTag": "E28011C0000004", "pileTagType": "红外"},
	})
	if st != 400 || m["code"] != "VEHICLE_LIST_INVALID" {
		t.Fatalf("有错清单: %d %v", st, m)
	}
	errs := m["errors"].([]any)
	if len(errs) != 4 {
		t.Fatalf("应逐行给出 4 处错误（缺车型、标识重复、识别方式、标识被占用）: %v", errs)
	}
	for _, x := range errs {
		if _, has := x.(map[string]any)["rule"]; has {
			t.Fatal("内部归类字段不应返回给运营商")
		}
	}
	if st, m := post([]map[string]any{{"brandId": "BR01", "plateNo": "X", "model": "m", "pileTag": "t"}}); st != 403 || m["code"] != "SCOPE_DENIED" {
		t.Fatalf("别家品牌: %d %v", st, m)
	}
	st, m = post([]map[string]any{{"brandId": "BR05", "plateNo": "SM000101", "model": "市民 S1", "pileTag": "E28011B0000001"}})
	if st != 200 || m["accepted"].(float64) != 1 {
		t.Fatalf("正确清单: %d %v", st, m)
	}
	var n int
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM fleet_vehicles WHERE brand_id='BR05'`).Scan(&n)
	if n != 0 {
		t.Fatal("联调清单不得进生产清单投影")
	}
}
