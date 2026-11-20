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
		"iss": "bike-iam", "sub": "AC0101", "name": "何＊＊", "role": "运营商业务", "client": "管理端",
		"perms": []string{"vehicle.view.own", "vehicle.edit.own"}, "scope": "OP01", "readonly": false,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	}
}

func TestParseKeys(t *testing.T) {
	pub, _ := newKey(t)
	keys, err := ParseKeys("k1=" + base64.StdEncoding.EncodeToString(pub))
	if err != nil || len(keys) != 1 || !keys["k1"].Equal(pub) {
		t.Fatalf("解析公钥失败: %v %v", keys, err)
	}
	if _, err := ParseKeys("k1=bad"); err == nil {
		t.Fatal("坏公钥应报错")
	}
	if keys, err := ParseKeys(""); err != nil || len(keys) != 0 {
		t.Fatal("空串应得到空表")
	}
}

func TestVerifyAdmin(t *testing.T) {
	pub, priv := newKey(t)
	v := NewAdminVerifier(map[string]ed25519.PublicKey{"k1": pub})

	c, err := v.Verify(sign(t, priv, "k1", baseClaims()))
	if err != nil {
		t.Fatalf("合法令牌应通过: %v", err)
	}
	if c.AccountID != "AC0101" || c.Scope != "OP01" || c.Readonly || !c.Has("vehicle.edit.own") || c.Has("access.view") {
		t.Fatalf("声明解析不对: %+v", c)
	}

	// 平台账号 scope 为 null
	pc := baseClaims()
	pc["scope"] = nil
	c, err = v.Verify(sign(t, priv, "k1", pc))
	if err != nil || c.Scope != "" {
		t.Fatalf("scope=null 应解析为空: %+v %v", c, err)
	}

	bad := []struct {
		name string
		tok  string
	}{
		{"过期", func() string {
			x := baseClaims()
			x["exp"] = time.Now().Add(-time.Minute).Unix()
			return sign(t, priv, "k1", x)
		}()},
		{"签发方不对", func() string { x := baseClaims(); x["iss"] = "bike-acct"; return sign(t, priv, "k1", x) }()},
		{"未知 kid", sign(t, priv, "k9", baseClaims())},
		{"别的私钥", func() string { _, p2 := newKey(t); return sign(t, p2, "k1", baseClaims()) }()},
		{"乱码", "abc.def.ghi"},
	}
	for _, b := range bad {
		if _, err := v.Verify(b.tok); err == nil {
			t.Errorf("%s 的令牌不应通过", b.name)
		}
	}
}

func TestFromRequest(t *testing.T) {
	pub, priv := newKey(t)
	v := NewAdminVerifier(map[string]ed25519.PublicKey{"k1": pub})
	r := httptest.NewRequest("GET", "/x", nil)
	if _, err := v.FromRequest(r); err == nil {
		t.Fatal("无令牌应失败")
	}
	r.Header.Set("Authorization", "Bearer "+sign(t, priv, "k1", baseClaims()))
	if _, err := v.FromRequest(r); err != nil {
		t.Fatalf("应通过: %v", err)
	}
}

func TestCallers(t *testing.T) {
	cs, err := ParseCallers("opgw=s1, ops=s2,onboard=s3")
	if err != nil {
		t.Fatal(err)
	}
	check := func(caller, auth string) bool {
		r := httptest.NewRequest("POST", "/internal/x", nil)
		if caller != "" {
			r.Header.Set("X-Caller", caller)
		}
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		_, ok := cs.Check(r)
		return ok
	}
	if !check("ops", "Bearer s2") {
		t.Error("正确凭证应通过")
	}
	if check("ops", "Bearer s1") || check("ride", "Bearer s1") || check("", "Bearer s2") || check("ops", "") {
		t.Error("错误凭证或未登记调用方不应通过")
	}
	if _, err := ParseCallers("opgw"); err == nil {
		t.Error("格式错应报错")
	}
}
