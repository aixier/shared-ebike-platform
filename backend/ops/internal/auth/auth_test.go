package auth

import (
	"crypto/ed25519"
	"crypto/rand"
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

func TestAdminVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	v, err := NewAdminVerifier("k1=" + base64.StdEncoding.EncodeToString(pub))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	good := sign(t, priv, "k1", jwt.MapClaims{
		"iss": "bike-iam", "sub": "AC0101", "name": "王*", "role": "运维值班", "client": "管理端",
		"perms": []string{"ops.monitor", "degrade.control"}, "scope": nil, "readonly": false,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})
	a, err := v.Verify(good)
	if err != nil {
		t.Fatalf("合法令牌应通过：%v", err)
	}
	if a.AccountID != "AC0101" || a.Scope != "" || !a.HasAny("degrade.control") || a.HasAny("access.pause") {
		t.Fatalf("声明解析不对：%+v", a)
	}

	op := sign(t, priv, "k1", jwt.MapClaims{"iss": "bike-iam", "sub": "AC0301", "perms": []string{"access.view.own"}, "scope": "OP02", "readonly": true, "exp": now.Add(time.Hour).Unix()})
	a, err = v.Verify(op)
	if err != nil || a.Scope != "OP02" || !a.Readonly {
		t.Fatalf("运营商令牌应带数据范围与只读：%+v %v", a, err)
	}

	expired := sign(t, priv, "k1", jwt.MapClaims{"iss": "bike-iam", "sub": "AC1", "exp": now.Add(-time.Minute).Unix()})
	if _, err := v.Verify(expired); err == nil {
		t.Fatal("过期令牌应拒绝")
	}
	wrongIss := sign(t, priv, "k1", jwt.MapClaims{"iss": "bike-acct", "sub": "U100001", "exp": now.Add(time.Hour).Unix()})
	if _, err := v.Verify(wrongIss); err == nil {
		t.Fatal("用户令牌不能当管理令牌")
	}
	unknownKid := sign(t, priv, "k9", jwt.MapClaims{"iss": "bike-iam", "sub": "AC1", "exp": now.Add(time.Hour).Unix()})
	if _, err := v.Verify(unknownKid); err == nil {
		t.Fatal("未知 kid 应拒绝")
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	forged := sign(t, other, "k1", jwt.MapClaims{"iss": "bike-iam", "sub": "AC1", "exp": now.Add(time.Hour).Unix()})
	if _, err := v.Verify(forged); err == nil {
		t.Fatal("别的私钥签的应拒绝")
	}
}

func TestCallers(t *testing.T) {
	c := NewCallerVerifier("ride=abc, cs=def")
	if !c.Verify("ride", "abc") || c.Verify("ride", "def") || c.Verify("pay", "abc") || c.Verify("", "") {
		t.Fatal("服务凭证按调用方逐一核对")
	}
}
