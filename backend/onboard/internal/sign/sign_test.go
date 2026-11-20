package sign

import "testing"

// trace: operator-api.yaml「签名测试向量」
func TestVector(t *testing.T) {
	body := []byte(`{"eventId":"EVT-TEST-0001","type":"开锁结果","orderId":"OD2026120100001","plateNo":"QH000004","occurredAt":"2026-12-01T12:00:00+08:00","unlock":{"result":"成功"}}`)
	if got := BodyHash(body); got != "6896bff3b2e7339f8a33d310d41a3e55bea806506800839ea25936b87435e914" {
		t.Fatalf("摘要不对: %s", got)
	}
	got := Sign("test-secret-do-not-use-in-production", "POST", "/open/v1/ride-events", "APP-TEST-0001", "1796097600", "8f3c2a1e5b7d4c90", body)
	if got != "e6bfa21f86aea8837b30b5c3576054fbabc3d9e373f05c140499af29c018a973" {
		t.Fatalf("签名不对: %s", got)
	}
	if !Equal(got, "E6BFA21F86AEA8837B30B5C3576054FBABC3D9E373F05C140499AF29C018A973") {
		t.Fatal("大小写不同应视为同一签名")
	}
	if Equal(got, "00") {
		t.Fatal("不同签名不应相等")
	}
}

func TestNonceLen(t *testing.T) {
	if NonceOK("short") || !NonceOK("8f3c2a1e5b7d4c90") {
		t.Fatal("随机数长度 16～64")
	}
	if len(NewNonce()) != 32 || !NonceOK(NewNonce()) {
		t.Fatal("NewNonce 应为 32 位十六进制")
	}
}
