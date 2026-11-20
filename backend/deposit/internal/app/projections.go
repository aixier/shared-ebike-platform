package app

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// 投影：骑行中订单、待支付与欠费、实名与冻结、规则版本与平台参数。只存本模块判断要用的字段。

func onRideCreated(_ *App, ctx context.Context, tx pgx.Tx, env Envelope) (func(context.Context), error) {
	var p struct {
		OrderID   string    `json:"orderId"`
		UserID    string    `json:"userId"`
		RequestAt time.Time `json:"requestAt"`
	}
	if err := decode(env, &p); err != nil {
		return nil, err
	}
	_, err := tx.Exec(ctx, `INSERT INTO proj_riding (order_id, user_id, created_at) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, p.OrderID, p.UserID, p.RequestAt)
	return nil, err
}

func onRideFinished(_ *App, ctx context.Context, tx pgx.Tx, env Envelope) (func(context.Context), error) {
	var p struct {
		OrderID string `json:"orderId"`
	}
	if err := decode(env, &p); err != nil {
		return nil, err
	}
	_, err := tx.Exec(ctx, `DELETE FROM proj_riding WHERE order_id = $1`, p.OrderID)
	return nil, err
}

func onBillIssued(_ *App, ctx context.Context, tx pgx.Tx, env Envelope) (func(context.Context), error) {
	var p struct {
		OrderID   string `json:"orderId"`
		UserID    string `json:"userId"`
		Payable   int64  `json:"payable"`
		CollectBy string `json:"collectBy"`
	}
	if err := decode(env, &p); err != nil {
		return nil, err
	}
	state, unpaid := "其他", int64(0)
	if p.CollectBy == "一键支付" && p.Payable > 0 {
		state, unpaid = "待支付", p.Payable
	}
	// 已付清或已转欠费的不回退
	_, err := tx.Exec(ctx, `INSERT INTO proj_bills (order_id, user_id, unpaid, state) VALUES ($1,$2,$3,$4)
		ON CONFLICT (order_id) DO UPDATE SET unpaid = EXCLUDED.unpaid, state = EXCLUDED.state
		WHERE proj_bills.state NOT IN ('已付清','欠费')`, p.OrderID, p.UserID, unpaid, state)
	return nil, err
}

func onBillPaid(_ *App, ctx context.Context, tx pgx.Tx, env Envelope) (func(context.Context), error) {
	var p struct {
		OrderID string `json:"orderId"`
		UserID  string `json:"userId"`
	}
	if err := decode(env, &p); err != nil {
		return nil, err
	}
	_, err := tx.Exec(ctx, `INSERT INTO proj_bills (order_id, user_id, unpaid, state) VALUES ($1,$2,0,'已付清')
		ON CONFLICT (order_id) DO UPDATE SET unpaid = 0, state = '已付清'`, p.OrderID, p.UserID)
	return nil, err
}

func onArrearsCreated(_ *App, ctx context.Context, tx pgx.Tx, env Envelope) (func(context.Context), error) {
	var p struct {
		OrderID string `json:"orderId"`
		UserID  string `json:"userId"`
		Amount  int64  `json:"amount"`
	}
	if err := decode(env, &p); err != nil {
		return nil, err
	}
	_, err := tx.Exec(ctx, `INSERT INTO proj_bills (order_id, user_id, unpaid, state) VALUES ($1,$2,$3,'欠费')
		ON CONFLICT (order_id) DO UPDATE SET unpaid = EXCLUDED.unpaid, state = '欠费'`, p.OrderID, p.UserID, p.Amount)
	return nil, err
}

func onArrearsCleared(_ *App, ctx context.Context, tx pgx.Tx, env Envelope) (func(context.Context), error) {
	var p struct {
		UserID string `json:"userId"`
	}
	if err := decode(env, &p); err != nil {
		return nil, err
	}
	_, err := tx.Exec(ctx, `UPDATE proj_bills SET unpaid = 0, state = '已付清' WHERE user_id = $1 AND state = '欠费'`, p.UserID)
	return nil, err
}

func upsertUser(ctx context.Context, tx pgx.Tx, uid, set string, arg any) error {
	_, err := tx.Exec(ctx, `INSERT INTO proj_users (user_id) VALUES ($1) ON CONFLICT DO NOTHING`, uid)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE proj_users SET `+set+` = $2 WHERE user_id = $1`, uid, arg)
	return err
}

// onUserRegistered 用户已注册：建押金投影（用户行与"未开通"账户，已有则不动）并记注册渠道。
func onUserRegistered(a *App, ctx context.Context, tx pgx.Tx, env Envelope) (func(context.Context), error) {
	var p struct {
		UserID       string    `json:"userId"`
		Channel      *string   `json:"channel"`
		RegisteredAt time.Time `json:"registeredAt"`
	}
	if err := decode(env, &p); err != nil {
		return nil, err
	}
	if p.UserID == "" {
		return nil, nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO proj_users (user_id, channel) VALUES ($1,$2)
		ON CONFLICT (user_id) DO UPDATE SET channel = COALESCE(EXCLUDED.channel, proj_users.channel)`, p.UserID, p.Channel); err != nil {
		return nil, err
	}
	_, err := tx.Exec(ctx, `INSERT INTO accounts (user_id, updated_at) VALUES ($1,$2) ON CONFLICT DO NOTHING`, p.UserID, a.now())
	return nil, err
}

func onRealname(_ *App, ctx context.Context, tx pgx.Tx, env Envelope) (func(context.Context), error) {
	var p struct {
		UserID string `json:"userId"`
		Status string `json:"status"`
	}
	if err := decode(env, &p); err != nil {
		return nil, err
	}
	return nil, upsertUser(ctx, tx, p.UserID, "realname_status", p.Status)
}

func userIDOf(env Envelope) (string, error) {
	var p struct {
		UserID string `json:"userId"`
	}
	err := decode(env, &p)
	return p.UserID, err
}

func onFrozen(_ *App, ctx context.Context, tx pgx.Tx, env Envelope) (func(context.Context), error) {
	uid, err := userIDOf(env)
	if err != nil {
		return nil, err
	}
	return nil, upsertUser(ctx, tx, uid, "frozen", true)
}

func onUnfrozen(_ *App, ctx context.Context, tx pgx.Tx, env Envelope) (func(context.Context), error) {
	uid, err := userIDOf(env)
	if err != nil {
		return nil, err
	}
	return nil, upsertUser(ctx, tx, uid, "frozen", false)
}

// onCancelled 账户已注销：之后不能开通免押、缴押金；已开通免押的授权随之关闭（留痕"免押关闭"）。
// 押金用户注销前须先退押（acct 待处理项），这里不动押金。
func onCancelled(a *App, ctx context.Context, tx pgx.Tx, env Envelope) (func(context.Context), error) {
	uid, err := userIDOf(env)
	if err != nil {
		return nil, err
	}
	if err := upsertUser(ctx, tx, uid, "cancelled", true); err != nil {
		return nil, err
	}
	acc, err := lockAccount(ctx, tx, uid, a.now())
	if err != nil || acc.Mode != "免押" {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE accounts SET mode='未开通', credit_provider=NULL, credit_score=NULL, credit_opened_at=NULL, updated_at=$2 WHERE user_id=$1`, uid, a.now()); err != nil {
		return nil, err
	}
	if err := a.emitStatus(ctx, tx, uid, systemActor); err != nil {
		return nil, err
	}
	why := "账户已注销"
	return nil, a.audit(ctx, tx, systemActor, "关闭免押", "user", uid, "账户已注销，信用免押授权随之关闭", &why,
		map[string]any{"mode": "免押", "creditProvider": acc.CreditProvider}, map[string]any{"mode": "未开通"})
}

func onRulePublished(_ *App, ctx context.Context, tx pgx.Tx, env Envelope) (func(context.Context), error) {
	var p struct {
		VersionID   string    `json:"versionId"`
		EffectiveAt time.Time `json:"effectiveAt"`
		Deposit     struct {
			Amount int64 `json:"amount"`
		} `json:"deposit"`
		CreditFree struct {
			Threshold int             `json:"threshold"`
			Providers json.RawMessage `json:"providers"`
		} `json:"creditFree"`
	}
	if err := decode(env, &p); err != nil {
		return nil, err
	}
	providers := []byte(p.CreditFree.Providers)
	if len(providers) == 0 {
		providers = []byte("null")
	}
	// 版本一经发布不可修改，重复到达不覆盖
	_, err := tx.Exec(ctx, `INSERT INTO proj_rule_versions (version_id, effective_at, deposit_amount, credit_threshold, providers)
		VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, p.VersionID, p.EffectiveAt, p.Deposit.Amount, p.CreditFree.Threshold, providers)
	return nil, err
}

func onRuleRevoked(_ *App, ctx context.Context, tx pgx.Tx, env Envelope) (func(context.Context), error) {
	var p struct {
		VersionID string `json:"versionId"`
	}
	if err := decode(env, &p); err != nil {
		return nil, err
	}
	_, err := tx.Exec(ctx, `UPDATE proj_rule_versions SET revoked = true WHERE version_id = $1`, p.VersionID)
	return nil, err
}

func onParamsChanged(_ *App, ctx context.Context, tx pgx.Tx, env Envelope) (func(context.Context), error) {
	var p struct {
		DepositRefundWorkdays *int      `json:"depositRefundWorkdays"`
		ChangedAt             time.Time `json:"changedAt"`
	}
	if err := decode(env, &p); err != nil {
		return nil, err
	}
	if p.DepositRefundWorkdays == nil {
		return nil, nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO proj_params (id, refund_workdays, changed_at) VALUES (1,$1,$2)
		ON CONFLICT (id) DO UPDATE SET refund_workdays = EXCLUDED.refund_workdays, changed_at = EXCLUDED.changed_at
		WHERE proj_params.changed_at IS NULL OR proj_params.changed_at <= EXCLUDED.changed_at`, *p.DepositRefundWorkdays, p.ChangedAt)
	return nil, err
}

// rule 现行规则中与押金有关的部分。
type rule struct {
	VersionID string
	Amount    int64
	Threshold int
	Providers []string
}

// querier 事务或连接池。
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// currentRule 按服务器时间取现行版本；没有返回 nil。
func (a *App) currentRule(ctx context.Context, q querier) (*rule, error) {
	r := &rule{}
	var prov []byte
	err := q.QueryRow(ctx, `SELECT version_id, deposit_amount, credit_threshold, providers FROM proj_rule_versions
		WHERE NOT revoked AND effective_at <= $1 ORDER BY effective_at DESC LIMIT 1`, a.now()).Scan(&r.VersionID, &r.Amount, &r.Threshold, &prov)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(prov, &r.Providers)
	return r, nil
}

// refundWorkdays 退押时限：平台参数，未收到时用配置的缺省值。
func (a *App) refundWorkdays(ctx context.Context, q querier) int {
	var n int
	if err := q.QueryRow(ctx, `SELECT refund_workdays FROM proj_params WHERE id = 1`).Scan(&n); err != nil {
		return a.RefundWorkdaysDefault
	}
	return n
}

// userFlags 实名与账户状态。
type userFlags struct {
	known     bool
	channel   string // 注册渠道：微信｜支付宝，未收到注册事件为空
	realname  string
	frozen    bool
	cancelled bool
}

func loadFlags(ctx context.Context, q querier, uid string) (userFlags, error) {
	f := userFlags{}
	var rn, ch *string
	err := q.QueryRow(ctx, `SELECT realname_status, frozen, cancelled, channel FROM proj_users WHERE user_id = $1`, uid).Scan(&rn, &f.frozen, &f.cancelled, &ch)
	if err == pgx.ErrNoRows {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	f.known = true
	if rn != nil {
		f.realname = *rn
	}
	if ch != nil {
		f.channel = *ch
	}
	return f, nil
}
