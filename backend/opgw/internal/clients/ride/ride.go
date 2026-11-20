// Package ride 调用 ride 的 /internal/ride/ride-events（按 contracts/openapi/ride.yaml）。
package ride

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

// Unlock 开锁结果部分。
type Unlock struct {
	Result string  `json:"result"`
	Reason *string `json:"reason"`
	At     string  `json:"at"`
}

// Lock 锁车部分。
type Lock struct {
	At                   string    `json:"at"`
	Location             []float64 `json:"location,omitempty"`
	VehicleSideInStation *bool     `json:"vehicleSideInStation"`
	DurationSec          *int64    `json:"durationSec"`
	DistanceKm           *float64  `json:"distanceKm"`
	Manual               bool      `json:"manual"`
}

// Event 送达 ride 的骑行事件。
type Event struct {
	OperatorEventID string  `json:"operatorEventId"`
	OrderID         *string `json:"orderId"`
	PlateNo         string  `json:"plateNo"`
	BrandID         string  `json:"brandId"`
	OperatorID      string  `json:"operatorId"`
	Kind            string  `json:"kind"`
	Unlock          *Unlock `json:"unlock"`
	Lock            *Lock   `json:"lock"`
	ReceivedAt      string  `json:"receivedAt"`
	Late            bool    `json:"late"`
}

// Client ride 客户端。
type Client struct {
	BaseURL    string
	ServiceKey string
	HTTP       *http.Client
}

// New 建客户端，默认 3 秒超时。
func New(baseURL, serviceKey string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), ServiceKey: serviceKey, HTTP: &http.Client{Timeout: 3 * time.Second}}
}

// Deliver 送达；返回 nil 表示 ride 已持久化（200）。
func (c *Client) Deliver(ctx context.Context, idemKey string, e Event) error {
	body, _ := json.Marshal(e)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/internal/ride/ride-events", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.ServiceKey)
	req.Header.Set("X-Caller", "opgw")
	req.Header.Set("Idempotency-Key", idemKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("送达 ride 失败：%v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ride 返回 HTTP %d：%s", resp.StatusCode, string(raw))
	}
	return nil
}
