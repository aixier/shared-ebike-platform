package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"bike/cs/internal/httpx"
)

// querier 是 pgxpool.Pool 与 pgx.Tx 的共同部分。
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

const ticketCols = `ticket_id, user_id, order_id, operator_id, category, entry, status, created_at, closed_at, assignee, assignee_name,
	facts, refund_proposal, force_end_proposal, force_end, awaiting_payee, refund_failed_payment_id, refund_pending, refund_seq`

func cst(t time.Time) time.Time { return t.In(CST) }

func cstp(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := t.In(CST)
	return &v
}

func scanTicket(row pgx.Row) (*Ticket, error) {
	t := &Ticket{}
	var facts, rp, fep, fe []byte
	err := row.Scan(&t.TicketID, &t.UserID, &t.OrderID, &t.OperatorID, &t.Category, &t.Entry, &t.Status, &t.CreatedAt, &t.ClosedAt,
		&t.Assignee, &t.AssigneeName, &facts, &rp, &fep, &fe, &t.AwaitingPayee, &t.RefundFailedPaymentID, &t.RefundPending, &t.RefundSeq)
	if err != nil {
		return nil, err
	}
	t.CreatedAt = cst(t.CreatedAt)
	t.ClosedAt = cstp(t.ClosedAt)
	for _, x := range []struct {
		b   []byte
		dst any
	}{{facts, &t.Facts}, {rp, &t.RefundProposal}, {fep, &t.ForceEndProposal}, {fe, &t.ForceEnd}} {
		if len(x.b) > 0 && string(x.b) != "null" {
			if err := json.Unmarshal(x.b, x.dst); err != nil {
				return nil, err
			}
		}
	}
	return t, nil
}

func jsonOrNil(v any) any {
	b, _ := json.Marshal(v)
	if string(b) == "null" {
		return nil
	}
	return b
}

// nextTicketID 发工单号：TK + 日期 + 4 位日序号。
func nextTicketID(ctx context.Context, tx pgx.Tx, now time.Time) (string, error) {
	day := now.In(CST).Format("20060102")
	var n int
	err := tx.QueryRow(ctx, `INSERT INTO ticket_day_seq (day, n) VALUES ($1, 1)
		ON CONFLICT (day) DO UPDATE SET n = ticket_day_seq.n + 1 RETURNING n`, day).Scan(&n)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("TK%s%04d", day, n), nil
}

func insertTicket(ctx context.Context, tx pgx.Tx, t *Ticket) error {
	_, err := tx.Exec(ctx, `INSERT INTO tickets (`+ticketCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`,
		t.TicketID, t.UserID, t.OrderID, t.OperatorID, t.Category, t.Entry, t.Status, t.CreatedAt, t.ClosedAt, t.Assignee, t.AssigneeName,
		jsonOrNil(t.Facts), jsonOrNil(t.RefundProposal), jsonOrNil(t.ForceEndProposal), jsonOrNil(t.ForceEnd),
		t.AwaitingPayee, t.RefundFailedPaymentID, t.RefundPending, t.RefundSeq)
	return err
}

// saveTicket 写回可变字段；订单事实快照不在此列。
func saveTicket(ctx context.Context, tx pgx.Tx, t *Ticket) error {
	_, err := tx.Exec(ctx, `UPDATE tickets SET category=$2, status=$3, closed_at=$4, assignee=$5, assignee_name=$6,
		refund_proposal=$7, force_end_proposal=$8, force_end=$9, awaiting_payee=$10, refund_failed_payment_id=$11, refund_pending=$12, refund_seq=$13
		WHERE ticket_id=$1`,
		t.TicketID, t.Category, t.Status, t.ClosedAt, t.Assignee, t.AssigneeName,
		jsonOrNil(t.RefundProposal), jsonOrNil(t.ForceEndProposal), jsonOrNil(t.ForceEnd), t.AwaitingPayee, t.RefundFailedPaymentID, t.RefundPending, t.RefundSeq)
	return err
}

func addMessage(ctx context.Context, tx pgx.Tx, t *Ticket, from, text string, at time.Time) error {
	at = cst(at.Truncate(time.Second))
	if _, err := tx.Exec(ctx, `INSERT INTO ticket_messages (ticket_id, sender, at, text) VALUES ($1,$2,$3,$4)`, t.TicketID, from, at, text); err != nil {
		return err
	}
	t.Messages = append(t.Messages, Message{From: from, At: at, Text: text})
	return nil
}

func addAction(ctx context.Context, tx pgx.Tx, t *Ticket, typ string, paymentID *string, by string, at time.Time) error {
	at = cst(at.Truncate(time.Second))
	if _, err := tx.Exec(ctx, `INSERT INTO ticket_actions (ticket_id, type, payment_id, at, by_account) VALUES ($1,$2,$3,$4,$5)`, t.TicketID, typ, paymentID, at, by); err != nil {
		return err
	}
	t.Actions = append(t.Actions, Action{Type: typ, PaymentID: paymentID, At: at, By: by})
	return nil
}

// loadChildren 读消息与处理动作。
func loadChildren(ctx context.Context, q querier, ts []*Ticket) error {
	if len(ts) == 0 {
		return nil
	}
	ids := make([]string, len(ts))
	byID := map[string]*Ticket{}
	for i, t := range ts {
		ids[i] = t.TicketID
		byID[t.TicketID] = t
		t.Messages, t.Actions = []Message{}, []Action{}
	}
	rows, err := q.Query(ctx, `SELECT ticket_id, sender, at, text FROM ticket_messages WHERE ticket_id = ANY($1) ORDER BY id`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		var m Message
		if err := rows.Scan(&id, &m.From, &m.At, &m.Text); err != nil {
			rows.Close()
			return err
		}
		m.At = cst(m.At)
		byID[id].Messages = append(byID[id].Messages, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = q.Query(ctx, `SELECT ticket_id, type, payment_id, at, by_account FROM ticket_actions WHERE ticket_id = ANY($1) ORDER BY id`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var a Action
		if err := rows.Scan(&id, &a.Type, &a.PaymentID, &a.At, &a.By); err != nil {
			return err
		}
		a.At = cst(a.At)
		byID[id].Actions = append(byID[id].Actions, a)
	}
	return rows.Err()
}

// loadTicket 读一张工单；forUpdate 时加行锁。找不到返回 NOT_FOUND。
func loadTicket(ctx context.Context, q querier, id string, forUpdate bool) (*Ticket, error) {
	sql := `SELECT ` + ticketCols + ` FROM tickets WHERE ticket_id = $1`
	if forUpdate {
		sql += ` FOR UPDATE`
	}
	t, err := scanTicket(q.QueryRow(ctx, sql, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.E("NOT_FOUND", "没有找到该工单")
	}
	if err != nil {
		return nil, err
	}
	if err := loadChildren(ctx, q, []*Ticket{t}); err != nil {
		return nil, err
	}
	return t, nil
}

// queryTickets 按条件读多张工单（含消息与动作）。
func queryTickets(ctx context.Context, q querier, where string, args ...any) ([]*Ticket, error) {
	rows, err := q.Query(ctx, `SELECT `+ticketCols+` FROM tickets `+where, args...)
	if err != nil {
		return nil, err
	}
	var ts []*Ticket
	for rows.Next() {
		t, err := scanTicket(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		ts = append(ts, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := loadChildren(ctx, q, ts); err != nil {
		return nil, err
	}
	return ts, nil
}

// refundLimit 读当前客服退款授权额度。
func (a *App) refundLimit(ctx context.Context, q querier) (int64, error) {
	var v int64
	err := q.QueryRow(ctx, `SELECT refund_limit FROM platform_params WHERE id = 1`).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return a.DefaultRefundLimit, nil
	}
	return v, err
}
