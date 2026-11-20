// Package core 是客服服务的实现：工单与对话、退款与强制结束的人工确认流程、账户处理、查询、事件收发。
// 业务判断（解释费用、提议退款与计费终点、判定类别）归客服智能体与客服本人，本包只执行状态机、权限、
// 调用其他服务的命令、留痕与事件。
// trace: backend/cs/README.md；contracts/openapi/cs.yaml；contracts/events/cs.md
package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"bike/cs/internal/auth"
	"bike/cs/internal/clients/acct"
	"bike/cs/internal/clients/bill"
	"bike/cs/internal/clients/deposit"
	"bike/cs/internal/clients/ride"
	"bike/cs/internal/httpx"
)

// Agent 是客服智能体的接入点。用户发消息后调用；运行时与模型未定，未接入时为 nil，工单留在"待处理"等客服接手。
type Agent interface {
	OnUserMessage(ctx context.Context, ticketID string) error
}

// App 持有依赖。
type App struct {
	DB      *pgxpool.Pool
	Ride    *ride.Client
	Bill    *bill.Client
	Acct    *acct.Client
	Deposit *deposit.Client

	UserKeys  auth.Keys
	AdminKeys auth.Keys
	Callers   auth.Callers

	// Now 是服务器时间，测试可替换。
	Now func() time.Time
	// Agent 为 nil 表示客服智能体未接入。
	Agent Agent
	// DefaultRefundLimit 是收到平台参数前的客服退款授权额度（分）。
	DefaultRefundLimit int64
	// EventRetentionDays 是事件流的保留天数，0 按 30。
	EventRetentionDays int
}

func (a *App) now() time.Time {
	if a.Now == nil {
		return time.Now().In(CST).Truncate(time.Second)
	}
	return a.Now().In(CST).Truncate(time.Second)
}

func staffActor(ad *auth.Admin) ActorRef {
	return ActorRef{Kind: "人", ID: ad.AccountID, Name: ad.Name}
}

// checkReason 事由至少 4 个字。
func checkReason(r string) (string, error) {
	r = strings.TrimSpace(r)
	if len([]rune(r)) < 4 {
		return "", httpx.E("REASON_REQUIRED", "请写明事由，至少 4 个字")
	}
	return r, nil
}

// yuan 把分格式化成"¥3.00"，用于给人看的消息。
func yuan(cents int64) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	return fmt.Sprintf("%s¥%d.%02d", sign, cents/100, cents%100)
}

// downKey 生成调用下游命令时的幂等键：同一入站幂等键重试得到同一下游键，没有入站键时随机。
func downKey(op, scope, inKey string) string {
	if inKey == "" {
		return newID("cs-" + op + "-")
	}
	h := sha256.Sum256([]byte(scope + "|" + inKey))
	return "cs-" + op + "-" + hex.EncodeToString(h[:16])
}

// notifyAgent 在用户发消息后交给客服智能体；未接入时什么都不做。
func (a *App) notifyAgent(ctx context.Context, ticketID string) {
	if a.Agent == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := a.Agent.OnUserMessage(cctx, ticketID); err != nil {
		slog.Warn("客服智能体处理失败，工单等客服接手", "ticketId", ticketID, "err", err)
	}
}

// scopeCheck 运营商账号只能看与操作本运营商订单的工单。
func scopeCheck(ad *auth.Admin, t *Ticket) error {
	if ad.Scope == "" {
		return nil
	}
	if t.OperatorID == nil || *t.OperatorID != ad.Scope {
		return httpx.E("SCOPE_DENIED", "该工单不在你的数据范围内")
	}
	return nil
}
