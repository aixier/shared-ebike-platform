package iam

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Modules 是发布留痕事件的全部 15 个模块（contracts/events/README.md 第 3、5 节）。
var Modules = []string{"acct", "ride", "opgw", "onboard", "fleet", "site", "rule", "bill", "pay", "deposit", "settle", "cs", "ops", "enforce", "iam"}

// Actor 是信封里的操作人。
type Actor struct {
	Kind string  `json:"kind"`
	ID   string  `json:"id"`
	Name *string `json:"name"`
}

// Envelope 是事件信封（contracts/events/README.md 第 1 节）。
type Envelope struct {
	EventID    string          `json:"eventId"`
	Type       string          `json:"type"`
	Version    int             `json:"version"`
	OccurredAt string          `json:"occurredAt"`
	Key        string          `json:"key"`
	Actor      *Actor          `json:"actor"`
	Payload    json.RawMessage `json:"payload"`
}

type auditPayload struct {
	Action     string  `json:"action"`
	TargetType string  `json:"targetType"`
	TargetID   string  `json:"targetId"`
	Summary    string  `json:"summary"`
	Reason     *string `json:"reason"`
	Before     any     `json:"before"`
	After      any     `json:"after"`
}

// systemActorID 是系统定时任务账号；本服务因收到事件而自动做的变更记在它名下。
const systemActorID = "AC0900"

var actorKinds = map[string]bool{"人": true, "智能体": true, "系统": true, "用户": true}

// enqueue 在业务事务里把事件写进发件表，返回事件编号。
func (s *Service) enqueue(ctx context.Context, tx pgx.Tx, typ, key string, actor Actor, payload any) (string, error) {
	pl, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	now := s.now()
	env := Envelope{EventID: "iam-" + randomID(), Type: typ, Version: 1, OccurredAt: s.fmtTime(now), Key: key, Actor: &actor, Payload: pl}
	body, err := json.Marshal(env)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO iam_outbox(event_id, subject, body, created_at) VALUES ($1,$2,$3,$4)`, env.EventID, typ, body, now)
	return env.EventID, err
}

// recordOwnAudit 记本服务自己的留痕：发 iam.audit.recorded，同时直接入库（自己再消费到时按 eventId 去重）。
func (s *Service) recordOwnAudit(ctx context.Context, tx pgx.Tx, actor Actor, p auditPayload) error {
	id, err := s.enqueue(ctx, tx, "iam.audit.recorded", p.TargetType+":"+p.TargetID, actor, p)
	if err != nil {
		return err
	}
	before, err := toObject(p.Before)
	if err != nil {
		return err
	}
	after, err := toObject(p.After)
	if err != nil {
		return err
	}
	if err := s.markProcessed(ctx, tx, id, "iam.audit.recorded"); err != nil {
		return err
	}
	return s.insertAudit(ctx, tx, id, "iam", s.now(), actor, p, before, after)
}

func toObject(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}

func (s *Service) markProcessed(ctx context.Context, tx pgx.Tx, eventID, typ string) error {
	_, err := tx.Exec(ctx, `INSERT INTO iam_processed_events(event_id, type, processed_at) VALUES ($1,$2,$3)`, eventID, typ, s.now())
	return err
}

func (s *Service) insertAudit(ctx context.Context, tx pgx.Tx, eventID, source string, at time.Time, actor Actor, p auditPayload, before, after []byte) error {
	day := at.In(s.loc).Format("2006-01-02")
	var n int
	if err := tx.QueryRow(ctx, `INSERT INTO iam_audit_day_seq(day, last) VALUES ($1, 1)
		ON CONFLICT (day) DO UPDATE SET last = iam_audit_day_seq.last + 1 RETURNING last`, day).Scan(&n); err != nil {
		return err
	}
	auditID := fmt.Sprintf("AU%s%04d", strings.ReplaceAll(day, "-", ""), n)
	name := actor.Name
	if (name == nil || *name == "") && actor.Kind != "用户" {
		// 没给显示名时取账号名称（智能体、系统、人）
		var nm string
		if err := tx.QueryRow(ctx, `SELECT name FROM iam_accounts WHERE account_id=$1`, actor.ID).Scan(&nm); err == nil {
			name = &nm
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	_, err := tx.Exec(ctx, `INSERT INTO iam_audit(audit_id, event_id, source, at, received_at, account_id, actor_name, actor_kind, action, target_type, target_id, summary, reason, before, after)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		auditID, eventID, source, at, s.now(), actor.ID, name, actor.Kind, p.Action, p.TargetType, p.TargetID, p.Summary, p.Reason, before, after)
	return err
}

// errReject 表示事件内容有误：留存备查并确认，不让总线无限重投。
type errReject struct{ msg string }

func (e *errReject) Error() string { return e.msg }

func reject(format string, args ...any) error { return &errReject{msg: fmt.Sprintf(format, args...)} }

// HandleEvent 处理一条订阅到的事件；返回错误表示暂时失败，等总线重投。
func (s *Service) HandleEvent(ctx context.Context, subject string, data []byte) error {
	err := s.handleEvent(ctx, subject, data)
	var rj *errReject
	if errors.As(err, &rj) {
		s.log.Warn("事件内容有误，已留存", "subject", subject, "err", rj.msg)
		_, dbErr := s.db.Exec(ctx, `INSERT INTO iam_rejected_events(subject, body, error, received_at) VALUES ($1,$2,$3,$4)`, subject, string(data), rj.msg, s.now())
		return dbErr
	}
	return err
}

func (s *Service) handleEvent(ctx context.Context, subject string, data []byte) error {
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return reject("信封不是有效的 JSON: %v", err)
	}
	if env.EventID == "" {
		return reject("缺 eventId")
	}
	typ := env.Type
	if typ == "" {
		typ = subject
	}
	var handle func(context.Context, pgx.Tx, *Envelope, string) error
	switch {
	case typ == "fleet.operator.exited":
		handle = s.onOperatorExited
	case strings.HasSuffix(typ, ".audit.recorded"):
		handle = s.onAuditRecorded
	default:
		return nil // 未订阅的类型，直接确认
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO iam_processed_events(event_id, type, processed_at) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, env.EventID, typ, s.now())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil // 已处理过
	}
	if err := handle(ctx, tx, &env, typ); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) onAuditRecorded(ctx context.Context, tx pgx.Tx, env *Envelope, typ string) error {
	at, err := time.Parse(time.RFC3339, env.OccurredAt)
	if err != nil {
		return reject("occurredAt 不是 RFC 3339: %q", env.OccurredAt)
	}
	actor := Actor{Kind: "系统", ID: systemActorID}
	if env.Actor != nil {
		actor = *env.Actor
	}
	if !actorKinds[actor.Kind] || actor.ID == "" {
		return reject("操作人不合法: %+v", actor)
	}
	var raw struct {
		Action     string          `json:"action"`
		TargetType string          `json:"targetType"`
		TargetID   string          `json:"targetId"`
		Summary    string          `json:"summary"`
		Reason     *string         `json:"reason"`
		Before     json.RawMessage `json:"before"`
		After      json.RawMessage `json:"after"`
	}
	if err := json.Unmarshal(env.Payload, &raw); err != nil {
		return reject("载荷不是有效的 JSON: %v", err)
	}
	if raw.Action == "" || raw.TargetType == "" || raw.TargetID == "" || raw.Summary == "" {
		return reject("载荷缺 action、targetType、targetId 或 summary")
	}
	p := auditPayload{Action: raw.Action, TargetType: raw.TargetType, TargetID: raw.TargetID, Summary: raw.Summary, Reason: raw.Reason}
	source := strings.TrimSuffix(typ, ".audit.recorded")
	return s.insertAudit(ctx, tx, env.EventID, source, at, actor, p, objectOrNil(raw.Before), objectOrNil(raw.After))
}

// objectOrNil：对象原样保存；null 或缺省为空；其他 JSON 值包成 {"value": ...}。
func objectOrNil(r json.RawMessage) []byte {
	t := bytes.TrimSpace(r)
	if len(t) == 0 || string(t) == "null" {
		return nil
	}
	if t[0] == '{' {
		return t
	}
	b, _ := json.Marshal(map[string]json.RawMessage{"value": t})
	return b
}

func (s *Service) onOperatorExited(ctx context.Context, tx pgx.Tx, env *Envelope, _ string) error {
	var p struct {
		OperatorID string  `json:"operatorId"`
		ExitedAt   string  `json:"exitedAt"`
		Reason     *string `json:"reason"`
	}
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		return reject("载荷不是有效的 JSON: %v", err)
	}
	if p.OperatorID == "" {
		return reject("缺 operatorId")
	}
	exitedAt, err := time.Parse(time.RFC3339, p.ExitedAt)
	if err != nil {
		exitedAt = s.now()
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_exited_operators(operator_id, exited_at, reason, event_id) VALUES ($1,$2,$3,$4) ON CONFLICT (operator_id) DO NOTHING`,
		p.OperatorID, exitedAt, p.Reason, env.EventID); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT account_id FROM iam_accounts WHERE operator_scope=$1 AND status=$2 ORDER BY account_id FOR UPDATE`, p.OperatorID, statusNormal)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	sys := s.systemActor(ctx, tx)
	for _, id := range ids {
		if _, err := tx.Exec(ctx, `UPDATE iam_accounts SET status=$2 WHERE account_id=$1`, id, statusReadonly); err != nil {
			return err
		}
		acct, err := s.getAccount(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := s.recordOwnAudit(ctx, tx, sys, auditPayload{
			Action: "调整权限", TargetType: "account", TargetID: id,
			Summary: fmt.Sprintf("运营商 %s 已退出，账号 %s 转只读", p.OperatorID, deref(acct.Username)), Reason: p.Reason,
			Before: map[string]any{"status": statusNormal}, After: map[string]any{"status": statusReadonly},
		}); err != nil {
			return err
		}
		if err := s.publishAccountChanged(ctx, tx, sys, acct, "转只读"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) systemActor(ctx context.Context, q querier) Actor {
	a := Actor{Kind: "系统", ID: systemActorID}
	var name string
	if err := q.QueryRow(ctx, `SELECT name FROM iam_accounts WHERE account_id=$1`, systemActorID).Scan(&name); err == nil {
		a.Name = &name
	}
	return a
}

// RelayOnce 把发件表里未发出的事件按顺序发到事件总线，返回发出的条数。
// publish 失败即停，剩下的下次再发；多实例时用 SKIP LOCKED 避免重复发送。
func (s *Service) RelayOnce(ctx context.Context, publish func(ctx context.Context, subject, msgID string, body []byte) error) (int, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT seq, event_id, subject, body FROM iam_outbox WHERE published_at IS NULL ORDER BY seq LIMIT 100 FOR UPDATE SKIP LOCKED`)
	if err != nil {
		return 0, err
	}
	type item struct {
		seq      int64
		id, subj string
		body     []byte
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.seq, &it.id, &it.subj, &it.body); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	sent := 0
	var pubErr error
	for _, it := range items {
		if pubErr = publish(ctx, it.subj, it.id, it.body); pubErr != nil {
			break
		}
		if _, err := tx.Exec(ctx, `UPDATE iam_outbox SET published_at=$2 WHERE seq=$1`, it.seq, s.now()); err != nil {
			return 0, err
		}
		sent++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return sent, pubErr
}
