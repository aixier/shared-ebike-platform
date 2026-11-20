package app

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"bike/onboard/internal/sign"
)

// trace: 任务 9 符合性测试 · R-39

// prepRun 准备一个可跑符合性测试的环境：BR05 联调中、开锁时限参数 1 秒、两次车辆清单（第二次新增车辆）。
func prepRun(t *testing.T) (*testEnv, *fakeOp) {
	e := newEnv(t)
	appID, secret := e.readyBR05()
	if _, err := testPool.Exec(context.Background(), `UPDATE params SET unlock_timeout_sec=1`); err != nil {
		t.Fatal(err)
	}
	op := newFakeOp(t, e.srv.URL, appID, secret)
	submitList := func(rows []map[string]any) {
		b, _ := json.Marshal(map[string]any{"rows": rows})
		if st, m := signedPost(e.srv.URL, "/sandbox/open/v1/vehicle-lists", appID, secret, b, time.Now(), sign.NewNonce()); st != 200 {
			t.Fatalf("提交清单: %d %v", st, m)
		}
	}
	submitList([]map[string]any{{"brandId": "BR05", "plateNo": "SM000101", "model": "市民 S1", "pileTag": "E28011B0000101"}})
	submitList([]map[string]any{{"brandId": "BR05", "plateNo": "SM000102", "model": "市民 S1", "pileTag": "E28011B0000102"}})
	return e, op
}

func (e *testEnv) startRun(op *fakeOp) Run {
	e.t.Helper()
	body := map[string]any{}
	if op != nil {
		body["operatorBaseUrl"] = op.srv.URL
	}
	st, _, raw := e.call(techOP04, "POST", "/api/onboard/integrations/BR05/runs", body)
	if st != 200 {
		e.t.Fatalf("发起测试: %d %s", st, raw)
	}
	var run Run
	mustJSON(e.t, raw, &run)
	return run
}

func caseMap(run Run) map[string]Case {
	m := map[string]Case{}
	for _, c := range run.Cases {
		m[c.CaseID] = c
	}
	return m
}

func failedIDs(run Run) []string {
	var out []string
	for _, c := range run.Cases {
		if c.Result != "通过" {
			reason := ""
			if c.FailReason != nil {
				reason = *c.FailReason
			}
			out = append(out, c.CaseID+"："+reason)
		}
	}
	return out
}

func TestConformanceAllPass(t *testing.T) {
	e, op := prepRun(t)
	run := e.startRun(op)
	if run.Summary.Total != 18 || run.Summary.Failed != 0 {
		t.Fatalf("合规实现应 18 项全部通过，失败: %v", failedIDs(run))
	}
	if run.RunID != "IN-BR05-R001" || run.Env != "联调环境" {
		t.Fatalf("轮次编号或环境不对: %s %s", run.RunID, run.Env)
	}
	if got := outboxTypes(t); got[len(got)-1] != "onboard.audit.recorded" {
		t.Fatalf("发起测试应留痕: %v", got)
	}
	// 第二轮编号递增，地址已记住
	run2 := e.startRun(nil)
	if run2.RunID != "IN-BR05-R002" || run2.Summary.Failed != 0 {
		t.Fatalf("第二轮: %s %v", run2.RunID, failedIDs(run2))
	}
	_, _, raw := e.call(techOP04, "GET", "/api/onboard/runs?brandId=BR05", nil)
	var runs []Run
	mustJSON(t, raw, &runs)
	if len(runs) != 2 || runs[0].RunID != "IN-BR05-R002" {
		t.Fatalf("轮次应新到旧: %s", raw)
	}
	if st, m, _ := e.call(techOP01, "GET", "/api/onboard/runs?brandId=BR05", nil); st != 403 || m["code"] != "SCOPE_DENIED" {
		t.Fatalf("别家运营商查轮次: %d %v", st, m)
	}
	in := e.integ(techOP04, "BR05")
	if in.LatestRun == nil || in.LatestRun.RunID != "IN-BR05-R002" || len(in.RunIDs) != 2 {
		t.Fatalf("接入记录的最新一轮不对: %+v", in.RunIDs)
	}
}

// S-08 样例：开锁结果晚于时限到达（原型样例为"开锁结果在请求后 41 秒才到达，规范要求 30 秒内"，这里按 1 秒时限缩放）
func TestConformanceLateUnlockResult(t *testing.T) {
	e, op := prepRun(t)
	op.normalDelay = 1200 * time.Millisecond
	run := e.startRun(op)
	cm := caseMap(run)
	if run.Summary.Failed != 1 || cm["M03-01"].Result != "失败" {
		t.Fatalf("只应 M03-01 失败: %v", failedIDs(run))
	}
	c := cm["M03-01"]
	if !regexp.MustCompile(`开锁结果在请求后 1\.[2-4] 秒才到达，规范要求 1 秒内`).MatchString(*c.FailReason) { // 机器忙时实测耗时会多出 0.1～0.2 秒
		t.Fatalf("失败原因应陈述事实: %s", *c.FailReason)
	}
	if c.Suggestion != nil || c.SuggestedBy != nil {
		t.Fatal("修改建议留给接入智能体，代码不得填写")
	}
}

func TestConformanceFaultyOperator(t *testing.T) {
	e := newEnv(t)
	appID, secret := e.readyBR05()
	_, _ = testPool.Exec(context.Background(), `UPDATE params SET unlock_timeout_sec=1`)
	op := newFakeOp(t, e.srv.URL, appID, secret)
	op.noIdem, op.noReplayCheck, op.noBackfill, op.noRepeat, op.noLockOnPile, op.missingDuration = true, true, true, true, true, true
	op.slowDelay = 100 * time.Millisecond // 没按"开锁慢"模拟
	run := e.startRun(op)
	cm := caseMap(run)
	want := map[string]string{
		"M01-02": "第二次开锁请求触发了第二次开锁",
		"M01-04": "触发了新的开锁结果",
		"M02-02": "没有收到锁车报告（车辆未上锁）",
		"M02-03": "未超过时限 1 秒",
		"M03-02": "锁车报告缺少 durationSec、distanceKm",
		"M03-03": "首次投递平台答复 SERVER_BUSY 后",
		"M03-04": "只收到 1 次",
		"SEC-02": "原样重放同一请求（随机数相同）答复已受理，规范要求拒绝（HTTP 401）",
	}
	for id, frag := range want {
		c := cm[id]
		if c.Result != "失败" || c.FailReason == nil || !strings.Contains(*c.FailReason, frag) {
			t.Errorf("%s 应失败且原因含 %q，实际 %s %v", id, frag, c.Result, ptrStr(c.FailReason))
		}
	}
	for _, id := range []string{"M01-01", "M01-03", "M02-01", "M02-04", "M03-01", "SEC-01"} {
		if cm[id].Result != "通过" {
			t.Errorf("%s 应通过: %v", id, ptrStr(cm[id].FailReason))
		}
	}
	for _, id := range []string{"M04-01", "M04-02", "M04-03"} {
		if cm[id].Result != "失败" || !strings.Contains(*cm[id].FailReason, "未收到本品牌的车辆清单") {
			t.Errorf("%s 没提交清单应失败: %v", id, ptrStr(cm[id].FailReason))
		}
	}
}

func ptrStr(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func TestConformancePreconditions(t *testing.T) {
	e := newEnv(t)
	e.registerBR05()
	// 未接入（还没领取联调密钥）
	if st, m, _ := e.call(techOP04, "POST", "/api/onboard/integrations/BR05/runs", nil); st != 409 || m["code"] != "CONFLICT" {
		t.Fatalf("未接入: %d %v", st, m)
	}
	e.call(techOP04, "POST", "/api/onboard/integrations/BR05/credential/rotate", map[string]any{"reason": "首次领取联调密钥"})
	// 没有运营商地址：需要向运营商发请求的用例判失败并说明
	run := e.startRun(nil)
	if c := caseMap(run)["M01-01"]; c.Result != "失败" || !strings.Contains(*c.FailReason, "没有运营商联调地址") {
		t.Fatalf("没有地址: %v", ptrStr(c.FailReason))
	}
	if st, m, _ := e.call(techOP04, "POST", "/api/onboard/integrations/BR05/runs", map[string]any{"operatorBaseUrl": "ftp://x"}); st != 400 || m["code"] != "INVALID" {
		t.Fatalf("非法地址: %d %v", st, m)
	}
	if st, m, _ := e.call(techOP01, "POST", "/api/onboard/integrations/BR05/runs", nil); st != 403 || m["code"] != "SCOPE_DENIED" {
		t.Fatalf("别家运营商: %d %v", st, m)
	}
	// 不变量：凭证吊销后不能发起测试
	e.call(techOP04, "POST", "/api/onboard/integrations/BR05/credential/revoke", map[string]any{"reason": "测试吊销凭证"})
	if st, m, _ := e.call(techOP04, "POST", "/api/onboard/integrations/BR05/runs", nil); st != 409 || m["code"] != "CREDENTIAL_DISABLED" {
		t.Fatalf("吊销后: %d %v", st, m)
	}
}
