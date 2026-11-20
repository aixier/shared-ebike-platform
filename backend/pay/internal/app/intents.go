package app

// 一键支付两步化：第一步预下单、返回调起参数（流水处理中）；第二步由渠道回调或定时查询给出结果并发事件。
// 模拟渠道下，按 PAY_MOCK_CALLBACK_DELAY 延迟自动模拟渠道回调；调起参数到期仍未支付的记失败。
// trace: R-09 BR-05；pay.yaml startOneTapPayment；events/pay.md

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"bike/pay/internal/channel"

	"github.com/jackc/pgx/v5"
)

// PayParams 契约 PayParams
type PayParams struct {
	PaymentID string         `json:"paymentId"`
	Channel   string         `json:"channel"`
	Mode      string         `json:"mode"`
	ExpiresAt string         `json:"expiresAt"`
	Params    map[string]any `json:"params"`
}

const reasonIntentExpired = "用户未在时限内完成支付"

// intentTTL、mockCallbackDelay 取配置；测试里直接改 App 上的字段
func (a *App) intentTTL() time.Duration {
	if a.IntentTTL > 0 {
		return a.IntentTTL
	}
	return 15 * time.Minute
}

// startIntent 预下单：成功则记调起参数并返回"结果未知 + payParams"，渠道拒绝则记失败。
func (a *App) startIntent(ctx context.Context, id string, s collectSpec) (CommandResult, error) {
	expires := a.Now().Add(a.intentTTL())
	var pr channel.PrepayResult
	prov, err := a.Ch.Get(s.np.Channel)
	if err == nil {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		pr, err = prov.Prepay(cctx, channel.PrepayReq{Merchant: s.merchant, OutTradeNo: id, UserID: deref(s.np.UserID), OpenID: s.openID,
			Amount: s.np.Amount, Description: s.np.Type, ExpiresAt: expires})
		cancel()
	}
	if err != nil { // 预下单没有明确结果：还没有任何钱动，按渠道失败处理，用户可重新发起
		a.Log.Warn("渠道预下单失败", "paymentId", id, "err", err)
		pr = channel.PrepayResult{Outcome: channel.Failed, FailReason: "渠道暂时不可用，请稍后重试"}
	}
	if pr.Outcome == channel.Failed {
		if err := a.applyOutcome(ctx, id, channel.Result{Outcome: channel.Failed, FailReason: pr.FailReason}, "渠道", systemActor, false); err != nil {
			return CommandResult{}, err
		}
		p, err := a.getPayment(ctx, a.Pool, id)
		if err != nil {
			return CommandResult{}, err
		}
		return resultOf(p), nil
	}
	pp := PayParams{PaymentID: id, Channel: s.np.Channel, Mode: pr.Mode, ExpiresAt: fmtTime(expires), Params: pr.Params}
	body, _ := json.Marshal(pp)
	var callbackAt *time.Time
	state := "awaiting"
	if pr.Mode == "mock" {
		if pr.AutoCallback {
			t := a.Now().Add(a.MockCallbackDelay)
			callbackAt = &t
		} else {
			state = "called" // 行为"未知"：不回调，也不按超时关闭，交给定时查询与运维确认
		}
	}
	if _, err := a.Pool.Exec(ctx, `INSERT INTO pay_intents (payment_id, pay_params, mode, expires_at, callback_at, state, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (payment_id) DO NOTHING`, id, body, pr.Mode, expires, callbackAt, state, a.Now()); err != nil {
		return CommandResult{}, err
	}
	p, err := a.getPayment(ctx, a.Pool, id)
	if err != nil {
		return CommandResult{}, err
	}
	return a.resultWithParams(ctx, p), nil
}

// resultWithParams 处理中的一键支付流水，带上调起参数（待用户支付）
func (a *App) resultWithParams(ctx context.Context, p *Payment) CommandResult {
	r := resultOf(p)
	if p.Status != "处理中" {
		return r
	}
	var body []byte
	err := a.Pool.QueryRow(ctx, `SELECT pay_params FROM pay_intents WHERE payment_id = $1 AND state <> 'closed' AND expires_at > $2`, p.PaymentID, a.Now()).Scan(&body)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			a.Log.Warn("读取调起参数失败", "paymentId", p.PaymentID, "err", err)
		}
		return r
	}
	var pp PayParams
	if json.Unmarshal(body, &pp) == nil {
		r.PayParams = &pp
	}
	return r
}

// RunMockCallbacks 模拟渠道回调：到点的待支付流水向渠道查询结果并记下（成功或失败）；返回处理条数。
func (a *App) RunMockCallbacks(ctx context.Context) (int, error) {
	rows, err := a.Pool.Query(ctx, `SELECT payment_id FROM pay_intents WHERE state = 'awaiting' AND callback_at IS NOT NULL AND callback_at <= $1 ORDER BY callback_at LIMIT 200`, a.Now())
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	n := 0
	for _, id := range ids {
		p, err := a.getPayment(ctx, a.Pool, id)
		if err != nil {
			return n, err
		}
		if p != nil && p.Status == "处理中" {
			if prov, err := a.Ch.Get(p.Channel); err == nil {
				cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				res, err := prov.Query(cctx, channel.QueryReq{Merchant: merchantOf(p.Account), Kind: channel.QueryCharge, OutNo: p.PaymentID})
				cancel()
				if err == nil && res.Outcome != channel.Unknown {
					if err := a.applyOutcome(ctx, id, res, "渠道回调", systemActor, false); err != nil {
						return n, err
					}
					n++
				}
			}
		}
		if _, err := a.Pool.Exec(ctx, `UPDATE pay_intents SET state = 'called' WHERE payment_id = $1 AND state = 'awaiting'`, id); err != nil {
			return n, err
		}
	}
	return n, nil
}

// ExpireIntents 调起参数到期仍未支付的：先向渠道查一次，仍未出结果则记失败（用户未在时限内完成支付）并发"收款失败"。
func (a *App) ExpireIntents(ctx context.Context) (int, error) {
	rows, err := a.Pool.Query(ctx, `SELECT payment_id FROM pay_intents WHERE state = 'awaiting' AND expires_at <= $1 ORDER BY expires_at LIMIT 200`, a.Now())
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	n := 0
	for _, id := range ids {
		p, err := a.getPayment(ctx, a.Pool, id)
		if err != nil {
			return n, err
		}
		if p != nil && p.Status == "处理中" {
			res := channel.Result{Outcome: channel.Failed, FailReason: reasonIntentExpired}
			if prov, err := a.Ch.Get(p.Channel); err == nil {
				cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				if q, err := prov.Query(cctx, channel.QueryReq{Merchant: merchantOf(p.Account), Kind: channel.QueryCharge, OutNo: p.PaymentID}); err == nil && q.Outcome != channel.Unknown {
					res = q // 到期前其实已经支付成功（或渠道已判失败）：以渠道为准
				}
				cancel()
			}
			if err := a.applyOutcome(ctx, id, res, "超时关闭", systemActor, false); err != nil {
				return n, err
			}
			n++
		}
		if _, err := a.Pool.Exec(ctx, `UPDATE pay_intents SET state = 'closed' WHERE payment_id = $1 AND state = 'awaiting'`, id); err != nil {
			return n, err
		}
	}
	return n, nil
}
