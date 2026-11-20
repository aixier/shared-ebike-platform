// Package deposit 是 bill 调 deposit 的小客户端，只写押金抵扣一条命令（contracts/openapi/deposit.yaml）。
package deposit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DeductCommand 押金抵扣
type DeductCommand struct {
	UserID  string `json:"userId"`
	OrderID string `json:"orderId"`
	Amount  int64  `json:"amount"`
}

// DeductResult 押金抵扣结果
type DeductResult struct {
	Result       string  `json:"result"` // 成功｜失败｜结果未知
	FailCode     *string `json:"failCode"`
	FailReason   *string `json:"failReason"`
	UserID       string  `json:"userId"`
	OrderID      string  `json:"orderId"`
	Deducted     int64   `json:"deducted"`
	Shortfall    int64   `json:"shortfall"`
	BalanceAfter int64   `json:"balanceAfter"`
	Status       *string `json:"status"`
	PaymentID    *string `json:"paymentId"`
}

// Client deposit 客户端
type Client struct {
	BaseURL    string
	ServiceKey string
	HTTP       *http.Client
}

// New 默认 3 秒超时
func New(base, key string) *Client {
	return &Client{BaseURL: strings.TrimRight(base, "/"), ServiceKey: key, HTTP: &http.Client{Timeout: 3 * time.Second}}
}

// Deduct 押金抵扣
func (c *Client) Deduct(ctx context.Context, idem string, cmd DeductCommand) (DeductResult, error) {
	var out DeductResult
	b, _ := json.Marshal(cmd)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/internal/deposit/deductions", bytes.NewReader(b))
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
		return out, fmt.Errorf("deposit 抵扣返回 %d：%s", resp.StatusCode, string(raw))
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("deposit 抵扣返回体无法解析：%w", err)
	}
	return out, nil
}
