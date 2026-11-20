// Package bill 是 bill 服务 /internal 接口的小客户端。
// trace: contracts/openapi/bill.yaml getBillFacts、refundOrder
package bill

import (
	"context"
	"net/url"

	"bike/cs/internal/clients/base"
)

// Actor 是操作人。
type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// Fee 是金额各项中本服务用到的字段（分）。
type Fee struct {
	Receivable  int64 `json:"receivable"`
	Payable     int64 `json:"payable"`
	ChannelPaid int64 `json:"channelPaid"`
	Refunded    int64 `json:"refunded"`
	NetReceived int64 `json:"netReceived"`
}

// Facts 是账单事实。
type Facts struct {
	OrderID               string  `json:"orderId"`
	UserID                string  `json:"userId"`
	BilledMinutes         *int64  `json:"billedMinutes"`
	Fee                   *Fee    `json:"fee"`
	PayStatus             *string `json:"payStatus"`
	RefundStatus          *string `json:"refundStatus"`
	RefundFailedPaymentID *string `json:"refundFailedPaymentId"`
	FeeLater              bool    `json:"feeLater"`
	Refundable            int64   `json:"refundable"`
}

// RefundCommand 是退款命令。
type RefundCommand struct {
	OrderID  string `json:"orderId"`
	Amount   int64  `json:"amount"`
	Reason   string `json:"reason"`
	TicketID string `json:"ticketId,omitempty"`
	Actor    Actor  `json:"actor"`
}

// RefundResult 是退款命令结果。
type RefundResult struct {
	Result        string  `json:"result"` // 成功｜失败｜结果未知
	FailCode      *string `json:"failCode"`
	FailReason    *string `json:"failReason"`
	OrderID       string  `json:"orderId"`
	PaymentID     *string `json:"paymentId"`
	Amount        int64   `json:"amount"`
	Refunded      int64   `json:"refunded"`
	RefundStatus  *string `json:"refundStatus"`
	AwaitingPayee bool    `json:"awaitingPayee"`
}

// Client 调 bill。
type Client struct{ c *base.Client }

// New 建客户端。
func New(baseURL, serviceKey string) *Client { return &Client{c: base.New(baseURL, serviceKey)} }

// GetFacts 读账单事实；还没有账单时返回 NOT_FOUND。
func (c *Client) GetFacts(ctx context.Context, orderID string) (*Facts, error) {
	var f Facts
	if err := c.c.Do(ctx, "GET", "/internal/bill/orders/"+url.PathEscape(orderID), "", nil, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// Refund 发退款命令。
func (c *Client) Refund(ctx context.Context, idemKey string, cmd RefundCommand) (*RefundResult, error) {
	var r RefundResult
	if err := c.c.Do(ctx, "POST", "/internal/bill/refunds", idemKey, cmd, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
