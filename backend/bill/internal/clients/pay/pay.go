// Package pay 是 bill 调 pay 的小客户端，只写用到的三条 /internal 命令（contracts/openapi/pay.yaml）。
// 一键支付两步化（第二轮变更单）：发起支付返回调起参数 payParams，结果以 pay.payment.succeeded 事件为准。
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

// Actor 操作人
type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// CreditChargeCommand 免押扣款
type CreditChargeCommand struct {
	BizNo   string `json:"bizNo"`
	UserID  string `json:"userId"`
	OrderID string `json:"orderId"`
	Amount  int64  `json:"amount"`
	Channel string `json:"channel"`
}

// OneTapPaymentCommand 发起一键支付
type OneTapPaymentCommand struct {
	BizNo   string `json:"bizNo"`
	Type    string `json:"type"` // 订单支付｜欠费补付｜月卡购买
	UserID  string `json:"userId"`
	OrderID string `json:"orderId,omitempty"`
	CardID  string `json:"cardId,omitempty"`
	Amount  int64  `json:"amount"`
	Channel string `json:"channel"`
}

// RefundCommand 退款
type RefundCommand struct {
	UserID   string `json:"userId"`
	OrderID  string `json:"orderId"`
	Amount   int64  `json:"amount"`
	TicketID string `json:"ticketId,omitempty"`
	Actor    *Actor `json:"actor,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// StatusError pay 返回了非 200
type StatusError struct {
	Path   string
	Status int
	Body   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("pay %s 返回 %d：%s", e.Path, e.Status, e.Body)
}

// Result 模块间命令的结果
type Result struct {
	Result      string     `json:"result"` // 成功｜失败｜结果未知
	FailCode    *string    `json:"failCode"`
	FailReason  *string    `json:"failReason"`
	PaymentID   string     `json:"paymentId"`
	Type        string     `json:"type"`
	Amount      int64      `json:"amount"`
	Status      string     `json:"status"`
	CompletedAt *time.Time `json:"completedAt"`
	// PayParams 调起参数（微信、支付宝收银台用），原样透传；没有为空
	PayParams json.RawMessage `json:"payParams,omitempty"`
}

// HasPayParams 是否带调起参数
func (r Result) HasPayParams() bool {
	s := strings.TrimSpace(string(r.PayParams))
	return s != "" && s != "null"
}

// Reason 失败原因一句话
func (r Result) Reason() string {
	if r.FailReason != nil {
		return *r.FailReason
	}
	return ""
}

// 发起支付的路径：第二轮起为 /internal/pay/payments；旧版 pay 只有 one-tap-payments，收到 404 时退回
const (
	PaymentsPath     = "/internal/pay/payments"
	LegacyOneTapPath = "/internal/pay/one-tap-payments"
)

// Client pay 客户端
type Client struct {
	BaseURL    string
	ServiceKey string
	HTTP       *http.Client
}

// New 默认 3 秒超时
func New(base, key string) *Client {
	return &Client{BaseURL: strings.TrimRight(base, "/"), ServiceKey: key, HTTP: &http.Client{Timeout: 3 * time.Second}}
}

func (c *Client) post(ctx context.Context, path, idem string, body any) (Result, error) {
	var out Result
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.ServiceKey)
	req.Header.Set("X-Caller", "bill")
	req.Header.Set("Idempotency-Key", idem)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return out, &StatusError{Path: path, Status: resp.StatusCode, Body: string(raw)}
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("pay %s 返回体无法解析：%w", path, err)
	}
	return out, nil
}

// ChargeCreditFree 免押扣款
func (c *Client) ChargeCreditFree(ctx context.Context, idem string, cmd CreditChargeCommand) (Result, error) {
	return c.post(ctx, "/internal/pay/credit-charges", idem, cmd)
}

// StartOneTap 发起一键支付：返回调起参数（或模拟渠道直接成功）
func (c *Client) StartOneTap(ctx context.Context, idem string, cmd OneTapPaymentCommand) (Result, error) {
	res, err := c.post(ctx, PaymentsPath, idem, cmd)
	var se *StatusError
	if errors.As(err, &se) && (se.Status == http.StatusNotFound || se.Status == http.StatusMethodNotAllowed) {
		return c.post(ctx, LegacyOneTapPath, idem, cmd)
	}
	return res, err
}

// Refund 原路退款
func (c *Client) Refund(ctx context.Context, idem string, cmd RefundCommand) (Result, error) {
	return c.post(ctx, "/internal/pay/refunds", idem, cmd)
}
