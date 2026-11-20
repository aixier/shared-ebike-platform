// Package auth 令牌与服务凭证校验（wire.md 第 2、3 节）。
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

// ParseKeys 解析 "kid=公钥base64,kid2=..."。公钥可以是 32 字节原始 Ed25519 公钥，也可以是 PKIX DER。
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
			// base64 本身可能以 = 结尾，Cut 只切第一个 =，这里不会出错；再试 URL 编码
			raw, err = base64.RawURLEncoding.DecodeString(strings.TrimSpace(b64))
			if err != nil {
				return nil, fmt.Errorf("公钥 %s 不是 base64", kid)
			}
		}
		switch {
		case len(raw) == ed25519.PublicKeySize:
			out[kid] = ed25519.PublicKey(raw)
		default:
			pk, err := x509.ParsePKIXPublicKey(raw)
			if err != nil {
				return nil, fmt.Errorf("公钥 %s 无法解析", kid)
			}
			ek, ok := pk.(ed25519.PublicKey)
			if !ok {
				return nil, fmt.Errorf("公钥 %s 不是 Ed25519", kid)
			}
			out[kid] = ek
		}
	}
	return out, nil
}

// ErrNoToken 没带令牌。
var ErrNoToken = errors.New("没有令牌")

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func verify(tok string, keys map[string]ed25519.PublicKey, iss string, claims jwt.Claims) error {
	_, err := jwt.ParseWithClaims(tok, claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		k, ok := keys[kid]
		if !ok {
			if len(keys) == 1 && kid == "" {
				for _, only := range keys {
					return only, nil
				}
			}
			return nil, fmt.Errorf("未知密钥编号 %q", kid)
		}
		return k, nil
	}, jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithIssuer(iss), jwt.WithExpirationRequired())
	return err
}

// UserClaims 用户令牌。
type UserClaims struct {
	jwt.RegisteredClaims
}

// Admin 管理令牌里的账号信息。
type Admin struct {
	jwt.RegisteredClaims
	Name     string   `json:"name"`
	Role     string   `json:"role"`
	Client   string   `json:"client"`
	Perms    []string `json:"perms"`
	Scope    *string  `json:"scope"`
	Readonly bool     `json:"readonly"`
}

// HasAny 是否具备任一权限点。
func (a *Admin) HasAny(perms []string) bool {
	for _, p := range perms {
		for _, q := range a.Perms {
			if p == q {
				return true
			}
		}
	}
	return false
}

// Verifier 校验三类凭证。
type Verifier struct {
	UserKeys  map[string]ed25519.PublicKey
	AdminKeys map[string]ed25519.PublicKey
	Callers   map[string]string // 调用方模块 → 凭证
}

// User 校验用户令牌，返回用户编号。没带令牌返回 ErrNoToken。
func (v *Verifier) User(r *http.Request) (string, error) {
	tok := bearer(r)
	if tok == "" {
		return "", ErrNoToken
	}
	var c UserClaims
	if err := verify(tok, v.UserKeys, "bike-acct", &c); err != nil {
		return "", err
	}
	if c.Subject == "" {
		return "", errors.New("令牌缺少用户编号")
	}
	return c.Subject, nil
}

// Admin 校验管理令牌。
func (v *Verifier) Admin(r *http.Request) (*Admin, error) {
	tok := bearer(r)
	if tok == "" {
		return nil, ErrNoToken
	}
	var c Admin
	if err := verify(tok, v.AdminKeys, "bike-iam", &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// Internal 校验服务间调用凭证，返回调用方模块名。
func (v *Verifier) Internal(r *http.Request) (string, error) {
	caller := r.Header.Get("X-Caller")
	tok := bearer(r)
	want, ok := v.Callers[caller]
	if caller == "" || tok == "" || !ok || subtle.ConstantTimeCompare([]byte(want), []byte(tok)) != 1 {
		return "", errors.New("服务凭证不对")
	}
	return caller, nil
}

// ParseCallers 解析 "模块=凭证,..."。
func ParseCallers(s string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		k, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && k != "" {
			out[k] = val
		}
	}
	return out
}
