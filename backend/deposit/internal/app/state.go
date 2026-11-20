package app

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type account struct {
	exists         bool
	UserID         string
	Mode           string
	DepositID      *string
	PrevDepositID  *string
	CreditProvider *string
	CreditScore    *int
	CreditOpenedAt *time.Time
}

type deposit struct {
	ID         string
	UserID     string
	AmountPaid int64
	Balance    int64
	Status     string
	PaidAt     time.Time
	PaymentID  string
	Channel    string
}

type refundReq struct {
	ID              string
	UserID          string
	DepositID       string
	Amount          int64
	AppliedAt       time.Time
	ExpectedAt      time.Time
	StoppedAt       *time.Time
	PaymentID       *string
	ManualPaymentID *string
	AwaitingPayee   bool
	Status          string
	RequestedBy     Actor
}

// lockAccount 取并锁定账户行，没有则建一行未开通。
func lockAccount(ctx context.Context, tx pgx.Tx, uid string, now time.Time) (account, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO accounts (user_id, updated_at) VALUES ($1,$2) ON CONFLICT DO NOTHING`, uid, now); err != nil {
		return account{}, err
	}
	return scanAccount(tx.QueryRow(ctx, accountSelect+` FOR UPDATE`, uid))
}

const accountSelect = `SELECT user_id, mode, deposit_id, prev_deposit_id, credit_provider, credit_score, credit_opened_at FROM accounts WHERE user_id = $1`

func scanAccount(row pgx.Row) (account, error) {
	a := account{exists: true}
	err := row.Scan(&a.UserID, &a.Mode, &a.DepositID, &a.PrevDepositID, &a.CreditProvider, &a.CreditScore, &a.CreditOpenedAt)
	if err == pgx.ErrNoRows {
		return account{Mode: "未开通"}, nil
	}
	return a, err
}

func readAccount(ctx context.Context, q querier, uid string) (account, error) {
	a, err := scanAccount(q.QueryRow(ctx, accountSelect, uid))
	if !a.exists {
		a.UserID = uid
	}
	return a, err
}

const depositSelect = `SELECT deposit_id, user_id, amount_paid, balance, status, paid_at, payment_id, channel FROM deposits WHERE deposit_id = $1`

func readDeposit(ctx context.Context, q querier, id string, forUpdate bool) (*deposit, error) {
	sql := depositSelect
	if forUpdate {
		sql += " FOR UPDATE"
	}
	d := &deposit{}
	err := q.QueryRow(ctx, sql, id).Scan(&d.ID, &d.UserID, &d.AmountPaid, &d.Balance, &d.Status, &d.PaidAt, &d.PaymentID, &d.Channel)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return d, err
}

const refundSelect = `SELECT request_id, user_id, deposit_id, amount, applied_at, expected_at, stopped_at, payment_id, manual_payment_id,
	awaiting_payee, status, requested_by FROM refund_requests`

func scanRefund(row pgx.Row) (*refundReq, error) {
	r := &refundReq{}
	var by []byte
	err := row.Scan(&r.ID, &r.UserID, &r.DepositID, &r.Amount, &r.AppliedAt, &r.ExpectedAt, &r.StoppedAt, &r.PaymentID, &r.ManualPaymentID, &r.AwaitingPayee, &r.Status, &by)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(by, &r.RequestedBy)
	return r, nil
}

// latestRefund 该押金最近一次未被拒绝的退押申请。
func latestRefund(ctx context.Context, q querier, depositID string) (*refundReq, error) {
	return scanRefund(q.QueryRow(ctx, refundSelect+` WHERE deposit_id = $1 AND status <> '已拒绝' ORDER BY applied_at DESC, request_id DESC LIMIT 1`, depositID))
}

// ---- 待处理项（BR-23）----

type blocker struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Next    map[string]any `json:"next,omitempty"`
	OrderID *string        `json:"orderId,omitempty"`
	Amount  *int64         `json:"amount,omitempty"`
}

func next(label, action string) map[string]any {
	return map[string]any{"label": label, "action": action}
}

// refundBlockers 依本模块投影列出退押前的待处理项：骑行中订单、未结欠费、待支付订单（含处理中的押金抵扣）。
func refundBlockers(ctx context.Context, q querier, uid string) ([]blocker, error) {
	out := []blocker{}
	var riding string
	err := q.QueryRow(ctx, `SELECT order_id FROM proj_riding WHERE user_id = $1 ORDER BY created_at, order_id LIMIT 1`, uid).Scan(&riding)
	if err != nil && err != pgx.ErrNoRows {
		return nil, err
	}
	if riding != "" {
		out = append(out, blocker{Code: "HAS_RIDING", Message: "有一单正在骑行，还车后再处理", Next: next("查看骑行中订单", "goRide"), OrderID: &riding})
	}
	var arrears int64
	if err := q.QueryRow(ctx, `SELECT COALESCE(sum(unpaid),0) FROM proj_bills WHERE user_id = $1 AND state = '欠费'`, uid).Scan(&arrears); err != nil {
		return nil, err
	}
	if arrears > 0 {
		out = append(out, blocker{Code: "HAS_ARREARS", Message: fmt.Sprintf("有 ¥%s 欠费未结清", yuan(arrears)), Next: next("一键补付", "payArrears"), Amount: &arrears})
	}
	var n int
	var first *string
	if err := q.QueryRow(ctx, `SELECT count(*), min(order_id) FROM (
			SELECT order_id FROM proj_bills WHERE user_id = $1 AND state = '待支付'
			UNION SELECT order_id FROM deductions WHERE user_id = $1 AND status = '处理中') t`, uid).Scan(&n, &first); err != nil {
		return nil, err
	}
	if n > 0 {
		out = append(out, blocker{Code: "UNPAID_ORDER", Message: fmt.Sprintf("有 %d 单待支付", n), Next: next("去支付", "payOrder"), OrderID: first})
	}
	return out, nil
}

// ---- 输出形状（deposit.yaml）----

func (a *App) ts(t time.Time) string { return t.In(a.Loc).Format(time.RFC3339) }

func (a *App) tsp(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := a.ts(*t)
	return &s
}

func (a *App) refundView(r *refundReq) map[string]any {
	if r == nil {
		return nil
	}
	return map[string]any{
		"appliedAt": a.ts(r.AppliedAt), "stoppedAt": a.tsp(r.StoppedAt), "expectedAt": a.ts(r.ExpectedAt), "amount": r.Amount,
		"paymentId": r.PaymentID, "manualPaymentId": r.ManualPaymentID, "awaitingPayee": r.AwaitingPayee,
	}
}

func paymentIDs(ctx context.Context, q querier, sql, depositID string) ([]string, error) {
	rows, err := q.Query(ctx, sql, depositID)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if ids == nil {
		ids = []string{}
	}
	return ids, err
}

const topupIDsSQL = `SELECT payment_id FROM charges WHERE deposit_id = $1 AND kind = '押金补缴' AND status = '成功' ORDER BY completed_at, charge_id`
const deductionIDsSQL = `SELECT payment_id FROM deductions WHERE deposit_id = $1 AND status = '成功' ORDER BY completed_at, deduction_id`

// stateView 生成 DepositState（三种形态之一）。
func (a *App) stateView(ctx context.Context, q querier, acc account) (map[string]any, *deposit, error) {
	switch acc.Mode {
	case "免押":
		v := map[string]any{"mode": "免押", "provider": acc.CreditProvider, "score": acc.CreditScore, "creditOpenedAt": a.tsp(acc.CreditOpenedAt)}
		return v, nil, nil
	case "押金":
		if acc.DepositID == nil {
			break
		}
		d, err := readDeposit(ctx, q, *acc.DepositID, false)
		if err != nil || d == nil {
			return map[string]any{"mode": "未开通"}, nil, err
		}
		v, err := a.paidView(ctx, q, d)
		return v, d, err
	}
	return map[string]any{"mode": "未开通"}, nil, nil
}

func (a *App) paidView(ctx context.Context, q querier, d *deposit) (map[string]any, error) {
	topups, err := paymentIDs(ctx, q, topupIDsSQL, d.ID)
	if err != nil {
		return nil, err
	}
	deds, err := paymentIDs(ctx, q, deductionIDsSQL, d.ID)
	if err != nil {
		return nil, err
	}
	r, err := latestRefund(ctx, q, d.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"mode": "押金", "amountPaid": d.AmountPaid, "balance": d.Balance, "status": d.Status, "paidAt": a.ts(d.PaidAt), "paymentId": d.PaymentID,
		"topupPaymentIds": topups, "deductionPaymentIds": deds, "refund": a.refundView(r),
	}, nil
}

// previousView 改开免押之前的旧押金。
func (a *App) previousView(ctx context.Context, q querier, acc account) (map[string]any, *refundReq, error) {
	if acc.Mode != "免押" || acc.PrevDepositID == nil {
		return nil, nil, nil
	}
	d, err := readDeposit(ctx, q, *acc.PrevDepositID, false)
	if err != nil || d == nil {
		return nil, nil, err
	}
	r, err := latestRefund(ctx, q, d.ID)
	if err != nil {
		return nil, nil, err
	}
	return map[string]any{"amountPaid": d.AmountPaid, "status": d.Status, "refund": a.refundView(r)}, r, nil
}

func refundable(d *deposit) bool {
	return d != nil && (d.Status == "已缴" || d.Status == "押金不足") && d.Balance > 0
}

// statusPayload 押金状态变更事件载荷。
func (a *App) statusPayload(acc account, d *deposit) map[string]any {
	p := map[string]any{"userId": acc.UserID, "mode": acc.Mode, "status": nil, "amountPaid": nil, "balance": nil, "creditProvider": nil, "rideable": false, "changedAt": a.ts(a.now())}
	switch acc.Mode {
	case "免押":
		p["creditProvider"] = acc.CreditProvider
		p["rideable"] = true
	case "押金":
		if d != nil {
			p["status"], p["amountPaid"], p["balance"] = d.Status, d.AmountPaid, d.Balance
			p["rideable"] = d.Status == "已缴"
		}
	}
	return p
}

// emitStatus 读当前账户与押金，发押金状态变更。
func (a *App) emitStatus(ctx context.Context, tx pgx.Tx, uid string, actor Actor) error {
	acc, err := readAccount(ctx, tx, uid)
	if err != nil {
		return err
	}
	var d *deposit
	if acc.Mode == "押金" && acc.DepositID != nil {
		if d, err = readDeposit(ctx, tx, *acc.DepositID, false); err != nil {
			return err
		}
	}
	return a.emit(ctx, tx, "deposit.status.changed", uid, actor, a.statusPayload(acc, d))
}

// isCurrentDeposit 该押金是否为用户当前押金（而非改开免押前的旧押金）。
func isCurrentDeposit(ctx context.Context, q querier, uid, depositID string) (bool, error) {
	acc, err := readAccount(ctx, q, uid)
	if err != nil {
		return false, err
	}
	return acc.Mode == "押金" && acc.DepositID != nil && *acc.DepositID == depositID, nil
}
