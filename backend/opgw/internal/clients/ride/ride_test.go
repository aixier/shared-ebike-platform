package ride

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeliver(t *testing.T) {
	var got map[string]any
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/ride/ride-events" || r.Header.Get("Authorization") != "Bearer sk" || r.Header.Get("X-Caller") != "opgw" || r.Header.Get("Idempotency-Key") != "E1" {
			w.WriteHeader(401)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"accepted":true,"orderId":"OD1"}`))
	}))
	defer s.Close()
	o := "OD1"
	err := New(s.URL, "sk").Deliver(context.Background(), "E1", Event{OperatorEventID: "E1", OrderID: &o, Kind: "开锁结果", Unlock: &Unlock{Result: "成功", At: "t"}})
	if err != nil {
		t.Fatal(err)
	}
	if got["lock"] != nil || got["kind"] != "开锁结果" {
		t.Fatalf("lock 应为 null：%v", got)
	}
	if _, ok := got["late"]; !ok {
		t.Fatal("late 必填")
	}
}

func TestDeliverFail(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer s.Close()
	if err := New(s.URL, "sk").Deliver(context.Background(), "E1", Event{}); err == nil {
		t.Fatal("500 应失败")
	}
}
