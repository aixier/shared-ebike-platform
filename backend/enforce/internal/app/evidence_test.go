package app

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bike/enforce/internal/digest"
)

// ---------- 照片 ----------

func TestUploadPhoto(t *testing.T) {
	e := newEnv(t)
	tok := e.officer("AC0503")

	content := jpegBytes("one")
	r := e.upload(tok, content, "")
	if r.status != http.StatusCreated {
		t.Fatalf("上传应 201：%d %s", r.status, r.raw)
	}
	m := r.json(t)
	if m["sha256"] != digest.Bytes(content) || m["contentType"] != "image/jpeg" || int(m["size"].(float64)) != len(content) || m["uploadedAt"] != "2026-12-01T12:00:00+08:00" {
		t.Fatalf("照片信息不对：%s", r.raw)
	}
	// 文件落在 ENFORCE_PHOTO_DIR
	files, _ := os.ReadDir(e.photo)
	if len(files) != 1 {
		t.Fatalf("照片目录应有 1 个文件，得到 %d", len(files))
	}

	r = e.upload(tok, pngBytes("two"), "")
	if r.status != http.StatusCreated || r.json(t)["contentType"] != "image/png" {
		t.Fatalf("PNG 上传不对：%d %s", r.status, r.raw)
	}

	// 幂等：同一个键只存一张
	a := e.upload(tok, jpegBytes("three"), "idem-1")
	b := e.upload(tok, jpegBytes("three"), "idem-1")
	if a.json(t)["photoId"] != b.json(t)["photoId"] || e.count("SELECT count(*) FROM photos") != 3 {
		t.Fatal("上传幂等不对")
	}
}

func TestUploadPhotoRejects(t *testing.T) {
	e := newEnv(t)
	tok := e.officer("AC0503")
	if r := e.upload(tok, []byte("GIF89a not allowed"), ""); r.status != 400 || r.code(t) != "INVALID" {
		t.Fatalf("非 JPEG/PNG 应 400：%d %s", r.status, r.raw)
	}
	big := append(jpegBytes(""), bytes.Repeat([]byte{0}, 10<<20)...)
	if r := e.upload(tok, big, ""); r.status != 400 || r.code(t) != "INVALID" {
		t.Fatalf("超过 10MB 应 400：%d %s", r.status, r.raw)
	}
	if r := e.upload(e.supervisor(), jpegBytes("x"), ""); r.status != 403 {
		t.Fatalf("非执法人员不能上传：%d", r.status)
	}
	if r := e.upload(e.token("AC0505", "执法人员", withReadonly()), jpegBytes("x"), ""); r.status != 403 || r.code(t) != "READONLY" {
		t.Fatalf("只读账号不能上传：%d %s", r.status, r.raw)
	}
}

func TestGetPhoto(t *testing.T) {
	e := newEnv(t)
	owner := e.officer("AC0503")
	id := e.uploadPhoto(owner, "p")

	r := e.do("GET", "/api/enforce/photos/"+id, owner, nil)
	if r.status != 200 || r.header.Get("Content-Type") != "image/jpeg" || !bytes.Equal(r.raw, jpegBytes("p")) {
		t.Fatalf("本人读照片不对：%d %s", r.status, r.header.Get("Content-Type"))
	}
	if r := e.do("GET", "/api/enforce/photos/"+id, e.officer("AC0504"), nil); r.status != 403 {
		t.Fatalf("他人照片应 403：%d", r.status)
	}
	if r := e.do("GET", "/api/enforce/photos/"+id, e.supervisor(), nil); r.status != 200 {
		t.Fatalf("主管部门可读：%d", r.status)
	}
	if r := e.do("GET", "/api/enforce/photos/"+id, e.token("AC0700", "客服"), nil); r.status != 403 {
		t.Fatalf("无 evidence.view 应 403：%d", r.status)
	}
	if r := e.do("GET", "/api/enforce/photos/PHnope", owner, nil); r.status != 404 || r.code(t) != "NOT_FOUND" {
		t.Fatalf("不存在应 404：%d", r.status)
	}
}

// ---------- 取证 ----------

func (e *env) capture(tok string, body map[string]any, hdr ...string) resp {
	e.t.Helper()
	return e.do("POST", "/api/enforce/my/evidence", tok, body, hdr...)
}

func TestCaptureWithGovCode(t *testing.T) {
	e := newEnv(t)
	e.seedPrototype()
	tok := e.officer("AC0503")
	p1 := e.uploadPhoto(tok, "a")
	p2 := e.uploadPhoto(tok, "b")

	r := e.capture(tok, map[string]any{
		"scanResult": "码已失效", "govCode": "sf71009215", "location": []float64{104.0407531, 30.60688},
		"address": "示范市西城区和平路", "accuracyM": 6.5, "vehicleDescription": "  车身简途字样  ",
		"photos": []string{p1, p2}, "note": "停在路口",
	})
	if r.status != http.StatusCreated {
		t.Fatalf("取证应 201：%d %s", r.status, r.raw)
	}
	m := r.json(t)
	want := map[string]any{
		"evidenceId": "EV20261201001", "officerId": "AC0503", "officerName": "AC0503姓名", "badgeNo": "BAC0503",
		"capturedAt": "2026-12-01T12:00:00+08:00", "submittedAt": "2026-12-01T12:00:00+08:00",
		"address": "示范市西城区和平路", "accuracyM": 6.5, "scanResult": "码已失效",
		"vehicleId": "VH001061", "plateNo": "JT542568", "brandId": "BR04", "brandName": "简途",
		"operatorId": "OP03", "operatorName": "简途共享", "codeStatus": "已失效",
		"vehicleDescription": "车身简途字样", "note": "停在路口", "locked": true,
	}
	for k, w := range want {
		if m[k] != w {
			t.Fatalf("%s：got %v want %v", k, m[k], w)
		}
	}
	loc := m["location"].([]any)
	if loc[0] != 104.040753 || loc[1] != 30.60688 {
		t.Fatalf("位置应保留 6 位小数：%v", loc)
	}
	// 摘要按契约公式
	wantSHA := digest.Evidence(digest.Input{EvidenceID: "EV20261201001", OfficerID: "AC0503", CapturedAt: "2026-12-01T12:00:00+08:00",
		Lng: 104.040753, Lat: 30.60688, ScanResult: "码已失效", PhotoSHA256: []string{digest.Bytes(jpegBytes("a")), digest.Bytes(jpegBytes("b"))}})
	if m["sha256"] != wantSHA {
		t.Fatalf("摘要不符：%v want %s", m["sha256"], wantSHA)
	}
	ps := m["photos"].([]any)
	if len(ps) != 2 || ps[0] != p1 || ps[1] != p2 {
		t.Fatalf("照片顺序不对：%v", ps)
	}

	// 发"证据已提交"事件进发件表
	if n := e.count("SELECT count(*) FROM outbox WHERE subject='enforce.evidence.submitted' AND body->>'key'='EV20261201001' AND body->'payload'->>'sha256'=$1 AND body->'payload'->>'vehicleId'='VH001061'", wantSHA); n != 1 {
		t.Fatal("证据已提交事件不对")
	}

	// 同日第二条顺序发号
	p3 := e.uploadPhoto(tok, "c")
	r = e.capture(tok, map[string]any{"scanResult": "无政府码", "location": []float64{104, 30}, "vehicleDescription": "途迈字样", "brandId": "BR06", "photos": []string{p3}, "capturedAt": "2026-12-01T11:50:00+08:00"})
	m = r.json(t)
	if m["evidenceId"] != "EV20261201002" || m["capturedAt"] != "2026-12-01T11:50:00+08:00" || m["brandId"] != "BR06" || m["operatorId"] != "OP05" || m["vehicleId"] != nil || m["codeStatus"] != nil || m["accuracyM"] != nil || m["note"] != nil {
		t.Fatalf("无码取证不对：%s", r.raw)
	}
}

func TestCaptureValidation(t *testing.T) {
	e := newEnv(t)
	tok := e.officer("AC0503")
	mine := e.uploadPhoto(tok, "m")
	other := e.uploadPhoto(e.officer("AC0504"), "o")
	ok := func() map[string]any {
		return map[string]any{"scanResult": "无政府码", "location": []float64{104, 30}, "vehicleDescription": "车", "photos": []string{mine}}
	}
	cases := map[string]func(map[string]any){
		"扫码结果不对": func(b map[string]any) { b["scanResult"] = "有效政府码" },
		"没有照片":   func(b map[string]any) { b["photos"] = []string{} },
		"没有位置":   func(b map[string]any) { delete(b, "location") },
		"位置不对":   func(b map[string]any) { b["location"] = []float64{104} },
		"车辆描述空":  func(b map[string]any) { b["vehicleDescription"] = "   " },
		"他人照片":   func(b map[string]any) { b["photos"] = []string{other} },
		"照片不存在":  func(b map[string]any) { b["photos"] = []string{"PHnope"} },
		"照片重复":   func(b map[string]any) { b["photos"] = []string{mine, mine} },
		"取证时刻不对": func(b map[string]any) { b["capturedAt"] = "昨天" },
	}
	for name, mut := range cases {
		b := ok()
		mut(b)
		r := e.capture(tok, b)
		if r.status != 400 || r.code(t) != "INVALID" || r.json(t)["message"] == "" {
			t.Fatalf("%s：应 400 INVALID，得到 %d %s", name, r.status, r.raw)
		}
	}
	if n := e.count("SELECT count(*) FROM evidence"); n != 0 {
		t.Fatal("校验失败不应落库")
	}
	// 照片已被其他证据引用不能再用
	if r := e.capture(tok, ok()); r.status != 201 {
		t.Fatalf("正常取证失败：%s", r.raw)
	}
	if r := e.capture(tok, ok()); r.status != 400 || r.code(t) != "INVALID" {
		t.Fatalf("照片已被引用应 400：%d %s", r.status, r.raw)
	}
	if r := e.capture(e.supervisor(), ok()); r.status != 403 {
		t.Fatalf("非执法人员不能取证：%d", r.status)
	}
}

func TestCaptureIdempotent(t *testing.T) {
	e := newEnv(t)
	tok := e.officer("AC0503")
	p := e.uploadPhoto(tok, "a")
	b := map[string]any{"scanResult": "无政府码", "location": []float64{104, 30}, "vehicleDescription": "车", "photos": []string{p}}
	r1 := e.capture(tok, b, "Idempotency-Key", "cap-1")
	r2 := e.capture(tok, b, "Idempotency-Key", "cap-1")
	if r1.status != 201 || r2.status != 201 || string(r1.raw) != string(r2.raw) {
		t.Fatalf("取证幂等不对：%d %d", r1.status, r2.status)
	}
	if e.count("SELECT count(*) FROM evidence") != 1 || e.count("SELECT count(*) FROM outbox WHERE subject <> 'enforce.audit.recorded'") != 1 || e.count("SELECT count(*) FROM outbox WHERE subject = 'enforce.audit.recorded'") != 2 { // 照片上传、取证各一条留痕
		t.Fatal("重复提交不应生成第二条证据")
	}
}

// ---------- 我的取证记录 ----------

func (e *env) quickCapture(tok, officerTag, at string) string {
	e.t.Helper()
	p := e.uploadPhoto(tok, officerTag+at)
	r := e.capture(tok, map[string]any{"scanResult": "无政府码", "location": []float64{104, 30}, "vehicleDescription": "车" + officerTag, "address": "地址" + officerTag, "photos": []string{p}, "capturedAt": at})
	if r.status != 201 {
		e.t.Fatalf("取证失败：%s", r.raw)
	}
	return r.json(e.t)["evidenceId"].(string)
}

func TestMyEvidence(t *testing.T) {
	e := newEnv(t)
	me, other := e.officer("AC0503"), e.officer("AC0504")
	a := e.quickCapture(me, "A", "2026-11-30T10:00:00+08:00")
	b := e.quickCapture(me, "A", "2026-12-01T09:00:00+08:00")
	c := e.quickCapture(other, "B", "2026-12-01T10:00:00+08:00")

	r := e.do("GET", "/api/enforce/my/evidence", me, nil)
	m := r.json(t)
	rows := m["rows"].([]any)
	if r.status != 200 || m["total"].(float64) != 2 || len(rows) != 2 || m["page"].(float64) != 1 || m["pageSize"].(float64) != 20 {
		t.Fatalf("我的记录不对：%s", r.raw)
	}
	if rows[0].(map[string]any)["evidenceId"] != b || rows[1].(map[string]any)["evidenceId"] != a {
		t.Fatal("应按取证时刻新到旧")
	}
	r = e.do("GET", "/api/enforce/my/evidence?page=2&pageSize=1", me, nil)
	rows = r.json(t)["rows"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["evidenceId"] != a {
		t.Fatalf("分页不对：%s", r.raw)
	}

	if r := e.do("GET", "/api/enforce/my/evidence/"+a, me, nil); r.status != 200 || r.json(t)["evidenceId"] != a {
		t.Fatalf("本人详情不对：%d", r.status)
	}
	if r := e.do("GET", "/api/enforce/my/evidence/"+c, me, nil); r.status != 403 || r.code(t) != "FORBIDDEN" {
		t.Fatalf("他人记录应 403：%d %s", r.status, r.raw)
	}
	if r := e.do("GET", "/api/enforce/my/evidence/EV00000000999", me, nil); r.status != 404 {
		t.Fatalf("不存在应 404：%d", r.status)
	}
}

// ---------- 防篡改 ----------

func TestUpdateAndDeleteAlwaysLocked(t *testing.T) {
	e := newEnv(t)
	me := e.officer("AC0503")
	id := e.quickCapture(me, "A", "2026-12-01T09:00:00+08:00")
	before := e.do("GET", "/api/enforce/my/evidence/"+id, me, nil).raw

	for _, c := range []struct{ m, path, tok string }{
		{"PUT", "/api/enforce/my/evidence/" + id, me},
		{"DELETE", "/api/enforce/my/evidence/" + id, me},
		{"PUT", "/api/enforce/my/evidence/" + id, e.officer("AC0504")},
		{"PUT", "/api/enforce/evidence/" + id, e.supervisor()},
	} {
		r := e.do(c.m, c.path, c.tok, map[string]any{"address": "改掉"})
		if r.status != 409 || r.code(t) != "EVIDENCE_LOCKED" || r.json(t)["message"] != "证据提交后不可修改或删除" {
			t.Fatalf("%s %s 应 409 EVIDENCE_LOCKED：%d %s", c.m, c.path, r.status, r.raw)
		}
	}
	for _, c := range []struct{ m, path, tok string }{
		{"PUT", "/api/enforce/my/evidence/EVnope", me},
		{"DELETE", "/api/enforce/my/evidence/EVnope", me},
		{"PUT", "/api/enforce/evidence/EVnope", e.supervisor()},
	} {
		if r := e.do(c.m, c.path, c.tok, nil); r.status != 404 {
			t.Fatalf("%s %s 不存在应 404：%d", c.m, c.path, r.status)
		}
	}
	if r := e.do("PUT", "/api/enforce/evidence/"+id, e.token("AC0700", "客服"), nil); r.status != 403 {
		t.Fatalf("主管部门修改接口无权限应 403：%d", r.status)
	}
	after := e.do("GET", "/api/enforce/my/evidence/"+id, me, nil).raw
	if string(before) != string(after) {
		t.Fatal("证据内容被改动")
	}
}

// 数据库层面：直接写 SQL 也改不了删不了
func TestDatabaseRejectsTampering(t *testing.T) {
	e := newEnv(t)
	me := e.officer("AC0503")
	id := e.quickCapture(me, "A", "2026-12-01T09:00:00+08:00")
	ctx := context.Background()
	stmts := []string{
		"UPDATE evidence SET address='改' WHERE evidence_id='" + id + "'",
		"DELETE FROM evidence WHERE evidence_id='" + id + "'",
		"TRUNCATE evidence CASCADE",
		"UPDATE evidence_photos SET photo_id='x'",
		"DELETE FROM evidence_photos",
		"UPDATE photos SET sha256='x'",
		"DELETE FROM photos",
		"TRUNCATE photos CASCADE",
	}
	for _, s := range stmts {
		if _, err := e.pool.Exec(ctx, s); err == nil {
			t.Fatalf("数据库应拒绝：%s", s)
		}
	}
	if e.count("SELECT count(*) FROM evidence") != 1 || e.count("SELECT count(*) FROM evidence_photos") != 1 {
		t.Fatal("证据被改动")
	}
}

func TestVerify(t *testing.T) {
	e := newEnv(t)
	me := e.officer("AC0503")
	sup := e.supervisor()
	id := e.quickCapture(me, "A", "2026-12-01T09:00:00+08:00")

	r := e.do("GET", "/api/enforce/evidence/"+id+"/verify", sup, nil)
	m := r.json(t)
	if r.status != 200 || m["ok"] != true || m["stored"] != m["actual"] || m["evidenceId"] != id {
		t.Fatalf("未篡改应验证通过：%s", r.raw)
	}

	// 照片文件被替换：重算不符
	var file string
	if err := e.pool.QueryRow(context.Background(), "SELECT p.file_name FROM photos p JOIN evidence_photos ep USING (photo_id) WHERE ep.evidence_id=$1", id).Scan(&file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.photo, file), jpegBytes("forged"), 0o644); err != nil {
		t.Fatal(err)
	}
	m = e.do("GET", "/api/enforce/evidence/"+id+"/verify", sup, nil).json(t)
	if m["ok"] != false || m["stored"] == m["actual"] {
		t.Fatalf("照片被替换应验证失败：%v", m)
	}

	// 有人绕过触发器改了位置：重算不符
	id2 := e.quickCapture(me, "A", "2026-12-01T09:30:00+08:00")
	ctx := context.Background()
	for _, s := range []string{
		"ALTER TABLE evidence DISABLE TRIGGER evidence_append_only",
		"UPDATE evidence SET lng=lng+0.001 WHERE evidence_id='" + id2 + "'",
		"ALTER TABLE evidence ENABLE TRIGGER evidence_append_only",
	} {
		if _, err := e.pool.Exec(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if m := e.do("GET", "/api/enforce/evidence/"+id2+"/verify", sup, nil).json(t); m["ok"] != false {
		t.Fatalf("位置被改应验证失败：%v", m)
	}
	if r := e.do("GET", "/api/enforce/evidence/EVnope/verify", sup, nil); r.status != 404 {
		t.Fatalf("不存在应 404：%d", r.status)
	}
	if r := e.do("GET", "/api/enforce/evidence/"+id+"/verify", me, nil); r.status != 403 {
		t.Fatalf("执法人员无 evidence.view 应 403：%d", r.status)
	}
}

// ---------- 未引用照片定期清理 ----------

func TestCleanupOrphanPhotos(t *testing.T) {
	e := newEnv(t)
	me := e.officer("AC0503")
	used := e.uploadPhoto(me, "used")
	if r := e.capture(me, map[string]any{"scanResult": "无政府码", "location": []float64{104, 30}, "vehicleDescription": "车", "photos": []string{used}}); r.status != 201 {
		t.Fatal(string(r.raw))
	}
	orphanOld := e.uploadPhoto(me, "old")
	e.now = fixedNow.Add(25 * time.Hour)
	orphanNew := e.uploadPhoto(me, "new")

	n, err := e.app.CleanupOrphanPhotos(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("应清理 1 张，得到 %d", n)
	}
	for id, want := range map[string]int{used: 1, orphanOld: 0, orphanNew: 1} {
		if got := e.count("SELECT count(*) FROM photos WHERE photo_id=$1", id); got != want {
			t.Fatalf("%s：got %d want %d", id, got, want)
		}
	}
	files, _ := os.ReadDir(e.photo)
	if len(files) != 2 {
		t.Fatalf("文件应剩 2 个，得到 %d", len(files))
	}
}
