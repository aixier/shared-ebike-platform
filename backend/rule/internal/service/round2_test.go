package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// trace: contracts/第二轮变更单_20261120.md 全体、rule 节

func roToken(perms []string) string {
	return token(perms, func(c jwt.MapClaims) { c["readonly"] = true })
}

func TestReadonly_全部管理端写接口拒绝(t *testing.T) {
	e := newEnv(t, true)
	roEdit := roToken([]string{"pricing.view", "pricing.edit"})
	roOps := roToken([]string{"platformParams.edit"})
	expect(t, e.do("POST", "/api/rule/pricing/versions", roEdit, map[string]any{"name": "x", "reason": "政府调价通知"}, nil), 403, "READONLY")
	expect(t, e.do("POST", "/api/rule/pricing/versions/PR0004/revoke", roEdit, map[string]any{"reason": "门槛另行研究"}, nil), 403, "READONLY")
	expect(t, e.do("PATCH", "/api/rule/platform-params", roOps, map[string]any{"items": map[string]any{"payWindowHours": 48}, "reason": "平台公司调整期限"}, nil), 403, "READONLY")
	// 只读不影响读接口
	expect(t, e.do("GET", "/api/rule/pricing/versions", roEdit, nil, nil), 200, "")
	expect(t, e.do("GET", "/api/rule/platform-params", roEdit, nil, nil), 200, "")
	if n := len(outboxRows(t)); n != 0 {
		t.Errorf("被拒的写操作不应产生事件：%d", n)
	}
}

func TestBody_非UTF8返回INVALID(t *testing.T) {
	e := newEnv(t, true)
	bad := "{\"name\":\"\xb0\xa1\xb0\xa1\",\"reason\":\"政府调价通知\"}" // GBK 字节
	r := e.do("POST", "/api/rule/pricing/versions", editor, bad, nil)
	expect(t, r, 400, "INVALID")
	if r.body["message"] != "请求体不是 UTF-8 编码" {
		t.Errorf("提示不对：%s", r.raw)
	}
	r = e.do("PATCH", "/api/rule/platform-params", opsTok, "{\"items\":{},\"reason\":\"\xb0\xa1\xb0\xa1\xb0\xa1\"}", nil)
	expect(t, r, 400, "INVALID")
	if r.body["message"] != "请求体不是 UTF-8 编码" {
		t.Errorf("提示不对：%s", r.raw)
	}
	r = e.do("POST", "/api/rule/pricing/versions/PR0004/revoke", editor, "{\"reason\":\"\xb0\xa1\xb0\xa1\xb0\xa1\"}", nil)
	expect(t, r, 400, "INVALID")
}

func TestParams_新参数缺省值与修改(t *testing.T) {
	e := newEnv(t, false)
	r := e.do("GET", "/api/rule/platform-params", loggedIn, nil, nil)
	expect(t, r, 200, "")
	if num(path(r.body, "items", "deferredVerifyDays")) != 3 || num(path(r.body, "items", "legalAge")) != 16 || path(r.body, "items", "payWindowHours") != nil {
		t.Errorf("延迟核验天数缺省 3、法定年龄缺省 16，其余未录入为 null：%s", r.raw)
	}
	// 范围校验
	bad := e.do("PATCH", "/api/rule/platform-params", opsTok, map[string]any{"items": map[string]any{"deferredVerifyDays": 0, "legalAge": 5}, "reason": "平台公司调整期限"}, nil)
	expect(t, bad, 400, "INVALID")
	if errs, _ := bad.body["errors"].([]any); len(errs) != 2 {
		t.Errorf("应逐项返回 2 个错误：%s", bad.raw)
	}
	// 修改只带一项，另一项仍是缺省值；事件载荷带两项当前值
	r = e.do("PATCH", "/api/rule/platform-params", opsTok, map[string]any{"items": map[string]any{"legalAge": 18}, "reason": "平台公司调整期限"}, nil)
	expect(t, r, 200, "")
	if num(path(r.body, "items", "legalAge")) != 18 || num(path(r.body, "items", "deferredVerifyDays")) != 3 {
		t.Errorf("修改结果不对：%s", r.raw)
	}
	rows := outboxRows(t)
	p := rows[0].Body["payload"].(map[string]any)
	if rows[0].Subject != "rule.params.changed" || num(p["legalAge"]) != 18 || num(p["deferredVerifyDays"]) != 3 {
		t.Errorf("参数变更载荷应含 deferredVerifyDays、legalAge：%v", p)
	}
	ap := rows[1].Body["payload"].(map[string]any)
	if path(ap, "after", "legalAge") == nil || path(ap, "before", "legalAge") == nil || path(ap, "after", "deferredVerifyDays") != nil {
		t.Errorf("留痕应只含改动项（含缺省值作为旧值）：%v", ap)
	}
	r = e.do("PATCH", "/api/rule/platform-params", opsTok, map[string]any{"items": map[string]any{"deferredVerifyDays": 7}, "reason": "平台公司调整期限"}, nil)
	expect(t, r, 200, "")
	if num(path(r.body, "items", "deferredVerifyDays")) != 7 || num(path(r.body, "items", "legalAge")) != 18 {
		t.Errorf("两项各自保留：%s", r.raw)
	}
}

func TestParams_事件载荷retentionYears是对象(t *testing.T) {
	e := newEnv(t, true)
	body := map[string]any{"items": map[string]any{"retentionYears": map[string]any{"orders": 7}, "trackRetentionDays": 90}, "reason": "平台公司调整期限"}
	expect(t, e.do("PATCH", "/api/rule/platform-params", opsTok, body, nil), 200, "")
	p := outboxRows(t)[0].Body["payload"].(map[string]any)
	ry, ok := p["retentionYears"].(map[string]any)
	if !ok || num(ry["orders"]) != 7 || num(p["trackRetentionDays"]) != 90 {
		t.Errorf("retentionYears 应为对象：%v", p)
	}
}

// ---------------------------------------------------------------- 品牌投影

func brandEvent(id, brand, op, name string) []byte {
	b, _ := json.Marshal(map[string]any{
		"eventId": id, "type": "fleet.brand.registered", "version": 1, "occurredAt": "2026-11-20T10:00:00+08:00", "key": brand,
		"actor":   map[string]any{"kind": "人", "id": "AC0002"},
		"payload": map[string]any{"brandId": brand, "operatorId": op, "name": name, "displayName": "【" + name + "】电单车", "providerId": nil},
	})
	return b
}

func TestConsumer_品牌投影去重与更新(t *testing.T) {
	newEnv(t, false)
	c := &Consumer{Pool: testPool}
	ctx := context.Background()
	if err := c.Handle(ctx, "fleet.brand.registered", brandEvent("e1", "BR01", "OP01", "青禾")); err != nil {
		t.Fatal(err)
	}
	if err := c.Handle(ctx, "fleet.brand.registered", brandEvent("e1", "BR01", "OP01", "重复")); err != nil {
		t.Fatal(err)
	}
	var name string
	_ = testPool.QueryRow(ctx, `SELECT name FROM brands WHERE brand_id='BR01'`).Scan(&name)
	if name != "青禾" {
		t.Errorf("同一 eventId 只处理一次：%s", name)
	}
	if err := c.Handle(ctx, "fleet.brand.registered", brandEvent("e2", "BR01", "OP01", "青禾新名")); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = testPool.QueryRow(ctx, `SELECT count(*) FROM brands`).Scan(&n)
	_ = testPool.QueryRow(ctx, `SELECT name FROM brands WHERE brand_id='BR01'`).Scan(&name)
	if n != 1 || name != "青禾新名" {
		t.Errorf("品牌信息变更应更新投影：%d %s", n, name)
	}
	if err := c.Handle(ctx, "fleet.brand.registered", []byte("not json")); err == nil {
		t.Error("格式不对的事件应报错（由消费者丢弃）")
	}
	if err := c.Handle(ctx, "fleet.brand.access_changed", []byte(`{"eventId":"x","type":"fleet.brand.access_changed","payload":{}}`)); err != nil {
		t.Errorf("未订阅的事件应忽略：%v", err)
	}
}

func TestCreate_引用的品牌必须已登记(t *testing.T) {
	e := newEnv(t, true)
	e.svc.CheckBrands = true
	c := &Consumer{Pool: testPool}
	if err := c.Handle(context.Background(), "fleet.brand.registered", brandEvent("e1", "BR01", "OP01", "青禾")); err != nil {
		t.Fatal(err)
	}
	body := func(ratios map[string]any) map[string]any {
		return map[string]any{"name": "品牌校验", "reason": "政府调价通知", "serviceFee": map[string]any{"ratios": ratios}}
	}
	before := countVersions(t)
	r := e.do("POST", "/api/rule/pricing/versions", editor, body(map[string]any{"BR01": 0.5, "BR07": 0.6, "BR05": 0.6}), nil)
	expect(t, r, 400, "INVALID")
	errs, _ := r.body["errors"].([]any)
	if len(errs) != 2 || errs[0].(map[string]any)["field"] != "serviceFee.ratios.BR05" {
		t.Errorf("应逐个指出未登记的品牌：%s", r.raw)
	}
	if countVersions(t) != before {
		t.Error("被拒绝不应写入版本")
	}
	expect(t, e.do("POST", "/api/rule/pricing/versions", editor, body(map[string]any{"BR01": 0.5}), nil), 200, "")
	e.clock.Set(anchor.Add(time.Hour))
	// 不改比例的版本不受影响（沿用旧版本里的品牌）
	expect(t, e.do("POST", "/api/rule/pricing/versions", editor, map[string]any{"name": "只改调度费", "reason": "政府调价通知", "dispatchFee": 600}, nil), 200, "")
}

func TestConsumer_经事件总线建投影(t *testing.T) {
	newEnv(t, false)
	nc, err := nats.Connect(testNats.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, _ := jetstream.New(nc)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = js.DeleteStream(ctx, fleetStream)
	if _, err := js.CreateStream(ctx, jetstream.StreamConfig{Name: fleetStream, Subjects: []string{"fleet.>"}, Storage: jetstream.FileStorage}); err != nil {
		t.Fatal(err)
	}
	for i, b := range []string{"BR01", "BR02"} {
		id := fmt.Sprintf("ev%d", i)
		if _, err := js.Publish(ctx, "fleet.brand.registered", brandEvent(id, b, "OP01", "品牌"+b), jetstream.WithMsgID(id)); err != nil {
			t.Fatal(err)
		}
	}
	// 与本订阅无关的主题不会被取走
	_, _ = js.Publish(ctx, "fleet.vehicle.listed", []byte(`{}`))

	rctx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { (&Consumer{Pool: testPool, NC: nc}).Run(rctx); close(done) }()
	deadline := time.Now().Add(15 * time.Second)
	var n int
	for time.Now().Before(deadline) {
		_ = testPool.QueryRow(ctx, `SELECT count(*) FROM brands`).Scan(&n)
		if n == 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	stop()
	<-done
	if n != 2 {
		t.Fatalf("应经总线建出 2 个品牌，实得 %d", n)
	}
}
