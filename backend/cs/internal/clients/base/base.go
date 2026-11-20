// Package base 是本服务调用其他服务的 HTTP 底座：带服务凭证、超时 3 秒、把对方的错误体转成本服务的错误。
// trace: contracts/wire.md 第 1、3 节
package base

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"bike/cs/internal/httpx"
)

// Client 调一个下游服务。
type Client struct {
	BaseURL    string
	ServiceKey string
	HTTP       *http.Client
}

// New 建客户端。
func New(baseURL, serviceKey string) *Client {
	return &Client{BaseURL: baseURL, ServiceKey: serviceKey, HTTP: &http.Client{Timeout: 3 * time.Second}}
}

// Do 发请求。idemKey 非空时带 Idempotency-Key；out 为 nil 时丢弃响应体。
// 对方返回错误体时得到 *httpx.Error（保留对方的 code、message 与附加字段）；网络失败或 5xx 得到 SERVER_BUSY。
func (c *Client) Do(ctx context.Context, method, path, idemKey string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.ServiceKey)
	req.Header.Set("X-Caller", "cs")
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		slog.Warn("调用下游失败", "method", method, "path", path, "err", err)
		return httpx.E("SERVER_BUSY", "相关服务暂时连不上，请稍后再试")
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 500 {
		return httpx.E("SERVER_BUSY", "相关服务暂时不可用，请稍后再试")
	}
	if resp.StatusCode >= 300 {
		return decodeError(resp.StatusCode, data)
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return httpx.E("SERVER_BUSY", "相关服务返回的数据无法识别")
	}
	return nil
}

func decodeError(status int, data []byte) error {
	m := map[string]any{}
	_ = json.Unmarshal(data, &m)
	if status == 401 || status == 403 {
		// 本服务凭证被拒，属于部署配置问题，对页面按服务不可用处理
		return httpx.E("SERVER_BUSY", "相关服务拒绝了本服务的调用，请联系管理员")
	}
	code, _ := m["code"].(string)
	msg, _ := m["message"].(string)
	if code == "" {
		switch status {
		case 404:
			code = "NOT_FOUND"
		case 400:
			code = "INVALID"
		default:
			code = "CONFLICT"
		}
	}
	if msg == "" {
		msg = "操作没有成功"
	}
	delete(m, "code")
	delete(m, "message")
	e := &httpx.Error{Code: code, Message: msg}
	if len(m) > 0 {
		e.Extra = m
	}
	return e
}
