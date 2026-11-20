package core

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"bike/cs/internal/httpx"
)

// runIdem 执行一个写操作：带 Idempotency-Key 时同一主体、同一接口、同一键只执行一次，重复到达返回第一次的结果。
// 5xx 结果不保存，允许重试。trace: contracts/wire.md 第 1 节 幂等
func (a *App) runIdem(w http.ResponseWriter, r *http.Request, principal string, fn func() (int, any, error)) {
	key := r.Header.Get("Idempotency-Key")
	if len(key) > 64 {
		httpx.WriteError(w, httpx.E("INVALID", "幂等键过长"))
		return
	}
	run := func() (int, []byte) {
		status, body, err := fn()
		if err != nil {
			s, eb := httpx.ErrorBody(err)
			b, _ := json.Marshal(eb)
			return s, b
		}
		b, _ := json.Marshal(body)
		return status, b
	}
	write := func(status int, b []byte) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write(append(b, '\n'))
	}
	if key == "" {
		write(run())
		return
	}
	ctx := r.Context()
	scope := principal + " " + r.Method + " " + r.URL.Path
	tag, err := a.DB.Exec(ctx, `INSERT INTO idempotency (scope, key) VALUES ($1, $2) ON CONFLICT DO NOTHING`, scope, key)
	if err != nil {
		httpx.WriteError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		var status *int
		var body []byte
		err := a.DB.QueryRow(ctx, `SELECT status, body FROM idempotency WHERE scope = $1 AND key = $2`, scope, key).Scan(&status, &body)
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.WriteError(w, httpx.E("CONFLICT", "同一请求正在处理，请稍后再试"))
			return
		}
		if err != nil {
			httpx.WriteError(w, err)
			return
		}
		if status == nil {
			httpx.WriteError(w, httpx.E("CONFLICT", "同一请求正在处理，请稍后再试"))
			return
		}
		write(*status, body)
		return
	}
	status, body := run()
	if status >= 500 {
		_, _ = a.DB.Exec(ctx, `DELETE FROM idempotency WHERE scope = $1 AND key = $2`, scope, key)
	} else {
		_, _ = a.DB.Exec(ctx, `UPDATE idempotency SET status = $3, body = $4 WHERE scope = $1 AND key = $2`, scope, key, status, body)
	}
	write(status, body)
}
