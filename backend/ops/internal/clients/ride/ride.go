// Package ride 调 ride 的补录锁车事实（contracts/openapi/ride.yaml /internal/ride/orders/{orderId}/lock-fact）
// 与 /healthz（骑行入口可用性采样）。
package ride

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"bike/ops/internal/clients/svccall"
)

// Actor 操作人。
type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// LockFactRequest 补录锁车事实。
type LockFactRequest struct {
	EndAt     string  `json:"endAt"`
	StationID *string `json:"stationId"`
	Reason    string  `json:"reason"`
	Actor     Actor   `json:"actor"`
}

// Order 只取用到的字段。
type Order struct {
	OrderID string `json:"orderId"`
	Status  string `json:"status"`
}

// Client ride 客户端。
type Client struct {
	c    *svccall.Caller
	base string
}

// New 构造。
func New(baseURL, serviceKey string) *Client {
	return &Client{c: svccall.New(baseURL, serviceKey), base: baseURL}
}

// BackfillLockFact 补录锁车事实。
func (r *Client) BackfillLockFact(ctx context.Context, orderID, idemKey string, req LockFactRequest) (*Order, error) {
	var out Order
	if err := r.c.Post(ctx, "/internal/ride/orders/"+url.PathEscape(orderID)+"/lock-fact", idemKey, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Healthy 骑行入口是否可用：GET /healthz 在 3 秒内返回 200。
func (r *Client) Healthy(ctx context.Context) bool {
	if r.base == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.base+"/healthz", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
