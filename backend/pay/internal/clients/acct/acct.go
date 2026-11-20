// Package acct 是 pay 调 acct 的小客户端，只写用到的接口（外部系统适配）。
// trace: R-11；contracts/openapi/acct.yaml internalCheckPayeeName
package acct

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// ErrUserNotFound acct 返回 404
var ErrUserNotFound = errors.New("acct 没有该用户")

// Client acct 客户端
type Client struct {
	BaseURL    string
	ServiceKey string
	HTTP       *http.Client
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 3 * time.Second}
}

// CheckPayeeName 核对收款户名是否为本人实名；实名姓名不出 acct。
func (c *Client) CheckPayeeName(ctx context.Context, userID, name string) (bool, error) {
	body, _ := json.Marshal(map[string]string{"name": name})
	u := c.BaseURL + "/internal/acct/users/" + url.PathEscape(userID) + "/payee-name-check"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.ServiceKey)
	req.Header.Set("X-Caller", "pay")
	resp, err := c.http().Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var out struct {
			Match bool `json:"match"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return false, err
		}
		return out.Match, nil
	case http.StatusNotFound:
		return false, ErrUserNotFound
	default:
		return false, fmt.Errorf("acct 返回 %d", resp.StatusCode)
	}
}
