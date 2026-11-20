package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"bike/onboard/internal/content"

	"github.com/jackc/pgx/v5"
)

// 接入智能体的工具后端：读接入规范、读测试结果、写修改建议、写验收报告摘要。
// 智能体本身（运行时与模型）未定；这里只提供它要用的能力与约束：
//   - 只能给失败用例写建议，不能改用例结果；
//   - 只能给已提交的验收报告写摘要，不能改结论与数字，不能确认验收（确认只有接入负责人的页面接口）。

// CaseDetail 给智能体读的用例结果，含判定所依据的事实。
type CaseDetail struct {
	Case
	Facts map[string]any `json:"facts"`
}

// RunDetail 给智能体读的一轮测试。
type RunDetail struct {
	Run
	Cases []CaseDetail `json:"cases"`
}

func (a *App) agentActor() Actor {
	return Actor{Kind: "智能体", ID: a.Cfg.AgentAccountID, Name: "接入智能体"}
}

// AgentSpec 读接入规范。
func (a *App) AgentSpec() content.Spec { return content.LoadSpec() }

// AgentRun 读一轮测试（runId 为空时取该品牌最新一轮）。
func (a *App) AgentRun(ctx context.Context, runID, brandID string) (*RunDetail, error) {
	if runID == "" {
		in, err := a.integByBrand(ctx, a.DB, brandID)
		if err != nil {
			return nil, err
		}
		lr, err := a.latestRun(ctx, in.IntegrationID)
		if err != nil {
			return nil, err
		}
		if lr == nil {
			return nil, errf(404, "NOT_FOUND", "该品牌还没有测试轮次")
		}
		runID = lr.RunID
	}
	run, err := a.loadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	d := &RunDetail{Run: *run}
	rows, err := a.DB.Query(ctx, `SELECT case_id, facts FROM run_cases WHERE run_id=$1 ORDER BY seq`, runID)
	if err != nil {
		return nil, err
	}
	facts := map[string]map[string]any{}
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		var f map[string]any
		_ = json.Unmarshal(raw, &f)
		facts[id] = f
	}
	rows.Close()
	for _, c := range run.Cases {
		d.Cases = append(d.Cases, CaseDetail{Case: c, Facts: facts[c.CaseID]})
	}
	return d, nil
}

// AgentReport 读验收报告与所依据的事实。
func (a *App) AgentReport(ctx context.Context, brandID string) (*AcceptanceView, error) {
	in, err := a.integByBrand(ctx, a.DB, brandID)
	if err != nil {
		return nil, err
	}
	p, err := a.params(ctx)
	if err != nil {
		return nil, err
	}
	return a.acceptanceView(ctx, in, p)
}

// AgentWriteSuggestion 给失败用例写修改建议。
func (a *App) AgentWriteSuggestion(ctx context.Context, runID, caseID, suggestion string) error {
	suggestion = strings.TrimSpace(suggestion)
	if utf8.RuneCountInString(suggestion) < 4 {
		return errf(400, "INVALID", "修改建议至少 4 个字")
	}
	return a.inTx(ctx, func(tx pgx.Tx) error {
		var result string
		var integ string
		err := tx.QueryRow(ctx, `SELECT c.result, r.integration_id FROM run_cases c JOIN runs r ON r.run_id=c.run_id WHERE c.run_id=$1 AND c.case_id=$2 FOR UPDATE`, runID, caseID).Scan(&result, &integ)
		if isNoRows(err) {
			return errf(404, "NOT_FOUND", "没有找到该用例")
		}
		if err != nil {
			return err
		}
		if result != "失败" {
			return errf(409, "CONFLICT", "只能给失败的用例写修改建议")
		}
		if _, err := tx.Exec(ctx, `UPDATE run_cases SET suggestion=$3, suggested_by=$4 WHERE run_id=$1 AND case_id=$2`, runID, caseID, suggestion, a.Cfg.AgentAccountID); err != nil {
			return err
		}
		return a.audit(ctx, tx, a.agentActor(), "提出联调建议", "integration", integ,
			fmt.Sprintf("%s 用例 %s 写入修改建议", runID, caseID), nil, nil, map[string]any{"suggestion": suggestion})
	})
}

// AgentWriteReportSummary 给已提交的验收报告写摘要。结论与数字不可改。
func (a *App) AgentWriteReportSummary(ctx context.Context, reportID, summary string) error {
	summary = strings.TrimSpace(summary)
	if utf8.RuneCountInString(summary) < 4 {
		return errf(400, "INVALID", "报告摘要至少 4 个字")
	}
	return a.inTx(ctx, func(tx pgx.Tx) error {
		var integ, result string
		err := tx.QueryRow(ctx, `SELECT integration_id, result FROM reports WHERE report_id=$1 FOR UPDATE`, reportID).Scan(&integ, &result)
		if isNoRows(err) {
			return errf(404, "NOT_FOUND", "没有找到该验收报告")
		}
		if err != nil {
			return err
		}
		if result != "待确认" {
			return errf(409, "CONFLICT", "验收报告已确认，不能再改摘要")
		}
		if _, err := tx.Exec(ctx, `UPDATE reports SET summary=$2, summary_by=$3 WHERE report_id=$1`, reportID, summary, a.Cfg.AgentAccountID); err != nil {
			return err
		}
		return a.audit(ctx, tx, a.agentActor(), "撰写验收摘要", "integration", integ,
			fmt.Sprintf("验收报告 %s 写入摘要", reportID), nil, nil, map[string]any{"summary": summary})
	})
}

// AgentBackend 把 App 适配成接入智能体工具的后端（agenttools.Backend）。
type AgentBackend struct{ A *App }

func (b AgentBackend) Spec() any { return b.A.AgentSpec() }
func (b AgentBackend) TestResults(ctx context.Context, runID, brandID string) (any, error) {
	return b.A.AgentRun(ctx, runID, brandID)
}
func (b AgentBackend) Report(ctx context.Context, brandID string) (any, error) {
	return b.A.AgentReport(ctx, brandID)
}
func (b AgentBackend) WriteSuggestion(ctx context.Context, runID, caseID, text string) error {
	return b.A.AgentWriteSuggestion(ctx, runID, caseID, text)
}
func (b AgentBackend) WriteReportSummary(ctx context.Context, reportID, text string) error {
	return b.A.AgentWriteReportSummary(ctx, reportID, text)
}
