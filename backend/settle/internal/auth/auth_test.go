package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func sign(t *testing.T, priv ed25519.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pub2, priv2, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(pub2)
	keys, err := ParseKeys("k1=" + base64.StdEncoding.EncodeToString(pub) + ",k2=" + base64.StdEncoding.EncodeToString(der))
	if err != nil {
		t.Fatal(err)
	}
	v := NewVerifier(keys)
	now := time.Now()
	base := jwt.MapClaims{"iss": "bike-iam", "sub": "AC0101", "name": "何＊＊", "role": "运营商业务", "client": "管理端", "perms": []string{"order.view.own"}, "scope": "OP01", "readonly": false, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()}
	c, err := v.Verify(sign(t, priv, "k1", base), now)
	if err != nil {
		t.Fatal(err)
	}
	if c.AccountID != "AC0101" || c.Scope != "OP01" || !c.Has("order.view.own") || c.Has("settlement.manage") || c.Readonly {
		t.Errorf("claims = %+v", c)
	}
	// 第二把公钥（DER 格式）
	fin := jwt.MapClaims{"iss": "bike-iam", "sub": "AC0201", "perms": []string{"settlement.manage"}, "scope": nil, "exp": now.Add(time.Hour).Unix()}
	c2, err := v.Verify(sign(t, priv2, "k2", fin), now)
	if err != nil || c2.Scope != "" || !c2.HasAny([]string{"x", "settlement.manage"}) {
		t.Fatalf("c2 = %+v %v", c2, err)
	}
	// 过期、签发方不对、未知 kid、签名不对
	exp := jwt.MapClaims{"iss": "bike-iam", "sub": "A", "exp": now.Add(-time.Minute).Unix()}
	if _, err := v.Verify(sign(t, priv, "k1", exp), now); err == nil {
		t.Error("过期应拒绝")
	}
	iss := jwt.MapClaims{"iss": "bike-acct", "sub": "U1", "exp": now.Add(time.Hour).Unix()}
	if _, err := v.Verify(sign(t, priv, "k1", iss), now); err == nil {
		t.Error("用户令牌应拒绝")
	}
	if _, err := v.Verify(sign(t, priv, "k9", base), now); err == nil {
		t.Error("未知 kid 应拒绝")
	}
	if _, err := v.Verify(sign(t, priv2, "k1", base), now); err == nil {
		t.Error("签名不对应拒绝")
	}
	if _, err := v.Verify("abc", now); err == nil {
		t.Error("乱码应拒绝")
	}
}

func TestParseKeysErrors(t *testing.T) {
	if _, err := ParseKeys("k1"); err == nil {
		t.Error("缺等号应报错")
	}
	if _, err := ParseKeys("k1=!!!"); err == nil {
		t.Error("非 base64 应报错")
	}
	m, err := ParseKeys("")
	if err != nil || len(m) != 0 {
		t.Error("空串应得空表")
	}
}
