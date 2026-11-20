package opgw

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUnlockAndEnd(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key-ride" || r.Header.Get("X-Caller") != "ride" || r.Header.Get("Idempotency-Key") == "" {
			w.WriteHeader(401)
			return
		}
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		got = append(got, r.URL.Path+":"+m["orderId"].(string))
		if r.URL.Path == "/internal/opgw/unlock" {
			_ = json.NewEncoder(w).Encode(map[string]any{"result": "已受理", "at": "2026-12-01T12:00:00+08:00"})
			return
		}
		w.WriteHeader(409)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "CREDENTIAL_DISABLED", "message": "凭证已停用"})
	}))
	defer srv.Close()
	c := New(srv.URL, "key-ride")
	r, err := c.Unlock(context.Background(), UnlockCommand{Target: Target{OrderID: "OD1", OperatorID: "OP01", BrandID: "BR01", PlateNo: "P1"}})
	if err != nil || r.Result != Accepted {
		t.Fatalf("开锁：%+v %v", r, err)
	}
	r, err = c.EndRide(context.Background(), EndRideCommand{Target: Target{OrderID: "OD1"}, Cause: CauseUserReturn})
	if err != nil || r.Result != Rejected {
		t.Fatalf("409 应视为拒绝：%+v %v", r, err)
	}
	if len(got) != 2 {
		t.Fatalf("应收到两次请求：%v", got)
	}
	srv.Close()
	r, err = c.Unlock(context.Background(), UnlockCommand{Target: Target{OrderID: "OD2"}})
	if err == nil || r.Result != Unreachable {
		t.Fatalf("连不上应为不可达：%+v", r)
	}
}
