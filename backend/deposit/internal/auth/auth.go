// Package auth 验登录令牌（用户令牌、管理令牌）与服务凭证。格式见 contracts/wire.md 第 2、3 节。
package auth

import (
	"crypto/ed25519"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// ParseKeys 解析 `kid=公钥base64` 逗号分隔的公钥表。公钥可为 32 字节原始公钥或 PKIX DER。
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
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
		if err != nil {
			return nil, fmt.Errorf("公钥 %s 不是合法 base64：%w", kid, err)
		}
		if len(raw) == ed25519.PublicKeySize {
			out[strings.TrimSpace(kid)] = ed25519.PublicKey(raw)
			continue
		}
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
	return out, nil
}

// Verifier 用一组公钥验签。
type Verifier struct{ keys map[string]ed25519.PublicKey }

// NewVerifier 建验签器。
func NewVerifier(keys map[string]ed25519.PublicKey) *Verifier { return &Verifier{keys: keys} }

var errBadToken = errors.New("令牌无效")

func (v *Verifier) parse(tok, iss string) (jwt.MapClaims, error) {
	claims := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(tok, claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		k, ok := v.keys[kid]
		if !ok {
			return nil, errBadToken
		}
		return k, nil
	}, jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithIssuer(iss), jwt.WithExpirationRequired())
	if err != nil {
		return nil, errBadToken
	}
	return claims, nil
}

// VerifyUser 验用户令牌，返回 userId。
func (v *Verifier) VerifyUser(tok string) (string, error) {
	c, err := v.parse(tok, "bike-acct")
	if err != nil {
		return "", err
	}
	sub, _ := c["sub"].(string)
	if sub == "" {
		return "", errBadToken
	}
	return sub, nil
}

// Admin 管理令牌里本服务用到的声明。
type Admin struct {
	AccountID string
	Name      string
	Role      string
	Perms     []string
	Scope     *string
	Readonly  bool
}

// Has 是否持有权限点。
func (a Admin) Has(p string) bool {
	for _, x := range a.Perms {
		if x == p {
			return true
		}
	}
	return false
}

// HasAny 列出多个权限点时有其一即可。
func (a Admin) HasAny(ps ...string) bool {
	for _, p := range ps {
		if a.Has(p) {
			return true
		}
	}
	return false
}

// VerifyAdmin 验管理令牌。
func (v *Verifier) VerifyAdmin(tok string) (Admin, error) {
	c, err := v.parse(tok, "bike-iam")
	if err != nil {
		return Admin{}, err
	}
	a := Admin{}
	a.AccountID, _ = c["sub"].(string)
	a.Name, _ = c["name"].(string)
	a.Role, _ = c["role"].(string)
	a.Readonly, _ = c["readonly"].(bool)
	if s, ok := c["scope"].(string); ok && s != "" {
		a.Scope = &s
	}
	if ps, ok := c["perms"].([]any); ok {
		for _, p := range ps {
			if s, ok := p.(string); ok {
				a.Perms = append(a.Perms, s)
			}
		}
	}
	if a.AccountID == "" {
		return Admin{}, errBadToken
	}
	return a, nil
}

// Bearer 取 Authorization 头里的令牌。
func Bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// ServiceAuth 允许的调用方与其凭证。
type ServiceAuth struct{ callers map[string]string }

// ParseCallers 解析 `模块=凭证` 逗号分隔的清单。
func ParseCallers(s string) (*ServiceAuth, error) {
	m := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok || k == "" || v == "" {
			return nil, fmt.Errorf("ALLOWED_CALLERS 格式应为 模块=凭证：%q", part)
		}
		m[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return &ServiceAuth{callers: m}, nil
}

// Check 校验调用方与凭证，通过时返回调用方模块名。
func (s *ServiceAuth) Check(r *http.Request) (string, bool) {
	caller := r.Header.Get("X-Caller")
	want, ok := s.callers[caller]
	if !ok {
		return "", false
	}
	got := Bearer(r)
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return "", false
	}
	return caller, true
}
