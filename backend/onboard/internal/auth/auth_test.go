package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func mint(t *testing.T, priv ed25519.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAdminVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keys, err := ParsePubKeys("k1=" + base64.StdEncoding.EncodeToString(pub))
	if err != nil {
		t.Fatal(err)
	}
	v := &AdminVerifier{Keys: keys}
	now := time.Now().Unix()
	good := mint(t, priv, "k1", jwt.MapClaims{"iss": "bike-iam", "sub": "AC0114", "name": "黎＊", "role": "运营商技术对接",
		"perms": []string{"integration.sandbox.own"}, "scope": "OP04", "readonly": false, "iat": now, "exp": now + 600})
	a, err := v.Verify("Bearer " + good)
	if err != nil {
		t.Fatal(err)
	}
	if a.AccountID != "AC0114" || a.Scope != "OP04" || !a.HasAny("x", "integration.sandbox.own") || a.HasAny("ops.monitor") {
		t.Fatalf("声明解析不对: %+v", a)
	}
	nullScope := mint(t, priv, "k1", jwt.MapClaims{"iss": "bike-iam", "sub": "AC0403", "perms": []string{"acceptance.confirm"}, "scope": nil, "exp": now + 600})
	if a, err := v.Verify("Bearer " + nullScope); err != nil || a.Scope != "" {
		t.Fatalf("scope 为 null 应视为全部: %v %+v", err, a)
	}
	for name, tok := range map[string]string{
		"过期":    mint(t, priv, "k1", jwt.MapClaims{"iss": "bike-iam", "sub": "A", "exp": now - 10}),
		"签发方不对": mint(t, priv, "k1", jwt.MapClaims{"iss": "bike-acct", "sub": "A", "exp": now + 600}),
		"未知密钥":  mint(t, priv, "k2", jwt.MapClaims{"iss": "bike-iam", "sub": "A", "exp": now + 600}),
	} {
		if _, err := v.Verify("Bearer " + tok); err == nil {
			t.Errorf("%s 应拒绝", name)
		}
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := v.Verify("Bearer " + mint(t, other, "k1", jwt.MapClaims{"iss": "bike-iam", "sub": "A", "exp": now + 600})); err == nil {
		t.Error("别的私钥签的应拒绝")
	}
	if _, err := v.Verify(""); err == nil {
		t.Error("没带令牌应拒绝")
	}
}

func TestCaller(t *testing.T) {
	c, err := ParseCallers("fleet=abc,ops=def")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/internal/x", nil)
	r.Header.Set("Authorization", "Bearer abc")
	r.Header.Set("X-Caller", "fleet")
	if who, ok := c.Check(r); !ok || who != "fleet" {
		t.Fatal("合法调用方应通过")
	}
	r.Header.Set("X-Caller", "ops")
	if _, ok := c.Check(r); ok {
		t.Fatal("凭证对不上应拒绝")
	}
	r.Header.Set("X-Caller", "ride")
	if _, ok := c.Check(r); ok {
		t.Fatal("不在清单里应拒绝")
	}
}
