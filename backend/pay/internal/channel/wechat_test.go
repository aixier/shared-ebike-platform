package channel

// trace: R-25（微信支付交易账单解析，未对真实账单验证，样例按官方格式手写）

import "testing"

// TC-PAY-WX-01：交易账单 CSV 解析：成功收款为收、退款为付，金额元转分，汇总行忽略
func TestParseWeChatTradeBill(t *testing.T) {
	csv := "交易时间,公众账号ID,商户号,特约商户号,设备号,微信订单号,商户订单号,用户标识,交易类型,交易状态,付款银行,货币种类,应结订单金额,代金券金额,微信退款单号,商户退款单号,退款金额,充值券退款金额,退款类型,退款状态,商品名称,商户数据包,手续费,费率,订单金额,申请退款金额,费率备注\n" +
		"`2026-12-01 10:00:00,`wx1,`190000,`0,`,`4200000001,`PY20261201100000001,`o1,`JSAPI,`SUCCESS,`OTHERS,`CNY,`4.50,`0.00,`0,`0,`0.00,`0.00,`,`,`骑行费,`,`0.03,`0.60%,`4.50,`0.00,`\n" +
		"`2026-12-01 11:00:00,`wx1,`190000,`0,`,`4200000001,`PY20261201100000001,`o1,`JSAPI,`REFUND,`OTHERS,`CNY,`0.00,`0.00,`5030000001,`PY20261201110000002,`2.00,`0.00,`ORIGINAL,`SUCCESS,`骑行费,`,`-0.01,`0.60%,`0.00,`2.00,`\n" +
		"总交易单数,应结订单总金额,退款总金额,充值券退款总金额,手续费总金额,订单总金额,申请退款总金额\n" +
		"`2,`4.50,`2.00,`0.00,`0.02,`4.50,`2.00\n"
	lines, err := ParseWeChatTradeBill([]byte(csv), "2026-12-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("%+v", lines)
	}
	if lines[0].OutNo != "PY20261201100000001" || lines[0].Direction != "收" || lines[0].Amount != 450 {
		t.Fatalf("%+v", lines[0])
	}
	if lines[1].OutNo != "PY20261201110000002" || lines[1].Direction != "付" || lines[1].Amount != 200 || lines[1].TradeNo != "5030000001" {
		t.Fatalf("%+v", lines[1])
	}
}

// TC-PAY-WX-02：未配置商户号时真实渠道不可用（反例：不得假装成功）
func TestWeChatNotConfigured(t *testing.T) {
	if _, err := NewWeChatPay(WeChatConfig{}); err == nil {
		t.Fatal("缺商户配置应报错")
	}
}

// TC-PAY-WX-03：金额"元"字符串转分
func TestYuanToFen(t *testing.T) {
	for s, want := range map[string]int64{"4.50": 450, "0.01": 1, "12": 1200, "-0.01": -1, "99.9": 9990} {
		got, err := yuanToFen(s)
		if err != nil || got != want {
			t.Errorf("%s → %d %v", s, got, err)
		}
	}
	if _, err := yuanToFen("abc"); err == nil {
		t.Error("非法金额应报错")
	}
}
