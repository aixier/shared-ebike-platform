package core

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// OrderFilter 订单查询条件。
type OrderFilter struct {
	UserID     string
	Q          string
	PlateNo    string
	Status     string
	BrandID    string
	OperatorID string
	Scope      *string // 运营商数据范围
	Degraded   *bool
	From, To   *time.Time
	OrderIDs   []string
	Page       int
	PageSize   int
	SortKey    string
	SortDir    string
}

var sortCols = map[string]string{
	"requestAt":   "request_at",
	"startAt":     "start_at",
	"endAt":       "(doc->>'endAt')",
	"durationSec": "((doc->>'durationSec')::bigint)",
	"orderId":     "order_id",
}

var userIDRe = regexp.MustCompile(`^U\d{6}$`)

// ListOrders 分页查订单（新到旧）。
func (s *Service) ListOrders(ctx context.Context, f OrderFilter) ([]*Order, int, error) {
	var where []string
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if f.UserID != "" {
		where = append(where, "user_id = "+arg(f.UserID))
	}
	if f.PlateNo != "" {
		where = append(where, "plate_no = "+arg(f.PlateNo))
	}
	if f.Status != "" {
		where = append(where, "status = "+arg(f.Status))
	}
	if f.BrandID != "" {
		where = append(where, "brand_id = "+arg(f.BrandID))
	}
	if f.OperatorID != "" {
		where = append(where, "operator_id = "+arg(f.OperatorID))
	}
	if f.Scope != nil {
		where = append(where, "operator_id = "+arg(*f.Scope))
	}
	if f.Degraded != nil {
		where = append(where, "degraded = "+arg(*f.Degraded))
	}
	if f.From != nil {
		where = append(where, "request_at >= "+arg(*f.From))
	}
	if f.To != nil {
		where = append(where, "request_at <= "+arg(*f.To))
	}
	if len(f.OrderIDs) > 0 {
		where = append(where, "order_id = ANY("+arg(f.OrderIDs)+")")
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		switch {
		case userIDRe.MatchString(q):
			where = append(where, "user_id = "+arg(q))
		default:
			p := arg("%" + q + "%")
			where = append(where, "(order_id ILIKE "+p+" OR plate_no = "+arg(q)+")")
		}
	}
	w := ""
	if len(where) > 0 {
		w = " WHERE " + strings.Join(where, " AND ")
	}
	var total int
	if err := s.store.pool.QueryRow(ctx, "SELECT count(*) FROM orders"+w, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	col, ok := sortCols[f.SortKey]
	if !ok {
		col = "request_at"
	}
	dir := "DESC"
	if strings.EqualFold(f.SortDir, "asc") {
		dir = "ASC"
	}
	page, size := f.Page, f.PageSize
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	if size > 200 {
		size = 200
	}
	q := fmt.Sprintf("SELECT doc, rev FROM orders%s ORDER BY %s %s NULLS LAST, order_id %s LIMIT %d OFFSET %d", w, col, dir, dir, size, (page-1)*size)
	os, err := s.store.queryOrders(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	// 数据库不可用期间建立、尚未回放的订单以内存为准
	s.mu.Lock()
	for i, o := range os {
		if m, ok := s.mem[o.OrderID]; ok {
			c := *m
			os[i] = &c
		}
	}
	s.mu.Unlock()
	return os, total, nil
}

// GetOrder 读一单（找不到返回 errNotFound）。
func (s *Service) GetOrder(ctx context.Context, id string) (*Order, error) {
	o, err := s.loadOrder(ctx, id)
	if err != nil {
		return nil, err
	}
	return o, nil
}

func isNotFound(err error) bool { return errors.Is(err, errNotFound) }
