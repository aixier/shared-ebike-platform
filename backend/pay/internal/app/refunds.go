package app

// 原路失败：用户提交本人实名收款账户（户名由 acct 核对），财务线下转账后录入。
// trace: R-11、R-47、NFR-11

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"bike/pay/internal/clients/acct"
)

// PayeeChecker 核对收款户名（acct）
type PayeeChecker interface {
	CheckPayeeName(ctx context.Context, userID, name string) (bool, error)
}

// RefundFailure 契约 RefundFailure
type RefundFailure struct {
	PaymentID      string          `json:"paymentId"`
	Type           string          `json:"type"`
	UserID         string          `json:"userId"`
	OrderID        *string         `json:"orderId"`
	Amount         int64           `json:"amount"`
	FailReason     *string         `json:"failReason"`
	CreatedAt      string          `json:"createdAt"`
	TicketID       *string         `json:"ticketId"`
	PayeeSubmitted *PayeeSubmitted `json:"payeeSubmitted"`
	Status         string          `json:"status"`
}

// 失败且还没有成功的人工转账的退款、退押
const refundFailureWhere = `WHERE c.type IN ('退款','退押') AND c.status = '失败'
  AND NOT EXISTS (SELECT 1 FROM payment_current m WHERE m.related_payment_id = c.payment_id
                  AND m.type IN ('人工转账退款','人工转账退押') AND m.status = '成功')`

func (a *App) refundFailures(ctx context.Context) ([]RefundFailure, error) {
	ps, err := a.queryPayments(ctx, a.Pool, refundFailureWhere+` ORDER BY c.created_at, c.seq`)
	if err != nil {
		return nil, err
	}
	out := []RefundFailure{}
	for _, p := range ps {
		st := "待用户填写收款账户"
		if p.PayeeSubmitted != nil {
			st = "待转账"
		}
		out = append(out, RefundFailure{PaymentID: p.PaymentID, Type: p.Type, UserID: deref(p.UserID), OrderID: p.OrderID, Amount: p.Amount,
			FailReason: p.FailReason, CreatedAt: p.CreatedAt, TicketID: p.TicketID, PayeeSubmitted: p.PayeeSubmitted, Status: st})
	}
	return out, nil
}

func (a *App) isRefundFailure(ctx context.Context, q querier, id string) (bool, error) {
	ps, err := a.queryPayments(ctx, q, refundFailureWhere+` AND c.payment_id = $1`, id)
	return len(ps) > 0, err
}

// maskName 脱敏户名：首字 + 全角星号（同原型 util.maskName）
func maskName(n string) string {
	r := []rune(n)
	if len(r) == 0 {
		return n
	}
	k := len(r) - 1
	if k < 1 {
		k = 1
	}
	return string(r[0]) + strings.Repeat("＊", k)
}

// maskAccount 账号只留首尾四位（同原型 util.maskAccount）
func maskAccount(acc string) string {
	s := strings.ReplaceAll(acc, " ", "")
	if len(s) < 8 {
		return s
	}
	return fmt.Sprintf("%s **** **** %s", s[:4], s[len(s)-4:])
}

var accountRe = regexp.MustCompile(`^\d{12,19}$`)

type submitPayeeReq struct {
	Name    string `json:"name"`
	Bank    string `json:"bank"`
	Account string `json:"account"`
}

type submitPayeeResp struct {
	OK    bool           `json:"ok"`
	Payee PayeeSubmitted `json:"payee"`
	Text  string         `json:"text"`
}

func (a *App) submitPayee(ctx context.Context, userID, id string, req submitPayeeReq) (*submitPayeeResp, *APIError, error) {
	p, err := a.getPayment(ctx, a.Pool, id)
	if err != nil {
		return nil, nil, err
	}
	if p == nil || deref(p.UserID) != userID {
		return nil, apiErr("NOT_FOUND", "没有找到需要填写收款账户的退款"), nil
	}
	if ok, err := a.isRefundFailure(ctx, a.Pool, id); err != nil {
		return nil, nil, err
	} else if !ok {
		return nil, apiErr("NOT_FOUND", "没有找到需要填写收款账户的退款"), nil
	}
	name := strings.TrimSpace(req.Name)
	acc := strings.ReplaceAll(req.Account, " ", "")
	bank := strings.TrimSpace(req.Bank)
	if name == "" {
		return nil, apiErr("INVALID", "请填写户名"), nil
	}
	if !accountRe.MatchString(acc) {
		return nil, apiErr("INVALID", "银行卡号格式不正确"), nil
	}
	if bank == "" {
		return nil, apiErr("INVALID", "请填写开户银行"), nil
	}
	match, err := a.Acct.CheckPayeeName(ctx, userID, name)
	if errors.Is(err, acct.ErrUserNotFound) {
		match, err = false, nil // 找不到用户按户名不一致处理，不暴露用户存在与否
	}
	if err != nil {
		a.Log.Warn("核对收款户名失败", "err", err)
		return nil, apiErr("SERVER_BUSY", "暂时无法核对户名，请稍后再试"), nil
	}
	if !match {
		return nil, apiErr("PAYEE_NAME_MISMATCH", ""), nil
	}
	now := a.Now()
	sub := PayeeSubmitted{Name: maskName(name), Bank: bank, Account: maskAccount(acc), SubmittedAt: fmtTime(now)}
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO payee_submissions (payment_id, user_id, name_masked, bank, account_tail, submitted_at) VALUES ($1,$2,$3,$4,$5,$6)`,
		id, userID, sub.Name, sub.Bank, sub.Account, now); err != nil {
		return nil, nil, err
	}
	if err := a.enqueue(ctx, tx, "pay.payee.submitted", p.BizNo, Actor{Kind: "用户", ID: userID}, map[string]any{
		"paymentId": id, "bizNo": p.BizNo, "account": p.Account, "userId": userID, "orderId": p.OrderID, "submittedAt": sub.SubmittedAt,
	}); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return &submitPayeeResp{OK: true, Payee: sub, Text: "已提交，平台公司财务将人工转账，到账后通知你"}, nil, nil
}

type transferReq struct {
	Payee   *Payee  `json:"payee"`
	TradeNo *string `json:"tradeNo"`
	Reason  string  `json:"reason"`
}

func (a *App) transfer(ctx context.Context, admin Actor, id string, req transferReq) (*Payment, *APIError, error) {
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)
	if err := lockKey(ctx, tx, "manual:"+id); err != nil {
		return nil, nil, err
	}
	p, err := a.getPayment(ctx, tx, id)
	if err != nil {
		return nil, nil, err
	}
	if p == nil {
		return nil, apiErr("NOT_FOUND", "没有找到该流水"), nil
	}
	if ok, err := a.isRefundFailure(ctx, tx, id); err != nil {
		return nil, nil, err
	} else if !ok {
		return nil, apiErr("CONFLICT", "该笔不是待人工转账的退款"), nil
	}
	var pe *Payee
	if req.Payee != nil && req.Payee.Name != "" && req.Payee.Bank != "" && req.Payee.Account != "" {
		pe = &Payee{Name: maskName(req.Payee.Name), Bank: req.Payee.Bank, Account: maskAccount(req.Payee.Account)}
	} else if p.PayeeSubmitted != nil {
		pe = &Payee{Name: p.PayeeSubmitted.Name, Bank: p.PayeeSubmitted.Bank, Account: p.PayeeSubmitted.Account}
	}
	if pe == nil {
		return nil, apiErr("INVALID", "用户尚未提供本人实名收款账户"), nil
	}
	typ, account := "人工转账退款", "骑行收入户"
	if p.Type == "退押" {
		typ, account = "人工转账退押", "押金专户"
	}
	now := a.Now()
	reason := req.Reason
	np := newPayment{BizNo: p.BizNo, Type: typ, Account: account, Direction: "付", Channel: "银行转账", UserID: p.UserID, OrderID: p.OrderID,
		Amount: p.Amount, RelatedPaymentID: &p.PaymentID, TicketID: p.TicketID, OperatorID: &admin.ID, Payee: pe}
	var trade *string
	if req.TradeNo != nil && strings.TrimSpace(*req.TradeNo) != "" {
		t := strings.TrimSpace(*req.TradeNo)
		trade = &t
	}
	mid, err := a.insertPayment(ctx, tx, np, "成功", "人工录入", trade, &now, &admin, &reason)
	if err != nil {
		return nil, nil, err
	}
	m, err := a.getPayment(ctx, tx, mid)
	if err != nil {
		return nil, nil, err
	}
	if err := a.emitResult(ctx, tx, m, admin); err != nil {
		return nil, nil, err
	}
	summary := fmt.Sprintf("原路退回失败（%s），人工转账 ¥%s 至 %s %s", id, yuan(p.Amount), pe.Bank, pe.Account)
	if err := a.audit(ctx, tx, admin, "人工转账", "payment", mid, summary, &reason, nil, map[string]any{"paymentId": mid, "amount": p.Amount}); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return m, nil, nil
}

// yuan 分转元的显示（留痕摘要用）
func yuan(f int64) string {
	neg := ""
	if f < 0 {
		neg, f = "-", -f
	}
	return fmt.Sprintf("%s%d.%02d", neg, f/100, f%100)
}
