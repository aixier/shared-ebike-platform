package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// trace: wire.md 第 1 节 错误码与 HTTP 状态
func TestStatusOf(t *testing.T) {
	cases := map[string]int{
		"INVALID": 400, "REASON_REQUIRED": 400,
		"NOT_LOGGED_IN": 401, "ADMIN_NOT_LOGGED_IN": 401,
		"FORBIDDEN": 403, "SCOPE_DENIED": 403, "READONLY": 403, "ACCOUNT_DISABLED": 403,
		"NOT_FOUND": 404,
		"CONFLICT":  409, "ALREADY_DONE": 409, "NOT_RIDING": 409, "REFUND_BLOCKED": 409,
		"SERVER_BUSY": 500, "INTERNAL": 500,
	}
	for code, want := range cases {
		if got := StatusOf(code); got != want {
			t.Errorf("%s: 想要 %d，得到 %d", code, want, got)
		}
	}
}

func TestWriteErrorBody(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, &Error{Code: "REFUND_BLOCKED", Message: "暂不能退押", Extra: map[string]any{"blockers": []any{map[string]any{"code": "HAS_RIDING", "message": "有骑行中订单"}}}})
	if rec.Code != 409 {
		t.Fatalf("状态码 %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type %q", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "REFUND_BLOCKED" || body["message"] != "暂不能退押" || body["blockers"] == nil {
		t.Fatalf("错误体不对：%v", body)
	}
}

func TestWriteErrorNonAPIErrorHidesDetail(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, errPlain("pq: connection refused at 10.0.0.1"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("状态码 %d", rec.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["code"] != "INTERNAL" {
		t.Fatalf("code=%v", body["code"])
	}
	if s, _ := body["message"].(string); s == "" || s == "pq: connection refused at 10.0.0.1" {
		t.Fatalf("内部细节不应外露：%q", s)
	}
}

func TestDecodeJSON(t *testing.T) {
	req := httptest.NewRequest("POST", "/", stringsReader(`{"text":"你好"}`))
	var v struct{ Text string }
	if err := DecodeJSON(req, &v); err != nil || v.Text != "你好" {
		t.Fatalf("err=%v v=%v", err, v)
	}
	req = httptest.NewRequest("POST", "/", stringsReader(`{bad`))
	err := DecodeJSON(req, &v)
	if e, ok := err.(*Error); !ok || e.Code != "INVALID" {
		t.Fatalf("坏 JSON 应为 INVALID：%v", err)
	}
}

// trace: wire.md 第 1 节 请求体编码
func TestDecodeJSONRejectsNonUTF8(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader("{\"a\":\"\xb5\xf7\"}"))
	var v map[string]any
	err := DecodeJSON(req, &v)
	var he *Error
	if !errors.As(err, &he) || he.Code != "INVALID" || !strings.Contains(he.Message, "请求体不是 UTF-8 编码") {
		t.Fatalf("%v", err)
	}
}
