// Package operator 是平台向运营商发 M-01、M-02 请求的客户端（按 contracts/operator-spec/operator-api.yaml）。
package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"bike/opgw/internal/sign"
)

// Target 一家运营商的接入点与密钥。
type Target struct {
	BaseURL string
	AppID   string
	Secret  string
}

// UnlockRequest M-01 报文。
type UnlockRequest struct {
	OrderID   string `json:"orderId"`
	PlateNo   string `json:"plateNo"`
	Degraded  bool   `json:"degraded"`
	RequestAt string `json:"requestAt"`
}

// EndRideRequest M-02 报文。
type EndRideRequest struct {
	OrderID   string `json:"orderId"`
	PlateNo   string `json:"plateNo"`
	Cause     string `json:"cause"`
	Reason    string `json:"reason,omitempty"`
	RequestAt string `json:"requestAt"`
}

// Reply 运营商的同步答复。
type Reply struct {
	Result       string `json:"result"`
	RejectReason string `json:"rejectReason,omitempty"`
	Message      string `json:"message,omitempty"`
}

// Client 发请求；Timeout 为平台侧超时（规范 3 秒）。
type Client struct {
	HTTP    *http.Client
	Timeout time.Duration
	Now     func() time.Time
}

// New 建客户端。
func New(timeout time.Duration) *Client {
	return &Client{HTTP: &http.Client{}, Timeout: timeout, Now: time.Now}
}

// Unlock 发 M-01。返回 error 表示不可达（超时、连接失败、答复无法识别）。
func (c *Client) Unlock(ctx context.Context, t Target, idemKey string, r UnlockRequest) (Reply, error) {
	return c.post(ctx, t, "/v1/unlock", idemKey, r)
}

// EndRide 发 M-02。
func (c *Client) EndRide(ctx context.Context, t Target, idemKey string, r EndRideRequest) (Reply, error) {
	return c.post(ctx, t, "/v1/end-ride", idemKey, r)
}

func (c *Client) post(ctx context.Context, t Target, path, idemKey string, v any) (Reply, error) {
	body, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(t.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return Reply{}, fmt.Errorf("运营商地址无效：%w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", idemKey)
	sign.Apply(req, t.Secret, t.AppID, strconv.FormatInt(c.Now().Unix(), 10), sign.NewNonce(), body)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Reply{}, fmt.Errorf("运营商 %s 内未答复", c.Timeout)
		}
		return Reply{}, fmt.Errorf("连接运营商失败：%v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return Reply{}, fmt.Errorf("运营商返回 HTTP %d", resp.StatusCode)
	}
	var rep Reply
	if err := json.Unmarshal(raw, &rep); err != nil || (rep.Result != "已受理" && rep.Result != "拒绝") {
		return Reply{}, fmt.Errorf("运营商答复无法识别")
	}
	return rep, nil
}
