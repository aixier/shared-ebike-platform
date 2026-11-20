// Package channel 是收单渠道适配（外部系统适配，确定性代码合法范围）。
// 本服务只通过 Provider 接口与微信支付、支付宝通信；具体实现有联调用的模拟渠道与真实渠道适配。
// trace: R-09、R-18、BR-06、R-34、R-25；ADR 2.3、3.1
package channel

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Outcome 渠道调用结果
type Outcome string

const (
	Succeeded Outcome = "成功"
	Failed    Outcome = "失败"
	Unknown   Outcome = "未知" // 超时、返回不明确、已受理未到账
)

// Merchant 收款通道（R-18 两个商户号）
type Merchant string

const (
	IncomeMerchant  Merchant = "收入" // 收入商户号 → 骑行收入户
	DepositMerchant Merchant = "押金" // 押金商户号 → 押金专户
)

// 渠道名，与契约 CollectChannel 取值一致
const (
	WeChat = "微信支付"
	Alipay = "支付宝"
)

// Result 渠道返回
type Result struct {
	Outcome     Outcome
	TradeNo     string // 渠道交易号（退款为渠道退款单号）
	FailReason  string // 失败时一句原因，可直接展示
	CompletedAt time.Time
}

// ChargeReq 一键支付、押金收款
type ChargeReq struct {
	Merchant    Merchant
	OutTradeNo  string // 本服务流水号
	UserID      string
	Amount      int64
	Description string
}

// PrepayReq 一键支付第一步：预下单，换取前端调起支付的参数
type PrepayReq struct {
	Merchant    Merchant
	OutTradeNo  string // 本服务流水号
	UserID      string
	OpenID      string // 付款人 openid（微信支付必填）
	Amount      int64
	Description string
	ExpiresAt   time.Time
}

// PrepayResult 预下单结果。Outcome 为空表示已受理、待用户支付；为 Failed 表示渠道直接拒绝。
type PrepayResult struct {
	Outcome      Outcome
	FailReason   string
	Params       map[string]any // 调起参数，字段名以渠道官方为准
	Mode         string         // mock｜real
	AutoCallback bool           // 模拟渠道：稍后会自动模拟渠道回调
}

// CreditChargeReq 免押后扣款（微信支付分、芝麻信用）
type CreditChargeReq struct {
	OutTradeNo string
	UserID     string
	OrderID    string
	Amount     int64
}

// RefundReq 原路退回
type RefundReq struct {
	Merchant       Merchant
	OutRefundNo    string // 本服务退款流水号
	OrigOutTradeNo string // 原收款流水号
	OrigTradeNo    string // 原渠道交易号
	OrigAmount     int64
	Amount         int64
	UserID         string
	Reason         string
}

// QueryKind 查询对象
type QueryKind string

const (
	QueryCharge QueryKind = "收款"
	QueryRefund QueryKind = "退款"
)

// QueryReq 向渠道查询结果
type QueryReq struct {
	Merchant Merchant
	Kind     QueryKind
	OutNo    string // 本服务流水号
	Credit   bool   // 是否免押后扣款
}

// BillLine 渠道对账单的一行
type BillLine struct {
	OutNo       string // 本服务流水号
	TradeNo     string
	Direction   string // 收｜付
	Amount      int64
	SettledDate string // 到账日 YYYY-MM-DD
}

// Provider 一个渠道（微信支付或支付宝）的全部能力
type Provider interface {
	Charge(ctx context.Context, req ChargeReq) (Result, error)
	Prepay(ctx context.Context, req PrepayReq) (PrepayResult, error)
	CreditCharge(ctx context.Context, req CreditChargeReq) (Result, error)
	Refund(ctx context.Context, req RefundReq) (Result, error)
	Query(ctx context.Context, req QueryReq) (Result, error)
	DownloadBill(ctx context.Context, m Merchant, date string) ([]BillLine, error)
}

// ErrNotConfigured 真实渠道缺商户配置
var ErrNotConfigured = errors.New("渠道未配置商户号")

// Router 按渠道名选实现
type Router map[string]Provider

// Get 取渠道
func (r Router) Get(name string) (Provider, error) {
	p, ok := r[name]
	if !ok || p == nil {
		return nil, fmt.Errorf("不支持的渠道 %q", name)
	}
	return p, nil
}
