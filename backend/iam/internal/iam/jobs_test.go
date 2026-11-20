package iam

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 总线发不出去时事件留在发件表，恢复后按顺序补发。
func TestRelayKeepsEventsWhenBusDown(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tok := e.login("yw.hao", "管理端")
	e.do("PUT", "/api/iam/accounts/AC0302/status", tok, map[string]any{"status": "停用", "reason": "人员离职停用"})
	n, err := e.svc.RelayOnce(ctx, func(context.Context, string, string, []byte) error { return errors.New("总线不可用") })
	if err == nil || n != 0 {
		t.Fatalf("应发送失败: %d %v", n, err)
	}
	var subjects []string
	n, err = e.svc.RelayOnce(ctx, func(_ context.Context, subj, id string, _ []byte) error {
		if id == "" {
			t.Fatal("缺消息编号")
		}
		subjects = append(subjects, subj)
		return nil
	})
	if err != nil || n != 2 || subjects[0] != "iam.audit.recorded" || subjects[1] != "iam.account.changed" {
		t.Fatalf("补发不对: %d %v %v", n, err, subjects)
	}
	n, _ = e.svc.RelayOnce(ctx, func(context.Context, string, string, []byte) error { return nil })
	if n != 0 {
		t.Fatal("已发出的不应重发")
	}
}

func TestCleanupExpiredSessions(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.login("kf.su", "管理端")
	tok := e.login("yw.hao", "管理端")
	e.do("PUT", "/api/iam/accounts/AC0302/status", tok, map[string]any{"status": "停用", "reason": "人员离职停用"}, "Idempotency-Key", "k1")
	e.now = e.now.Add(30 * 24 * time.Hour)
	if err := e.svc.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	var s, i, a int
	_ = e.db.QueryRow(ctx, "SELECT count(*) FROM iam_sessions").Scan(&s)
	_ = e.db.QueryRow(ctx, "SELECT count(*) FROM iam_idempotency").Scan(&i)
	_ = e.db.QueryRow(ctx, "SELECT count(*) FROM iam_audit").Scan(&a)
	if s != 0 || i != 0 || a == 0 {
		t.Fatalf("清理不对: 会话 %d 幂等 %d 留痕 %d", s, i, a)
	}
}
