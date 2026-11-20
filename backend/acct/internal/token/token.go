// Package token 用户令牌签发与校验、管理令牌校验（EdDSA），格式见 contracts/wire.md 第 2 节。
package token

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	UserIssuer  = "bike-acct"
	AdminIssuer = "bike-iam"
)

// ParsePrivateKey 解析 base64 私钥：32 字节种子、64 字节私钥或 PKCS8 DER
func ParsePrivateKey(s string) (ed25519.PrivateKey, error) {
	b, err := decodeB64(s)
	if err != nil {
		return nil, err
	}
	switch len(b) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(b), nil
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(b), nil
	}
	k, err := x509.ParsePKCS8PrivateKey(b)
	if err != nil {
		return nil, fmt.Errorf("私钥格式不对: %w", err)
	}
	p, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("私钥不是 Ed25519")
	}
	return p, nil
}

// ParsePublicKeys 解析 "kid=base64,kid=base64"；公钥可为 32 字节原始值或 PKIX DER
func ParsePublicKeys(env string) (map[string]ed25519.PublicKey, error) {
	out := map[string]ed25519.PublicKey{}
	for _, part := range strings.Split(env, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kid, val, ok := strings.Cut(part, "=")
		if !ok || kid == "" {
			return nil, fmt.Errorf("公钥项缺 kid: %q", part)
		}
		pub, err := parsePublic(val)
		if err != nil {
			return nil, fmt.Errorf("公钥 %s: %w", kid, err)
		}
		out[kid] = pub
	}
	return out, nil
}

func parsePublic(s string) (ed25519.PublicKey, error) {
	b, err := decodeB64(s)
	if err != nil {
		return nil, err
	}
	if len(b) == ed25519.PublicKeySize {
		return ed25519.PublicKey(b), nil
	}
	k, err := x509.ParsePKIXPublicKey(b)
	if err != nil {
		return nil, err
	}
	p, ok := k.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("不是 Ed25519 公钥")
	}
	return p, nil
}

// decodeB64 兼容标准与 URL 编码、带不带填充；base64 里的 "=" 填充只在末尾
func decodeB64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("不是 base64")
}

// PublicPEM 公钥 PEM（PKIX）
func PublicPEM(pub ed25519.PublicKey) string {
	der, _ := x509.MarshalPKIXPublicKey(pub)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// Issuer 用户令牌签发方
type Issuer struct {
	Kid string
	Key ed25519.PrivateKey
	TTL time.Duration
}

// Issue 签发用户令牌，返回令牌、令牌编号（jti，用于作废）与过期时刻
func (i *Issuer) Issue(userID string, now time.Time) (string, string, time.Time, error) {
	jti := randHex(16)
	now = now.Truncate(time.Second)
	exp := now.Add(i.TTL)
	c := jwt.MapClaims{"iss": UserIssuer, "sub": userID, "iat": now.Unix(), "exp": exp.Unix(), "jti": jti}
	t := jwt.NewWithClaims(jwt.SigningMethodEdDSA, c)
	t.Header["kid"] = i.Kid
	s, err := t.SignedString(i.Key)
	return s, jti, exp, err
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// UserClaims 用户令牌里的声明
type UserClaims struct {
	UserID   string
	ID       string
	IssuedAt time.Time
	Expires  time.Time
}

// AdminClaims 管理令牌里的声明
type AdminClaims struct {
	AccountID string
	Name      string
	Role      string
	Client    string
	Perms     []string
	Scope     *string
	Readonly  bool
}

// HasAny 有其一即可
func (a AdminClaims) HasAny(perms ...string) bool {
	for _, p := range perms {
		for _, q := range a.Perms {
			if p == q {
				return true
			}
		}
	}
	return false
}

func parse(keys map[string]ed25519.PublicKey, raw, issuer string, now time.Time) (jwt.MapClaims, error) {
	c := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(raw, c, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		k, ok := keys[kid]
		if !ok {
			return nil, fmt.Errorf("未知 kid %q", kid)
		}
		return k, nil
	}, jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithIssuer(issuer), jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(func() time.Time { return now }))
	if err != nil {
		return nil, err
	}
	return c, nil
}

// VerifyUser 校验用户令牌
func VerifyUser(keys map[string]ed25519.PublicKey, raw string, now time.Time) (UserClaims, error) {
	c, err := parse(keys, raw, UserIssuer, now)
	if err != nil {
		return UserClaims{}, err
	}
	sub, _ := c.GetSubject()
	if sub == "" {
		return UserClaims{}, errors.New("缺 sub")
	}
	out := UserClaims{UserID: sub}
	out.ID, _ = c["jti"].(string)
	if v, err := c.GetIssuedAt(); err == nil && v != nil {
		out.IssuedAt = v.Time
	}
	if v, err := c.GetExpirationTime(); err == nil && v != nil {
		out.Expires = v.Time
	}
	return out, nil
}

// VerifyAdmin 校验管理令牌
func VerifyAdmin(keys map[string]ed25519.PublicKey, raw string, now time.Time) (AdminClaims, error) {
	c, err := parse(keys, raw, AdminIssuer, now)
	if err != nil {
		return AdminClaims{}, err
	}
	out := AdminClaims{}
	out.AccountID, _ = c.GetSubject()
	if out.AccountID == "" {
		return AdminClaims{}, errors.New("缺 sub")
	}
	out.Name, _ = c["name"].(string)
	out.Role, _ = c["role"].(string)
	out.Client, _ = c["client"].(string)
	out.Readonly, _ = c["readonly"].(bool)
	if s, ok := c["scope"].(string); ok && s != "" {
		out.Scope = &s
	}
	if ps, ok := c["perms"].([]any); ok {
		for _, p := range ps {
			if s, ok := p.(string); ok {
				out.Perms = append(out.Perms, s)
			}
		}
	}
	return out, nil
}
