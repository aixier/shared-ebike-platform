package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"bike/bill/internal/billing"
	"bike/bill/internal/clients/pay"
)

// 月卡与优惠券（R-02 BR-07）。

// 月卡记录状态
const (
	cardPaying = "支付中"
	cardPaid   = "已支付"
	cardFailed = "支付失败"
)

// MonthCard 月卡（swagger MonthCard）
type MonthCard struct {
	CardID           string          `json:"cardId"`
	UserID           string          `json:"userId"`
	PricingVersionID string          `json:"pricingVersionId"`
	Price            int64           `json:"price"`
	PerOrderCap      int64           `json:"perOrderCap"`
	StartAt          time.Time       `json:"startAt"`
	EndAt            time.Time       `json:"endAt"`
	PaymentID        *string         `json:"paymentId"`
	Status           string          `json:"status"`
	UsedOrderIDs     []string        `json:"usedOrderIds"`
	PayState         string          `json:"payState"`  // 已支付｜待支付
	PayParams        json.RawMessage `json:"payParams"` // 待支付时的调起参数
	state            string
	paidAt           *time.Time
}

func (a *App) t(x time.Time) time.Time { return x.In(a.Loc).Truncate(time.Second) }

func (a *App) listCards(ctx context.Context, q querier, where string, args ...any) ([]MonthCard, error) {
	rows, err := q.Query(ctx, `SELECT card_id, user_id, pricing_version_id, price, per_order_cap, start_at, end_at, payment_id, state, paid_at, pay_params,
		COALESCE((SELECT array_agg(order_id ORDER BY bill_pushed_at) FROM bills WHERE month_card_id = month_cards.card_id AND month_card_deduct > 0), '{}')
		FROM month_cards WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := a.Now()
	out := []MonthCard{}
	for rows.Next() {
		var c MonthCard
		var params []byte
		if err := rows.Scan(&c.CardID, &c.UserID, &c.PricingVersionID, &c.Price, &c.PerOrderCap, &c.StartAt, &c.EndAt, &c.PaymentID, &c.state, &c.paidAt, &params, &c.UsedOrderIDs); err != nil {
			return nil, err
		}
		c.Status = billing.MonthCardStatus(c.StartAt, c.EndAt, now)
		c.PayState = "已支付"
		if c.state != cardPaid {
			c.PayState, c.Status, c.PayParams = "待支付", "未生效", params
		}
		c.StartAt, c.EndAt = a.t(c.StartAt), a.t(c.EndAt)
		out = append(out, c)
	}
	return out, rows.Err()
}

// userCards 本人已支付的月卡，按生效时间新到旧
func (a *App) userCards(ctx context.Context, userID string) ([]MonthCard, error) {
	return a.listCards(ctx, a.Pool, `user_id=$1 AND state=$2 ORDER BY start_at DESC`, userID, cardPaid)
}

// MonthCardRuleView 现行月卡规则
type MonthCardRuleView struct {
	Price       int64  `json:"price"`
	Days        int    `json:"days"`
	PerOrderCap int64  `json:"perOrderCap"`
	AppliesTo   string `json:"appliesTo,omitempty"`
	Text        string `json:"text"`
}

func cardRuleView(v *billing.Version) MonthCardRuleView {
	return MonthCardRuleView{Price: v.MonthCard.Price, Days: v.MonthCard.Days, PerOrderCap: v.MonthCard.PerOrderCap, AppliesTo: v.MonthCard.AppliesTo,
		Text: fmt.Sprintf("%d 天内每单骑行费最多抵 %s（不抵调度费）", v.MonthCard.Days, billing.Yuan(v.MonthCard.PerOrderCap))}
}

// bizError 用户可见的业务错误
type bizError struct {
	status    int
	code, msg string
	extra     map[string]any
}

func (e *bizError) Error() string { return e.msg }

func biz(status int, code, msg string) *bizError {
	return &bizError{status: status, code: code, msg: msg}
}

// BuyMonthCard 购买月卡：价格取现行规则，有未到期的卡时顺延
func (a *App) BuyMonthCard(ctx context.Context, userID string) (*MonthCard, error) {
	now := a.Now()
	var card MonthCard
	var channel, existingID string
	err := inTx(ctx, a.Pool, func(tx pgx.Tx) error {
		if err := ensureProfile(ctx, tx, userID); err != nil {
			return err
		}
		// 同一用户的购买串行
		if _, err := tx.Exec(ctx, `SELECT 1 FROM user_profiles WHERE user_id=$1 FOR UPDATE`, userID); err != nil {
			return err
		}
		prof, err := getProfile(ctx, tx, userID)
		if err != nil {
			return err
		}
		if prof.Frozen || prof.Cancelled {
			return biz(409, "ACCOUNT_FROZEN", "账户已冻结，解冻后才能购买月卡")
		}
		if str(prof.RealnameStatus) != "已核验" {
			return biz(409, "NEED_ONBOARDING", "完成实名核验后即可购买月卡")
		}
		var paying int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM month_cards WHERE user_id=$1 AND state=$2`, userID, cardPaying).Scan(&paying); err != nil {
			return err
		}
		if paying > 0 {
			// 上一次购买已经发起、等用户调起收银台：重复购买返回同一张待支付卡与同一份调起参数
			err := tx.QueryRow(ctx, `SELECT card_id FROM month_cards WHERE user_id=$1 AND state=$2 AND pay_params IS NOT NULL ORDER BY created_at DESC LIMIT 1`, userID, cardPaying).Scan(&existingID)
			if err == nil {
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			return biz(409, "CONFLICT", "上一次购买的支付结果尚未返回，稍后再试")
		}
		v, err := currentVersion(ctx, tx, now)
		if errors.Is(err, errNotFound) {
			return biz(409, "CONFLICT", "暂无可售的月卡规则")
		}
		if err != nil {
			return err
		}
		var ends []time.Time
		rows, err := tx.Query(ctx, `SELECT end_at FROM month_cards WHERE user_id=$1 AND state=$2 AND end_at > $3`, userID, cardPaid, now)
		if err != nil {
			return err
		}
		for rows.Next() {
			var e time.Time
			if err := rows.Scan(&e); err != nil {
				rows.Close()
				return err
			}
			ends = append(ends, e)
		}
		rows.Close()
		start, end := billing.MonthCardSpan(now, ends, v.MonthCard.Days)
		var seq int64
		if err := tx.QueryRow(ctx, `SELECT nextval('month_card_seq')`).Scan(&seq); err != nil {
			return err
		}
		card = MonthCard{CardID: fmt.Sprintf("MC%s%04d", now.In(a.Loc).Format("20060102"), seq), UserID: userID, PricingVersionID: v.VersionID,
			Price: v.MonthCard.Price, PerOrderCap: v.MonthCard.PerOrderCap, StartAt: start, EndAt: end}
		channel = str(prof.Channel)
		_, err = tx.Exec(ctx, `INSERT INTO month_cards (card_id, user_id, pricing_version_id, price, per_order_cap, start_at, end_at, state, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, card.CardID, userID, card.PricingVersionID, card.Price, card.PerOrderCap, start, end, cardPaying, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	if existingID != "" {
		cs, err := a.listCards(ctx, a.Pool, `card_id=$1`, existingID)
		if err != nil || len(cs) == 0 {
			return nil, err
		}
		return &cs[0], nil
	}
	if card.Price == 0 {
		return a.finishCard(ctx, card.CardID, pay.Result{Result: "成功", PaymentID: ""})
	}
	res, err := a.Pay.StartOneTap(ctx, "card-"+card.CardID, pay.OneTapPaymentCommand{BizNo: card.CardID, Type: "月卡购买", UserID: userID, CardID: card.CardID, Amount: card.Price, Channel: collectChannel(channel)})
	if err != nil {
		a.Log.Warn("月卡支付调用失败", "cardId", card.CardID, "err", err)
		return nil, biz(409, "CONFLICT", "支付服务暂时没有响应，请稍后再试")
	}
	return a.finishCard(ctx, card.CardID, res)
}

// finishCard 按支付结果落定月卡
func (a *App) finishCard(ctx context.Context, cardID string, res pay.Result) (*MonthCard, error) {
	var bizErr error
	err := inTx(ctx, a.Pool, func(tx pgx.Tx) error {
		switch res.Result {
		case "成功":
			at := a.Now()
			if res.CompletedAt != nil {
				at = *res.CompletedAt
			}
			return a.activateCard(ctx, tx, cardID, res.PaymentID, at, Actor{Kind: "用户"})
		case "失败":
			if _, err := tx.Exec(ctx, `UPDATE month_cards SET state=$2, payment_id=$3 WHERE card_id=$1 AND state=$4`, cardID, cardFailed, nullIfEmpty(res.PaymentID), cardPaying); err != nil {
				return err
			}
			r := res.Reason()
			if r == "" {
				r = "渠道未返回原因"
			}
			bizErr = biz(409, "PAY_DECLINED", "月卡没有买成功："+r)
			return nil
		default:
			if res.HasPayParams() { // 两步支付：卡先是"待支付"，款到（支付成功事件）后生效
				_, err := tx.Exec(ctx, `UPDATE month_cards SET payment_id=$2, pay_params=$3 WHERE card_id=$1 AND state=$4`, cardID, nullIfEmpty(res.PaymentID), []byte(res.PayParams), cardPaying)
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE month_cards SET payment_id=$2 WHERE card_id=$1`, cardID, nullIfEmpty(res.PaymentID)); err != nil {
				return err
			}
			bizErr = biz(409, "CONFLICT", "支付结果尚未返回，稍后在“我的月卡”查看")
			return nil
		}
	})
	if err != nil {
		return nil, err
	}
	if bizErr != nil {
		return nil, bizErr
	}
	cs, err := a.listCards(ctx, a.Pool, `card_id=$1`, cardID)
	if err != nil || len(cs) == 0 {
		return nil, err
	}
	return &cs[0], nil
}

// activateCard 月卡款到账：置为已支付，发"月卡已购买"与留痕；重复到达不重复处理
func (a *App) activateCard(ctx context.Context, tx pgx.Tx, cardID, paymentID string, paidAt time.Time, actor Actor) error {
	var userID string
	var price int64
	var start, end time.Time
	err := tx.QueryRow(ctx, `UPDATE month_cards SET state=$2, payment_id=$3, paid_at=$4, pay_params=NULL WHERE card_id=$1 AND state<>$2 RETURNING user_id, price, start_at, end_at`,
		cardID, cardPaid, nullIfEmpty(paymentID), paidAt).Scan(&userID, &price, &start, &end)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if actor.Kind == "用户" && actor.ID == "" {
		actor.ID = userID
	}
	if err := emit(ctx, tx, "bill.card.purchased", userID, actor, paidAt, map[string]any{
		"cardId": cardID, "userId": userID, "price": price, "paidAt": paidAt, "startAt": start, "endAt": end,
	}); err != nil {
		return err
	}
	return emitAudit(ctx, tx, actor, paidAt, Audit{Action: "购买月卡", TargetType: "user", TargetID: userID,
		Summary: fmt.Sprintf("用户 %s 购买月卡 %s，%s", userID, cardID, billing.Yuan(price)), After: map[string]any{"cardId": cardID, "startAt": start, "endAt": end}})
}

// onCardPayment 月卡款的收款事件（bizNo 为月卡编号）
func (a *App) onCardPayment(ctx context.Context, tx pgx.Tx, typ string, e payEvent) error {
	switch typ {
	case "pay.payment.succeeded":
		at := a.Now()
		if e.CompletedAt != nil {
			at = *e.CompletedAt
		}
		return a.activateCard(ctx, tx, e.BizNo, e.PaymentID, at, Actor{Kind: "用户", ID: e.UserID})
	case "pay.payment.failed":
		_, err := tx.Exec(ctx, `UPDATE month_cards SET state=$2 WHERE card_id=$1 AND state=$3`, e.BizNo, cardFailed, cardPaying)
		return err
	}
	return nil
}

// RetryCards 重查支付中的月卡（同一幂等键取 pay 的最新结果）
func (a *App) RetryCards(ctx context.Context) error {
	rows, err := a.Pool.Query(ctx, `SELECT card_id, user_id, price FROM month_cards WHERE state=$1 AND created_at < $2`, cardPaying, a.Now().Add(-30*time.Second))
	if err != nil {
		return err
	}
	type it struct {
		id, user string
		price    int64
	}
	var items []it
	for rows.Next() {
		var x it
		if err := rows.Scan(&x.id, &x.user, &x.price); err != nil {
			rows.Close()
			return err
		}
		items = append(items, x)
	}
	rows.Close()
	for _, x := range items {
		prof, err := getProfile(ctx, a.Pool, x.user)
		if err != nil {
			return err
		}
		res, err := a.Pay.StartOneTap(ctx, "card-"+x.id, pay.OneTapPaymentCommand{BizNo: x.id, Type: "月卡购买", UserID: x.user, CardID: x.id, Amount: x.price, Channel: collectChannel(str(prof.Channel))})
		if err != nil {
			continue
		}
		if _, err := a.finishCard(ctx, x.id, res); err != nil {
			var be *bizError
			if !errors.As(err, &be) {
				return err
			}
		}
	}
	return nil
}

/* ---------------- 优惠券 ---------------- */

// Coupon 优惠券（swagger Coupon）
type Coupon struct {
	CouponID    string     `json:"couponId"`
	UserID      string     `json:"userId"`
	TemplateID  string     `json:"templateId"`
	Name        string     `json:"name"`
	Amount      int64      `json:"amount"`
	IssuedAt    time.Time  `json:"issuedAt"`
	ExpireAt    time.Time  `json:"expireAt"`
	ObtainedBy  string     `json:"obtainedBy"`
	UsedOrderID *string    `json:"usedOrderId"`
	UsedAt      *time.Time `json:"usedAt"`
	Status      string     `json:"status"`
}

// issueCoupon 发一张券（系统发放或用户领取）
func (a *App) issueCoupon(ctx context.Context, tx pgx.Tx, userID string, tpl billing.CouponTemplate, by string, at time.Time) (*Coupon, error) {
	var seq int64
	if err := tx.QueryRow(ctx, `SELECT nextval('coupon_seq')`).Scan(&seq); err != nil {
		return nil, err
	}
	c := Coupon{CouponID: fmt.Sprintf("CP%08d", seq), UserID: userID, TemplateID: tpl.TemplateID, Name: tpl.Name, Amount: tpl.Amount,
		IssuedAt: at, ExpireAt: billing.CouponExpireAt(at, tpl.ValidDays, a.Loc), ObtainedBy: by, Status: "未使用"}
	_, err := tx.Exec(ctx, `INSERT INTO coupons (coupon_id, user_id, template_id, name, amount, issued_at, expire_at, obtained_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		c.CouponID, c.UserID, c.TemplateID, c.Name, c.Amount, c.IssuedAt, c.ExpireAt, c.ObtainedBy)
	c.IssuedAt, c.ExpireAt = a.t(c.IssuedAt), a.t(c.ExpireAt)
	return &c, err
}

// listCoupons 未使用在前，按到期时间升序；status 为空不筛选
func (a *App) listCoupons(ctx context.Context, userID, status string) ([]Coupon, error) {
	rows, err := a.Pool.Query(ctx, `SELECT coupon_id, user_id, template_id, name, amount, issued_at, expire_at, obtained_by, used_order_id, used_at FROM coupons WHERE user_id=$1 ORDER BY expire_at, coupon_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := a.Now()
	var unused, rest []Coupon
	for rows.Next() {
		var c Coupon
		if err := rows.Scan(&c.CouponID, &c.UserID, &c.TemplateID, &c.Name, &c.Amount, &c.IssuedAt, &c.ExpireAt, &c.ObtainedBy, &c.UsedOrderID, &c.UsedAt); err != nil {
			return nil, err
		}
		c.Status = billing.CouponStatus(c.UsedOrderID != nil, c.ExpireAt, now)
		c.IssuedAt, c.ExpireAt = a.t(c.IssuedAt), a.t(c.ExpireAt)
		if c.UsedAt != nil {
			u := a.t(*c.UsedAt)
			c.UsedAt = &u
		}
		if status != "" && c.Status != status {
			continue
		}
		if c.Status == "未使用" {
			unused = append(unused, c)
		} else {
			rest = append(rest, c)
		}
	}
	return append(append([]Coupon{}, unused...), rest...), rows.Err()
}

// TemplateView 券模板与能否领取
type TemplateView struct {
	billing.CouponTemplate
	Kind      string  `json:"kind"`
	Claimable bool    `json:"claimable"`
	Why       *string `json:"why"`
}

// CouponTemplates 现行规则的券模板；userID 为空表示未登录
func (a *App) CouponTemplates(ctx context.Context, userID string) ([]TemplateView, error) {
	now := a.Now()
	v, err := currentVersion(ctx, a.Pool, now)
	if errors.Is(err, errNotFound) {
		return []TemplateView{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []TemplateView{}
	for _, tpl := range v.Coupons {
		w := billing.ClaimWindowAt(tpl, now, a.Loc)
		tv := TemplateView{CouponTemplate: tpl, Kind: billing.KindOf(tpl, a.Loc)}
		switch {
		case userID == "":
			tv.Why = sp("请先授权登录")
		case !w.Open:
			tv.Why = sp(w.Why)
		default:
			got, err := a.claimedInWindow(ctx, a.Pool, userID, tpl.TemplateID, w.Start)
			if err != nil {
				return nil, err
			}
			if got >= maxInt(tpl.PerUser, 1) {
				tv.Why = sp("本期已领取")
			} else {
				tv.Claimable = true
			}
		}
		out = append(out, tv)
	}
	return out, nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (a *App) claimedInWindow(ctx context.Context, q querier, userID, templateID string, start time.Time) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM coupons WHERE user_id=$1 AND template_id=$2 AND issued_at >= $3`, userID, templateID, start).Scan(&n)
	return n, err
}

// ClaimCoupon 领取"用户领取"类券：每周五 17:00 至周日开放，每期每人限领
func (a *App) ClaimCoupon(ctx context.Context, userID, templateID string) (*Coupon, error) {
	now := a.Now()
	var c *Coupon
	err := inTx(ctx, a.Pool, func(tx pgx.Tx) error {
		v, err := currentVersion(ctx, tx, now)
		if errors.Is(err, errNotFound) {
			return biz(404, "NOT_FOUND", "没有找到该优惠券")
		}
		if err != nil {
			return err
		}
		var tpl *billing.CouponTemplate
		for i := range v.Coupons {
			if v.Coupons[i].TemplateID == templateID {
				tpl = &v.Coupons[i]
			}
		}
		if tpl == nil {
			return biz(404, "NOT_FOUND", "没有找到该优惠券")
		}
		w := billing.ClaimWindowAt(*tpl, now, a.Loc)
		if !w.Open {
			return biz(409, "COUPON_NOT_OPEN", "该券当前不能领取："+w.Why)
		}
		if err := ensureProfile(ctx, tx, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM user_profiles WHERE user_id=$1 FOR UPDATE`, userID); err != nil {
			return err
		}
		got, err := a.claimedInWindow(ctx, tx, userID, templateID, w.Start)
		if err != nil {
			return err
		}
		if got >= maxInt(tpl.PerUser, 1) {
			return biz(409, "COUPON_LIMIT", "该券每期每人限领，你已领取")
		}
		c, err = a.issueCoupon(ctx, tx, userID, *tpl, "用户领取", now)
		return err
	})
	return c, err
}
