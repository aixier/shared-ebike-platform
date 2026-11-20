package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// trace: 第二轮变更单 · 全体、onboard

func auditActions(t *testing.T) []string {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `SELECT body->'payload'->>'action' FROM outbox WHERE subject='onboard.audit.recorded' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

// 全体 1：运营商退出事件之后，该运营商 scope 的写操作一律 READONLY，读不受影响。
func TestOperatorExitedRejectsWrites(t *testing.T) {
	e := newEnv(t)
	e.registerBR05()
	write := func() (int, map[string]any) {
		st, m, _ := e.call(techOP04, "PUT", "/api/onboard/sandbox/payment-mode", map[string]any{"mode": "余额不足"})
		return st, m
	}
	if st, m := write(); st != 200 {
		t.Fatalf("退出前应可写: %d %v", st, m)
	}
	e.event("fleet.operator.exited", Actor{Kind: "人", ID: "AC0401"}, map[string]any{"operatorId": "OP04", "brandIds": []string{"BR05"}, "exitedAt": time.Now(), "readonlyUntil": time.Now().Add(24 * time.Hour), "reason": "线下结果：退出运营"})
	for _, c := range []struct{ method, path string }{
		{"PUT", "/api/onboard/sandbox/payment-mode"},
		{"POST", "/api/onboard/integrations/BR05/credential/rotate"},
		{"POST", "/api/onboard/integrations/BR05/credential/issue"},
		{"POST", "/api/onboard/integrations/BR05/credential/revoke"},
		{"PUT", "/api/onboard/integrations/BR05/operator-base-url"},
		{"POST", "/api/onboard/integrations/BR05/runs"},
		{"POST", "/api/onboard/integrations/BR05/real-rides"},
		{"POST", "/api/onboard/integrations/BR05/real-rides/rides"},
		{"POST", "/api/onboard/integrations/BR05/real-rides/rides/X/end"},
	} {
		st, m, _ := e.call(techOP04, c.method, c.path, map[string]any{"mode": "正常", "reason": "退出后仍想操作", "operatorBaseUrl": "http://127.0.0.1:9", "plateNo": "A", "simUserId": "SBX-U001"})
		if st != 403 || m["code"] != "READONLY" {
			t.Fatalf("%s %s 退出后应 403 READONLY: %d %v", c.method, c.path, st, m)
		}
	}
	if st, _, _ := e.call(techOP04, "GET", "/api/onboard/integrations", nil); st != 200 {
		t.Fatalf("退出后读不受影响: %d", st)
	}
	// 别家运营商不受影响
	if st, _, _ := e.call(techOP01, "PUT", "/api/onboard/sandbox/payment-mode", map[string]any{"mode": "正常"}); st != 200 {
		t.Fatalf("别家运营商仍可写: %d", st)
	}
	// 运维账号（无 scope）的写操作不因此被拒
	if st, m, _ := e.call(leadOps, "POST", "/api/onboard/acceptance/IN-BR05/confirm", map[string]any{"reason": "运营商已退出的验收"}); st == 403 && m["code"] == "READONLY" {
		t.Fatalf("运维账号不应被 READONLY 拒绝: %v", m)
	}
}

// 全体 1：只读令牌调所有写接口都是 READONLY。
func TestReadonlyTokenAllWrites(t *testing.T) {
	e := newEnv(t)
	e.registerBR05()
	roTech := techOP04
	roTech.readonly = true
	for _, c := range []struct{ method, path string }{
		{"PUT", "/api/onboard/sandbox/payment-mode"},
		{"POST", "/api/onboard/integrations/BR05/credential/rotate"},
		{"POST", "/api/onboard/integrations/BR05/credential/issue"},
		{"POST", "/api/onboard/integrations/BR05/credential/revoke"},
		{"PUT", "/api/onboard/integrations/BR05/operator-base-url"},
		{"POST", "/api/onboard/integrations/BR05/runs"},
		{"POST", "/api/onboard/integrations/BR05/real-rides"},
		{"POST", "/api/onboard/integrations/BR05/real-rides/rides"},
		{"POST", "/api/onboard/integrations/BR05/real-rides/rides/X/end"},
	} {
		st, m, _ := e.call(roTech, c.method, c.path, map[string]any{"mode": "正常", "reason": "只读令牌试写", "operatorBaseUrl": "http://127.0.0.1:9"})
		if st != 403 || m["code"] != "READONLY" {
			t.Fatalf("%s %s 只读令牌应 403 READONLY: %d %v", c.method, c.path, st, m)
		}
	}
	ro := leadOps
	ro.readonly = true
	if st, m, _ := e.call(ro, "POST", "/api/onboard/integrations/BR05/credential/restore", map[string]any{"reason": "只读令牌试恢复"}); st != 403 || m["code"] != "READONLY" {
		t.Fatalf("restore: %d %v", st, m)
	}
	if st, m, _ := e.call(ro, "POST", "/api/onboard/acceptance/IN-BR05/confirm", map[string]any{"reason": "只读令牌试确认"}); st != 403 || m["code"] != "READONLY" {
		t.Fatalf("confirm: %d %v", st, m)
	}
}

// 全体 3：非 UTF-8 的请求体返回 400 INVALID。
func TestNonUTF8Body(t *testing.T) {
	e := newEnv(t)
	e.registerBR05()
	gbk := []byte(`{"reason":"` + string([]byte{0xD6, 0xD0, 0xCE, 0xC4, 0xCA, 0xC2, 0xD3, 0xC9}) + `"}`) // “中文事由”的 GBK 字节
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/onboard/integrations/BR05/credential/rotate", bytes.NewReader(gbk))
	req.Header.Set("Authorization", "Bearer "+token(t, techOP04))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if resp.StatusCode != 400 || m["code"] != "INVALID" || !strings.Contains(m["message"].(string), "请求体不是 UTF-8 编码") {
		t.Fatalf("非 UTF-8: %d %s", resp.StatusCode, raw)
	}
}

// 联调入口：运营商系统提交的非 UTF-8 报文同样 400 INVALID（验签通过后判断）。
func TestSandboxNonUTF8Body(t *testing.T) {
	e := newEnv(t)
	appID, secret := e.readyBR05()
	body := []byte(`{"rows":[{"brandId":"BR05","plateNo":"` + string([]byte{0xD6, 0xD0, 0xCE, 0xC4}) + `"}]}`)
	st, m := signedPost(e.srv.URL, "/sandbox/open/v1/vehicle-lists", appID, secret, body, time.Now(), "n-utf8-0123456789ab")
	if st != 400 || m["code"] != "INVALID" || !strings.Contains(m["message"].(string), "UTF-8") {
		t.Fatalf("联调入口非 UTF-8: %d %v", st, m)
	}
}

// onboard：凭证签发。首次交出密钥并通知 fleet 开始联调；已签发过再调 CONFLICT。
func TestIssueCredential(t *testing.T) {
	e := newEnv(t)
	e.registerBR05()
	path := "/api/onboard/integrations/BR05/credential/issue"
	if st, m, _ := e.call(techOP04, "POST", path, map[string]any{"reason": "短"}); st != 400 || m["code"] != "REASON_REQUIRED" {
		t.Fatalf("事由: %d %v", st, m)
	}
	if st, m, _ := e.call(techOP01, "POST", path, map[string]any{"reason": "别家运营商越权"}); st != 403 || m["code"] != "SCOPE_DENIED" {
		t.Fatalf("越权: %d %v", st, m)
	}
	st, m, raw := e.call(techOP04, "POST", path, map[string]any{"reason": "首次领取联调密钥"})
	if st != 200 {
		t.Fatalf("签发: %d %s", st, raw)
	}
	secret := m["secret"].(string)
	cred := m["credentials"].(map[string]any)
	if !strings.HasPrefix(secret, "sk_br05_") || cred["sandboxKeyMasked"] != "sk_****"+secret[len(secret)-4:] || cred["status"] != "有效" {
		t.Fatalf("签发结果: %v", m)
	}
	if in := e.integ(techOP04, "BR05"); in.Status != "联调中" {
		t.Fatalf("首次签发应置为联调中: %s", in.Status)
	}
	acts := auditActions(t)
	if !contains(acts, "签发联调凭证") || !contains(acts, "开始联调") {
		t.Fatalf("留痕动作: %v", acts)
	}
	// 之后只返回尾号
	_, _, raw = e.call(techOP04, "GET", "/api/onboard/sandbox", nil)
	if strings.Contains(string(raw), secret) {
		t.Fatal("密钥明文只出现一次")
	}
	if st, m, _ := e.call(techOP04, "POST", path, map[string]any{"reason": "再次签发应被拒绝"}); st != 409 || m["code"] != "CONFLICT" {
		t.Fatalf("重复签发: %d %v", st, m)
	}
	// 签发过的凭证可继续轮换
	if st, _, _ := e.call(techOP04, "POST", "/api/onboard/integrations/BR05/credential/rotate", map[string]any{"reason": "密钥疑似泄露"}); st != 200 {
		t.Fatalf("签发后轮换: %d", st)
	}
}

// onboard：凭证恢复。接入负责人把已吊销的凭证恢复，旧密钥不恢复。
func TestRestoreCredential(t *testing.T) {
	e := newEnv(t)
	appID, oldSecret := e.readyBR05()
	path := "/api/onboard/integrations/BR05/credential/restore"
	if st, m, _ := e.call(techOP04, "POST", path, map[string]any{"reason": "运营商自己无权恢复"}); st != 403 || m["code"] != "FORBIDDEN" {
		t.Fatalf("运营商账号无权恢复: %d %v", st, m)
	}
	if st, m, _ := e.call(leadOps, "POST", path, map[string]any{"reason": "凭证本就有效"}); st != 409 || m["code"] != "ALREADY_DONE" {
		t.Fatalf("有效凭证恢复: %d %v", st, m)
	}
	if st, m, _ := e.call(leadOps, "POST", "/api/onboard/integrations/BR99/credential/restore", map[string]any{"reason": "不存在的品牌"}); st != 404 || m["code"] != "NOT_FOUND" {
		t.Fatalf("不存在: %d %v", st, m)
	}
	if st, _, _ := e.call(techOP04, "POST", "/api/onboard/integrations/BR05/credential/revoke", map[string]any{"reason": "联调结束停用凭证"}); st != 200 {
		t.Fatal("吊销失败")
	}
	if st, m, _ := e.call(leadOps, "POST", path, map[string]any{"reason": "短"}); st != 400 || m["code"] != "REASON_REQUIRED" {
		t.Fatalf("事由: %d %v", st, m)
	}
	st, m, raw := e.call(leadOps, "POST", path, map[string]any{"reason": "运营商确认误吊销，恢复凭证"})
	if st != 200 {
		t.Fatalf("恢复: %d %s", st, raw)
	}
	cred := m["credentials"].(map[string]any)
	secret := m["secret"].(string)
	if cred["status"] != "有效" || cred["disabledAt"] != nil || cred["disabledBy"] != nil || cred["sandboxAppId"] != appID || cred["rotatedBy"] != "AC0403" ||
		secret == oldSecret || cred["sandboxKeyMasked"] != "sk_****"+secret[len(secret)-4:] {
		t.Fatalf("恢复结果: %v", m)
	}
	if a := lastAudit(t); a["payload"].(map[string]any)["action"] != "恢复联调凭证" || strings.Contains(string(mustMarshal(a)), secret) {
		t.Fatalf("留痕不对或泄露密钥: %v", a)
	}
	// 恢复后运营商可再次轮换；旧密钥验签不过
	if st, _, _ := e.call(techOP04, "POST", "/api/onboard/integrations/BR05/credential/rotate", map[string]any{"reason": "恢复后换新密钥"}); st != 200 {
		t.Fatal("恢复后应能轮换")
	}
	// 运营商退出后不能恢复
	e.call(techOP04, "POST", "/api/onboard/integrations/BR05/credential/revoke", map[string]any{"reason": "退出前吊销凭证"})
	e.event("fleet.operator.exited", Actor{Kind: "人", ID: "AC0401"}, map[string]any{"operatorId": "OP04", "brandIds": []string{"BR05"}, "exitedAt": time.Now(), "reason": "线下结果：退出"})
	if st, m, _ := e.call(leadOps, "POST", path, map[string]any{"reason": "退出后不应能恢复"}); st != 409 || m["code"] != "CONFLICT" {
		t.Fatalf("退出后恢复: %d %v", st, m)
	}
}

// onboard：运营商联调地址登记。
func TestOperatorBaseURLRegistration(t *testing.T) {
	e := newEnv(t)
	e.registerBR05()
	path := "/api/onboard/integrations/BR05/operator-base-url"
	if in := e.integ(techOP04, "BR05"); in.OperatorBaseURL != nil {
		t.Fatal("未登记时为空")
	}
	for _, bad := range []string{"", "ftp://x", "not a url", "http://"} {
		if st, m, _ := e.call(techOP04, "PUT", path, map[string]any{"operatorBaseUrl": bad}); st != 400 || m["code"] != "INVALID" {
			t.Fatalf("非法地址 %q: %d %v", bad, st, m)
		}
	}
	if st, m, _ := e.call(techOP01, "PUT", path, map[string]any{"operatorBaseUrl": "https://op01.example/bike"}); st != 403 || m["code"] != "SCOPE_DENIED" {
		t.Fatalf("别家: %d %v", st, m)
	}
	if st, m, _ := e.call(techOP04, "PUT", "/api/onboard/integrations/BR99/operator-base-url", map[string]any{"operatorBaseUrl": "https://x.example"}); st != 404 || m["code"] != "NOT_FOUND" {
		t.Fatalf("不存在: %d %v", st, m)
	}
	st, m, raw := e.call(techOP04, "PUT", path, map[string]any{"operatorBaseUrl": "https://api.citybus.example/bike"})
	if st != 200 || m["operatorBaseUrl"] != "https://api.citybus.example/bike" {
		t.Fatalf("登记: %d %s", st, raw)
	}
	in := e.integ(techOP04, "BR05")
	if in.OperatorBaseURL == nil || *in.OperatorBaseURL != "https://api.citybus.example/bike" {
		t.Fatalf("接入记录应带地址: %v", in.OperatorBaseURL)
	}
	a := lastAudit(t)["payload"].(map[string]any)
	if a["action"] != "登记联调地址" || a["targetType"] != "integration" {
		t.Fatalf("留痕: %v", a)
	}
	// 可更换，以最后一次为准
	if st, _, _ := e.call(techOP04, "PUT", path, map[string]any{"operatorBaseUrl": "https://api2.citybus.example"}); st != 200 {
		t.Fatal("更换失败")
	}
	if in := e.integ(techOP04, "BR05"); *in.OperatorBaseURL != "https://api2.citybus.example" {
		t.Fatalf("以最后一次为准: %s", *in.OperatorBaseURL)
	}
}

// 兼容：发起测试的请求体里带 operatorBaseUrl 同样登记并留痕。
func TestRunBodyRegistersBaseURL(t *testing.T) {
	e, op := prepRun(t)
	e.startRun(op)
	in := e.integ(techOP04, "BR05")
	if in.OperatorBaseURL == nil || *in.OperatorBaseURL != op.srv.URL {
		t.Fatalf("测试请求体里的地址应被登记: %v", in.OperatorBaseURL)
	}
	if !contains(auditActions(t), "登记联调地址") {
		t.Fatalf("登记应留痕: %v", auditActions(t))
	}
}

// onboard：订阅 fleet.code.status_changed，刷新码状态投影。
func TestCodeStatusChangedRefreshesProjection(t *testing.T) {
	e := newEnv(t)
	e.registerBR05()
	listVehicle(e, "VH001151", "SM000001", "SF27239795")
	listVehicle(e, "VH001152", "SM000002", "SF27239796")
	if in := e.integ(techOP04, "BR05"); !strings.HasPrefix(*in.VehicleList.CodeStatus, "待启用") {
		t.Fatalf("初始: %s", *in.VehicleList.CodeStatus)
	}
	at := time.Now()
	change := func(vid, from, to string, when time.Time) {
		e.event("fleet.code.status_changed", Actor{Kind: "系统", ID: "fleet"}, map[string]any{"vehicleId": vid, "brandId": "BR05", "govCode": "SF", "from": from, "to": to, "changedAt": when})
	}
	change("VH001151", "待启用", "已启用", at)
	in := e.integ(techOP04, "BR05")
	if got := *in.VehicleList.CodeStatus; got != "已启用 1 辆，待启用 1 辆，其他 0 辆" {
		t.Fatalf("部分启用: %s", got)
	}
	change("VH001152", "待启用", "已启用", at)
	if got := *e.integ(techOP04, "BR05").VehicleList.CodeStatus; got != "已启用" {
		t.Fatalf("全部启用: %s", got)
	}
	// 迟到的旧事件不覆盖新状态
	change("VH001152", "待启用", "待启用", at.Add(-time.Hour))
	if got := *e.integ(techOP04, "BR05").VehicleList.CodeStatus; got != "已启用" {
		t.Fatalf("旧事件不应回退: %s", got)
	}
	// 清单事件迟到（早于码状态变更）也不覆盖
	e.event("fleet.vehicle.listed", Actor{Kind: "人", ID: "AC0114"}, map[string]any{"vehicleId": "VH001152", "brandId": "BR05", "operatorId": "OP04", "plateNo": "SM000002",
		"govCode": "SF27239796", "pileTagType": "RFID", "pileTag": "E1", "codeStatus": "待启用", "listedAt": at.Add(-2 * time.Hour)})
	if got := *e.integ(techOP04, "BR05").VehicleList.CodeStatus; got != "已启用" {
		t.Fatalf("迟到的清单事件不应覆盖码状态: %s", got)
	}
	// 运营商退出：逐车失效
	change("VH001151", "已启用", "已失效", at.Add(time.Minute))
	change("VH001152", "已启用", "已失效", at.Add(time.Minute))
	if got := *e.integ(techOP04, "BR05").VehicleList.CodeStatus; got != "已失效" {
		t.Fatalf("失效: %s", got)
	}
	// 未知车辆、未知取值忽略
	change("VH999999", "待启用", "已启用", at)
	e.event("fleet.code.status_changed", Actor{Kind: "系统", ID: "fleet"}, map[string]any{"vehicleId": "VH001151", "brandId": "BR05", "to": "乱写", "changedAt": at.Add(time.Hour)})
	if got := *e.integ(techOP04, "BR05").VehicleList.CodeStatus; got != "已失效" {
		t.Fatalf("未知取值应忽略: %s", got)
	}
}

// onboard：调 fleet 的开始联调与上线留痕，actor 只会是人或系统。
func TestFleetCallsActorAndAudit(t *testing.T) {
	e := newEnv(t)
	e.registerBR05()
	e.fleet.down = true
	e.call(techOP04, "POST", "/api/onboard/integrations/BR05/credential/rotate", map[string]any{"reason": "首次领取联调密钥"})
	e.fleet.down = false
	e.fleet.actors = nil
	e.app.RetryPendingStarts(context.Background())
	if len(e.fleet.actors) != 1 || e.fleet.actors[0] != "系统" {
		t.Fatalf("定时任务重试时 actor 应为系统: %v", e.fleet.actors)
	}
	if a := auditActions(t); !contains(a, "开始联调") {
		t.Fatalf("开始联调应留痕: %v", a)
	}
}

func allAudits(t *testing.T) []string {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `SELECT body::text FROM outbox WHERE subject='onboard.audit.recorded' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return out
}
