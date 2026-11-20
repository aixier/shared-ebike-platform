package domain

import (
	"testing"
	"time"
)

// trace: BR-14 BR-15 任务 7

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func ver(id string, seq int, eff string) Version {
	return Version{VersionID: id, Seq: seq, Name: id, EffectiveAt: ts(eff), CreatedAt: ts(eff).Add(-time.Hour)}
}

func TestChain_状态与失效时刻(t *testing.T) {
	now := ts("2026-12-01T12:00:00+08:00")
	revokedAt := ts("2026-11-30T10:00:00+08:00")
	vs := []Version{
		ver("PR0001", 1, "2026-07-01T00:00:00+08:00"),
		ver("PR0002", 2, "2026-10-15T00:00:00+08:00"),
		ver("PR0003", 3, "2026-12-01T00:00:00+08:00"),
		ver("PR0004", 4, "2027-01-01T00:00:00+08:00"),
		ver("PR0005", 5, "2027-02-01T00:00:00+08:00"),
	}
	vs[4].RevokedAt = &revokedAt
	views := Chain(vs, now)
	want := map[string][2]string{
		"PR0001": {StatusExpired, "2026-10-15T00:00:00+08:00"},
		"PR0002": {StatusExpired, "2026-12-01T00:00:00+08:00"},
		"PR0003": {StatusActive, "2027-01-01T00:00:00+08:00"},
		"PR0004": {StatusPending, ""},
		"PR0005": {StatusRevoked, ""},
	}
	if len(views) != 5 {
		t.Fatalf("应返回 5 个版本，实得 %d", len(views))
	}
	active := 0
	for _, v := range views {
		w := want[v.VersionID]
		if v.Status != w[0] {
			t.Errorf("%s 状态应为 %s，实得 %s", v.VersionID, w[0], v.Status)
		}
		got := ""
		if v.EffectiveUntil != nil {
			got = *v.EffectiveUntil
		}
		if got != w[1] {
			t.Errorf("%s 失效时刻应为 %q，实得 %q", v.VersionID, w[1], got)
		}
		if v.Status == StatusActive {
			active++
		}
	}
	if active != 1 {
		t.Errorf("任一时刻恰好一个生效版本，实得 %d", active)
	}
}

func TestAt_按时刻取有效版本(t *testing.T) {
	revokedAt := ts("2026-11-30T10:00:00+08:00")
	vs := []Version{
		ver("PR0001", 1, "2026-07-01T00:00:00+08:00"),
		ver("PR0002", 2, "2026-10-15T00:00:00+08:00"),
		ver("PR0003", 3, "2026-12-01T00:00:00+08:00"),
		ver("PR0004", 4, "2027-01-01T00:00:00+08:00"),
	}
	vs[3].RevokedAt = &revokedAt
	cases := []struct {
		at   string
		want string
	}{
		{"2026-06-30T23:59:59+08:00", ""},
		{"2026-07-01T00:00:00+08:00", "PR0001"},
		// BR-14 样例：10-14 23:51 开锁按 PR0001（OD2026101412805）
		{"2026-10-14T23:51:00+08:00", "PR0001"},
		{"2026-10-15T00:00:00+08:00", "PR0002"},
		{"2026-12-01T12:00:00+08:00", "PR0003"},
		// PR0004 已撤销，不参与取值
		{"2027-03-01T00:00:00+08:00", "PR0003"},
	}
	for _, c := range cases {
		got := At(vs, ts(c.at))
		id := ""
		if got != nil {
			id = got.VersionID
		}
		if id != c.want {
			t.Errorf("at=%s 应取 %q，实得 %q", c.at, c.want, id)
		}
	}
}

func TestChain_输入顺序打乱也正确(t *testing.T) {
	now := ts("2026-12-01T12:00:00+08:00")
	vs := []Version{
		ver("PR0003", 3, "2026-12-01T00:00:00+08:00"),
		ver("PR0001", 1, "2026-07-01T00:00:00+08:00"),
	}
	views := Chain(vs, now)
	if views[0].VersionID != "PR0003" || views[0].Status != StatusActive {
		t.Errorf("应保持输入顺序且 PR0003 生效中：%+v", views[0])
	}
	if views[1].Status != StatusExpired {
		t.Errorf("PR0001 应已失效，实得 %s", views[1].Status)
	}
}

func TestSortForList_新到旧(t *testing.T) {
	vs := []Version{
		ver("PR0001", 1, "2026-07-01T00:00:00+08:00"),
		ver("PR0003", 3, "2026-12-01T00:00:00+08:00"),
		ver("PR0002", 2, "2026-10-15T00:00:00+08:00"),
	}
	SortNewestFirst(vs)
	if vs[0].VersionID != "PR0003" || vs[2].VersionID != "PR0001" {
		t.Errorf("排序不对：%s %s %s", vs[0].VersionID, vs[1].VersionID, vs[2].VersionID)
	}
}
