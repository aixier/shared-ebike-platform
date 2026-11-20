package app

import (
	"context"
	"encoding/json"

	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"bike/deposit/internal/clients/pay"
	"bike/deposit/internal/workday"
)

// checkEligible 未实名、冻结、已注销的不能开通免押、缴押金（R-15 R-16）。
func checkEligible(f userFlags, verb string) error {
	switch {
	case f.cancelled:
		return newErr(409, "ACCOUNT_CANCELLED", "该账户已注销").with("next", next("重新授权", "login"))
	case f.frozen:
		return newErr(409, "ACCOUNT_FROZEN", "账户已冻结，解冻后才能"+verb).with("next", next("查看解冻方法", "unfreezeGuide"))
	case f.realname != "已核验":
		return newErr(409, "NEED_ONBOARDING", "完成实名核验后即可"+verb).with("next", next("去完成", "onboarding"))
	}
	return nil
}

// ======================= 信用免押（R-16） =======================

var creditProviders = []string{"微信支付分", "芝麻信用"}

func (a *App) openCreditFree(ctx context.Context, uid, provider string) (map[string]any, error) {
	f, err := loadFlags(ctx, a.DB, uid)
	if err != nil {
		return nil, err
	}
	if provider == "" {
		provider = "微信支付分" // 缺省按注册渠道：微信为微信支付分，支付宝为芝麻信用；渠道未知时微信支付分
		if f.channel == "支付宝" {
			provider = "芝麻信用"
		}
	}
	if !slices.Contains(creditProviders, provider) {
		return nil, errInvalid("信用服务只能是微信支付分或芝麻信用")
	}
	if err := checkEligible(f, "开通免押"); err != nil {
		return nil, err
	}
	acc, err := readAccount(ctx, a.DB, uid)
	if err != nil {
		return nil, err
	}
	if err := a.creditPrecheck(ctx, a.DB, acc); err != nil {
		return nil, err
	}
	r, err := a.currentRule(ctx, a.DB)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errBusy("押金规则尚未同步，请稍后再试")
	}
	if len(r.Providers) > 0 && !slices.Contains(r.Providers, provider) {
		return nil, errInvalid(provider + "暂不支持免押")
	}
	score, err := a.Credit.Score(ctx, uid, provider)
	if err != nil {
		a.Log.Warn("信用分查询失败", "user", uid, "err", err)
		return nil, errBusy("信用分暂时查不到，请稍后再试或缴押金骑行")
	}
	if score < r.Threshold {
		return nil, newErr(409, "CREDIT_SCORE_LOW", fmt.Sprintf("%s暂未达到免押条件（需 %d 分），缴押金 ¥%s 也可以骑行", provider, r.Threshold, yuan(r.Amount))).
			with("score", score).with("threshold", r.Threshold).with("depositAmount", r.Amount).with("next", next("缴押金骑行", "payDeposit"))
	}
	var view map[string]any
	err = a.inTx(ctx, func(tx pgx.Tx) error {
		now := a.now()
		acc, err := lockAccount(ctx, tx, uid, now)
		if err != nil {
			return err
		}
		if err := a.creditPrecheck(ctx, tx, acc); err != nil {
			return err
		}
		prev := acc.PrevDepositID
		if acc.Mode == "押金" {
			prev = acc.DepositID
		}
		if _, err := tx.Exec(ctx, `UPDATE accounts SET mode='免押', deposit_id=NULL, prev_deposit_id=$2, credit_provider=$3, credit_score=$4, credit_opened_at=$5, updated_at=$5
			WHERE user_id=$1`, uid, prev, provider, score, now); err != nil {
			return err
		}
		if err := a.emitStatus(ctx, tx, uid, userActor(uid)); err != nil {
			return err
		}
		if err := a.audit(ctx, tx, userActor(uid), "开通免押", "user", uid, fmt.Sprintf("开通%s信用免押（信用分 %d，门槛 %d）", provider, score, r.Threshold), nil,
			map[string]any{"mode": acc.Mode}, map[string]any{"mode": "免押", "creditProvider": provider}); err != nil {
			return err
		}
		acc, err = readAccount(ctx, tx, uid)
		if err != nil {
			return err
		}
		view, _, err = a.stateView(ctx, tx, acc)
		return err
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"deposit": view, "score": score, "threshold": r.Threshold}, nil
}

func (a *App) creditPrecheck(ctx context.Context, q querier, acc account) error {
	if acc.Mode == "免押" {
		return newErr(409, "ALREADY_DONE", "你已开通信用免押")
	}
	if acc.Mode == "押金" && acc.DepositID != nil {
		d, err := readDeposit(ctx, q, *acc.DepositID, false)
		if err != nil {
			return err
		}
		if d != nil && (d.Status == "已缴" || d.Status == "押金不足") {
			return newErr(409, "REFUND_BLOCKED", fmt.Sprintf("开通免押前请先申请退还押金 ¥%s", yuan(d.Balance))).with("next", next("申请退押", "refundDeposit"))
		}
	}
	return nil
}

// ======================= 押金缴纳与补缴（R-15 BR-25） =======================

type chargeRow struct {
	ID, UserID, Kind, Channel, Status string
	DepositID, PaymentID, FailReason  *string
	Amount                            int64
	PricingVersionID                  *string
}

const chargeSelect = `SELECT charge_id, user_id, kind, channel, status, deposit_id, payment_id, fail_reason, amount, pricing_version_id FROM charges`

func scanCharge(row pgx.Row) (*chargeRow, error) {
	c := &chargeRow{}
	err := row.Scan(&c.ID, &c.UserID, &c.Kind, &c.Channel, &c.Status, &c.DepositID, &c.PaymentID, &c.FailReason, &c.Amount, &c.PricingVersionID)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return c, err
}

// charge 缴纳（kind=押金缴纳）或补缴（kind=押金补缴）。同一用户同一幂等键只建一笔收款记录，重试取最新结果。
func (a *App) charge(ctx context.Context, uid, idemKey, kind string) (map[string]any, error) {
	var c *chargeRow
	err := a.inTx(ctx, func(tx pgx.Tx) error {
		now := a.now()
		acc, err := lockAccount(ctx, tx, uid, now)
		if err != nil {
			return err
		}
		if c, err = scanCharge(tx.QueryRow(ctx, chargeSelect+` WHERE user_id=$1 AND idem_key=$2`, uid, idemKey)); err != nil || c != nil {
			return err
		}
		var pending int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM charges WHERE user_id=$1 AND status='处理中'`, uid).Scan(&pending); err != nil {
			return err
		}
		var d *deposit
		if acc.Mode == "押金" && acc.DepositID != nil {
			if d, err = readDeposit(ctx, tx, *acc.DepositID, true); err != nil {
				return err
			}
		}
		f, err := loadFlags(ctx, tx, uid)
		if err != nil {
			return err
		}
		c = &chargeRow{UserID: uid, Kind: kind, Channel: a.collectChannel(f), Status: "处理中"}
		if kind == "押金缴纳" {
			if err := checkEligible(f, "缴押金"); err != nil {
				return err
			}
			if acc.Mode == "免押" {
				return newErr(409, "ALREADY_DONE", "你已开通信用免押，不用再交押金")
			}
			if d != nil && d.Status != "已退" {
				return newErr(409, "ALREADY_DONE", "你已缴纳押金")
			}
			if pending > 0 {
				return errResultUnknown()
			}
			r, err := a.currentRule(ctx, tx)
			if err != nil {
				return err
			}
			if r == nil {
				return errBusy("押金规则尚未同步，请稍后再试")
			}
			c.Amount, c.PricingVersionID = r.Amount, &r.VersionID
		} else {
			if d == nil || d.Status != "押金不足" || d.AmountPaid <= d.Balance {
				return newErr(409, "NOTHING_TO_PAY", "押金余额充足，无需补缴")
			}
			if pending > 0 {
				return errResultUnknown()
			}
			c.Amount, c.DepositID = d.AmountPaid-d.Balance, &d.ID // 补足到缴纳时金额，不补调价差（BR-25）
		}
		if c.ID, err = nextID(ctx, tx, "seq_charge", "DC"); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO charges (charge_id, user_id, kind, deposit_id, amount, channel, pricing_version_id, status, idem_key, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'处理中',$8,$9)`, c.ID, uid, kind, c.DepositID, c.Amount, c.Channel, c.PricingVersionID, idemKey, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	switch c.Status {
	case "成功":
		return a.chargeResult(ctx, c.ID)
	case "失败":
		return nil, declined(c.Kind, c.FailReason)
	}
	res, err := a.Pay.ChargeDeposit(ctx, c.ID, pay.ChargeCommand{BizNo: c.ID, Type: c.Kind, UserID: uid, Amount: c.Amount, Channel: c.Channel})
	if err != nil {
		a.Log.Warn("押金收款调用失败", "charge", c.ID, "err", err)
		return nil, errResultUnknown()
	}
	o := outcomeOf(res)
	if err := a.inTx(ctx, func(tx pgx.Tx) error { return a.applyCharge(ctx, tx, c.ID, o) }); err != nil {
		return nil, err
	}
	switch o.Result {
	case "成功":
		return a.chargeResult(ctx, c.ID)
	case "失败":
		return nil, declined(c.Kind, o.FailReason)
	}
	return nil, errResultUnknown()
}

// collectChannel 押金收款渠道：按注册渠道（微信收微信支付，支付宝收支付宝），未知用部署配置的缺省值。
func (a *App) collectChannel(f userFlags) string {
	switch f.channel {
	case "微信":
		return "微信支付"
	case "支付宝":
		return "支付宝"
	}
	return a.DefaultChannel
}

func declined(kind string, reason *string) error {
	why := "渠道未返回结果"
	if reason != nil && *reason != "" {
		why = *reason
	}
	verb := "押金没有缴成功"
	if kind == "押金补缴" {
		verb = "补缴没有成功"
	}
	return newErr(409, "PAY_DECLINED", verb+"："+why).with("next", next("换个方式重新支付", "payDeposit"))
}

func (a *App) chargeResult(ctx context.Context, chargeID string) (map[string]any, error) {
	c, err := scanCharge(a.DB.QueryRow(ctx, chargeSelect+` WHERE charge_id=$1`, chargeID))
	if err != nil {
		return nil, err
	}
	acc, err := readAccount(ctx, a.DB, c.UserID)
	if err != nil {
		return nil, err
	}
	view, _, err := a.stateView(ctx, a.DB, acc)
	if err != nil {
		return nil, err
	}
	return map[string]any{"deposit": view, "paymentId": c.PaymentID, "amount": c.Amount}, nil
}

// outcome pay 命令或事件给出的结果。
type outcome struct {
	Result     string // 成功｜失败｜结果未知
	PaymentID  string
	FailCode   string
	FailReason *string
	At         time.Time
	Related    *string
}

func outcomeOf(r pay.Result) outcome {
	o := outcome{Result: r.Result, PaymentID: r.PaymentID, FailReason: r.FailReason, At: time.Now()}
	if r.FailCode != nil {
		o.FailCode = *r.FailCode
	}
	if r.CompletedAt != nil {
		o.At = *r.CompletedAt
	}
	return o
}

func ledgerStatus(result string) string {
	switch result {
	case "成功", "失败":
		return result
	}
	return "处理中"
}

// recordLedger 记专户台账一笔；同一流水只能从处理中变为成功或失败，终态不再改。
func (a *App) recordLedger(ctx context.Context, tx pgx.Tx, paymentID, typ, direction, uid string, orderID *string, amount int64, channel string, o outcome, bizNo string) error {
	if paymentID == "" || amount <= 0 {
		return nil
	}
	st := ledgerStatus(o.Result)
	var completed *time.Time
	if st == "成功" {
		t := o.At
		completed = &t
	}
	var fail *string
	if st == "失败" {
		fail = o.FailReason
	}
	_, err := tx.Exec(ctx, `INSERT INTO ledger (payment_id, type, direction, user_id, order_id, amount, channel, status, fail_reason, biz_no, related_payment_id, created_at, completed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (payment_id) DO UPDATE SET status=EXCLUDED.status, fail_reason=EXCLUDED.fail_reason, completed_at=EXCLUDED.completed_at
		WHERE ledger.status = '处理中' AND EXCLUDED.status <> '处理中'`,
		paymentID, typ, direction, uid, orderID, amount, channel, st, fail, bizNo, o.Related, a.now(), completed)
	return err
}

// applyCharge 落收款结果。成功时缴纳建押金、补缴加余额，并发押金状态变更。
func (a *App) applyCharge(ctx context.Context, tx pgx.Tx, chargeID string, o outcome) error {
	c, err := scanCharge(tx.QueryRow(ctx, chargeSelect+` WHERE charge_id=$1 FOR UPDATE`, chargeID))
	if err != nil || c == nil {
		return err
	}
	if err := a.recordLedger(ctx, tx, o.PaymentID, c.Kind, "收", c.UserID, nil, c.Amount, c.Channel, o, c.ID); err != nil {
		return err
	}
	if c.Status != "处理中" {
		return nil
	}
	now := a.now()
	switch o.Result {
	case "失败":
		_, err = tx.Exec(ctx, `UPDATE charges SET status='失败', payment_id=NULLIF($2,''), fail_reason=$3, completed_at=$4 WHERE charge_id=$1`, c.ID, o.PaymentID, o.FailReason, now)
		return err
	case "成功":
	default:
		_, err = tx.Exec(ctx, `UPDATE charges SET payment_id=NULLIF($2,'') WHERE charge_id=$1`, c.ID, o.PaymentID)
		return err
	}
	acc, err := lockAccount(ctx, tx, c.UserID, now)
	if err != nil {
		return err
	}
	if c.Kind == "押金缴纳" {
		depID, err := nextID(ctx, tx, "seq_deposit", "DP")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO deposits (deposit_id, user_id, amount_paid, balance, status, paid_at, payment_id, channel, pricing_version_id, created_at)
			VALUES ($1,$2,$3,$3,'已缴',$4,$5,$6,$7,$8)`, depID, c.UserID, c.Amount, o.At, o.PaymentID, c.Channel, c.PricingVersionID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE charges SET status='成功', payment_id=$2, deposit_id=$3, completed_at=$4 WHERE charge_id=$1`, c.ID, o.PaymentID, depID, o.At); err != nil {
			return err
		}
		attach := acc.Mode == "未开通"
		if acc.Mode == "押金" && acc.DepositID != nil {
			old, err := readDeposit(ctx, tx, *acc.DepositID, false)
			if err != nil {
				return err
			}
			attach = old == nil || old.Status == "已退"
		}
		if !attach {
			// 收款前用户已改开免押或已有押金：钱在专户里单独成一笔押金，等人工处理
			a.Log.Error("押金收款到账时用户已有押金或免押，未挂到账户", "user", c.UserID, "deposit", depID)
			return a.audit(ctx, tx, userActor(c.UserID), "缴纳押金", "user", c.UserID, fmt.Sprintf("缴纳押金 ¥%s（流水 %s）；到账时用户已有押金或免押，未挂到账户，待人工处理", yuan(c.Amount), o.PaymentID), nil,
				nil, map[string]any{"depositId": depID, "amountPaid": c.Amount})
		}
		if _, err := tx.Exec(ctx, `UPDATE accounts SET mode='押金', deposit_id=$2, updated_at=$3 WHERE user_id=$1`, c.UserID, depID, now); err != nil {
			return err
		}
		if err := a.audit(ctx, tx, userActor(c.UserID), "缴纳押金", "user", c.UserID, fmt.Sprintf("缴纳押金 ¥%s（流水 %s）", yuan(c.Amount), o.PaymentID), nil,
			map[string]any{"mode": acc.Mode}, map[string]any{"mode": "押金", "status": "已缴", "amountPaid": c.Amount, "balance": c.Amount}); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(ctx, `UPDATE charges SET status='成功', payment_id=$2, completed_at=$3 WHERE charge_id=$1`, c.ID, o.PaymentID, o.At); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE deposits SET balance = balance + $2,
			status = CASE WHEN status IN ('已缴','押金不足') THEN (CASE WHEN balance + $2 >= amount_paid THEN '已缴' ELSE '押金不足' END) ELSE status END
			WHERE deposit_id = $1`, *c.DepositID, c.Amount); err != nil {
			return err
		}
		nd, err := readDeposit(ctx, tx, *c.DepositID, false)
		if err != nil {
			return err
		}
		if err := a.audit(ctx, tx, userActor(c.UserID), "补缴押金", "user", c.UserID, fmt.Sprintf("补缴押金 ¥%s（流水 %s），押金余额 ¥%s", yuan(c.Amount), o.PaymentID, yuan(nd.Balance)), nil,
			map[string]any{"balance": nd.Balance - c.Amount}, map[string]any{"status": nd.Status, "balance": nd.Balance}); err != nil {
			return err
		}
	}
	return a.emitStatus(ctx, tx, c.UserID, userActor(c.UserID))
}

// ======================= 退押（R-17 BR-23 BR-24 BR-25） =======================

func (a *App) requestRefund(ctx context.Context, uid string, actor Actor, reason, ticketID *string) (*refundReq, error) {
	var out *refundReq
	err := a.inTx(ctx, func(tx pgx.Tx) error {
		now := a.now()
		acc, err := lockAccount(ctx, tx, uid, now)
		if err != nil {
			return err
		}
		var d *deposit
		if acc.Mode == "押金" && acc.DepositID != nil {
			if d, err = readDeposit(ctx, tx, *acc.DepositID, true); err != nil {
				return err
			}
		}
		if !refundable(d) {
			msg := "你当前没有在平台缴纳押金"
			if d != nil && d.Status == "退押中" {
				msg = "押金正在退还中"
			}
			return newErr(409, "NOT_DEPOSIT_USER", msg)
		}
		var pending int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM charges WHERE user_id=$1 AND status='处理中'`, uid).Scan(&pending); err != nil {
			return err
		}
		if pending > 0 {
			return errResultUnknown()
		}
		bl, err := refundBlockers(ctx, tx, uid)
		if err != nil {
			return err
		}
		if len(bl) > 0 {
			return blockedErr(bl)
		}
		id, err := nextID(ctx, tx, "seq_refund", "RF")
		if err != nil {
			return err
		}
		by, _ := json.Marshal(actor)
		r := &refundReq{ID: id, UserID: uid, DepositID: d.ID, Amount: d.Balance, AppliedAt: now,
			ExpectedAt: workday.Add(now, a.refundWorkdays(ctx, tx)), Status: "待阻止开单", RequestedBy: actor}
		if _, err := tx.Exec(ctx, `INSERT INTO refund_requests (request_id, user_id, deposit_id, amount, applied_at, expected_at, status, requested_by, reason, ticket_id, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,'待阻止开单',$7,$8,$9,$5)`, r.ID, uid, d.ID, r.Amount, now, r.ExpectedAt, by, reason, ticketID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE deposits SET status='退押中' WHERE deposit_id=$1`, d.ID); err != nil {
			return err
		}
		if err := a.emit(ctx, tx, "deposit.refund.requested", uid, actor, map[string]any{"requestId": r.ID, "userId": uid, "amount": r.Amount, "appliedAt": a.ts(now)}); err != nil {
			return err
		}
		if err := a.emitStatus(ctx, tx, uid, actor); err != nil {
			return err
		}
		who := "用户"
		if actor.Kind != "用户" {
			who = "客服协助"
		}
		why := reason
		if why == nil {
			s := "用户申请"
			why = &s
		}
		if err := a.audit(ctx, tx, actor, "申请退押", "user", uid, fmt.Sprintf("%s申请退押 ¥%s，已通知停止开新单，收到回执后原路退回", who, yuan(r.Amount)), why,
			map[string]any{"status": d.Status}, map[string]any{"status": "退押中", "requestId": r.ID}); err != nil {
			return err
		}
		out = r
		return nil
	})
	return out, err
}

func blockedErr(bl []blocker) *apiError {
	msgs := make([]string, len(bl))
	for i, b := range bl {
		msgs[i] = b.Message
	}
	return newErr(409, "REFUND_BLOCKED", "暂不能退押："+strings.Join(msgs, "；")).with("blockers", bl).with("next", next("先去处理", "resolveBlockers"))
}

func (a *App) refundText(r *refundReq) string {
	t := r.ExpectedAt.In(a.Loc)
	return fmt.Sprintf("押金 ¥%s 将原路退回，预计 %d月%d日 前到账。退押后需重新缴押或开通免押才能骑行。", yuan(r.Amount), int(t.Month()), t.Day())
}

// onAdmissionBlocked 收到 ride 的"已阻止开单"回执：仍有骑行中订单则拒绝退押，否则记回执时刻并向 pay 发退押命令（BR-24）。
func onAdmissionBlocked(a *App, ctx context.Context, tx pgx.Tx, env Envelope) (func(context.Context), error) {
	var p struct {
		UserID        string    `json:"userId"`
		RequestID     string    `json:"requestId"`
		BlockedAt     time.Time `json:"blockedAt"`
		RidingOrderID *string   `json:"ridingOrderId"`
	}
	if err := decode(env, &p); err != nil {
		return nil, err
	}
	r, err := scanRefund(tx.QueryRow(ctx, refundSelect+` WHERE request_id=$1 FOR UPDATE`, p.RequestID))
	if err != nil || r == nil || r.Status != "待阻止开单" {
		return nil, err
	}
	if p.RidingOrderID != nil && *p.RidingOrderID != "" {
		why := "仍有骑行中订单 " + *p.RidingOrderID
		if _, err := tx.Exec(ctx, `UPDATE refund_requests SET status='已拒绝', reject_reason=$2, stopped_at=$3 WHERE request_id=$1`, r.ID, why, p.BlockedAt); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE deposits SET status = CASE WHEN balance < amount_paid THEN '押金不足' ELSE '已缴' END
			WHERE deposit_id=$1 AND status='退押中'`, r.DepositID); err != nil {
			return nil, err
		}
		if cur, err := isCurrentDeposit(ctx, tx, r.UserID, r.DepositID); err != nil {
			return nil, err
		} else if cur {
			if err := a.emitStatus(ctx, tx, r.UserID, systemActor); err != nil {
				return nil, err
			}
		}
		return nil, a.audit(ctx, tx, systemActor, "恢复押金", "user", r.UserID, "退押申请未通过："+why+"，押金恢复原状态", &why, map[string]any{"status": "退押中"}, nil)
	}
	if _, err := tx.Exec(ctx, `UPDATE refund_requests SET status='退款中', stopped_at=$2 WHERE request_id=$1`, r.ID, p.BlockedAt); err != nil {
		return nil, err
	}
	id := r.ID
	return func(ctx context.Context) { a.sendRefund(ctx, id) }, nil
}

// sendRefund 向 pay 发退押命令（幂等键 = 退押申请编号），并落结果。pay 不可用时留给定时任务补发。
func (a *App) sendRefund(ctx context.Context, requestID string) {
	r, err := scanRefund(a.DB.QueryRow(ctx, refundSelect+` WHERE request_id=$1`, requestID))
	if err != nil || r == nil || r.Status != "退款中" || r.AwaitingPayee {
		return
	}
	d, err := readDeposit(ctx, a.DB, r.DepositID, false)
	if err != nil || d == nil {
		a.Log.Error("退押找不到押金", "request", requestID, "err", err)
		return
	}
	orig := []string{d.PaymentID}
	topups, err := paymentIDs(ctx, a.DB, topupIDsSQL, d.ID)
	if err != nil {
		return
	}
	orig = append(orig, topups...)
	act := &pay.Actor{Kind: r.RequestedBy.Kind, ID: r.RequestedBy.ID, Name: r.RequestedBy.Name}
	res, err := a.Pay.RefundDeposit(ctx, r.ID, pay.RefundCommand{BizNo: r.ID, UserID: r.UserID, Amount: r.Amount, OriginalPaymentIDs: orig, Actor: act})
	if err != nil {
		a.Log.Warn("退押命令未送达，稍后补发", "request", r.ID, "err", err)
		return
	}
	o := outcomeOf(res)
	if err := a.inTx(ctx, func(tx pgx.Tx) error { return a.applyRefund(ctx, tx, r.ID, o) }); err != nil {
		a.Log.Error("落退押结果失败", "request", r.ID, "err", err)
	}
}

// applyRefund 落原路退押的结果。
func (a *App) applyRefund(ctx context.Context, tx pgx.Tx, requestID string, o outcome) error {
	r, err := scanRefund(tx.QueryRow(ctx, refundSelect+` WHERE request_id=$1 FOR UPDATE`, requestID))
	if err != nil || r == nil {
		return err
	}
	d, err := readDeposit(ctx, tx, r.DepositID, true)
	if err != nil || d == nil {
		return err
	}
	if err := a.recordLedger(ctx, tx, o.PaymentID, "退押", "付", r.UserID, nil, r.Amount, d.Channel, o, r.ID); err != nil {
		return err
	}
	if r.Status != "退款中" {
		return nil
	}
	switch o.Result {
	case "成功":
		return a.finalizeRefund(ctx, tx, r, d, "原路", o.PaymentID, o.At)
	case "失败":
		awaiting := o.FailCode == "" || o.FailCode == "CHANNEL_FAILED"
		if !awaiting {
			a.Log.Error("退押被 pay 拒绝，需人工核查", "request", r.ID, "failCode", o.FailCode)
		}
		_, err = tx.Exec(ctx, `UPDATE refund_requests SET payment_id=COALESCE(NULLIF($2,''), payment_id), awaiting_payee=$3 WHERE request_id=$1`, r.ID, o.PaymentID, awaiting)
		return err
	default:
		_, err = tx.Exec(ctx, `UPDATE refund_requests SET payment_id=COALESCE(NULLIF($2,''), payment_id) WHERE request_id=$1`, r.ID, o.PaymentID)
		return err
	}
}

// finalizeRefund 退押到账：余额清零、状态已退、发已退押与状态变更、留痕。
func (a *App) finalizeRefund(ctx context.Context, tx pgx.Tx, r *refundReq, d *deposit, mode, paymentID string, at time.Time) error {
	if mode == "原路" {
		if _, err := tx.Exec(ctx, `UPDATE refund_requests SET status='已退', pay_mode='原路', payment_id=$2, awaiting_payee=false, refunded_at=$3 WHERE request_id=$1`, r.ID, paymentID, at); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(ctx, `UPDATE refund_requests SET status='已退', pay_mode='人工转账', manual_payment_id=$2, awaiting_payee=false, refunded_at=$3 WHERE request_id=$1`, r.ID, paymentID, at); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE deposits SET balance = 0, status='已退' WHERE deposit_id=$1`, d.ID); err != nil {
		return err
	}
	if err := a.emit(ctx, tx, "deposit.refunded", r.UserID, systemActor, map[string]any{"requestId": r.ID, "userId": r.UserID, "amount": r.Amount, "mode": mode, "refundedAt": a.ts(at)}); err != nil {
		return err
	}
	if cur, err := isCurrentDeposit(ctx, tx, r.UserID, d.ID); err != nil {
		return err
	} else if cur {
		if err := a.emitStatus(ctx, tx, r.UserID, systemActor); err != nil {
			return err
		}
	}
	return a.audit(ctx, tx, systemActor, "退还押金", "user", r.UserID, fmt.Sprintf("押金 ¥%s 已%s退还（流水 %s）", yuan(r.Amount), map[string]string{"原路": "原路", "人工转账": "人工转账"}[mode], paymentID), nil,
		map[string]any{"status": "退押中", "balance": d.Balance}, map[string]any{"status": "已退", "balance": 0})
}

// ======================= 押金抵扣（BR-22） =======================

type deductCmd struct {
	UserID  string `json:"userId"`
	OrderID string `json:"orderId"`
	Amount  int64  `json:"amount"`
}

type deductionRow struct {
	ID, UserID, OrderID, DepositID, Status string
	Requested, Amount                      int64
	PaymentID                              *string
}

const deductionSelect = `SELECT deduction_id, user_id, order_id, deposit_id, status, requested, amount, payment_id FROM deductions`

func scanDeduction(row pgx.Row) (*deductionRow, error) {
	d := &deductionRow{}
	err := row.Scan(&d.ID, &d.UserID, &d.OrderID, &d.DepositID, &d.Status, &d.Requested, &d.Amount, &d.PaymentID)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return d, err
}

// deduct 执行抵扣命令，返回 DeductResult。
func (a *App) deduct(ctx context.Context, caller, idemKey string, cmd deductCmd) (map[string]any, error) {
	var row *deductionRow
	var failed map[string]any
	err := a.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		if row, err = scanDeduction(tx.QueryRow(ctx, deductionSelect+` WHERE caller=$1 AND idem_key=$2`, caller, idemKey)); err != nil || row != nil {
			return err
		}
		var owner string
		if err := tx.QueryRow(ctx, `SELECT user_id FROM proj_bills WHERE order_id=$1`, cmd.OrderID).Scan(&owner); err != nil && err != pgx.ErrNoRows {
			return err
		}
		if owner != "" && owner != cmd.UserID {
			return errInvalid("该订单不属于该用户，押金只能抵本人的骑行费")
		}
		now := a.now()
		acc, err := lockAccount(ctx, tx, cmd.UserID, now)
		if err != nil {
			return err
		}
		var d *deposit
		if acc.Mode == "押金" && acc.DepositID != nil {
			if d, err = readDeposit(ctx, tx, *acc.DepositID, true); err != nil {
				return err
			}
		}
		fail := func(code, reason string) {
			failed = map[string]any{"result": "失败", "failCode": code, "failReason": reason, "userId": cmd.UserID, "orderId": cmd.OrderID,
				"deducted": 0, "shortfall": cmd.Amount, "balanceAfter": nil, "status": nil, "paymentId": nil}
			if d != nil {
				failed["balanceAfter"], failed["status"] = d.Balance, d.Status
			}
		}
		if d == nil || d.Status == "已退" {
			fail("NOT_DEPOSIT_USER", "该用户不是押金用户")
			return nil
		}
		if d.Status == "退押中" || d.Balance <= 0 {
			fail("NO_BALANCE", "押金余额为 0 或正在退押")
			return nil
		}
		amt := min(cmd.Amount, d.Balance)
		id, err := nextID(ctx, tx, "seq_deduction", "DD")
		if err != nil {
			return err
		}
		// 先占用余额，划转失败再退回
		if _, err := tx.Exec(ctx, `UPDATE deposits SET balance = balance - $2,
			status = CASE WHEN balance - $2 < amount_paid THEN '押金不足' ELSE status END WHERE deposit_id=$1`, d.ID, amt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO deductions (deduction_id, caller, idem_key, user_id, order_id, deposit_id, requested, amount, status, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'处理中',$9)`, id, caller, idemKey, cmd.UserID, cmd.OrderID, d.ID, cmd.Amount, amt, now); err != nil {
			return err
		}
		row = &deductionRow{ID: id, UserID: cmd.UserID, OrderID: cmd.OrderID, DepositID: d.ID, Status: "处理中", Requested: cmd.Amount, Amount: amt}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if failed != nil {
		return failed, nil
	}
	if row.Status == "处理中" {
		a.sendTransfer(ctx, row)
	}
	return a.deductResult(ctx, row.ID)
}

func (a *App) sendTransfer(ctx context.Context, row *deductionRow) {
	res, err := a.Pay.TransferDeposit(ctx, row.ID, pay.TransferCommand{BizNo: row.ID, UserID: row.UserID, OrderID: row.OrderID, Amount: row.Amount})
	if err != nil {
		a.Log.Warn("专户划转未送达，稍后补发", "deduction", row.ID, "err", err)
		return
	}
	if err := a.inTx(ctx, func(tx pgx.Tx) error { return a.applyDeduction(ctx, tx, row.ID, outcomeOf(res)) }); err != nil {
		a.Log.Error("落划转结果失败", "deduction", row.ID, "err", err)
	}
}

// applyDeduction 落专户划转结果。
func (a *App) applyDeduction(ctx context.Context, tx pgx.Tx, id string, o outcome) error {
	row, err := scanDeduction(tx.QueryRow(ctx, deductionSelect+` WHERE deduction_id=$1 FOR UPDATE`, id))
	if err != nil || row == nil {
		return err
	}
	order := row.OrderID
	if err := a.recordLedger(ctx, tx, o.PaymentID, "押金抵扣", "转", row.UserID, &order, row.Amount, "押金专户划转", o, row.ID); err != nil {
		return err
	}
	if row.Status != "处理中" {
		return nil
	}
	now := a.now()
	switch o.Result {
	case "成功":
	case "失败":
		if _, err := tx.Exec(ctx, `UPDATE deductions SET status='失败', payment_id=NULLIF($2,''), fail_reason=$3, completed_at=$4 WHERE deduction_id=$1`, id, o.PaymentID, o.FailReason, now); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE deposits SET balance = balance + $2,
			status = CASE WHEN status IN ('已缴','押金不足') THEN (CASE WHEN balance + $2 >= amount_paid THEN '已缴' ELSE '押金不足' END) ELSE status END
			WHERE deposit_id=$1`, row.DepositID, row.Amount)
		return err
	default:
		_, err := tx.Exec(ctx, `UPDATE deductions SET payment_id=NULLIF($2,'') WHERE deduction_id=$1`, id, o.PaymentID)
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE deductions SET status='成功', payment_id=$2, completed_at=$3 WHERE deduction_id=$1`, id, o.PaymentID, o.At); err != nil {
		return err
	}
	d, err := readDeposit(ctx, tx, row.DepositID, false)
	if err != nil {
		return err
	}
	if err := a.emit(ctx, tx, "deposit.deducted", row.UserID, systemActor, map[string]any{"userId": row.UserID, "orderId": row.OrderID, "amount": row.Amount,
		"balanceAfter": d.Balance, "deductedAt": a.ts(o.At)}); err != nil {
		return err
	}
	if err := a.emitStatus(ctx, tx, row.UserID, systemActor); err != nil {
		return err
	}
	why := "BR-22 押金只抵扣本人未付骑行费"
	return a.audit(ctx, tx, systemActor, "抵扣押金", "order", row.OrderID, fmt.Sprintf("订单 %s 支付期限届满未付，自动从押金抵扣 ¥%s，押金余额 ¥%s", row.OrderID, yuan(row.Amount), yuan(d.Balance)), &why,
		map[string]any{"balance": d.Balance + row.Amount}, map[string]any{"balance": d.Balance, "status": d.Status})
}

func (a *App) deductResult(ctx context.Context, id string) (map[string]any, error) {
	row, err := scanDeduction(a.DB.QueryRow(ctx, deductionSelect+` WHERE deduction_id=$1`, id))
	if err != nil {
		return nil, err
	}
	d, err := readDeposit(ctx, a.DB, row.DepositID, false)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"userId": row.UserID, "orderId": row.OrderID, "failCode": nil, "failReason": nil,
		"deducted": row.Amount, "shortfall": row.Requested - row.Amount, "balanceAfter": d.Balance, "status": d.Status, "paymentId": row.PaymentID}
	switch row.Status {
	case "成功":
		out["result"] = "成功"
	case "失败":
		out["result"], out["failCode"], out["failReason"] = "失败", "TRANSFER_FAILED", "专户划转失败，押金未动"
		out["deducted"], out["shortfall"] = 0, row.Requested
	default:
		out["result"] = "结果未知"
	}
	return out, nil
}

// ======================= pay 事件 =======================

type payHead struct {
	PaymentID        string     `json:"paymentId"`
	BizNo            string     `json:"bizNo"`
	Type             string     `json:"type"`
	Account          string     `json:"account"`
	UserID           string     `json:"userId"`
	Amount           int64      `json:"amount"`
	CompletedAt      *time.Time `json:"completedAt"`
	FailedAt         *time.Time `json:"failedAt"`
	FailReason       *string    `json:"failReason"`
	RelatedPaymentID *string    `json:"relatedPaymentId"`
}

func (p payHead) outcome(result string, fallback time.Time) outcome {
	o := outcome{Result: result, PaymentID: p.PaymentID, FailReason: p.FailReason, At: fallback, Related: p.RelatedPaymentID}
	if p.CompletedAt != nil {
		o.At = *p.CompletedAt
	}
	return o
}

func onPaymentResult(a *App, ctx context.Context, tx pgx.Tx, env Envelope) (func(context.Context), error) {
	var p payHead
	if err := decode(env, &p); err != nil {
		return nil, err
	}
	if p.Account != "押金专户" {
		return nil, nil
	}
	result := map[string]string{"pay.payment.succeeded": "成功", "pay.payment.failed": "失败", "pay.payment.unknown": "结果未知"}[env.Type]
	o := p.outcome(result, env.OccurredAt)
	switch p.Type {
	case "押金缴纳", "押金补缴":
		return nil, a.applyCharge(ctx, tx, p.BizNo, o)
	case "押金抵扣":
		return nil, a.applyDeduction(ctx, tx, p.BizNo, o)
	}
	return nil, nil
}

func (a *App) refundByPayment(ctx context.Context, tx pgx.Tx, p payHead) (*refundReq, error) {
	r, err := scanRefund(tx.QueryRow(ctx, refundSelect+` WHERE request_id=$1 FOR UPDATE`, p.BizNo))
	if err != nil || r != nil {
		return r, err
	}
	if p.RelatedPaymentID != nil {
		return scanRefund(tx.QueryRow(ctx, refundSelect+` WHERE payment_id=$1 FOR UPDATE`, *p.RelatedPaymentID))
	}
	return nil, nil
}

func onRefundSucceeded(a *App, ctx context.Context, tx pgx.Tx, env Envelope) (func(context.Context), error) {
	var p payHead
	if err := decode(env, &p); err != nil {
		return nil, err
	}
	if p.Account != "押金专户" {
		return nil, nil
	}
	switch p.Type {
	case "退押":
		r, err := a.refundByPayment(ctx, tx, p)
		if err != nil || r == nil {
			return nil, err
		}
		return nil, a.applyRefund(ctx, tx, r.ID, p.outcome("成功", env.OccurredAt))
	case "人工转账退押":
		r, err := a.refundByPayment(ctx, tx, p)
		if err != nil || r == nil {
			return nil, err
		}
		d, err := readDeposit(ctx, tx, r.DepositID, true)
		if err != nil || d == nil {
			return nil, err
		}
		o := p.outcome("成功", env.OccurredAt)
		if err := a.recordLedger(ctx, tx, p.PaymentID, "人工转账退押", "付", r.UserID, nil, p.Amount, "银行转账", o, r.ID); err != nil {
			return nil, err
		}
		if r.Status != "退款中" {
			return nil, nil
		}
		return nil, a.finalizeRefund(ctx, tx, r, d, "人工转账", p.PaymentID, o.At)
	}
	return nil, nil
}

func onRefundFailed(a *App, ctx context.Context, tx pgx.Tx, env Envelope) (func(context.Context), error) {
	var p payHead
	if err := decode(env, &p); err != nil {
		return nil, err
	}
	if p.Account != "押金专户" || p.Type != "退押" {
		return nil, nil
	}
	r, err := a.refundByPayment(ctx, tx, p)
	if err != nil || r == nil {
		return nil, err
	}
	at := env.OccurredAt
	if p.FailedAt != nil {
		at = *p.FailedAt
	}
	o := outcome{Result: "失败", PaymentID: p.PaymentID, FailCode: "CHANNEL_FAILED", FailReason: p.FailReason, At: at}
	return nil, a.applyRefund(ctx, tx, r.ID, o)
}

func onPayeeSubmitted(_ *App, ctx context.Context, tx pgx.Tx, env Envelope) (func(context.Context), error) {
	var p struct {
		PaymentID string `json:"paymentId"`
		Account   string `json:"account"`
	}
	if err := decode(env, &p); err != nil {
		return nil, err
	}
	if p.Account != "押金专户" {
		return nil, nil
	}
	_, err := tx.Exec(ctx, `UPDATE refund_requests SET awaiting_payee=false WHERE payment_id=$1 AND status='退款中'`, p.PaymentID)
	return nil, err
}

// ======================= 定时补发 =======================

// RetryPending 补发结果未知或未送达的收款、划转、退押命令（同一幂等键向 pay 取最新结果）。多实例时用数据库锁只跑一份。
func (a *App) RetryPending(ctx context.Context) {
	conn, err := a.DB.Acquire(ctx)
	if err != nil {
		return
	}
	defer conn.Release()
	var ok bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(31010)`).Scan(&ok); err != nil || !ok {
		return
	}
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(31010)`) }()

	charges, err := collect(ctx, a, chargeSelect+` WHERE status='处理中' ORDER BY created_at LIMIT 100`, scanCharge)
	if err == nil {
		for _, c := range charges {
			res, err := a.Pay.ChargeDeposit(ctx, c.ID, pay.ChargeCommand{BizNo: c.ID, Type: c.Kind, UserID: c.UserID, Amount: c.Amount, Channel: c.Channel})
			if err != nil {
				continue
			}
			if err := a.inTx(ctx, func(tx pgx.Tx) error { return a.applyCharge(ctx, tx, c.ID, outcomeOf(res)) }); err != nil {
				a.Log.Error("补发收款落账失败", "charge", c.ID, "err", err)
			}
		}
	}
	deds, err := collect(ctx, a, deductionSelect+` WHERE status='处理中' ORDER BY created_at LIMIT 100`, scanDeduction)
	if err == nil {
		for _, d := range deds {
			a.sendTransfer(ctx, d)
		}
	}
	rows, err := a.DB.Query(ctx, `SELECT r.request_id FROM refund_requests r LEFT JOIN ledger l ON l.payment_id = r.payment_id
		WHERE r.status='退款中' AND NOT r.awaiting_payee AND (r.payment_id IS NULL OR l.status = '处理中') ORDER BY r.applied_at LIMIT 100`)
	if err == nil {
		ids, _ := pgx.CollectRows(rows, pgx.RowTo[string])
		for _, id := range ids {
			a.sendRefund(ctx, id)
		}
	}
}

func collect[T any](ctx context.Context, a *App, sql string, scan func(pgx.Row) (*T, error)) ([]*T, error) {
	rows, err := a.DB.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
