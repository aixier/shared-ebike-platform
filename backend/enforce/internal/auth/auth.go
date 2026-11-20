// Package auth 校验 iam 签发的管理令牌（EdDSA）与模块间服务凭证（contracts/wire.md 第 2、3 节）。
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

// ErrInvalidToken 表示令牌缺失、签名不对、过期或签发方不对。
var ErrInvalidToken = errors.New("令牌无效")

// Admin 是管理令牌里本服务用到的声明。
type Admin struct {
	AccountID string
	Name      string
	Role      string
	Client    string
	Perms     []string
	Scope     string // 空串表示不限运营商
	Readonly  bool
	BadgeNo   string // 契约未列出的声明；iam 给出时使用
}

// HasAnyPerm 列出多个权限点时有其一即可。
func (a Admin) HasAnyPerm(perms ...string) bool {
	for _, want := range perms {
		for _, p := range a.Perms {
			if p == want {
				return true
			}
		}
	}
	return false
}

// ParsePublicKeys 解析 `kid=公钥base64` 逗号分隔的配置；公钥可为 32 字节原始值或 PKIX DER。
func ParsePublicKeys(env string) (map[string]ed25519.PublicKey, error) {
	keys := map[string]ed25519.PublicKey{}
	for _, item := range strings.Split(env, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		kid, b64, ok := strings.Cut(item, "=")
		if !ok || kid == "" {
			return nil, fmt.Errorf("公钥配置格式不对：%q", item)
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
		if err != nil {
			return nil, fmt.Errorf("公钥 %s 不是 base64：%w", kid, err)
		}
		if len(raw) == ed25519.PublicKeySize {
			keys[kid] = ed25519.PublicKey(raw)
			continue
		}
		pk, err := x509.ParsePKIXPublicKey(raw)
		if err != nil {
			return nil, fmt.Errorf("公钥 %s 无法解析：%w", kid, err)
		}
		ek, ok := pk.(ed25519.PublicKey)
		if !ok {
			return nil, fmt.Errorf("公钥 %s 不是 Ed25519", kid)
		}
		keys[kid] = ek
	}
	return keys, nil
}

// AdminVerifier 用部署配置下发的公钥验管理令牌。
type AdminVerifier struct {
	keys map[string]ed25519.PublicKey
}

func NewAdminVerifier(keys map[string]ed25519.PublicKey) *AdminVerifier {
	return &AdminVerifier{keys: keys}
}

type adminClaims struct {
	Name     string   `json:"name"`
	Role     string   `json:"role"`
	Client   string   `json:"client"`
	Perms    []string `json:"perms"`
	Scope    *string  `json:"scope"`
	Readonly bool     `json:"readonly"`
	BadgeNo  string   `json:"badgeNo"`
	jwt.RegisteredClaims
}

// Verify 验签、验过期与签发方，返回声明。
func (v *AdminVerifier) Verify(token string) (Admin, error) {
	if token == "" {
		return Admin{}, ErrInvalidToken
	}
	var c adminClaims
	_, err := jwt.ParseWithClaims(token, &c, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		k, ok := v.keys[kid]
		if !ok {
			return nil, errors.New("未知密钥编号")
		}
		return k, nil
	}, jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithIssuer("bike-iam"), jwt.WithExpirationRequired())
	if err != nil || c.Subject == "" {
		return Admin{}, ErrInvalidToken
	}
	a := Admin{AccountID: c.Subject, Name: c.Name, Role: c.Role, Client: c.Client, Perms: c.Perms, Readonly: c.Readonly, BadgeNo: c.BadgeNo}
	if c.Scope != nil {
		a.Scope = *c.Scope
	}
	return a, nil
}

// CallerChecker 校验 /internal 调用方的服务凭证。
type CallerChecker struct {
	allowed map[string]string
}

// ParseAllowedCallers 解析 `模块=凭证` 逗号分隔的配置。
func ParseAllowedCallers(env string) (*CallerChecker, error) {
	m := map[string]string{}
	for _, item := range strings.Split(env, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		name, key, ok := strings.Cut(item, "=")
		if !ok || name == "" || key == "" {
			return nil, fmt.Errorf("调用方配置格式不对：%q", item)
		}
		m[strings.TrimSpace(name)] = strings.TrimSpace(key)
	}
	return &CallerChecker{allowed: m}, nil
}

// Allow 判断调用方与凭证是否对得上。
func (c *CallerChecker) Allow(caller, key string) bool {
	want, ok := c.allowed[caller]
	if !ok || key == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(key)) == 1
}
