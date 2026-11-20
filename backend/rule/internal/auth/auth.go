// Package auth 校验管理令牌（iam 签发，EdDSA）与服务间调用凭证，格式见 contracts/wire.md 第 2、3 节。
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

	"bike/rule/internal/domain"
)

// ErrToken 令牌缺失、无效或过期。
var ErrToken = errors.New("令牌无效")

// ErrCaller 服务凭证对不上。
var ErrCaller = errors.New("调用方凭证无效")

const adminIssuer = "bike-iam"

// ParsePubKeys 解析 `kid=公钥base64` 逗号分隔的公钥表。公钥可为 32 字节原始 Ed25519 公钥，或 PKIX(DER) 编码。
func ParsePubKeys(s string) (map[string]ed25519.PublicKey, error) {
	out := map[string]ed25519.PublicKey{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kid, b64, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("公钥项格式应为 kid=base64：%q", part)
		}
		kid = strings.TrimSpace(kid)
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
		if err != nil {
			return nil, fmt.Errorf("公钥 %s 不是合法 base64：%w", kid, err)
		}
		if len(raw) == ed25519.PublicKeySize {
			out[kid] = ed25519.PublicKey(raw)
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
		out[kid] = ek
	}
	return out, nil
}

// Admin 管理令牌里的身份与权限。
type Admin struct {
	AccountID string
	Name      string
	Role      string
	Client    string
	Perms     []string
	Scope     *string
	Readonly  bool
}

// Has 是否有某个权限点。
func (a *Admin) Has(p string) bool {
	for _, x := range a.Perms {
		if x == p {
			return true
		}
	}
	return false
}

// Actor 令牌对应的操作人。
func (a *Admin) Actor() domain.Actor {
	return domain.Actor{Kind: "人", ID: a.AccountID, Name: a.Name}
}

type adminClaims struct {
	Name     string   `json:"name"`
	Role     string   `json:"role"`
	Client   string   `json:"client"`
	Perms    []string `json:"perms"`
	Scope    *string  `json:"scope"`
	Readonly bool     `json:"readonly"`
	jwt.RegisteredClaims
}

// AdminVerifier 用部署配置下发的公钥验管理令牌。
type AdminVerifier struct {
	Keys map[string]ed25519.PublicKey
}

// Verify 验签、验签发方与过期。
func (v *AdminVerifier) Verify(token string) (*Admin, error) {
	var c adminClaims
	_, err := jwt.ParseWithClaims(token, &c, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		if k, ok := v.Keys[kid]; ok {
			return k, nil
		}
		if kid == "" && len(v.Keys) == 1 {
			for _, k := range v.Keys {
				return k, nil
			}
		}
		return nil, fmt.Errorf("未知密钥编号 %q", kid)
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodEdDSA.Alg()}),
		jwt.WithIssuer(adminIssuer),
		jwt.WithExpirationRequired(),
	)
	if err != nil || c.Subject == "" {
		return nil, ErrToken
	}
	return &Admin{AccountID: c.Subject, Name: c.Name, Role: c.Role, Client: c.Client, Perms: c.Perms, Scope: c.Scope, Readonly: c.Readonly}, nil
}

// Bearer 取 Authorization: Bearer 后的令牌。
func Bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	tok, ok := strings.CutPrefix(h, "Bearer ")
	tok = strings.TrimSpace(tok)
	return tok, ok && tok != ""
}

// Callers 允许的调用方 → 该调用方的凭证。
type Callers map[string]string

// ParseCallers 解析 `模块=凭证` 逗号分隔的调用方表。
func ParseCallers(s string) (Callers, error) {
	out := Callers{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, key, ok := strings.Cut(part, "=")
		if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("调用方项格式应为 模块=凭证：%q", name)
		}
		out[strings.TrimSpace(name)] = strings.TrimSpace(key)
	}
	return out, nil
}

// Check 校验 X-Caller 与其凭证，返回调用方模块名。
func (c Callers) Check(r *http.Request) (string, error) {
	name := strings.TrimSpace(r.Header.Get("X-Caller"))
	tok, ok := Bearer(r)
	want, known := c[name]
	if !ok || !known || subtle.ConstantTimeCompare([]byte(tok), []byte(want)) != 1 {
		return "", ErrCaller
	}
	return name, nil
}
