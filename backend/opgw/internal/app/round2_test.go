package app

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// 第二轮：接入凭证录入与修改、只读与退出、UTF-8、保留期、开锁失败事件

func createBody(m map[string]any) map[string]any {
	b := map[string]any{"holderType": "运营商", "holderId": "OP09", "holderName": "新野出行", "operatorBaseUrl": "https://op09.example.com/api", "ipWhitelist": []string{"1.2.3.4", "10.0.0.0/24"}}
	for k, v := range m {
		b[k] = v
	}
	return b
}

func TestCreateCredential(t *testing.T) {
	e := newEnv(t, nil)
	tk := adminToken(e, tokenOpt{})
	st, m := e.admin("POST", "/api/opgw/credentials", tk, "c1", createBody(map[string]any{"reason": "新运营商签约接入"}))
	if st != 200 {
		t.Fatalf("%d %v", st, m)
	}
	secret := m["secret"].(string)
	c := m["credential"].(map[string]any)
	if !strings.HasPrefix(secret, "sk_") || c["credentialId"] != "CRD0001" || c["holderType"] != "运营商" || c["holderName"] != "新野出行" ||
		c["operatorBaseUrl"] != "https://op09.example.com/api" || c["baseUrl"] != c["operatorBaseUrl"] || c["status"] != "有效" ||
		c["keyMasked"] != "****"+secret[len(secret)-4:] || len(c["ipWhitelist"].([]any)) != 2 || c["operatorIds"].([]any)[0] != "OP09" {
		t.Fatalf("%v", m)
	}
	// 之后读取只有尾号，没有密钥明文
	st, m = e.admin("GET", "/api/opgw/credentials/CRD0001", tk, "", nil)
	if st != 200 || m["keyMasked"] != "****"+secret[len(secret)-4:] || m["secret"] != nil {
		t.Fatalf("%d %v", st, m)
	}
	// 密钥明文不入库、幂等记录里也是密文
	var enc string
	_ = testPool.QueryRow(context.Background(), `SELECT secret_enc FROM credentials`).Scan(&enc)
	var stored []byte
	_ = testPool.QueryRow(context.Background(), `SELECT body FROM idempotency WHERE scope LIKE 'create:%'`).Scan(&stored)
	if strings.Contains(enc, secret) || strings.Contains(string(stored), secret) {
		t.Fatal("密钥不得明文入库")
	}
	// 同一幂等键重复：第一次的结果，不再新建
	st, m2 := e.admin("POST", "/api/opgw/credentials", tk, "c1", createBody(map[string]any{"reason": "新运营商签约接入"}))
	if st != 200 || m2["secret"] != secret {
		t.Fatalf("幂等：%d %v", st, m2)
	}
	var n int
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM credentials`).Scan(&n)
	if n != 1 {
		t.Fatal(n)
	}
	// 录入的密钥可用来验签，地址用于向运营商发指令
	if _, got := e.credByApp(c["appId"].(string)); got != secret {
		t.Fatal("登记的密钥与返回的不一致")
	}
	// 留痕：动作、操作人是人、无密钥
	audits := outboxPayloads(t, "opgw.audit.recorded")
	last := audits[len(audits)-1]
	p := last["payload"].(map[string]any)
	if p["action"] != "录入接入凭证" || p["reason"] != "新运营商签约接入" || last["actor"].(map[string]any)["kind"] != "人" || strings.Contains(string(mustJSON(audits)), secret) {
		t.Fatalf("%v", last)
	}
	// 重复录入：持有方已有凭证
	if st, m = e.admin("POST", "/api/opgw/credentials", tk, "c2", createBody(nil)); st != 409 || m["code"] != "CONFLICT" {
		t.Fatalf("%d %v", st, m)
	}
	// 首次已接入不再自动生成（已被覆盖）
	e.accessChanged("A1", "BR09", "OP09", "已接入")
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM credentials`).Scan(&n)
	if n != 1 {
		t.Fatalf("已登记的运营商不再自动生成：%d", n)
	}
}

func TestCreateProviderCredential(t *testing.T) {
	e := newEnv(t, nil)
	tk := adminToken(e, tokenOpt{})
	// 服务商必须写明覆盖
	if st, m := e.admin("POST", "/api/opgw/credentials", tk, "", createBody(map[string]any{"holderType": "车联网服务商", "holderId": "SP07"})); st != 400 || m["code"] != "INVALID" {
		t.Fatalf("%d %v", st, m)
	}
	st, m := e.admin("POST", "/api/opgw/credentials", tk, "", createBody(map[string]any{"holderType": "车联网服务商", "holderId": "SP07", "operatorIds": []string{"OP11", "OP12", "OP11"}}))
	if st != 200 || len(m["credential"].(map[string]any)["operatorIds"].([]any)) != 2 {
		t.Fatalf("%d %v", st, m)
	}
	// 覆盖冲突
	if st, m = e.admin("POST", "/api/opgw/credentials", tk, "", createBody(map[string]any{"holderType": "运营商", "holderId": "OP12"})); st != 409 || m["code"] != "CONFLICT" {
		t.Fatalf("%d %v", st, m)
	}
	// 运营商自行接入不能写别家
	if st, m = e.admin("POST", "/api/opgw/credentials", tk, "", createBody(map[string]any{"operatorIds": []string{"OP10"}})); st != 400 {
		t.Fatalf("%d %v", st, m)
	}
	// 品牌首次已接入：并入已登记的服务商凭证，不另生成
	e.accessChanged("P1", "BR11", "OP11", "已接入")
	var n int
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM credentials`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d", n)
	}
}

func TestCreateCredentialValidation(t *testing.T) {
	e := newEnv(t, nil)
	tk := adminToken(e, tokenOpt{})
	cases := []map[string]any{
		{"holderType": "别的"},
		{"holderId": ""},
		{"operatorBaseUrl": ""},
		{"operatorBaseUrl": "ftp://x.example.com"},
		{"operatorBaseUrl": "https://u:p@x.example.com"},
		{"operatorBaseUrl": "/relative"},
		{"ipWhitelist": []string{"not-an-ip"}},
	}
	for i, c := range cases {
		if st, m := e.admin("POST", "/api/opgw/credentials", tk, "", createBody(c)); st != 400 || m["code"] != "INVALID" {
			t.Fatalf("用例 %d：%d %v", i, st, m)
		}
	}
	if st, m := e.admin("POST", "/api/opgw/credentials", tk, "", createBody(map[string]any{"reason": "短"})); st != 400 || m["code"] != "REASON_REQUIRED" {
		t.Fatalf("%d %v", st, m)
	}
	// 权限、只读、范围
	if st, m := e.admin("POST", "/api/opgw/credentials", adminToken(e, tokenOpt{perms: []string{"order.view"}}), "", createBody(nil)); st != 403 || m["code"] != "FORBIDDEN" {
		t.Fatalf("%d %v", st, m)
	}
	if st, m := e.admin("POST", "/api/opgw/credentials", adminToken(e, tokenOpt{readonly: true}), "", createBody(nil)); st != 403 || m["code"] != "READONLY" {
		t.Fatalf("%d %v", st, m)
	}
	if st, _ := e.admin("POST", "/api/opgw/credentials", "", "", createBody(nil)); st != 401 {
		t.Fatal(st)
	}
	op1 := "OP01"
	if st, m := e.admin("POST", "/api/opgw/credentials", adminToken(e, tokenOpt{scope: &op1}), "", createBody(nil)); st != 403 || m["code"] != "SCOPE_DENIED" {
		t.Fatalf("%d %v", st, m)
	}
	if st, _ := e.admin("POST", "/api/opgw/credentials", adminToken(e, tokenOpt{scope: &op1}), "", createBody(map[string]any{"holderId": "OP01"})); st != 200 {
		t.Fatal("带范围的账号可为本运营商录入")
	}
	var n int
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM credentials`).Scan(&n)
	if n != 1 {
		t.Fatalf("校验失败不得入库：%d", n)
	}
}

func TestUpdateCredential(t *testing.T) {
	e := newEnv(t, nil)
	e.seedOperators() // CRD0001=OP01, CRD0002=SP01(OP02)
	tk := adminToken(e, tokenOpt{})
	// 事由必填
	if st, m := e.admin("PATCH", "/api/opgw/credentials/CRD0001", tk, "", map[string]any{"operatorBaseUrl": "https://new.example.com"}); st != 400 || m["code"] != "REASON_REQUIRED" {
		t.Fatalf("%d %v", st, m)
	}
	if st, m := e.admin("PATCH", "/api/opgw/credentials/CRD0001", tk, "", map[string]any{"reason": "只有事由没有改动"}); st != 400 || m["code"] != "INVALID" {
		t.Fatalf("%d %v", st, m)
	}
	if st, m := e.admin("PATCH", "/api/opgw/credentials/CRD0001", tk, "", map[string]any{"reason": "地址写错了", "operatorBaseUrl": "nope"}); st != 400 || m["code"] != "INVALID" {
		t.Fatalf("%d %v", st, m)
	}
	st, m := e.admin("PATCH", "/api/opgw/credentials/CRD0001", tk, "u1", map[string]any{"reason": "运营商换了出口地址", "operatorBaseUrl": "https://new.example.com", "ipWhitelist": []string{"9.9.9.9"}, "holderName": "青禾新名"})
	if st != 200 || m["operatorBaseUrl"] != "https://new.example.com" || m["holderName"] != "青禾新名" || len(m["ipWhitelist"].([]any)) != 1 || m["keyMasked"] == nil {
		t.Fatalf("%d %v", st, m)
	}
	audits := outboxPayloads(t, "opgw.audit.recorded")
	p := audits[len(audits)-1]["payload"].(map[string]any)
	if p["action"] != "修改接入凭证" || p["reason"] != "运营商换了出口地址" || p["before"].(map[string]any)["operatorBaseUrl"] != e.op.URL {
		t.Fatalf("%v", p)
	}
	// 环境变量只是兜底：管理端改过的不再被启动同步覆盖
	e.a.Cfg.BaseURLs = map[string]string{"OP01": "https://env.example.com"}
	e.a.Cfg.HolderNames = map[string]string{"OP01": "环境名称"}
	e.a.Cfg.IPWhitelists = map[string][]string{"OP01": {"1.1.1.1"}}
	if err := e.a.SyncHolderSettings(context.Background()); err != nil {
		t.Fatal(err)
	}
	c, _ := e.cred("OP01")
	if c.BaseURL != "https://new.example.com" || c.HolderName != "青禾新名" || c.IPWhitelist[0] != "9.9.9.9" {
		t.Fatalf("%+v", c)
	}
	// 未经管理端登记的仍按环境变量兜底
	e.a.Cfg.BaseURLs = map[string]string{"SP01": "https://env-sp.example.com"}
	if err := e.a.SyncHolderSettings(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c2, _ := e.cred("OP02"); c2.BaseURL != "https://env-sp.example.com" {
		t.Fatalf("%+v", c2)
	}
	// 覆盖范围：运营商凭证不可改；服务商可改，不得抢别家
	if st, m = e.admin("PATCH", "/api/opgw/credentials/CRD0001", tk, "", map[string]any{"reason": "想改覆盖范围", "operatorIds": []string{"OP01", "OP05"}}); st != 400 {
		t.Fatalf("%d %v", st, m)
	}
	if st, m = e.admin("PATCH", "/api/opgw/credentials/CRD0002", tk, "", map[string]any{"reason": "抢运营商试试", "operatorIds": []string{"OP02", "OP01"}}); st != 409 || m["code"] != "CONFLICT" {
		t.Fatalf("%d %v", st, m)
	}
	st, m = e.admin("PATCH", "/api/opgw/credentials/CRD0002", tk, "", map[string]any{"reason": "服务商新增代接", "operatorIds": []string{"OP02", "OP06", "OP07"}})
	if st != 200 || len(m["operatorIds"].([]any)) != 3 {
		t.Fatalf("%d %v", st, m)
	}
	audits = outboxPayloads(t, "opgw.audit.recorded")
	if audits[len(audits)-1]["payload"].(map[string]any)["action"] != "调整凭证范围" {
		t.Fatalf("%v", audits[len(audits)-1])
	}
	// 范围账号不得调整覆盖
	op2 := "OP02"
	if st, m = e.admin("PATCH", "/api/opgw/credentials/CRD0002", adminToken(e, tokenOpt{scope: &op2}), "", map[string]any{"reason": "范围账号调整", "operatorIds": []string{"OP02"}}); st != 403 || m["code"] != "SCOPE_DENIED" {
		t.Fatalf("%d %v", st, m)
	}
	// 停用后不能改；只读、越界、不存在
	e.admin("POST", "/api/opgw/credentials/CRD0001/disable", tk, "", map[string]string{"reason": "运营商申请暂停接入"})
	if st, m = e.admin("PATCH", "/api/opgw/credentials/CRD0001", tk, "", map[string]any{"reason": "停用后修改", "holderName": "x"}); st != 409 || m["code"] != "CREDENTIAL_DISABLED" {
		t.Fatalf("%d %v", st, m)
	}
	if st, m = e.admin("PATCH", "/api/opgw/credentials/CRD0003", adminToken(e, tokenOpt{readonly: true}), "", map[string]any{"reason": "只读账号修改", "holderName": "x"}); st != 403 || m["code"] != "READONLY" {
		t.Fatalf("%d %v", st, m)
	}
	if st, _ = e.admin("PATCH", "/api/opgw/credentials/CRD9999", tk, "", map[string]any{"reason": "不存在的凭证", "holderName": "x"}); st != 404 {
		t.Fatal(st)
	}
}

// 录入的地址确实被开锁指令使用
func TestCreatedCredentialUsedForCommands(t *testing.T) {
	e := newEnv(t, nil)
	tk := adminToken(e, tokenOpt{})
	st, m := e.admin("POST", "/api/opgw/credentials", tk, "", createBody(map[string]any{"holderId": "OP09", "operatorBaseUrl": e.op.URL}))
	if st != 200 {
		t.Fatalf("%d %v", st, m)
	}
	st, m = e.internal("/internal/opgw/unlock", "k-unlock-1", map[string]any{"orderId": "OD1", "operatorId": "OP09", "brandId": "BR09", "plateNo": "QH000001", "degraded": false})
	if st != 200 || m["result"] != "已受理" || e.opCalls != 1 {
		t.Fatalf("%d %v calls=%d", st, m, e.opCalls)
	}
}

// 全体 1：运营商退出后该范围的写操作 READONLY；查看不受影响
func TestWritesRejectedAfterOperatorExited(t *testing.T) {
	e := newEnv(t, nil)
	e.seedOperators()
	op1 := "OP01"
	tk := adminToken(e, tokenOpt{scope: &op1})
	e.event("EX1", "fleet.operator.exited", map[string]any{"operatorId": "OP01", "brandIds": []string{"BR01", "BR02"}, "exitedAt": rfc3339(e.clock), "readonlyUntil": rfc3339(e.clock.AddDate(1, 0, 0)), "reason": "合同到期退出"})
	for _, path := range []string{"/api/opgw/credentials/CRD0001/rotate", "/api/opgw/credentials/CRD0001/disable"} {
		if st, m := e.admin("POST", path, tk, "", map[string]string{"reason": "退出后试图写入"}); st != 403 || m["code"] != "READONLY" {
			t.Fatalf("%s：%d %v", path, st, m)
		}
	}
	if st, m := e.admin("PATCH", "/api/opgw/credentials/CRD0001", tk, "", map[string]any{"reason": "退出后试图修改", "holderName": "x"}); st != 403 || m["code"] != "READONLY" {
		t.Fatalf("%d %v", st, m)
	}
	if st, m := e.admin("POST", "/api/opgw/credentials", tk, "", createBody(map[string]any{"holderId": "OP01"})); st != 403 || m["code"] != "READONLY" {
		t.Fatalf("%d %v", st, m)
	}
	if st, m := e.admin("GET", "/api/opgw/credentials/CRD0001", tk, "", nil); st != 200 || m["status"] != "停用" {
		t.Fatalf("查看不受影响：%d %v", st, m)
	}
	// 平台账号（无范围）与别家不受影响
	if st, m := e.admin("PATCH", "/api/opgw/credentials/CRD0003", adminToken(e, tokenOpt{}), "", map[string]any{"reason": "平台账号照常", "holderName": "x"}); st != 200 {
		t.Fatalf("%d %v", st, m)
	}
	op4 := "OP04"
	if st, m := e.admin("PATCH", "/api/opgw/credentials/CRD0003", adminToken(e, tokenOpt{scope: &op4}), "", map[string]any{"reason": "别家照常修改", "holderName": "y"}); st != 200 {
		t.Fatalf("%d %v", st, m)
	}
	// 重复投递退出事件不出错
	e.event("EX1", "fleet.operator.exited", map[string]any{"operatorId": "OP01", "brandIds": []string{"BR01"}, "exitedAt": rfc3339(e.clock), "reason": "重复"})
	e.event("EX2", "fleet.operator.exited", map[string]any{"operatorId": "OP01", "brandIds": []string{"BR01"}, "exitedAt": rfc3339(e.clock), "reason": "又一次"})
}

func TestNonUTF8Body(t *testing.T) {
	e := newEnv(t, nil)
	tk := adminToken(e, tokenOpt{})
	gbk := []byte("{\"reason\":\"\xd6\xd0\xce\xc4\xca\xc2\xd3\xc9\"}") // GBK 的“中文事由”
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/opgw/credentials/CRD0001/rotate"},
		{"POST", "/api/opgw/credentials"},
		{"PATCH", "/api/opgw/credentials/CRD0001"},
	} {
		req, _ := http.NewRequest(c.method, e.srv.URL+c.path, bytes.NewReader(gbk))
		req.Header.Set("Authorization", "Bearer "+tk)
		st, m := do(t, req)
		if st != 400 || m["code"] != "INVALID" || !strings.Contains(m["message"].(string), "请求体不是 UTF-8 编码") {
			t.Fatalf("%s %s：%d %v", c.method, c.path, st, m)
		}
	}
	// 内部命令与运营商入口同样
	req, _ := http.NewRequest("POST", e.srv.URL+"/internal/opgw/unlock", bytes.NewReader(gbk))
	req.Header.Set("Authorization", "Bearer ride-key")
	req.Header.Set("X-Caller", "ride")
	req.Header.Set("Idempotency-Key", "k")
	if st, m := do(t, req); st != 400 || m["code"] != "INVALID" {
		t.Fatalf("%d %v", st, m)
	}
	req, _ = http.NewRequest("POST", e.srv.URL+"/open/v1/ride-events", bytes.NewReader(gbk))
	if st, m := do(t, req); st != 400 || m["code"] != "INVALID" {
		t.Fatalf("%d %v", st, m)
	}
	// 合法 UTF-8 中文照常
	e.seedOperators()
	if st, m := e.admin("POST", "/api/opgw/credentials/CRD0001/disable", tk, "", map[string]string{"reason": "中文事由正常通过"}); st != 200 {
		t.Fatalf("%d %v", st, m)
	}
}

func TestEventRetentionDays(t *testing.T) {
	c, err := LoadConfig(func(k string) string {
		return map[string]string{"DB_DSN": "x", "OPGW_SECRET_KEY": b64(make([]byte, 32))}[k]
	})
	if err != nil || c.EventRetentionDays != 30 {
		t.Fatalf("默认 30：%v %d", err, c.EventRetentionDays)
	}
	c, err = LoadConfig(func(k string) string {
		return map[string]string{"DB_DSN": "x", "OPGW_SECRET_KEY": b64(make([]byte, 32)), "EVENT_RETENTION_DAYS": "7"}[k]
	})
	if err != nil || c.EventRetentionDays != 7 {
		t.Fatalf("%v %d", err, c.EventRetentionDays)
	}
	for _, bad := range []string{"0", "-1", "abc"} {
		if _, err := LoadConfig(func(k string) string {
			return map[string]string{"DB_DSN": "x", "OPGW_SECRET_KEY": b64(make([]byte, 32)), "EVENT_RETENTION_DAYS": bad}[k]
		}); err == nil {
			t.Fatalf("%s 应报错", bad)
		}
	}
	nc, js := connectJS(t)
	defer nc.Close()
	ctx := context.Background()
	_ = js.DeleteStream(ctx, "EVT_OPGW")
	if err := EnsureStream(ctx, js, 7); err != nil {
		t.Fatal(err)
	}
	st, _ := js.Stream(ctx, "EVT_OPGW")
	info, _ := st.Info(ctx)
	if info.Config.MaxAge != 7*24*time.Hour {
		t.Fatalf("保留期 %v", info.Config.MaxAge)
	}
	// 改配置后再次确保，流的保留期随之更新
	if err := EnsureStream(ctx, js, 14); err != nil {
		t.Fatal(err)
	}
	st, _ = js.Stream(ctx, "EVT_OPGW")
	info, _ = st.Info(ctx)
	if info.Config.MaxAge != 14*24*time.Hour {
		t.Fatalf("保留期 %v", info.Config.MaxAge)
	}
	_ = js.DeleteStream(ctx, "EVT_OPGW")
}

// 发布 opgw.unlock.failed：带 reason；成功、重复不发
func TestUnlockFailedEvent(t *testing.T) {
	e := newEnv(t, nil)
	e.seedOperators()
	for i, reason := range []string{"车辆无响应", "电量不足"} {
		order := []string{"OD-F1", "OD-F2"}[i]
		if st, m := e.internal("/internal/opgw/unlock", "k-"+order, map[string]any{"orderId": order, "operatorId": "OP01", "brandId": "BR01", "plateNo": "QH000001", "degraded": false}); st != 200 {
			t.Fatalf("%d %v", st, m)
		}
		ev := map[string]any{"eventId": "EV-" + order, "type": "开锁结果", "orderId": order, "plateNo": "QH000001", "occurredAt": rfc3339(e.clock.Add(-time.Second)),
			"unlock": map[string]any{"result": "失败", "failReason": reason}}
		if st, m := e.signed("OP01", "/open/v1/ride-events", ev, nil); st != 200 || m["duplicate"] != false {
			t.Fatalf("%d %v", st, m)
		}
		// 重复到达不再发
		if st, m := e.signed("OP01", "/open/v1/ride-events", ev, nil); st != 200 || m["duplicate"] != true {
			t.Fatalf("%d %v", st, m)
		}
	}
	// 开锁成功不发
	if st, _ := e.internal("/internal/opgw/unlock", "k-ok", map[string]any{"orderId": "OD-OK", "operatorId": "OP01", "brandId": "BR01", "plateNo": "QH000002", "degraded": false}); st != 200 {
		t.Fatal(st)
	}
	if st, _ := e.signed("OP01", "/open/v1/ride-events", map[string]any{"eventId": "EV-OK", "type": "开锁结果", "orderId": "OD-OK", "plateNo": "QH000002", "occurredAt": rfc3339(e.clock),
		"unlock": map[string]any{"result": "成功"}}, nil); st != 200 {
		t.Fatal(st)
	}
	got := outboxPayloads(t, "opgw.unlock.failed")
	if len(got) != 2 {
		t.Fatalf("应发 2 条，得 %d", len(got))
	}
	p := got[0]["payload"].(map[string]any)
	if p["reason"] != "车辆无响应" || p["orderId"] != "OD-F1" || p["operatorId"] != "OP01" || p["brandId"] != "BR01" || p["plateNo"] != "QH000001" ||
		p["operatorEventId"] != "EV-OD-F1" || p["late"] != false || p["failedAt"] == nil || got[0]["key"] != "OD-F1" {
		t.Fatalf("%v", got[0])
	}
	if got[1]["payload"].(map[string]any)["reason"] != "电量不足" {
		t.Fatalf("%v", got[1])
	}
	// 载荷不含用户身份
	if strings.Contains(string(mustJSON(got)), "userId") {
		t.Fatal("不得含用户身份")
	}
}

// 失败的请求体不得被登记的幂等键锁死：400 不登记
func TestIdempotencyNotStoredFor400(t *testing.T) {
	e := newEnv(t, nil)
	tk := adminToken(e, tokenOpt{})
	if st, _ := e.admin("POST", "/api/opgw/credentials", tk, "same-key", createBody(map[string]any{"operatorBaseUrl": "nope"})); st != 400 {
		t.Fatal(st)
	}
	if st, m := e.admin("POST", "/api/opgw/credentials", tk, "same-key", createBody(nil)); st != 200 {
		t.Fatalf("改正后可用原键重发：%d %v", st, m)
	}
}

func (e *env) credByApp(appID string) (*Credential, string) {
	e.t.Helper()
	c, err := e.a.credByAppID(context.Background(), appID)
	if err != nil || c == nil {
		e.t.Fatalf("没有应用 %s：%v", appID, err)
	}
	s, _ := e.a.secretOf(c)
	return c, s
}
