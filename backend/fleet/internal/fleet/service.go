// Package fleet 是车码与接入状态服务的实现：运营商、品牌、车联网服务商登记，车辆清单与政府码，
// 接入状态机，暂停可租记录，以及发件表事件与留痕。
package fleet

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Service 持有数据库与时钟。时钟一律用服务器时间。
type Service struct {
	db           *pgxpool.Pool
	now          func() time.Time
	systemActor  Actor // 系统自动执行的动作（如按清单发码）记在这个操作人名下
	readonlyDays int   // 运营商退出后只读保留天数；0 为不设截止（readonlyUntil 为 null）
}

// SetReadonlyDays 设置退出后的只读保留天数（0 为不设截止）。
func (s *Service) SetReadonlyDays(n int) { s.readonlyDays = n }

func NewService(db *pgxpool.Pool, now func() time.Time, systemActor Actor) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{db: db, now: now, systemActor: systemActor}
}

func (s *Service) tx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.db, fn)
}

// nextSeq 发号：计数器加一并返回。
func nextSeq(ctx context.Context, tx pgx.Tx, name string) (int64, error) {
	var v int64
	err := tx.QueryRow(ctx, `INSERT INTO counters (name, value) VALUES ($1, 1)
		ON CONFLICT (name) DO UPDATE SET value = counters.value + 1 RETURNING value`, name).Scan(&v)
	return v, err
}

// bumpSeq 把计数器抬到至少 v（登记已有编号后调用，避免发重）。
func bumpSeq(ctx context.Context, tx pgx.Tx, name string, v int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO counters (name, value) VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE SET value = GREATEST(counters.value, EXCLUDED.value)`, name, v)
	return err
}

// newGovCode 发政府码：SF + 8 位随机数字，全表不重复（码不复用，车辆记录不删）。
func newGovCode(ctx context.Context, tx pgx.Tx) (string, error) {
	for i := 0; i < 50; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(100000000))
		if err != nil {
			return "", err
		}
		code := fmt.Sprintf("SF%08d", n.Int64())
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM vehicles WHERE gov_code=$1)`, code).Scan(&exists); err != nil {
			return "", err
		}
		if !exists {
			return code, nil
		}
	}
	return "", errors.New("政府码发号多次冲突")
}

// newEventID 生成 UUID v4 形式的事件编号。
func newEventID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// envelope 是事件信封（contracts/events/README.md 第 1 节）。
type envelope struct {
	EventID    string `json:"eventId"`
	Type       string `json:"type"`
	Version    int    `json:"version"`
	OccurredAt string `json:"occurredAt"`
	Key        string `json:"key"`
	Actor      Actor  `json:"actor"`
	Payload    any    `json:"payload"`
}

// emit 在业务事务里把事件写进发件表。
func (s *Service) emit(ctx context.Context, tx pgx.Tx, typ, key string, actor Actor, at time.Time, payload any) error {
	e := envelope{EventID: newEventID(), Type: typ, Version: 1, OccurredAt: fmtTime(at), Key: key, Actor: actor, Payload: payload}
	_, err := tx.Exec(ctx, `INSERT INTO outbox (event_id, type, key, body) VALUES ($1, $2, $3, $4)`,
		e.EventID, typ, key, mustJSON(e))
	return err
}

// audit 写"留痕已记录"事件（fleet.audit.recorded）。
func (s *Service) audit(ctx context.Context, tx pgx.Tx, actor Actor, at time.Time, action, targetType, targetID, summary string, reason *string, before, after map[string]any) error {
	payload := map[string]any{
		"action": action, "targetType": targetType, "targetId": targetID, "summary": summary,
		"reason": reason, "before": nilIfEmpty(before), "after": nilIfEmpty(after),
	}
	return s.emit(ctx, tx, "fleet.audit.recorded", targetType+":"+targetID, actor, at, payload)
}

func nilIfEmpty(m map[string]any) any {
	if len(m) == 0 {
		return nil
	}
	return m
}

func strPtr(s string) *string { return &s }
