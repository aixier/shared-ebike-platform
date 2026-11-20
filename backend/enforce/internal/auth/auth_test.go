package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"errors"
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

func sign(t *testing.T, priv ed25519.PrivateKey, kid string, claims jwt.MapClaims) string {
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func baseClaims() jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss": "bike-iam", "sub": "AC0503", "name": "王＊＊", "role": "执法人员", "client": "执法端",
		"perms": []string{"evidence.view"}, "scope": nil, "readonly": false,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	}
}

func TestParsePublicKeysRawAndDER(t *testing.T) {
	p1, _ := newKey(t)
	p2, _ := newKey(t)
	der, _ := x509.MarshalPKIXPublicKey(p2)
	env := "k1=" + base64.StdEncoding.EncodeToString(p1) + ", k2=" + base64.StdEncoding.EncodeToString(der)
	keys, err := ParsePublicKeys(env)
	if err != nil {
		t.Fatal(err)
	}
	if !keys["k1"].Equal(p1) || !keys["k2"].Equal(p2) {
		t.Fatal("公钥解析不对")
	}
	if _, err := ParsePublicKeys("k1=bad"); err == nil {
		t.Fatal("坏公钥应报错")
	}
}

func TestVerifyAdminToken(t *testing.T) {
	pub, priv := newKey(t)
	v := NewAdminVerifier(map[string]ed25519.PublicKey{"k1": pub})

	c := baseClaims()
	c["badgeNo"] = "031322"
	got, err := v.Verify(sign(t, priv, "k1", c))
	if err != nil {
		t.Fatal(err)
	}
	if got.AccountID != "AC0503" || got.Role != "执法人员" || got.Name != "王＊＊" || got.BadgeNo != "031322" || got.Scope != "" || got.Readonly {
		t.Fatalf("声明解析不对：%+v", got)
	}
	if !got.HasAnyPerm("evidence.export", "evidence.view") || got.HasAnyPerm("evidence.export") {
		t.Fatal("权限点判断不对")
	}

	scoped := baseClaims()
	scoped["scope"] = "OP01"
	scoped["readonly"] = true
	got, err = v.Verify(sign(t, priv, "k1", scoped))
	if err != nil || got.Scope != "OP01" || !got.Readonly {
		t.Fatalf("数据范围解析不对：%+v %v", got, err)
	}
}

func TestVerifyRejects(t *testing.T) {
	pub, priv := newKey(t)
	_, other := newKey(t)
	v := NewAdminVerifier(map[string]ed25519.PublicKey{"k1": pub})

	expired := baseClaims()
	expired["exp"] = time.Now().Add(-time.Minute).Unix()
	wrongIss := baseClaims()
	wrongIss["iss"] = "bike-acct"

	cases := map[string]string{
		"过期":    sign(t, priv, "k1", expired),
		"签发方不对": sign(t, priv, "k1", wrongIss),
		"未知密钥":  sign(t, priv, "k9", baseClaims()),
		"签名不对":  sign(t, other, "k1", baseClaims()),
		"乱码":    "abc.def.ghi",
		"空":     "",
	}
	for name, tok := range cases {
		if _, err := v.Verify(tok); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("%s：应拒绝，得到 %v", name, err)
		}
	}
	hs := jwt.NewWithClaims(jwt.SigningMethodHS256, baseClaims())
	hs.Header["kid"] = "k1"
	s, _ := hs.SignedString([]byte("secret"))
	if _, err := v.Verify(s); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("非 EdDSA 算法应拒绝")
	}
}

func TestCallerChecker(t *testing.T) {
	c, err := ParseAllowedCallers("ops=s1, cs=s2")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Allow("ops", "s1") || c.Allow("ops", "s2") || c.Allow("ride", "s1") || c.Allow("", "") {
		t.Fatal("服务凭证判断不对")
	}
	if _, err := ParseAllowedCallers("ops"); err == nil {
		t.Fatal("格式不对应报错")
	}
	empty, _ := ParseAllowedCallers("")
	if empty.Allow("ops", "s1") {
		t.Fatal("未配置时不得放行")
	}
}
