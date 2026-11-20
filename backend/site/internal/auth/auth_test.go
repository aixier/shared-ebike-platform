package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func sign(t *testing.T, priv ed25519.PrivateKey, kid string, claims jwt.MapClaims) string {
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func base(perms ...string) jwt.MapClaims {
	return jwt.MapClaims{"iss": "bike-iam", "sub": "AC0001", "name": "王＊", "role": "主管部门", "client": "管理端",
		"perms": perms, "scope": nil, "readonly": false, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}
}

func TestAdminVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	v := NewAdminVerifier(map[string]ed25519.PublicKey{"k1": pub})
	a, err := v.Verify("Bearer " + sign(t, priv, "k1", base("station.edit")))
	if err != nil {
		t.Fatal(err)
	}
	if a.AccountID != "AC0001" || a.Name != "王＊" || !a.Has("station.edit") || a.Has("pile.edit") {
		t.Fatalf("声明解析错误: %+v", a)
	}
	if !a.HasAny() || !a.HasAny("pile.edit", "station.edit") || a.HasAny("pile.edit") {
		t.Fatal("权限点判断错误")
	}

	// 过期、错误签发方、未知 kid、错误签名、缺令牌
	exp := base()
	exp["exp"] = time.Now().Add(-time.Minute).Unix()
	iss := base()
	iss["iss"] = "bike-acct"
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	for name, h := range map[string]string{
		"过期":    "Bearer " + sign(t, priv, "k1", exp),
		"签发方":   "Bearer " + sign(t, priv, "k1", iss),
		"未知kid": "Bearer " + sign(t, priv, "k9", base()),
		"错签名":   "Bearer " + sign(t, other, "k1", base()),
		"空":     "",
		"格式":    "Basic abc",
	} {
		if _, err := v.Verify(h); err == nil {
			t.Errorf("%s 的令牌应被拒绝", name)
		}
	}
}

func TestScopeReadonly(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	v := NewAdminVerifier(map[string]ed25519.PublicKey{"k1": pub})
	c := base()
	c["scope"] = "OP01"
	c["readonly"] = true
	a, err := v.Verify("Bearer " + sign(t, priv, "k1", c))
	if err != nil {
		t.Fatal(err)
	}
	if a.Scope != "OP01" || !a.Readonly {
		t.Fatalf("scope/readonly 解析错误: %+v", a)
	}
}

func TestCaller(t *testing.T) {
	cv := CallerVerifier{Allowed: map[string]string{"ride": "secret-r"}}
	r := httptest.NewRequest("POST", "/internal/site/x", nil)
	r.Header.Set("Authorization", "Bearer secret-r")
	r.Header.Set("X-Caller", "ride")
	if name, ok := cv.Verify(r); !ok || name != "ride" {
		t.Fatal("合法调用方应通过")
	}
	r.Header.Set("Authorization", "Bearer wrong")
	if _, ok := cv.Verify(r); ok {
		t.Fatal("凭证不符应拒绝")
	}
	r.Header.Set("X-Caller", "ops")
	r.Header.Set("Authorization", "Bearer secret-r")
	if _, ok := cv.Verify(r); ok {
		t.Fatal("未登记的调用方应拒绝")
	}
}

func TestSecretEqual(t *testing.T) {
	if !SecretEqual("abc", "abc") || SecretEqual("abc", "abd") || SecretEqual("", "") {
		t.Fatal("密钥比较错误（空密钥不得视为相等）")
	}
}
