// Package agenttools 接入智能体的工具定义（名称、说明、输入 schema、调用）。
// 运行时与模型未定，这里只提供工具；对话循环、规则文本与评测集待运行时确定后接入。
// 工具只有读与写提议，没有确认验收：验收确认是接入负责人在页面上做的（人确认点）。
package agenttools

import (
	"context"
	"encoding/json"
	"errors"
)

// Backend 工具背后的服务能力（由 app.App 适配）。
type Backend interface {
	Spec() any
	TestResults(ctx context.Context, runID, brandID string) (any, error)
	Report(ctx context.Context, brandID string) (any, error)
	WriteSuggestion(ctx context.Context, runID, caseID, text string) error
	WriteReportSummary(ctx context.Context, reportID, text string) error
}

// Tool 一个工具。
type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	Call        func(ctx context.Context, args json.RawMessage) (any, error)
}

func decode(args json.RawMessage, v any, required ...string) error {
	if err := json.Unmarshal(args, v); err != nil {
		return errors.New("参数不是合法的 JSON")
	}
	var m map[string]any
	_ = json.Unmarshal(args, &m)
	for _, k := range required {
		if s, _ := m[k].(string); s == "" {
			return errors.New("缺少参数 " + k)
		}
	}
	return nil
}

// Tools 接入智能体可用的工具。
func Tools(b Backend) []Tool {
	return []Tool{
		{
			Name:        "read_spec",
			Description: "读接入规范：名称、版本、章节与参考报文。解释失败原因、给修改建议时引用章节号。",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
			Call:        func(ctx context.Context, _ json.RawMessage) (any, error) { return b.Spec(), nil },
		},
		{
			Name:        "read_test_results",
			Description: "读一轮符合性测试：每项用例的结果、失败原因（平台记录的事实）与判定所依据的数据。runId 为空时读该品牌最新一轮。",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"runId":{"type":"string"},"brandId":{"type":"string"}}}`),
			Call: func(ctx context.Context, args json.RawMessage) (any, error) {
				var p struct{ RunID, BrandID string }
				if err := decode(args, &p); err != nil {
					return nil, err
				}
				if p.RunID == "" && p.BrandID == "" {
					return nil, errors.New("runId 与 brandId 至少给一个")
				}
				return b.TestResults(ctx, p.RunID, p.BrandID)
			},
		},
		{
			Name:        "read_acceptance",
			Description: "读某品牌的验收情况：真车数量、订单与锁车报告匹配数、时限是否达标、验收结果。结论由这些数字决定，不能改。",
			InputSchema: json.RawMessage(`{"type":"object","required":["brandId"],"properties":{"brandId":{"type":"string"}}}`),
			Call: func(ctx context.Context, args json.RawMessage) (any, error) {
				var p struct{ BrandID string }
				if err := decode(args, &p, "brandId"); err != nil {
					return nil, err
				}
				return b.Report(ctx, p.BrandID)
			},
		},
		{
			Name:        "write_case_suggestion",
			Description: "给失败的用例写修改建议（只能写失败的用例，不能改结果）。建议要指出改什么、引用规范章节。",
			InputSchema: json.RawMessage(`{"type":"object","required":["runId","caseId","suggestion"],"properties":{"runId":{"type":"string"},"caseId":{"type":"string"},"suggestion":{"type":"string","minLength":4}}}`),
			Call: func(ctx context.Context, args json.RawMessage) (any, error) {
				var p struct{ RunID, CaseID, Suggestion string }
				if err := decode(args, &p, "runId", "caseId", "suggestion"); err != nil {
					return nil, err
				}
				return map[string]bool{"ok": true}, b.WriteSuggestion(ctx, p.RunID, p.CaseID, p.Suggestion)
			},
		},
		{
			Name:        "write_acceptance_summary",
			Description: "给已提交、待确认的验收报告写摘要。只写说明文字，验收结论与数字不能改；确认由接入负责人做。",
			InputSchema: json.RawMessage(`{"type":"object","required":["reportId","summary"],"properties":{"reportId":{"type":"string"},"summary":{"type":"string","minLength":4}}}`),
			Call: func(ctx context.Context, args json.RawMessage) (any, error) {
				var p struct{ ReportID, Summary string }
				if err := decode(args, &p, "reportId", "summary"); err != nil {
					return nil, err
				}
				return map[string]bool{"ok": true}, b.WriteReportSummary(ctx, p.ReportID, p.Summary)
			},
		},
	}
}
