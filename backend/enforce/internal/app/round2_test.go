package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// ---------- 只读与运营商退出 ----------

// 写接口：令牌只读、所属运营商已退出的范围账号，一律 READONLY；权限点与只读一起判
func TestReadonlyAndOperatorExit(t *testing.T) {
	e := newEnv(t)
	e.seedPrototype() // OP03 已退出，OP01 在营
	me := e.officer("AC0503")
	id := e.quickCapture(me, "A", "2026-12-01T09:00:00+08:00")

	// 只读执法人员：上传、取证、修改、删除都 READONLY
	ro := e.token("AC0503", "执法人员", withReadonly())
	if r := e.upload(ro, jpegBytes("x"), ""); r.status != 403 || r.code(t) != "READONLY" {
		t.Fatalf("只读上传：%d %s", r.status, r.raw)
	}
	if r := e.capture(ro, map[string]any{}); r.status != 403 || r.code(t) != "READONLY" {
		t.Fatalf("只读取证：%d %s", r.status, r.raw)
	}
	for _, m := range []string{"PUT", "DELETE"} {
		if r := e.do(m, "/api/enforce/my/evidence/"+id, ro, nil); r.status != 403 || r.code(t) != "READONLY" {
			t.Fatalf("只读 %s 本人证据：%d %s", m, r.status, r.raw)
		}
	}
	// 只读账号仍可读
	if r := e.do("GET", "/api/enforce/my/evidence/"+id, ro, nil); r.status != 200 {
		t.Fatalf("只读账号应能读：%d", r.status)
	}
	if r := e.do("POST", "/api/enforce/scan", ro, map[string]any{"code": "SF86020226"}); r.status != 200 {
		t.Fatalf("只读账号应能扫码核查：%d", r.status)
	}

	// 主管部门 PUT：先判权限点，再判只读，最后才是 EVIDENCE_LOCKED
	if r := e.do("PUT", "/api/enforce/evidence/"+id, e.token("AC0700", "客服", withReadonly()), nil); r.status != 403 || r.code(t) != "FORBIDDEN" {
		t.Fatalf("无权限点且只读应先报 FORBIDDEN：%d %s", r.status, r.raw)
	}
	if r := e.do("PUT", "/api/enforce/evidence/"+id, e.supervisor(withReadonly()), nil); r.status != 403 || r.code(t) != "READONLY" {
		t.Fatalf("有权限点但只读应 READONLY：%d %s", r.status, r.raw)
	}
	if r := e.do("PUT", "/api/enforce/evidence/"+id, e.supervisor(), nil); r.status != 409 || r.code(t) != "EVIDENCE_LOCKED" {
		t.Fatalf("有权限点可写应 EVIDENCE_LOCKED：%d %s", r.status, r.raw)
	}
	// 本人证据接口：非执法人员岗位 FORBIDDEN
	if r := e.do("PUT", "/api/enforce/my/evidence/"+id, e.supervisor(), nil); r.status != 403 || r.code(t) != "FORBIDDEN" {
		t.Fatalf("非执法人员改本人证据应 FORBIDDEN：%d %s", r.status, r.raw)
	}

	// 运营商退出：令牌没带 readonly，也以订阅到的事件为准
	op01 := e.supervisor(withScope("OP01"))
	op03 := e.supervisor(withScope("OP03"))
	if r := e.do("POST", "/api/enforce/evidence-exports", op03, map[string]any{}); r.status != 403 || r.code(t) != "READONLY" {
		t.Fatalf("已退出运营商导出应 READONLY：%d %s", r.status, r.raw)
	}
	if r := e.do("PUT", "/api/enforce/evidence/"+id, op03, nil); r.status != 403 || r.code(t) != "READONLY" {
		t.Fatalf("已退出运营商 PUT 应 READONLY：%d %s", r.status, r.raw)
	}
	// 在营运营商的写判断不受影响（范围内没有证据，导出报 INVALID 而不是 READONLY）
	if r := e.do("POST", "/api/enforce/evidence-exports", op01, map[string]any{}); r.status != 400 || r.code(t) != "INVALID" {
		t.Fatalf("在营运营商不应 READONLY：%d %s", r.status, r.raw)
	}
	// 读不受限
	if r := e.do("GET", "/api/enforce/evidence", op03, nil); r.status != 200 {
		t.Fatalf("已退出运营商仍可查询：%d", r.status)
	}
	if r := e.do("GET", "/api/enforce/evidence-exports", op03, nil); r.status != 200 {
		t.Fatalf("已退出运营商仍可看导出记录：%d", r.status)
	}
}

// 事件晚于令牌：同一个令牌在 fleet.operator.exited 到达前后判断不同
func TestOperatorExitedEventFlipsWrite(t *testing.T) {
	e := newEnv(t)
	e.seedPrototype()
	e.quickCapture(e.officer("AC0503"), "A", "2026-12-01T09:00:00+08:00")
	tok := e.supervisor(withScope("OP05"))
	if r := e.do("POST", "/api/enforce/evidence-exports", tok, map[string]any{}); r.status == 403 {
		t.Fatalf("退出前不应拒绝：%s", r.raw)
	}
	e.seed(operatorExited("x9", "OP05", []string{"BR06"}, "2026-12-01T11:00:00+08:00"))
	if r := e.do("POST", "/api/enforce/evidence-exports", tok, map[string]any{}); r.status != 403 || r.code(t) != "READONLY" {
		t.Fatalf("退出后应 READONLY：%d %s", r.status, r.raw)
	}
}

// ---------- 请求体编码 ----------

func TestRequestBodyMustBeUTF8(t *testing.T) {
	e := newEnv(t)
	bad := []byte("{\"code\":\"\xd6\xd0\xce\xc4\"}") // GBK 的“中文”
	officer := e.officer("AC0503")
	sup := e.supervisor()
	for _, c := range []struct{ path, tok string }{
		{"/api/enforce/scan", officer},
		{"/api/enforce/my/evidence", officer},
		{"/api/enforce/evidence-exports", sup},
	} {
		req, _ := http.NewRequest("POST", e.srv.URL+c.path, bytes.NewReader(bad))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.tok)
		r := e.send(req)
		if r.status != 400 || r.code(t) != "INVALID" || r.json(t)["message"] != "请求体不是 UTF-8 编码" {
			t.Fatalf("%s 非 UTF-8 应 400 INVALID：%d %s", c.path, r.status, r.raw)
		}
	}
	// UTF-8 的中文照常
	if r := e.do("POST", "/api/enforce/scan", officer, map[string]any{"code": "中文内容"}); r.status != 200 {
		t.Fatalf("UTF-8 中文应正常：%d %s", r.status, r.raw)
	}
}

// ---------- 照片格式提示 ----------

func TestPhotoFormatMessage(t *testing.T) {
	e := newEnv(t)
	r := e.upload(e.officer("AC0503"), []byte("GIF89a not allowed"), "")
	if r.status != 400 || r.code(t) != "INVALID" || r.json(t)["message"] != "照片格式不支持，只收 JPEG 或 PNG 格式" {
		t.Fatalf("格式提示不对：%d %s", r.status, r.raw)
	}
	if n := e.count("SELECT count(*) FROM photos"); n != 0 {
		t.Fatal("被拒绝的照片不应入库")
	}
}

// ---------- 留痕：取证 ----------

func (e *env) auditActions() []map[string]any {
	e.t.Helper()
	rows, err := e.pool.Query(context.Background(), "SELECT body FROM outbox WHERE subject='enforce.audit.recorded' ORDER BY id")
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var b []byte
		_ = rows.Scan(&b)
		var env map[string]any
		_ = json.Unmarshal(b, &env)
		out = append(out, env)
	}
	return out
}

func TestCaptureWritesAuditAction(t *testing.T) {
	e := newEnv(t)
	me := e.officer("AC0503")
	photo := e.uploadPhoto(me, "p")
	r := e.capture(me, map[string]any{"scanResult": "无政府码", "location": []float64{104, 30}, "vehicleDescription": "车", "photos": []string{photo}})
	if r.status != 201 {
		t.Fatalf("取证失败：%s", r.raw)
	}
	evID := r.json(t)["evidenceId"].(string)

	logs := e.auditActions()
	if len(logs) != 2 {
		t.Fatalf("上传与取证各一条留痕，得到 %d", len(logs))
	}
	up, cap := logs[0], logs[1]
	for _, l := range logs {
		p := l["payload"].(map[string]any)
		if (p["action"] != "上传取证照片" && p["action"] != "提交取证记录") || p["targetType"] != "evidence" {
			t.Fatalf("动作应为上传取证照片或提交取证记录：%v", p)
		}
		if a := l["actor"].(map[string]any); a["kind"] != "人" || a["id"] != "AC0503" {
			t.Fatalf("操作人不对：%v", a)
		}
	}
	if up["payload"].(map[string]any)["targetId"] != photo || cap["payload"].(map[string]any)["targetId"] != evID {
		t.Fatalf("targetId 不对：%v %v", up["payload"], cap["payload"])
	}
	if cap["key"] != "evidence:"+evID {
		t.Fatalf("key 不对：%v", cap["key"])
	}
	// 失败的取证不留痕
	before := len(e.auditActions())
	_ = e.capture(me, map[string]any{"scanResult": "无政府码"})
	if len(e.auditActions()) != before {
		t.Fatal("失败的取证不应留痕")
	}
	// 幂等重放不重复留痕
	p2 := e.uploadPhoto(me, "q")
	body := map[string]any{"scanResult": "无政府码", "location": []float64{104, 30}, "vehicleDescription": "车2", "photos": []string{p2}}
	_ = e.capture(me, body, "Idempotency-Key", "k1")
	n := len(e.auditActions())
	_ = e.capture(me, body, "Idempotency-Key", "k1")
	if len(e.auditActions()) != n {
		t.Fatal("幂等重放不应重复留痕")
	}
}

// ---------- 已移除或退出的车辆保持"码已失效" ----------

func TestScanKeepsInvalidAfterRemovedOrExit(t *testing.T) {
	e := newEnv(t)
	e.seedPrototype()
	officer := e.officer("AC0503")
	scan := func(code string) map[string]any {
		r := e.do("POST", "/api/enforce/scan", officer, map[string]any{"code": code})
		if r.status != 200 {
			t.Fatalf("扫码失败：%s", r.raw)
		}
		return r.json(t)
	}

	// 车辆移出清单后，迟到的“已启用”不得恢复
	e.seed(vehicleRemoved("r1", "VH000004", "BR01", "OP01", "SF86020226", "2026-11-30T08:00:00+08:00"))
	e.seed(codeChanged("r2", "VH000004", "BR01", "SF86020226", "已失效", "已启用", "2026-11-30T09:00:00+08:00"))
	res := scan("SF86020226")
	v := res["vehicle"].(map[string]any)
	if res["scanResult"] != "码已失效" || v["codeStatus"] != "已失效" || res["needCapture"] != true {
		t.Fatalf("已移除车辆应显示码已失效：%v", res)
	}

	// 运营商退出事件早于该运营商的码状态事件：名下车辆仍显示已失效，失效时刻取退出时刻
	e.seed(vehicleListed("l1", "VH009001", "BR06", "OP05", "TM000001", "SF12345678", "途迈", "已启用", nil, "2026-08-01T10:00:00+08:00"))
	if res := scan("SF12345678"); res["scanResult"] != "有效政府码" {
		t.Fatalf("退出前应有效：%v", res)
	}
	e.seed(operatorExited("o1", "OP05", []string{"BR06"}, "2026-12-01T10:00:00+08:00"))
	res = scan("SF12345678")
	v = res["vehicle"].(map[string]any)
	if res["scanResult"] != "码已失效" || v["codeStatus"] != "已失效" || v["operatorStatus"] != "已退出" || v["codeInvalidAt"] != "2026-12-01T10:00:00+08:00" {
		t.Fatalf("运营商退出后名下车辆应显示码已失效：%v", res)
	}
	// 之后又到达“已启用”的码状态事件，也不恢复
	e.seed(codeChanged("o2", "VH009001", "BR06", "SF12345678", "已失效", "已启用", "2026-12-01T10:30:00+08:00"))
	if res := scan("SF12345678"); res["scanResult"] != "码已失效" {
		t.Fatalf("退出后不得恢复：%v", res)
	}

	// 取证带出的码状态同样是已失效
	photo := e.uploadPhoto(officer, "z")
	r := e.capture(officer, map[string]any{"scanResult": "码已失效", "govCode": "SF12345678", "location": []float64{104, 30}, "vehicleDescription": "途迈", "photos": []string{photo}})
	if r.status != 201 || r.json(t)["codeStatus"] != "已失效" || r.json(t)["operatorId"] != "OP05" {
		t.Fatalf("取证应带出已失效：%d %s", r.status, r.raw)
	}
}

// 车辆先于品牌与运营商到达、状态事件没带运营商时，运营商经品牌关联
func TestOperatorExitViaBrandWhenVehicleHasNoOperator(t *testing.T) {
	e := newEnv(t)
	e.seed(codeChanged("c1", "VH009002", "BR06", "SF22223333", "待启用", "已启用", "2026-08-01T10:00:00+08:00"))
	e.seed(brandRegistered("b1", "BR06", "OP05", "途迈", "途迈出行科技有限公司", "途迈出行", "已接入"))
	e.seed(operatorExited("o1", "OP05", []string{"BR06"}, "2026-12-01T10:00:00+08:00"))
	r := e.do("POST", "/api/enforce/scan", e.officer("AC0503"), map[string]any{"code": "SF22223333"})
	if r.status != 200 || !strings.Contains(string(r.raw), `"scanResult":"码已失效"`) {
		t.Fatalf("经品牌关联的运营商退出也应显示已失效：%s", r.raw)
	}
}
