// Package auth 校验登录令牌与服务凭证（权限，确定性代码合法范围）。
// trace: wire.md 第 2、3 节
package auth

import (
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// ParseKeys 解析 `kid=公钥base64` 逗号分隔的公钥配置。
func ParseKeys(s string) (map[string]ed25519.PublicKey, error) {
	out := map[string]ed25519.PublicKey{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kid, b64, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("公钥配置格式应为 kid=公钥：%q", part)
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("公钥 %s 不是有效的 Ed25519 公钥", kid)
		}
		out[kid] = ed25519.PublicKey(raw)
	}
	return out, nil
}

// Admin 管理令牌里的声明
type Admin struct {
	AccountID string
	Name      string
	Role      string
	Client    string
	Perms     []string
	Scope     *string
	Readonly  bool
}

// HasAny 有其一即可
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

// Verifier 令牌校验器
type Verifier struct {
	UserKeys  map[string]ed25519.PublicKey
	AdminKeys map[string]ed25519.PublicKey
}

var ErrNoToken = errors.New("未带令牌")

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func parse(tok string, keys map[string]ed25519.PublicKey, iss string) (jwt.MapClaims, error) {
	if tok == "" {
		return nil, ErrNoToken
	}
	claims := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(tok, claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		k, ok := keys[kid]
		if !ok {
			return nil, fmt.Errorf("未知的密钥编号 %q", kid)
		}
		return k, nil
	}, jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithIssuer(iss), jwt.WithExpirationRequired())
	if err != nil {
		return nil, err
	}
	return claims, nil
}

// User 校验用户令牌，返回 userId。
func (v *Verifier) User(r *http.Request) (string, error) {
	c, err := parse(bearer(r), v.UserKeys, "bike-acct")
	if err != nil {
		return "", err
	}
	sub, _ := c["sub"].(string)
	if sub == "" {
		return "", errors.New("令牌缺少 sub")
	}
	return sub, nil
}

// Admin 校验管理令牌。
func (v *Verifier) Admin(r *http.Request) (*Admin, error) {
	c, err := parse(bearer(r), v.AdminKeys, "bike-iam")
	if err != nil {
		return nil, err
	}
	a := &Admin{}
	a.AccountID, _ = c["sub"].(string)
	a.Name, _ = c["name"].(string)
	a.Role, _ = c["role"].(string)
	a.Client, _ = c["client"].(string)
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
		return nil, errors.New("令牌缺少 sub")
	}
	return a, nil
}

// Callers 允许的调用方 → 凭证
type Callers map[string]string

// ParseCallers 解析 `模块=凭证` 逗号分隔。
func ParseCallers(s string) (Callers, error) {
	out := Callers{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, key, ok := strings.Cut(part, "=")
		if !ok || name == "" || key == "" {
			return nil, fmt.Errorf("ALLOWED_CALLERS 格式应为 模块=凭证：%q", part)
		}
		out[name] = key
	}
	return out, nil
}

// Check 校验服务凭证，返回调用方模块名。
func (c Callers) Check(r *http.Request) (string, bool) {
	name := r.Header.Get("X-Caller")
	want, ok := c[name]
	if !ok {
		return "", false
	}
	got := bearer(r)
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return "", false
	}
	return name, true
}
