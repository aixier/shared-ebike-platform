package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// trace: contracts/wire.md 第 2、3 节 任务 4

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func sign(t *testing.T, priv ed25519.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	if kid != "" {
		tok.Header["kid"] = kid
	}
	s, err := tok.SignedString(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func claims(exp time.Duration) jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss": "bike-iam", "sub": "AC0001", "name": "李＊＊", "role": "主管部门管理员", "client": "管理端",
		"perms": []string{"pricing.view", "pricing.edit"}, "scope": nil, "readonly": false,
		"iat": now.Unix(), "exp": now.Add(exp).Unix(),
	}
}

func TestParsePubKeys_原始与PKIX两种编码(t *testing.T) {
	pub1, _ := newKey(t)
	pub2, _ := newKey(t)
	der, _ := x509.MarshalPKIXPublicKey(pub2)
	env := "k1=" + base64.StdEncoding.EncodeToString(pub1) + ", k2=" + base64.StdEncoding.EncodeToString(der)
	keys, err := ParsePubKeys(env)
	if err != nil {
		t.Fatal(err)
	}
	if !keys["k1"].Equal(pub1) || !keys["k2"].Equal(pub2) {
		t.Error("公钥解析不对")
	}
	if _, err := ParsePubKeys("k1=abc"); err == nil {
		t.Error("坏公钥应报错")
	}
	if keys, err := ParsePubKeys(""); err != nil || len(keys) != 0 {
		t.Error("空串应得到空表")
	}
}

func TestVerifyAdmin(t *testing.T) {
	pub, priv := newKey(t)
	_, other := newKey(t)
	v := &AdminVerifier{Keys: map[string]ed25519.PublicKey{"k1": pub}}

	a, err := v.Verify(sign(t, priv, "k1", claims(time.Hour)))
	if err != nil {
		t.Fatalf("合法令牌应通过：%v", err)
	}
	if a.AccountID != "AC0001" || a.Role != "主管部门管理员" || !a.Has("pricing.edit") || a.Has("x") || a.Readonly || a.Scope != nil {
		t.Errorf("声明解析不对：%+v", a)
	}
	act := a.Actor()
	if act.Kind != "人" || act.ID != "AC0001" || act.Name != "李＊＊" {
		t.Errorf("操作人不对：%+v", act)
	}
	// 只有一把公钥时可不带 kid
	if _, err := v.Verify(sign(t, priv, "", claims(time.Hour))); err != nil {
		t.Errorf("单把公钥不带 kid 应通过：%v", err)
	}
	bad := []string{
		sign(t, priv, "k1", claims(-time.Minute)),
		sign(t, other, "k1", claims(time.Hour)),
		sign(t, priv, "k9", claims(time.Hour)),
		"garbage",
	}
	c := claims(time.Hour)
	c["iss"] = "bike-acct"
	bad = append(bad, sign(t, priv, "k1", c))
	c = claims(time.Hour)
	delete(c, "exp")
	bad = append(bad, sign(t, priv, "k1", c))
	for i, tok := range bad {
		if _, err := v.Verify(tok); err == nil {
			t.Errorf("第 %d 个坏令牌应拒绝", i)
		}
	}
	c = claims(time.Hour)
	c["scope"] = "OP01"
	c["readonly"] = true
	a, err = v.Verify(sign(t, priv, "k1", c))
	if err != nil || a.Scope == nil || *a.Scope != "OP01" || !a.Readonly {
		t.Errorf("范围与只读解析不对：%+v %v", a, err)
	}
}

func TestBearer(t *testing.T) {
	r, _ := http.NewRequest("GET", "/", nil)
	if _, ok := Bearer(r); ok {
		t.Error("无头应失败")
	}
	r.Header.Set("Authorization", "Bearer abc")
	if s, ok := Bearer(r); !ok || s != "abc" {
		t.Error("应取出令牌")
	}
}

func TestCallers(t *testing.T) {
	c, err := ParseCallers("bill=s1, ride = s2")
	if err != nil {
		t.Fatal(err)
	}
	req := func(caller, key string) *http.Request {
		r, _ := http.NewRequest("GET", "/internal/rule/effective-version", nil)
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		if caller != "" {
			r.Header.Set("X-Caller", caller)
		}
		return r
	}
	if name, err := c.Check(req("bill", "s1")); err != nil || name != "bill" {
		t.Errorf("合法调用方应通过：%v", err)
	}
	if _, err := c.Check(req("ride", "s2")); err != nil {
		t.Errorf("去空白后应通过：%v", err)
	}
	for _, r := range []*http.Request{req("bill", "s2"), req("pay", "s1"), req("", "s1"), req("bill", "")} {
		if _, err := c.Check(r); !errors.Is(err, ErrCaller) {
			t.Error("不合法调用方应拒绝")
		}
	}
	if _, err := ParseCallers("bill"); err == nil {
		t.Error("格式不对应报错")
	}
}
