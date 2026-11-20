// Package auth 管理令牌验签与服务凭证校验（contracts/wire.md 第 2、3 节）。
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

// Admin 管理令牌里的身份。Scope 为空表示不限运营商。
type Admin struct {
	AccountID string
	Name      string
	Role      string
	Perms     []string
	Scope     string
	Readonly  bool
}

// HasAny 列出多个权限点时有其一即可。
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

// ParsePubKeys 解析 `kid=公钥base64` 逗号分隔。
func ParsePubKeys(s string) (map[string]ed25519.PublicKey, error) {
	out := map[string]ed25519.PublicKey{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kid, b64, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("公钥格式应为 kid=base64: %q", part)
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("公钥 %s 不是 base64: %w", kid, err)
		}
		if len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("公钥 %s 长度不对", kid)
		}
		out[kid] = ed25519.PublicKey(raw)
	}
	return out, nil
}

// AdminVerifier 验 iam 签发的管理令牌。
type AdminVerifier struct {
	Keys map[string]ed25519.PublicKey
}

type adminClaims struct {
	Name     string   `json:"name"`
	Role     string   `json:"role"`
	Perms    []string `json:"perms"`
	Scope    *string  `json:"scope"`
	Readonly bool     `json:"readonly"`
	jwt.RegisteredClaims
}

// Verify 校验 Authorization 头里的令牌。
func (v *AdminVerifier) Verify(header string) (*Admin, error) {
	raw, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || raw == "" {
		return nil, errors.New("没有令牌")
	}
	var c adminClaims
	_, err := jwt.ParseWithClaims(raw, &c, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		k, ok := v.Keys[kid]
		if !ok {
			return nil, fmt.Errorf("未知密钥 %q", kid)
		}
		return k, nil
	}, jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithIssuer("bike-iam"), jwt.WithExpirationRequired())
	if err != nil {
		return nil, err
	}
	a := &Admin{AccountID: c.Subject, Name: c.Name, Role: c.Role, Perms: c.Perms, Readonly: c.Readonly}
	if c.Scope != nil {
		a.Scope = *c.Scope
	}
	return a, nil
}

// Callers 允许的调用方 → 凭证。
type Callers map[string]string

// ParseCallers 解析 `模块=凭证` 逗号分隔。
func ParseCallers(s string) (Callers, error) {
	out := Callers{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("调用方格式应为 模块=凭证: %q", part)
		}
		out[k] = v
	}
	return out, nil
}

// Check 校验 /internal 请求的调用方凭证；返回调用方模块名。
func (c Callers) Check(r *http.Request) (string, bool) {
	who := r.Header.Get("X-Caller")
	want, ok := c[who]
	if !ok || want == "" {
		return "", false
	}
	got, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return "", false
	}
	return who, true
}
