// Package svccall 是本服务调别的服务 /internal 接口时的最小 HTTP 调用：
// 带服务凭证、调用方名、幂等键，3 秒超时，解析对方错误体。只在本服务内使用。
package svccall

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Caller 调用参数。
type Caller struct {
	BaseURL    string
	ServiceKey string
	HTTP       *http.Client
}

// RemoteError 对方返回的非 2xx。
type RemoteError struct {
	Status  int
	Code    string
	Message string
}

func (e *RemoteError) Error() string { return fmt.Sprintf("%d %s %s", e.Status, e.Code, e.Message) }

// New 构造；超时默认 3 秒（wire.md）。
func New(baseURL, serviceKey string) *Caller {
	return &Caller{BaseURL: baseURL, ServiceKey: serviceKey, HTTP: &http.Client{Timeout: 3 * time.Second}}
}

// Post 发 POST，out 为 nil 时忽略响应体。网络错误与超时原样返回（调用方按 SERVER_BUSY 处理）。
func (c *Caller) Post(ctx context.Context, path, idemKey string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.ServiceKey)
	req.Header.Set("X-Caller", "ops")
	req.Header.Set("Idempotency-Key", idemKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		re := &RemoteError{Status: resp.StatusCode}
		var eb struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(data, &eb) == nil {
			re.Code, re.Message = eb.Code, eb.Message
		}
		return re
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}
