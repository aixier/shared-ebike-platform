package app

import (
	"context"
	"errors"
	"time"

	"bike/bill/internal/billing"
)

// 对外视图（swagger OrderBill、Bill、Fee、BillSummary、BillFacts）

// Fee 金额各项
type Fee struct {
	RideFee         int64   `json:"rideFee"`
	DispatchFee     int64   `json:"dispatchFee"`
	Receivable      int64   `json:"receivable"`
	MonthCardDeduct int64   `json:"monthCardDeduct"`
	MonthCardID     *string `json:"monthCardId"`
	CouponDeduct    int64   `json:"couponDeduct"`
	CouponID        *string `json:"couponId"`
	Payable         int64   `json:"payable"`
	ChannelPaid     int64   `json:"channelPaid"`
	DepositDeducted int64   `json:"depositDeducted"`
	Unpaid          int64   `json:"unpaid"`
	Refunded        int64   `json:"refunded"`
	NetReceived     int64   `json:"netReceived"`
}

// BillDetail 逐行账单明细与说明
type BillDetail struct {
	Lines         []billing.Line `json:"lines"`
	BilledMinutes int            `json:"billedMinutes"`
	DurationSec   int64          `json:"durationSec"`
	Receivable    int64          `json:"receivable"`
	Payable       int64          `json:"payable"`
	Paid          int64          `json:"paid"`
	Unpaid        int64          `json:"unpaid"`
	Refunded      int64          `json:"refunded"`
	PayStatus     string         `json:"payStatus"`
	PayMethod     *string        `json:"payMethod"`
	PayDeadline   *time.Time     `json:"payDeadline"`
	CanPay        bool           `json:"canPay"`
	CouponName    *string        `json:"couponName"`
	MonthCardID   *string        `json:"monthCardId"`
	Notes         []string       `json:"notes"`
	Headline      string         `json:"headline"`
}

// OrderBill 订单的费用部分
type OrderBill struct {
	OrderID               string      `json:"orderId"`
	UserID                string      `json:"userId,omitempty"`
	PricingVersionID      string      `json:"pricingVersionId"`
	BilledMinutes         *int        `json:"billedMinutes"`
	BillPushedAt          *time.Time  `json:"billPushedAt"`
	Fee                   *Fee        `json:"fee"`
	Bill                  *BillDetail `json:"bill"`
	PayStatus             *string     `json:"payStatus"`
	PayMethod             *string     `json:"payMethod"`
	ArrearsPaidAt         *time.Time  `json:"arrearsPaidAt"`
	RefundStatus          *string     `json:"refundStatus"`
	RefundFailedPaymentID *string     `json:"refundFailedPaymentId"`
	Degraded              bool        `json:"degraded"`
	FeeLater              bool        `json:"feeLater"`
	StatusText            string      `json:"statusText"`
}

func (a *App) tp(x *time.Time) *time.Time {
	if x == nil {
		return nil
	}
	v := a.t(*x)
	return &v
}

func (b *Bill) view() billing.View {
	return billing.View{BilledMinutes: b.BilledMinutes, RideFee: b.RideFee, DispatchFee: b.DispatchFee, MonthCardDeduct: b.MonthCardDeduct,
		CouponDeduct: b.CouponDeduct, CouponName: str(b.CouponName), Payable: b.Payable, ChannelPaid: b.ChannelPaid, DepositDeducted: b.DepositDeducted,
		Refunded: b.Refunded, InStation: b.InStation, DistanceToStationM: b.DistanceToStationM, ZeroUnlock: b.ZeroUnlock, PayStatus: b.PayStatus,
		PayMethod: str(b.PayMethod), RefundStatus: b.refundStatus()}
}

func (b *Bill) refundStatus() string {
	return billing.RefundStatusOf(b.amounts(), b.RefundFailedPaymentID != nil)
}

func (b *Bill) fee() *Fee {
	am := b.amounts()
	return &Fee{RideFee: b.RideFee, DispatchFee: b.DispatchFee, Receivable: b.Receivable, MonthCardDeduct: b.MonthCardDeduct, MonthCardID: b.MonthCardID,
		CouponDeduct: b.CouponDeduct, CouponID: b.CouponID, Payable: b.Payable, ChannelPaid: b.ChannelPaid, DepositDeducted: b.DepositDeducted,
		Unpaid: am.Unpaid(), Refunded: b.Refunded, NetReceived: am.NetReceived()}
}

func (a *App) orderBill(b *Bill, admin bool) OrderBill {
	v := b.view()
	am := b.amounts()
	var deadline *time.Time
	if b.PayStatus == stPending && b.CollectBy == byOneTap {
		deadline = a.tp(b.PayDeadline)
	}
	var couponName *string
	if b.CouponDeduct > 0 {
		couponName = b.CouponName
	}
	detail := &BillDetail{Lines: billing.Lines(v), BilledMinutes: b.BilledMinutes, DurationSec: b.DurationSec, Receivable: b.Receivable, Payable: b.Payable,
		Paid: b.ChannelPaid + b.DepositDeducted, Unpaid: am.Unpaid(), Refunded: b.Refunded, PayStatus: b.PayStatus, PayMethod: b.PayMethod, PayDeadline: deadline,
		CanPay: billing.CanPay(v, b.pending() && !b.hasIntent()), CouponName: couponName, MonthCardID: b.MonthCardID, Notes: billing.Notes(v), Headline: billing.Headline(v)}
	rs := v.RefundStatus
	bm := b.BilledMinutes
	ob := OrderBill{OrderID: b.OrderID, PricingVersionID: b.PricingVersionID, BilledMinutes: &bm, BillPushedAt: a.tp(&b.BillPushedAt),
		Fee: b.fee(), Bill: detail, PayStatus: &b.PayStatus, PayMethod: b.PayMethod, ArrearsPaidAt: a.tp(b.ArrearsPaidAt), RefundStatus: &rs,
		RefundFailedPaymentID: b.RefundFailedPaymentID, Degraded: b.Degraded, StatusText: billing.StatusText(v)}
	if admin {
		ob.UserID = b.UserID
	}
	return ob
}

// BillFacts 账单事实（cs 读）
type BillFacts struct {
	OrderID               string     `json:"orderId"`
	UserID                string     `json:"userId"`
	PricingVersionID      string     `json:"pricingVersionId"`
	BilledMinutes         *int       `json:"billedMinutes"`
	BillPushedAt          *time.Time `json:"billPushedAt"`
	PayDeadline           *time.Time `json:"payDeadline"`
	Fee                   *Fee       `json:"fee"`
	PayStatus             *string    `json:"payStatus"`
	PayMethod             *string    `json:"payMethod"`
	RefundStatus          *string    `json:"refundStatus"`
	RefundFailedPaymentID *string    `json:"refundFailedPaymentId"`
	Degraded              bool       `json:"degraded"`
	FeeLater              bool       `json:"feeLater"`
	Refundable            int64      `json:"refundable"`
}

func (a *App) billFacts(ctx context.Context, b *Bill) (BillFacts, error) {
	ob := a.orderBill(b, true)
	inflight, err := inflightRefunds(ctx, a.Pool, b.BillID)
	if err != nil {
		return BillFacts{}, err
	}
	refundable := b.amounts().Refundable() - inflight
	if refundable < 0 {
		refundable = 0
	}
	return BillFacts{OrderID: b.OrderID, UserID: b.UserID, PricingVersionID: b.PricingVersionID, BilledMinutes: ob.BilledMinutes, BillPushedAt: ob.BillPushedAt,
		PayDeadline: ob.Bill.PayDeadline, Fee: ob.Fee, PayStatus: ob.PayStatus, PayMethod: ob.PayMethod, RefundStatus: ob.RefundStatus,
		RefundFailedPaymentID: b.RefundFailedPaymentID, Degraded: b.Degraded, Refundable: refundable}, nil
}

// ActiveMonthCard 当前生效的月卡
type ActiveMonthCard struct {
	CardID      string    `json:"cardId"`
	PerOrderCap int64     `json:"perOrderCap"`
	StartAt     time.Time `json:"startAt"`
	EndAt       time.Time `json:"endAt"`
	Status      string    `json:"status"`
}

// BillSummary 账户摘要
type BillSummary struct {
	UserID            string           `json:"userId"`
	Arrears           int64            `json:"arrears"`
	ArrearsOrderIDs   []string         `json:"arrearsOrderIds"`
	UnpaidOrderIDs    []string         `json:"unpaidOrderIds"`
	MonthCard         *ActiveMonthCard `json:"monthCard"`
	UsableCouponCount int              `json:"usableCouponCount"`
}

func (a *App) summary(ctx context.Context, userID string) (BillSummary, error) {
	s := BillSummary{UserID: userID, ArrearsOrderIDs: []string{}, UnpaidOrderIDs: []string{}}
	bills, err := listBills(ctx, a.Pool, `user_id=$1 AND pay_status IN ($2,$3) ORDER BY bill_pushed_at`, userID, stArrears, stPending)
	if err != nil {
		return s, err
	}
	for _, b := range bills {
		if b.PayStatus == stArrears {
			s.Arrears += b.unpaid()
			s.ArrearsOrderIDs = append(s.ArrearsOrderIDs, b.OrderID)
		} else if b.CollectBy == byOneTap {
			s.UnpaidOrderIDs = append(s.UnpaidOrderIDs, b.OrderID)
		}
	}
	now := a.Now()
	cards, err := a.listCards(ctx, a.Pool, `user_id=$1 AND state=$2 AND start_at <= $3 AND end_at > $3 ORDER BY start_at LIMIT 1`, userID, cardPaid, now)
	if err != nil {
		return s, err
	}
	if len(cards) > 0 {
		c := cards[0]
		s.MonthCard = &ActiveMonthCard{CardID: c.CardID, PerOrderCap: c.PerOrderCap, StartAt: c.StartAt, EndAt: c.EndAt, Status: c.Status}
	}
	if err := a.Pool.QueryRow(ctx, `SELECT count(*) FROM coupons WHERE user_id=$1 AND used_order_id IS NULL AND expire_at >= $2 AND issued_at <= $2`, userID, now).Scan(&s.UsableCouponCount); err != nil {
		return s, err
	}
	return s, nil
}

// userKnown bill 是否知道这个用户
func (a *App) userKnown(ctx context.Context, userID string) (bool, error) {
	var ok bool
	err := a.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_profiles WHERE user_id=$1) OR EXISTS(SELECT 1 FROM bills WHERE user_id=$1)
		OR EXISTS(SELECT 1 FROM month_cards WHERE user_id=$1) OR EXISTS(SELECT 1 FROM coupons WHERE user_id=$1)`, userID).Scan(&ok)
	return ok, err
}

// RideEstimate 骑行中实时估算
type RideEstimate struct {
	OrderID             string    `json:"orderId"`
	PricingVersionID    string    `json:"pricingVersionId"`
	AsOf                time.Time `json:"asOf"`
	ElapsedSec          int64     `json:"elapsedSec"`
	LiveBilledMinutes   int       `json:"liveBilledMinutes"`
	LiveRideFee         int64     `json:"liveRideFee"`
	LiveMonthCardDeduct int64     `json:"liveMonthCardDeduct"`
	LiveEstimate        int64     `json:"liveEstimate"`
}

func (a *App) estimate(ctx context.Context, userID, orderID string, startAt time.Time, versionID string) (*RideEstimate, error) {
	v, err := getVersion(ctx, a.Pool, versionID)
	if errors.Is(err, errNotFound) {
		return nil, biz(404, "NOT_FOUND", "没有找到该规则版本")
	}
	if err != nil {
		return nil, err
	}
	now := a.Now()
	sec := int64(now.Sub(startAt).Seconds())
	if sec < 0 {
		sec = 0
	}
	mins := billing.BilledMinutes(sec)
	ride := billing.RideFee(v.Ride, mins)
	cards, err := paidCards(ctx, a.Pool, userID)
	if err != nil {
		return nil, err
	}
	var cd int64
	if c := billing.CardFor(cards, startAt); c != nil {
		cd = c.PerOrderCap
		if ride < cd {
			cd = ride
		}
	}
	return &RideEstimate{OrderID: orderID, PricingVersionID: versionID, AsOf: a.t(now), ElapsedSec: sec, LiveBilledMinutes: mins, LiveRideFee: ride,
		LiveMonthCardDeduct: cd, LiveEstimate: ride - cd}, nil
}
