package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStartAndGoLive(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer svc" || r.Header.Get("X-Caller") != "onboard" || r.Header.Get("Idempotency-Key") == "" {
			w.WriteHeader(401)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		seen = append(seen, r.URL.Path)
		switch r.URL.Path {
		case "/internal/fleet/brands/BR05/start-integration":
			if body["actor"] == nil {
				w.WriteHeader(400)
				return
			}
			_, _ = w.Write([]byte(`{"brandId":"BR05","accessStatus":"联调中","statusSince":"2026-11-10T09:30:00+08:00"}`))
		case "/internal/fleet/brands/BR05/go-live":
			if body["reason"] == nil {
				w.WriteHeader(400)
				return
			}
			_, _ = w.Write([]byte(`{"brand":{"brandId":"BR05"},"enabledCodes":60}`))
		case "/internal/fleet/brands/BR09/go-live":
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"code":"CONFLICT","message":"品牌不在联调中"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "svc")
	ctx := context.Background()
	actor := Actor{Kind: "人", ID: "AC0114"}
	st, err := c.StartIntegration(ctx, "BR05", "k1", actor)
	if err != nil || st.AccessStatus != "联调中" || st.StatusSince.IsZero() {
		t.Fatalf("开始联调: %v %+v", err, st)
	}
	gl, err := c.GoLive(ctx, "BR05", "k2", actor, "验收报告已确认")
	if err != nil || gl.EnabledCodes != 60 {
		t.Fatalf("上线: %v %+v", err, gl)
	}
	_, err = c.GoLive(ctx, "BR09", "k3", actor, "验收报告已确认")
	var fe *Error
	if !errors.As(err, &fe) || fe.Status != 409 || fe.Code != "CONFLICT" {
		t.Fatalf("应返回 fleet 的业务错误: %v", err)
	}
	if len(seen) != 3 {
		t.Fatalf("调用次数不对: %v", seen)
	}
}

func TestUnreachable(t *testing.T) {
	c := New("http://127.0.0.1:1", "svc")
	_, err := c.StartIntegration(context.Background(), "BR05", "k", Actor{Kind: "系统", ID: "onboard"})
	var fe *Error
	if err == nil || errors.As(err, &fe) {
		t.Fatalf("连不上应是网络错误而不是业务错误: %v", err)
	}
}

// 第二轮：调 fleet 时 actor.kind 只会是人或系统。
func TestActorKindNormalized(t *testing.T) {
	var kinds []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Actor Actor `json:"actor"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		kinds = append(kinds, body.Actor.Kind)
		_, _ = w.Write([]byte(`{"brandId":"BR05","accessStatus":"联调中","statusSince":"2026-11-10T09:30:00+08:00","brand":{},"enabledCodes":0}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "svc")
	for _, k := range []string{"人", "系统", "智能体", "用户", ""} {
		if _, err := c.StartIntegration(context.Background(), "BR05", "k", Actor{Kind: k, ID: "x"}); err != nil {
			t.Fatal(err)
		}
		if _, err := c.GoLive(context.Background(), "BR05", "k", Actor{Kind: k, ID: "x"}, "事由事由"); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"人", "人", "系统", "系统", "系统", "系统", "系统", "系统", "系统", "系统"}
	for i, k := range kinds {
		if k != want[i] {
			t.Fatalf("第 %d 次 actor.kind=%s，应为 %s", i, k, want[i])
		}
	}
}
