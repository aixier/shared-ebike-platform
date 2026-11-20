// Package auth 校验登录令牌（用户令牌、管理令牌）与服务凭证。
// trace: contracts/wire.md 第 2、3 节
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

// Keys 是 kid → 公钥。
type Keys map[string]ed25519.PublicKey

// ParseKeys 解析 `kid=公钥base64` 逗号分隔的配置；公钥可以是 32 字节原始公钥或 PKIX DER。
func ParseKeys(s string) (Keys, error) {
	ks := Keys{}
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
			return nil, fmt.Errorf("公钥 %s 不是 base64：%w", kid, err)
		}
		if len(raw) == ed25519.PublicKeySize {
			ks[strings.TrimSpace(kid)] = ed25519.PublicKey(raw)
			continue
		}
		pk, err := x509.ParsePKIXPublicKey(raw)
		if err != nil {
			return nil, fmt.Errorf("公钥 %s 无法解析：%w", kid, err)
		}
		ed, ok := pk.(ed25519.PublicKey)
		if !ok {
			return nil, fmt.Errorf("公钥 %s 不是 Ed25519", kid)
		}
		ks[strings.TrimSpace(kid)] = ed
	}
	return ks, nil
}

func (ks Keys) parse(token, iss string) (jwt.MapClaims, error) {
	claims := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		k, ok := ks[kid]
		if !ok {
			return nil, errors.New("未知的密钥编号")
		}
		return k, nil
	}, jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithIssuer(iss), jwt.WithExpirationRequired())
	if err != nil {
		return nil, err
	}
	return claims, nil
}

// VerifyUser 校验用户令牌，返回用户编号。
func (ks Keys) VerifyUser(token string) (string, error) {
	c, err := ks.parse(token, "bike-acct")
	if err != nil {
		return "", err
	}
	sub, _ := c["sub"].(string)
	if sub == "" {
		return "", errors.New("令牌缺少用户编号")
	}
	return sub, nil
}

// Admin 是管理令牌里的声明。
type Admin struct {
	AccountID string
	Name      string
	Role      string
	Client    string
	Perms     map[string]bool
	Scope     string // 运营商账号的运营商编号；其余为空
	ReadOnly  bool
}

// Has 是否有某个权限点。
func (a *Admin) Has(p string) bool { return a.Perms[p] }

// HasAny 是否有其中任一权限点。
func (a *Admin) HasAny(ps ...string) bool {
	for _, p := range ps {
		if a.Perms[p] {
			return true
		}
	}
	return false
}

// VerifyAdmin 校验管理令牌。
func (ks Keys) VerifyAdmin(token string) (*Admin, error) {
	c, err := ks.parse(token, "bike-iam")
	if err != nil {
		return nil, err
	}
	a := &Admin{Perms: map[string]bool{}}
	a.AccountID, _ = c["sub"].(string)
	a.Name, _ = c["name"].(string)
	a.Role, _ = c["role"].(string)
	a.Client, _ = c["client"].(string)
	a.Scope, _ = c["scope"].(string)
	a.ReadOnly, _ = c["readonly"].(bool)
	if ps, ok := c["perms"].([]any); ok {
		for _, p := range ps {
			if s, ok := p.(string); ok {
				a.Perms[s] = true
			}
		}
	}
	if a.AccountID == "" {
		return nil, errors.New("令牌缺少账号编号")
	}
	return a, nil
}

// Bearer 取请求头里的令牌。
func Bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// Callers 是允许的调用方 → 凭证。
type Callers map[string]string

// ParseCallers 解析 `模块=凭证` 逗号分隔的配置。
func ParseCallers(s string) (Callers, error) {
	c := Callers{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, key, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("调用方配置格式应为 模块=凭证：%q", part)
		}
		c[strings.TrimSpace(name)] = strings.TrimSpace(key)
	}
	return c, nil
}

// Check 校验服务间调用；返回调用方模块名。
func (c Callers) Check(r *http.Request) (string, bool) {
	who := r.Header.Get("X-Caller")
	want, ok := c[who]
	if !ok || want == "" {
		return "", false
	}
	got := Bearer(r)
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return "", false
	}
	return who, true
}
