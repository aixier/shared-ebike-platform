package billing

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

var cst = time.FixedZone("CST", 8*3600)

func tm(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", s, cst)
	if err != nil {
		panic(err)
	}
	return t
}

// PR0001：起步 1.5 元含 3 分钟，0.4 元/分钟，调度费 3 元
var pr1 = Version{VersionID: "PR0001", Ride: RideRule{StartFee: 150, StartMinutes: 3, PerMinute: 40}, DispatchFee: 300}

// PR0002：0.5 元/分钟，调度费 5 元
var pr2 = Version{VersionID: "PR0002", Ride: RideRule{StartFee: 150, StartMinutes: 3, PerMinute: 50}, DispatchFee: 500}

func TestBilledMinutes(t *testing.T) {
	cases := []struct {
		sec  int64
		want int
	}{{0, 1}, {1, 1}, {59, 1}, {60, 1}, {61, 2}, {1080, 18}, {1081, 19}, {1148, 20}, {-5, 1}}
	for _, c := range cases {
		if got := BilledMinutes(c.sec); got != c.want {
			t.Errorf("BilledMinutes(%d)=%d，期望 %d", c.sec, got, c.want)
		}
	}
}

func TestRideFee(t *testing.T) {
	cases := []struct {
		r    RideRule
		min  int
		want int64
	}{
		{pr1.Ride, 1, 150}, {pr1.Ride, 3, 150}, {pr1.Ride, 4, 190}, {pr1.Ride, 20, 830},
		{pr2.Ride, 18, 900}, {pr2.Ride, 26, 1300},
	}
	for _, c := range cases {
		if got := RideFee(c.r, c.min); got != c.want {
			t.Errorf("RideFee(%v,%d)=%d，期望 %d", c.r, c.min, got, c.want)
		}
	}
}

// SCHEMA 4.1 算例：PR0002，站外还车 18 分钟，月卡 3 元，绿色出行券 3 元，应付 8 元
func TestPrice_SchemaExample(t *testing.T) {
	card := &Card{CardID: "MC1", StartAt: tm("2026-11-01 00:00:00"), EndAt: tm("2026-12-01 00:00:00"), PerOrderCap: 300}
	cps := []Coupon{{CouponID: "CP1", Name: "绿色出行券", Amount: 300, IssuedAt: tm("2026-11-01 00:00:00"), ExpireAt: tm("2026-11-14 23:59:59")}}
	r := Price(Input{Version: pr2, DurationSec: 18 * 60, InStation: false, Card: card, Coupons: cps, BillAt: tm("2026-11-10 08:00:00")})
	want := Result{BilledMinutes: 18, RideFee: 900, DispatchFee: 500, Receivable: 1400, MonthCardID: "MC1", MonthCardDeduct: 300, CouponID: "CP1", CouponName: "绿色出行券", CouponDeduct: 300, Payable: 800}
	if r != want {
		t.Fatalf("得到 %+v\n期望 %+v", r, want)
	}
}

// 月卡只抵骑行费：骑行费低于上限时只抵骑行费
func TestPrice_CardCappedByRideFee(t *testing.T) {
	card := &Card{CardID: "MC1", PerOrderCap: 300}
	r := Price(Input{Version: pr2, DurationSec: 60, InStation: false, Card: card})
	if r.MonthCardDeduct != 150 || r.Payable != 500 {
		t.Fatalf("月卡应只抵骑行费 150，应付 500（调度费），得到 %+v", r)
	}
}

// 券可抵调度费，券面额大于剩余时只抵剩余
func TestPrice_CouponCoversDispatchAndCapped(t *testing.T) {
	card := &Card{CardID: "MC1", PerOrderCap: 300}
	cps := []Coupon{{CouponID: "CP1", Amount: 1000, IssuedAt: tm("2026-11-01 00:00:00"), ExpireAt: tm("2026-11-30 23:59:59")}}
	r := Price(Input{Version: pr2, DurationSec: 60, InStation: false, Card: card, Coupons: cps, BillAt: tm("2026-11-02 00:00:00")})
	if r.CouponDeduct != 500 || r.Payable != 0 {
		t.Fatalf("券应抵 500 调度费，应付 0，得到 %+v", r)
	}
}

// BR-07：抵扣额最大的一张；同额选先到期；过期、未发放、已用的不选
func TestChooseCoupon(t *testing.T) {
	at := tm("2026-11-10 12:00:00")
	cps := []Coupon{
		{CouponID: "A", Amount: 200, IssuedAt: tm("2026-11-01 00:00:00"), ExpireAt: tm("2026-11-20 23:59:59")},
		{CouponID: "B", Amount: 300, IssuedAt: tm("2026-11-01 00:00:00"), ExpireAt: tm("2026-11-25 23:59:59")},
		{CouponID: "C", Amount: 300, IssuedAt: tm("2026-11-01 00:00:00"), ExpireAt: tm("2026-11-15 23:59:59")},
		{CouponID: "D", Amount: 900, IssuedAt: tm("2026-11-01 00:00:00"), ExpireAt: tm("2026-11-09 23:59:59")}, // 已过期
		{CouponID: "E", Amount: 900, IssuedAt: tm("2026-11-11 00:00:00"), ExpireAt: tm("2026-11-30 23:59:59")}, // 尚未发放
		{CouponID: "F", Amount: 900, IssuedAt: tm("2026-11-01 00:00:00"), ExpireAt: tm("2026-11-30 23:59:59"), Used: true},
	}
	if c := ChooseCoupon(cps, at, 1000); c == nil || c.CouponID != "C" {
		t.Fatalf("同额 300 应选先到期的 C，得到 %+v", c)
	}
	// 剩余只有 200 时，A、B、C 抵扣额都是 200，选最先到期的 C
	if c := ChooseCoupon(cps, at, 200); c == nil || c.CouponID != "C" {
		t.Fatalf("抵扣额同为 200 应选先到期的 C，得到 %+v", c)
	}
	if c := ChooseCoupon(cps, at, 0); c != nil {
		t.Fatalf("剩余为 0 不应选券，得到 %+v", c)
	}
	// 到期时刻当刻仍可用
	if c := ChooseCoupon(cps[:1], tm("2026-11-20 23:59:59"), 100); c == nil {
		t.Fatal("到期时刻当刻应仍可用")
	}
}

// 月卡生效区间：card.startAt ≤ 订单 startAt < card.endAt
func TestCardFor(t *testing.T) {
	cards := []Card{
		{CardID: "M1", StartAt: tm("2026-10-01 00:00:00"), EndAt: tm("2026-10-31 00:00:00")},
		{CardID: "M2", StartAt: tm("2026-10-31 00:00:00"), EndAt: tm("2026-11-30 00:00:00")},
	}
	if c := CardFor(cards, tm("2026-10-31 00:00:00")); c == nil || c.CardID != "M2" {
		t.Fatalf("边界时刻应属于后一张，得到 %+v", c)
	}
	if c := CardFor(cards, tm("2026-10-01 00:00:00")); c == nil || c.CardID != "M1" {
		t.Fatalf("起始时刻应属于 M1，得到 %+v", c)
	}
	if c := CardFor(cards, tm("2026-11-30 00:00:00")); c != nil {
		t.Fatalf("结束时刻不在区间内，得到 %+v", c)
	}
}

// BR-03：超时后才报开锁成功，整单 0 元，计费分钟 0，不用月卡与券
func TestPrice_Zero(t *testing.T) {
	card := &Card{CardID: "MC1", PerOrderCap: 300}
	cps := []Coupon{{CouponID: "CP1", Amount: 300, IssuedAt: tm("2026-11-01 00:00:00"), ExpireAt: tm("2026-11-30 23:59:59")}}
	r := Price(Input{Version: pr2, DurationSec: 600, InStation: false, Card: card, Coupons: cps, BillAt: tm("2026-11-02 00:00:00"), Zero: true})
	if r != (Result{}) {
		t.Fatalf("应全为 0，得到 %+v", r)
	}
}

func TestVersionAt(t *testing.T) {
	vs := []Version{
		{VersionID: "PR0002", EffectiveAt: tm("2026-10-15 00:00:00")},
		{VersionID: "PR0001", EffectiveAt: tm("2026-07-01 00:00:00")},
		{VersionID: "PR0009", EffectiveAt: tm("2026-11-01 00:00:00"), Revoked: true},
		{VersionID: "PR0003", EffectiveAt: tm("2026-12-01 00:00:00")},
	}
	cases := map[string]string{
		"2026-10-14 23:59:59": "PR0001",
		"2026-10-15 00:00:00": "PR0002",
		"2026-11-20 00:00:00": "PR0002",
		"2026-12-01 00:00:01": "PR0003",
	}
	for at, want := range cases {
		v := VersionAt(vs, tm(at))
		if v == nil || v.VersionID != want {
			t.Errorf("%s 应取 %s，得到 %+v", at, want, v)
		}
	}
	if VersionAt(vs, tm("2026-01-01 00:00:00")) != nil {
		t.Error("最早版本生效之前应没有版本")
	}
}

// 恒等式：应付 = 渠道已收 + 押金抵扣 + 未付；订单实收 = 渠道已收 + 押金抵扣 − 已退款
func TestAmountIdentities(t *testing.T) {
	cases := []Amounts{
		{Payable: 800, ChannelPaid: 800},
		{Payable: 1190, DepositDeducted: 1190},
		{Payable: 1450, DepositDeducted: 600},
		{Payable: 930, ChannelPaid: 930, Refunded: 300},
		{Payable: 1300},
		{},
	}
	for _, a := range cases {
		if a.Payable != a.ChannelPaid+a.DepositDeducted+a.Unpaid() {
			t.Errorf("应付恒等式不成立：%+v", a)
		}
		if a.NetReceived() != a.ChannelPaid+a.DepositDeducted-a.Refunded {
			t.Errorf("实收恒等式不成立：%+v", a)
		}
	}
	if (Amounts{Payable: 1450, DepositDeducted: 600}).Unpaid() != 850 {
		t.Error("押金抵扣不足时未付应为 850")
	}
}

// 用原型 709 个已结束订单回放计价，逐项核对
type fixture struct {
	Versions []struct {
		VersionID   string    `json:"versionId"`
		EffectiveAt time.Time `json:"effectiveAt"`
		Ride        RideRule  `json:"ride"`
		DispatchFee int64     `json:"dispatchFee"`
	} `json:"versions"`
	Cases []struct {
		OrderID          string    `json:"orderId"`
		PricingVersionID string    `json:"pricingVersionId"`
		StartAt          time.Time `json:"startAt"`
		BillPushedAt     time.Time `json:"billPushedAt"`
		DurationSec      int64     `json:"durationSec"`
		InStation        bool      `json:"inStation"`
		Zero             bool      `json:"zero"`
		Cards            []struct {
			CardID      string    `json:"cardId"`
			StartAt     time.Time `json:"startAt"`
			EndAt       time.Time `json:"endAt"`
			PerOrderCap int64     `json:"perOrderCap"`
		} `json:"cards"`
		Coupons []struct {
			CouponID string    `json:"couponId"`
			Name     string    `json:"name"`
			Amount   int64     `json:"amount"`
			IssuedAt time.Time `json:"issuedAt"`
			ExpireAt time.Time `json:"expireAt"`
		} `json:"coupons"`
		Expect struct {
			BilledMinutes   int     `json:"billedMinutes"`
			RideFee         int64   `json:"rideFee"`
			DispatchFee     int64   `json:"dispatchFee"`
			Receivable      int64   `json:"receivable"`
			MonthCardID     *string `json:"monthCardId"`
			MonthCardDeduct int64   `json:"monthCardDeduct"`
			CouponID        *string `json:"couponId"`
			CouponDeduct    int64   `json:"couponDeduct"`
			Payable         int64   `json:"payable"`
			ChannelPaid     int64   `json:"channelPaid"`
			DepositDeducted int64   `json:"depositDeducted"`
			Refunded        int64   `json:"refunded"`
			Unpaid          int64   `json:"unpaid"`
			NetReceived     int64   `json:"netReceived"`
		} `json:"expect"`
	} `json:"cases"`
}

func TestPrice_PrototypeReplay(t *testing.T) {
	raw, err := os.ReadFile("testdata/prototype_orders.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx fixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	vs := map[string]Version{}
	for _, v := range fx.Versions {
		vs[v.VersionID] = Version{VersionID: v.VersionID, EffectiveAt: v.EffectiveAt, Ride: v.Ride, DispatchFee: v.DispatchFee}
	}
	if len(fx.Cases) < 700 {
		t.Fatalf("样例太少：%d", len(fx.Cases))
	}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	for _, c := range fx.Cases {
		var cards []Card
		for _, k := range c.Cards {
			cards = append(cards, Card{CardID: k.CardID, StartAt: k.StartAt, EndAt: k.EndAt, PerOrderCap: k.PerOrderCap})
		}
		var cps []Coupon
		for _, k := range c.Coupons {
			cps = append(cps, Coupon{CouponID: k.CouponID, Name: k.Name, Amount: k.Amount, IssuedAt: k.IssuedAt, ExpireAt: k.ExpireAt})
		}
		r := Price(Input{Version: vs[c.PricingVersionID], DurationSec: c.DurationSec, InStation: c.InStation, Card: CardFor(cards, c.StartAt), Coupons: cps, BillAt: c.BillPushedAt, Zero: c.Zero})
		e := c.Expect
		if r.BilledMinutes != e.BilledMinutes || r.RideFee != e.RideFee || r.DispatchFee != e.DispatchFee || r.Receivable != e.Receivable ||
			r.MonthCardID != str(e.MonthCardID) || r.MonthCardDeduct != e.MonthCardDeduct || r.CouponID != str(e.CouponID) ||
			r.CouponDeduct != e.CouponDeduct || r.Payable != e.Payable {
			t.Errorf("%s 计价不一致：得到 %+v，期望 %+v", c.OrderID, r, e)
			continue
		}
		a := Amounts{Payable: r.Payable, ChannelPaid: e.ChannelPaid, DepositDeducted: e.DepositDeducted, Refunded: e.Refunded}
		if a.Unpaid() != e.Unpaid || a.NetReceived() != e.NetReceived {
			t.Errorf("%s 恒等式与原型不一致：未付 %d/%d 实收 %d/%d", c.OrderID, a.Unpaid(), e.Unpaid, a.NetReceived(), e.NetReceived)
		}
	}
}
