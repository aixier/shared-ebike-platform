package app

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// 幂等记录：同一个键重复到达返回第一次的结果。

type idemHit struct {
	Status int
	Body   []byte
}

func (a *App) idemGet(ctx context.Context, scope, key string) (*idemHit, error) {
	var h idemHit
	err := a.DB.QueryRow(ctx, `SELECT status, body FROM idempotency WHERE scope=$1 AND key=$2`, scope, key).Scan(&h.Status, &h.Body)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &h, nil
}

// idemPut 保存结果；已存在时不覆盖。
func (a *App) idemPut(ctx context.Context, q querier, scope, key string, status int, body []byte) error {
	_, err := q.Exec(ctx, `INSERT INTO idempotency (scope, key, status, body, created_at) VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`,
		scope, key, status, body, a.Now())
	return err
}
