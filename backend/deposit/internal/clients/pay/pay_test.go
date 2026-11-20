package pay

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// trace: contracts/openapi/pay.yaml /internal/pay/deposit-charges、deposit-refunds、deposit-transfers
func TestClientCommands(t *testing.T) {
	var got struct {
		path, caller, auth, idem string
		body                     map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.caller, got.auth, got.idem = r.URL.Path, r.Header.Get("X-Caller"), r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key")
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"成功","paymentId":"PM1","type":"押金缴纳","account":"押金专户","amount":19900,"status":"成功","completedAt":"2026-12-01T12:00:00+08:00"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "deposit-key")
	res, err := c.ChargeDeposit(context.Background(), "k1", ChargeCommand{BizNo: "CH1", Type: "押金缴纳", UserID: "U100004", Amount: 19900, Channel: "微信支付"})
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/internal/pay/deposit-charges" || got.caller != "deposit" || got.auth != "Bearer deposit-key" || got.idem != "k1" {
		t.Fatalf("请求不对：%+v", got)
	}
	if got.body["bizNo"] != "CH1" || got.body["amount"].(float64) != 19900 {
		t.Fatalf("请求体不对：%v", got.body)
	}
	if res.Result != "成功" || res.PaymentID != "PM1" || res.CompletedAt == nil {
		t.Fatalf("结果不对：%+v", res)
	}

	_, _ = c.RefundDeposit(context.Background(), "k2", RefundCommand{BizNo: "RQ1", UserID: "U1", Amount: 100, OriginalPaymentIDs: []string{"PM1"}})
	if got.path != "/internal/pay/deposit-refunds" || got.body["originalPaymentIds"] == nil {
		t.Fatalf("退押请求不对：%+v", got)
	}
	_, _ = c.TransferDeposit(context.Background(), "k3", TransferCommand{BizNo: "DD1", UserID: "U1", OrderID: "OD1", Amount: 50})
	if got.path != "/internal/pay/deposit-transfers" || got.body["orderId"] != "OD1" {
		t.Fatalf("划转请求不对：%+v", got)
	}
}

func TestClientErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/pay/deposit-charges" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"code":"INVALID","message":"金额不对"}`))
			return
		}
		w.WriteHeader(500)
	}))
	defer srv.Close()
	c := New(srv.URL, "k")
	_, err := c.ChargeDeposit(context.Background(), "k1", ChargeCommand{})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != "INVALID" || ae.Status != 400 {
		t.Fatalf("4xx 应返回 APIError：%v", err)
	}
	_, err = c.RefundDeposit(context.Background(), "k1", RefundCommand{})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("5xx 应为不可用：%v", err)
	}
	c2 := New("http://127.0.0.1:1", "k")
	c2.HTTP.Timeout = 500 * time.Millisecond
	if _, err := c2.TransferDeposit(context.Background(), "k", TransferCommand{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("连不上应为不可用：%v", err)
	}
}
