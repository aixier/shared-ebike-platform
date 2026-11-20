package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// idempotent 在一个事务里执行写操作：同一 scope+key 重复到达时返回第一次的结果，不重复执行。
// key 为空时直接执行。失败的结果不保存，再次到达会重新执行。
func (a *App) idempotent(ctx context.Context, scope, key string, fn func(tx pgx.Tx) (int, any, error)) (int, []byte, error) {
	if len(key) > 64 {
		return 0, nil, errInvalid("幂等键不能超过 64 个字符")
	}
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if key != "" {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", scope+"|"+key); err != nil {
			return 0, nil, err
		}
		var status int
		var body []byte
		err := tx.QueryRow(ctx, "SELECT status, body FROM idempotency WHERE scope=$1 AND key=$2", scope, key).Scan(&status, &body)
		if err == nil {
			return status, body, tx.Commit(ctx)
		}
		if err != pgx.ErrNoRows {
			return 0, nil, err
		}
	}

	status, v, err := fn(tx)
	if err != nil {
		return 0, nil, err
	}
	body, err := json.Marshal(v)
	if err != nil {
		return 0, nil, err
	}
	if key != "" {
		if _, err := tx.Exec(ctx, "INSERT INTO idempotency (scope, key, status, body) VALUES ($1,$2,$3,$4)", scope, key, status, body); err != nil {
			return 0, nil, err
		}
	}
	return status, body, tx.Commit(ctx)
}

// runIdem 执行并直接写响应。
func (a *App) runIdem(w http.ResponseWriter, r *http.Request, scope string, fn func(tx pgx.Tx) (int, any, error)) error {
	status, body, err := a.idempotent(r.Context(), scope, r.Header.Get("Idempotency-Key"), fn)
	if err != nil {
		return err
	}
	writeRaw(w, status, body)
	return nil
}

// nextSeq 按种类与日期发日序号。
func nextSeq(ctx context.Context, tx pgx.Tx, kind, day string) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `INSERT INTO id_seq (kind, day, n) VALUES ($1,$2,1)
		ON CONFLICT (kind, day) DO UPDATE SET n = id_seq.n + 1 RETURNING n`, kind, day).Scan(&n)
	return n, err
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---------- 发件表 ----------

type actor struct {
	Kind string  `json:"kind"`
	ID   string  `json:"id"`
	Name *string `json:"name"`
}

func personActor(id, name string) actor {
	var n *string
	if name != "" {
		n = &name
	}
	return actor{Kind: "人", ID: id, Name: n}
}

type envelope struct {
	EventID    string `json:"eventId"`
	Type       string `json:"type"`
	Version    int    `json:"version"`
	OccurredAt string `json:"occurredAt"`
	Key        string `json:"key"`
	Actor      actor  `json:"actor"`
	Payload    any    `json:"payload"`
}

// enqueue 在业务事务里写发件表，事务成功事件才存在。
func (a *App) enqueue(ctx context.Context, tx pgx.Tx, typ, key string, act actor, payload any) error {
	env := envelope{
		EventID:    "enforce-" + randHex(16),
		Type:       typ,
		Version:    1,
		OccurredAt: fmtTime(a.now()),
		Key:        key,
		Actor:      act,
		Payload:    payload,
	}
	body, err := json.Marshal(env)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO outbox (event_id, subject, body) VALUES ($1,$2,$3)", env.EventID, typ, body)
	if err != nil {
		return fmt.Errorf("写发件表失败：%w", err)
	}
	return nil
}

// audit 写"留痕已记录"事件（R-47）。
func (a *App) audit(ctx context.Context, tx pgx.Tx, act actor, action, targetType, targetID, summary string, reason *string) error {
	return a.enqueue(ctx, tx, "enforce.audit.recorded", targetType+":"+targetID, act, map[string]any{
		"action": action, "targetType": targetType, "targetId": targetID, "summary": summary,
		"reason": reason, "before": nil, "after": nil,
	})
}
