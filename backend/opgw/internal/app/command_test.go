package app

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"bike/opgw/internal/sign"
)

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func unlockBody(order, op, brand, plate string, degraded bool) map[string]any {
	return map[string]any{"orderId": order, "operatorId": op, "brandId": brand, "vehicleId": "VH000004", "plateNo": plate, "degraded": degraded}
}

// unlockOK 以 ride 身份请求开锁并断言已受理。
func (e *env) unlockOK(order, op, brand, plate string) {
	e.t.Helper()
	st, m := e.internal("/internal/opgw/unlock", "u-"+order, unlockBody(order, op, brand, plate, false))
	if st != 200 || m["result"] != "已受理" {
		e.t.Fatalf("开锁未受理：%d %v", st, m)
	}
}

// 任务 7：M-01 开锁

func TestUnlockAccepted(t *testing.T) {
	e := newEnv(t, nil)
	e.seedOperators()
	c, secret := e.cred("OP01")
	var got map[string]any
	var hdr http.Header
	e.opHandler = func(w http.ResponseWriter, r *http.Request, body []byte) {
		hdr = r.Header.Clone()
		if r.URL.Path != "/v1/unlock" || !sign.Verify(secret, "POST", "/v1/unlock", r.Header.Get("X-App-Id"), r.Header.Get("X-Timestamp"), r.Header.Get("X-Nonce"), body, r.Header.Get("X-Signature")) {
			w.WriteHeader(401)
			return
		}
		_ = json.Unmarshal(body, &got)
		_, _ = w.Write([]byte(`{"result":"已受理"}`))
	}
	st, m := e.internal("/internal/opgw/unlock", "k1", unlockBody("OD2026120111001", "OP01", "BR01", "QH000004", true))
	if st != 200 || m["result"] != "已受理" || m["rejectReason"] != nil || m["at"] == nil {
		t.Fatalf("%d %v", st, m)
	}
	if hdr.Get("X-App-Id") != c.AppID || hdr.Get("Idempotency-Key") == "" {
		t.Fatal("请求头不全")
	}
	// 运营商只拿到订单号、车号、降级标记、请求时刻（NFR-11）
	if len(got) != 4 || got["orderId"] != "OD2026120111001" || got["plateNo"] != "QH000004" || got["degraded"] != true || got["requestAt"] == nil {
		t.Fatalf("报文：%v", got)
	}
}

func TestUnlockSameOrderNotRepeated(t *testing.T) {
	e := newEnv(t, nil)
	e.seedOperators()
	e.unlockOK("OD2026120111001", "OP01", "BR01", "QH000004")
	// 同一幂等键
	st, m := e.internal("/internal/opgw/unlock", "u-OD2026120111001", unlockBody("OD2026120111001", "OP01", "BR01", "QH000004", false))
	// 不同幂等键、同一订单
	st2, m2 := e.internal("/internal/opgw/unlock", "other", unlockBody("OD2026120111001", "OP01", "BR01", "QH000004", false))
	if st != 200 || st2 != 200 || m["result"] != "已受理" || m2["result"] != "已受理" || m2["at"] != m["at"] {
		t.Fatalf("%v %v", m, m2)
	}
	if e.opCalls != 1 {
		t.Fatalf("同一订单只向运营商发一次，得 %d", e.opCalls)
	}
}

func TestUnlockRejectedAndUnreachable(t *testing.T) {
	e := newEnv(t, nil)
	e.seedOperators()
	e.opHandler = func(w http.ResponseWriter, r *http.Request, body []byte) {
		_, _ = w.Write([]byte(`{"result":"拒绝","rejectReason":"电量不足","message":"电量 8%"}`))
	}
	st, m := e.internal("/internal/opgw/unlock", "k1", unlockBody("OD2026090213697", "OP01", "BR01", "QH000015", false))
	if st != 200 || m["result"] != "拒绝" || m["rejectReason"] != "电量不足" || m["operatorMessage"] != "电量 8%" {
		t.Fatalf("%v", m)
	}
	// 3 秒超时（测试缩短为 300 毫秒）判不可达；之后同一订单再请求会重发
	e.opHandler = func(w http.ResponseWriter, r *http.Request, body []byte) {
		time.Sleep(600 * time.Millisecond)
	}
	st, m = e.internal("/internal/opgw/unlock", "k2", unlockBody("OD2026120111003", "OP01", "BR01", "QH000004", false))
	if st != 200 || m["result"] != "不可达" || m["operatorMessage"] == nil {
		t.Fatalf("%v", m)
	}
	e.opHandler = nil
	calls := e.opCalls
	st, m = e.internal("/internal/opgw/unlock", "k3", unlockBody("OD2026120111003", "OP01", "BR01", "QH000004", false))
	if m["result"] != "已受理" || e.opCalls != calls+1 {
		t.Fatalf("不可达后可重发：%v", m)
	}
}

func TestUnlockValidationAndCredential(t *testing.T) {
	e := newEnv(t, nil)
	e.seedOperators()
	if st, m := e.internal("/internal/opgw/unlock", "", unlockBody("OD1", "OP01", "BR01", "QH000004", false)); st != 400 || m["code"] != "INVALID" {
		t.Fatalf("缺幂等键：%d %v", st, m)
	}
	if st, _ := e.internal("/internal/opgw/unlock", "k", map[string]any{"orderId": "OD1", "operatorId": "OP01", "brandId": "BR01", "plateNo": "QH000004"}); st != 400 {
		t.Fatal("缺降级标记应 400")
	}
	if st, m := e.internal("/internal/opgw/unlock", "k", unlockBody("OD1", "OP05", "BR06", "TJ000001", false)); st != 409 || m["code"] != "CREDENTIAL_DISABLED" {
		t.Fatalf("无凭证：%d %v", st, m)
	}
	tk := adminToken(e, tokenOpt{})
	e.admin("POST", "/api/opgw/credentials/CRD0001/disable", tk, "d", map[string]string{"reason": "停用后不再发指令"})
	calls := e.opCalls
	if st, m := e.internal("/internal/opgw/unlock", "k9", unlockBody("OD2", "OP01", "BR01", "QH000004", false)); st != 409 || m["code"] != "CREDENTIAL_DISABLED" {
		t.Fatalf("停用：%d %v", st, m)
	}
	if e.opCalls != calls {
		t.Fatal("停用后不得向运营商发请求")
	}
}

// 任务 8：M-02 结束骑行

func TestEndRide(t *testing.T) {
	e := newEnv(t, nil)
	e.seedOperators()
	var got map[string]any
	var path string
	e.opHandler = func(w http.ResponseWriter, r *http.Request, body []byte) {
		path = r.URL.Path
		got = nil
		_ = json.Unmarshal(body, &got)
		_, _ = w.Write([]byte(`{"result":"已受理"}`))
	}
	base := map[string]any{"orderId": "OD2026092111452", "operatorId": "OP01", "brandId": "BR01", "plateNo": "QH000004"}
	with := func(kv ...any) map[string]any {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	if st, m := e.internal("/internal/opgw/end-ride", "e0", with("cause", "随便结束")); st != 400 || m["code"] != "INVALID" {
		t.Fatalf("%d %v", st, m)
	}
	if st, m := e.internal("/internal/opgw/end-ride", "e1", with("cause", "客服强制结束")); st != 400 || m["code"] != "REASON_REQUIRED" {
		t.Fatalf("强制结束须带事由：%d %v", st, m)
	}
	st, m := e.internal("/internal/opgw/end-ride", "e2", with("cause", "客服强制结束", "reason", "车辆故障无法还车，用户来电"))
	if st != 200 || m["result"] != "已受理" || path != "/v1/end-ride" || got["cause"] != "客服强制结束" || got["reason"] != "车辆故障无法还车，用户来电" {
		t.Fatalf("%d %v %v", st, m, got)
	}
	for i, cause := range []string{"用户点还车", "入桩未锁", "超时后才报开锁成功"} {
		st, m := e.internal("/internal/opgw/end-ride", "c"+string(rune('a'+i)), with("cause", cause, "orderId", "OD20261201110"+string(rune('1'+i))))
		if st != 200 || m["result"] != "已受理" || got["cause"] != cause {
			t.Fatalf("%s：%d %v", cause, st, m)
		}
		if _, has := got["reason"]; has {
			t.Fatal("非强制结束不带事由")
		}
	}
	// 同一订单同一原因不重复发
	calls := e.opCalls
	e.internal("/internal/opgw/end-ride", "e3", with("cause", "客服强制结束", "reason", "车辆故障无法还车，用户来电"))
	if e.opCalls != calls {
		t.Fatal("同一订单同一原因不重复发")
	}
	var n int
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM commands WHERE kind LIKE '结束骑行:%'`).Scan(&n)
	if n != 4 {
		t.Fatal(n)
	}
}

// 任务 12：运营商不可达的检测与事件

func TestUnreachableAndRecovered(t *testing.T) {
	e := newEnv(t, nil)
	e.seedOperators()
	e.opHandler = func(w http.ResponseWriter, r *http.Request, body []byte) { w.WriteHeader(503) }
	for i := 0; i < 4; i++ {
		e.internal("/internal/opgw/unlock", "f"+string(rune('0'+i)), unlockBody("OD202612011100"+string(rune('0'+i)), "OP01", "BR01", "QH000004", false))
		e.advance(time.Second)
	}
	un := outboxPayloads(t, "opgw.operator.unreachable")
	if len(un) != 1 {
		t.Fatalf("连续 3 次失败发一次不可达，得 %d", len(un))
	}
	p := un[0]["payload"].(map[string]any)
	if p["operatorId"] != "OP01" || len(p["brandIds"].([]any)) != 2 || p["since"] != "2026-12-01T12:00:05+08:00" || p["lastError"] != "运营商返回 HTTP 503" || un[0]["key"] != "OP01" {
		t.Fatalf("%v", un[0])
	}
	// 拒绝也算可达
	e.opHandler = func(w http.ResponseWriter, r *http.Request, body []byte) {
		_, _ = w.Write([]byte(`{"result":"拒绝","rejectReason":"车辆故障"}`))
	}
	e.internal("/internal/opgw/unlock", "ok1", unlockBody("OD2026120111009", "OP01", "BR01", "QH000004", false))
	rec := outboxPayloads(t, "opgw.operator.recovered")
	if len(rec) != 1 || rec[0]["payload"].(map[string]any)["recoveredAt"] == nil {
		t.Fatalf("%v", rec)
	}
	e.internal("/internal/opgw/unlock", "ok2", unlockBody("OD2026120111010", "OP01", "BR01", "QH000004", false))
	if len(outboxPayloads(t, "opgw.operator.recovered")) != 1 {
		t.Fatal("恢复只发一次")
	}
	// 失败次数未到阈值不发
	e.opHandler = func(w http.ResponseWriter, r *http.Request, body []byte) { w.WriteHeader(500) }
	e.internal("/internal/opgw/unlock", "g1", unlockBody("OD2026120111011", "OP01", "BR01", "QH000004", false))
	e.opHandler = nil
	e.internal("/internal/opgw/unlock", "g2", unlockBody("OD2026120111012", "OP01", "BR01", "QH000004", false))
	if len(outboxPayloads(t, "opgw.operator.unreachable")) != 1 {
		t.Fatal("中间有成功则重新计数")
	}
}
