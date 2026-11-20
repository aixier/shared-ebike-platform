package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"bike/ops/internal/httpx"
)

// AgentTools 运行智能体的工具（README"运行智能体"：读健康指标、读限额使用量、写降级事件与暂停建议）。
//
// 智能体本身（运行时、模型、规则文本、评测集）未定，任务列表第 11、12 项保持"未开始"。
// 这里只给它工具，并把硬约束写进工具而不是提示词：
//   - 指标与限额使用量由代码算，智能体只读；
//   - 写暂停建议时指标值、门槛、持续分钟由代码从当前指标填入，指标没有持续低于门槛时拒绝；
//   - 智能体只能宣布进入与退出降级、提出暂停建议；收紧、停止没有工具（只有值班人能做）；
//   - 自动暂停开关开启时，建议直接执行暂停，仍留建议记录与留痕。
type AgentTools struct{ a *App }

// Agent 返回运行智能体的工具集。
func (a *App) Agent() AgentTools { return AgentTools{a: a} }

func requireAgent(actor Actor) error {
	if actor.Kind != "智能体" || actor.ID == "" {
		return httpx.Forbidden("只有运行智能体可以使用这些工具")
	}
	return nil
}

// ReadHealth 读某品牌的健康指标、持续低于门槛的分钟数、运营商通道与依赖熔断情况。
func (t AgentTools) ReadHealth(ctx context.Context, brandID string) (BrandHealth, Thresholds, error) {
	var h BrandHealth
	var th Thresholds
	err := t.a.read(ctx, func(tx pgx.Tx) error {
		var err error
		if th, err = t.a.thresholds(ctx, tx); err != nil {
			return err
		}
		h, err = t.a.brandHealth(ctx, tx, brandID, th, t.a.now())
		return err
	})
	return h, th, err
}

// ReadUsage 读降级状态与限额使用量。
func (t AgentTools) ReadUsage(ctx context.Context) (DegradeStatus, error) { return t.a.Status(ctx) }

// DeclareDegrade 宣布进入降级（与值班人手动进入同一状态机，操作人记为智能体）。
func (t AgentTools) DeclareDegrade(ctx context.Context, agent Actor, req EnterRequest) (DegradeEvent, error) {
	if err := requireAgent(agent); err != nil {
		return DegradeEvent{}, err
	}
	return t.a.Enter(ctx, agent, req)
}

// DeclareRecovery 宣布退出降级。
func (t AgentTools) DeclareRecovery(ctx context.Context, agent Actor, note string) (DegradeEvent, error) {
	if err := requireAgent(agent); err != nil {
		return DegradeEvent{}, err
	}
	return t.a.Exit(ctx, agent, &note)
}

// ProposeSuspend 对品牌自身问题提出暂停可租建议。note 是智能体的判断说明（为何是品牌自身问题而非公共问题）。
func (t AgentTools) ProposeSuspend(ctx context.Context, agent Actor, brandID, note string) (SuspendSuggestion, error) {
	if err := requireAgent(agent); err != nil {
		return SuspendSuggestion{}, err
	}
	if !httpx.ValidReason(note) {
		return SuspendSuggestion{}, httpx.New(400, "REASON_REQUIRED", "请写明判断说明")
	}
	a := t.a
	var sg SuspendSuggestion
	var auto bool
	err := a.tx(ctx, func(tx pgx.Tx) error {
		th, err := a.thresholds(ctx, tx)
		if err != nil {
			return err
		}
		h, err := a.brandHealth(ctx, tx, brandID, th, a.now())
		if err != nil {
			return err
		}
		if h.AccessStatus != "已接入" {
			return httpx.Conflict("品牌当前不是已接入，不能提出暂停建议")
		}
		if !h.Below || h.LowValue == nil || h.SustainMinutes < th.Pause.SustainMinutes {
			return httpx.Conflict("该品牌指标没有持续低于门槛，不能提出暂停建议")
		}
		var pending int
		_ = tx.QueryRow(ctx, `SELECT count(*) FROM suspend_suggestions WHERE brand_id = $1 AND status = '待确认'`, brandID).Scan(&pending)
		if pending > 0 {
			return httpx.New(409, "ALREADY_DONE", "该品牌已有待确认的暂停建议")
		}
		now := a.now()
		id, err := a.nextID(ctx, tx, "OPS-SG", now)
		if err != nil {
			return err
		}
		text := strings.TrimSpace(note)
		if _, err := tx.Exec(ctx, `INSERT INTO suspend_suggestions (id, brand_id, at, metric, value, threshold, sustain_minutes, by, by_name, note, status)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'待确认')`, id, brandID, now, h.LowMetric, *h.LowValue, *h.LowThreshold, h.SustainMinutes, agent.ID, agent.Name, text); err != nil {
			return err
		}
		if err := a.emit(ctx, tx, "ops.brand.suspend_suggested", brandID, agent, map[string]any{
			"suggestionId": id, "brandId": brandID, "metric": h.LowMetric, "value": *h.LowValue, "threshold": *h.LowThreshold,
			"sustainMinutes": h.SustainMinutes, "autoResume": autoResumeFor(h.LowMetric), "at": a.fmtTime(now),
		}); err != nil {
			return err
		}
		bid := brandID
		if err := a.raiseAlert(ctx, tx, "警告", AlertSuspendSuggested,
			fmt.Sprintf("运行智能体建议暂停 %s：%s %.1f%% 持续 %d 分钟低于门槛；%s", a.brandName(ctx, tx, brandID), h.LowMetric, *h.LowValue*100, h.SustainMinutes, text), &bid, nil, ""); err != nil {
			return err
		}
		if auto, err = a.autoPause(ctx, tx); err != nil {
			return err
		}
		ss, err := a.querySuggestions(ctx, tx, `WHERE id = $1`, id)
		if err != nil {
			return err
		}
		sg = ss[0]
		return nil
	})
	if err != nil || !auto {
		return sg, err
	}
	// 自动暂停开关开启：由智能体直接暂停
	v, th, sm := sg.Value, sg.Threshold, sg.SustainMinutes
	_, err = a.pauseBrand(ctx, agent, brandID, pauseSpec{metric: sg.Metric, value: &v, threshold: &th, sustainMinutes: &sm, note: sg.Note + "（自动暂停开关已开启）"},
		derivedKey("auto-pause", sg.ID), func(tx pgx.Tx, pauseID string) error {
			_, err := tx.Exec(ctx, `UPDATE suspend_suggestions SET status = '已确认', decided_by = $2, decided_by_name = $3, decided_at = $4,
				decision_reason = '自动暂停开关已开启', pause_id = $5 WHERE id = $1`, sg.ID, agent.ID, agent.Name, a.now(), pauseID)
			return err
		})
	if err != nil {
		return sg, err
	}
	return a.loadSuggestion(ctx, sg.ID)
}

// Todo 待办计数。
type Todo struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Count int    `json:"count"`
	Menu  string `json:"menu"`
}

// Todos 按登录人的权限点返回计数大于 0 的待办。
func (a *App) Todos(ctx context.Context, can func(perms ...string) bool) ([]Todo, error) {
	out := []Todo{}
	err := a.read(ctx, func(tx pgx.Tx) error {
		if can("ops.monitor") {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM alerts WHERE ack_at IS NULL`).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				out = append(out, Todo{"alerts", "未确认告警", n, "运行监控"})
			}
		}
		if can("degrade.closure.view", "degrade.closure.handle") {
			orders, err := queryOrders(ctx, tx, `WHERE degraded AND closed_at IS NULL`)
			if err != nil {
				return err
			}
			n, now := 0, a.now()
			for _, o := range orders {
				if a.closureStatus(o, now) == "待人工" {
					n++
				}
			}
			if n > 0 {
				out = append(out, Todo{"closure", "降级单待人工处理", n, "恢复闭合"})
			}
		}
		if can("access.pause", "ops.monitor") {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM suspend_suggestions WHERE status = '待确认'`).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				out = append(out, Todo{"suspend", "暂停可租建议待确认", n, "运行监控"})
			}
		}
		return nil
	})
	return out, err
}
