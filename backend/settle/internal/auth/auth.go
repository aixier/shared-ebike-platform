// Package auth 校验 iam 签发的管理令牌（EdDSA），给出账号、权限点、数据范围与只读标记。
package auth

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims 是管理令牌里本服务用到的声明。
type Claims struct {
	AccountID string
	Name      string
	Role      string
	Client    string
	Perms     []string
	Scope     string // 运营商账号为其运营商编号，其余为空
	Readonly  bool
}

// Has 是否有某权限点。
func (c *Claims) Has(p string) bool {
	for _, x := range c.Perms {
		if x == p {
			return true
		}
	}
	return false
}

// HasAny 列出的权限点有其一即可。
func (c *Claims) HasAny(ps []string) bool {
	for _, p := range ps {
		if c.Has(p) {
			return true
		}
	}
	return false
}

// ParseKeys 解析 `kid=公钥base64` 逗号分隔的公钥表；公钥可为 32 字节原始公钥或 PKIX DER。
func ParseKeys(s string) (map[string]ed25519.PublicKey, error) {
	out := map[string]ed25519.PublicKey{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kid, b64, ok := strings.Cut(part, "=")
		if !ok || kid == "" {
			return nil, fmt.Errorf("公钥配置格式应为 kid=base64: %q", part)
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			if raw, err = base64.RawStdEncoding.DecodeString(b64); err != nil {
				return nil, fmt.Errorf("公钥 %s 不是 base64", kid)
			}
		}
		if len(raw) == ed25519.PublicKeySize {
			out[kid] = ed25519.PublicKey(raw)
			continue
		}
		pk, err := x509.ParsePKIXPublicKey(raw)
		if err != nil {
			return nil, fmt.Errorf("公钥 %s 无法解析: %w", kid, err)
		}
		ek, ok := pk.(ed25519.PublicKey)
		if !ok {
			return nil, fmt.Errorf("公钥 %s 不是 Ed25519", kid)
		}
		out[kid] = ek
	}
	return out, nil
}

// Verifier 管理令牌校验器。
type Verifier struct {
	keys map[string]ed25519.PublicKey
}

// NewVerifier 用公钥表构造校验器。
func NewVerifier(keys map[string]ed25519.PublicKey) *Verifier { return &Verifier{keys: keys} }

// ErrInvalid 令牌无效或过期。
var ErrInvalid = errors.New("令牌无效")

// Verify 验签、校验签发方与过期时刻，返回声明。
func (v *Verifier) Verify(token string, now time.Time) (*Claims, error) {
	p := jwt.NewParser(jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithIssuer("bike-iam"), jwt.WithExpirationRequired(), jwt.WithTimeFunc(func() time.Time { return now }))
	mc := jwt.MapClaims{}
	_, err := p.ParseWithClaims(token, mc, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		k, ok := v.keys[kid]
		if !ok {
			return nil, ErrInvalid
		}
		return k, nil
	})
	if err != nil {
		return nil, ErrInvalid
	}
	c := &Claims{}
	c.AccountID, _ = mc["sub"].(string)
	c.Name, _ = mc["name"].(string)
	c.Role, _ = mc["role"].(string)
	c.Client, _ = mc["client"].(string)
	c.Scope, _ = mc["scope"].(string)
	c.Readonly, _ = mc["readonly"].(bool)
	if ps, ok := mc["perms"].([]any); ok {
		for _, x := range ps {
			if s, ok := x.(string); ok {
				c.Perms = append(c.Perms, s)
			}
		}
	}
	if c.AccountID == "" {
		return nil, ErrInvalid
	}
	return c, nil
}
