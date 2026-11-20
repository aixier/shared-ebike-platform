package app

import (
	"context"
	"testing"
)

// 任务 11：车联网服务商代接，一份凭证覆盖名下多家（R-37）

func TestProviderCredentialCoversMany(t *testing.T) {
	e := newEnv(t, nil)
	e.accessChanged("P1", "BR03", "OP02", "已接入")
	e.accessChanged("P2", "BR07", "OP06", "已接入")
	var n int
	_ = testPool.QueryRow(context.Background(), `SELECT count(*) FROM credentials`).Scan(&n)
	if n != 1 {
		t.Fatalf("服务商名下多家只一份凭证，得 %d", n)
	}
	c, _ := e.cred("OP06")
	if c.HolderType != "车联网服务商" || c.HolderName != "泰达车联网" || len(c.OperatorIDs) != 2 {
		t.Fatalf("%+v", c)
	}
	audits := outboxPayloads(t, "opgw.audit.recorded")
	if len(audits) != 2 || audits[1]["payload"].(map[string]any)["action"] != "调整凭证范围" {
		t.Fatalf("%v", audits)
	}
	// 服务商用同一份凭证为名下两家报事件
	e.unlockOK("OD2026120111001", "OP02", "BR03", "CL000707")
	e.unlockOK("OD2026120111002", "OP06", "BR07", "XX000001")
	for _, o := range []struct{ order, plate string }{{"OD2026120111001", "CL000707"}, {"OD2026120111002", "XX000001"}} {
		st, m := e.signed("OP02", "/open/v1/ride-events", unlockEvent("EV-"+o.order, o.order, o.plate), nil)
		if st != 200 || m["received"] != true {
			t.Fatalf("%d %v", st, m)
		}
	}
	// 一家退出：移出覆盖范围，凭证仍有效
	e.event("PX", "fleet.operator.exited", map[string]any{"operatorId": "OP06", "brandIds": []string{"BR07"}, "exitedAt": rfc3339(e.clock), "reason": "退出本市"})
	c, _ = e.cred("OP02")
	if c.Status != "有效" || len(c.OperatorIDs) != 1 || c.OperatorIDs[0] != "OP02" {
		t.Fatalf("%+v", c)
	}
	// 退出的那家的订单不再属于这份凭证
	st, m := e.signed("OP02", "/open/v1/ride-events", lockEvent("EV-L2", "OD2026120111002", "XX000001"), nil)
	if st != 403 || m["code"] != "SCOPE_DENIED" {
		t.Fatalf("%d %v", st, m)
	}
	// 最后一家退出：停用
	e.event("PY", "fleet.operator.exited", map[string]any{"operatorId": "OP02", "brandIds": []string{"BR03"}, "exitedAt": rfc3339(e.clock), "reason": "退出本市"})
	var status string
	_ = testPool.QueryRow(context.Background(), `SELECT status FROM credentials`).Scan(&status)
	if status != "停用" {
		t.Fatal(status)
	}
}
