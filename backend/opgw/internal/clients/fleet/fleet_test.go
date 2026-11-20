package fleet

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSubmitOK(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var sub Submission
		_ = json.NewDecoder(r.Body).Decode(&sub)
		if r.URL.Path != "/internal/fleet/vehicle-lists" || r.Header.Get("Idempotency-Key") == "" || sub.Actor.Kind != "系统" {
			w.WriteHeader(400)
			return
		}
		_, _ = w.Write([]byte(`{"added":1,"vehicles":[{"vehicleId":"VH000004","plateNo":"QH000004","govCode":"SF86020226","codeStatus":"已启用","brandId":"BR01"}]}`))
	}))
	defer s.Close()
	resp, err := New(s.URL, "sk").Submit(context.Background(), "k", Submission{OperatorID: "OP01", Rows: []Row{{BrandID: "BR01"}}, Actor: Actor{Kind: "系统", ID: "opgw"}})
	if err != nil || resp.Result == nil || resp.Result.Vehicles[0].GovCode != "SF86020226" {
		t.Fatalf("%+v %v", resp, err)
	}
}

func TestSubmitErrorPassthrough(t *testing.T) {
	body := `{"code":"VEHICLE_LIST_INVALID","message":"清单有错","errors":[{"row":2,"field":"plateNo","message":"车号重复"}]}`
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(body))
	}))
	defer s.Close()
	resp, err := New(s.URL, "sk").Submit(context.Background(), "k", Submission{})
	if err != nil || resp.Status != 400 || string(resp.Body) != body || resp.Result != nil {
		t.Fatalf("%+v %v", resp, err)
	}
}
