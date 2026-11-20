package iam

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// AuditRecord 是对外返回的留痕记录。
type AuditRecord struct {
	AuditID    string          `json:"auditId"`
	At         string          `json:"at"`
	AccountID  string          `json:"accountId"`
	ActorName  *string         `json:"actorName"`
	ActorKind  string          `json:"actorKind"`
	Action     string          `json:"action"`
	TargetType string          `json:"targetType"`
	TargetID   string          `json:"targetId"`
	Summary    string          `json:"summary"`
	Reason     *string         `json:"reason"`
	Before     json.RawMessage `json:"before"`
	After      json.RawMessage `json:"after"`
}

func (s *Service) handleListAudit(w http.ResponseWriter, r *http.Request, p *principal) {
	if err := requirePerm(p, "audit.view"); err != nil {
		s.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	where := []string{"TRUE"}
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if v := q.Get("accountId"); v != "" {
		add("account_id=$%d", v)
	}
	var actions []string
	for _, a := range q["action"] {
		for _, x := range strings.Split(a, ",") {
			if x = strings.TrimSpace(x); x != "" {
				actions = append(actions, x)
			}
		}
	}
	if len(actions) > 0 {
		add("action=ANY($%d)", actions)
	}
	if v := q.Get("targetType"); v != "" {
		add("target_type=$%d", v)
	}
	if v := q.Get("targetId"); v != "" {
		add("target_id=$%d", v)
	}
	if v := q.Get("actorKind"); v != "" {
		add("actor_kind=$%d", v)
	}
	for _, f := range []struct{ name, op string }{{"from", ">="}, {"to", "<="}} {
		v := q.Get(f.name)
		if v == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			s.fail(w, r, apiError("INVALID", f.name+" 应为带时区的时间，如 2026-12-01T12:00:00+08:00"))
			return
		}
		add("at"+f.op+"$%d", t)
	}
	if v := strings.TrimSpace(q.Get("q")); v != "" {
		args = append(args, likePattern(v))
		n := len(args)
		where = append(where, fmt.Sprintf("(summary LIKE $%d OR reason LIKE $%d OR target_id LIKE $%d)", n, n, n))
	}
	dir := sortDir(r, "DESC")
	order := fmt.Sprintf("at %s, audit_id %s", dir, dir)
	page, size := pageParams(r)
	cond := strings.Join(where, " AND ")
	ctx := r.Context()
	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM iam_audit WHERE `+cond, args...).Scan(&total); err != nil {
		s.fail(w, r, err)
		return
	}
	args = append(args, size, (page-1)*size)
	rows, err := s.db.Query(ctx, fmt.Sprintf(`SELECT audit_id, at, account_id, actor_name, actor_kind, action, target_type, target_id, summary, reason, before, after
		FROM iam_audit WHERE %s ORDER BY %s LIMIT $%d OFFSET $%d`, cond, order, len(args)-1, len(args)), args...)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rows.Close()
	list := []AuditRecord{}
	for rows.Next() {
		var a AuditRecord
		var at time.Time
		var before, after []byte
		if err := rows.Scan(&a.AuditID, &at, &a.AccountID, &a.ActorName, &a.ActorKind, &a.Action, &a.TargetType, &a.TargetID, &a.Summary, &a.Reason, &before, &after); err != nil {
			s.fail(w, r, err)
			return
		}
		a.At = s.fmtTime(at)
		a.Before, a.After = rawOrNull(before), rawOrNull(after)
		list = append(list, a)
	}
	if err := rows.Err(); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": total, "page": page, "pageSize": size, "rows": list})
}

func rawOrNull(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("null")
	}
	return b
}

func (s *Service) handleAuditActions(w http.ResponseWriter, r *http.Request, p *principal) {
	if err := requirePerm(p, "audit.view"); err != nil {
		s.fail(w, r, err)
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT DISTINCT action COLLATE "C" AS a FROM iam_audit ORDER BY a`)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rows.Close()
	list := []string{}
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			s.fail(w, r, err)
			return
		}
		list = append(list, a)
	}
	writeJSON(w, http.StatusOK, list)
}
