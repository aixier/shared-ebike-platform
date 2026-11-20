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

func genKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func sign(t *testing.T, priv ed25519.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	tk := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	tk.Header["kid"] = kid
	s, err := tk.SignedString(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestParseKeys(t *testing.T) {
	pub, _ := genKey(t)
	keys, err := ParseKeys("k1=" + base64.StdEncoding.EncodeToString(pub))
	if err != nil || len(keys) != 1 || !keys["k1"].Equal(pub) {
		t.Fatalf("解析公钥失败：%v %v", keys, err)
	}
	if _, err := ParseKeys("k1=notbase64!!"); err == nil {
		t.Fatal("非法公钥应报错")
	}
	if keys, err := ParseKeys(""); err != nil || len(keys) != 0 {
		t.Fatal("空串应得到空表")
	}
}

// trace: wire.md 第 2 节 用户令牌
func TestVerifyUser(t *testing.T) {
	pub, priv := genKey(t)
	v := NewVerifier(map[string]ed25519.PublicKey{"k1": pub})
	now := time.Now()
	good := sign(t, priv, "k1", jwt.MapClaims{"iss": "bike-acct", "sub": "U100004", "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
	uid, err := v.VerifyUser(good)
	if err != nil || uid != "U100004" {
		t.Fatalf("合法用户令牌应通过：%v %v", uid, err)
	}
	expired := sign(t, priv, "k1", jwt.MapClaims{"iss": "bike-acct", "sub": "U100004", "exp": now.Add(-time.Minute).Unix()})
	if _, err := v.VerifyUser(expired); err == nil {
		t.Fatal("过期令牌应拒绝")
	}
	wrongIss := sign(t, priv, "k1", jwt.MapClaims{"iss": "bike-iam", "sub": "U100004", "exp": now.Add(time.Hour).Unix()})
	if _, err := v.VerifyUser(wrongIss); err == nil {
		t.Fatal("签发方不对应拒绝")
	}
	unknownKid := sign(t, priv, "k9", jwt.MapClaims{"iss": "bike-acct", "sub": "U100004", "exp": now.Add(time.Hour).Unix()})
	if _, err := v.VerifyUser(unknownKid); err == nil {
		t.Fatal("未知 kid 应拒绝")
	}
	_, other := genKey(t)
	forged := sign(t, other, "k1", jwt.MapClaims{"iss": "bike-acct", "sub": "U100004", "exp": now.Add(time.Hour).Unix()})
	if _, err := v.VerifyUser(forged); err == nil {
		t.Fatal("伪造签名应拒绝")
	}
}

// trace: wire.md 第 2 节 管理令牌
func TestVerifyAdmin(t *testing.T) {
	pub, priv := genKey(t)
	v := NewVerifier(map[string]ed25519.PublicKey{"k1": pub})
	now := time.Now()
	tok := sign(t, priv, "k1", jwt.MapClaims{"iss": "bike-iam", "sub": "AC0201", "name": "吕＊＊", "role": "财务", "client": "管理端",
		"perms": []string{"deposit.view"}, "scope": nil, "readonly": false, "exp": now.Add(time.Hour).Unix()})
	a, err := v.VerifyAdmin(tok)
	if err != nil {
		t.Fatal(err)
	}
	if a.AccountID != "AC0201" || !a.Has("deposit.view") || a.Has("user.view") || a.Scope != nil {
		t.Fatalf("管理令牌解析不对：%+v", a)
	}
	if !a.HasAny("user.view", "deposit.view") {
		t.Fatal("列出多个权限点时有其一即可")
	}
	op := sign(t, priv, "k1", jwt.MapClaims{"iss": "bike-iam", "sub": "AC0101", "perms": []string{"deposit.view"}, "scope": "OP01", "exp": now.Add(time.Hour).Unix()})
	a2, err := v.VerifyAdmin(op)
	if err != nil || a2.Scope == nil || *a2.Scope != "OP01" {
		t.Fatalf("数据范围解析不对：%+v %v", a2, err)
	}
}

// trace: wire.md 第 3 节 服务间调用
func TestServiceAuth(t *testing.T) {
	sa, err := ParseCallers("bill=s1,cs=s2")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/internal/deposit/deductions", nil)
	r.Header.Set("Authorization", "Bearer s1")
	r.Header.Set("X-Caller", "bill")
	if c, ok := sa.Check(r); !ok || c != "bill" {
		t.Fatal("凭证正确应通过")
	}
	r.Header.Set("Authorization", "Bearer s2")
	if _, ok := sa.Check(r); ok {
		t.Fatal("凭证与调用方不符应拒绝")
	}
	r.Header.Set("X-Caller", "ride")
	if _, ok := sa.Check(r); ok {
		t.Fatal("不在清单的调用方应拒绝")
	}
}

func TestBearer(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if Bearer(r) != "" {
		t.Fatal("无请求头应为空")
	}
	r.Header.Set("Authorization", "Bearer abc")
	if Bearer(r) != "abc" {
		t.Fatal("应取出令牌")
	}
}
