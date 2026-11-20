package base

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"bike/cs/internal/httpx"
)

func TestDoSendsCredentialsAndDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk" || r.Header.Get("X-Caller") != "cs" || r.Header.Get("Idempotency-Key") != "k1" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "sk")
	var out struct{ OK bool }
	if err := c.Do(context.Background(), "POST", "/x", "k1", map[string]any{"a": 1}, &out); err != nil || !out.OK {
		t.Fatalf("err=%v out=%v", err, out)
	}
}

func TestDoMapsErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/blocked":
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"code":"CANCEL_BLOCKED","message":"有欠费","blockers":[{"code":"HAS_ARREARS","message":"欠费"}]}`))
		case "/missing":
			w.WriteHeader(404)
		case "/boom":
			w.WriteHeader(500)
		case "/denied":
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"code":"NOT_LOGGED_IN","message":"x"}`))
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "sk")
	ctx := context.Background()
	var e *httpx.Error
	err := c.Do(ctx, "GET", "/blocked", "", nil, nil)
	if !errors.As(err, &e) || e.Code != "CANCEL_BLOCKED" || e.Extra["blockers"] == nil {
		t.Fatalf("blocked: %v", err)
	}
	err = c.Do(ctx, "GET", "/missing", "", nil, nil)
	if !errors.As(err, &e) || e.Code != "NOT_FOUND" {
		t.Fatalf("missing: %v", err)
	}
	err = c.Do(ctx, "GET", "/boom", "", nil, nil)
	if !errors.As(err, &e) || e.Code != "SERVER_BUSY" {
		t.Fatalf("boom: %v", err)
	}
	err = c.Do(ctx, "GET", "/denied", "", nil, nil)
	if !errors.As(err, &e) || e.Code != "SERVER_BUSY" {
		t.Fatalf("denied 应按服务不可用处理: %v", err)
	}
	dead := New("http://127.0.0.1:1", "sk")
	err = dead.Do(ctx, "GET", "/x", "", nil, nil)
	if !errors.As(err, &e) || e.Code != "SERVER_BUSY" {
		t.Fatalf("连不上: %v", err)
	}
}
