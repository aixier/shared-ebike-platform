// Package fleet 调 fleet 的暂停可租、恢复可租（contracts/openapi/fleet.yaml /internal/fleet/brands/{brandId}/pause|resume）。
package fleet

import (
	"context"
	"net/url"

	"bike/ops/internal/clients/svccall"
)

// Actor 操作人。
type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// PauseRequest 暂停可租请求体。
type PauseRequest struct {
	Actor          Actor    `json:"actor"`
	Metric         string   `json:"metric"`
	Value          *float64 `json:"value"`
	Threshold      *float64 `json:"threshold"`
	SustainMinutes *int     `json:"sustainMinutes"`
	Reason         string   `json:"reason"`
}

// ResumeRequest 恢复可租请求体。
type ResumeRequest struct {
	Actor  Actor  `json:"actor"`
	Reason string `json:"reason"`
}

// Pause fleet 的暂停可租记录（只取用到的字段）。
type Pause struct {
	PauseID   string  `json:"pauseId"`
	BrandID   string  `json:"brandId"`
	PausedAt  string  `json:"pausedAt"`
	ResumedAt *string `json:"resumedAt"`
}

// PauseResult 暂停或恢复结果。
type PauseResult struct {
	Brand struct {
		BrandID      string `json:"brandId"`
		AccessStatus string `json:"accessStatus"`
	} `json:"brand"`
	Pause Pause `json:"pause"`
}

// Client fleet 客户端。
type Client struct{ c *svccall.Caller }

// New 构造。
func New(baseURL, serviceKey string) *Client { return &Client{c: svccall.New(baseURL, serviceKey)} }

// PauseBrand 暂停可租。
func (f *Client) PauseBrand(ctx context.Context, brandID, idemKey string, req PauseRequest) (*PauseResult, error) {
	var out PauseResult
	if err := f.c.Post(ctx, "/internal/fleet/brands/"+url.PathEscape(brandID)+"/pause", idemKey, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ResumeBrand 恢复可租。
func (f *Client) ResumeBrand(ctx context.Context, brandID, idemKey string, req ResumeRequest) (*PauseResult, error) {
	var out PauseResult
	if err := f.c.Post(ctx, "/internal/fleet/brands/"+url.PathEscape(brandID)+"/resume", idemKey, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
