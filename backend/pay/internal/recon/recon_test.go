package recon

// trace: R-25、R-18；原型 rules.js computeRecon 为对照答案，SCHEMA 4.5、5.6

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"testing"
)

// 原型数据（元）→ 本服务口径（分）
type protoPayment struct {
	PaymentID          string  `json:"paymentId"`
	Type               string  `json:"type"`
	Account            string  `json:"account"`
	Direction          string  `json:"direction"`
	OrderID            *string `json:"orderId"`
	Amount             float64 `json:"amount"`
	Channel            string  `json:"channel"`
	Status             string  `json:"status"`
	CreatedAt          string  `json:"createdAt"`
	CompletedAt        *string `json:"completedAt"`
	ChannelSettledDate *string `json:"channelSettledDate"`
}

type protoItem struct {
	Type      string  `json:"type"`
	Amount    float64 `json:"amount"`
	PaymentID *string `json:"paymentId"`
	Status    string  `json:"status"`
}

type protoRecon struct {
	Date          string `json:"date"`
	ChannelActual struct {
		Wechat, Alipay, Total float64
	} `json:"channelActual"`
	OrderPayments    float64     `json:"orderPayments"`
	DepositDeducted  float64     `json:"depositDeducted"`
	RefundsOriginal  float64     `json:"refundsOriginal"`
	RefundsManual    float64     `json:"refundsManual"`
	OrderNetReceived float64     `json:"orderNetReceived"`
	MonthCardIncome  float64     `json:"monthCardIncome"`
	Expected         float64     `json:"expected"`
	Diff             float64     `json:"diff"`
	DiffItems        []protoItem `json:"diffItems"`
	Status           string      `json:"status"`
}

func fen(y float64) int64 { return int64(math.Round(y * 100)) }

func loadProto(t *testing.T) ([]Flow, []protoRecon) {
	t.Helper()
	b, err := os.ReadFile("testdata/payments.json")
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Payments       []protoPayment `json:"payments"`
		Reconciliation []protoRecon   `json:"reconciliation"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	// 原型文件按创建时刻排列；本服务也按创建时刻、流水号排序
	sort.SliceStable(d.Payments, func(i, j int) bool { return d.Payments[i].CreatedAt < d.Payments[j].CreatedAt })
	var flows []Flow
	for _, p := range d.Payments {
		f := Flow{PaymentID: p.PaymentID, Type: p.Type, Account: p.Account, Direction: p.Direction,
			Channel: p.Channel, Status: p.Status, Amount: fen(p.Amount)}
		if p.OrderID != nil {
			f.OrderID = *p.OrderID
		}
		if p.CompletedAt != nil {
			f.CompletedDate = (*p.CompletedAt)[:10]
		}
		if p.ChannelSettledDate != nil {
			f.SettledDate = *p.ChannelSettledDate
		}
		flows = append(flows, f)
	}
	return flows, d.Reconciliation
}

// TC-PAY-RECON-01：91 天对账逐项与原型答案一致
func TestComputeMatchesPrototype(t *testing.T) {
	flows, answers := loadProto(t)
	if len(answers) < 90 {
		t.Fatalf("原型对账条数异常：%d", len(answers))
	}
	for _, a := range answers {
		r := Compute(flows, a.Date)
		check := func(name string, got int64, want float64) {
			if got != fen(want) {
				t.Errorf("%s %s：得到 %d，应为 %d", a.Date, name, got, fen(want))
			}
		}
		check("wechat", r.ChannelActual.Wechat, a.ChannelActual.Wechat)
		check("alipay", r.ChannelActual.Alipay, a.ChannelActual.Alipay)
		check("total", r.ChannelActual.Total, a.ChannelActual.Total)
		check("orderPayments", r.OrderPayments, a.OrderPayments)
		check("depositDeducted", r.DepositDeducted, a.DepositDeducted)
		check("refundsOriginal", r.RefundsOriginal, a.RefundsOriginal)
		check("refundsManual", r.RefundsManual, a.RefundsManual)
		check("orderNetReceived", r.OrderNetReceived, a.OrderNetReceived)
		check("monthCardIncome", r.MonthCardIncome, a.MonthCardIncome)
		check("expected", r.Expected, a.Expected)
		check("diff", r.Diff, a.Diff)
		if len(r.DiffItems) != len(a.DiffItems) {
			t.Errorf("%s 差异项数：得到 %d，应为 %d", a.Date, len(r.DiffItems), len(a.DiffItems))
			continue
		}
		for i, it := range r.DiffItems {
			w := a.DiffItems[i]
			if it.Type != w.Type || it.Amount != fen(w.Amount) || it.Status != w.Status {
				t.Errorf("%s 差异项 %d：得到 %+v，应为 %+v", a.Date, i, it, w)
			}
			if w.PaymentID != nil && (it.PaymentID == nil || *it.PaymentID != *w.PaymentID) {
				t.Errorf("%s 差异项 %d 流水号不符", a.Date, i)
			}
		}
		if r.Status != a.Status {
			t.Errorf("%s 状态：得到 %s，应为 %s", a.Date, r.Status, a.Status)
		}
	}
}

// TC-PAY-RECON-02：差异 = 各差异项之和；应到账 = 订单实收 + 月卡款 − 押金抵扣
func TestIdentities(t *testing.T) {
	flows, answers := loadProto(t)
	for _, a := range answers {
		r := Compute(flows, a.Date)
		var s int64
		for _, it := range r.DiffItems {
			s += it.Amount
		}
		if s != r.Diff {
			t.Errorf("%s 差异 %d ≠ 差异项之和 %d", a.Date, r.Diff, s)
		}
		if r.Expected != r.OrderNetReceived+r.MonthCardIncome-r.DepositDeducted {
			t.Errorf("%s 应到账恒等式不成立", a.Date)
		}
	}
}

// TC-PAY-RECON-03：2026-11-30 支付宝未到账 ¥25.5，待处理
func TestUnsettledSample(t *testing.T) {
	flows, _ := loadProto(t)
	r := Compute(flows, "2026-11-30")
	if r.Status != "差异待处理" || r.Diff != -2550 {
		t.Fatalf("得到 %s %d", r.Status, r.Diff)
	}
	if r.DiffItems[0].Type != "渠道未到账" || r.DiffItems[0].HandledBy != nil {
		t.Fatalf("差异项 %+v", r.DiffItems[0])
	}
}

// TC-PAY-RECON-04：押金专户的缴纳、退押不进收单对账（反例）
func TestDepositExcluded(t *testing.T) {
	flows := []Flow{
		{PaymentID: "A", Type: "押金缴纳", Account: "押金专户", Direction: "收", Channel: "微信支付", Status: "成功", Amount: 9900, CompletedDate: "2026-12-01"},
		{PaymentID: "B", Type: "退押", Account: "押金专户", Direction: "付", Channel: "微信支付", Status: "成功", Amount: 9900, CompletedDate: "2026-12-01"},
	}
	r := Compute(flows, "2026-12-01")
	if r.Expected != 0 || r.ChannelActual.Total != 0 || len(r.DiffItems) != 0 || r.Status != "平" {
		t.Fatalf("押金不应参与对账：%+v", r)
	}
}

// TC-PAY-RECON-05：跨日到账当日为负、次日为正，处理中与失败的流水不计
func TestCrossDay(t *testing.T) {
	flows := []Flow{
		{PaymentID: "A", Type: "订单支付", Account: "骑行收入户", Direction: "收", Channel: "支付宝", Status: "成功", Amount: 450, CompletedDate: "2026-12-01", SettledDate: "2026-12-02"},
		{PaymentID: "B", Type: "订单扣款", Account: "骑行收入户", Direction: "收", Channel: "微信支付", Status: "失败", Amount: 300},
		{PaymentID: "C", Type: "订单扣款", Account: "骑行收入户", Direction: "收", Channel: "微信支付", Status: "处理中", Amount: 300},
	}
	d1 := Compute(flows, "2026-12-01")
	if d1.Diff != -450 || d1.DiffItems[0].Type != "跨日到账" || d1.DiffItems[0].Status != "已处理" {
		t.Fatalf("当日 %+v", d1)
	}
	d2 := Compute(flows, "2026-12-02")
	if d2.Diff != 450 || d2.Expected != 0 || d2.ChannelActual.Alipay != 450 {
		t.Fatalf("次日 %+v", d2)
	}
}
