package core

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bike/ride/internal/auth"
	"bike/ride/internal/geo"

	"github.com/golang-jwt/jwt/v5"
)

// 第二轮：订单状态"开锁中"、开锁位置 unlockLocation（{lat,lng}）。
func TestUnlockingStatusAndUnlockLocation(t *testing.T) {
	e := newEnv(t)
	e.svc.snap.mu.Lock()
	e.svc.snap.vehicles["VH000004"].Location = &geo.Pt{104.0657, 30.6595}
	e.svc.snap.mu.Unlock()

	r := e.do("POST", "/api/ride/unlock", "U100341", map[string]any{"code": "SF86020226"})
	if r.status != 200 {
		t.Fatalf("开锁：%d %s", r.status, r.raw)
	}
	loc, _ := r.body["unlockLocation"].(map[string]any)
	if loc == nil || loc["lat"] != 30.6595 || loc["lng"] != 104.0657 {
		t.Fatalf("开锁位置应为 {lat,lng}：%s", r.raw)
	}
	if sl, _ := r.body["startLocation"].([]any); len(sl) != 2 {
		t.Fatalf("startLocation 保持数组：%s", r.raw)
	}
	// 开锁中的订单：对外就是"开锁中"，状态筛选精确
	o := &Order{OrderID: "OD2026112000009", UserID: "U100999", VehicleID: "VH000005", BrandID: "BR01", PlateNo: "QH0005", OperatorID: "OP01",
		PricingVersionID: "PR0003", Status: StUnlocking, RequestAt: ts(time.Now()), Rev: 1, UnlockLocation: &LatLng{Lat: 30.1, Lng: 104.2}}
	if err := e.svc.saveOrder(context.Background(), o, true, nil); err != nil {
		t.Fatal(err)
	}
	adm := "admin:" + e.adminToken([]string{"order.view"}, nil)
	g := e.do("GET", "/api/ride/admin/orders/OD2026112000009", adm, nil)
	if g.status != 200 || g.body["status"] != "开锁中" || g.body["unlockLocation"].(map[string]any)["lat"] != 30.1 {
		t.Fatalf("开锁中详情：%s", g.raw)
	}
	l := e.do("GET", "/api/ride/admin/orders?status=开锁中", adm, nil)
	if rows := l.body["rows"].([]any); l.status != 200 || len(rows) != 1 {
		t.Fatalf("按开锁中筛：%s", l.raw)
	}
	l = e.do("GET", "/api/ride/admin/orders?status=骑行中", adm, nil)
	for _, row := range l.body["rows"].([]any) {
		if row.(map[string]any)["orderId"] == "OD2026112000009" {
			t.Fatalf("骑行中筛选不应含开锁中：%s", l.raw)
		}
	}
	// 当前骑行中接口只返回骑行中订单，开锁中不算
	if c := e.do("GET", "/api/ride/current", "U100999", nil); c.status != 204 {
		t.Fatalf("开锁中不应出现在骑行中：%d %s", c.status, c.raw)
	}
}

// 无位置的车 unlockLocation 为 null。
func TestUnlockLocationNull(t *testing.T) {
	e := newEnv(t)
	e.svc.snap.mu.Lock()
	e.svc.snap.vehicles["VH000004"].Location = nil
	e.svc.snap.mu.Unlock()
	r := e.do("POST", "/api/ride/unlock", "U100341", map[string]any{"code": "SF86020226"})
	if r.status != 200 {
		t.Fatalf("开锁：%s", r.raw)
	}
	if v, ok := r.body["unlockLocation"]; !ok || v != nil {
		t.Fatalf("无位置应为 null 且字段存在：%s", r.raw)
	}
}

// 第二轮：非 UTF-8 请求体 → 400 INVALID。
func TestRequestBodyNotUTF8(t *testing.T) {
	e := newEnv(t)
	gbk := []byte("{\"code\":\"\xd6\xd0\xce\xc4\"}") // "中文" 的 GBK 字节
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/ride/scan", bytes.NewReader(gbk))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(res.Body)
	if res.StatusCode != 400 || !bytes.Contains(buf.Bytes(), []byte(`"INVALID"`)) || !bytes.Contains(buf.Bytes(), []byte("请求体不是 UTF-8 编码")) {
		t.Fatalf("应 400 INVALID：%d %s", res.StatusCode, buf.String())
	}
	// 合法 UTF-8 中文照常
	if r := e.do("POST", "/api/ride/scan", "", map[string]any{"code": "中文"}); r.status != 404 {
		t.Fatalf("UTF-8 中文：%d %s", r.status, r.raw)
	}
}

// 第二轮：管理端写操作遇 readonly 令牌 → 403 READONLY（读不受影响）。
func TestAdminWriteReadonly(t *testing.T) {
	e := newEnv(t)
	tok := func(ro bool) string {
		tk := jwt.NewWithClaims(jwt.SigningMethodEdDSA, &auth.Admin{RegisteredClaims: jwt.RegisteredClaims{Issuer: "bike-iam", Subject: "AC0101", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))},
			Role: "客服", Perms: []string{"order.view"}, Readonly: ro})
		tk.Header["kid"] = "k1"
		s, _ := tk.SignedString(e.priv)
		return s
	}
	h := e.svc.adminReq(e.v, func(w http.ResponseWriter, r *http.Request, a *auth.Admin) { w.WriteHeader(200) })
	call := func(method string, ro bool) (int, string) {
		req := httptest.NewRequest(method, "/api/ride/admin/x", nil)
		req.Header.Set("Authorization", "Bearer "+tok(ro))
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec.Code, rec.Body.String()
	}
	if c, b := call("POST", true); c != 403 || !strings.Contains(b, `"READONLY"`) {
		t.Fatalf("只读令牌写操作应 403 READONLY：%d %s", c, b)
	}
	if c, _ := call("DELETE", true); c != 403 {
		t.Fatalf("DELETE 也拒绝：%d", c)
	}
	if c, b := call("GET", true); c != 200 {
		t.Fatalf("只读令牌可读：%d %s", c, b)
	}
	if c, b := call("POST", false); c != 200 {
		t.Fatalf("非只读可写：%d %s", c, b)
	}
}
