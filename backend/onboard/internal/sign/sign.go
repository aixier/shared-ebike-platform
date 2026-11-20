// Package sign 按接入规范实现运营商报文签名（两个方向同一算法、同一把密钥）。
// 本模块自己再实现一遍，不复用 opgw 代码；以 contracts/operator-spec/operator-api.yaml 为准。
package sign

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// BodyHash 请求体原文的 SHA-256，小写十六进制。
func BodyHash(body []byte) string {
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

// StringToSign 方法\n路径\nAppId\n时间戳\n随机数\n请求体摘要。
func StringToSign(method, path, appID, ts, nonce string, body []byte) string {
	return strings.Join([]string{method, path, appID, ts, nonce, BodyHash(body)}, "\n")
}

// Sign HMAC-SHA256(密钥, 待签名串)，小写十六进制。
func Sign(secret, method, path, appID, ts, nonce string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(StringToSign(method, path, appID, ts, nonce, body)))
	return hex.EncodeToString(m.Sum(nil))
}

// Equal 常量时间比较两个十六进制签名（大小写不敏感）。
func Equal(a, b string) bool {
	return hmac.Equal([]byte(strings.ToLower(a)), []byte(strings.ToLower(b)))
}

// NonceOK 随机数 16～64 个字符。
func NonceOK(n string) bool { return len(n) >= 16 && len(n) <= 64 }

// NewNonce 生成 32 位十六进制随机数。
func NewNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
