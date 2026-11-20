// Package auth 校验管理令牌（iam 签发，EdDSA）与服务凭证（/internal 调用方）。
// 格式见 contracts/wire.md 第 2、3 节。
package auth

import (
	"crypto/ed25519"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// Admin 管理令牌里的登录人。
type Admin struct {
	AccountID string
	Name      string
	Role      string
	Client    string
	Perms     []string
	Scope     string // 运营商账号为其运营商编号，其余为空
	Readonly  bool
}

// HasAny 有其一即可（swagger x-permissions 的语义）。
func (a *Admin) HasAny(perms ...string) bool {
	for _, p := range perms {
		for _, q := range a.Perms {
			if p == q {
				return true
			}
		}
	}
	return false
}

// ParseKeys 解析 `kid=公钥base64` 逗号分隔；公钥可为 32 字节原始公钥或 PKIX DER。
func ParseKeys(s string) (map[string]ed25519.PublicKey, error) {
	out := map[string]ed25519.PublicKey{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kid, b64, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("公钥配置格式应为 kid=base64：%q", part)
		}
		raw, err := decodeB64(b64)
		if err != nil {
			return nil, fmt.Errorf("公钥 %s 不是 base64：%w", kid, err)
		}
		switch {
		case len(raw) == ed25519.PublicKeySize:
			out[strings.TrimSpace(kid)] = ed25519.PublicKey(raw)
		default:
			k, err := x509.ParsePKIXPublicKey(raw)
			if err != nil {
				return nil, fmt.Errorf("公钥 %s 无法解析：%w", kid, err)
			}
			ek, ok := k.(ed25519.PublicKey)
			if !ok {
				return nil, fmt.Errorf("公钥 %s 不是 Ed25519", kid)
			}
			out[strings.TrimSpace(kid)] = ek
		}
	}
	return out, nil
}

func decodeB64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	// 公钥 base64 里可能含 '='，按标准与 URL 两种写法依次尝试
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("无法解码")
}

// AdminVerifier 管理令牌校验器。
type AdminVerifier struct {
	keys map[string]ed25519.PublicKey
}

// NewAdminVerifier 用 ADMIN_JWT_PUBKEYS 构造。
func NewAdminVerifier(pubkeys string) (*AdminVerifier, error) {
	k, err := ParseKeys(pubkeys)
	if err != nil {
		return nil, err
	}
	return &AdminVerifier{keys: k}, nil
}

// ErrInvalidToken 令牌无效（签名、过期、签发方、kid）。
var ErrInvalidToken = errors.New("登录已失效")

// Verify 验签、过期与签发方，返回登录人。
func (v *AdminVerifier) Verify(token string) (*Admin, error) {
	claims := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		k, ok := v.keys[kid]
		if !ok {
			return nil, fmt.Errorf("未知 kid %q", kid)
		}
		return k, nil
	}, jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithIssuer("bike-iam"), jwt.WithExpirationRequired())
	if err != nil {
		return nil, ErrInvalidToken
	}
	a := &Admin{}
	a.AccountID, _ = claims["sub"].(string)
	a.Name, _ = claims["name"].(string)
	a.Role, _ = claims["role"].(string)
	a.Client, _ = claims["client"].(string)
	a.Scope, _ = claims["scope"].(string)
	a.Readonly, _ = claims["readonly"].(bool)
	if ps, ok := claims["perms"].([]any); ok {
		for _, p := range ps {
			if s, ok := p.(string); ok {
				a.Perms = append(a.Perms, s)
			}
		}
	}
	if a.AccountID == "" {
		return nil, ErrInvalidToken
	}
	return a, nil
}

// CallerVerifier /internal 调用方凭证表（ALLOWED_CALLERS）。
type CallerVerifier struct {
	callers map[string]string
}

// NewCallerVerifier 解析 `模块=凭证` 逗号分隔。
func NewCallerVerifier(s string) *CallerVerifier {
	m := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		name, key, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && name != "" && key != "" {
			m[strings.TrimSpace(name)] = strings.TrimSpace(key)
		}
	}
	return &CallerVerifier{callers: m}
}

// Verify 调用方与凭证是否对得上。
func (c *CallerVerifier) Verify(caller, key string) bool {
	want, ok := c.callers[caller]
	if !ok || key == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(key)) == 1
}
