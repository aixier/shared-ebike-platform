package core

import "time"

// CST 是本服务对外输出时刻用的时区（+08:00）。
var CST = time.FixedZone("CST", 8*3600)

// AgentAccountID 是客服智能体的账号编号（cs.yaml RefundProposal.proposedBy）。
const AgentAccountID = "AC0904"

// 工单状态（状态机：待处理 → 处理中 → 待主管确认 → 已解决）。
const (
	StatusPending    = "待处理"
	StatusProcessing = "处理中"
	StatusSupervisor = "待主管确认"
	StatusResolved   = "已解决"
)

// 消息发送方。
const (
	FromUser  = "用户"
	FromAgent = "客服智能体"
	FromStaff = "客服"
)

// 退款提议的处理结论。
const (
	DecisionPending    = "待确认"
	DecisionConfirmed  = "已确认"
	DecisionRejected   = "已驳回"
	DecisionSupervisor = "待主管确认"
)

// 枚举取值，来自 contracts/openapi/cs.yaml。
var (
	Categories = set("费用异议", "调度费异议", "订单无法结束", "开锁失败", "账户冻结", "退押", "注销", "旧押金咨询", "月卡与优惠券", "稍后扣费咨询", "开具发票", "费用咨询")
	Entries    = set("我的订单", "骑行中页", "失败提示页", "押金", "账户", "我的")
	Statuses   = set(StatusPending, StatusProcessing, StatusSupervisor, StatusResolved)
	RefundKind = set("全额退款", "部分退款", "不退款")
)

func set(vs ...string) map[string]bool {
	m := map[string]bool{}
	for _, v := range vs {
		m[v] = true
	}
	return m
}

// Message 是对话消息。
type Message struct {
	From string    `json:"from"`
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// Facts 是订单事实快照，建单时写入后不改。
type Facts struct {
	OrderID        string     `json:"orderId"`
	StartAt        *time.Time `json:"startAt"`
	EndAt          *time.Time `json:"endAt"`
	BilledMinutes  *int64     `json:"billedMinutes"`
	DistanceKm     *float64   `json:"distanceKm"`
	Receivable     *int64     `json:"receivable"`
	Payable        *int64     `json:"payable"`
	NetReceived    *int64     `json:"netReceived"`
	InStation      *bool      `json:"inStation"`
	InStationBasis *string    `json:"inStationBasis"`
}

// RefundProposal 是退款提议。
type RefundProposal struct {
	Amount     int64      `json:"amount"`
	Kind       string     `json:"kind"`
	Basis      string     `json:"basis"`
	ProposedAt time.Time  `json:"proposedAt"`
	ProposedBy string     `json:"proposedBy"`
	Decision   string     `json:"decision"`
	DecidedBy  *string    `json:"decidedBy"`
	DecidedAt  *time.Time `json:"decidedAt"`
	OverLimit  bool       `json:"overLimit"`
}

// ForceEndProposal 是强制结束提议。
type ForceEndProposal struct {
	BilledEndAt time.Time `json:"billedEndAt"`
	StationID   *string   `json:"stationId"`
}

// ForceEndRecord 是已执行的强制结束。
type ForceEndRecord struct {
	At          time.Time `json:"at"`
	By          string    `json:"by"`
	Reason      string    `json:"reason"`
	BilledEndAt time.Time `json:"billedEndAt"`
}

// Action 是处理动作。
type Action struct {
	Type      string    `json:"type"`
	PaymentID *string   `json:"paymentId"`
	At        time.Time `json:"at"`
	By        string    `json:"by"`
}

// Ticket 是工单的完整状态。
type Ticket struct {
	TicketID              string
	UserID                string
	OrderID               *string
	OperatorID            *string
	Category              string
	Entry                 string
	Status                string
	CreatedAt             time.Time
	ClosedAt              *time.Time
	Assignee              *string
	AssigneeName          *string
	Facts                 *Facts
	RefundProposal        *RefundProposal
	ForceEndProposal      *ForceEndProposal
	ForceEnd              *ForceEndRecord
	AwaitingPayee         bool
	RefundFailedPaymentID *string
	RefundPending         bool
	RefundSeq             int
	Messages              []Message
	Actions               []Action
}

// AdminTicket 是客服看到的工单（cs.yaml Ticket）。
type AdminTicket struct {
	TicketID              string            `json:"ticketId"`
	UserID                string            `json:"userId"`
	OrderID               *string           `json:"orderId"`
	Category              string            `json:"category"`
	Entry                 string            `json:"entry"`
	CreatedAt             time.Time         `json:"createdAt"`
	Status                string            `json:"status"`
	ClosedAt              *time.Time        `json:"closedAt"`
	Assignee              *string           `json:"assignee"`
	AssigneeName          *string           `json:"assigneeName"`
	Messages              []Message         `json:"messages"`
	Facts                 *Facts            `json:"facts"`
	RefundProposal        *RefundProposal   `json:"refundProposal"`
	ForceEndProposal      *ForceEndProposal `json:"forceEndProposal"`
	ForceEnd              *ForceEndRecord   `json:"forceEnd"`
	Actions               []Action          `json:"actions"`
	AwaitingPayee         bool              `json:"awaitingPayee"`
	RefundFailedPaymentID *string           `json:"refundFailedPaymentId"`
	RefundLimit           int64             `json:"refundLimit"`
}

// RefundProposalBrief 是用户可见的退款提议摘要。
type RefundProposalBrief struct {
	Amount   int64  `json:"amount"`
	Kind     string `json:"kind"`
	Decision string `json:"decision"`
}

// UserTicket 是用户看到的对话（cs.yaml UserTicket）。
type UserTicket struct {
	TicketID              string               `json:"ticketId"`
	OrderID               *string              `json:"orderId"`
	Category              string               `json:"category"`
	Entry                 string               `json:"entry"`
	CreatedAt             time.Time            `json:"createdAt"`
	Status                string               `json:"status"`
	ClosedAt              *time.Time           `json:"closedAt"`
	Messages              []Message            `json:"messages"`
	RefundProposal        *RefundProposalBrief `json:"refundProposal"`
	AwaitingPayee         bool                 `json:"awaitingPayee"`
	RefundFailedPaymentID *string              `json:"refundFailedPaymentId"`
}

// TicketBrief 是对话摘要。
type TicketBrief struct {
	TicketID  string    `json:"ticketId"`
	Category  string    `json:"category"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
}

// Admin 视图。
func (t *Ticket) Admin(limit int64) AdminTicket {
	msgs, acts := t.Messages, t.Actions
	if msgs == nil {
		msgs = []Message{}
	}
	if acts == nil {
		acts = []Action{}
	}
	return AdminTicket{
		TicketID: t.TicketID, UserID: t.UserID, OrderID: t.OrderID, Category: t.Category, Entry: t.Entry,
		CreatedAt: t.CreatedAt, Status: t.Status, ClosedAt: t.ClosedAt, Assignee: t.Assignee, AssigneeName: t.AssigneeName,
		Messages: msgs, Facts: t.Facts, RefundProposal: t.RefundProposal, ForceEndProposal: t.ForceEndProposal, ForceEnd: t.ForceEnd,
		Actions: acts, AwaitingPayee: t.AwaitingPayee, RefundFailedPaymentID: t.RefundFailedPaymentID, RefundLimit: limit,
	}
}

// User 视图。
func (t *Ticket) User() UserTicket {
	msgs := t.Messages
	if msgs == nil {
		msgs = []Message{}
	}
	u := UserTicket{TicketID: t.TicketID, OrderID: t.OrderID, Category: t.Category, Entry: t.Entry, CreatedAt: t.CreatedAt,
		Status: t.Status, ClosedAt: t.ClosedAt, Messages: msgs, AwaitingPayee: t.AwaitingPayee}
	if t.AwaitingPayee {
		u.RefundFailedPaymentID = t.RefundFailedPaymentID
	}
	if p := t.RefundProposal; p != nil {
		u.RefundProposal = &RefundProposalBrief{Amount: p.Amount, Kind: p.Kind, Decision: p.Decision}
	}
	return u
}

// Brief 视图。
func (t *Ticket) Brief() TicketBrief {
	return TicketBrief{TicketID: t.TicketID, Category: t.Category, Status: t.Status, CreatedAt: t.CreatedAt}
}
