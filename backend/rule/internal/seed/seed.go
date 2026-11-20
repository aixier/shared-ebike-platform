// Package seed 载入原型样例（原型/mockdata/pricing.json，金额已换成分）。
// 只在配置 RULE_SEED=prototype 且库为空时用于本机演示与联调；生产不载入。
// 时间按 SCHEMA.md 第 3 节整体平移：days = 今天 − 锚点日期，让"今天"与原型的"现在"对齐。
package seed

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"bike/rule/internal/domain"
)

//go:embed prototype.json
var prototypeJSON []byte

var cst = time.FixedZone("+08:00", 8*3600)

const layout = "2006-01-02 15:04:05"

type rawVersion struct {
	VersionID   string       `json:"versionId"`
	Name        string       `json:"name"`
	EffectiveAt string       `json:"effectiveAt"`
	CreatedAt   string       `json:"createdAt"`
	CreatedBy   domain.Actor `json:"createdBy"`
	Note        *string      `json:"note"`
	domain.Content
}

type rawFile struct {
	Anchor         string       `json:"anchor"`
	Versions       []rawVersion `json:"versions"`
	PlatformParams struct {
		UpdatedAt string            `json:"updatedAt"`
		UpdatedBy domain.Actor      `json:"updatedBy"`
		Items     domain.ParamItems `json:"items"`
	} `json:"platformParams"`
}

// Params 平台参数样例。
type Params struct {
	UpdatedAt time.Time
	UpdatedBy domain.Actor
	Items     domain.ParamItems
}

// Data 样例全集。
type Data struct {
	Versions []domain.Version
	Params   Params
}

// Load 解析样例，并按 now 所在日期（北京时间）平移全部时刻。
func Load(now time.Time) (*Data, error) {
	var f rawFile
	if err := json.Unmarshal(prototypeJSON, &f); err != nil {
		return nil, fmt.Errorf("样例文件损坏：%w", err)
	}
	anchor, err := time.ParseInLocation("2006-01-02", f.Anchor, cst)
	if err != nil {
		return nil, err
	}
	n := now.In(cst)
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, cst)
	days := int(today.Sub(anchor).Hours() / 24)
	shift := func(s string) (time.Time, error) {
		t, err := time.ParseInLocation(layout, s, cst)
		if err != nil {
			return time.Time{}, err
		}
		return t.AddDate(0, 0, days), nil
	}
	out := &Data{}
	for _, rv := range f.Versions {
		eff, err := shift(rv.EffectiveAt)
		if err != nil {
			return nil, err
		}
		created, err := shift(rv.CreatedAt)
		if err != nil {
			return nil, err
		}
		seq, err := strconv.Atoi(strings.TrimPrefix(rv.VersionID, "PR"))
		if err != nil {
			return nil, fmt.Errorf("版本号不合法：%s", rv.VersionID)
		}
		out.Versions = append(out.Versions, domain.Version{
			VersionID: rv.VersionID, Seq: seq, Name: rv.Name, EffectiveAt: eff, CreatedAt: created,
			CreatedBy: rv.CreatedBy, Note: rv.Note, Content: rv.Content,
		})
	}
	up, err := shift(f.PlatformParams.UpdatedAt)
	if err != nil {
		return nil, err
	}
	out.Params = Params{UpdatedAt: up, UpdatedBy: f.PlatformParams.UpdatedBy, Items: f.PlatformParams.Items}
	return out, nil
}
