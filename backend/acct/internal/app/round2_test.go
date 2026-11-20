package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// 第二轮：非 UTF-8 请求体返回 400 INVALID
func TestNonUTF8Body(t *testing.T) {
	e := newEnv(t)
	post := func(path string, body []byte, hdr map[string]string) (int, map[string]any) {
		req, _ := http.NewRequest("POST", e.srv.URL+path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var m map[string]any
		_ = json.NewDecoder(res.Body).Decode(&m)
		return res.StatusCode, m
	}
	// GBK 编码的 {"channel":"微信","phone":"13812345678"}
	gbk := append([]byte(`{"channel":"`), 0xCE, 0xA2, 0xD0, 0xC5)
	gbk = append(gbk, []byte(`","phone":"13812345678"}`)...)
	st, b := post("/api/acct/authorize", gbk, nil)
	if st != 400 || b["code"] != "INVALID" || !strings.Contains(b["message"].(string), "请求体不是 UTF-8 编码") {
		t.Fatalf("授权接口非 UTF-8 应 400 INVALID: %d %v", st, b)
	}
	uid, _ := e.login("13812345678")
	cmd := append([]byte(`{"reason":"`), 0xD3, 0xC3, 0xBB, 0xA7, 0xC9, 0xEA, 0xC7, 0xEB)
	cmd = append(cmd, []byte(`","actor":{"kind":"人","id":"AC0301"}}`)...)
	st, b = post("/internal/acct/users/"+uid+"/cancel", cmd, internalHdr("cs", "cs-key", "g1"))
	if st != 400 || b["code"] != "INVALID" || !strings.Contains(b["message"].(string), "UTF-8") {
		t.Fatalf("内部命令非 UTF-8 应 400 INVALID: %d %v", st, b)
	}
}

// 第二轮：内部接口 401 用 UNAUTHORIZED
func TestInternalUnauthorizedCode(t *testing.T) {
	e := newEnv(t)
	r := e.do("GET", "/internal/acct/token-keys", nil, internalHdr("cs", "错的", ""))
	if r.status != 401 || r.str("code") != "UNAUTHORIZED" {
		t.Fatalf("服务凭证无效应 401 UNAUTHORIZED: %d %v", r.status, r.body)
	}
	if r := e.do("GET", "/internal/acct/token-keys", nil, nil); r.status != 401 || r.str("code") != "UNAUTHORIZED" {
		t.Fatalf("缺凭证应 401 UNAUTHORIZED: %d %v", r.status, r.body)
	}
}

func auditActions(e *env, uid string) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, a := range e.outbox("acct.audit.recorded") {
		p := a["payload"].(map[string]any)
		if p["targetId"] == uid {
			p["_actor"] = a["actor"]
			out[p["action"].(string)] = p
		}
	}
	return out
}

// 第二轮：acct 自己执行的动作自己记留痕（注册、实名、查看用户信息），摘要不含个人信息
func TestAuditRegisterRealNameView(t *testing.T) {
	e := newEnv(t)
	uid, tok := e.login("13812345678")
	acts := auditActions(e, uid)
	reg := acts["注册用户"]
	if reg == nil || reg["targetType"] != "user" || reg["_actor"].(map[string]any)["kind"] != "用户" || reg["_actor"].(map[string]any)["id"] != uid {
		t.Fatalf("注册应留痕，操作人为用户本人: %v", acts)
	}
	if r := e.do("POST", "/api/acct/real-name", map[string]any{"name": "张三丰", "idNo": adultID}, idem(bearer(tok), "rn1")); r.status != 200 {
		t.Fatalf("实名: %d %v", r.status, r.body)
	}
	rn := auditActions(e, uid)["核验实名"]
	if rn == nil || rn["after"].(map[string]any)["status"] != "已核验" {
		t.Fatalf("实名应留痕: %v", rn)
	}
	// 客服读用户：留痕
	adm := bearer(e.adminToken([]string{"user.view"}, nil))
	if r := e.do("GET", "/api/acct/admin/users/"+uid, nil, adm); r.status != 200 {
		t.Fatalf("客服读用户: %d %v", r.status, r.body)
	}
	if v := auditActions(e, uid)["查看用户信息"]; v == nil || v["_actor"].(map[string]any)["id"] != "AC0301" {
		t.Fatalf("查看用户信息应留痕: %v", v)
	}
	// 留痕里不得出现姓名、证件号、完整手机号
	for _, a := range e.outbox("acct.audit.recorded") {
		b, _ := json.Marshal(a)
		for _, s := range []string{"张三丰", adultID, "13812345678"} {
			if strings.Contains(string(b), s) {
				t.Fatalf("留痕含个人信息 %q: %s", s, b)
			}
		}
	}
}

// 未满法定年龄、先骑后验登记也留痕"核验实名"
func TestAuditRealNameFailAndDeferred(t *testing.T) {
	e := newEnv(t)
	uid, tok := e.login("13812345678")
	if r := e.do("POST", "/api/acct/real-name", map[string]any{"name": "张小明", "idNo": minorID}, idem(bearer(tok), "rn1")); r.str("code") != "UNDER_AGE" {
		t.Fatalf("应未满年龄: %v", r.body)
	}
	rn := auditActions(e, uid)["核验实名"]
	if rn == nil || rn["after"].(map[string]any)["status"] != "未通过" || rn["reason"] == nil {
		t.Fatalf("未通过应留痕并带原因: %v", rn)
	}
}

// 管理令牌 readonly 为真时读接口照常；本服务没有管理端写接口
func TestReadonlyAdminCanRead(t *testing.T) {
	e := newEnv(t)
	uid, _ := e.login("13812345678")
	ro := e.readonlyAdminToken([]string{"user.view", "account.unfreeze"})
	if r := e.do("GET", "/api/acct/admin/users/"+uid, nil, bearer(ro)); r.status != 200 {
		t.Fatalf("只读令牌应可读: %d %v", r.status, r.body)
	}
	if r := e.do("GET", "/api/acct/admin/frozen-accounts", nil, bearer(ro)); r.status != 200 {
		t.Fatalf("只读令牌应可读冻结列表: %d %v", r.status, r.body)
	}
}
