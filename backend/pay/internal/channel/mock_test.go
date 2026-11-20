package channel

// trace: R-09、R-34、R-25（模拟渠道：成功、失败、结果未知、渠道未到账、跨日到账）

import (
	"context"
	"testing"
	"time"
)

var bj = time.FixedZone("CST", 8*3600)

func newMock(t *testing.T, cfg MockConfig, now time.Time) *Mock {
	t.Helper()
	m := NewMock(WeChat, cfg, NewMemStore())
	m.Now = func() time.Time { return now }
	return m
}

// TC-PAY-MOCK-01：配置解析
func TestParseMockConfig(t *testing.T) {
	c, err := ParseMockConfig("成功", "U1:失败,U2:跨日", "失败")
	if err != nil || c.Default != BehaveOK || c.Users["U1"] != BehaveFail || c.Users["U2"] != BehaveNextDay || c.UnknownResolve != BehaveFail {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err := ParseMockConfig("乱写", "", ""); err == nil {
		t.Fatal("非法取值应报错")
	}
}

// TC-PAY-MOCK-02：成功收款当日入账单；跨日到账进次日账单；渠道未到账不进账单
func TestMockSettleDates(t *testing.T) {
	now := time.Date(2026, 12, 1, 23, 59, 0, 0, bj)
	m := newMock(t, MockConfig{Default: BehaveOK, Users: map[string]Behavior{"U2": BehaveNextDay, "U3": BehaveUnsettled}, UnknownResolve: BehaveOK}, now)
	ctx := context.Background()
	for _, u := range []string{"U1", "U2", "U3"} {
		r, err := m.Charge(ctx, ChargeReq{Merchant: IncomeMerchant, OutTradeNo: "PY-" + u, UserID: u, Amount: 450})
		if err != nil || r.Outcome != Succeeded || r.TradeNo == "" {
			t.Fatalf("%s %+v %v", u, r, err)
		}
	}
	d1, _ := m.DownloadBill(ctx, IncomeMerchant, "2026-12-01")
	d2, _ := m.DownloadBill(ctx, IncomeMerchant, "2026-12-02")
	if len(d1) != 1 || d1[0].OutNo != "PY-U1" {
		t.Fatalf("当日账单 %+v", d1)
	}
	if len(d2) != 1 || d2[0].OutNo != "PY-U2" {
		t.Fatalf("次日账单 %+v", d2)
	}
	other, _ := m.DownloadBill(ctx, DepositMerchant, "2026-12-01")
	if len(other) != 0 {
		t.Fatal("押金商户号的账单不应含收入商户号的交易")
	}
}

// TC-PAY-MOCK-03：失败带原因；结果未知可经查询补齐
func TestMockFailAndUnknown(t *testing.T) {
	now := time.Date(2026, 12, 1, 10, 0, 0, 0, bj)
	m := newMock(t, MockConfig{Default: BehaveOK, Users: map[string]Behavior{"UF": BehaveFail, "UU": BehaveUnknown}, UnknownResolve: BehaveOK}, now)
	ctx := context.Background()
	r, _ := m.CreditCharge(ctx, CreditChargeReq{OutTradeNo: "P1", UserID: "UF", Amount: 300})
	if r.Outcome != Failed || r.FailReason != "用户支付账户余额不足" {
		t.Fatalf("%+v", r)
	}
	r, _ = m.CreditCharge(ctx, CreditChargeReq{OutTradeNo: "P2", UserID: "UU", Amount: 300})
	if r.Outcome != Unknown {
		t.Fatalf("%+v", r)
	}
	q, _ := m.Query(ctx, QueryReq{Kind: QueryCharge, OutNo: "P2", Credit: true})
	if q.Outcome != Succeeded || q.TradeNo == "" {
		t.Fatalf("查询应补齐 %+v", q)
	}
	if _, err := m.Query(ctx, QueryReq{Kind: QueryCharge, OutNo: "不存在"}); err != nil {
		t.Fatal(err)
	}
	if q, _ := m.Query(ctx, QueryReq{Kind: QueryCharge, OutNo: "不存在"}); q.Outcome != Failed {
		t.Fatalf("渠道无此单应视为失败 %+v", q)
	}
}

// TC-PAY-MOCK-04：仍未知的保持未知（交运维确认）
func TestMockStaysUnknown(t *testing.T) {
	m := newMock(t, MockConfig{Default: BehaveUnknown, UnknownResolve: BehaveUnknown}, time.Date(2026, 12, 1, 10, 0, 0, 0, bj))
	ctx := context.Background()
	m.Charge(ctx, ChargeReq{Merchant: IncomeMerchant, OutTradeNo: "P1", UserID: "U1", Amount: 100})
	if q, _ := m.Query(ctx, QueryReq{Kind: QueryCharge, OutNo: "P1"}); q.Outcome != Unknown {
		t.Fatalf("%+v", q)
	}
}

// TC-PAY-MOCK-05：退款成功进账单为付；原路失败带原因；退押先受理后到账
func TestMockRefund(t *testing.T) {
	now := time.Date(2026, 12, 1, 10, 0, 0, 0, bj)
	m := newMock(t, MockConfig{Default: BehaveOK, Users: map[string]Behavior{"UF": BehaveFail}, UnknownResolve: BehaveOK}, now)
	ctx := context.Background()
	r, _ := m.Refund(ctx, RefundReq{Merchant: IncomeMerchant, OutRefundNo: "R1", UserID: "U1", Amount: 200})
	if r.Outcome != Succeeded {
		t.Fatalf("%+v", r)
	}
	bill, _ := m.DownloadBill(ctx, IncomeMerchant, "2026-12-01")
	if len(bill) != 1 || bill[0].Direction != "付" {
		t.Fatalf("%+v", bill)
	}
	r, _ = m.Refund(ctx, RefundReq{Merchant: IncomeMerchant, OutRefundNo: "R2", UserID: "UF", Amount: 200})
	if r.Outcome != Failed || r.FailReason != "原支付账户已注销，渠道拒绝原路退回" {
		t.Fatalf("%+v", r)
	}
	r, _ = m.Refund(ctx, RefundReq{Merchant: DepositMerchant, OutRefundNo: "R3", UserID: "U1", Amount: 9900})
	if r.Outcome != Unknown {
		t.Fatalf("退押应先受理 %+v", r)
	}
	q, _ := m.Query(ctx, QueryReq{Merchant: DepositMerchant, Kind: QueryRefund, OutNo: "R3"})
	if q.Outcome != Succeeded {
		t.Fatalf("%+v", q)
	}
	r, _ = m.Refund(ctx, RefundReq{Merchant: DepositMerchant, OutRefundNo: "R4", UserID: "UF", Amount: 9900})
	q, _ = m.Query(ctx, QueryReq{Merchant: DepositMerchant, Kind: QueryRefund, OutNo: "R4"})
	if r.Outcome != Unknown || q.Outcome != Failed {
		t.Fatalf("退押原路失败应在查询时给出 %+v %+v", r, q)
	}
}

// TC-PAY-MOCK-06：同一流水号重复收款不重复扣（渠道侧幂等）
func TestMockIdempotent(t *testing.T) {
	m := newMock(t, MockConfig{Default: BehaveOK}, time.Date(2026, 12, 1, 10, 0, 0, 0, bj))
	ctx := context.Background()
	a, _ := m.Charge(ctx, ChargeReq{Merchant: IncomeMerchant, OutTradeNo: "P1", UserID: "U1", Amount: 100})
	b, _ := m.Charge(ctx, ChargeReq{Merchant: IncomeMerchant, OutTradeNo: "P1", UserID: "U1", Amount: 100})
	if a.TradeNo != b.TradeNo {
		t.Fatal("同一流水号应返回同一交易")
	}
	bill, _ := m.DownloadBill(ctx, IncomeMerchant, "2026-12-01")
	if len(bill) != 1 {
		t.Fatalf("%+v", bill)
	}
}

// TC-PAY-MOCK-06：预下单只记待支付交易（未知），按该用户行为在回调（查询）时出结果；行为"未知"的不自动回调
func TestMockPrepay(t *testing.T) {
	now := time.Date(2026, 12, 1, 10, 0, 0, 0, bj)
	m := newMock(t, MockConfig{Default: BehaveOK, Users: map[string]Behavior{"UF": BehaveFail, "UU": BehaveUnknown}, UnknownResolve: BehaveOK}, now)
	ctx := context.Background()
	p, err := m.Prepay(ctx, PrepayReq{Merchant: IncomeMerchant, OutTradeNo: "PY-1", UserID: "U1", Amount: 500})
	if err != nil || p.Mode != "mock" || !p.AutoCallback || p.Outcome != "" || p.Params["prepayId"] == nil {
		t.Fatalf("%+v %v", p, err)
	}
	if b, _ := m.DownloadBill(ctx, IncomeMerchant, "2026-12-01"); len(b) != 0 {
		t.Fatal("未回调前不应出现在账单里")
	}
	r, _ := m.Query(ctx, QueryReq{Merchant: IncomeMerchant, Kind: QueryCharge, OutNo: "PY-1"})
	if r.Outcome != Succeeded || r.TradeNo == "" {
		t.Fatalf("回调后应成功：%+v", r)
	}
	if b, _ := m.DownloadBill(ctx, IncomeMerchant, "2026-12-01"); len(b) != 1 {
		t.Fatal("回调成功后入账单")
	}
	pf, _ := m.Prepay(ctx, PrepayReq{Merchant: IncomeMerchant, OutTradeNo: "PY-2", UserID: "UF", Amount: 500})
	if rf, _ := m.Query(ctx, QueryReq{OutNo: "PY-2"}); !pf.AutoCallback || rf.Outcome != Failed || rf.FailReason == "" {
		t.Fatalf("%+v", rf)
	}
	pu, _ := m.Prepay(ctx, PrepayReq{Merchant: IncomeMerchant, OutTradeNo: "PY-3", UserID: "UU", Amount: 500})
	if pu.AutoCallback {
		t.Fatal("行为未知的用户不应自动回调")
	}
}
