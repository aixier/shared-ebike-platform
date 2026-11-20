package app

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// AdminClaims iam 签发的管理令牌（contracts/wire.md 第 2 节）。
type AdminClaims struct {
	Name     string   `json:"name"`
	Role     string   `json:"role"`
	Client   string   `json:"client"`
	Perms    []string `json:"perms"`
	Scope    *string  `json:"scope"`
	Readonly bool     `json:"readonly"`
	jwt.RegisteredClaims
}

type ctxKey int

const adminKey ctxKey = 1

func adminFrom(ctx context.Context) *AdminClaims {
	c, _ := ctx.Value(adminKey).(*AdminClaims)
	return c
}

// verifyAdmin 验签、过期、签发方。
func (a *App) verifyAdmin(r *http.Request) (*AdminClaims, bool) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		return nil, false
	}
	claims := &AdminClaims{}
	_, err := jwt.ParseWithClaims(tok, claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		k, ok := a.Cfg.AdminPubKeys[kid]
		if !ok {
			return nil, jwt.ErrTokenUnverifiable
		}
		return k, nil
	}, jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithIssuer("bike-iam"), jwt.WithExpirationRequired(), jwt.WithTimeFunc(a.Now))
	if err != nil {
		return nil, false
	}
	return claims, true
}

// adminOnly 管理端接口：要求令牌与权限点之一；write 为真时拒绝只读账号。
func (a *App) adminOnly(write bool, perms []string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := a.verifyAdmin(r)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "ADMIN_NOT_LOGGED_IN", "登录已失效，请重新登录")
			return
		}
		if !hasAny(c.Perms, perms) {
			writeErr(w, http.StatusForbidden, "FORBIDDEN", "当前账号没有管理生产凭证的权限")
			return
		}
		if write && c.Readonly {
			writeErr(w, http.StatusForbidden, "READONLY", "当前账号为只读，不能修改")
			return
		}
		// 运营商已退出：不依赖令牌里的 readonly 何时刷新，按事件投影拒绝该运营商范围的写操作
		if write && c.Scope != nil {
			exited, err := a.operatorExited(r.Context(), *c.Scope)
			if err != nil {
				serverBusy(w)
				return
			}
			if exited {
				writeErr(w, http.StatusForbidden, "READONLY", "该运营商已退出，只能查看，不能修改")
				return
			}
		}
		h(w, r.WithContext(context.WithValue(r.Context(), adminKey, c)))
	}
}

func hasAny(have, want []string) bool {
	for _, w := range want {
		for _, h := range have {
			if h == w {
				return true
			}
		}
	}
	return false
}

// internalOnly 服务间调用：X-Caller 在允许清单内且凭证一致。
func (a *App) internalOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller := r.Header.Get("X-Caller")
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		want, ok := a.Cfg.AllowedCallers[caller]
		if !ok || tok == "" || subtle.ConstantTimeCompare([]byte(tok), []byte(want)) != 1 {
			writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "调用方凭证无效")
			return
		}
		h(w, r)
	}
}

// operatorExited 运营商是否已按 fleet.operator.exited 退出。
func (a *App) operatorExited(ctx context.Context, operatorID string) (bool, error) {
	var n int
	if err := a.DB.QueryRow(ctx, `SELECT count(*) FROM exited_operators WHERE operator_id=$1`, operatorID).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}
