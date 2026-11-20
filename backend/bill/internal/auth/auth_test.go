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

func keypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func sign(t *testing.T, priv ed25519.PrivateKey, kid string, claims jwt.MapClaims) string {
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestParseKeys(t *testing.T) {
	pub, _ := keypair(t)
	ks, err := ParseKeys("k1=" + base64.StdEncoding.EncodeToString(pub))
	if err != nil || len(ks) != 1 {
		t.Fatalf("%v %v", ks, err)
	}
	if _, err := ParseKeys("k1=???"); err == nil {
		t.Fatal("非法公钥应报错")
	}
}

func TestUserToken(t *testing.T) {
	pub, priv := keypair(t)
	v := Verifier{UserKeys: map[string]ed25519.PublicKey{"k1": pub}}
	now := time.Now()
	good := sign(t, priv, "k1", jwt.MapClaims{"iss": "bike-acct", "sub": "U100004", "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer "+good)
	u, err := v.User(r)
	if err != nil || u != "U100004" {
		t.Fatalf("%s %v", u, err)
	}
	bad := []string{
		sign(t, priv, "k1", jwt.MapClaims{"iss": "bike-iam", "sub": "U1", "exp": now.Add(time.Hour).Unix()}),
		sign(t, priv, "k1", jwt.MapClaims{"iss": "bike-acct", "sub": "U1", "exp": now.Add(-time.Hour).Unix()}),
		sign(t, priv, "k2", jwt.MapClaims{"iss": "bike-acct", "sub": "U1", "exp": now.Add(time.Hour).Unix()}),
		"garbage",
	}
	for i, b := range bad {
		r.Header.Set("Authorization", "Bearer "+b)
		if _, err := v.User(r); err == nil {
			t.Errorf("#%d 应拒绝", i)
		}
	}
	r.Header.Del("Authorization")
	if _, err := v.User(r); err == nil {
		t.Error("无令牌应拒绝")
	}
}

func TestAdminToken(t *testing.T) {
	pub, priv := keypair(t)
	v := Verifier{AdminKeys: map[string]ed25519.PublicKey{"a1": pub}}
	now := time.Now()
	tok := sign(t, priv, "a1", jwt.MapClaims{"iss": "bike-iam", "sub": "AC0902", "name": "财务甲", "role": "财务", "client": "管理端",
		"perms": []string{"arrears.view"}, "scope": "OP01", "readonly": true, "exp": now.Add(time.Hour).Unix()})
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	a, err := v.Admin(r)
	if err != nil {
		t.Fatal(err)
	}
	if a.AccountID != "AC0902" || a.Scope != "OP01" || !a.Readonly || !a.Has("arrears.view") || a.Has("order.view") {
		t.Fatalf("%+v", a)
	}
	if !a.HasAny("order.view", "arrears.view") {
		t.Fatal("列出多个权限时有其一即可")
	}
}

func TestInternalCaller(t *testing.T) {
	c, err := ParseCallers("cs=secret1,ops=secret2")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/", nil)
	r.Header.Set("Authorization", "Bearer secret1")
	r.Header.Set("X-Caller", "cs")
	if name, ok := c.Check(r); !ok || name != "cs" {
		t.Fatal("应通过")
	}
	r.Header.Set("X-Caller", "ops")
	if _, ok := c.Check(r); ok {
		t.Fatal("凭证与调用方不符应拒绝")
	}
	r.Header.Set("X-Caller", "ride")
	if _, ok := c.Check(r); ok {
		t.Fatal("不在名单应拒绝")
	}
}
