// Package auth 验登录令牌（用户令牌 acct 签发、管理令牌 iam 签发，EdDSA）与服务间凭证。见 contracts/wire.md 第 2、3 节。
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

// ParseKeys 解析 "kid=公钥base64,kid2=..."
func ParseKeys(s string) (map[string]ed25519.PublicKey, error) {
	out := map[string]ed25519.PublicKey{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kid, b64, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("公钥格式应为 kid=base64：%q", part)
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
		if err != nil {
			return nil, fmt.Errorf("公钥 %s 不是 base64：%w", kid, err)
		}
		if len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("公钥 %s 长度不对", kid)
		}
		out[strings.TrimSpace(kid)] = ed25519.PublicKey(raw)
	}
	return out, nil
}

// Verifier 令牌校验
type Verifier struct {
	UserKeys  map[string]ed25519.PublicKey
	AdminKeys map[string]ed25519.PublicKey
}

// ErrNoToken 没有令牌或令牌无效
var ErrNoToken = errors.New("未登录或登录失效")

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

func parse(tok string, keys map[string]ed25519.PublicKey, iss string, claims jwt.Claims) error {
	if tok == "" {
		return ErrNoToken
	}
	_, err := jwt.ParseWithClaims(tok, claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		k, ok := keys[kid]
		if !ok {
			return nil, errors.New("未知密钥")
		}
		return k, nil
	}, jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithIssuer(iss), jwt.WithExpirationRequired())
	if err != nil {
		return ErrNoToken
	}
	return nil
}

// User 校验用户令牌，返回 userId
func (v Verifier) User(r *http.Request) (string, error) {
	var c jwt.RegisteredClaims
	if err := parse(bearer(r), v.UserKeys, "bike-acct", &c); err != nil {
		return "", err
	}
	if c.Subject == "" {
		return "", ErrNoToken
	}
	return c.Subject, nil
}

// Admin 管理令牌的声明
type Admin struct {
	jwt.RegisteredClaims
	AccountID string   `json:"-"`
	Name      string   `json:"name"`
	Role      string   `json:"role"`
	Client    string   `json:"client"`
	Perms     []string `json:"perms"`
	Scope     string   `json:"scope"`
	Readonly  bool     `json:"readonly"`
}

// Has 是否有某个权限点
func (a *Admin) Has(p string) bool {
	for _, x := range a.Perms {
		if x == p {
			return true
		}
	}
	return false
}

// HasAny 列出多个权限点时有其一即可
func (a *Admin) HasAny(ps ...string) bool {
	for _, p := range ps {
		if a.Has(p) {
			return true
		}
	}
	return false
}

// Admin 校验管理令牌
func (v Verifier) Admin(r *http.Request) (*Admin, error) {
	var c Admin
	if err := parse(bearer(r), v.AdminKeys, "bike-iam", &c); err != nil {
		return nil, err
	}
	c.AccountID = c.Subject
	if c.AccountID == "" {
		return nil, ErrNoToken
	}
	return &c, nil
}

// Callers 允许的调用方 → 凭证
type Callers map[string]string

// ParseCallers 解析 "模块=凭证,..."
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
		out[strings.TrimSpace(name)] = strings.TrimSpace(key)
	}
	return out, nil
}

// Check 校验服务间调用：X-Caller 在名单内且凭证一致
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
