// Package auth 做两件事：校验 iam 签发的管理令牌（EdDSA），校验 /internal 调用方的服务凭证。
// 规则见 contracts/wire.md 第 2、3 节。只做验证，不做业务判断。
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

// AdminClaims 是管理令牌里本服务用到的声明。
type AdminClaims struct {
	AccountID string
	Name      string
	Role      string
	Client    string
	Perms     []string
	Scope     string // 运营商账号为运营商编号，其余为空
	Readonly  bool
}

// Has 判断是否有某个权限点。
func (c *AdminClaims) Has(perm string) bool {
	for _, p := range c.Perms {
		if p == perm {
			return true
		}
	}
	return false
}

// HasAny 列出多个时有其一即可。
func (c *AdminClaims) HasAny(perms ...string) bool {
	for _, p := range perms {
		if c.Has(p) {
			return true
		}
	}
	return false
}

// ParseKeys 解析 `kid=公钥base64` 逗号分隔的公钥表。公钥可以是 32 字节原始公钥或 PKIX DER。
func ParseKeys(s string) (map[string]ed25519.PublicKey, error) {
	out := map[string]ed25519.PublicKey{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kid, b64, ok := strings.Cut(part, "=")
		if !ok || kid == "" {
			return nil, fmt.Errorf("公钥格式应为 kid=base64: %q", part)
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("公钥 %s 不是 base64: %w", kid, err)
		}
		switch {
		case len(raw) == ed25519.PublicKeySize:
			out[kid] = ed25519.PublicKey(raw)
		default:
			k, err := x509.ParsePKIXPublicKey(raw)
			if err != nil {
				return nil, fmt.Errorf("公钥 %s 无法解析: %w", kid, err)
			}
			ek, ok := k.(ed25519.PublicKey)
			if !ok {
				return nil, fmt.Errorf("公钥 %s 不是 Ed25519", kid)
			}
			out[kid] = ek
		}
	}
	return out, nil
}

// AdminVerifier 校验管理令牌。
type AdminVerifier struct {
	keys map[string]ed25519.PublicKey
}

func NewAdminVerifier(keys map[string]ed25519.PublicKey) *AdminVerifier {
	return &AdminVerifier{keys: keys}
}

// Verify 验签、验过期、验签发方，返回声明。
func (v *AdminVerifier) Verify(token string) (*AdminClaims, error) {
	mc := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(token, mc, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		k, ok := v.keys[kid]
		if !ok {
			return nil, fmt.Errorf("未知的密钥编号 %q", kid)
		}
		return k, nil
	}, jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithIssuer("bike-iam"), jwt.WithExpirationRequired())
	if err != nil {
		return nil, err
	}
	c := &AdminClaims{}
	c.AccountID, _ = mc["sub"].(string)
	c.Name, _ = mc["name"].(string)
	c.Role, _ = mc["role"].(string)
	c.Client, _ = mc["client"].(string)
	c.Scope, _ = mc["scope"].(string)
	c.Readonly, _ = mc["readonly"].(bool)
	if ps, ok := mc["perms"].([]any); ok {
		for _, p := range ps {
			if s, ok := p.(string); ok {
				c.Perms = append(c.Perms, s)
			}
		}
	}
	if c.AccountID == "" {
		return nil, errors.New("令牌缺少 sub")
	}
	return c, nil
}

// FromRequest 从 Authorization 头取令牌并校验。
func (v *AdminVerifier) FromRequest(r *http.Request) (*AdminClaims, error) {
	tok, ok := bearer(r)
	if !ok {
		return nil, errors.New("缺少令牌")
	}
	return v.Verify(tok)
}

func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return "", false
	}
	t := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	return t, t != ""
}

// Callers 是"允许的调用方 → 凭证"表。
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
			return nil, fmt.Errorf("调用方格式应为 模块=凭证: %q", part)
		}
		out[strings.TrimSpace(name)] = strings.TrimSpace(key)
	}
	return out, nil
}

// Check 校验 X-Caller 与 Bearer 凭证，返回调用方模块名。
func (cs Callers) Check(r *http.Request) (string, bool) {
	caller := r.Header.Get("X-Caller")
	want, ok := cs[caller]
	if !ok {
		return "", false
	}
	got, ok := bearer(r)
	if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return "", false
	}
	return caller, true
}
