package app

// 模块间命令：免押扣款、一键支付、退款、押金收款、退押、专户划转、确认收款结果。
// 三态结果：成功、失败（带原因）、结果未知；结果未知的由定时查询补齐，仍未知的由运维经 ops 确认。
// trace: R-09 R-18 BR-06 R-11 R-15 R-17 BR-22 R-34 BR-32

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"bike/pay/internal/channel"

	"github.com/jackc/pgx/v5"
)

// CommandResult 契约 PayCommandResult
type CommandResult struct {
	Result      string     `json:"result"`
	FailCode    *string    `json:"failCode"`
	FailReason  *string    `json:"failReason"`
	PaymentID   string     `json:"paymentId"`
	Type        string     `json:"type,omitempty"`
	Account     string     `json:"account,omitempty"`
	Amount      int64      `json:"amount,omitempty"`
	Status      string     `json:"status,omitempty"`
	CompletedAt *string    `json:"completedAt"`
	PayParams   *PayParams `json:"payParams,omitempty"`
}

func resultOf(p *Payment) CommandResult {
	r := CommandResult{PaymentID: p.PaymentID, Type: p.Type, Account: p.Account, Amount: p.Amount, Status: p.Status,
		CompletedAt: p.CompletedAt, FailCode: p.FailCode, FailReason: p.FailReason}
	switch p.Status {
	case "成功":
		r.Result = "成功"
		r.FailCode, r.FailReason = nil, nil
	case "失败":
		r.Result = "失败"
	default:
		r.Result = "结果未知"
		r.FailCode, r.FailReason = nil, nil
	}
	return r
}

func failResult(p *Payment, code, reason string) CommandResult {
	r := CommandResult{Result: "失败", FailCode: &code, FailReason: &reason}
	if p != nil {
		r.PaymentID, r.Type, r.Account, r.Amount, r.Status, r.CompletedAt = p.PaymentID, p.Type, p.Account, p.Amount, p.Status, p.CompletedAt
	}
	return r
}

// ---------- 幂等 ----------

type idemHit struct {
	paymentID *string
	status    int
	body      []byte
}

func (a *App) idemGet(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, scope, key string) (*idemHit, error) {
	h := &idemHit{}
	var st *int
	err := q.QueryRow(ctx, `SELECT payment_id, status_code, body FROM idempotency_keys WHERE scope=$1 AND key=$2`, scope, key).Scan(&h.paymentID, &st, &h.body)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if st != nil {
		h.status = *st
	}
	return h, nil
}

func (a *App) idemPut(ctx context.Context, tx pgx.Tx, scope, key string, paymentID *string, status int, body any) error {
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	_, err := tx.Exec(ctx, `INSERT INTO idempotency_keys (scope, key, payment_id, status_code, body, created_at) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT DO NOTHING`, scope, key, paymentID, status, b, a.Now())
	return err
}

// replay：同一幂等键再到达。带流水的返回该流水的最新结果（契约：可取到最新结果），否则返回首次结果。
func (a *App) replay(ctx context.Context, h *idemHit) (CommandResult, error) {
	if h.paymentID != nil {
		p, err := a.getPayment(ctx, a.Pool, *h.paymentID)
		if err != nil {
			return CommandResult{}, err
		}
		if p != nil {
			return a.resultWithParams(ctx, p), nil
		}
	}
	var r CommandResult
	err := json.Unmarshal(h.body, &r)
	return r, err
}

// ---------- 收款（扣款、一键支付、押金收款） ----------

type collectSpec struct {
	np       newPayment
	merchant channel.Merchant
	intent   bool   // 一键支付两步：只预下单并返回调起参数，结果由回调或查询给出
	openID   string // 付款人 openid（微信支付预下单用）
}

func merchantOf(account string) channel.Merchant {
	if account == "押金专户" {
		return channel.DepositMerchant
	}
	return channel.IncomeMerchant
}

// runCollect：同一业务单号只成功一次；处理中的不重复发起。
func (a *App) runCollect(ctx context.Context, scope, key string, s collectSpec) (CommandResult, error) {
	if h, err := a.idemGet(ctx, a.Pool, scope, key); err != nil || h != nil {
		if err != nil {
			return CommandResult{}, err
		}
		return a.replay(ctx, h)
	}
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return CommandResult{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockKey(ctx, tx, "biz:"+s.np.BizNo); err != nil {
		return CommandResult{}, err
	}
	if h, err := a.idemGet(ctx, tx, scope, key); err != nil || h != nil {
		if err != nil {
			return CommandResult{}, err
		}
		tx.Rollback(ctx)
		return a.replay(ctx, h)
	}
	prev, err := a.queryPayments(ctx, tx, `WHERE c.biz_no = $1 AND c.type = $2 AND c.status IN ('成功','处理中') ORDER BY c.created_at`, s.np.BizNo, s.np.Type)
	if err != nil {
		return CommandResult{}, err
	}
	for _, p := range prev {
		if p.Status == "成功" {
			r := failResult(p, "ALREADY_PAID", "该业务单号已收款成功")
			if err := a.idemPut(ctx, tx, scope, key, nil, 200, r); err != nil {
				return CommandResult{}, err
			}
			return r, tx.Commit(ctx)
		}
	}
	if len(prev) > 0 { // 处理中：不重复发起，返回结果未知
		p := prev[0]
		if err := a.idemPut(ctx, tx, scope, key, &p.PaymentID, 200, nil); err != nil {
			return CommandResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return CommandResult{}, err
		}
		return a.resultWithParams(ctx, p), nil
	}
	id, err := a.insertPayment(ctx, tx, s.np, "处理中", "记账", nil, nil, nil, nil)
	if err != nil {
		return CommandResult{}, err
	}
	if err := a.idemPut(ctx, tx, scope, key, &id, 200, nil); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CommandResult{}, err
	}

	if s.intent {
		return a.startIntent(ctx, id, s)
	}

	// 事务外调渠道
	prov, err := a.Ch.Get(s.np.Channel)
	var res channel.Result
	if err == nil {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if s.np.Credit {
			res, err = prov.CreditCharge(cctx, channel.CreditChargeReq{OutTradeNo: id, UserID: deref(s.np.UserID), OrderID: deref(s.np.OrderID), Amount: s.np.Amount})
		} else {
			res, err = prov.Charge(cctx, channel.ChargeReq{Merchant: s.merchant, OutTradeNo: id, UserID: deref(s.np.UserID), Amount: s.np.Amount, Description: s.np.Type})
		}
		cancel()
	}
	if err != nil {
		a.Log.Warn("渠道调用未返回明确结果", "paymentId", id, "err", err)
		res = channel.Result{Outcome: channel.Unknown}
	}
	if err := a.applyOutcome(ctx, id, res, "渠道", systemActor, true); err != nil {
		return CommandResult{}, err
	}
	p, err := a.getPayment(ctx, a.Pool, id)
	if err != nil {
		return CommandResult{}, err
	}
	return resultOf(p), nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// applyOutcome 把渠道结果记成新状态并发事件。emitUnknown 为真时，结果未知也发"收款结果未知"。
// 只有处理中的流水会被改写（防止查询与人工确认交错时重复记结果）。
func (a *App) applyOutcome(ctx context.Context, id string, res channel.Result, source string, actor Actor, emitUnknown bool) error {
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockKey(ctx, tx, "pay:"+id); err != nil {
		return err
	}
	p, err := a.getPayment(ctx, tx, id)
	if err != nil || p == nil {
		return err
	}
	if p.Status != "处理中" {
		return nil
	}
	switch res.Outcome {
	case channel.Succeeded:
		at := res.CompletedAt
		if at.IsZero() {
			at = a.Now()
		}
		if err := a.addResult(ctx, tx, id, "成功", nil, nil, strPtr(res.TradeNo), &at, source, nil, nil); err != nil {
			return err
		}
	case channel.Failed:
		code := "DECLINED"
		if !isCollect(p.Type) {
			code = "CHANNEL_FAILED"
		}
		reason := res.FailReason
		if reason == "" {
			reason = "渠道拒绝"
		}
		if err := a.addResult(ctx, tx, id, "失败", &code, &reason, strPtr(res.TradeNo), nil, source, nil, nil); err != nil {
			return err
		}
	default:
		if !emitUnknown {
			return nil
		}
		if res.TradeNo != "" {
			if err := a.addResult(ctx, tx, id, "处理中", nil, nil, strPtr(res.TradeNo), nil, source, nil, nil); err != nil {
				return err
			}
		}
	}
	np, err := a.getPayment(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := a.emitResult(ctx, tx, np, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---------- 退款（按订单原路退回） ----------

// RefundCmd 契约 RefundCommand
type RefundCmd struct {
	UserID   string  `json:"userId"`
	OrderID  string  `json:"orderId"`
	Amount   int64   `json:"amount"`
	TicketID *string `json:"ticketId"`
	Actor    *Actor  `json:"actor"`
	Reason   *string `json:"reason"`
}

var (
	errNoCollection    = errors.New("该订单没有可退的收款")
	errAlreadyRefunded = errors.New("该订单已全额退款")
)

func (a *App) runRefund(ctx context.Context, scope, key string, c RefundCmd) (CommandResult, error) {
	if h, err := a.idemGet(ctx, a.Pool, scope, key); err != nil || h != nil {
		if err != nil {
			return CommandResult{}, err
		}
		return a.replay(ctx, h)
	}
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return CommandResult{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockKey(ctx, tx, "order:"+c.OrderID); err != nil {
		return CommandResult{}, err
	}
	if h, err := a.idemGet(ctx, tx, scope, key); err != nil || h != nil {
		if err != nil {
			return CommandResult{}, err
		}
		tx.Rollback(ctx)
		return a.replay(ctx, h)
	}
	// 渠道已收：本订单在微信、支付宝成功的收款；已退：本订单全部原路退款（处理中、失败待人工转账的都占额度）
	ps, err := a.queryPayments(ctx, tx, `WHERE c.order_id = $1 AND c.user_id = $2 ORDER BY c.created_at DESC, c.seq DESC`, c.OrderID, c.UserID)
	if err != nil {
		return CommandResult{}, err
	}
	var received, refunded int64
	var cols []*Payment
	refundedOf := map[string]int64{}
	for _, p := range ps {
		switch {
		case p.Account == "骑行收入户" && (p.Type == "订单扣款" || p.Type == "订单支付" || p.Type == "欠费补付") && p.Status == "成功" &&
			(p.Channel == channel.WeChat || p.Channel == channel.Alipay):
			received += p.Amount
			cols = append(cols, p)
		case p.Type == "退款":
			refunded += p.Amount
			if p.RelatedPaymentID != nil {
				refundedOf[*p.RelatedPaymentID] += p.Amount
			}
		}
	}
	if len(cols) == 0 {
		return CommandResult{}, errNoCollection
	}
	if refunded >= received { // 已被全额退过：再退是冲突，不是金额超限
		return CommandResult{}, errAlreadyRefunded
	}
	if c.Amount > received-refunded {
		r := failResult(nil, "EXCEED_RECEIVED", "累计退款超过该订单渠道已收")
		if err := a.idemPut(ctx, tx, scope, key, nil, 200, r); err != nil {
			return CommandResult{}, err
		}
		return r, tx.Commit(ctx)
	}
	// 原路：选可退余额足够的最近一笔收款（渠道退款按单笔原交易）
	var orig *Payment
	for _, p := range cols {
		if p.Amount-refundedOf[p.PaymentID] >= c.Amount {
			orig = p
			break
		}
	}
	if orig == nil {
		r := failResult(nil, "EXCEED_RECEIVED", "单笔原支付可退余额不足，请分笔退款")
		if err := a.idemPut(ctx, tx, scope, key, nil, 200, r); err != nil {
			return CommandResult{}, err
		}
		return r, tx.Commit(ctx)
	}
	var opID *string
	actor := systemActor
	if c.Actor != nil {
		actor = *c.Actor
		if c.Actor.Kind == "人" || c.Actor.Kind == "智能体" {
			opID = &c.Actor.ID
		}
	}
	np := newPayment{BizNo: c.OrderID, Type: "退款", Account: "骑行收入户", Direction: "付", Channel: orig.Channel,
		UserID: &c.UserID, OrderID: &c.OrderID, Amount: c.Amount, RelatedPaymentID: &orig.PaymentID, TicketID: c.TicketID,
		OperatorID: opID, Note: c.Reason}
	id, err := a.insertPayment(ctx, tx, np, "处理中", "记账", nil, nil, c.Actor, c.Reason)
	if err != nil {
		return CommandResult{}, err
	}
	if err := a.audit(ctx, tx, actor, "执行退款", "payment", id,
		fmt.Sprintf("订单 %s 原路退回 ¥%s（原支付 %s）", c.OrderID, yuan(c.Amount), orig.PaymentID), c.Reason, nil,
		map[string]any{"orderId": c.OrderID, "userId": c.UserID, "amount": c.Amount, "originalPaymentId": orig.PaymentID}); err != nil {
		return CommandResult{}, err
	}
	if err := a.idemPut(ctx, tx, scope, key, &id, 200, nil); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CommandResult{}, err
	}
	res := a.callRefund(ctx, orig.Channel, channel.RefundReq{Merchant: channel.IncomeMerchant, OutRefundNo: id, OrigOutTradeNo: orig.PaymentID,
		OrigTradeNo: deref(orig.ChannelTradeNo), OrigAmount: orig.Amount, Amount: c.Amount, UserID: c.UserID, Reason: deref(c.Reason)})
	if err := a.applyOutcome(ctx, id, res, "渠道", actor, false); err != nil {
		return CommandResult{}, err
	}
	p, err := a.getPayment(ctx, a.Pool, id)
	if err != nil {
		return CommandResult{}, err
	}
	return resultOf(p), nil
}

func (a *App) callRefund(ctx context.Context, ch string, req channel.RefundReq) channel.Result {
	prov, err := a.Ch.Get(ch)
	var res channel.Result
	if err == nil {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		res, err = prov.Refund(cctx, req)
		cancel()
	}
	if err != nil {
		a.Log.Warn("退款渠道调用未返回明确结果", "paymentId", req.OutRefundNo, "err", err)
		return channel.Result{Outcome: channel.Unknown}
	}
	return res
}

// ---------- 退押 ----------

// DepositRefundCmd 契约 DepositRefundCommand
type DepositRefundCmd struct {
	BizNo              string   `json:"bizNo"`
	UserID             string   `json:"userId"`
	Amount             int64    `json:"amount"`
	OriginalPaymentIDs []string `json:"originalPaymentIds"`
	Actor              *Actor   `json:"actor"`
}

var errOriginalNotFound = errors.New("原押金收款流水不存在")

func (a *App) runDepositRefund(ctx context.Context, scope, key string, c DepositRefundCmd) (CommandResult, error) {
	if h, err := a.idemGet(ctx, a.Pool, scope, key); err != nil || h != nil {
		if err != nil {
			return CommandResult{}, err
		}
		return a.replay(ctx, h)
	}
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return CommandResult{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockKey(ctx, tx, "biz:"+c.BizNo); err != nil {
		return CommandResult{}, err
	}
	if h, err := a.idemGet(ctx, tx, scope, key); err != nil || h != nil {
		if err != nil {
			return CommandResult{}, err
		}
		tx.Rollback(ctx)
		return a.replay(ctx, h)
	}
	// 同一退押申请只退一次：已有的直接返回其结果
	prev, err := a.queryPayments(ctx, tx, `WHERE c.biz_no = $1 AND c.type = '退押' ORDER BY c.created_at LIMIT 1`, c.BizNo)
	if err != nil {
		return CommandResult{}, err
	}
	if len(prev) > 0 {
		if err := a.idemPut(ctx, tx, scope, key, &prev[0].PaymentID, 200, nil); err != nil {
			return CommandResult{}, err
		}
		return resultOf(prev[0]), tx.Commit(ctx)
	}
	var origs []*Payment
	var total int64
	for _, oid := range c.OriginalPaymentIDs {
		p, err := a.getPayment(ctx, tx, oid)
		if err != nil {
			return CommandResult{}, err
		}
		if p == nil || deref(p.UserID) != c.UserID || (p.Type != "押金缴纳" && p.Type != "押金补缴") || p.Status != "成功" {
			return CommandResult{}, errOriginalNotFound
		}
		origs = append(origs, p)
		total += p.Amount
	}
	if c.Amount > total {
		r := failResult(nil, "EXCEED_RECEIVED", "退押金额超过原押金收款")
		if err := a.idemPut(ctx, tx, scope, key, nil, 200, r); err != nil {
			return CommandResult{}, err
		}
		return r, tx.Commit(ctx)
	}
	orig := origs[0]
	for _, p := range origs {
		if p.Amount >= c.Amount {
			orig = p
			break
		}
	}
	var opID *string
	actor := systemActor
	if c.Actor != nil {
		actor = *c.Actor
		if c.Actor.Kind == "人" || c.Actor.Kind == "智能体" {
			opID = &c.Actor.ID
		}
	}
	np := newPayment{BizNo: c.BizNo, Type: "退押", Account: "押金专户", Direction: "付", Channel: orig.Channel,
		UserID: &c.UserID, Amount: c.Amount, RelatedPaymentID: &orig.PaymentID, OperatorID: opID}
	id, err := a.insertPayment(ctx, tx, np, "处理中", "记账", nil, nil, c.Actor, nil)
	if err != nil {
		return CommandResult{}, err
	}
	if err := a.idemPut(ctx, tx, scope, key, &id, 200, nil); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CommandResult{}, err
	}
	res := a.callRefund(ctx, orig.Channel, channel.RefundReq{Merchant: channel.DepositMerchant, OutRefundNo: id, OrigOutTradeNo: orig.PaymentID,
		OrigTradeNo: deref(orig.ChannelTradeNo), OrigAmount: orig.Amount, Amount: c.Amount, UserID: c.UserID, Reason: "退押"})
	if err := a.applyOutcome(ctx, id, res, "渠道", actor, false); err != nil {
		return CommandResult{}, err
	}
	p, err := a.getPayment(ctx, a.Pool, id)
	if err != nil {
		return CommandResult{}, err
	}
	return resultOf(p), nil
}

// ---------- 专户划转（押金抵扣） ----------

// DepositTransferCmd 契约 DepositTransferCommand
type DepositTransferCmd struct {
	BizNo   string `json:"bizNo"`
	UserID  string `json:"userId"`
	OrderID string `json:"orderId"`
	Amount  int64  `json:"amount"`
}

func (a *App) runDepositTransfer(ctx context.Context, scope, key string, c DepositTransferCmd) (CommandResult, error) {
	if h, err := a.idemGet(ctx, a.Pool, scope, key); err != nil || h != nil {
		if err != nil {
			return CommandResult{}, err
		}
		return a.replay(ctx, h)
	}
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return CommandResult{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockKey(ctx, tx, "biz:"+c.BizNo); err != nil {
		return CommandResult{}, err
	}
	if h, err := a.idemGet(ctx, tx, scope, key); err != nil || h != nil {
		if err != nil {
			return CommandResult{}, err
		}
		tx.Rollback(ctx)
		return a.replay(ctx, h)
	}
	prev, err := a.queryPayments(ctx, tx, `WHERE c.biz_no = $1 AND c.type = '押金抵扣' AND c.status = '成功' LIMIT 1`, c.BizNo)
	if err != nil {
		return CommandResult{}, err
	}
	if len(prev) > 0 {
		r := failResult(prev[0], "ALREADY_PAID", "该抵扣记录已划转")
		if err := a.idemPut(ctx, tx, scope, key, nil, 200, r); err != nil {
			return CommandResult{}, err
		}
		return r, tx.Commit(ctx)
	}
	now := a.Now()
	trade := "ZH" + now.In(bj).Format("20060102150405") + randDigits(6)
	note := "一键支付期限届满未付，按 BR-22 从押金专户划转抵扣"
	np := newPayment{BizNo: c.BizNo, Type: "押金抵扣", Account: "押金专户", Direction: "转", Channel: "押金专户划转",
		UserID: &c.UserID, OrderID: &c.OrderID, Amount: c.Amount, Note: &note}
	id, err := a.insertPayment(ctx, tx, np, "成功", "记账", &trade, &now, nil, nil)
	if err != nil {
		return CommandResult{}, err
	}
	p, err := a.getPayment(ctx, tx, id)
	if err != nil {
		return CommandResult{}, err
	}
	if err := a.emitResult(ctx, tx, p, systemActor); err != nil {
		return CommandResult{}, err
	}
	if err := a.idemPut(ctx, tx, scope, key, &id, 200, nil); err != nil {
		return CommandResult{}, err
	}
	return resultOf(p), tx.Commit(ctx)
}

// ---------- 确认收款结果（ops） ----------

// ConfirmCmd 契约 ConfirmResultCommand
type ConfirmCmd struct {
	Result string `json:"result"`
	Reason string `json:"reason"`
	Actor  *Actor `json:"actor"`
}

var errPaymentNotFound = errors.New("流水不存在")

func (a *App) runConfirm(ctx context.Context, scope, key, id string, c ConfirmCmd) (CommandResult, error) {
	if h, err := a.idemGet(ctx, a.Pool, scope, key); err != nil || h != nil {
		if err != nil {
			return CommandResult{}, err
		}
		var r CommandResult
		return r, json.Unmarshal(h.body, &r)
	}
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return CommandResult{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockKey(ctx, tx, "pay:"+id); err != nil {
		return CommandResult{}, err
	}
	p, err := a.getPayment(ctx, tx, id)
	if err != nil {
		return CommandResult{}, err
	}
	if p == nil {
		return CommandResult{}, errPaymentNotFound
	}
	var r CommandResult
	if p.Status != "处理中" || !isCollect(p.Type) {
		r = failResult(p, "NOT_PENDING", "该流水不在结果未知状态")
	} else {
		now := a.Now()
		reason := c.Reason
		if c.Result == "已收款" {
			err = a.addResult(ctx, tx, id, "成功", nil, nil, nil, &now, "人工确认", c.Actor, &reason)
		} else {
			code, fr := "DECLINED", "经人工确认未收款"
			err = a.addResult(ctx, tx, id, "失败", &code, &fr, nil, nil, "人工确认", c.Actor, &reason)
		}
		if err != nil {
			return CommandResult{}, err
		}
		np, err := a.getPayment(ctx, tx, id)
		if err != nil {
			return CommandResult{}, err
		}
		if err := a.emitResult(ctx, tx, np, *c.Actor); err != nil {
			return CommandResult{}, err
		}
		r = resultOf(np)
	}
	if err := a.idemPut(ctx, tx, scope, key, nil, 200, r); err != nil {
		return CommandResult{}, err
	}
	return r, tx.Commit(ctx)
}

// ---------- 查询补齐（定时任务） ----------

// ResolvePending 向渠道查询处理中的流水，查到结果的记下并发事件；仍未知的不动（等运维确认）。
func (a *App) ResolvePending(ctx context.Context, olderThan time.Duration) (int, error) {
	ps, err := a.queryPayments(ctx, a.Pool, `WHERE c.status = '处理中' AND c.channel IN ('微信支付','支付宝') AND c.created_at <= $1
		AND NOT EXISTS (SELECT 1 FROM pay_intents i WHERE i.payment_id = c.payment_id AND i.state = 'awaiting' AND i.callback_at IS NOT NULL) ORDER BY c.created_at LIMIT 200`, a.Now().Add(-olderThan))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range ps {
		prov, err := a.Ch.Get(p.Channel)
		if err != nil {
			continue
		}
		kind := channel.QueryCharge
		if !isCollect(p.Type) {
			kind = channel.QueryRefund
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		res, err := prov.Query(cctx, channel.QueryReq{Merchant: merchantOf(p.Account), Kind: kind, OutNo: p.PaymentID, Credit: p.Credit})
		cancel()
		if err != nil || res.Outcome == channel.Unknown {
			continue
		}
		if err := a.applyOutcome(ctx, p.PaymentID, res, "查询", systemActor, false); err != nil {
			return n, err
		}
		n++
	}
	m, err := a.ExpireIntents(ctx)
	return n + m, err
}
