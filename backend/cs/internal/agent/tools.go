// Package agent 是客服智能体的工具层：读订单事实快照、读规则文本、回复、判定类别、写退款提议、写强制结束提议。
// 智能体本身（运行时、模型、提示词编排）未定，不在这里实现；工具只做取数据与写记录，不含判断。
// 退款与强制结束只能写提议，执行要经客服确认（core.ConfirmRefund / core.ForceEnd）。
// trace: backend/cs/README.md「客服智能体」；backend/cs/任务列表.md 第 6 项（智能体本身保持未开始）
package agent

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"

	"bike/cs/internal/core"
)

//go:embed rules/客服规则.md tools.schema.json
var files embed.FS

// Rules 是规则文本与当前参数。
type Rules struct {
	Text        string `json:"text"`
	RefundLimit int64  `json:"refundLimit"`
}

// Tools 是智能体可用的工具。
type Tools struct{ App *core.App }

// ReadTicket 读订单事实快照与对话。
func (t Tools) ReadTicket(ctx context.Context, ticketID string) (*core.AgentTicketView, error) {
	return t.App.AgentReadTicket(ctx, ticketID)
}

// ReadRules 读规则文本。
func (t Tools) ReadRules(ctx context.Context) (*Rules, error) {
	b, err := files.ReadFile("rules/客服规则.md")
	if err != nil {
		return nil, err
	}
	limit, err := t.App.RefundLimit(ctx)
	if err != nil {
		return nil, err
	}
	return &Rules{Text: string(b), RefundLimit: limit}, nil
}

// Reply 在对话里回复用户。
func (t Tools) Reply(ctx context.Context, ticketID, text string) error {
	_, err := t.App.AgentReply(ctx, ticketID, text)
	return err
}

// SetCategory 判定类别。
func (t Tools) SetCategory(ctx context.Context, ticketID, category string) error {
	_, err := t.App.AgentSetCategory(ctx, ticketID, category)
	return err
}

// ProposeRefund 写退款提议。
func (t Tools) ProposeRefund(ctx context.Context, ticketID string, in core.RefundProposalInput) (*core.RefundProposal, error) {
	tk, err := t.App.AgentProposeRefund(ctx, ticketID, in)
	if err != nil {
		return nil, err
	}
	return tk.RefundProposal, nil
}

// ProposeForceEnd 写强制结束提议。
func (t Tools) ProposeForceEnd(ctx context.Context, ticketID string, in core.ForceEndProposalInput) (*core.ForceEndProposal, error) {
	tk, err := t.App.AgentProposeForceEnd(ctx, ticketID, in)
	if err != nil {
		return nil, err
	}
	return tk.ForceEndProposal, nil
}

// Schema 返回工具的输入输出定义（JSON Schema），供将来接入的运行时注册工具。
func Schema() (json.RawMessage, error) {
	b, err := files.ReadFile("tools.schema.json")
	if err != nil {
		return nil, err
	}
	if !json.Valid(b) {
		return nil, fmt.Errorf("tools.schema.json 不是合法 JSON")
	}
	return b, nil
}

// Call 按工具名调用（运行时接入时的统一入口）；args 为工具输入的 JSON。
func (t Tools) Call(ctx context.Context, name, ticketID string, args json.RawMessage) (any, error) {
	switch name {
	case "read_ticket":
		return t.ReadTicket(ctx, ticketID)
	case "read_rules":
		return t.ReadRules(ctx)
	case "reply":
		var in struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, t.Reply(ctx, ticketID, in.Text)
	case "set_category":
		var in struct {
			Category string `json:"category"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, t.SetCategory(ctx, ticketID, in.Category)
	case "propose_refund":
		var in core.RefundProposalInput
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, err
		}
		return t.ProposeRefund(ctx, ticketID, in)
	case "propose_force_end":
		var in core.ForceEndProposalInput
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, err
		}
		return t.ProposeForceEnd(ctx, ticketID, in)
	}
	return nil, fmt.Errorf("没有这个工具：%s", name)
}
