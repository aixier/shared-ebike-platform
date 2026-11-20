package pay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChargeCreditFree(t *testing.T) {
	var got CreditChargeCommand
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/pay/credit-charges" || r.Header.Get("X-Caller") != "bill" ||
			r.Header.Get("Authorization") != "Bearer k" || r.Header.Get("Idempotency-Key") != "i1" {
			w.WriteHeader(401)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"result":"成功","paymentId":"PY1","amount":800,"status":"成功","completedAt":"2026-12-01T12:00:00+08:00"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "k")
	res, err := c.ChargeCreditFree(context.Background(), "i1", CreditChargeCommand{BizNo: "B1", UserID: "U1", OrderID: "O1", Amount: 800, Channel: "微信支付"})
	if err != nil || res.Result != "成功" || res.PaymentID != "PY1" || got.Amount != 800 {
		t.Fatalf("%+v %v %+v", res, err, got)
	}
}

func TestErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"code":"INVALID","message":"x"}`))
	}))
	defer srv.Close()
	if _, err := New(srv.URL, "k").Refund(context.Background(), "i", RefundCommand{}); err == nil {
		t.Fatal("非 200 应返回错误")
	}
}

// 发起支付：新路径返回调起参数，原样带回
func TestStartOneTap_PayParams(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"result":"结果未知","paymentId":"PY1","status":"待支付","payParams":{"prepayId":"wx1","sign":"s"}}`))
	}))
	defer srv.Close()
	res, err := New(srv.URL, "k").StartOneTap(context.Background(), "i", OneTapPaymentCommand{BizNo: "B", Type: "订单支付", UserID: "U", Amount: 100, Channel: "微信支付"})
	if err != nil || path != PaymentsPath || !res.HasPayParams() || string(res.PayParams) != `{"prepayId":"wx1","sign":"s"}` {
		t.Fatalf("%+v %v %s", res, err, path)
	}
}

// 旧版 pay 只有 one-tap-payments：新路径 404 时退回旧路径；没有 payParams
func TestStartOneTap_FallbackLegacy(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == PaymentsPath {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte(`{"result":"成功","paymentId":"PY2","amount":100}`))
	}))
	defer srv.Close()
	res, err := New(srv.URL, "k").StartOneTap(context.Background(), "i", OneTapPaymentCommand{BizNo: "B", Amount: 100})
	if err != nil || res.Result != "成功" || res.HasPayParams() || len(paths) != 2 || paths[1] != LegacyOneTapPath {
		t.Fatalf("%+v %v %v", res, err, paths)
	}
	// 非 404 的错误不退回
	paths = nil
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(400)
	}))
	defer srv2.Close()
	if _, err := New(srv2.URL, "k").StartOneTap(context.Background(), "i", OneTapPaymentCommand{}); err == nil || len(paths) != 1 {
		t.Fatalf("%v %v", err, paths)
	}
}
