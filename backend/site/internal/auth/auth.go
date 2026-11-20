// Package auth 管理令牌验签、权限点、服务凭证与密钥比较（contracts/wire.md 第 2、3 节）。
package auth

import (
	"crypto/ed25519"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// Admin 管理令牌里的声明
type Admin struct {
	AccountID string
	Name      string
	Role      string
	Client    string
	Perms     map[string]bool
	Scope     string // 运营商编号；非运营商账号为空
	Readonly  bool
}

// Has 是否有某权限点
func (a Admin) Has(p string) bool { return a.Perms[p] }

// HasAny 有其一即可；不列权限点表示登录即可
func (a Admin) HasAny(ps ...string) bool {
	if len(ps) == 0 {
		return true
	}
	for _, p := range ps {
		if a.Perms[p] {
			return true
		}
	}
	return false
}

// AdminVerifier 用部署配置给的公钥验管理令牌
type AdminVerifier struct{ keys map[string]ed25519.PublicKey }

// NewAdminVerifier 新建
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
	jwt.RegisteredClaims
}

// ErrNoToken 没带令牌
var ErrNoToken = errors.New("没有登录令牌")

// Verify 校验 Authorization 头
func (v *AdminVerifier) Verify(header string) (Admin, error) {
	raw, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || strings.TrimSpace(raw) == "" {
		return Admin{}, ErrNoToken
	}
	var c adminClaims
	_, err := jwt.ParseWithClaims(strings.TrimSpace(raw), &c, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		k, ok := v.keys[kid]
		if !ok {
			return nil, errors.New("未知的密钥编号")
		}
		return k, nil
	}, jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithIssuer("bike-iam"), jwt.WithExpirationRequired())
	if err != nil {
		return Admin{}, err
	}
	a := Admin{AccountID: c.Subject, Name: c.Name, Role: c.Role, Client: c.Client, Readonly: c.Readonly, Perms: map[string]bool{}}
	for _, p := range c.Perms {
		a.Perms[p] = true
	}
	if c.Scope != nil {
		a.Scope = *c.Scope
	}
	return a, nil
}

// CallerVerifier 校验 /internal 调用方的服务凭证
type CallerVerifier struct{ Allowed map[string]string }

// Verify 返回调用方模块名
func (cv CallerVerifier) Verify(r *http.Request) (string, bool) {
	name := r.Header.Get("X-Caller")
	want, ok := cv.Allowed[name]
	got, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || !SecretEqual(got, want) {
		return "", false
	}
	return name, true
}

// SecretEqual 定长时间比较；空密钥一律不相等
func SecretEqual(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
