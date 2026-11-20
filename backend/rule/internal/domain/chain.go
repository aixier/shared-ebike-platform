package domain

import (
	"sort"
	"time"
)

// activeChain 返回未撤销的版本，按生效时刻、版本序号从早到晚排列。
func activeChain(vs []Version) []Version {
	out := make([]Version, 0, len(vs))
	for _, v := range vs {
		if v.RevokedAt == nil {
			out = append(out, v)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].EffectiveAt.Equal(out[j].EffectiveAt) {
			return out[i].EffectiveAt.Before(out[j].EffectiveAt)
		}
		return out[i].Seq < out[j].Seq
	})
	return out
}

// At 返回时刻 t 有效的版本（effectiveAt ≤ t < 下一版生效时刻，不含已撤销）；没有则 nil。
func At(vs []Version, t time.Time) *Version {
	var cur *Version
	for _, v := range activeChain(vs) {
		if !v.EffectiveAt.After(t) {
			v := v
			cur = &v
		}
	}
	return cur
}

// Previous 返回链上紧挨在 v 之前的版本（v 生效时失效的那个）；没有则 nil。
func Previous(vs []Version, v Version) *Version {
	var prev *Version
	for _, x := range activeChain(vs) {
		if x.VersionID == v.VersionID {
			return prev
		}
		x := x
		prev = &x
	}
	return nil
}

// Chain 按当前时刻算出每个版本的状态与失效时刻，保持输入顺序返回对外形状。
func Chain(vs []Version, now time.Time) []View {
	chain := activeChain(vs)
	until := map[string]*time.Time{}
	status := map[string]string{}
	for i, v := range chain {
		var next *Version
		if i+1 < len(chain) {
			next = &chain[i+1]
		}
		switch {
		case v.EffectiveAt.After(now):
			status[v.VersionID] = StatusPending
		case next != nil && !next.EffectiveAt.After(now):
			status[v.VersionID] = StatusExpired
		default:
			status[v.VersionID] = StatusActive
		}
		if next != nil {
			t := next.EffectiveAt
			until[v.VersionID] = &t
		}
	}
	out := make([]View, 0, len(vs))
	for _, v := range vs {
		st, ok := status[v.VersionID]
		if !ok {
			st = StatusRevoked
		}
		out = append(out, toView(v, st, until[v.VersionID]))
	}
	return out
}

// ViewOf 算出单个版本的对外形状（状态依赖整条链）。
func ViewOf(vs []Version, id string, now time.Time) (View, bool) {
	for _, view := range Chain(vs, now) {
		if view.VersionID == id {
			return view, true
		}
	}
	return View{}, false
}

// SortNewestFirst 按生效时刻新到旧排序（同时刻按序号新到旧）。
func SortNewestFirst(vs []Version) {
	sort.SliceStable(vs, func(i, j int) bool {
		if !vs[i].EffectiveAt.Equal(vs[j].EffectiveAt) {
			return vs[i].EffectiveAt.After(vs[j].EffectiveAt)
		}
		return vs[i].Seq > vs[j].Seq
	})
}

func toView(v Version, status string, until *time.Time) View {
	return View{
		VersionID:      v.VersionID,
		Name:           v.Name,
		Status:         status,
		EffectiveAt:    FormatTime(v.EffectiveAt),
		EffectiveUntil: fmtPtr(until),
		CreatedAt:      FormatTime(v.CreatedAt),
		CreatedBy:      v.CreatedBy,
		RevokedAt:      fmtPtr(v.RevokedAt),
		RevokedBy:      v.RevokedBy,
		Note:           v.Note,
		Content:        v.Content,
	}
}
