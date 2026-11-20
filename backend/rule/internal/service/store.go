package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"bike/rule/internal/domain"
	"bike/rule/internal/seed"
	"bike/rule/migrations"
)

// 事务级咨询锁编号：版本写入串行、参数写入串行、发件表发布只跑一份。
const (
	lockVersions = 73070001
	lockParams   = 73070002
	lockRelay    = 73070003
)

// Migrate 执行内嵌的迁移（goose 自带迁移台账）。
func Migrate(ctx context.Context, dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.UpContext(ctx, db, ".")
}

const versionCols = `version_id, seq, name, effective_at, created_at, created_by, note, content, revoked_at, revoked_by`

func scanVersion(row pgx.Row) (domain.Version, error) {
	var v domain.Version
	var createdBy, content []byte
	var revokedBy []byte
	if err := row.Scan(&v.VersionID, &v.Seq, &v.Name, &v.EffectiveAt, &v.CreatedAt, &createdBy, &v.Note, &content, &v.RevokedAt, &revokedBy); err != nil {
		return v, err
	}
	if err := json.Unmarshal(createdBy, &v.CreatedBy); err != nil {
		return v, err
	}
	if err := json.Unmarshal(content, &v.Content); err != nil {
		return v, err
	}
	if revokedBy != nil {
		var a domain.Actor
		if err := json.Unmarshal(revokedBy, &a); err != nil {
			return v, err
		}
		v.RevokedBy = &a
	}
	if v.Content.Coupons == nil {
		v.Content.Coupons = []domain.CouponTemplate{}
	}
	if v.Content.ServiceFee.Ratios == nil {
		v.Content.ServiceFee.Ratios = map[string]float64{}
	}
	return v, nil
}

// loadVersions 读出全部版本（版本数量很小，整条链在内存里算）。
func loadVersions(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}) ([]domain.Version, error) {
	rows, err := q.Query(ctx, `SELECT `+versionCols+` FROM pricing_versions ORDER BY effective_at, seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Version
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func insertVersion(ctx context.Context, tx pgx.Tx, v domain.Version) error {
	createdBy, _ := json.Marshal(v.CreatedBy)
	content, err := json.Marshal(v.Content)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO pricing_versions (version_id, seq, name, effective_at, created_at, created_by, note, content)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, v.VersionID, v.Seq, v.Name, v.EffectiveAt, v.CreatedAt, createdBy, v.Note, content)
	return err
}

// paramsRow 平台参数现值。
type paramsRow struct {
	Items     domain.ParamItems
	UpdatedAt *time.Time
	UpdatedBy *domain.Actor
}

func loadParams(ctx context.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}) (paramsRow, error) {
	var items, by []byte
	var at time.Time
	err := q.QueryRow(ctx, `SELECT items, updated_at, updated_by FROM platform_params WHERE id = 1`).Scan(&items, &at, &by)
	if errors.Is(err, pgx.ErrNoRows) {
		return paramsRow{Items: domain.ParamItems{}.WithDefaults()}, nil
	}
	if err != nil {
		return paramsRow{}, err
	}
	var p paramsRow
	if err := json.Unmarshal(items, &p.Items); err != nil {
		return p, err
	}
	p.Items = p.Items.WithDefaults()
	var a domain.Actor
	if err := json.Unmarshal(by, &a); err != nil {
		return p, err
	}
	p.UpdatedAt, p.UpdatedBy = &at, &a
	return p, nil
}

func saveParams(ctx context.Context, tx pgx.Tx, items domain.ParamItems, at time.Time, by domain.Actor) error {
	ib, _ := json.Marshal(items)
	bb, _ := json.Marshal(by)
	_, err := tx.Exec(ctx, `INSERT INTO platform_params (id, items, updated_at, updated_by) VALUES (1, $1, $2, $3)
		ON CONFLICT (id) DO UPDATE SET items = EXCLUDED.items, updated_at = EXCLUDED.updated_at, updated_by = EXCLUDED.updated_by`, ib, at, bb)
	return err
}

func lockXact(ctx context.Context, tx pgx.Tx, key int64) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key)
	return err
}

// idemGet 查幂等键的第一次结果。
func idemGet(ctx context.Context, tx pgx.Tx, scope, key string) (int, []byte, bool, error) {
	if key == "" {
		return 0, nil, false, nil
	}
	var status int
	var body []byte
	err := tx.QueryRow(ctx, `SELECT status, body FROM idempotency_keys WHERE scope = $1 AND key = $2`, scope, key).Scan(&status, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, false, nil
	}
	return status, body, err == nil, err
}

func idemPut(ctx context.Context, tx pgx.Tx, scope, key string, status int, body []byte) error {
	if key == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO idempotency_keys (scope, key, status, body) VALUES ($1,$2,$3,$4)`, scope, key, status, body)
	return err
}

// envelope 事件信封（contracts/events/README.md 第 1 节）。
type envelope struct {
	EventID    string       `json:"eventId"`
	Type       string       `json:"type"`
	Version    int          `json:"version"`
	OccurredAt string       `json:"occurredAt"`
	Key        string       `json:"key"`
	Actor      domain.Actor `json:"actor"`
	Payload    any          `json:"payload"`
}

func newEventID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "rule-" + hex.EncodeToString(b)
}

// enqueue 在业务事务里写发件表；主题等于事件类型。
func enqueue(ctx context.Context, tx pgx.Tx, typ, key string, actor domain.Actor, at time.Time, payload any) error {
	env := envelope{EventID: newEventID(), Type: typ, Version: 1, OccurredAt: domain.FormatTime(at), Key: key, Actor: actor, Payload: payload}
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox (event_id, subject, body) VALUES ($1,$2,$3)`, env.EventID, typ, b)
	return err
}

// enqueueAudit 写"留痕已记录"事件；key = targetType:targetId。
func enqueueAudit(ctx context.Context, tx pgx.Tx, actor domain.Actor, at time.Time, a domain.AuditEvent) error {
	return enqueue(ctx, tx, domain.EventAuditRecorded, a.TargetType+":"+a.TargetID, actor, at, a)
}

// SeedIfEmpty 库里没有任何版本时载入原型样例（仅 RULE_SEED=prototype 时由入口调用）。
func SeedIfEmpty(ctx context.Context, pool *pgxpool.Pool, now time.Time) error {
	d, err := seed.Load(now)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockXact(ctx, tx, lockVersions); err != nil {
		return err
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM pricing_versions`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	for _, v := range d.Versions {
		if err := insertVersion(ctx, tx, v); err != nil {
			return fmt.Errorf("载入样例 %s：%w", v.VersionID, err)
		}
	}
	if err := saveParams(ctx, tx, d.Params.Items, d.Params.UpdatedAt, d.Params.UpdatedBy); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
