package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustJSON(t *testing.T, raw []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("解析失败: %v %s", err, raw)
	}
}

func (e *testEnv) integ(w who, brand string) IntegrationView {
	e.t.Helper()
	_, _, raw := e.call(w, "GET", "/api/onboard/integrations", nil)
	var list []IntegrationView
	mustJSON(e.t, raw, &list)
	for _, in := range list {
		if in.BrandID == brand {
			return in
		}
	}
	e.t.Fatalf("没有 %s 的接入记录: %s", brand, raw)
	return IntegrationView{}
}

// trace: 任务 7；rotateCredential · R-38；首次发放时调 fleet 置为联调中
func TestRotateFirstIssueStartsIntegration(t *testing.T) {
	e := newEnv(t)
	e.registerBR05()
	st, m, raw := e.call(techOP04, "POST", "/api/onboard/integrations/BR05/credential/rotate", map[string]any{"reason": "首次领取联调密钥"})
	if st != 200 {
		t.Fatalf("%d %s", st, raw)
	}
	secret, _ := m["secret"].(string)
	cred := m["credentials"].(map[string]any)
	if !strings.HasPrefix(secret, "sk_br05_") || cred["sandboxKeyMasked"] != "sk_****"+secret[len(secret)-4:] || cred["rotatedBy"] != "AC0114" {
		t.Fatalf("轮换结果不对: %v", m)
	}
	if len(e.fleet.calls) != 1 || e.fleet.calls[0] != "/internal/fleet/brands/BR05/start-integration" {
		t.Fatalf("首次发放应调 fleet 开始联调: %v", e.fleet.calls)
	}
	in := e.integ(techOP04, "BR05")
	if in.Status != "联调中" || *in.StatusHistory[len(in.StatusHistory)-1].Note != "首次发放联调凭证" {
		t.Fatalf("应置为联调中: %+v", in.StatusHistory)
	}
	if a := auditActions(t); !contains(a, "轮换联调凭证") || !contains(a, "开始联调") || strings.Contains(string(mustMarshal(allAudits(t))), secret) {
		t.Fatalf("留痕不对或泄露了密钥: %v", a)
	}
	// 再次轮换不再调 fleet
	e.call(techOP04, "POST", "/api/onboard/integrations/BR05/credential/rotate", map[string]any{"reason": "密钥疑似泄露"})
	if len(e.fleet.calls) != 1 {
		t.Fatalf("非首次不应再调 fleet: %v", e.fleet.calls)
	}
}

func mustMarshal(v any) []byte { b, _ := json.Marshal(v); return b }

func TestRotateFleetDownRetriedByJob(t *testing.T) {
	e := newEnv(t)
	e.registerBR05()
	e.fleet.down = true
	if st, _, raw := e.call(techOP04, "POST", "/api/onboard/integrations/BR05/credential/rotate", map[string]any{"reason": "首次领取联调密钥"}); st != 200 {
		t.Fatalf("fleet 不可用不影响轮换: %d %s", st, raw)
	}
	if in := e.integ(techOP04, "BR05"); in.Status != "未接入" {
		t.Fatal("fleet 未成功时投影不变")
	}
	e.fleet.down = false
	e.app.RetryPendingStarts(t.Context())
	if in := e.integ(techOP04, "BR05"); in.Status != "联调中" {
		t.Fatalf("定时任务重试后应为联调中: %s", in.Status)
	}
}

func TestRotateRevokeRules(t *testing.T) {
	e := newEnv(t)
	e.registerBR05()
	path := "/api/onboard/integrations/BR05/credential/"
	if st, m, _ := e.call(techOP04, "POST", path+"rotate", map[string]any{"reason": "短"}); st != 400 || m["code"] != "REASON_REQUIRED" {
		t.Fatalf("事由不足 4 字: %d %v", st, m)
	}
	if st, m, _ := e.call(techOP01, "POST", path+"rotate", map[string]any{"reason": "越权轮换别家"}); st != 403 || m["code"] != "SCOPE_DENIED" {
		t.Fatalf("别家运营商: %d %v", st, m)
	}
	if st, m, _ := e.call(techOP04, "POST", "/api/onboard/integrations/BR99/credential/rotate", map[string]any{"reason": "不存在的品牌"}); st != 404 || m["code"] != "NOT_FOUND" {
		t.Fatalf("不存在: %d %v", st, m)
	}
	st, m, _ := e.call(techOP04, "POST", path+"revoke", map[string]any{"reason": "联调结束停用凭证"})
	cred, _ := m["credentials"].(map[string]any)
	if st != 200 || cred["status"] != "停用" || cred["disabledBy"] != "AC0114" || cred["disabledAt"] == nil {
		t.Fatalf("吊销: %d %v", st, m)
	}
	if st, m, _ := e.call(techOP04, "POST", path+"revoke", map[string]any{"reason": "再次吊销凭证"}); st != 409 || m["code"] != "ALREADY_DONE" {
		t.Fatalf("重复吊销: %d %v", st, m)
	}
	// 不变量：吊销后不能轮换
	if st, m, _ := e.call(techOP04, "POST", path+"rotate", map[string]any{"reason": "吊销后想轮换"}); st != 409 || m["code"] != "CREDENTIAL_DISABLED" {
		t.Fatalf("吊销后轮换: %d %v", st, m)
	}
}

// trace: getSandbox、setMockPayment · R-38
func TestSandboxAndPaymentMode(t *testing.T) {
	e := newEnv(t)
	e.registerBR05()
	st, m, raw := e.call(techOP04, "GET", "/api/onboard/sandbox", nil)
	if st != 200 || m["paymentMode"] != "正常" || len(m["simVehicles"].([]any)) != 8 || len(m["simUsers"].([]any)) != 5 || m["baseUrl"] == "" {
		t.Fatalf("联调环境: %d %s", st, raw)
	}
	creds := m["credentials"].([]any)
	if len(creds) != 1 || creds[0].(map[string]any)["brandId"] != "BR05" {
		t.Fatalf("凭证列表: %v", creds)
	}
	if st, m, _ := e.call(techOP04, "PUT", "/api/onboard/sandbox/payment-mode", map[string]any{"mode": "透支"}); st != 400 || m["code"] != "INVALID" {
		t.Fatalf("非法模式: %d %v", st, m)
	}
	if st, m, _ := e.call(techOP04, "PUT", "/api/onboard/sandbox/payment-mode", map[string]any{"mode": "余额不足", "reason": "测"}); st != 400 || m["code"] != "REASON_REQUIRED" {
		t.Fatalf("事由太短: %d %v", st, m)
	}
	if st, m, _ := e.call(techOP04, "PUT", "/api/onboard/sandbox/payment-mode", map[string]any{"mode": "余额不足"}); st != 200 || m["paymentMode"] != "余额不足" {
		t.Fatalf("切换: %d %v", st, m)
	}
	if _, m, _ := e.call(techOP04, "GET", "/api/onboard/sandbox", nil); m["paymentMode"] != "余额不足" {
		t.Fatal("切换后应生效")
	}
	if _, m, _ := e.call(techOP01, "GET", "/api/onboard/sandbox", nil); m["paymentMode"] != "正常" {
		t.Fatal("只影响本运营商")
	}
}

// trace: getIntegrationSpec · R-36
func TestSpecEndpoint(t *testing.T) {
	e := newEnv(t)
	st, m, _ := e.call(techOP04, "GET", "/api/onboard/spec", nil)
	if st != 200 || m["name"] == "" || len(m["sections"].([]any)) < 8 {
		t.Fatalf("规范: %d %v", st, m)
	}
}
