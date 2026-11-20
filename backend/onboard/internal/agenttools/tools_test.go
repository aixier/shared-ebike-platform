package agenttools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type fakeBackend struct {
	suggested map[string]string
	summary   map[string]string
}

func (f *fakeBackend) Spec() any { return map[string]string{"name": "规范"} }
func (f *fakeBackend) TestResults(ctx context.Context, runID, brandID string) (any, error) {
	if runID == "" && brandID == "" {
		return nil, errors.New("缺参数")
	}
	return map[string]string{"runId": runID, "brandId": brandID}, nil
}
func (f *fakeBackend) Report(ctx context.Context, brandID string) (any, error) {
	return map[string]string{"brandId": brandID}, nil
}
func (f *fakeBackend) WriteSuggestion(ctx context.Context, runID, caseID, text string) error {
	f.suggested[runID+"/"+caseID] = text
	return nil
}
func (f *fakeBackend) WriteReportSummary(ctx context.Context, reportID, text string) error {
	f.summary[reportID] = text
	return nil
}

// trace: README 接入智能体——工具为读接入规范、读测试结果、写报告；没有确认验收的工具
func TestTools(t *testing.T) {
	b := &fakeBackend{suggested: map[string]string{}, summary: map[string]string{}}
	tools := Tools(b)
	names := map[string]Tool{}
	for _, tl := range tools {
		if tl.Description == "" || !json.Valid(tl.InputSchema) {
			t.Fatalf("工具定义不完整: %s", tl.Name)
		}
		names[tl.Name] = tl
	}
	for _, n := range []string{"read_spec", "read_test_results", "read_acceptance", "write_case_suggestion", "write_acceptance_summary"} {
		if _, ok := names[n]; !ok {
			t.Fatalf("缺工具 %s", n)
		}
	}
	for n := range names {
		if n == "confirm_acceptance" {
			t.Fatal("智能体不能确认验收")
		}
	}
	ctx := context.Background()
	if _, err := names["read_test_results"].Call(ctx, json.RawMessage(`{"brandId":"BR05"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := names["write_case_suggestion"].Call(ctx, json.RawMessage(`{"runId":"IN-BR05-R003","caseId":"M03-01","suggestion":"先回已受理"}`)); err != nil {
		t.Fatal(err)
	}
	if b.suggested["IN-BR05-R003/M03-01"] != "先回已受理" {
		t.Fatal("建议未写入")
	}
	if _, err := names["write_case_suggestion"].Call(ctx, json.RawMessage(`{"runId":"R"}`)); err == nil {
		t.Fatal("缺必填参数应报错")
	}
	if _, err := names["write_acceptance_summary"].Call(ctx, json.RawMessage(`{"reportId":"IN-BR05-AR01","summary":"全部匹配"}`)); err != nil || b.summary["IN-BR05-AR01"] != "全部匹配" {
		t.Fatal("摘要未写入")
	}
}
