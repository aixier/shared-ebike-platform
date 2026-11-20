// Package pay 是调用 pay 服务 /internal 命令的小客户端（按 contracts/openapi/pay.yaml 手写，只写用到的三个命令）。
package pay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrUnavailable 连不上、超时或 5xx：结果未知，调用方用同一幂等键重试。
var ErrUnavailable = errors.New("pay 服务不可用")

// APIError pay 返回的 4xx 错误。
type APIError struct {
	Status  int
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string { return fmt.Sprintf("pay %d %s：%s", e.Status, e.Code, e.Message) }

// Actor 操作人。
type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// ChargeCommand 对应 DepositChargeCommand。
type ChargeCommand struct {
	BizNo   string `json:"bizNo"`
	Type    string `json:"type"` // 押金缴纳｜押金补缴
	UserID  string `json:"userId"`
	Amount  int64  `json:"amount"`
	Channel string `json:"channel"` // 微信支付｜支付宝
}

// RefundCommand 对应 DepositRefundCommand。
type RefundCommand struct {
	BizNo              string   `json:"bizNo"`
	UserID             string   `json:"userId"`
	Amount             int64    `json:"amount"`
	OriginalPaymentIDs []string `json:"originalPaymentIds"`
	Actor              *Actor   `json:"actor,omitempty"`
}

// TransferCommand 对应 DepositTransferCommand。
type TransferCommand struct {
	BizNo   string `json:"bizNo"`
	UserID  string `json:"userId"`
	OrderID string `json:"orderId"`
	Amount  int64  `json:"amount"`
}

// Result 对应 PayCommandResult。
type Result struct {
	Result      string     `json:"result"` // 成功｜失败｜结果未知
	FailCode    *string    `json:"failCode"`
	FailReason  *string    `json:"failReason"`
	PaymentID   string     `json:"paymentId"`
	Type        string     `json:"type"`
	Account     string     `json:"account"`
	Amount      int64      `json:"amount"`
	Status      string     `json:"status"`
	CompletedAt *time.Time `json:"completedAt"`
}

// Client pay 客户端。
type Client struct {
	BaseURL string
	Key     string
	HTTP    *http.Client
}

// New 建客户端，默认 3 秒超时（wire.md 第 1 节）。
func New(baseURL, serviceKey string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Key: serviceKey, HTTP: &http.Client{Timeout: 3 * time.Second}}
}

// ChargeDeposit 押金收款。
func (c *Client) ChargeDeposit(ctx context.Context, idemKey string, cmd ChargeCommand) (Result, error) {
	return c.post(ctx, "/internal/pay/deposit-charges", idemKey, cmd)
}

// RefundDeposit 退押。
func (c *Client) RefundDeposit(ctx context.Context, idemKey string, cmd RefundCommand) (Result, error) {
	return c.post(ctx, "/internal/pay/deposit-refunds", idemKey, cmd)
}

// TransferDeposit 专户划转。
func (c *Client) TransferDeposit(ctx context.Context, idemKey string, cmd TransferCommand) (Result, error) {
	return c.post(ctx, "/internal/pay/deposit-transfers", idemKey, cmd)
}

func (c *Client) post(ctx context.Context, path, idemKey string, body any) (Result, error) {
	var res Result
	b, err := json.Marshal(body)
	if err != nil {
		return res, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return res, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("X-Caller", "deposit")
	req.Header.Set("Idempotency-Key", idemKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return res, fmt.Errorf("%w：%v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	switch {
	case resp.StatusCode >= 500:
		return res, fmt.Errorf("%w：HTTP %d", ErrUnavailable, resp.StatusCode)
	case resp.StatusCode >= 400:
		ae := &APIError{Status: resp.StatusCode}
		_ = json.Unmarshal(raw, ae)
		return res, ae
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return res, fmt.Errorf("%w：响应无法解析", ErrUnavailable)
	}
	return res, nil
}
