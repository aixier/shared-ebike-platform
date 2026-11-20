package deposit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeduct(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var cmd DeductCommand
		_ = json.NewDecoder(r.Body).Decode(&cmd)
		if r.URL.Path != "/internal/deposit/deductions" || r.Header.Get("Idempotency-Key") == "" || r.Header.Get("X-Caller") != "bill" {
			w.WriteHeader(400)
			return
		}
		_ = json.NewEncoder(w).Encode(DeductResult{Result: "成功", UserID: cmd.UserID, OrderID: cmd.OrderID, Deducted: 600, Shortfall: cmd.Amount - 600})
	}))
	defer srv.Close()
	res, err := New(srv.URL, "k").Deduct(context.Background(), "d1", DeductCommand{UserID: "U1", OrderID: "O1", Amount: 1450})
	if err != nil || res.Deducted != 600 || res.Shortfall != 850 {
		t.Fatalf("%+v %v", res, err)
	}
}
