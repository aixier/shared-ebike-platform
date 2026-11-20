package token

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func TestParsePrivateKeyFormats(t *testing.T) {
	_, priv := newKey(t)
	pk8, _ := x509.MarshalPKCS8PrivateKey(priv)
	for name, s := range map[string]string{
		"种子32字节": base64.StdEncoding.EncodeToString(priv.Seed()),
		"完整64字节": base64.StdEncoding.EncodeToString(priv),
		"PKCS8":  base64.StdEncoding.EncodeToString(pk8),
	} {
		got, err := ParsePrivateKey(s)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !got.Equal(priv) {
			t.Fatalf("%s: 私钥不一致", name)
		}
	}
	if _, err := ParsePrivateKey("bad!!"); err == nil {
		t.Fatal("非法私钥应报错")
	}
}

func TestParsePublicKeys(t *testing.T) {
	pub1, _ := newKey(t)
	pub2, _ := newKey(t)
	der, _ := x509.MarshalPKIXPublicKey(pub2)
	spec := "k1=" + base64.StdEncoding.EncodeToString(pub1) + ", k2=" + base64.StdEncoding.EncodeToString(der)
	keys, err := ParsePublicKeys(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || !keys["k1"].Equal(pub1) || !keys["k2"].Equal(pub2) {
		t.Fatalf("公钥解析不对: %v", keys)
	}
	if k, err := ParsePublicKeys(""); err != nil || len(k) != 0 {
		t.Fatal("空串应为空集合")
	}
	if _, err := ParsePublicKeys("k1"); err == nil {
		t.Fatal("缺等号应报错")
	}
}

func TestSignAndVerify(t *testing.T) {
	pub, priv := newKey(t)
	s := NewSigner("2026a", priv, 8*time.Hour)
	now := time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)
	scope := "OP01"
	tok, exp, err := s.Sign(Claims{Subject: "AC0101", Name: "何＊＊", Role: "运营商业务", Client: "管理端", Perms: []string{"order.view.own"}, Scope: &scope, Readonly: false, SessionID: "S1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Equal(now.Add(8 * time.Hour)) {
		t.Fatalf("过期时刻不对: %v", exp)
	}
	// 头部带 kid、alg=EdDSA
	head, _ := base64.RawURLEncoding.DecodeString(strings.Split(tok, ".")[0])
	var h map[string]any
	_ = json.Unmarshal(head, &h)
	if h["kid"] != "2026a" || h["alg"] != "EdDSA" {
		t.Fatalf("头部不对: %v", h)
	}
	// 载荷按 wire.md 第 2 节的声明名
	body, _ := base64.RawURLEncoding.DecodeString(strings.Split(tok, ".")[1])
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	for _, k := range []string{"iss", "sub", "name", "role", "client", "perms", "scope", "readonly", "iat", "exp"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("缺少声明 %s: %v", k, m)
		}
	}
	if m["iss"] != "bike-iam" || m["scope"] != "OP01" {
		t.Fatalf("声明值不对: %v", m)
	}

	v := NewVerifier(map[string]ed25519.PublicKey{"2026a": pub})
	c, err := v.Verify(tok, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject != "AC0101" || c.Role != "运营商业务" || c.Scope == nil || *c.Scope != "OP01" || c.SessionID != "S1" || len(c.Perms) != 1 {
		t.Fatalf("解析结果不对: %+v", c)
	}
	if _, err := v.Verify(tok, now.Add(9*time.Hour)); err == nil {
		t.Fatal("过期令牌应拒绝")
	}
}

func TestScopeNullForPlatformAccount(t *testing.T) {
	_, priv := newKey(t)
	s := NewSigner("k", priv, time.Hour)
	tok, _, _ := s.Sign(Claims{Subject: "AC0001", Perms: []string{}}, time.Now())
	body, _ := base64.RawURLEncoding.DecodeString(strings.Split(tok, ".")[1])
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	if v, ok := m["scope"]; !ok || v != nil {
		t.Fatalf("非运营商账号 scope 应为 null: %v", m)
	}
}

func TestVerifyRejects(t *testing.T) {
	pub, priv := newKey(t)
	_, other := newKey(t)
	now := time.Now()
	v := NewVerifier(map[string]ed25519.PublicKey{"k": pub})
	// 别的私钥签的
	tok, _, _ := NewSigner("k", other, time.Hour).Sign(Claims{Subject: "AC0001"}, now)
	if _, err := v.Verify(tok, now); err == nil {
		t.Fatal("签名不符应拒绝")
	}
	// 未知 kid
	tok, _, _ = NewSigner("x", priv, time.Hour).Sign(Claims{Subject: "AC0001"}, now)
	if _, err := v.Verify(tok, now); err == nil {
		t.Fatal("未知 kid 应拒绝")
	}
	if _, err := v.Verify("abc", now); err == nil {
		t.Fatal("乱码应拒绝")
	}
}

func TestPublicKeyPEM(t *testing.T) {
	pub, _ := newKey(t)
	p := PublicKeyPEM(pub)
	if !strings.HasPrefix(p, "-----BEGIN PUBLIC KEY-----") {
		t.Fatalf("PEM 不对: %s", p)
	}
}
