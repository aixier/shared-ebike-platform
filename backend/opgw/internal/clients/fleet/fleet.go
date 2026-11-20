// Package fleet 调用 fleet 的 /internal/fleet/vehicle-lists（按 contracts/openapi/fleet.yaml）。
package fleet

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

// Row 车辆清单一行。
type Row struct {
	BrandID     string `json:"brandId"`
	PlateNo     string `json:"plateNo"`
	Model       string `json:"model"`
	PileTag     string `json:"pileTag"`
	PileTagType string `json:"pileTagType,omitempty"`
}

// Actor 操作人。
type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// Submission 提交体。
type Submission struct {
	OperatorID string `json:"operatorId"`
	Rows       []Row  `json:"rows"`
	Actor      Actor  `json:"actor"`
}

// Vehicle fleet 返回的车辆（只取用到的字段）。
type Vehicle struct {
	VehicleID  string `json:"vehicleId"`
	PlateNo    string `json:"plateNo"`
	GovCode    string `json:"govCode"`
	CodeStatus string `json:"codeStatus"`
}

// Result 入库结果。
type Result struct {
	Added    int       `json:"added"`
	Vehicles []Vehicle `json:"vehicles"`
}

// Response 原样带回 fleet 的状态与报文，便于把逐行错误原样返回给运营商。
type Response struct {
	Status int
	Body   []byte
	Result *Result // 仅 200 时有
}

// Client fleet 客户端。
type Client struct {
	BaseURL    string
	ServiceKey string
	HTTP       *http.Client
}

// New 建客户端，默认 3 秒超时。
func New(baseURL, serviceKey string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), ServiceKey: serviceKey, HTTP: &http.Client{Timeout: 3 * time.Second}}
}

// Submit 提交车辆清单。error 表示 fleet 不可达。
func (c *Client) Submit(ctx context.Context, idemKey string, s Submission) (Response, error) {
	body, _ := json.Marshal(s)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/internal/fleet/vehicle-lists", bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.ServiceKey)
	req.Header.Set("X-Caller", "opgw")
	req.Header.Set("Idempotency-Key", idemKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("调用 fleet 失败：%v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	out := Response{Status: resp.StatusCode, Body: raw}
	if resp.StatusCode == http.StatusOK {
		var r Result
		if err := json.Unmarshal(raw, &r); err != nil {
			return Response{}, fmt.Errorf("fleet 答复无法识别")
		}
		out.Result = &r
	}
	return out, nil
}
