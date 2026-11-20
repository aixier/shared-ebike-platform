package acct

// trace: R-11、NFR-11；acct.yaml /internal/acct/users/{userId}/payee-name-check

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TC-PAY-ACCT-01：按契约带服务凭证与调用方，返回是否一致
func TestCheckPayeeName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" || r.Header.Get("X-Caller") != "pay" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/internal/acct/users/U404/payee-name-check" {
			w.WriteHeader(404)
			w.Write([]byte(`{"code":"NOT_FOUND","message":"没有该用户"}`))
			return
		}
		var b struct{ Name string }
		json.NewDecoder(r.Body).Decode(&b)
		json.NewEncoder(w).Encode(map[string]bool{"match": b.Name == "张三"})
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, ServiceKey: "k"}
	ctx := context.Background()
	if m, err := c.CheckPayeeName(ctx, "U1", "张三"); err != nil || !m {
		t.Fatalf("%v %v", m, err)
	}
	if m, err := c.CheckPayeeName(ctx, "U1", "李四"); err != nil || m {
		t.Fatalf("%v %v", m, err)
	}
	if _, err := c.CheckPayeeName(ctx, "U404", "张三"); err != ErrUserNotFound {
		t.Fatalf("应为找不到用户：%v", err)
	}
	bad := &Client{BaseURL: srv.URL, ServiceKey: "x"}
	if _, err := bad.CheckPayeeName(ctx, "U1", "张三"); err == nil {
		t.Fatal("凭证错应报错")
	}
}
