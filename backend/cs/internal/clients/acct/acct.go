// Package acct 是 acct 服务 /internal 接口的小客户端。
// trace: contracts/openapi/acct.yaml internalLookupUsers、internalGetUser、internalUnfreezeUser、internalCancelUser
package acct

import (
	"context"
	"net/url"
	"time"

	"bike/cs/internal/clients/base"
)

// Actor 是操作人。
type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// UserBrief 是按手机号找到的用户。
type UserBrief struct {
	UserID        string `json:"userId"`
	AccountStatus string `json:"accountStatus"`
}

// User 是用户信息中本服务用到的字段。
type User struct {
	UserID  string `json:"userId"`
	Account struct {
		Status string `json:"status"`
	} `json:"account"`
}

// Command 是人工解冻、协助注销的命令体。
type Command struct {
	Reason   string  `json:"reason"`
	Actor    Actor   `json:"actor"`
	TicketID *string `json:"ticketId"`
}

// CancelResult 是注销结果。
type CancelResult struct {
	OK          bool      `json:"ok"`
	UserID      string    `json:"userId"`
	CancelledAt time.Time `json:"cancelledAt"`
}

// Client 调 acct。
type Client struct{ c *base.Client }

// New 建客户端。
func New(baseURL, serviceKey string) *Client { return &Client{c: base.New(baseURL, serviceKey)} }

// Lookup 按完整手机号或尾号找用户（二选一）。
func (c *Client) Lookup(ctx context.Context, phone, phoneTail string) ([]UserBrief, error) {
	q := url.Values{}
	if phone != "" {
		q.Set("phone", phone)
	} else {
		q.Set("phoneTail", phoneTail)
	}
	var out struct {
		Users []UserBrief `json:"users"`
	}
	if err := c.c.Do(ctx, "GET", "/internal/acct/users/lookup?"+q.Encode(), "", nil, &out); err != nil {
		return nil, err
	}
	return out.Users, nil
}

// GetUser 读用户信息。
func (c *Client) GetUser(ctx context.Context, userID string) (*User, error) {
	var u User
	if err := c.c.Do(ctx, "GET", "/internal/acct/users/"+url.PathEscape(userID), "", nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// Unfreeze 人工解冻，返回解冻后的用户。
func (c *Client) Unfreeze(ctx context.Context, userID, idemKey string, cmd Command) (*User, error) {
	var u User
	if err := c.c.Do(ctx, "POST", "/internal/acct/users/"+url.PathEscape(userID)+"/unfreeze", idemKey, cmd, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// Cancel 协助注销。
func (c *Client) Cancel(ctx context.Context, userID, idemKey string, cmd Command) (*CancelResult, error) {
	var r CancelResult
	if err := c.c.Do(ctx, "POST", "/internal/acct/users/"+url.PathEscape(userID)+"/cancel", idemKey, cmd, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
