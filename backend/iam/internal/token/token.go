// Package token 签发与校验管理令牌（EdDSA），声明见 contracts/wire.md 第 2 节。
package token

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Issuer 是管理令牌的签发方标识。
const Issuer = "bike-iam"

// Claims 是管理令牌的业务声明。
type Claims struct {
	Subject   string   // 账号编号
	Name      string   // 脱敏显示名
	BadgeNo   *string  // 执法证号：执法端令牌有，其余 nil
	Role      string   // 岗位
	Client    string   // 管理端｜执法端
	Perms     []string // 权限点
	Scope     *string  // 数据范围：运营商编号或 null
	Readonly  bool     // 是否只读
	SessionID string   // 会话编号（jti），本服务用于退出与停用后作废
	IssuedAt  time.Time
	ExpiresAt time.Time
}

type wireClaims struct {
	jwt.RegisteredClaims
	Name     string   `json:"name"`
	BadgeNo  *string  `json:"badgeNo"`
	Role     string   `json:"role"`
	Client   string   `json:"client"`
	Perms    []string `json:"perms"`
	Scope    *string  `json:"scope"`
	Readonly bool     `json:"readonly"`
}

// Signer 持私钥签发令牌。
type Signer struct {
	kid  string
	priv ed25519.PrivateKey
	ttl  time.Duration
}

// NewSigner 创建签发器。
func NewSigner(kid string, priv ed25519.PrivateKey, ttl time.Duration) *Signer {
	return &Signer{kid: kid, priv: priv, ttl: ttl}
}

// Kid 返回签发密钥编号。
func (s *Signer) Kid() string { return s.kid }

// PublicKey 返回签发私钥对应的公钥。
func (s *Signer) PublicKey() ed25519.PublicKey { return s.priv.Public().(ed25519.PublicKey) }

// Sign 签发令牌，返回令牌与过期时刻。
func (s *Signer) Sign(c Claims, now time.Time) (string, time.Time, error) {
	exp := now.Add(s.ttl)
	perms := c.Perms
	if perms == nil {
		perms = []string{}
	}
	wc := wireClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    Issuer,
			Subject:   c.Subject,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
			ID:        c.SessionID,
		},
		Name: c.Name, BadgeNo: c.BadgeNo, Role: c.Role, Client: c.Client, Perms: perms, Scope: c.Scope, Readonly: c.Readonly,
	}
	t := jwt.NewWithClaims(jwt.SigningMethodEdDSA, wc)
	t.Header["kid"] = s.kid
	str, err := t.SignedString(s.priv)
	if err != nil {
		return "", time.Time{}, err
	}
	return str, exp.Truncate(time.Second), nil
}

// Verifier 按 kid 选公钥校验令牌。
type Verifier struct {
	keys map[string]ed25519.PublicKey
}

// NewVerifier 创建校验器。
func NewVerifier(keys map[string]ed25519.PublicKey) *Verifier {
	return &Verifier{keys: keys}
}

// ErrInvalid 表示令牌无效或已过期。
var ErrInvalid = errors.New("令牌无效")

// Verify 验签、验过期、验签发方。
func (v *Verifier) Verify(tok string, now time.Time) (*Claims, error) {
	var wc wireClaims
	_, err := jwt.ParseWithClaims(tok, &wc, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		k, ok := v.keys[kid]
		if !ok {
			return nil, fmt.Errorf("未知 kid %q", kid)
		}
		return k, nil
	}, jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithIssuer(Issuer), jwt.WithExpirationRequired(), jwt.WithTimeFunc(func() time.Time { return now }))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	c := &Claims{Subject: wc.Subject, Name: wc.Name, BadgeNo: wc.BadgeNo, Role: wc.Role, Client: wc.Client, Perms: wc.Perms, Scope: wc.Scope, Readonly: wc.Readonly, SessionID: wc.ID}
	if wc.IssuedAt != nil {
		c.IssuedAt = wc.IssuedAt.Time
	}
	if wc.ExpiresAt != nil {
		c.ExpiresAt = wc.ExpiresAt.Time
	}
	return c, nil
}

// ParsePrivateKey 解析 base64 私钥：32 字节种子、64 字节完整私钥或 PKCS#8 DER。
func ParsePrivateKey(b64 string) (ed25519.PrivateKey, error) {
	raw, err := decodeB64(b64)
	if err != nil {
		return nil, err
	}
	switch len(raw) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(raw), nil
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(raw), nil
	}
	k, err := x509.ParsePKCS8PrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("私钥格式不对: %w", err)
	}
	ek, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("私钥不是 Ed25519")
	}
	return ek, nil
}

// ParsePublicKeys 解析 `kid=公钥base64` 逗号分隔的公钥表；公钥为 32 字节原始值或 PKIX DER。
func ParsePublicKeys(spec string) (map[string]ed25519.PublicKey, error) {
	out := map[string]ed25519.PublicKey{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kid, val, ok := strings.Cut(part, "=")
		if !ok || kid == "" {
			return nil, fmt.Errorf("公钥项格式应为 kid=base64: %q", part)
		}
		pub, err := parsePublicKey(val)
		if err != nil {
			return nil, fmt.Errorf("公钥 %s: %w", kid, err)
		}
		out[strings.TrimSpace(kid)] = pub
	}
	return out, nil
}

func parsePublicKey(b64 string) (ed25519.PublicKey, error) {
	raw, err := decodeB64(b64)
	if err != nil {
		return nil, err
	}
	if len(raw) == ed25519.PublicKeySize {
		return ed25519.PublicKey(raw), nil
	}
	k, err := x509.ParsePKIXPublicKey(raw)
	if err != nil {
		return nil, fmt.Errorf("公钥格式不对: %w", err)
	}
	ek, ok := k.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("公钥不是 Ed25519")
	}
	return ek, nil
}

// decodeB64 兼容标准、URL 安全、带或不带填充的 base64。
func decodeB64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, "=")
	s = strings.NewReplacer("-", "+", "_", "/").Replace(s)
	raw, err := base64.RawStdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("base64 解码失败: %w", err)
	}
	return raw, nil
}

// PublicKeyPEM 把公钥编码为 PEM（PKIX）。
func PublicKeyPEM(pub ed25519.PublicKey) string {
	der, _ := x509.MarshalPKIXPublicKey(pub)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}
