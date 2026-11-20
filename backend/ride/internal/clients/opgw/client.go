// Package opgw 按 contracts/openapi/opgw.yaml 手写的小客户端：只有开锁（M-01）与结束骑行（M-02）两个命令。
package opgw

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Target 指令目标（CommandTarget）。
type Target struct {
	OrderID    string `json:"orderId"`
	OperatorID string `json:"operatorId"`
	BrandID    string `json:"brandId"`
	VehicleID  string `json:"vehicleId,omitempty"`
	PlateNo    string `json:"plateNo"`
}

// UnlockCommand 开锁命令。
type UnlockCommand struct {
	Target
	Degraded bool `json:"degraded"`
}

// 结束原因（EndRideCommand.cause）。
const (
	CauseUserReturn = "用户点还车"
	CausePileNoLock = "入桩未锁"
	CauseLateUnlock = "超时后才报开锁成功"
	CauseForceEnd   = "客服强制结束"
)

// EndRideCommand 结束骑行命令。
type EndRideCommand struct {
	Target
	Cause  string `json:"cause"`
	Reason string `json:"reason,omitempty"`
}

// 运营商同步答复（CommandResult.result）。
const (
	Accepted    = "已受理"
	Rejected    = "拒绝"
	Unreachable = "不可达"
)

// Result 运营商的同步答复。
type Result struct {
	Result          string  `json:"result"`
	RejectReason    *string `json:"rejectReason"`
	OperatorMessage *string `json:"operatorMessage"`
	At              string  `json:"at"`
	CostMs          int     `json:"costMs"`
}

// Client opgw 客户端。
type Client struct {
	BaseURL    string
	Caller     string // 本服务模块名 ride
	ServiceKey string
	HTTP       *http.Client
}

// New 新建客户端，默认 3 秒超时（wire.md 第 1 节）。
func New(baseURL, serviceKey string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Caller: "ride", ServiceKey: serviceKey, HTTP: &http.Client{Timeout: 3 * time.Second}}
}

// Unlock 请求运营商开锁；幂等键用订单号，重复请求不重复开锁。
func (c *Client) Unlock(ctx context.Context, cmd UnlockCommand) (Result, error) {
	return c.post(ctx, "/internal/opgw/unlock", "unlock:"+cmd.OrderID, cmd)
}

// EndRide 请求运营商结束骑行。
func (c *Client) EndRide(ctx context.Context, cmd EndRideCommand) (Result, error) {
	return c.post(ctx, "/internal/opgw/end-ride", "end:"+cmd.OrderID+":"+cmd.Cause, cmd)
}

func (c *Client) post(ctx context.Context, path, idem string, body any) (Result, error) {
	var res Result
	if c == nil || c.BaseURL == "" {
		return Result{Result: Unreachable}, fmt.Errorf("没有配置 OPGW_URL")
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(b))
	if err != nil {
		return Result{Result: Unreachable}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.ServiceKey)
	req.Header.Set("X-Caller", c.Caller)
	req.Header.Set("Idempotency-Key", idem)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Result{Result: Unreachable}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		// 凭证停用等：运营商不能受理，按拒绝处理
		if resp.StatusCode == http.StatusConflict {
			other := "其他"
			return Result{Result: Rejected, RejectReason: &other, OperatorMessage: &e.Message}, nil
		}
		return Result{Result: Unreachable}, fmt.Errorf("opgw 返回 %d %s", resp.StatusCode, e.Code)
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return Result{Result: Unreachable}, err
	}
	return res, nil
}
