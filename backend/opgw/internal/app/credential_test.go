package app

import (
	"context"
	"strings"
	"testing"
)

// 任务 6：生产凭证

func TestCredentialGeneratedOnFirstAccess(t *testing.T) {
	e := newEnv(t, nil)
	e.accessChanged("E1", "BR06", "OP05", "联调中")
	var n int
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM credentials`).Scan(&n)
	if n != 0 {
		t.Fatal("联调中不生成生产凭证")
	}
	e.accessChanged("E2", "BR01", "OP01", "已接入")
	e.accessChanged("E3", "BR02", "OP01", "已接入") // 同一运营商第二个品牌
	e.accessChanged("E4", "BR01", "OP01", "暂停可租")
	e.accessChanged("E5", "BR01", "OP01", "已接入") // 恢复，不是首次
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM credentials`).Scan(&n)
	if n != 1 {
		t.Fatalf("每家运营商一份凭证，得 %d", n)
	}
	c, secret := e.cred("OP01")
	if c.CredentialID != "CRD0001" || c.HolderType != "运营商" || c.HolderName != "青禾出行" || c.Status != "有效" || c.BaseURL != e.op.URL {
		t.Fatalf("%+v", c)
	}
	if !strings.HasPrefix(c.AppID, "app_") || !strings.HasSuffix(secret, c.KeyMasked[4:]) || len(c.KeyMasked) != 8 {
		t.Fatalf("掩码只留末 4 位：%s", c.KeyMasked)
	}
	var enc string
	_ = testPool.QueryRow(context.Background(), `SELECT secret_enc FROM credentials`).Scan(&enc)
	if strings.Contains(enc, secret) {
		t.Fatal("密钥不得明文入库")
	}
	audits := outboxPayloads(t, "opgw.audit.recorded")
	if len(audits) != 1 {
		t.Fatalf("生成应留痕 1 条，得 %d", len(audits))
	}
	p := audits[0]["payload"].(map[string]any)
	if p["action"] != "生成生产凭证" || p["targetType"] != "credential" || audits[0]["actor"].(map[string]any)["kind"] != "系统" || audits[0]["key"] != "credential:CRD0001" {
		t.Fatalf("%v", audits[0])
	}
}

func TestListAndGetCredential(t *testing.T) {
	e := newEnv(t, nil)
	e.seedOperators()
	tk := adminToken(e, tokenOpt{})
	st, m := e.admin("GET", "/api/opgw/credentials?page=1&pageSize=2", tk, "", nil)
	if st != 200 || m["total"].(float64) != 3 || len(m["rows"].([]any)) != 2 || m["pageSize"].(float64) != 2 {
		t.Fatalf("%d %v", st, m)
	}
	row := m["rows"].([]any)[0].(map[string]any)
	if _, ok := row["secret"]; ok {
		t.Fatal("列表不返回密钥")
	}
	if _, ok := row["secretEnc"]; ok {
		t.Fatal("列表不返回密文")
	}
	st, m = e.admin("GET", "/api/opgw/credentials?operatorId=OP02", tk, "", nil)
	if st != 200 || m["total"].(float64) != 1 || m["rows"].([]any)[0].(map[string]any)["holderType"] != "车联网服务商" {
		t.Fatalf("按运营商过滤：%v", m)
	}
	st, m = e.admin("GET", "/api/opgw/credentials?status=停用", tk, "", nil)
	if st != 200 || m["total"].(float64) != 0 {
		t.Fatalf("按状态过滤：%v", m)
	}
	st, m = e.admin("GET", "/api/opgw/credentials/CRD0001", tk, "", nil)
	if st != 200 || m["credentialId"] != "CRD0001" || m["rotatedAt"] != nil {
		t.Fatalf("%d %v", st, m)
	}
	if _, ok := m["lastSeenAt"]; !ok {
		t.Fatal("没有值的字段给 null")
	}
	if st, m = e.admin("GET", "/api/opgw/credentials/CRD9999", tk, "", nil); st != 404 || m["code"] != "NOT_FOUND" {
		t.Fatalf("%d %v", st, m)
	}
	// 数据范围：带运营商范围的账号只见本家
	op1 := "OP01"
	st, m = e.admin("GET", "/api/opgw/credentials", adminToken(e, tokenOpt{scope: &op1}), "", nil)
	if st != 200 || m["total"].(float64) != 1 {
		t.Fatalf("范围过滤：%v", m)
	}
	if st, m = e.admin("GET", "/api/opgw/credentials/CRD0002", adminToken(e, tokenOpt{scope: &op1}), "", nil); st != 403 || m["code"] != "SCOPE_DENIED" {
		t.Fatalf("越界：%d %v", st, m)
	}
}

func TestRotateCredential(t *testing.T) {
	e := newEnv(t, nil)
	e.seedOperators()
	old, oldSecret := e.cred("OP01")
	tk := adminToken(e, tokenOpt{})
	if st, m := e.admin("POST", "/api/opgw/credentials/CRD0001/rotate", tk, "r1", map[string]string{"reason": "短"}); st != 400 || m["code"] != "REASON_REQUIRED" {
		t.Fatalf("%d %v", st, m)
	}
	st, m := e.admin("POST", "/api/opgw/credentials/CRD0001/rotate", tk, "r1", map[string]string{"reason": "疑似密钥泄露，立即轮换"})
	if st != 200 {
		t.Fatalf("%d %v", st, m)
	}
	secret := m["secret"].(string)
	cred := m["credential"].(map[string]any)
	if secret == oldSecret || cred["rotatedAt"] == nil || cred["rotatedBy"].(map[string]any)["id"] != "AC0501" || cred["keyMasked"] != "****"+secret[len(secret)-4:] {
		t.Fatalf("%v", m)
	}
	// 同一幂等键重复到达：返回第一次的结果，不再轮换
	st, m2 := e.admin("POST", "/api/opgw/credentials/CRD0001/rotate", tk, "r1", map[string]string{"reason": "疑似密钥泄露，立即轮换"})
	if st != 200 || m2["secret"] != secret {
		t.Fatalf("幂等：%v", m2)
	}
	var stored []byte
	_ = testPool.QueryRow(context.Background(), `SELECT body FROM idempotency WHERE scope LIKE 'rotate:%'`).Scan(&stored)
	if strings.Contains(string(stored), secret) {
		t.Fatal("幂等记录不得含密钥明文")
	}
	// 旧密钥立即失效
	now, _ := e.cred("OP01")
	if now.AppID != old.AppID {
		t.Fatal("轮换不换应用标识")
	}
	if s, _ := e.a.secretOf(now); s != secret {
		t.Fatal("新密钥未生效")
	}
	audits := outboxPayloads(t, "opgw.audit.recorded")
	last := audits[len(audits)-1]
	if p := last["payload"].(map[string]any); p["action"] != "轮换生产凭证" || p["reason"] != "疑似密钥泄露，立即轮换" || last["actor"].(map[string]any)["kind"] != "人" {
		t.Fatalf("%v", last)
	}
	if strings.Contains(string(mustJSON(audits)), secret) {
		t.Fatal("留痕不得含密钥")
	}
}

func TestDisableCredential(t *testing.T) {
	e := newEnv(t, nil)
	e.seedOperators()
	tk := adminToken(e, tokenOpt{})
	st, m := e.admin("POST", "/api/opgw/credentials/CRD0001/disable", tk, "d1", map[string]string{"reason": "运营商申请暂停接入"})
	if st != 200 || m["status"] != "停用" || m["disabledReason"] != "运营商申请暂停接入" || m["disabledBy"].(map[string]any)["kind"] != "人" {
		t.Fatalf("%d %v", st, m)
	}
	if st, m = e.admin("POST", "/api/opgw/credentials/CRD0001/disable", tk, "d2", map[string]string{"reason": "再次停用试试"}); st != 409 || m["code"] != "ALREADY_DONE" {
		t.Fatalf("%d %v", st, m)
	}
	if st, m = e.admin("POST", "/api/opgw/credentials/CRD0001/rotate", tk, "r9", map[string]string{"reason": "停用后轮换试试"}); st != 409 || m["code"] != "CREDENTIAL_DISABLED" {
		t.Fatalf("%d %v", st, m)
	}
	if st, _ = e.admin("POST", "/api/opgw/credentials/CRD9999/disable", tk, "d3", map[string]string{"reason": "不存在的凭证"}); st != 404 {
		t.Fatal(st)
	}
}

func TestOperatorExitedDisables(t *testing.T) {
	e := newEnv(t, nil)
	e.seedOperators()
	e.event("X1", "fleet.operator.exited", map[string]any{"operatorId": "OP01", "brandIds": []string{"BR01", "BR02"}, "exitedAt": rfc3339(e.clock), "reason": "合同到期退出"})
	c, _ := e.cred("OP01")
	if c.Status != "停用" || c.DisabledBy.Kind != "系统" || *c.DisabledReason != "合同到期退出" {
		t.Fatalf("%+v", c)
	}
	if o, _ := e.cred("OP04"); o.Status != "有效" {
		t.Fatal("单家停用不影响他家")
	}
}

func TestBrandExitOnlyWhenAllBrandsExit(t *testing.T) {
	e := newEnv(t, nil)
	e.seedOperators()
	e.accessChanged("Y1", "BR01", "OP01", "已退出")
	if c, _ := e.cred("OP01"); c.Status != "有效" {
		t.Fatal("还有在营品牌时不停用")
	}
	e.accessChanged("Y2", "BR02", "OP01", "已退出")
	if c, _ := e.cred("OP01"); c.Status != "停用" {
		t.Fatal("全部品牌退出后停用")
	}
}

func TestSyncHolderSettings(t *testing.T) {
	e := newEnv(t, nil)
	e.seedOperators()
	e.a.Cfg.BaseURLs = map[string]string{"OP01": "https://new.example.com"}
	e.a.Cfg.IPWhitelists = map[string][]string{"OP01": {"10.0.0.0/8"}}
	e.a.Cfg.RateLimitPerMinute = 120
	if err := e.a.SyncHolderSettings(context.Background()); err != nil {
		t.Fatal(err)
	}
	c, _ := e.cred("OP01")
	if c.BaseURL != "https://new.example.com" || len(c.IPWhitelist) != 1 || c.RateLimitPerMinute != 120 {
		t.Fatalf("%+v", c)
	}
}
