package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// 三名执法人员、四条证据，覆盖筛选维度
func seedEvidence(e *env) (ids []string) {
	e.seedPrototype()
	a, b := e.officer("AC0503"), e.officer("AC0504")
	mk := func(tok, tag string, body map[string]any) string {
		body["photos"] = []string{e.uploadPhoto(tok, tag)}
		r := e.capture(tok, body)
		if r.status != 201 {
			e.t.Fatalf("取证失败：%s", r.raw)
		}
		return r.json(e.t)["evidenceId"].(string)
	}
	ids = append(ids,
		mk(a, "1", map[string]any{"scanResult": "码已失效", "govCode": "SF71009215", "location": []float64{104.1, 30.1}, "address": "望江路300号", "vehicleDescription": "简途车", "capturedAt": "2026-11-28T10:00:00+08:00"}),
		mk(a, "2", map[string]any{"scanResult": "无政府码", "brandId": "BR06", "location": []float64{104.2, 30.2}, "address": "和平路181号", "vehicleDescription": "途迈字样 TM610769", "capturedAt": "2026-11-29T10:00:00+08:00"}),
		mk(b, "3", map[string]any{"scanResult": "码未启用", "govCode": "SF27239795", "location": []float64{104.3, 30.3}, "address": "人民路", "vehicleDescription": "公交车队", "capturedAt": "2026-11-30T23:30:00+08:00"}),
		mk(b, "4", map[string]any{"scanResult": "无政府码", "location": []float64{104.4, 30.4}, "address": "无名路", "vehicleDescription": "白色无牌", "capturedAt": "2026-12-01T00:10:00+08:00"}),
	)
	return ids
}

func evidenceIDs(t *testing.T, r resp) []string {
	t.Helper()
	var out []string
	for _, row := range r.json(t)["rows"].([]any) {
		out = append(out, row.(map[string]any)["evidenceId"].(string))
	}
	return out
}

func TestListEvidenceFilters(t *testing.T) {
	e := newEnv(t)
	ids := seedEvidence(e)
	sup := e.supervisor()

	cases := map[string][]string{
		"":                                {ids[3], ids[2], ids[1], ids[0]},
		"?scanResult=无政府码":                {ids[3], ids[1]},
		"?brandId=BR06":                   {ids[1]},
		"?operatorId=OP03":                {ids[0]},
		"?officerId=AC0504":               {ids[3], ids[2]},
		"?from=2026-11-29&to=2026-11-30":  {ids[2], ids[1]},
		"?from=2026-12-01":                {ids[3]},
		"?q=TM610769":                     {ids[1]},
		"?q=JT542568":                     {ids[0]},
		"?q=" + ids[2]:                    {ids[2]},
		"?q=和平路":                          {ids[1]},
		"?sortKey=capturedAt&sortDir=asc": {ids[0], ids[1], ids[2], ids[3]},
		"?pageSize=2&page=2":              {ids[1], ids[0]},
	}
	for q, want := range cases {
		r := e.do("GET", "/api/enforce/evidence"+q, sup, nil)
		if r.status != 200 {
			t.Fatalf("%s：%d %s", q, r.status, r.raw)
		}
		got := evidenceIDs(t, r)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s：got %v want %v", q, got, want)
		}
	}
	r := e.do("GET", "/api/enforce/evidence?scanResult=无政府码", sup, nil)
	if r.json(t)["total"].(float64) != 2 {
		t.Fatal("total 不对")
	}
	row := r.json(t)["rows"].([]any)[1].(map[string]any)
	if row["officerName"] != "AC0503姓名" || row["brandName"] != "途迈" || row["operatorName"] != "途迈出行" {
		t.Fatalf("证据名称字段不对：%v", row)
	}
	if r := e.do("GET", "/api/enforce/evidence?from=昨天", sup, nil); r.status != 400 {
		t.Fatalf("日期不对应 400：%d", r.status)
	}
	if r := e.do("GET", "/api/enforce/evidence", e.officer("AC0503"), nil); r.status != 403 || r.code(t) != "FORBIDDEN" {
		t.Fatalf("无 evidence.view 应 403：%d", r.status)
	}
}

// 带数据范围的账号只看该运营商的证据
func TestListEvidenceScope(t *testing.T) {
	e := newEnv(t)
	ids := seedEvidence(e)
	tok := e.token("AC0301", "运营商业务", withPerms("evidence.view"), withScope("OP03"))
	if got := evidenceIDs(t, e.do("GET", "/api/enforce/evidence", tok, nil)); strings.Join(got, ",") != ids[0] {
		t.Fatalf("数据范围过滤不对：%v", got)
	}
	if r := e.do("GET", "/api/enforce/evidence/"+ids[1], tok, nil); r.status != 403 || r.code(t) != "SCOPE_DENIED" {
		t.Fatalf("越界应 403 SCOPE_DENIED：%d %s", r.status, r.raw)
	}
}

func TestGetEvidence(t *testing.T) {
	e := newEnv(t)
	ids := seedEvidence(e)
	r := e.do("GET", "/api/enforce/evidence/"+ids[0], e.supervisor(), nil)
	m := r.json(t)
	if r.status != 200 || m["vehicleId"] != "VH001061" || m["plateNo"] != "JT542568" || m["locked"] != true {
		t.Fatalf("详情不对：%s", r.raw)
	}
	if r := e.do("GET", "/api/enforce/evidence/EVnope", e.supervisor(), nil); r.status != 404 {
		t.Fatalf("不存在应 404：%d", r.status)
	}
}

func TestExportEvidence(t *testing.T) {
	e := newEnv(t)
	ids := seedEvidence(e)
	e.now = time.Date(2026, 12, 1, 10, 5, 0, 0, shanghai())
	sup := e.supervisor()

	r := e.do("POST", "/api/enforce/evidence-exports", sup, map[string]any{"filter": map[string]any{"scanResult": "无政府码"}, "reason": "移交城管线下处置"})
	if r.status != http.StatusCreated {
		t.Fatalf("导出应 201：%d %s", r.status, r.raw)
	}
	m := r.json(t)
	ex := m["export"].(map[string]any)
	if ex["exportId"] != "EV-EXP-20261201-01" || ex["by"] != "AC0001" || ex["byName"] != "AC0001姓名" || ex["count"].(float64) != 2 ||
		ex["fileName"] != "执法证据_202612011005.zip" || ex["at"] != "2026-12-01T10:05:00+08:00" {
		t.Fatalf("导出记录不对：%v", ex)
	}
	evs := ex["evidenceIds"].([]any)
	if evs[0] != ids[1] || evs[1] != ids[3] {
		t.Fatalf("导出按取证时刻旧到新：%v", evs)
	}
	if ex["filter"].(map[string]any)["scanResult"] != "无政府码" {
		t.Fatal("筛选条件未记录")
	}
	if m["csvFileName"] != "执法证据_202612011005.csv" {
		t.Fatalf("CSV 文件名不对：%v", m["csvFileName"])
	}
	csv := m["csv"].(string)
	lines := strings.Split(strings.TrimRight(csv, "\n"), "\n")
	if !strings.HasPrefix(csv, string(rune(0xFEFF))+"证据编号,取证时间,执法人员工号,核查结果,品牌,运营商,车辆描述,地址,经度,纬度,照片数,SHA-256") || len(lines) != 3 {
		t.Fatalf("CSV 不对：%q", csv)
	}
	if !strings.HasPrefix(lines[1], ids[1]+",2026-11-29T10:00:00+08:00,BAC0503,无政府码,途迈,途迈出行,途迈字样 TM610769,和平路181号,104.2,30.2,1,") {
		t.Fatalf("CSV 行不对：%s", lines[1])
	}

	// 导出留记录：导出事件 + 留痕事件
	if n := e.count("SELECT count(*) FROM outbox WHERE subject='enforce.evidence.exported' AND body->'payload'->>'count'='2' AND body->>'key'='EV-EXP-20261201-01'"); n != 1 {
		t.Fatal("证据已导出事件不对")
	}
	if n := e.count(`SELECT count(*) FROM outbox WHERE subject='enforce.audit.recorded' AND body->'payload'->>'action'='导出证据'
		AND body->'payload'->>'targetType'='evidence' AND body->'payload'->>'targetId'='EV-EXP-20261201-01' AND body->'payload'->>'reason'='移交城管线下处置'
		AND body->>'key'='evidence:EV-EXP-20261201-01' AND body->'actor'->>'id'='AC0001' AND body->'actor'->>'kind'='人'`); n != 1 {
		t.Fatal("留痕事件不对")
	}

	// 第二次导出，导出记录新到旧
	e.now = e.now.Add(time.Minute)
	if r := e.do("POST", "/api/enforce/evidence-exports", sup, map[string]any{}); r.status != 201 {
		t.Fatalf("不带条件导出全部：%d %s", r.status, r.raw)
	}
	r = e.do("GET", "/api/enforce/evidence-exports", sup, nil)
	var list []map[string]any
	_ = json.Unmarshal(r.raw, &list)
	if len(list) != 2 || list[0]["exportId"] != "EV-EXP-20261201-02" || list[0]["count"].(float64) != 4 || list[1]["byName"] != "AC0001姓名" {
		t.Fatalf("导出记录列表不对：%s", r.raw)
	}
}

func TestExportRejects(t *testing.T) {
	e := newEnv(t)
	seedEvidence(e)
	sup := e.supervisor()
	if r := e.do("POST", "/api/enforce/evidence-exports", sup, map[string]any{"filter": map[string]any{"brandId": "BR99"}}); r.status != 400 || r.code(t) != "INVALID" {
		t.Fatalf("没有符合条件的证据应 400 INVALID：%d %s", r.status, r.raw)
	}
	if r := e.do("POST", "/api/enforce/evidence-exports", sup, map[string]any{"reason": "短"}); r.status != 400 || r.code(t) != "REASON_REQUIRED" {
		t.Fatalf("事由不足 4 字应 REASON_REQUIRED：%d %s", r.status, r.raw)
	}
	viewOnly := e.token("AC0002", "主管部门管理员", withPerms("evidence.view"))
	if r := e.do("POST", "/api/enforce/evidence-exports", viewOnly, map[string]any{}); r.status != 403 {
		t.Fatalf("无 evidence.export 应 403：%d", r.status)
	}
	if r := e.do("POST", "/api/enforce/evidence-exports", e.supervisor(withReadonly()), map[string]any{}); r.status != 403 || r.code(t) != "READONLY" {
		t.Fatalf("只读账号不能导出：%d %s", r.status, r.raw)
	}
	if n := e.count("SELECT count(*) FROM evidence_exports"); n != 0 {
		t.Fatal("失败的导出不应留记录")
	}
	// 幂等
	a := e.do("POST", "/api/enforce/evidence-exports", sup, map[string]any{}, "Idempotency-Key", "x1")
	b := e.do("POST", "/api/enforce/evidence-exports", sup, map[string]any{}, "Idempotency-Key", "x1")
	if string(a.raw) != string(b.raw) || e.count("SELECT count(*) FROM evidence_exports") != 1 {
		t.Fatal("导出幂等不对")
	}
}

// ---------- 发件表发布 ----------

func TestPublishOutbox(t *testing.T) {
	e := newEnv(t)
	me := e.officer("AC0503")
	id := e.quickCapture(me, "A", "2026-12-01T09:00:00+08:00")
	_, js := connectJS(t)
	ctx := context.Background()

	if err := e.app.EnsureStream(ctx, js); err != nil {
		t.Fatal(err)
	}
	n, err := e.app.PublishPending(ctx, js)
	if err != nil || n != 3 { // 上传照片留痕、取证留痕、证据已提交
		t.Fatalf("应发布 3 条：%d %v", n, err)
	}
	if left := e.count("SELECT count(*) FROM outbox WHERE published_at IS NULL"); left != 0 {
		t.Fatal("发布后应标记")
	}
	stream, err := js.Stream(ctx, "EVT_ENFORCE")
	if err != nil {
		t.Fatal(err)
	}
	msg, err := stream.GetLastMsgForSubject(ctx, "enforce.evidence.submitted")
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	_ = json.Unmarshal(msg.Data, &env)
	if env["type"] != "enforce.evidence.submitted" || env["key"] != id || env["version"].(float64) != 1 || msg.Header.Get("Nats-Msg-Id") != env["eventId"] {
		t.Fatalf("信封不对：%s %v", msg.Data, msg.Header)
	}
	for _, f := range []string{"eventId", "occurredAt", "actor", "payload"} {
		if env[f] == nil {
			t.Fatalf("信封缺 %s", f)
		}
	}
	p := env["payload"].(map[string]any)
	for _, f := range []string{"evidenceId", "officerId", "scanResult", "vehicleId", "brandId", "operatorId", "location", "capturedAt", "sha256"} {
		if _, ok := p[f]; !ok {
			t.Fatalf("载荷缺 %s（没有值也要给 null）", f)
		}
	}
	if n, _ := e.app.PublishPending(ctx, js); n != 0 {
		t.Fatal("已发布的不应重发")
	}
}

// 总线不可用：事件留在发件表，接口照常
func TestOutboxWhenBusDown(t *testing.T) {
	e := newEnv(t)
	nc, js := connectJS(t)
	nc.Close()
	me := e.officer("AC0503")
	e.quickCapture(me, "A", "2026-12-01T09:00:00+08:00")
	if _, err := e.app.PublishPending(context.Background(), js); err == nil {
		t.Fatal("总线不可用应返回错误")
	}
	if left := e.count("SELECT count(*) FROM outbox WHERE published_at IS NULL"); left != 3 {
		t.Fatal("事件应留在发件表")
	}
	var _ jetstream.JetStream = js
}

// ---------- 服务凭证 ----------

func TestRequireCaller(t *testing.T) {
	e := newEnv(t)
	h := e.app.RequireCaller(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	call := func(caller, key string) int {
		req := httptest.NewRequest("POST", "/internal/enforce/x", nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		req.Header.Set("X-Caller", caller)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	if call("ops", "ops-secret") != 204 || call("ops", "bad") != 401 || call("cs", "ops-secret") != 401 || call("ops", "") != 401 {
		t.Fatal("服务凭证校验不对")
	}
}

// ---------- 定时任务锁 ----------

func TestJobLockRunsOnce(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	conn, err := e.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", jobLockKey); err != nil {
		t.Fatal(err)
	}
	ran := false
	if err := e.app.withJobLock(ctx, func(context.Context) error { ran = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if ran {
		t.Fatal("另一实例持锁时不应执行")
	}
	_, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", jobLockKey)
	if err := e.app.withJobLock(ctx, func(context.Context) error { ran = true; return nil }); err != nil || !ran {
		t.Fatal("拿到锁应执行")
	}
}

func TestStartJobsSchedule(t *testing.T) {
	e := newEnv(t)
	if _, err := e.app.StartJobs(context.Background(), "不是表达式"); err == nil {
		t.Fatal("cron 表达式不对应报错")
	}
	c, err := e.app.StartJobs(context.Background(), "@every 1h")
	if err != nil {
		t.Fatal(err)
	}
	c.Stop()
}
