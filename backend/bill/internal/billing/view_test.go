package billing

import (
	"reflect"
	"testing"
)

func TestYuan(t *testing.T) {
	cases := map[int64]string{0: "¥0.0", 450: "¥4.5", 800: "¥8.0", 1300: "¥13.0", 455: "¥4.55", 5: "¥0.05", -300: "-¥3.0"}
	for c, want := range cases {
		if got := Yuan(c); got != want {
			t.Errorf("Yuan(%d)=%s，期望 %s", c, got, want)
		}
	}
}

// SCHEMA 4.1 算例的账单行：骑行费、调度费、月卡抵扣、优惠券、应付；抵扣行为负数
func TestLines_SchemaExample(t *testing.T) {
	v := View{BilledMinutes: 18, RideFee: 900, DispatchFee: 500, MonthCardDeduct: 300, CouponDeduct: 300, CouponName: "绿色出行券", Payable: 800, ChannelPaid: 800, PayStatus: "已支付", PayMethod: "免押自动扣款"}
	got := Lines(v)
	want := []Line{
		{Key: "rideFee", Label: "骑行费（18 分钟）", Amount: 900},
		{Key: "dispatchFee", Label: "调度费", Amount: 500, Note: "站点外还车"},
		{Key: "monthCardDeduct", Label: "月卡抵扣", Amount: -300},
		{Key: "couponDeduct", Label: "优惠券（绿色出行券）", Amount: -300},
		{Key: "payable", Label: "应付", Amount: 800, Total: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("得到 %+v\n期望 %+v", got, want)
	}
	if h := Headline(v); h != "已自动扣款 ¥8.0" {
		t.Errorf("headline=%s", h)
	}
	if s := StatusText(v); s != "已支付" {
		t.Errorf("statusText=%s", s)
	}
}

// 押金抵扣与退款行；金额为 0 的抵扣行不出现
func TestLines_DepositAndRefund(t *testing.T) {
	v := View{BilledMinutes: 29, RideFee: 1190, Payable: 1190, DepositDeducted: 1190, Refunded: 200, PayStatus: "押金抵扣", PayMethod: "押金抵扣", RefundStatus: "部分退款"}
	got := Lines(v)
	keys := []string{}
	for _, l := range got {
		keys = append(keys, l.Key)
	}
	if !reflect.DeepEqual(keys, []string{"rideFee", "payable", "depositDeducted", "refunded"}) {
		t.Fatalf("行顺序不对：%v", keys)
	}
	if got[3].Amount != -200 {
		t.Errorf("已退款行应为负数")
	}
	if n := Notes(v); len(n) != 1 || n[0] != "超过一键支付期限未付，已从押金抵扣 ¥11.9" {
		t.Errorf("notes=%v", n)
	}
	if s := StatusText(v); s != "已部分退款" {
		t.Errorf("statusText=%s", s)
	}
}

func TestHeadlineAndStatus(t *testing.T) {
	cases := []struct {
		v        View
		headline string
		status   string
	}{
		{View{Payable: 1300, PayStatus: "待支付", PayMethod: "一键支付"}, "应付 ¥13.0", "待支付"},
		{View{Payable: 1450, PayStatus: "欠费", PayMethod: "免押自动扣款"}, "欠费 ¥14.5", "欠费"},
		{View{PayStatus: "无需支付"}, "本单无需支付", "无需支付"},
		{View{Payable: 1230, ChannelPaid: 1230, PayStatus: "欠费已补付", PayMethod: "免押自动扣款"}, "已支付 ¥12.3", "已补付"},
		{View{Payable: 930, ChannelPaid: 930, PayStatus: "已支付", PayMethod: "一键支付"}, "已支付 ¥9.3", "已支付"},
		{View{ZeroUnlock: true, PayStatus: "无需支付"}, "本单无需支付", "超时未开锁 · 0 元"},
		{View{Payable: 270, ChannelPaid: 270, Refunded: 270, PayStatus: "已支付", RefundStatus: "全额退款"}, "已支付 ¥2.7", "已全额退款"},
		{View{Payable: 270, ChannelPaid: 270, PayStatus: "已支付", RefundStatus: "原路失败·待人工转账"}, "已支付 ¥2.7", "退款处理中"},
	}
	for i, c := range cases {
		if h := Headline(c.v); h != c.headline {
			t.Errorf("#%d headline=%s，期望 %s", i, h, c.headline)
		}
		if s := StatusText(c.v); s != c.status {
			t.Errorf("#%d status=%s，期望 %s", i, s, c.status)
		}
	}
}

func TestNotes(t *testing.T) {
	d := 230
	v := View{InStation: false, DispatchFee: 500, DistanceToStationM: &d}
	if n := Notes(v); len(n) != 1 || n[0] != "站点外还车，含调度费 ¥5.0（距最近站点 230 米）" {
		t.Errorf("notes=%v", n)
	}
	v = View{InStation: true, ZeroUnlock: true}
	if n := Notes(v); len(n) != 1 || n[0] != "车辆在超时后才开锁，平台已立即结束，本单 0 元" {
		t.Errorf("notes=%v", n)
	}
	if n := Notes(View{InStation: true}); n == nil || len(n) != 0 {
		t.Errorf("无说明时应为空数组而非 nil：%v", n)
	}
}

func TestCanPay(t *testing.T) {
	if !CanPay(View{Payable: 100, PayStatus: "待支付"}, false) {
		t.Error("待支付未付大于 0 应能支付")
	}
	if CanPay(View{Payable: 100, PayStatus: "待支付"}, true) {
		t.Error("有结果未知的扣款时不能支付")
	}
	if CanPay(View{Payable: 100, ChannelPaid: 100, PayStatus: "已支付"}, false) {
		t.Error("已支付不能再付")
	}
}

func TestRefundStatusOf(t *testing.T) {
	cases := []struct {
		a      Amounts
		failed bool
		want   string
	}{
		{Amounts{Payable: 900, ChannelPaid: 900}, false, "无"},
		{Amounts{Payable: 900, ChannelPaid: 900, Refunded: 300}, false, "部分退款"},
		{Amounts{Payable: 900, ChannelPaid: 900, Refunded: 900}, false, "全额退款"},
		{Amounts{Payable: 900, ChannelPaid: 900, Refunded: 300}, true, "原路失败·待人工转账"},
	}
	for _, c := range cases {
		if got := RefundStatusOf(c.a, c.failed); got != c.want {
			t.Errorf("%+v failed=%v 得到 %s，期望 %s", c.a, c.failed, got, c.want)
		}
	}
}
