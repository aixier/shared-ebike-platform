package sign

import (
	"net/http"
	"testing"
)

// 接入规范里的签名测试向量
const (
	vecSecret = "test-secret-do-not-use-in-production"
	vecAppID  = "APP-TEST-0001"
	vecTS     = "1796097600"
	vecNonce  = "8f3c2a1e5b7d4c90"
	vecBody   = `{"eventId":"EVT-TEST-0001","type":"开锁结果","orderId":"OD2026120100001","plateNo":"QH000004","occurredAt":"2026-12-01T12:00:00+08:00","unlock":{"result":"成功"}}`
	vecDigest = "6896bff3b2e7339f8a33d310d41a3e55bea806506800839ea25936b87435e914"
	vecSig    = "e6bfa21f86aea8837b30b5c3576054fbabc3d9e373f05c140499af29c018a973"
)

func TestBodyDigestVector(t *testing.T) {
	if got := BodyDigest([]byte(vecBody)); got != vecDigest {
		t.Fatalf("摘要不符: %s", got)
	}
}

func TestSignVector(t *testing.T) {
	got := Sign(vecSecret, "POST", "/open/v1/ride-events", vecAppID, vecTS, vecNonce, []byte(vecBody))
	if got != vecSig {
		t.Fatalf("签名不符: %s", got)
	}
}

func TestVerifyConstantCompare(t *testing.T) {
	if !Verify(vecSecret, "POST", "/open/v1/ride-events", vecAppID, vecTS, vecNonce, []byte(vecBody), vecSig) {
		t.Fatal("正确签名应通过")
	}
	if Verify(vecSecret, "POST", "/open/v1/ride-events", vecAppID, vecTS, vecNonce, []byte(vecBody+" "), vecSig) {
		t.Fatal("请求体被篡改应不通过")
	}
	if Verify("other", "POST", "/open/v1/ride-events", vecAppID, vecTS, vecNonce, []byte(vecBody), vecSig) {
		t.Fatal("密钥不同应不通过")
	}
}

func TestApplyHeaders(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://op.example.com/v1/unlock?x=1", nil)
	Apply(req, vecSecret, vecAppID, vecTS, vecNonce, []byte(vecBody))
	if req.Header.Get("X-App-Id") != vecAppID || req.Header.Get("X-Timestamp") != vecTS || req.Header.Get("X-Nonce") != vecNonce {
		t.Fatal("请求头缺失")
	}
	want := Sign(vecSecret, "POST", "/v1/unlock", vecAppID, vecTS, vecNonce, []byte(vecBody))
	if req.Header.Get("X-Signature") != want {
		t.Fatal("签名路径应不含域名与查询串")
	}
}

func TestTimestampWindow(t *testing.T) {
	if !TimestampOK("1000", 1300, 300) || !TimestampOK("1300", 1000, 300) {
		t.Fatal("边界 300 秒内应通过")
	}
	if TimestampOK("1000", 1301, 300) || TimestampOK("abc", 1000, 300) {
		t.Fatal("超窗或非数字应拒绝")
	}
}

func TestNonceFormat(t *testing.T) {
	if NonceOK("short") || !NonceOK(vecNonce) {
		t.Fatal("随机数长度 16～64")
	}
}
