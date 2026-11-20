package iam

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func urlq(s string) string { return url.QueryEscape(s) }

// 把原型 audit.json 的样例转成各模块发来的留痕事件。
type protoAudit struct {
	AuditID    string          `json:"auditId"`
	At         string          `json:"at"`
	AccountID  string          `json:"accountId"`
	Action     string          `json:"action"`
	TargetType string          `json:"targetType"`
	TargetID   string          `json:"targetId"`
	Summary    string          `json:"summary"`
	Reason     *string         `json:"reason"`
	Before     json.RawMessage `json:"before"`
	After      json.RawMessage `json:"after"`
}

// 原型里操作人类别由账号编号推出：AC0900 系统、AC09xx 智能体、U 开头用户、其余人。
func protoKind(id string) string {
	switch {
	case id == "AC0900":
		return "系统"
	case strings.HasPrefix(id, "AC09"):
		return "智能体"
	case strings.HasPrefix(id, "U"):
		return "用户"
	}
	return "人"
}

// 动作所属的发布模块（只用于构造主题，不影响保存）。
var actionModule = map[string]string{
	"新增规则版本": "rule", "调整站点": "site", "登记桩": "site", "设置桩状态": "site", "提交车辆清单": "fleet", "发放政府码": "fleet",
	"设置运营商退出": "fleet", "确认验收": "onboard", "暂停可租": "ops", "恢复可租": "ops", "进入降级": "ops", "收紧降级": "ops", "退出降级": "ops",
	"抵扣押金": "deposit", "申请退押": "deposit", "确认退款": "cs", "确认超额退款": "cs", "驳回退款": "cs", "人工转账": "pay", "强制结束订单": "cs",
	"冻结账户": "acct", "解冻账户": "acct", "注销账户": "acct", "生成结算单": "settle", "调整结算单": "settle", "审核结算单": "settle",
	"发送结算单": "settle", "登记到账": "settle", "处理异议": "settle", "开通账号": "iam", "调整权限": "iam", "导出证据": "enforce",
}

func auditEvent(t *testing.T, a protoAudit, loc *time.Location) (string, []byte) {
	t.Helper()
	at, err := time.ParseInLocation("2006-01-02 15:04:05", a.At, loc)
	if err != nil {
		t.Fatal(err)
	}
	mod := actionModule[a.Action]
	if mod == "" {
		mod = "ops"
	}
	nullable := func(r json.RawMessage) any {
		if len(r) == 0 || string(r) == "null" {
			return nil
		}
		return r
	}
	env := map[string]any{
		"eventId":    "EV-" + a.AuditID,
		"type":       mod + ".audit.recorded",
		"version":    1,
		"occurredAt": at.Format(time.RFC3339),
		"key":        a.TargetType + ":" + a.TargetID,
		"actor":      map[string]any{"kind": protoKind(a.AccountID), "id": a.AccountID, "name": nil},
		"payload": map[string]any{
			"action": a.Action, "targetType": a.TargetType, "targetId": a.TargetID, "summary": a.Summary,
			"reason": a.Reason, "before": nullable(a.Before), "after": nullable(a.After),
		},
	}
	b, _ := json.Marshal(env)
	return mod + ".audit.recorded", b
}

func loadProtoAudit(t *testing.T) []protoAudit {
	t.Helper()
	raw, err := os.ReadFile("testdata/audit.json")
	if err != nil {
		t.Fatal(err)
	}
	var list []protoAudit
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	return list
}

func TestIngestPrototypeAuditAndQuery(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	list := loadProtoAudit(t)
	for _, a := range list {
		subj, body := auditEvent(t, a, e.svc.loc)
		if err := e.svc.HandleEvent(ctx, subj, body); err != nil {
			t.Fatalf("%s: %v", a.AuditID, err)
		}
	}
	// 重复送达按 eventId 去重
	subj, body := auditEvent(t, list[0], e.svc.loc)
	if err := e.svc.HandleEvent(ctx, subj, body); err != nil {
		t.Fatal(err)
	}
	tok := e.login("zgbm.zhou", "管理端")
	r := e.do("GET", "/api/iam/audit?pageSize=5", tok, nil)
	if r.Status != 200 {
		t.Fatal(string(r.Raw))
	}
	if int(r.Body["total"].(float64)) != len(list) {
		t.Fatalf("总数应为 %d，实际 %v", len(list), r.Body["total"])
	}
	rows := r.Body["rows"].([]any)
	// 新到旧
	var prev string
	for _, x := range rows {
		at := x.(map[string]any)["at"].(string)
		if prev != "" && at > prev {
			t.Fatalf("应按时间新到旧: %s 在 %s 之后", at, prev)
		}
		prev = at
	}
	// 留痕编号 AU + 日期 + 4 位
	first := rows[0].(map[string]any)
	if id := first["auditId"].(string); !strings.HasPrefix(id, "AU20") || len(id) != 14 {
		t.Fatalf("留痕编号格式不对: %s", id)
	}

	// 按操作人类别：智能体
	r = e.do("GET", "/api/iam/audit?actorKind="+urlq("智能体")+"&pageSize=200", tok, nil)
	want := 0
	for _, a := range list {
		if protoKind(a.AccountID) == "智能体" {
			want++
		}
	}
	if int(r.Body["total"].(float64)) != want {
		t.Fatalf("智能体留痕应 %d 条，实际 %v", want, r.Body["total"])
	}
	for _, x := range r.Body["rows"].([]any) {
		m := x.(map[string]any)
		if m["actorKind"] != "智能体" || m["actorName"] == nil {
			t.Fatalf("智能体留痕应带显示名: %v", m)
		}
	}

	// 按动作多选
	r = e.do("GET", "/api/iam/audit?action="+urlq("调整站点")+"&action="+urlq("登记桩")+"&pageSize=200", tok, nil)
	want = 0
	for _, a := range list {
		if a.Action == "调整站点" || a.Action == "登记桩" {
			want++
		}
	}
	if int(r.Body["total"].(float64)) != want {
		t.Fatalf("动作多选应 %d 条，实际 %v", want, r.Body["total"])
	}

	// 按对象
	r = e.do("GET", "/api/iam/audit?targetType=station&targetId=ST0001", tok, nil)
	for _, x := range r.Body["rows"].([]any) {
		m := x.(map[string]any)
		if m["targetType"] != "station" || m["targetId"] != "ST0001" {
			t.Fatalf("对象筛选不对: %v", m)
		}
	}
	if r.Body["total"].(float64) < 1 {
		t.Fatal("ST0001 应有留痕")
	}

	// 按账号
	r = e.do("GET", "/api/iam/audit?accountId=AC0001&pageSize=200", tok, nil)
	want = 0
	for _, a := range list {
		if a.AccountID == "AC0001" {
			want++
		}
	}
	if int(r.Body["total"].(float64)) != want {
		t.Fatalf("按账号应 %d 条，实际 %v", want, r.Body["total"])
	}
	// 人的留痕显示名取账号姓名
	if r.Body["rows"].([]any)[0].(map[string]any)["actorName"] != "周＊＊" {
		t.Fatalf("显示名不对: %s", r.Raw)
	}

	// 按时间（含边界）
	from := list[0].At
	at0, _ := time.ParseInLocation("2006-01-02 15:04:05", from, e.svc.loc)
	r = e.do("GET", "/api/iam/audit?from="+urlq(at0.Format(time.RFC3339))+"&to="+urlq(at0.Format(time.RFC3339)), tok, nil)
	if r.Body["total"].(float64) < 1 {
		t.Fatalf("时间边界应包含: %s", r.Raw)
	}

	// 关键字
	r = e.do("GET", "/api/iam/audit?q="+urlq("文昌街"), tok, nil)
	if r.Body["total"].(float64) < 1 {
		t.Fatalf("关键字匹配不到: %s", r.Raw)
	}

	// 已出现的动作，按名称排序、不重复
	r2 := e.do("GET", "/api/iam/audit/actions", tok, nil)
	var acts []string
	_ = json.Unmarshal(r2.Raw, &acts)
	seen := map[string]bool{}
	for _, a := range list {
		seen[a.Action] = true
	}
	if len(acts) != len(seen) {
		t.Fatalf("动作数应 %d，实际 %d", len(seen), len(acts))
	}
	for i := 1; i < len(acts); i++ {
		if acts[i-1] >= acts[i] {
			t.Fatalf("应按名称排序: %v", acts)
		}
	}

	// 无权限
	expectErr(t, e.do("GET", "/api/iam/audit", e.login("kf.su", "管理端"), nil), 403, "FORBIDDEN")
	// 时间格式错
	expectErr(t, e.do("GET", "/api/iam/audit?from=yesterday", tok, nil), 400, "INVALID")
}

func TestAuditAppendOnly(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a := loadProtoAudit(t)[0]
	subj, body := auditEvent(t, a, e.svc.loc)
	if err := e.svc.HandleEvent(ctx, subj, body); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(ctx, "UPDATE iam_audit SET summary='改掉'"); err == nil {
		t.Fatal("留痕不允许修改")
	}
	if _, err := e.db.Exec(ctx, "DELETE FROM iam_audit"); err == nil {
		t.Fatal("留痕不允许删除")
	}
	if _, err := e.db.Exec(ctx, "TRUNCATE iam_audit"); err == nil {
		t.Fatal("留痕不允许清空")
	}
}

func TestIngestUserAndAgentActors(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	env := func(id, kind string, name any, eventID string) []byte {
		b, _ := json.Marshal(map[string]any{
			"eventId": eventID, "type": "acct.audit.recorded", "version": 1, "occurredAt": "2026-12-01T10:00:00+08:00",
			"key": "user:U100598", "actor": map[string]any{"kind": kind, "id": id, "name": name},
			"payload": map[string]any{"action": "注销账户", "targetType": "user", "targetId": "U100598", "summary": "用户本人注销账户", "reason": nil, "before": map[string]any{"status": "正常"}, "after": map[string]any{"status": "已注销"}},
		})
		return b
	}
	if err := e.svc.HandleEvent(ctx, "acct.audit.recorded", env("U100598", "用户", nil, "E-u1")); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.HandleEvent(ctx, "acct.audit.recorded", env("AC0904", "智能体", "客服智能体", "E-a1")); err != nil {
		t.Fatal(err)
	}
	tok := e.login("zgbm.zhou", "管理端")
	r := e.do("GET", "/api/iam/audit?actorKind="+urlq("用户"), tok, nil)
	rows := r.Body["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("用户留痕应 1 条: %s", r.Raw)
	}
	m := rows[0].(map[string]any)
	if m["accountId"] != "U100598" || m["actorName"] != nil || m["after"].(map[string]any)["status"] != "已注销" {
		t.Fatalf("用户留痕不对: %v", m)
	}
	r = e.do("GET", "/api/iam/audit?accountId=AC0904", tok, nil)
	if r.Body["rows"].([]any)[0].(map[string]any)["actorName"] != "客服智能体" {
		t.Fatalf("智能体显示名不对: %s", r.Raw)
	}
}

// 解析不了或缺字段的事件不阻塞后续：留存备查后确认。
func TestRejectMalformedAudit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	bad := [][]byte{
		[]byte("not json"),
		[]byte(`{"eventId":"E1","type":"ride.audit.recorded","occurredAt":"2026-12-01T10:00:00+08:00","actor":{"kind":"人","id":"AC0001"},"payload":{"action":"","targetType":"order","targetId":"OD1","summary":"x"}}`),
		[]byte(`{"eventId":"E2","type":"ride.audit.recorded","occurredAt":"bad","actor":{"kind":"人","id":"AC0001"},"payload":{"action":"强制结束订单","targetType":"order","targetId":"OD1","summary":"x"}}`),
		[]byte(`{"eventId":"E3","type":"ride.audit.recorded","occurredAt":"2026-12-01T10:00:00+08:00","actor":{"kind":"外星人","id":"AC0001"},"payload":{"action":"强制结束订单","targetType":"order","targetId":"OD1","summary":"x"}}`),
	}
	for i, b := range bad {
		if err := e.svc.HandleEvent(ctx, "ride.audit.recorded", b); err != nil {
			t.Fatalf("第 %d 条不应返回错误（否则会无限重投）: %v", i, err)
		}
	}
	var n int
	_ = e.db.QueryRow(ctx, "SELECT count(*) FROM iam_rejected_events").Scan(&n)
	if n != len(bad) {
		t.Fatalf("应留存 %d 条，实际 %d", len(bad), n)
	}
	_ = e.db.QueryRow(ctx, "SELECT count(*) FROM iam_audit").Scan(&n)
	if n != 0 {
		t.Fatal("坏事件不应入留痕")
	}
}

// 留痕事件的 targetType 取值比 swagger 的枚举多 credential、ticket，也照收。
func TestIngestExtraTargetTypes(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for i, tt := range []string{"credential", "ticket"} {
		b := []byte(fmt.Sprintf(`{"eventId":"X%d","type":"opgw.audit.recorded","version":1,"occurredAt":"2026-12-01T10:00:00+08:00","key":"%s:C1","actor":{"kind":"人","id":"AC0403","name":null},"payload":{"action":"联调凭证轮换","targetType":"%s","targetId":"C1","summary":"轮换","reason":"到期轮换","before":null,"after":null}}`, i, tt, tt))
		if err := e.svc.HandleEvent(ctx, "opgw.audit.recorded", b); err != nil {
			t.Fatal(err)
		}
	}
	tok := e.login("zgbm.zhou", "管理端")
	r := e.do("GET", "/api/iam/audit?targetType=credential", tok, nil)
	if r.Body["total"].(float64) != 1 {
		t.Fatalf("应 1 条: %s", r.Raw)
	}
	// 同一天日序号递增
	rows := e.do("GET", "/api/iam/audit", tok, nil).Body["rows"].([]any)
	ids := []string{rows[0].(map[string]any)["auditId"].(string), rows[1].(map[string]any)["auditId"].(string)}
	if !(ids[0] == "AU202612010002" && ids[1] == "AU202612010001") {
		t.Fatalf("日序号不对: %v", ids)
	}
}
