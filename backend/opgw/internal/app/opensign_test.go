package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"bike/opgw/internal/sign"
)

// 任务 5：运营商入口的验签、防重放、白名单、限流

func (e *env) rawSigned(appID, secret, ts, nonce string, body []byte, tamper func(*http.Request)) (int, map[string]any) {
	req, _ := http.NewRequest("POST", e.srv.URL+"/open/v1/ride-events", bytes.NewReader(body))
	sign.Apply(req, secret, appID, ts, nonce, body)
	if tamper != nil {
		tamper(req)
	}
	return do(e.t, req)
}

func TestOpenSignatureChecks(t *testing.T) {
	e := newEnv(t, nil)
	e.seedOperators()
	e.unlockOK("OD2026120100001", "OP01", "BR01", "QH000004")
	c, secret := e.cred("OP01")
	body, _ := json.Marshal(unlockEvent("EV-S", "OD2026120100001", "QH000004"))
	now := strconv.FormatInt(e.clock.Unix(), 10)

	cases := []struct {
		name   string
		app    string
		secret string
		ts     string
		nonce  string
		tamper func(*http.Request)
		st     int
		code   string
	}{
		{"未登记", "app_unknown", secret, now, "n000000000000001", nil, 401, "SIGNATURE_INVALID"},
		{"密钥不对", c.AppID, "wrong", now, "n000000000000002", nil, 401, "SIGNATURE_INVALID"},
		{"时间戳过期", c.AppID, secret, strconv.FormatInt(e.clock.Unix()-301, 10), "n000000000000003", nil, 401, "TIMESTAMP_EXPIRED"},
		{"时间戳超前", c.AppID, secret, strconv.FormatInt(e.clock.Unix()+301, 10), "n000000000000004", nil, 401, "TIMESTAMP_EXPIRED"},
		{"随机数太短", c.AppID, secret, now, "short", nil, 400, "INVALID"},
		{"缺签名头", c.AppID, secret, now, "n000000000000005", func(r *http.Request) { r.Header.Del("X-Signature") }, 401, "SIGNATURE_INVALID"},
		{"路径不符", c.AppID, secret, now, "n000000000000006", func(r *http.Request) {
			r.Header.Set("X-Signature", sign.Sign(secret, "POST", "/open/v1/vehicle-lists", c.AppID, now, "n000000000000006", body))
		}, 401, "SIGNATURE_INVALID"},
	}
	for _, k := range cases {
		st, m := e.rawSigned(k.app, k.secret, k.ts, k.nonce, body, k.tamper)
		if st != k.st || m["code"] != k.code {
			t.Errorf("%s：%d %v", k.name, st, m)
		}
	}
	// 正确的请求通过；同一随机数再来被拒
	if st, m := e.rawSigned(c.AppID, secret, now, "n00000000000000A", body, nil); st != 200 {
		t.Fatalf("%d %v", st, m)
	}
	if st, m := e.rawSigned(c.AppID, secret, now, "n00000000000000A", body, nil); st != 401 || m["code"] != "NONCE_REPLAYED" {
		t.Fatalf("重放：%d %v", st, m)
	}
	// 签名不对的请求不消耗随机数
	e.rawSigned(c.AppID, "wrong", now, "n00000000000000B", body, nil)
	if st, _ := e.rawSigned(c.AppID, secret, now, "n00000000000000B", body, nil); st != 200 {
		t.Fatal("伪造请求不应占用随机数")
	}
	got, _ := e.cred("OP01")
	if got.LastSeenAt == nil {
		t.Fatal("验签通过应记最近请求时刻")
	}
}

func TestOpenDisabledCredential(t *testing.T) {
	e := newEnv(t, nil)
	e.seedOperators()
	c, secret := e.cred("OP01")
	e.admin("POST", "/api/opgw/credentials/CRD0001/disable", adminToken(e, tokenOpt{}), "d", map[string]string{"reason": "凭证泄露停用"})
	body := []byte(`{}`)
	if st, m := e.rawSigned(c.AppID, secret, strconv.FormatInt(e.clock.Unix(), 10), "n000000000000011", body, nil); st != 403 || m["code"] != "CREDENTIAL_DISABLED" {
		t.Fatalf("%d %v", st, m)
	}
	// 他家不受影响
	if st, _ := e.signed("OP04", "/open/v1/ride-events", unlockEvent("X", "OD-NONE", "ZF000001"), nil); st != 404 {
		t.Fatal(st)
	}
}

func TestOpenRotatedOldKeyRejected(t *testing.T) {
	e := newEnv(t, nil)
	e.seedOperators()
	c, oldSecret := e.cred("OP01")
	e.admin("POST", "/api/opgw/credentials/CRD0001/rotate", adminToken(e, tokenOpt{}), "r", map[string]string{"reason": "定期轮换密钥"})
	if st, m := e.rawSigned(c.AppID, oldSecret, strconv.FormatInt(e.clock.Unix(), 10), "n000000000000021", []byte(`{}`), nil); st != 401 || m["code"] != "SIGNATURE_INVALID" {
		t.Fatalf("旧密钥立即失效：%d %v", st, m)
	}
}

func TestOpenIPWhitelist(t *testing.T) {
	e := newEnv(t, func(c *Config) {
		c.IPWhitelists = map[string][]string{"OP01": {"10.1.2.0/24"}, "OP04": {"127.0.0.1"}}
		c.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	})
	e.seedOperators()
	body := unlockEvent("X", "OD-NONE", "QH000004")
	if st, m := e.signed("OP01", "/open/v1/ride-events", body, nil); st != 403 || m["code"] != "IP_NOT_ALLOWED" {
		t.Fatalf("%d %v", st, m)
	}
	// 经入口网关转发、来源在白名单
	if st, _ := e.signed("OP01", "/open/v1/ride-events", body, map[string]string{"X-Forwarded-For": "1.1.1.1, 10.1.2.9"}); st != 404 {
		t.Fatal("白名单内应放行到业务校验", st)
	}
	if st, _ := e.signed("OP04", "/open/v1/ride-events", body, nil); st != 404 {
		t.Fatal(st)
	}
}

func TestOpenRateLimit(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.RateLimitPerMinute = 3 })
	e.seedOperators()
	body := unlockEvent("X", "OD-NONE", "QH000004")
	for i := 0; i < 3; i++ {
		if st, _ := e.signed("OP01", "/open/v1/ride-events", body, nil); st != 404 {
			t.Fatal(st)
		}
	}
	if st, m := e.signed("OP01", "/open/v1/ride-events", body, nil); st != 429 || m["code"] != "RATE_LIMITED" {
		t.Fatalf("%d %v", st, m)
	}
	if st, _ := e.signed("OP04", "/open/v1/ride-events", body, nil); st != 404 {
		t.Fatal("限流按凭证分开")
	}
	e.advance(time.Minute)
	if st, _ := e.signed("OP01", "/open/v1/ride-events", body, nil); st != 404 {
		t.Fatal("下一分钟恢复")
	}
}

func TestCleanupWindows(t *testing.T) {
	e := newEnv(t, nil)
	e.seedOperators()
	e.signed("OP01", "/open/v1/ride-events", unlockEvent("X", "OD-NONE", "QH000004"), nil)
	e.advance(2 * time.Hour)
	if err := e.a.CleanupWindows(context.Background()); err != nil {
		t.Fatal(err)
	}
	var n, r int
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM nonces`).Scan(&n)
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM rate_counters`).Scan(&r)
	if n != 0 || r != 0 {
		t.Fatal(n, r)
	}
}
