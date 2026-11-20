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

// trace: wire.md 第 2 节 用户令牌
func TestVerifyUser(t *testing.T) {
	pub, priv := newKey(t)
	ks, err := ParseKeys("k1=" + base64.StdEncoding.EncodeToString(pub))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	good := sign(t, priv, "k1", jwt.MapClaims{"iss": "bike-acct", "sub": "U100004", "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
	uid, err := ks.VerifyUser(good)
	if err != nil || uid != "U100004" {
		t.Fatalf("uid=%q err=%v", uid, err)
	}
	expired := sign(t, priv, "k1", jwt.MapClaims{"iss": "bike-acct", "sub": "U1", "iat": now.Add(-2 * time.Hour).Unix(), "exp": now.Add(-time.Hour).Unix()})
	if _, err := ks.VerifyUser(expired); err == nil {
		t.Fatal("过期令牌应被拒绝")
	}
	wrongIss := sign(t, priv, "k1", jwt.MapClaims{"iss": "bike-iam", "sub": "U1", "exp": now.Add(time.Hour).Unix()})
	if _, err := ks.VerifyUser(wrongIss); err == nil {
		t.Fatal("签发方不对应被拒绝")
	}
	_, otherPriv := newKey(t)
	forged := sign(t, otherPriv, "k1", jwt.MapClaims{"iss": "bike-acct", "sub": "U1", "exp": now.Add(time.Hour).Unix()})
	if _, err := ks.VerifyUser(forged); err == nil {
		t.Fatal("伪造签名应被拒绝")
	}
	unknownKid := sign(t, priv, "k9", jwt.MapClaims{"iss": "bike-acct", "sub": "U1", "exp": now.Add(time.Hour).Unix()})
	if _, err := ks.VerifyUser(unknownKid); err == nil {
		t.Fatal("未知 kid 应被拒绝")
	}
}

// trace: wire.md 第 2 节 管理令牌
func TestVerifyAdmin(t *testing.T) {
	pub, priv := newKey(t)
	ks, _ := ParseKeys("a1=" + base64.StdEncoding.EncodeToString(pub))
	now := time.Now()
	s := sign(t, priv, "a1", jwt.MapClaims{
		"iss": "bike-iam", "sub": "AC0101", "name": "客服甲", "role": "客服", "client": "管理端",
		"perms": []string{"refund.confirm", "order.view"}, "scope": nil, "readonly": false,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})
	a, err := ks.VerifyAdmin(s)
	if err != nil {
		t.Fatal(err)
	}
	if a.AccountID != "AC0101" || a.Name != "客服甲" || !a.Has("refund.confirm") || a.Has("refund.confirm.overLimit") || a.Scope != "" || a.ReadOnly {
		t.Fatalf("声明解析不对：%+v", a)
	}
	if !a.HasAny("x", "order.view") || a.HasAny("x", "y") {
		t.Fatal("HasAny 不对")
	}
	s2 := sign(t, priv, "a1", jwt.MapClaims{"iss": "bike-iam", "sub": "AC0201", "scope": "OP01", "readonly": true, "perms": []string{}, "exp": now.Add(time.Hour).Unix()})
	a2, err := ks.VerifyAdmin(s2)
	if err != nil || a2.Scope != "OP01" || !a2.ReadOnly {
		t.Fatalf("a2=%+v err=%v", a2, err)
	}
	userTok := sign(t, priv, "a1", jwt.MapClaims{"iss": "bike-acct", "sub": "U1", "exp": now.Add(time.Hour).Unix()})
	if _, err := ks.VerifyAdmin(userTok); err == nil {
		t.Fatal("用户令牌不能当管理令牌")
	}
}

func TestParseKeysRejectsBad(t *testing.T) {
	if _, err := ParseKeys("k1=not-base64!!"); err == nil {
		t.Fatal("坏公钥应报错")
	}
	ks, err := ParseKeys("")
	if err != nil || len(ks) != 0 {
		t.Fatal("空配置应得到空集合")
	}
}

func TestBearer(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if Bearer(r) != "" {
		t.Fatal("没有头应为空")
	}
	r.Header.Set("Authorization", "Bearer abc")
	if Bearer(r) != "abc" {
		t.Fatal("取不到令牌")
	}
}

// trace: wire.md 第 3 节 服务间调用
func TestCallers(t *testing.T) {
	c, err := ParseCallers("ride=s1, bill=s2")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/internal/cs/x", nil)
	r.Header.Set("Authorization", "Bearer s2")
	r.Header.Set("X-Caller", "bill")
	if who, ok := c.Check(r); !ok || who != "bill" {
		t.Fatalf("应通过：%q %v", who, ok)
	}
	r.Header.Set("X-Caller", "ride")
	if _, ok := c.Check(r); ok {
		t.Fatal("凭证与调用方对不上应拒绝")
	}
	r.Header.Set("X-Caller", "pay")
	if _, ok := c.Check(r); ok {
		t.Fatal("未登记的调用方应拒绝")
	}
}
