package iam

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// idempotent 在一个事务里执行写操作，并按 Idempotency-Key 记下结果：
// 同一账号、同一操作、同一键重复到达时返回第一次的结果，不再执行。
// 先占位再执行，并发重复请求会等第一笔提交后读到它的结果；第一笔失败回滚则不留记录，可重试。
func (s *Service) idempotent(w http.ResponseWriter, r *http.Request, accountID, op string, fn func(ctx context.Context, tx pgx.Tx) (int, any, error)) {
	ctx := r.Context()
	key := r.Header.Get("Idempotency-Key")
	if len(key) > 64 {
		s.fail(w, r, apiError("INVALID", "幂等键不能超过 64 个字符"))
		return
	}
	scope := accountID + ":" + op
	tx, err := s.db.Begin(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(ctx)
	if key != "" {
		tag, err := tx.Exec(ctx, `INSERT INTO iam_idempotency(scope, key, created_at) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, scope, key, s.now())
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if tag.RowsAffected() == 0 {
			_ = tx.Rollback(ctx)
			var status *int
			var body []byte
			if err := s.db.QueryRow(ctx, `SELECT status_code, body FROM iam_idempotency WHERE scope=$1 AND key=$2`, scope, key).Scan(&status, &body); err != nil {
				s.fail(w, r, err)
				return
			}
			if status == nil {
				s.fail(w, r, apiError("IDEMPOTENCY_IN_PROGRESS", "同一请求正在处理中，请稍后查看结果"))
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(*status)
			_, _ = w.Write(body)
			return
		}
	}
	status, res, err := fn(ctx, tx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	body, err := json.Marshal(res)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if key != "" {
		if _, err := tx.Exec(ctx, `UPDATE iam_idempotency SET status_code=$3, body=$4 WHERE scope=$1 AND key=$2`, scope, key, status, body); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

// Cleanup 清理过期太久的会话与幂等记录（留痕不清理）。
func (s *Service) Cleanup(ctx context.Context) error {
	cut := s.now().Add(-s.retention)
	if _, err := s.db.Exec(ctx, `DELETE FROM iam_sessions WHERE expires_at < $1`, cut); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `DELETE FROM iam_idempotency WHERE created_at < $1`, cut)
	return err
}
