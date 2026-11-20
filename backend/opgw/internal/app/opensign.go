package app

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"bike/opgw/internal/sign"
)

type openReq struct {
	Cred *Credential
	Body []byte
}

const openKey ctxKey = 2

func openFrom(ctx context.Context) *openReq {
	o, _ := ctx.Value(openKey).(*openReq)
	return o
}

// clientIP 来源地址；只有直连方是入口网关时才取 X-Forwarded-For 的最后一跳。
func (a *App) clientIP(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remote, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	remote = remote.Unmap()
	trusted := false
	for _, p := range a.Cfg.TrustedProxies {
		if p.Contains(remote) {
			trusted = true
			break
		}
	}
	if trusted {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip, err := netip.ParseAddr(strings.TrimSpace(parts[len(parts)-1])); err == nil {
				return ip.Unmap(), true
			}
		}
	}
	return remote, true
}

func ipAllowed(ip netip.Addr, list []string) bool {
	if len(list) == 0 {
		return true // 未登记白名单时不限来源（见 README 配置说明）
	}
	for _, s := range list {
		p, err := parsePrefix(s)
		if err == nil && p.Contains(ip) {
			return true
		}
	}
	return false
}

// operatorSigned 运营商入口：登记、停用、白名单、时间戳、签名、随机数、限流，任一不满足即拒绝。
func (a *App) operatorSigned(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		appID := r.Header.Get("X-App-Id")
		ts := r.Header.Get("X-Timestamp")
		nonce := r.Header.Get("X-Nonce")
		sig := r.Header.Get("X-Signature")
		body, err := readBody(r)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "INVALID", "请求体读取失败")
			return
		}
		if appID == "" || ts == "" || nonce == "" || sig == "" {
			writeErr(w, http.StatusUnauthorized, "SIGNATURE_INVALID", "缺少签名请求头 X-App-Id、X-Timestamp、X-Nonce、X-Signature")
			return
		}
		cred, err := a.credByAppID(ctx, appID)
		if err != nil {
			serverBusy(w)
			return
		}
		if cred == nil {
			writeErr(w, http.StatusUnauthorized, "SIGNATURE_INVALID", "应用标识未登记")
			return
		}
		if cred.Status != "有效" {
			writeErr(w, http.StatusForbidden, "CREDENTIAL_DISABLED", "凭证已停用，请联系平台公司")
			return
		}
		if ip, ok := a.clientIP(r); !ok || !ipAllowed(ip, cred.IPWhitelist) {
			writeErr(w, http.StatusForbidden, "IP_NOT_ALLOWED", "来源不在白名单，请联系平台公司登记来源")
			return
		}
		now := a.Now()
		if !sign.TimestampOK(ts, now.Unix(), a.Cfg.SignWindowSec) {
			writeErr(w, http.StatusUnauthorized, "TIMESTAMP_EXPIRED", "时间戳与平台时间相差超过 300 秒，请校时后重发")
			return
		}
		if !sign.NonceOK(nonce) {
			writeErr(w, http.StatusBadRequest, "INVALID", "X-Nonce 须为 16～64 个字符")
			return
		}
		secret, err := a.secretOf(cred)
		if err != nil {
			serverBusy(w)
			return
		}
		if !sign.Verify(secret, r.Method, r.URL.Path, appID, ts, nonce, body, sig) {
			writeErr(w, http.StatusUnauthorized, "SIGNATURE_INVALID", "签名不一致，请核对密钥与待签名串")
			return
		}
		// 签名通过后才记随机数，避免他人用伪造请求消耗随机数
		tag, err := a.DB.Exec(ctx, `INSERT INTO nonces (app_id, nonce, seen_at) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, appID, nonce, now)
		if err != nil {
			serverBusy(w)
			return
		}
		if tag.RowsAffected() == 0 {
			writeErr(w, http.StatusUnauthorized, "NONCE_REPLAYED", "随机数重复，请换随机数重发")
			return
		}
		var n int
		minute := now.Truncate(time.Minute)
		if err := a.DB.QueryRow(ctx, `INSERT INTO rate_counters (app_id, minute, n) VALUES ($1,$2,1)
			ON CONFLICT (app_id, minute) DO UPDATE SET n = rate_counters.n + 1 RETURNING n`, appID, minute).Scan(&n); err != nil {
			serverBusy(w)
			return
		}
		if n > cred.RateLimitPerMinute {
			w.Header().Set("Retry-After", "60")
			writeErr(w, http.StatusTooManyRequests, "RATE_LIMITED", "超出限流，请退避后重试")
			return
		}
		_, _ = a.DB.Exec(ctx, `UPDATE credentials SET last_seen_at=$2 WHERE credential_id=$1`, cred.CredentialID, now)
		h(w, r.WithContext(context.WithValue(ctx, openKey, &openReq{Cred: cred, Body: body})))
	}
}

// CleanupWindows 清理过期的随机数、限流计数与幂等记录。
func (a *App) CleanupWindows(ctx context.Context) error {
	now := a.Now()
	if _, err := a.DB.Exec(ctx, `DELETE FROM nonces WHERE seen_at < $1`, now.Add(-2*time.Duration(a.Cfg.SignWindowSec)*time.Second)); err != nil {
		return err
	}
	if _, err := a.DB.Exec(ctx, `DELETE FROM rate_counters WHERE minute < $1`, now.Add(-time.Hour)); err != nil {
		return err
	}
	_, err := a.DB.Exec(ctx, `DELETE FROM idempotency WHERE created_at < $1`, now.Add(-7*24*time.Hour))
	return err
}
