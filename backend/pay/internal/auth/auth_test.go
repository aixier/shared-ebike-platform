package auth

// trace: wire.md 第 2、3 节（令牌校验、服务凭证校验）

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func keyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func sign(t *testing.T, priv ed25519.PrivateKey, kid string, c jwt.MapClaims) string {
	tk := jwt.NewWithClaims(jwt.SigningMethodEdDSA, c)
	tk.Header["kid"] = kid
	s, err := tk.SignedString(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TC-PAY-AUTH-01：公钥配置解析
func TestParseKeys(t *testing.T) {
	pub, _ := keyPair(t)
	ks, err := ParseKeys("k1=" + base64.StdEncoding.EncodeToString(pub))
	if err != nil || len(ks) != 1 {
		t.Fatalf("%v %v", ks, err)
	}
	if _, err := ParseKeys("k1=bad"); err == nil {
		t.Fatal("坏公钥应报错")
	}
}

// TC-PAY-AUTH-02：用户令牌验签、发行方、过期
func TestUserToken(t *testing.T) {
	pub, priv := keyPair(t)
	v := &Verifier{UserKeys: map[string]ed25519.PublicKey{"k1": pub}}
	now := time.Now()
	ok := sign(t, priv, "k1", jwt.MapClaims{"iss": "bike-acct", "sub": "U100004", "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer "+ok)
	uid, err := v.User(r)
	if err != nil || uid != "U100004" {
		t.Fatalf("%s %v", uid, err)
	}
	for name, tok := range map[string]string{
		"过期":    sign(t, priv, "k1", jwt.MapClaims{"iss": "bike-acct", "sub": "U1", "exp": now.Add(-time.Minute).Unix()}),
		"发行方错":  sign(t, priv, "k1", jwt.MapClaims{"iss": "bike-iam", "sub": "U1", "exp": now.Add(time.Hour).Unix()}),
		"未知kid": sign(t, priv, "k9", jwt.MapClaims{"iss": "bike-acct", "sub": "U1", "exp": now.Add(time.Hour).Unix()}),
	} {
		r.Header.Set("Authorization", "Bearer "+tok)
		if _, err := v.User(r); err == nil {
			t.Errorf("%s 应被拒绝", name)
		}
	}
}

// TC-PAY-AUTH-03：管理令牌权限点与只读
func TestAdminToken(t *testing.T) {
	pub, priv := keyPair(t)
	v := &Verifier{AdminKeys: map[string]ed25519.PublicKey{"a": pub}}
	tok := sign(t, priv, "a", jwt.MapClaims{"iss": "bike-iam", "sub": "AC0301", "name": "财务甲", "role": "财务", "client": "管理端",
		"perms": []string{"recon.manage"}, "scope": nil, "readonly": false, "exp": time.Now().Add(time.Hour).Unix()})
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	a, err := v.Admin(r)
	if err != nil || a.AccountID != "AC0301" || !a.HasAny("x", "recon.manage") || a.HasAny("refund.manualTransfer") {
		t.Fatalf("%+v %v", a, err)
	}
	if a.Scope != nil {
		t.Fatal("scope 应为空")
	}
}

// TC-PAY-AUTH-04：服务凭证（调用方 → 凭证表）
func TestInternalCaller(t *testing.T) {
	c, err := ParseCallers("bill=s1,ops=s2")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/", nil)
	r.Header.Set("Authorization", "Bearer s1")
	r.Header.Set("X-Caller", "bill")
	if name, ok := c.Check(r); !ok || name != "bill" {
		t.Fatal("bill 应通过")
	}
	r.Header.Set("X-Caller", "ops")
	if _, ok := c.Check(r); ok {
		t.Fatal("凭证与调用方不符应拒绝")
	}
	r.Header.Set("X-Caller", "cs")
	if _, ok := c.Check(r); ok {
		t.Fatal("未登记的调用方应拒绝")
	}
}
