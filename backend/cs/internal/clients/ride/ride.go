// Package ride 是 ride 服务 /internal 接口的小客户端，只写本服务用到的接口。
// trace: contracts/openapi/ride.yaml internalGetRideOrder、internalListRideOrders、internalForceEndRide
package ride

import (
	"context"
	"net/url"
	"strconv"
	"time"

	"bike/cs/internal/clients/base"
)

// Actor 是操作人。
type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// Order 是 AdminRideOrder 中本服务用到的字段。
type Order struct {
	OrderID        string     `json:"orderId"`
	UserID         string     `json:"userId"`
	OperatorID     string     `json:"operatorId"`
	VehicleID      string     `json:"vehicleId"`
	PlateNo        string     `json:"plateNo"`
	Status         string     `json:"status"`
	RequestAt      time.Time  `json:"requestAt"`
	StartAt        *time.Time `json:"startAt"`
	EndAt          *time.Time `json:"endAt"`
	EndMethod      *string    `json:"endMethod"`
	DurationSec    *int64     `json:"durationSec"`
	DistanceKm     *float64   `json:"distanceKm"`
	InStation      *bool      `json:"inStation"`
	InStationBasis *string    `json:"inStationBasis"`
}

// OrderPage 是分页订单事实。
type OrderPage struct {
	Rows     []Order `json:"rows"`
	Total    int     `json:"total"`
	Page     int     `json:"page"`
	PageSize int     `json:"pageSize"`
}

// ForceEndRequest 是强制结束命令。
type ForceEndRequest struct {
	BilledEndAt *time.Time `json:"billedEndAt,omitempty"`
	StationID   *string    `json:"stationId"`
	TicketID    *string    `json:"ticketId"`
	Reason      string     `json:"reason"`
	Actor       Actor      `json:"actor"`
}

// ForceEndResult 是强制结束结果。
type ForceEndResult struct {
	Order       Order  `json:"order"`
	LockRequest string `json:"lockRequest"`
}

// Client 调 ride。
type Client struct{ c *base.Client }

// New 建客户端。
func New(baseURL, serviceKey string) *Client { return &Client{c: base.New(baseURL, serviceKey)} }

// GetOrder 读订单事实。
func (c *Client) GetOrder(ctx context.Context, orderID string) (*Order, error) {
	var o Order
	if err := c.c.Do(ctx, "GET", "/internal/ride/orders/"+url.PathEscape(orderID), "", nil, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

// ListByPlate 按车号查订单事实。
func (c *Client) ListByPlate(ctx context.Context, plateNo string, pageSize int) (*OrderPage, error) {
	q := url.Values{"plateNo": {plateNo}, "page": {"1"}, "pageSize": {strconv.Itoa(pageSize)}}
	var p OrderPage
	if err := c.c.Do(ctx, "GET", "/internal/ride/orders?"+q.Encode(), "", nil, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ForceEnd 强制结束。
func (c *Client) ForceEnd(ctx context.Context, orderID, idemKey string, req ForceEndRequest) (*ForceEndResult, error) {
	var r ForceEndResult
	if err := c.c.Do(ctx, "POST", "/internal/ride/orders/"+url.PathEscape(orderID)+"/force-end", idemKey, req, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
