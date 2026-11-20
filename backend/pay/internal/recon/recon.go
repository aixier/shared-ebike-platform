// Package recon 是每日收单对账的纯算术（指标算术，确定性代码合法范围）。
// 口径：SCHEMA 4.5；对照答案：原型 shared/mock/rules.js computeRecon。
// trace: R-25、R-18、NFR-09
package recon

import "fmt"

// Flow 是参与对账的一笔流水（只取对账需要的字段）。
type Flow struct {
	PaymentID     string
	Type          string
	Account       string
	Direction     string // 收｜付｜转
	Channel       string
	Status        string
	OrderID       string
	Amount        int64  // 分，正数
	CompletedDate string // 完成日（北京时间 YYYY-MM-DD），未完成为空
	SettledDate   string // 渠道到账日，渠道未到账为空
}

// ChannelActual 渠道实到（按到账日汇总的净额）
type ChannelActual struct {
	Wechat int64 `json:"wechat"`
	Alipay int64 `json:"alipay"`
	Total  int64 `json:"total"`
}

// DiffItem 差异项
type DiffItem struct {
	Type      string  `json:"type"`
	Amount    int64   `json:"amount"`
	PaymentID *string `json:"paymentId"`
	OrderID   *string `json:"orderId"`
	Status    string  `json:"status"`
	HandledBy *string `json:"handledBy"`
	Note      *string `json:"note"`
}

// Result 某日对账结果
type Result struct {
	Date             string        `json:"date"`
	ChannelActual    ChannelActual `json:"channelActual"`
	OrderPayments    int64         `json:"orderPayments"`
	DepositDeducted  int64         `json:"depositDeducted"`
	RefundsOriginal  int64         `json:"refundsOriginal"`
	RefundsManual    int64         `json:"refundsManual"`
	OrderNetReceived int64         `json:"orderNetReceived"`
	MonthCardIncome  int64         `json:"monthCardIncome"`
	Expected         int64         `json:"expected"`
	Diff             int64         `json:"diff"`
	DiffItems        []DiffItem    `json:"diffItems"`
	Status           string        `json:"status"`
}

// AutoHandledBy 由流水事实即可解释的差异项（人工转账、跨日到账）的处理人标记。
// 这两类差异是事实匹配的结果，不是判断；渠道未到账一律待财务处理。
const AutoHandledBy = "系统"

func isIncomeIn(t string) bool {
	return t == "订单扣款" || t == "订单支付" || t == "欠费补付"
}

func isChannel(f Flow) bool {
	return f.Account == "骑行收入户" && (f.Channel == "微信支付" || f.Channel == "支付宝")
}

func sign(f Flow) int64 {
	if f.Direction == "收" {
		return 1
	}
	return -1
}

func sp(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Compute 计算 date 当日的收单对账。flows 须按创建时刻、流水号排序，差异项顺序由此确定。
func Compute(flows []Flow, date string) Result {
	r := Result{Date: date, DiffItems: []DiffItem{}}
	auto := AutoHandledBy
	for _, f := range flows {
		if isChannel(f) && f.Status == "成功" && f.SettledDate == date {
			switch f.Channel {
			case "微信支付":
				r.ChannelActual.Wechat += f.Amount * sign(f)
			case "支付宝":
				r.ChannelActual.Alipay += f.Amount * sign(f)
			}
		}
	}
	r.ChannelActual.Total = r.ChannelActual.Wechat + r.ChannelActual.Alipay

	for _, f := range flows {
		if f.Status != "成功" || f.CompletedDate != date {
			continue
		}
		switch {
		case f.Account == "骑行收入户" && isIncomeIn(f.Type):
			r.OrderPayments += f.Amount
		case f.Type == "月卡购买":
			r.MonthCardIncome += f.Amount
		case f.Type == "押金抵扣":
			r.DepositDeducted += f.Amount
		case f.Type == "退款":
			r.RefundsOriginal += f.Amount
		case f.Type == "人工转账退款":
			r.RefundsManual += f.Amount
		}
		switch {
		case f.Type == "人工转账退款":
			note := "银行转账退回，收单渠道无对应流水"
			r.DiffItems = append(r.DiffItems, DiffItem{Type: "人工转账退款不经收单渠道", Amount: f.Amount,
				PaymentID: sp(f.PaymentID), OrderID: sp(f.OrderID), Status: "已处理", HandledBy: &auto, Note: &note})
		case isChannel(f) && f.SettledDate == "":
			note := "平台记录成功，渠道对账单无此笔，待向渠道核实"
			r.DiffItems = append(r.DiffItems, DiffItem{Type: "渠道未到账", Amount: -f.Amount * sign(f),
				PaymentID: sp(f.PaymentID), OrderID: sp(f.OrderID), Status: "待处理", Note: &note})
		case isChannel(f) && f.SettledDate != date:
			note := fmt.Sprintf("渠道于 %s 入账", f.SettledDate)
			r.DiffItems = append(r.DiffItems, DiffItem{Type: "跨日到账", Amount: -f.Amount * sign(f),
				PaymentID: sp(f.PaymentID), OrderID: sp(f.OrderID), Status: "已处理", HandledBy: &auto, Note: &note})
		}
	}
	for _, f := range flows {
		if isChannel(f) && f.Status == "成功" && f.SettledDate == date && f.CompletedDate != "" && f.CompletedDate != date {
			note := fmt.Sprintf("%s 发生、本日入账", f.CompletedDate)
			r.DiffItems = append(r.DiffItems, DiffItem{Type: "跨日到账", Amount: f.Amount * sign(f),
				PaymentID: sp(f.PaymentID), OrderID: sp(f.OrderID), Status: "已处理", HandledBy: &auto, Note: &note})
		}
	}
	r.OrderNetReceived = r.OrderPayments + r.DepositDeducted - r.RefundsOriginal - r.RefundsManual
	r.Expected = r.OrderNetReceived + r.MonthCardIncome - r.DepositDeducted
	r.Diff = r.ChannelActual.Total - r.Expected
	r.Status = Status(r.DiffItems)
	return r
}

// Status 由差异项得出对账状态（不含"当日未出"，那由调用方按出具与否给出）。
func Status(items []DiffItem) string {
	if len(items) == 0 {
		return "平"
	}
	for _, it := range items {
		if it.Status == "待处理" {
			return "差异待处理"
		}
	}
	return "差异已处理"
}
