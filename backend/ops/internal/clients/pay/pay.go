// Package pay 调 pay 的确认收款结果（contracts/openapi/pay.yaml /internal/pay/payments/{paymentId}/confirm）。
package pay

import (
	"context"
	"net/url"

	"bike/ops/internal/clients/svccall"
)

// Actor 操作人。
type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// ConfirmRequest 人工确认的收款结果。
type ConfirmRequest struct {
	Result string `json:"result"` // 已收款｜未收款
	Reason string `json:"reason"`
	Actor  Actor  `json:"actor"`
}

// CommandResult 命令结果。
type CommandResult struct {
	Result     string  `json:"result"` // 成功｜失败｜结果未知
	FailCode   *string `json:"failCode"`
	FailReason *string `json:"failReason"`
	PaymentID  string  `json:"paymentId"`
}

// Client pay 客户端。
type Client struct{ c *svccall.Caller }

// New 构造。
func New(baseURL, serviceKey string) *Client { return &Client{c: svccall.New(baseURL, serviceKey)} }

// ConfirmPayment 确认结果未知的扣款。
func (p *Client) ConfirmPayment(ctx context.Context, paymentID, idemKey string, req ConfirmRequest) (*CommandResult, error) {
	var out CommandResult
	if err := p.c.Post(ctx, "/internal/pay/payments/"+url.PathEscape(paymentID)+"/confirm", idemKey, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
