package core

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"bike/cs/internal/auth"
	"bike/cs/internal/clients/acct"
	"bike/cs/internal/clients/deposit"
	"bike/cs/internal/httpx"
)

// UserActionRequest 是人工解冻、协助退押、协助注销的请求。
type UserActionRequest struct {
	Reason   string  `json:"reason"`
	TicketID *string `json:"ticketId"`
}

// UnfreezeResult 是 cs.yaml UnfreezeResult。
type UnfreezeResult struct {
	OK            bool   `json:"ok"`
	UserID        string `json:"userId"`
	AccountStatus string `json:"accountStatus"`
}

// AssistRefundDepositResult 是 cs.yaml AssistRefundDepositResult。
type AssistRefundDepositResult struct {
	UserID string `json:"userId"`
	Refund struct {
		AppliedAt  time.Time  `json:"appliedAt"`
		StoppedAt  *time.Time `json:"stoppedAt"`
		ExpectedAt time.Time  `json:"expectedAt"`
		Amount     int64      `json:"amount"`
		PaymentID  *string    `json:"paymentId"`
	} `json:"refund"`
	Text string `json:"text"`
}

// AssistCancelResult 是 cs.yaml AssistCancelResult。
type AssistCancelResult struct {
	OK          bool      `json:"ok"`
	UserID      string    `json:"userId"`
	CancelledAt time.Time `json:"cancelledAt"`
}

// userAction 的公共前置：事由、数据范围、关联工单须属于该用户。
func (a *App) userActionPrecheck(ctx context.Context, ad *auth.Admin, userID string, req UserActionRequest) (string, *string, error) {
	why, err := checkReason(req.Reason)
	if err != nil {
		return "", nil, err
	}
	if ad.Scope != "" {
		return "", nil, httpx.E("SCOPE_DENIED", "运营商账号不能处理用户账户")
	}
	var tid *string
	if req.TicketID != nil && strings.TrimSpace(*req.TicketID) != "" {
		id := strings.TrimSpace(*req.TicketID)
		t, err := loadTicket(ctx, a.DB, id, false)
		if err != nil {
			return "", nil, err
		}
		if t.UserID != userID {
			return "", nil, httpx.E("INVALID", "该工单不是这个用户的")
		}
		tid = &id
	}
	return why, tid, nil
}

// Unfreeze 客服写事由后调 acct 人工解冻；带工单时记一条"人工解冻"处理动作。
func (a *App) Unfreeze(ctx context.Context, ad *auth.Admin, userID string, req UserActionRequest, inKey string) (*UnfreezeResult, error) {
	why, tid, err := a.userActionPrecheck(ctx, ad, userID, req)
	if err != nil {
		return nil, err
	}
	u, err := a.Acct.Unfreeze(ctx, userID, downKey("uf", ad.AccountID+"|"+userID, inKey), acct.Command{Reason: why, TicketID: tid,
		Actor: acct.Actor{Kind: "人", ID: ad.AccountID, Name: ad.Name}})
	if err != nil {
		return nil, err
	}
	now := a.now()
	err = a.withTx(ctx, func(tx pgx.Tx) error {
		if tid != nil {
			t, err := loadTicket(ctx, tx, *tid, true)
			if err != nil {
				return err
			}
			if err := addAction(ctx, tx, t, "人工解冻", nil, ad.AccountID, now); err != nil {
				return err
			}
		}
		// 人工解冻的留痕由 acct 记（actor 取请求里的 actor），cs 只记工单本身
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &UnfreezeResult{OK: true, UserID: userID, AccountStatus: u.Account.Status}, nil
}

// AssistRefundDeposit 调 deposit 协助退押，规则与用户自助相同。
func (a *App) AssistRefundDeposit(ctx context.Context, ad *auth.Admin, userID string, req UserActionRequest, inKey string) (*AssistRefundDepositResult, error) {
	why, tid, err := a.userActionPrecheck(ctx, ad, userID, req)
	if err != nil {
		return nil, err
	}
	cmd := deposit.AssistRefundCommand{UserID: userID, Reason: why, Actor: deposit.Actor{Kind: "人", ID: ad.AccountID, Name: ad.Name}}
	if tid != nil {
		cmd.TicketID = *tid
	}
	r, err := a.Deposit.AssistRefund(ctx, downKey("dr", ad.AccountID+"|"+userID, inKey), cmd)
	if err != nil {
		return nil, err
	}
	switch r.Result {
	case "成功":
	case "失败":
		code, msg := "REFUND_BLOCKED", "暂不能退押"
		if r.FailCode != nil {
			code = *r.FailCode
		}
		if r.FailReason != nil && *r.FailReason != "" {
			msg = *r.FailReason
		}
		e := httpx.E(code, msg)
		if len(r.Blockers) > 0 {
			bs := make([]any, 0, len(r.Blockers))
			for _, b := range r.Blockers {
				var v any
				_ = json.Unmarshal(b, &v)
				bs = append(bs, v)
			}
			e.Extra = map[string]any{"blockers": bs}
		}
		return nil, e
	default:
		return nil, httpx.E("SERVER_BUSY", "退押申请的结果暂时未知，请稍后重试同一操作")
	}
	out := &AssistRefundDepositResult{UserID: userID, Text: r.Text}
	if r.Refund != nil {
		out.Refund.AppliedAt = r.Refund.AppliedAt.In(CST)
		out.Refund.StoppedAt = cstp(r.Refund.StoppedAt)
		out.Refund.ExpectedAt = r.Refund.ExpectedAt.In(CST)
		out.Refund.Amount = r.Refund.Amount
		out.Refund.PaymentID = r.Refund.PaymentID
	}
	// 协助退押的留痕由 deposit 记，cs 只记工单本身
	return out, nil
}

// AssistCancel 调 acct 协助注销，规则与用户自助相同。
func (a *App) AssistCancel(ctx context.Context, ad *auth.Admin, userID string, req UserActionRequest, inKey string) (*AssistCancelResult, error) {
	why, tid, err := a.userActionPrecheck(ctx, ad, userID, req)
	if err != nil {
		return nil, err
	}
	r, err := a.Acct.Cancel(ctx, userID, downKey("cc", ad.AccountID+"|"+userID, inKey), acct.Command{Reason: why, TicketID: tid,
		Actor: acct.Actor{Kind: "人", ID: ad.AccountID, Name: ad.Name}})
	if err != nil {
		return nil, err
	}
	// 协助注销的留痕由 acct 记，cs 只记工单本身
	return &AssistCancelResult{OK: true, UserID: userID, CancelledAt: r.CancelledAt.In(CST)}, nil
}
