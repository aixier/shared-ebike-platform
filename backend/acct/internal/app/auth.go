package app

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"bike/acct/internal/token"
)

type ctxKey int

const (
	keyUser ctxKey = iota
	keyAdmin
	keyCaller
)

type userPrincipal struct {
	UserID  string
	JTI     string
	Expires time.Time
}

func userIDFrom(r *http.Request) string {
	if p, ok := r.Context().Value(keyUser).(userPrincipal); ok {
		return p.UserID
	}
	return ""
}

func adminFrom(r *http.Request) token.AdminClaims {
	c, _ := r.Context().Value(keyAdmin).(token.AdminClaims)
	return c
}

func callerFrom(r *http.Request) string {
	c, _ := r.Context().Value(keyCaller).(string)
	return c
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// userAuth 校验用户令牌：验签与过期、已作废、账户已注销。
// cancelledCode 为真时已注销返回 ACCOUNT_CANCELLED，否则按未登录处理。
func (a *App) userAuth(cancelledCode bool, h hfn) http.HandlerFunc {
	return a.serve(func(r *http.Request) (any, error) {
		p, err := a.checkUserToken(r.Context(), bearerToken(r), cancelledCode)
		if err != nil {
			return nil, err
		}
		return h(r.WithContext(context.WithValue(r.Context(), keyUser, p)))
	})
}

func (a *App) checkUserToken(ctx context.Context, raw string, cancelledCode bool) (userPrincipal, error) {
	if raw == "" {
		return userPrincipal{}, errf("NOT_LOGGED_IN", "")
	}
	c, err := token.VerifyUser(a.Cfg.UserPubKeys, raw, a.Now())
	if err != nil {
		return userPrincipal{}, errf("NOT_LOGGED_IN", "登录已失效，请重新授权登录")
	}
	var status string
	var validAfter *time.Time
	var revoked bool
	err = a.Pool.QueryRow(ctx, `SELECT status, tokens_valid_after, EXISTS (SELECT 1 FROM revoked_tokens WHERE jti=$2)
		FROM users WHERE user_id=$1`, c.UserID, c.ID).Scan(&status, &validAfter, &revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return userPrincipal{}, errf("NOT_LOGGED_IN", "")
	}
	if err != nil {
		return userPrincipal{}, err
	}
	if status == "已注销" {
		if cancelledCode {
			return userPrincipal{}, errf("ACCOUNT_CANCELLED", "")
		}
		return userPrincipal{}, errf("NOT_LOGGED_IN", "该账户已注销，请重新授权登录")
	}
	if revoked || (validAfter != nil && !c.IssuedAt.After(validAfter.Truncate(time.Second))) {
		return userPrincipal{}, errf("NOT_LOGGED_IN", "登录已失效，请重新授权登录")
	}
	return userPrincipal{UserID: c.UserID, JTI: c.ID, Expires: c.Expires}, nil
}

// handleLogout 当前令牌作废；令牌已失效时也返回成功
func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := token.VerifyUser(a.Cfg.UserPubKeys, bearerToken(r), a.Now()); err == nil && c.ID != "" {
		_, err := a.Pool.Exec(r.Context(), `INSERT INTO revoked_tokens (jti, user_id, expires_at) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, c.ID, c.UserID, c.Expires)
		if err != nil {
			st, b := a.render(nil, err)
			writeRaw(w, st, b)
			return
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// adminAuth 校验管理令牌与权限点（列出多个时有其一即可）；运营商账号（scope 不为空）不能看骑行用户
func (a *App) adminAuth(perms []string, h hfn) http.HandlerFunc {
	return a.serve(func(r *http.Request) (any, error) {
		c, err := token.VerifyAdmin(a.Cfg.AdminPubKeys, bearerToken(r), a.Now())
		if err != nil {
			return nil, errf("ADMIN_NOT_LOGGED_IN", "")
		}
		if !c.HasAny(perms...) {
			return nil, errf("FORBIDDEN", "")
		}
		if c.Scope != nil {
			return nil, errf("FORBIDDEN", "运营商账号不能查看骑行用户信息")
		}
		return h(r.WithContext(context.WithValue(r.Context(), keyAdmin, c)))
	})
}

// internalAuth 校验调用方服务凭证；command 为真时必须带幂等键
func (a *App) internalAuth(command bool, h hfn) http.HandlerFunc {
	inner := h
	if command {
		inner = a.withIdem("internal", func(r *http.Request) string { return callerFrom(r) + ":" + r.URL.Path }, true, h)
	}
	return a.serve(func(r *http.Request) (any, error) {
		caller := r.Header.Get("X-Caller")
		want, ok := a.Cfg.AllowedCallers[caller]
		got := bearerToken(r)
		if !ok || got == "" || subtle.ConstantTimeCompare([]byte(want), []byte(got)) != 1 {
			return nil, &apiError{status: 401, Code: "UNAUTHORIZED", Message: "服务凭证无效"}
		}
		return inner(r.WithContext(context.WithValue(r.Context(), keyCaller, caller)))
	})
}
