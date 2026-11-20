package app

// 资金流水：写入（只追加）与读出（契约 Payment、UserPayment）。
// trace: R-18、R-12；SCHEMA 5.6

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Payee 收款人
type Payee struct {
	Name    string `json:"name"`
	Bank    string `json:"bank"`
	Account string `json:"account"`
}

// PayeeSubmitted 用户提交的本人收款账户（脱敏）
type PayeeSubmitted struct {
	Name        string `json:"name"`
	Bank        string `json:"bank"`
	Account     string `json:"account"`
	SubmittedAt string `json:"submittedAt"`
}

// Payment 资金流水（契约 Payment）
type Payment struct {
	PaymentID          string          `json:"paymentId"`
	Type               string          `json:"type"`
	Account            string          `json:"account"`
	Direction          string          `json:"direction"`
	UserID             *string         `json:"userId"`
	OrderID            *string         `json:"orderId"`
	CardID             *string         `json:"cardId"`
	Amount             int64           `json:"amount"`
	Channel            string          `json:"channel"`
	ChannelTradeNo     *string         `json:"channelTradeNo"`
	Status             string          `json:"status"`
	FailReason         *string         `json:"failReason"`
	CreatedAt          string          `json:"createdAt"`
	CompletedAt        *string         `json:"completedAt"`
	ChannelSettledDate *string         `json:"channelSettledDate"`
	RelatedPaymentID   *string         `json:"relatedPaymentId"`
	TicketID           *string         `json:"ticketId"`
	OperatorAccountID  *string         `json:"operatorAccountId"`
	Payee              *Payee          `json:"payee"`
	PayeeSubmitted     *PayeeSubmitted `json:"payeeSubmitted"`
	Note               *string         `json:"note"`

	// 以下不出接口
	BizNo       string  `json:"-"`
	FailCode    *string `json:"-"`
	Credit      bool    `json:"-"`
	createdAtT  time.Time
	completedAt *time.Time
}

// UserPayment 用户本人看的流水
type UserPayment struct {
	PaymentID   string  `json:"paymentId"`
	Type        string  `json:"type"`
	Amount      int64   `json:"amount"`
	Channel     string  `json:"channel"`
	Status      string  `json:"status"`
	FailReason  *string `json:"failReason"`
	CreatedAt   string  `json:"createdAt"`
	CompletedAt *string `json:"completedAt"`
}

func (p *Payment) userView() UserPayment {
	return UserPayment{PaymentID: p.PaymentID, Type: p.Type, Amount: p.Amount, Channel: p.Channel, Status: p.Status,
		FailReason: p.FailReason, CreatedAt: p.CreatedAt, CompletedAt: p.CompletedAt}
}

const paymentSelect = `
SELECT c.payment_id, c.biz_no, c.type, c.account, c.direction, c.user_id, c.order_id, c.card_id, c.amount, c.channel, c.credit,
       c.related_payment_id, c.ticket_id, c.operator_account_id, c.payee, c.note, c.created_at,
       c.status, c.fail_code, c.fail_reason, c.channel_trade_no, c.completed_at,
       (SELECT min(b.settled_date)::text FROM channel_bill_lines b WHERE b.out_no = c.payment_id) AS settled,
       ps.name_masked, ps.bank, ps.account_tail, ps.submitted_at
FROM payment_current c
LEFT JOIN LATERAL (SELECT * FROM payee_submissions s WHERE s.payment_id = c.payment_id ORDER BY s.id DESC LIMIT 1) ps ON true
`

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func scanPayments(rows pgx.Rows) ([]*Payment, error) {
	defer rows.Close()
	var out []*Payment
	for rows.Next() {
		p := &Payment{}
		var payee []byte
		var settled, psName, psBank, psAcc *string
		var psAt *time.Time
		if err := rows.Scan(&p.PaymentID, &p.BizNo, &p.Type, &p.Account, &p.Direction, &p.UserID, &p.OrderID, &p.CardID, &p.Amount,
			&p.Channel, &p.Credit, &p.RelatedPaymentID, &p.TicketID, &p.OperatorAccountID, &payee, &p.Note, &p.createdAtT,
			&p.Status, &p.FailCode, &p.FailReason, &p.ChannelTradeNo, &p.completedAt, &settled, &psName, &psBank, &psAcc, &psAt); err != nil {
			return nil, err
		}
		p.CreatedAt = fmtTime(p.createdAtT)
		if p.Status == "成功" {
			p.CompletedAt = fmtTimePtr(p.completedAt)
		}
		p.ChannelSettledDate = settled
		if len(payee) > 0 {
			p.Payee = &Payee{}
			_ = json.Unmarshal(payee, p.Payee)
		}
		if psName != nil {
			p.PayeeSubmitted = &PayeeSubmitted{Name: *psName, Bank: *psBank, Account: *psAcc, SubmittedAt: fmtTime(*psAt)}
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (a *App) queryPayments(ctx context.Context, q querier, where string, args ...any) ([]*Payment, error) {
	rows, err := q.Query(ctx, paymentSelect+" "+where, args...)
	if err != nil {
		return nil, err
	}
	return scanPayments(rows)
}

func (a *App) getPayment(ctx context.Context, q querier, id string) (*Payment, error) {
	ps, err := a.queryPayments(ctx, q, `WHERE c.payment_id = $1`, id)
	if err != nil {
		return nil, err
	}
	if len(ps) == 0 {
		return nil, nil
	}
	return ps[0], nil
}

// newPayment 待写入的流水头
type newPayment struct {
	BizNo, Type, Account, Direction, Channel string
	UserID, OrderID, CardID                  *string
	Amount                                   int64
	Credit                                   bool
	RelatedPaymentID, TicketID, OperatorID   *string
	Payee                                    *Payee
	Note                                     *string
}

func randDigits(n int) string {
	s := ""
	for i := 0; i < n; i++ {
		d, _ := rand.Int(rand.Reader, big.NewInt(10))
		s += fmt.Sprint(d.Int64())
	}
	return s
}

// insertPayment 写流水头与第一条状态记录；流水号 PY + 创建时刻（到秒）+ 3 位
func (a *App) insertPayment(ctx context.Context, tx pgx.Tx, np newPayment, status, source string, tradeNo *string, completedAt *time.Time, actor *Actor, reason *string) (string, error) {
	now := a.Now()
	var payee []byte
	if np.Payee != nil {
		payee, _ = json.Marshal(np.Payee)
	}
	for i := 0; i < 20; i++ {
		id := "PY" + now.In(bj).Format("20060102150405") + randDigits(3)
		_, err := tx.Exec(ctx, `SAVEPOINT ins`)
		if err != nil {
			return "", err
		}
		_, err = tx.Exec(ctx, `INSERT INTO payments (payment_id, biz_no, type, account, direction, user_id, order_id, card_id, amount, channel, credit,
			related_payment_id, ticket_id, operator_account_id, payee, note, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
			id, np.BizNo, np.Type, np.Account, np.Direction, np.UserID, np.OrderID, np.CardID, np.Amount, np.Channel, np.Credit,
			np.RelatedPaymentID, np.TicketID, np.OperatorID, payee, np.Note, now)
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT ins`); err != nil {
				return "", err
			}
			continue
		}
		if err != nil {
			return "", err
		}
		if err := a.addResult(ctx, tx, id, status, nil, nil, tradeNo, completedAt, source, actor, reason); err != nil {
			return "", err
		}
		return id, nil
	}
	return "", errors.New("流水号冲突过多")
}

// addResult 追加一条状态记录
func (a *App) addResult(ctx context.Context, tx pgx.Tx, id, status string, failCode, failReason, tradeNo *string, completedAt *time.Time, source string, actor *Actor, reason *string) error {
	var aj []byte
	if actor != nil {
		aj, _ = json.Marshal(actor)
	}
	_, err := tx.Exec(ctx, `INSERT INTO payment_results (payment_id, status, fail_code, fail_reason, channel_trade_no, completed_at, source, actor, reason, recorded_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, id, status, failCode, failReason, tradeNo, completedAt, source, aj, reason, a.Now())
	return err
}

// flowHead 事件的"流水头"
func flowHead(p *Payment) map[string]any {
	return map[string]any{
		"paymentId": p.PaymentID, "bizNo": p.BizNo, "type": p.Type, "account": p.Account,
		"userId": p.UserID, "orderId": p.OrderID, "amount": p.Amount, "channel": p.Channel,
	}
}

func isCollect(t string) bool {
	switch t {
	case "订单扣款", "订单支付", "欠费补付", "月卡购买", "押金缴纳", "押金补缴", "押金抵扣":
		return true
	}
	return false
}

// emitResult 按流水当前状态发事件（成功、失败、结果未知；退款类发退款成功、退款失败）
func (a *App) emitResult(ctx context.Context, tx pgx.Tx, p *Payment, actor Actor) error {
	h := flowHead(p)
	now := fmtTime(a.Now())
	if isCollect(p.Type) {
		switch p.Status {
		case "成功":
			h["completedAt"] = p.CompletedAt
			h["channelTradeNo"] = p.ChannelTradeNo
			return a.enqueue(ctx, tx, "pay.payment.succeeded", p.BizNo, actor, h)
		case "失败":
			h["failReason"] = p.FailReason
			h["failedAt"] = now
			return a.enqueue(ctx, tx, "pay.payment.failed", p.BizNo, actor, h)
		default:
			h["since"] = p.CreatedAt
			return a.enqueue(ctx, tx, "pay.payment.unknown", p.BizNo, actor, h)
		}
	}
	h["relatedPaymentId"] = p.RelatedPaymentID
	switch p.Status {
	case "成功":
		h["completedAt"] = p.CompletedAt
		return a.enqueue(ctx, tx, "pay.refund.succeeded", p.BizNo, actor, h)
	case "失败":
		h["failReason"] = p.FailReason
		h["failedAt"] = now
		return a.enqueue(ctx, tx, "pay.refund.failed", p.BizNo, actor, h)
	}
	return nil // 退款处理中：契约没有对应事件，到账或失败后再发
}
