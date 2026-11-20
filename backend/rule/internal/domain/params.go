package domain

import (
	"encoding/json"
	"reflect"
	"sort"
)

// 平台公司参数（需求 6.9）。初始值无甲方来源，不在代码里设缺省；未设置的项为 null。

// PauseThreshold 暂停可租门槛。
type PauseThreshold struct {
	UnlockSuccessRate *float64 `json:"unlockSuccessRate"`
	Availability      *float64 `json:"availability"`
	SustainMinutes    *int64   `json:"sustainMinutes"`
}

// Acceptance 验收条件。
type Acceptance struct {
	RealVehicles *int64 `json:"realVehicles"`
	RealOrders   *int64 `json:"realOrders"`
	TargetDays   *int64 `json:"targetDays"`
}

// RetentionYears 数据保存期限（年）。
type RetentionYears struct {
	Orders   *int64 `json:"orders"`
	Payments *int64 `json:"payments"`
	Audit    *int64 `json:"audit"`
	Evidence *int64 `json:"evidence"`
}

// ParamItems 平台公司参数项（契约 PlatformParamItems）。
type ParamItems struct {
	PayWindowHours         *int64          `json:"payWindowHours"`
	CsRefundLimit          *int64          `json:"csRefundLimit"`
	BackfillWindowHours    *int64          `json:"backfillWindowHours"`
	UnlockReportTimeoutSec *int64          `json:"unlockReportTimeoutSec"`
	NearbyRadiusM          *int64          `json:"nearbyRadiusM"`
	ArrearsFreezeDays      *int64          `json:"arrearsFreezeDays"`
	DepositRefundWorkdays  *int64          `json:"depositRefundWorkdays"`
	BillPushTargetSec      *int64          `json:"billPushTargetSec"`
	PauseThreshold         *PauseThreshold `json:"pauseThreshold"`
	Acceptance             *Acceptance     `json:"acceptance"`
	AvailabilityTarget     *float64        `json:"availabilityTarget"`
	UnlockP90TargetSec     *int64          `json:"unlockP90TargetSec"`
	RetentionYears         *RetentionYears `json:"retentionYears"`
	TrackRetentionDays     *int64          `json:"trackRetentionDays"`
	DeferredVerifyDays     *int64          `json:"deferredVerifyDays"`
	LegalAge               *int64          `json:"legalAge"`
}

// 两项有缺省值的参数（第二轮变更单）：未录入时按缺省值返回与发布，其余参数无甲方来源，仍为 null。
const (
	DefaultDeferredVerifyDays int64 = 3
	DefaultLegalAge           int64 = 16
)

// WithDefaults 把有缺省值而尚未录入的参数补上缺省值；it 不会被改动。
func (p ParamItems) WithDefaults() ParamItems {
	if p.DeferredVerifyDays == nil {
		v := DefaultDeferredVerifyDays
		p.DeferredVerifyDays = &v
	}
	if p.LegalAge == nil {
		v := DefaultLegalAge
		p.LegalAge = &v
	}
	return p
}

var (
	rPositive = intRange{1, -1, 0, ""}
	rMoneyNN  = intRange{0, -1, 0, "分"}
	rDeferred = intRange{1, 30, 0, "天"}
	rLegalAge = intRange{10, 30, 0, "岁"}
)

// rateDecimals 比率类参数（成功率、可用率、目标值）允许的小数位。
const rateDecimals = 4

var paramKeys = map[string]bool{
	"payWindowHours": true, "csRefundLimit": true, "backfillWindowHours": true, "unlockReportTimeoutSec": true,
	"nearbyRadiusM": true, "arrearsFreezeDays": true, "depositRefundWorkdays": true, "billPushTargetSec": true,
	"pauseThreshold": true, "acceptance": true, "availabilityTarget": true, "unlockP90TargetSec": true,
	"retentionYears": true, "trackRetentionDays": true, "deferredVerifyDays": true, "legalAge": true,
}

// ParseParamsPatch 校验要改的参数项（请求体 items）；至少一项，逐项返回错误。
func ParseParamsPatch(v any) (ParamItems, []FieldError) {
	c := &checker{}
	m, ok := v.(map[string]any)
	if !ok || len(m) == 0 {
		return ParamItems{}, []FieldError{{Field: "items", Message: "请至少修改一项参数"}}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !paramKeys[k] {
			c.add(nil, "items."+k, "不认识的参数项 "+k)
		}
	}
	p := ParamItems{
		PayWindowHours:         c.int(m, "payWindowHours", "items.payWindowHours", "一键支付期限（小时）", rPositive, nil),
		CsRefundLimit:          c.int(m, "csRefundLimit", "items.csRefundLimit", "客服退款授权额度", rMoneyNN, nil),
		BackfillWindowHours:    c.int(m, "backfillWindowHours", "items.backfillWindowHours", "补报窗口（小时）", rPositive, nil),
		UnlockReportTimeoutSec: c.int(m, "unlockReportTimeoutSec", "items.unlockReportTimeoutSec", "开锁报告时限（秒）", rPositive, nil),
		NearbyRadiusM:          c.int(m, "nearbyRadiusM", "items.nearbyRadiusM", "附近车辆范围（米）", rPositive, nil),
		ArrearsFreezeDays:      c.int(m, "arrearsFreezeDays", "items.arrearsFreezeDays", "欠费冻结天数", rPositive, nil),
		DepositRefundWorkdays:  c.int(m, "depositRefundWorkdays", "items.depositRefundWorkdays", "退押时限（工作日）", rPositive, nil),
		BillPushTargetSec:      c.int(m, "billPushTargetSec", "items.billPushTargetSec", "账单推送目标（秒）", rPositive, nil),
		AvailabilityTarget:     c.decimal(m, "availabilityTarget", "items.availabilityTarget", "可用性目标值", 0, 1, rateDecimals),
		UnlockP90TargetSec:     c.int(m, "unlockP90TargetSec", "items.unlockP90TargetSec", "开锁 P90 目标（秒）", rPositive, nil),
		TrackRetentionDays:     c.int(m, "trackRetentionDays", "items.trackRetentionDays", "轨迹保存天数", rPositive, nil),
		DeferredVerifyDays:     c.int(m, "deferredVerifyDays", "items.deferredVerifyDays", "延迟核验天数", rDeferred, nil),
		LegalAge:               c.int(m, "legalAge", "items.legalAge", "法定年龄", rLegalAge, nil),
	}
	if o, ok := c.obj(m, "pauseThreshold", "items.pauseThreshold", "暂停可租门槛"); ok {
		p.PauseThreshold = &PauseThreshold{
			UnlockSuccessRate: c.decimal(o, "unlockSuccessRate", "items.pauseThreshold.unlockSuccessRate", "开锁成功率门槛", 0, 1, rateDecimals),
			Availability:      c.decimal(o, "availability", "items.pauseThreshold.availability", "可用率门槛", 0, 1, rateDecimals),
			SustainMinutes:    c.int(o, "sustainMinutes", "items.pauseThreshold.sustainMinutes", "持续分钟数", rPositive, nil),
		}
	}
	if o, ok := c.obj(m, "acceptance", "items.acceptance", "验收条件"); ok {
		p.Acceptance = &Acceptance{
			RealVehicles: c.int(o, "realVehicles", "items.acceptance.realVehicles", "真车联调车辆数", rPositive, nil),
			RealOrders:   c.int(o, "realOrders", "items.acceptance.realOrders", "真车联调订单数", rPositive, nil),
			TargetDays:   c.int(o, "targetDays", "items.acceptance.targetDays", "接入目标天数", rPositive, nil),
		}
	}
	if o, ok := c.obj(m, "retentionYears", "items.retentionYears", "数据保存期限"); ok {
		p.RetentionYears = &RetentionYears{
			Orders:   c.int(o, "orders", "items.retentionYears.orders", "订单保存年限", rPositive, nil),
			Payments: c.int(o, "payments", "items.retentionYears.payments", "资金流水保存年限", rPositive, nil),
			Audit:    c.int(o, "audit", "items.retentionYears.audit", "操作留痕保存年限", rPositive, nil),
			Evidence: c.int(o, "evidence", "items.retentionYears.evidence", "执法证据保存年限", rPositive, nil),
		}
	}
	if len(c.errs) > 0 {
		return ParamItems{}, c.errs
	}
	return p, nil
}

// MergeParams 把改动项套到现值上；嵌套项只改传入的字段。cur 不会被改动。
func MergeParams(cur, p ParamItems) ParamItems {
	out := cur
	pick := func(dst **int64, src *int64) {
		if src != nil {
			v := *src
			*dst = &v
		}
	}
	pickF := func(dst **float64, src *float64) {
		if src != nil {
			v := *src
			*dst = &v
		}
	}
	pick(&out.PayWindowHours, p.PayWindowHours)
	pick(&out.CsRefundLimit, p.CsRefundLimit)
	pick(&out.BackfillWindowHours, p.BackfillWindowHours)
	pick(&out.UnlockReportTimeoutSec, p.UnlockReportTimeoutSec)
	pick(&out.NearbyRadiusM, p.NearbyRadiusM)
	pick(&out.ArrearsFreezeDays, p.ArrearsFreezeDays)
	pick(&out.DepositRefundWorkdays, p.DepositRefundWorkdays)
	pick(&out.BillPushTargetSec, p.BillPushTargetSec)
	pickF(&out.AvailabilityTarget, p.AvailabilityTarget)
	pick(&out.UnlockP90TargetSec, p.UnlockP90TargetSec)
	pick(&out.TrackRetentionDays, p.TrackRetentionDays)
	pick(&out.DeferredVerifyDays, p.DeferredVerifyDays)
	pick(&out.LegalAge, p.LegalAge)
	if p.PauseThreshold != nil {
		n := PauseThreshold{}
		if cur.PauseThreshold != nil {
			n = *cur.PauseThreshold
		}
		pickF(&n.UnlockSuccessRate, p.PauseThreshold.UnlockSuccessRate)
		pickF(&n.Availability, p.PauseThreshold.Availability)
		pick(&n.SustainMinutes, p.PauseThreshold.SustainMinutes)
		out.PauseThreshold = &n
	}
	if p.Acceptance != nil {
		n := Acceptance{}
		if cur.Acceptance != nil {
			n = *cur.Acceptance
		}
		pick(&n.RealVehicles, p.Acceptance.RealVehicles)
		pick(&n.RealOrders, p.Acceptance.RealOrders)
		pick(&n.TargetDays, p.Acceptance.TargetDays)
		out.Acceptance = &n
	}
	if p.RetentionYears != nil {
		n := RetentionYears{}
		if cur.RetentionYears != nil {
			n = *cur.RetentionYears
		}
		pick(&n.Orders, p.RetentionYears.Orders)
		pick(&n.Payments, p.RetentionYears.Payments)
		pick(&n.Audit, p.RetentionYears.Audit)
		pick(&n.Evidence, p.RetentionYears.Evidence)
		out.RetentionYears = &n
	}
	return out
}

// DiffParams 列出改动的参数项（顶层键）的旧值与新值，用于留痕。
func DiffParams(before, after ParamItems) (map[string]any, map[string]any) {
	return diffTop(before, after)
}

// diffTop 按 JSON 顶层键比较两个值，返回改动键的旧值与新值。
func diffTop(before, after any) (map[string]any, map[string]any) {
	var b, a map[string]any
	bb, _ := json.Marshal(before)
	ab, _ := json.Marshal(after)
	_ = json.Unmarshal(bb, &b)
	_ = json.Unmarshal(ab, &a)
	ob, oa := map[string]any{}, map[string]any{}
	for k, av := range a {
		if bv := b[k]; !reflect.DeepEqual(av, bv) {
			ob[k], oa[k] = bv, av
		}
	}
	return ob, oa
}
