package token

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func TestParseKeysFormats(t *testing.T) {
	pub, priv := newKey(t)
	// 私钥：32 字节种子、64 字节、PKCS8 DER 都接受
	der, _ := x509.MarshalPKCS8PrivateKey(priv)
	for _, s := range []string{
		base64.StdEncoding.EncodeToString(priv.Seed()),
		base64.StdEncoding.EncodeToString(priv),
		base64.StdEncoding.EncodeToString(der),
	} {
		k, err := ParsePrivateKey(s)
		if err != nil || !k.Equal(priv) {
			t.Fatalf("私钥解析失败: %v", err)
		}
	}
	pkix, _ := x509.MarshalPKIXPublicKey(pub)
	env := "k1=" + base64.StdEncoding.EncodeToString(pub) + ", k2=" + base64.StdEncoding.EncodeToString(pkix)
	m, err := ParsePublicKeys(env)
	if err != nil || len(m) != 2 || !m["k1"].Equal(pub) || !m["k2"].Equal(pub) {
		t.Fatalf("公钥解析失败: %v %v", err, m)
	}
	if _, err := ParsePublicKeys("bad"); err == nil {
		t.Fatal("缺 kid 应报错")
	}
	if m, err := ParsePublicKeys(""); err != nil || len(m) != 0 {
		t.Fatal("空串应得空集合")
	}
	if !strings.Contains(PublicPEM(pub), "BEGIN PUBLIC KEY") {
		t.Fatal("PEM 格式不对")
	}
}

func TestUserTokenRoundTrip(t *testing.T) {
	pub, priv := newKey(t)
	iss := &Issuer{Kid: "u1", Key: priv, TTL: time.Hour}
	now := time.Now()
	tok, jti, exp, err := iss.Issue("U100004", now)
	if err != nil || jti == "" || !exp.Equal(now.Add(time.Hour).Truncate(time.Second)) {
		t.Fatalf("签发失败: %v %v", err, exp)
	}
	keys := map[string]ed25519.PublicKey{"u1": pub}
	c, err := VerifyUser(keys, tok, now)
	if err != nil || c.UserID != "U100004" || c.ID != jti {
		t.Fatalf("验签失败: %v %+v", err, c)
	}
	// 过期
	if _, err := VerifyUser(keys, tok, now.Add(2*time.Hour)); err == nil {
		t.Fatal("过期令牌应拒绝")
	}
	// kid 不认识
	if _, err := VerifyUser(map[string]ed25519.PublicKey{"other": pub}, tok, now); err == nil {
		t.Fatal("未知 kid 应拒绝")
	}
	// 别的私钥签的
	_, priv2 := newKey(t)
	tok2, _, _, _ := (&Issuer{Kid: "u1", Key: priv2, TTL: time.Hour}).Issue("U1", now)
	if _, err := VerifyUser(keys, tok2, now); err == nil {
		t.Fatal("签名不符应拒绝")
	}
	// 管理令牌（iss 不对）不能当用户令牌用
	adm := signAdmin(t, priv, "u1", jwt.MapClaims{"iss": "bike-iam", "sub": "AC0301", "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
	if _, err := VerifyUser(keys, adm, now); err == nil {
		t.Fatal("iss 不符应拒绝")
	}
}

func signAdmin(t *testing.T, priv ed25519.PrivateKey, kid string, c jwt.MapClaims) string {
	tk := jwt.NewWithClaims(jwt.SigningMethodEdDSA, c)
	tk.Header["kid"] = kid
	s, err := tk.SignedString(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAdminToken(t *testing.T) {
	pub, priv := newKey(t)
	now := time.Now()
	keys := map[string]ed25519.PublicKey{"a1": pub}
	s := signAdmin(t, priv, "a1", jwt.MapClaims{
		"iss": "bike-iam", "sub": "AC0301", "name": "苏＊＊", "role": "客服", "client": "管理端",
		"perms": []string{"user.view"}, "scope": nil, "readonly": false,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})
	c, err := VerifyAdmin(keys, s, now)
	if err != nil || c.AccountID != "AC0301" || !c.HasAny("account.unfreeze", "user.view") || c.HasAny("x") || c.Scope != nil {
		t.Fatalf("管理令牌解析不对: %v %+v", err, c)
	}
	s2 := signAdmin(t, priv, "a1", jwt.MapClaims{"iss": "bike-iam", "sub": "AC0101", "scope": "OP01", "perms": []string{}, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
	c2, err := VerifyAdmin(keys, s2, now)
	if err != nil || c2.Scope == nil || *c2.Scope != "OP01" {
		t.Fatalf("scope 解析不对: %v %+v", err, c2)
	}
}
