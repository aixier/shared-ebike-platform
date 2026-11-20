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

func sign(t *testing.T, priv ed25519.PrivateKey, kid string, claims jwt.Claims) string {
	t.Helper()
	tk := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	tk.Header["kid"] = kid
	s, err := tk.SignedString(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestUserAndAdminTokens(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keys, err := ParseKeys("k1=" + base64.StdEncoding.EncodeToString(pub))
	if err != nil {
		t.Fatal(err)
	}
	v := &Verifier{UserKeys: keys, AdminKeys: keys}
	now := time.Now()
	ok := sign(t, priv, "k1", jwt.RegisteredClaims{Issuer: "bike-acct", Subject: "U100341", ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour))})
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer "+ok)
	if uid, err := v.User(r); err != nil || uid != "U100341" {
		t.Fatalf("合法用户令牌：%v %v", uid, err)
	}
	expired := sign(t, priv, "k1", jwt.RegisteredClaims{Issuer: "bike-acct", Subject: "U1", ExpiresAt: jwt.NewNumericDate(now.Add(-time.Minute))})
	r.Header.Set("Authorization", "Bearer "+expired)
	if _, err := v.User(r); err == nil {
		t.Fatal("过期令牌应拒绝")
	}
	wrongIss := sign(t, priv, "k1", jwt.RegisteredClaims{Issuer: "bike-iam", Subject: "U1", ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour))})
	r.Header.Set("Authorization", "Bearer "+wrongIss)
	if _, err := v.User(r); err == nil {
		t.Fatal("签发方不对应拒绝")
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	forged := sign(t, other, "k1", jwt.RegisteredClaims{Issuer: "bike-acct", Subject: "U1", ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour))})
	r.Header.Set("Authorization", "Bearer "+forged)
	if _, err := v.User(r); err == nil {
		t.Fatal("伪造签名应拒绝")
	}
	r.Header.Del("Authorization")
	if _, err := v.User(r); err != ErrNoToken {
		t.Fatal("没带令牌应返回 ErrNoToken")
	}
	scope := "OP01"
	adm := sign(t, priv, "k1", &Admin{RegisteredClaims: jwt.RegisteredClaims{Issuer: "bike-iam", Subject: "AC0101", ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour))}, Role: "客服", Perms: []string{"order.view"}, Scope: &scope})
	r.Header.Set("Authorization", "Bearer "+adm)
	a, err := v.Admin(r)
	if err != nil || a.Subject != "AC0101" || *a.Scope != "OP01" || !a.HasAny([]string{"x", "order.view"}) || a.HasAny([]string{"x"}) {
		t.Fatalf("管理令牌：%+v %v", a, err)
	}
}

func TestInternal(t *testing.T) {
	v := &Verifier{Callers: ParseCallers("opgw=k-opgw, cs=k-cs")}
	r := httptest.NewRequest("POST", "/internal/ride/x", nil)
	r.Header.Set("Authorization", "Bearer k-cs")
	r.Header.Set("X-Caller", "cs")
	if c, err := v.Internal(r); err != nil || c != "cs" {
		t.Fatalf("合法调用方：%v %v", c, err)
	}
	r.Header.Set("X-Caller", "opgw")
	if _, err := v.Internal(r); err == nil {
		t.Fatal("凭证与调用方不匹配应拒绝")
	}
	r.Header.Set("X-Caller", "bill")
	if _, err := v.Internal(r); err == nil {
		t.Fatal("不在名单的调用方应拒绝")
	}
}
