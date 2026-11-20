package clients_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"bike/ops/internal/clients/fleet"
	"bike/ops/internal/clients/pay"
	"bike/ops/internal/clients/ride"
	"bike/ops/internal/clients/svccall"
)

// 按 fleet/ride/pay 的 swagger 写的替身：校验请求头与路径，返回契约形状。
func TestClientsAgainstStandIns(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k-ops" || r.Header.Get("X-Caller") != "ops" || r.Header.Get("Idempotency-Key") == "" {
			w.WriteHeader(401)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "UNAUTHORIZED", "message": "凭证不对"})
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		seen = append(seen, r.URL.Path)
		switch r.URL.Path {
		case "/internal/fleet/brands/BR02/pause":
			if body["metric"] != "人工" || body["actor"].(map[string]any)["id"] != "AC0101" {
				t.Errorf("暂停请求体 %v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"brand": map[string]any{"brandId": "BR02", "accessStatus": "暂停可租"}, "pause": map[string]any{"pauseId": "PS1", "brandId": "BR02", "pausedAt": "2026-12-01T12:00:00+08:00", "metric": "人工"}})
		case "/internal/fleet/brands/BR01/pause":
			w.WriteHeader(409)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "CONFLICT", "message": "品牌当前为联调中"})
		case "/internal/ride/orders/OD2026112714209/lock-fact":
			_ = json.NewEncoder(w).Encode(map[string]any{"orderId": "OD2026112714209", "status": "已结束"})
		case "/internal/pay/payments/PY1/confirm":
			_ = json.NewEncoder(w).Encode(map[string]any{"result": "成功", "paymentId": "PY1"})
		case "/healthz":
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	f := fleet.New(srv.URL, "k-ops")
	res, err := f.PauseBrand(ctx, "BR02", "idem-1", fleet.PauseRequest{Actor: fleet.Actor{Kind: "人", ID: "AC0101"}, Metric: "人工", Reason: "运营商车辆大面积故障"})
	if err != nil || res.Brand.AccessStatus != "暂停可租" || res.Pause.PauseID != "PS1" {
		t.Fatalf("暂停可租 %v %+v", err, res)
	}
	_, err = f.PauseBrand(ctx, "BR01", "idem-2", fleet.PauseRequest{Metric: "人工", Reason: "xxxx"})
	var re *svccall.RemoteError
	if !errors.As(err, &re) || re.Status != 409 || re.Code != "CONFLICT" {
		t.Fatalf("对方错误应解析为 RemoteError：%v", err)
	}

	rc := ride.New(srv.URL, "k-ops")
	o, err := rc.BackfillLockFact(ctx, "OD2026112714209", "idem-3", ride.LockFactRequest{EndAt: "2026-11-27T21:40:00+08:00", Reason: "用户来电确认"})
	if err != nil || o.Status != "已结束" {
		t.Fatalf("补录锁车 %v %+v", err, o)
	}

	pc := pay.New(srv.URL, "k-ops")
	pr, err := pc.ConfirmPayment(ctx, "PY1", "idem-4", pay.ConfirmRequest{Result: "已收款", Reason: "渠道核实已扣款"})
	if err != nil || pr.Result != "成功" {
		t.Fatalf("确认收款 %v %+v", err, pr)
	}

	// healthz 不带凭证，替身返回 401：视为不可用
	if ride.New(srv.URL, "").Healthy(ctx) {
		t.Fatal("非 200 视为不可用")
	}
	if ride.New("", "").Healthy(ctx) {
		t.Fatal("没配地址视为不可用")
	}
}
