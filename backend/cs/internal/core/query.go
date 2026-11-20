package core

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"bike/cs/internal/auth"
	"bike/cs/internal/httpx"
)

// TicketFilter 是工单列表的筛选条件。
type TicketFilter struct {
	Status   []string
	Category string
	UserID   string
	OrderID  string
	Assignee string
	Q        string
	Page     int
	PageSize int
	SortDir  string
}

// TicketPage 是分页结果。
type TicketPage struct {
	Rows     []AdminTicket `json:"rows"`
	Total    int           `json:"total"`
	Page     int           `json:"page"`
	PageSize int           `json:"pageSize"`
}

func scopeWhere(ad *auth.Admin, conds []string, args []any) ([]string, []any) {
	if ad.Scope != "" {
		args = append(args, ad.Scope)
		conds = append(conds, fmt.Sprintf("operator_id = $%d", len(args)))
	}
	return conds, args
}

// ListTickets 工单列表，按创建时间新到旧（sortDir=asc 时旧到新）。
func (a *App) ListTickets(ctx context.Context, ad *auth.Admin, f TicketFilter) (*TicketPage, error) {
	var conds []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	if len(f.Status) > 0 {
		for _, s := range f.Status {
			if !Statuses[s] {
				return nil, httpx.E("INVALID", "工单状态不对")
			}
		}
		add("status = ANY($%d)", f.Status)
	}
	if f.Category != "" {
		add("category = $%d", f.Category)
	}
	if f.UserID != "" {
		add("user_id = $%d", f.UserID)
	}
	if f.OrderID != "" {
		add("order_id = $%d", f.OrderID)
	}
	if f.Assignee != "" {
		add("assignee = $%d", f.Assignee)
	}
	if f.Q != "" {
		args = append(args, f.Q)
		n := len(args)
		conds = append(conds, fmt.Sprintf("(ticket_id = $%d OR order_id = $%d OR user_id = $%d)", n, n, n))
	}
	conds, args = scopeWhere(ad, conds, args)
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 {
		f.PageSize = 20
	}
	if f.PageSize > 200 {
		f.PageSize = 200
	}
	var total int
	if err := a.DB.QueryRow(ctx, `SELECT count(*) FROM tickets `+where, args...).Scan(&total); err != nil {
		return nil, err
	}
	dir := "DESC"
	if strings.EqualFold(f.SortDir, "asc") {
		dir = "ASC"
	}
	args = append(args, f.PageSize, (f.Page-1)*f.PageSize)
	ts, err := queryTickets(ctx, a.DB, fmt.Sprintf(`%s ORDER BY created_at %s, ticket_id %s LIMIT $%d OFFSET $%d`, where, dir, dir, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, err
	}
	limit, err := a.refundLimit(ctx, a.DB)
	if err != nil {
		return nil, err
	}
	rows := make([]AdminTicket, 0, len(ts))
	for _, t := range ts {
		rows = append(rows, t.Admin(limit))
	}
	return &TicketPage{Rows: rows, Total: total, Page: f.Page, PageSize: f.PageSize}, nil
}

// ListTicketsBy 按用户编号或订单号查工单，新到旧，不分页。
func (a *App) ListTicketsBy(ctx context.Context, ad *auth.Admin, column, value string) ([]AdminTicket, error) {
	conds, args := []string{column + " = $1"}, []any{value}
	conds, args = scopeWhere(ad, conds, args)
	ts, err := queryTickets(ctx, a.DB, `WHERE `+strings.Join(conds, " AND ")+` ORDER BY created_at DESC, ticket_id DESC`, args...)
	if err != nil {
		return nil, err
	}
	limit, err := a.refundLimit(ctx, a.DB)
	if err != nil {
		return nil, err
	}
	out := make([]AdminTicket, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Admin(limit))
	}
	return out, nil
}

// SearchResult 是 cs.yaml SearchResult。
type SearchResult struct {
	MatchedBy string          `json:"matchedBy"`
	Users     []SearchUserHit `json:"users"`
	Orders    []SearchOrder   `json:"orders"`
}

// SearchUserHit 是命中的用户。
type SearchUserHit struct {
	UserID          string `json:"userId"`
	TicketCount     int    `json:"ticketCount"`
	OpenTicketCount int    `json:"openTicketCount"`
}

// SearchOrder 是命中的订单。
type SearchOrder struct {
	OrderID   string   `json:"orderId"`
	UserID    string   `json:"userId"`
	TicketIDs []string `json:"ticketIds"`
}

var (
	rePhone   = regexp.MustCompile(`^1[3-9]\d{9}$`)
	reTail    = regexp.MustCompile(`^\d{4,10}$`)
	reDigits  = regexp.MustCompile(`^\d+$`)
	reUserID  = regexp.MustCompile(`^U\d{6}$`)
	reOrderID = regexp.MustCompile(`^OD\d{13}$`)
	rePlate   = regexp.MustCompile(`^[A-Z0-9\-]{2,20}$`)
)

// classify 按 contracts/kernel.md 的编号格式识别查询词。
func classify(q string) (string, string) {
	u := strings.ToUpper(q)
	switch {
	case rePhone.MatchString(q):
		return "手机号", q
	case reUserID.MatchString(u):
		return "用户编号", u
	case reOrderID.MatchString(u):
		return "订单号", u
	case reTail.MatchString(q):
		return "手机尾号", q
	case reDigits.MatchString(q):
		return "", q
	case rePlate.MatchString(u):
		return "车号", u
	}
	return "无", q
}

// Search 按手机号、尾号、用户编号、订单号、车号查；只返回编号与本模块的工单数。
func (a *App) Search(ctx context.Context, ad *auth.Admin, q string) (*SearchResult, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, httpx.E("INVALID", "请输入手机号、手机尾号、用户编号、订单号或车号")
	}
	kind, v := classify(q)
	if kind == "" {
		if len(q) < 4 {
			return nil, httpx.E("INVALID", "手机尾号至少 4 位数字")
		}
		return nil, httpx.E("INVALID", "手机号格式不对，请输入 11 位手机号或 4～10 位尾号")
	}
	res := &SearchResult{MatchedBy: kind, Users: []SearchUserHit{}, Orders: []SearchOrder{}}
	var userIDs []string
	notFound := func(err error) bool {
		var he *httpx.Error
		return errors.As(err, &he) && he.Code == "NOT_FOUND"
	}
	switch kind {
	case "手机号", "手机尾号":
		if ad.Scope != "" {
			break
		}
		phone, tail := "", ""
		if kind == "手机号" {
			phone = v
		} else {
			tail = v
		}
		us, err := a.Acct.Lookup(ctx, phone, tail)
		if err != nil {
			return nil, err
		}
		for _, u := range us {
			userIDs = append(userIDs, u.UserID)
		}
	case "用户编号":
		if ad.Scope != "" {
			break
		}
		if _, err := a.Acct.GetUser(ctx, v); err != nil {
			if !notFound(err) {
				return nil, err
			}
		} else {
			userIDs = append(userIDs, v)
		}
	case "订单号":
		o, err := a.Ride.GetOrder(ctx, v)
		if err != nil && !notFound(err) {
			return nil, err
		}
		if err == nil && (ad.Scope == "" || o.OperatorID == ad.Scope) {
			res.Orders = append(res.Orders, SearchOrder{OrderID: o.OrderID, UserID: o.UserID})
		}
	case "车号":
		p, err := a.Ride.ListByPlate(ctx, v, 20)
		if err != nil {
			return nil, err
		}
		for _, o := range p.Rows {
			if ad.Scope == "" || o.OperatorID == ad.Scope {
				res.Orders = append(res.Orders, SearchOrder{OrderID: o.OrderID, UserID: o.UserID})
			}
		}
	}
	if len(res.Orders) > 20 {
		res.Orders = res.Orders[:20]
	}
	for i := range res.Orders {
		o := &res.Orders[i]
		o.TicketIDs = []string{}
		rows, err := a.DB.Query(ctx, `SELECT ticket_id FROM tickets WHERE order_id = $1 ORDER BY created_at DESC`, o.OrderID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			o.TicketIDs = append(o.TicketIDs, id)
		}
		rows.Close()
		if ad.Scope == "" && !contains(userIDs, o.UserID) {
			userIDs = append(userIDs, o.UserID)
		}
	}
	if len(userIDs) > 20 {
		userIDs = userIDs[:20]
	}
	for _, uid := range userIDs {
		h := SearchUserHit{UserID: uid}
		if err := a.DB.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE status <> '已解决') FROM tickets WHERE user_id = $1`, uid).Scan(&h.TicketCount, &h.OpenTicketCount); err != nil {
			return nil, err
		}
		res.Users = append(res.Users, h)
	}
	// 客服查看了这些用户：只记 cs 自己的查看（acct 的读取由 acct 记）；摘要不带手机号
	if len(res.Users) > 0 {
		now := a.now()
		err := a.withTx(ctx, func(tx pgx.Tx) error {
			for _, u := range res.Users {
				if err := emitAudit(ctx, tx, staffActor(ad), now, Audit{Action: "查看用户信息", TargetType: "user", TargetID: u.UserID,
					Summary: "客服查询并查看用户 " + u.UserID + "（按" + kind + "命中）"}); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return res, nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// Todo 是待办项。
type Todo struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Count int    `json:"count"`
	Menu  string `json:"menu"`
}

// Todos 按当前账号的权限点返回待办计数，只返回大于 0 的项。
func (a *App) Todos(ctx context.Context, ad *auth.Admin) ([]Todo, error) {
	out := []Todo{}
	conds, args := scopeWhere(ad, nil, nil)
	extra := ""
	if len(conds) > 0 {
		extra = " AND " + strings.Join(conds, " AND ")
	}
	count := func(cond string) (int, error) {
		var n int
		err := a.DB.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE `+cond+extra, args...).Scan(&n)
		return n, err
	}
	items := []struct {
		perm, key, label, menu, cond string
	}{
		{"refund.confirm", "tickets", "待处理工单", "退款处理", "status IN ('待处理', '处理中')"},
		{"refund.confirm.overLimit", "overLimit", "待主管确认的退款", "退款处理", "status = '待主管确认'"},
		{"order.forceEnd", "forceEnd", "待强制结束的订单", "强制结束订单", "category = '订单无法结束' AND status <> '已解决'"},
	}
	for _, it := range items {
		if !ad.Has(it.perm) {
			continue
		}
		n, err := count(it.cond)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			out = append(out, Todo{Key: it.key, Label: it.label, Count: n, Menu: it.menu})
		}
	}
	return out, nil
}
