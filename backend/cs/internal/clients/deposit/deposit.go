// Package deposit 是 deposit 服务 /internal 接口的小客户端。
// trace: contracts/openapi/deposit.yaml assistRefundDeposit
package deposit

import (
	"context"
	"encoding/json"
	"time"

	"bike/cs/internal/clients/base"
)

// Actor 是操作人。
type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// AssistRefundCommand 是协助退押命令。
type AssistRefundCommand struct {
	UserID   string `json:"userId"`
	Reason   string `json:"reason"`
	Actor    Actor  `json:"actor"`
	TicketID string `json:"ticketId,omitempty"`
}

// Refund 是退押申请。
type Refund struct {
	AppliedAt  time.Time  `json:"appliedAt"`
	StoppedAt  *time.Time `json:"stoppedAt"`
	ExpectedAt time.Time  `json:"expectedAt"`
	Amount     int64      `json:"amount"`
	PaymentID  *string    `json:"paymentId"`
}

// AssistRefundResult 是协助退押结果。
type AssistRefundResult struct {
	Result     string            `json:"result"` // 成功｜失败｜结果未知
	FailCode   *string           `json:"failCode"`
	FailReason *string           `json:"failReason"`
	UserID     string            `json:"userId"`
	Blockers   []json.RawMessage `json:"blockers"`
	Refund     *Refund           `json:"refund"`
	Text       string            `json:"text"`
}

// Client 调 deposit。
type Client struct{ c *base.Client }

// New 建客户端。
func New(baseURL, serviceKey string) *Client { return &Client{c: base.New(baseURL, serviceKey)} }

// AssistRefund 协助退押。
func (c *Client) AssistRefund(ctx context.Context, idemKey string, cmd AssistRefundCommand) (*AssistRefundResult, error) {
	var r AssistRefundResult
	if err := c.c.Do(ctx, "POST", "/internal/deposit/assist-refunds", idemKey, cmd, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
