// Package sign 实现运营商接入规范的 HMAC-SHA256 签名（两个方向同一算法）。
package sign

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
)

// BodyDigest 请求体原文的 SHA-256，小写十六进制。
func BodyDigest(body []byte) string {
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

// StringToSign 待签名串：方法、路径、应用编号、时间戳、随机数、请求体摘要，以换行连接。
func StringToSign(method, path, appID, ts, nonce string, body []byte) string {
	return method + "\n" + path + "\n" + appID + "\n" + ts + "\n" + nonce + "\n" + BodyDigest(body)
}

// Sign 计算 X-Signature。
func Sign(secret, method, path, appID, ts, nonce string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(StringToSign(method, path, appID, ts, nonce, body)))
	return hex.EncodeToString(m.Sum(nil))
}

// Verify 常量时间比较签名。
func Verify(secret, method, path, appID, ts, nonce string, body []byte, sig string) bool {
	want := Sign(secret, method, path, appID, ts, nonce, body)
	return hmac.Equal([]byte(want), []byte(sig))
}

// Apply 给发往运营商的请求加签名头。路径取 URL.Path，不含域名与查询串。
func Apply(req *http.Request, secret, appID, ts, nonce string, body []byte) {
	req.Header.Set("X-App-Id", appID)
	req.Header.Set("X-Timestamp", ts)
	req.Header.Set("X-Nonce", nonce)
	req.Header.Set("X-Signature", Sign(secret, req.Method, req.URL.Path, appID, ts, nonce, body))
}

// TimestampOK 时间戳（Unix 秒）与本地时间相差不超过 window 秒。
func TimestampOK(ts string, nowUnix, window int64) bool {
	v, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	d := nowUnix - v
	if d < 0 {
		d = -d
	}
	return d <= window
}

// NonceOK 随机数 16～64 个字符。
func NonceOK(n string) bool {
	return len(n) >= 16 && len(n) <= 64
}

// NewNonce 生成 32 位十六进制随机数。
func NewNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
