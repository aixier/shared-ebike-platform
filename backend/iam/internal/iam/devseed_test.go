package iam

import (
	"context"
	"testing"
)

func TestDevAdminSeedOffByDefault(t *testing.T) {
	e := newEnv(t)
	var n int
	_ = e.db.QueryRow(context.Background(), `SELECT count(*) FROM iam_accounts WHERE username='admin'`).Scan(&n)
	if n != 0 {
		t.Fatal("开关未开时不应有 admin")
	}
	if err := e.svc.SeedDevAdmin(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	expectErr(t, e.do("POST", "/api/iam/login", "", map[string]any{"username": "admin", "password": "Shifan@2026", "client": "管理端"}), 401, "LOGIN_FAILED")
}

func TestDevAdminSeedReadonlyViewer(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ { // 幂等
		if err := e.svc.SeedDevAdmin(ctx, true); err != nil {
			t.Fatal(err)
		}
	}
	tok := e.login("admin", "管理端")
	me := e.do("GET", "/api/iam/me", tok, nil)
	if me.Status != 200 || me.Body["readonly"] != true {
		t.Fatalf("me: %s", me.Raw)
	}
	// 令牌里也是只读
	claims, err := e.svc.verifier.Verify(tok, e.now)
	if err != nil || !claims.Readonly {
		t.Fatalf("令牌 readonly 应为真: %v %+v", err, claims)
	}
	// 权限：无任何写类权限点
	perms := toStrings(me.Body["permissions"])
	for _, bad := range []string{"pricing.edit", "station.edit", "pile.edit", "refund.confirm", "refund.confirm.overLimit", "deposit.assist",
		"account.cancel.assist", "account.unfreeze", "order.forceEnd", "degrade.control", "degrade.closure.handle", "access.pause",
		"access.setExit", "settlement.adjust", "evidence.export", "platformParams.edit", "vehicle.edit.own"} {
		if contains(perms, bad) {
			t.Fatalf("不应含写权限 %s", bad)
		}
	}
	for _, want := range []string{"order.view", "settlement.manage", "audit.view", "ops.monitor", "account.manage"} {
		if !contains(perms, want) {
			t.Fatalf("缺查看权限 %s", want)
		}
	}
	// 菜单覆盖所有管理端岗位菜单
	menus := toStrings(me.Body["menus"])
	rows, err := e.db.Query(ctx, `SELECT DISTINCT u FROM iam_role_templates t, unnest(t.menus) u WHERE t.client='管理端' AND t.loginable`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cnt := 0
	for rows.Next() {
		var m string
		_ = rows.Scan(&m)
		cnt++
		if !contains(menus, m) {
			t.Fatalf("缺菜单 %s", m)
		}
	}
	if cnt < 10 || len(menus) != cnt {
		t.Fatalf("菜单数 %d / %d", len(menus), cnt)
	}
	// 查看可用，写被拒
	if r := e.do("GET", "/api/iam/accounts", tok, nil); r.Status != 200 {
		t.Fatalf("查看账号: %s", r.Raw)
	}
	if r := e.do("GET", "/api/iam/audit", tok, nil); r.Status != 200 {
		t.Fatalf("查看留痕: %s", r.Raw)
	}
	expectErr(t, e.do("POST", "/api/iam/accounts", tok, map[string]any{"username": "x.test", "name": "t", "role": "客服", "reason": "测试开通账号"}), 403, "READONLY")
	expectErr(t, e.do("PUT", "/api/iam/accounts/AC0001/status", tok, map[string]any{"status": "停用", "reason": "测试停用账号"}), 403, "READONLY")
}

func TestDevAdminSeedSwitchOffDisables(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.svc.SeedDevAdmin(ctx, true); err != nil {
		t.Fatal(err)
	}
	tok := e.login("admin", "管理端")
	if err := e.svc.SeedDevAdmin(ctx, false); err != nil {
		t.Fatal(err)
	}
	expectErr(t, e.do("POST", "/api/iam/login", "", map[string]any{"username": "admin", "password": "Shifan@2026", "client": "管理端"}), 403, "ACCOUNT_DISABLED")
	expectErr(t, e.do("GET", "/api/iam/me", tok, nil), 403, "ACCOUNT_DISABLED")
}

func TestDevAdminSeedDoesNotOverwriteForeignAccount(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, err := e.db.Exec(ctx, `UPDATE iam_accounts SET username='admin' WHERE account_id='AC0001'`)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SeedDevAdmin(ctx, true); err == nil {
		t.Fatal("不是种子创建的 admin 不应被覆盖")
	}
}
