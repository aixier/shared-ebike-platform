package operator

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"bike/opgw/internal/sign"
)

// 运营商替身：验签后按需答复
func stub(t *testing.T, secret string, h func(w http.ResponseWriter, body []byte)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !sign.Verify(secret, r.Method, r.URL.Path, r.Header.Get("X-App-Id"), r.Header.Get("X-Timestamp"), r.Header.Get("X-Nonce"), body, r.Header.Get("X-Signature")) {
			w.WriteHeader(401)
			return
		}
		if r.Header.Get("Idempotency-Key") == "" {
			w.WriteHeader(400)
			return
		}
		h(w, body)
	}))
}

func TestUnlockAccepted(t *testing.T) {
	var got UnlockRequest
	s := stub(t, "k1", func(w http.ResponseWriter, body []byte) {
		_ = json.Unmarshal(body, &got)
		_, _ = w.Write([]byte(`{"result":"已受理"}`))
	})
	defer s.Close()
	c := New(time.Second)
	rep, err := c.Unlock(context.Background(), Target{BaseURL: s.URL, AppID: "A", Secret: "k1"}, "unlock:OD1", UnlockRequest{OrderID: "OD1", PlateNo: "QH000004", Degraded: true, RequestAt: "2026-12-01T12:00:00+08:00"})
	if err != nil || rep.Result != "已受理" {
		t.Fatalf("%v %v", rep, err)
	}
	if got.OrderID != "OD1" || !got.Degraded {
		t.Fatalf("报文不符 %+v", got)
	}
}

func TestEndRideRejected(t *testing.T) {
	s := stub(t, "k1", func(w http.ResponseWriter, body []byte) {
		_, _ = w.Write([]byte(`{"result":"拒绝","rejectReason":"订单不存在","message":"无此单"}`))
	})
	defer s.Close()
	rep, err := New(time.Second).EndRide(context.Background(), Target{BaseURL: s.URL, AppID: "A", Secret: "k1"}, "k", EndRideRequest{OrderID: "OD1", Cause: "用户点还车"})
	if err != nil || rep.RejectReason != "订单不存在" || rep.Message != "无此单" {
		t.Fatalf("%v %v", rep, err)
	}
}

func TestTimeoutIsUnreachable(t *testing.T) {
	s := stub(t, "k1", func(w http.ResponseWriter, body []byte) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{"result":"已受理"}`))
	})
	defer s.Close()
	_, err := New(100*time.Millisecond).Unlock(context.Background(), Target{BaseURL: s.URL, AppID: "A", Secret: "k1"}, "k", UnlockRequest{})
	if err == nil {
		t.Fatal("超时应判不可达")
	}
}

func TestBadStatusAndBodyUnreachable(t *testing.T) {
	s := stub(t, "other", func(w http.ResponseWriter, body []byte) {})
	defer s.Close()
	if _, err := New(time.Second).Unlock(context.Background(), Target{BaseURL: s.URL, AppID: "A", Secret: "k1"}, "k", UnlockRequest{}); err == nil {
		t.Fatal("401 应判不可达")
	}
	s2 := stub(t, "k1", func(w http.ResponseWriter, body []byte) { _, _ = w.Write([]byte(`{"result":"好的"}`)) })
	defer s2.Close()
	if _, err := New(time.Second).Unlock(context.Background(), Target{BaseURL: s2.URL, AppID: "A", Secret: "k1"}, "k", UnlockRequest{}); err == nil {
		t.Fatal("无法识别的答复应判不可达")
	}
}
