package httpx

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteError(t *testing.T) {
	w := httptest.NewRecorder()
	WriteError(w, New(409, "DEGRADE_NOT_ACTIVE", "当前不在降级中").With("eventId", nil))
	if w.Code != 409 {
		t.Fatalf("状态码 %d", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["code"] != "DEGRADE_NOT_ACTIVE" || body["message"] != "当前不在降级中" {
		t.Fatalf("错误体 %v", body)
	}
	if _, ok := body["eventId"]; !ok {
		t.Fatal("附加字段随错误体返回")
	}

	w = httptest.NewRecorder()
	WriteError(w, errors.New("pq: 连接失败 at line 3"))
	if w.Code != 500 || strings.Contains(w.Body.String(), "pq:") {
		t.Fatalf("内部错误不暴露细节：%d %s", w.Code, w.Body.String())
	}
}

func TestValidReason(t *testing.T) {
	if ValidReason("太短") || ValidReason("   ") || !ValidReason("故障恢复了") || !ValidReason("abcd") {
		t.Fatal("事由至少 4 个字")
	}
}

func TestDecodeRejectsNonUTF8(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader("{\"reason\":\"\xd6\xd0\xce\xc4\"}"))
	var v map[string]any
	err := Decode(r, &v)
	if err == nil || err.Code != "INVALID" || err.Status != 400 || !strings.Contains(err.Message, "请求体不是 UTF-8 编码") {
		t.Fatalf("GBK 请求体应返回 400 INVALID：%v", err)
	}
}

func TestDecode(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"reason":`))
	var v map[string]any
	if err := Decode(r, &v); err == nil || err.Code != "INVALID" {
		t.Fatal("坏 JSON 返回 INVALID")
	}
	r = httptest.NewRequest("POST", "/", strings.NewReader(""))
	if err := Decode(r, &v); err != nil {
		t.Fatal("空请求体当作空对象")
	}
}
