// Package fleet 按 contracts/openapi/fleet.yaml 手写的小客户端，只含本模块用到的两个内部命令。
package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Actor 操作人。
type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// normalizeActor 调 fleet 时 actor.kind 只能是"人"或"系统"（第二轮变更单），其他一律按"系统"。
func normalizeActor(a Actor) Actor {
	if a.Kind != "人" && a.Kind != "系统" {
		a.Kind = "系统"
	}
	return a
}

// Error fleet 返回的非 2xx 业务错误。
type Error struct {
	Status  int
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("fleet %d %s %s", e.Status, e.Code, e.Message) }

// Client 调 fleet 的 /internal 接口。
type Client struct {
	BaseURL    string
	ServiceKey string
	HTTP       *http.Client
}

// New 默认 3 秒超时（wire.md 第 1 节）。
func New(baseURL, serviceKey string) *Client {
	return &Client{BaseURL: baseURL, ServiceKey: serviceKey, HTTP: &http.Client{Timeout: 3 * time.Second}}
}

// StartResult 开始联调后的当前接入状态。
type StartResult struct {
	BrandID      string    `json:"brandId"`
	AccessStatus string    `json:"accessStatus"`
	StatusSince  time.Time `json:"statusSince"`
}

// StartIntegration 品牌开始联调（未接入 → 联调中）。
func (c *Client) StartIntegration(ctx context.Context, brandID, idemKey string, actor Actor) (*StartResult, error) {
	var out StartResult
	err := c.post(ctx, "/internal/fleet/brands/"+brandID+"/start-integration", idemKey, map[string]any{"actor": normalizeActor(actor)}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// GoLiveResult 上线结果。
type GoLiveResult struct {
	Brand        json.RawMessage `json:"brand"`
	EnabledCodes int             `json:"enabledCodes"`
}

// GoLive 品牌上线（联调中 → 已接入，待启用的码启用）。
func (c *Client) GoLive(ctx context.Context, brandID, idemKey string, actor Actor, reason string) (*GoLiveResult, error) {
	var out GoLiveResult
	err := c.post(ctx, "/internal/fleet/brands/"+brandID+"/go-live", idemKey, map[string]any{"actor": normalizeActor(actor), "reason": reason}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) post(ctx context.Context, path, idemKey string, body, out any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.ServiceKey)
	req.Header.Set("X-Caller", "onboard")
	req.Header.Set("Idempotency-Key", idemKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		e := &Error{Status: resp.StatusCode}
		_ = json.Unmarshal(data, e)
		return e
	}
	return json.Unmarshal(data, out)
}
