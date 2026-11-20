// Package calc 是结算的纯算术：服务费基数、单单服务费、归月、出单与调整项、合计、月卡分摊。
// 不碰数据库、不读时钟，输入输出全部显式；金额一律整数分。
// 口径见原型 mockdata/SCHEMA.md 4.2～4.4 与需求 BR-17～BR-20。
package calc

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Loc 是归月、到账期限所用的时区（东八区）。
var Loc = time.FixedZone("CST", 8*3600)

// 服务费基数口径（规则版本 serviceFee.basis 的取值）。
const (
	BasisReceivable  = "订单应收"
	BasisNetReceived = "订单实收"
)

// 调整项类型。
const (
	TypeLate    = "晚到补报"
	TypeRefund  = "出单后退款"
	TypeDispute = "异议核实"
	TypeManual  = "人工调整"
)

// Ratio 是服务费比例，以百万分之一为单位（0.62 = 620000），避免浮点误差。
type Ratio int64

const ratioScale = 1000000

// ParseRatio 解析 0～1 之间、最多 6 位小数的比例。
func ParseRatio(s string) (Ratio, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("比例为空")
	}
	intPart, frac, hasDot := strings.Cut(s, ".")
	if strings.Contains(frac, ".") || intPart == "" || (hasDot && frac == "") || len(frac) > 6 {
		return 0, fmt.Errorf("比例格式不正确: %q", s)
	}
	ip, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil || ip < 0 || intPart[0] < '0' || intPart[0] > '9' {
		return 0, fmt.Errorf("比例格式不正确: %q", s)
	}
	var fp int64
	if frac != "" {
		for _, ch := range frac {
			if ch < '0' || ch > '9' {
				return 0, fmt.Errorf("比例格式不正确: %q", s)
			}
		}
		fp, _ = strconv.ParseInt(frac+strings.Repeat("0", 6-len(frac)), 10, 64)
	}
	r := Ratio(ip*ratioScale + fp)
	if r > ratioScale {
		return 0, fmt.Errorf("比例超过 1: %q", s)
	}
	return r, nil
}

// String 输出最短的十进制表示，如 0.62、1、0。
func (r Ratio) String() string {
	ip := int64(r) / ratioScale
	fp := int64(r) % ratioScale
	if fp == 0 {
		return strconv.FormatInt(ip, 10)
	}
	f := strings.TrimRight(fmt.Sprintf("%06d", fp), "0")
	return fmt.Sprintf("%d.%s", ip, f)
}

// MarshalJSON 以 JSON 数字输出（如 0.62）。
func (r Ratio) MarshalJSON() ([]byte, error) { return []byte(r.String()), nil }

// UnmarshalJSON 接受 JSON 数字或字符串。
func (r *Ratio) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	v, err := ParseRatio(s)
	if err != nil {
		return err
	}
	*r = v
	return nil
}

// Fee 单单服务费 = 基数 × 比例，四舍五入到分（负数对称，远离零）。
func Fee(base int64, r Ratio) int64 {
	p := base * int64(r)
	if p >= 0 {
		return (p + ratioScale/2) / ratioScale
	}
	return -((-p + ratioScale/2) / ratioScale)
}

// MonthOf 返回时刻所在的结算月（东八区）。
func MonthOf(t time.Time) string { return t.In(Loc).Format("2006-01") }

// ValidMonth 判断 YYYY-MM 格式与月份范围。
func ValidMonth(m string) bool {
	if len(m) != 7 || m[4] != '-' {
		return false
	}
	_, err := time.ParseInLocation("2006-01", m, Loc)
	return err == nil
}

// MonthStart 返回结算月第一天 00:00（东八区）。
func MonthStart(m string) (time.Time, error) {
	if !ValidMonth(m) {
		return time.Time{}, fmt.Errorf("结算月格式不正确: %q", m)
	}
	return time.ParseInLocation("2006-01", m, Loc)
}

// NextMonth 下一个结算月。
func NextMonth(m string) string {
	s, _ := MonthStart(m)
	return s.AddDate(0, 1, 0).Format("2006-01")
}

// PrevMonth 上一个结算月。
func PrevMonth(m string) string {
	s, _ := MonthStart(m)
	return s.AddDate(0, -1, 0).Format("2006-01")
}

// DueDate 到账期限：次月 6 日（含）。
func DueDate(m string) string { return NextMonth(m) + "-06" }

// Version 是结算用到的规则版本内容。
type Version struct {
	ID           string
	EffectiveAt  time.Time
	Basis        string
	Ratios       map[string]Ratio
	StartFee     int64
	StartMinutes int
	PerMinute    int64
	DispatchFee  int64
	Allocation   string
	Revoked      bool
}

// RideFee 骑行费 = 起步价 + max(0, 计费分钟 − 起步时长) × 每分钟价。
func RideFee(v Version, minutes int) int64 {
	extra := minutes - v.StartMinutes
	if extra < 0 {
		extra = 0
	}
	return v.StartFee + int64(extra)*v.PerMinute
}

// Order 是结算用的订单事实（来自 ride 与 bill 的事件投影）。
type Order struct {
	OrderID          string
	BrandID          string
	OperatorID       string
	PricingVersionID string
	RequestAt        time.Time
	StartAt          time.Time
	EndAt            time.Time
	ReportReceivedAt time.Time
	BilledMinutes    int
	DistanceKm       *float64
	Degraded         bool
	UnlockResult     string // 成功｜超时后成功
	HasBill          bool
	Receivable       int64
	DispatchFee      int64
	Payable          int64
	MonthCardDeduct  int64
}

// SettleMonth 订单的结算月 = 结束时刻所在月。
func (o Order) SettleMonth() string { return MonthOf(o.EndAt) }

// CorrectionDelta 异议采纳时的基数更正额：按核实分钟数重算骑行费，调度费不变，减去原应收。
func CorrectionDelta(v Version, o Order, minutes int) int64 {
	return RideFee(v, minutes) + o.DispatchFee - o.Receivable
}

// Refund 是一次退款成功后的累计状态。
type Refund struct {
	OrderID       string
	RefundedTotal int64
	NetReceived   int64
	Kind          string // 部分退款｜全额退款
	At            time.Time
}

// Correction 是采纳异议后的基数更正。
type Correction struct {
	OrderID   string
	DisputeID string
	BaseDelta int64
	At        time.Time
}

func latestRefund(rs []Refund, t time.Time) *Refund {
	var best *Refund
	for i := range rs {
		r := &rs[i]
		if r.At.After(t) {
			continue
		}
		if best == nil || r.RefundedTotal > best.RefundedTotal || (r.RefundedTotal == best.RefundedTotal && r.At.After(best.At)) {
			best = r
		}
	}
	return best
}

// RefundedAt 截至 t 的累计已退款。
func RefundedAt(rs []Refund, t time.Time) int64 {
	if r := latestRefund(rs, t); r != nil {
		return r.RefundedTotal
	}
	return 0
}

// BaseAt 订单在时刻 t 的服务费基数。
// 应收为 0 → 0；已全额退款 → 0；应收口径：应收 − 累计退款；实收口径：退款后实收（未退款取应付）；再加已采纳异议的更正额。
func BaseAt(o Order, basis string, refunds []Refund, corr []Correction, t time.Time) int64 {
	if !o.HasBill || o.Receivable == 0 {
		return 0
	}
	rf := latestRefund(refunds, t)
	if rf != nil && rf.Kind == "全额退款" {
		return 0
	}
	var b int64
	if basis == BasisNetReceived {
		b = o.Payable
		if rf != nil {
			b = rf.NetReceived
		}
	} else {
		b = o.Receivable
		if rf != nil {
			b -= rf.RefundedTotal
		}
	}
	for _, c := range corr {
		if !c.At.After(t) {
			b += c.BaseDelta
		}
	}
	return b
}

// Settled 是订单最近一次进入结算记录后的状态。
type Settled struct {
	Base         int64
	Fee          int64 // 所有结算记录的服务费累加
	At           time.Time
	SettlementID string
}

// Line 结算单逐单明细。
type Line struct {
	OrderID              string
	BrandID              string
	StartAt              time.Time
	EndAt                time.Time
	BilledMinutes        int
	DistanceKm           *float64
	Degraded             bool
	PricingVersionID     string
	Receivable           int64
	RefundedAtGeneration int64
	Base                 int64
	Ratio                Ratio
	Fee                  int64
	Remark               string
}

// AdjItem 调整项逐单记录。
type AdjItem struct {
	OrderID      string
	EndAt        time.Time
	PreviousBase int64
	NewBase      int64
	BaseDelta    int64
	Ratio        Ratio
	FeeDelta     int64
}

// AdjDraft 出单时由代码算出的调整项（待财务确认）。
type AdjDraft struct {
	Type      string
	BrandID   string
	DisputeID string
	Items     []AdjItem
	BaseDelta int64
	FeeDelta  int64
}

// Input 某运营商某结算月出单（或预览）的输入。
type Input struct {
	Month       string
	At          time.Time
	Orders      []Order
	Versions    map[string]Version
	Refunds     map[string][]Refund
	Corrections map[string][]Correction
	Settled     map[string]Settled
}

// Ready 订单能否进结算记录：已收到账单、已收到其规则版本、平台在 at 之前收到结束报告。
func Ready(o Order, vs map[string]Version, at time.Time) bool {
	if !o.HasBill || o.ReportReceivedAt.IsZero() || o.ReportReceivedAt.After(at) {
		return false
	}
	_, ok := vs[o.PricingVersionID]
	return ok
}

// RatioOf 开锁时刻版本中该品牌的比例；版本里没有该品牌时为 0。
func RatioOf(o Order, vs map[string]Version) Ratio { return vs[o.PricingVersionID].Ratios[o.BrandID] }

// Build 计算某运营商某结算月的明细与调整项。
func Build(in Input) ([]Line, []AdjDraft) {
	orders := append([]Order(nil), in.Orders...)
	sort.SliceStable(orders, func(i, j int) bool {
		if !orders[i].RequestAt.Equal(orders[j].RequestAt) {
			return orders[i].RequestAt.Before(orders[j].RequestAt)
		}
		return orders[i].OrderID < orders[j].OrderID
	})
	var lines []Line
	type gk struct{ typ, brand, dispute string }
	groups := map[gk]*AdjDraft{}
	var order []gk
	push := func(k gk, it AdjItem) {
		g, ok := groups[k]
		if !ok {
			g = &AdjDraft{Type: k.typ, BrandID: k.brand, DisputeID: k.dispute}
			groups[k] = g
			order = append(order, k)
		}
		g.Items = append(g.Items, it)
		g.BaseDelta += it.BaseDelta
		g.FeeDelta += it.FeeDelta
	}
	for _, o := range orders {
		if !Ready(o, in.Versions, in.At) {
			continue
		}
		v := in.Versions[o.PricingVersionID]
		r := RatioOf(o, in.Versions)
		last, settled := in.Settled[o.OrderID]
		sm := o.SettleMonth()
		if !settled {
			switch {
			case sm == in.Month:
				b := BaseAt(o, v.Basis, in.Refunds[o.OrderID], in.Corrections[o.OrderID], in.At)
				rf := RefundedAt(in.Refunds[o.OrderID], in.At)
				lines = append(lines, Line{OrderID: o.OrderID, BrandID: o.BrandID, StartAt: o.StartAt, EndAt: o.EndAt, BilledMinutes: o.BilledMinutes, DistanceKm: o.DistanceKm, Degraded: o.Degraded, PricingVersionID: o.PricingVersionID, Receivable: o.Receivable, RefundedAtGeneration: rf, Base: b, Ratio: r, Fee: Fee(b, r), Remark: Remark(o, b, rf)})
			case sm < in.Month:
				nb := BaseAt(o, v.Basis, in.Refunds[o.OrderID], in.Corrections[o.OrderID], in.At)
				push(gk{TypeLate, o.BrandID, ""}, AdjItem{OrderID: o.OrderID, EndAt: o.EndAt, PreviousBase: 0, NewBase: nb, BaseDelta: nb, Ratio: r, FeeDelta: Fee(nb, r)})
			}
			continue
		}
		if !last.At.Before(in.At) {
			continue
		}
		nb := BaseAt(o, v.Basis, in.Refunds[o.OrderID], in.Corrections[o.OrderID], in.At)
		if nb == last.Base {
			continue
		}
		k := gk{TypeRefund, o.BrandID, ""}
		for _, c := range in.Corrections[o.OrderID] {
			if c.At.After(last.At) && !c.At.After(in.At) {
				k = gk{TypeDispute, o.BrandID, c.DisputeID}
				break
			}
		}
		push(k, AdjItem{OrderID: o.OrderID, EndAt: o.EndAt, PreviousBase: last.Base, NewBase: nb, BaseDelta: nb - last.Base, Ratio: r, FeeDelta: Fee(nb, r) - last.Fee})
	}
	sort.SliceStable(lines, func(i, j int) bool {
		if !lines[i].EndAt.Equal(lines[j].EndAt) {
			return lines[i].EndAt.Before(lines[j].EndAt)
		}
		return lines[i].OrderID < lines[j].OrderID
	})
	// 调整项按（类型、品牌、异议编号）排序，编号顺序稳定
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].typ != order[j].typ {
			return order[i].typ < order[j].typ
		}
		if order[i].brand != order[j].brand {
			return order[i].brand < order[j].brand
		}
		return order[i].dispute < order[j].dispute
	})
	adjs := make([]AdjDraft, 0, len(order))
	for _, k := range order {
		adjs = append(adjs, *groups[k])
	}
	return lines, adjs
}

// Remark 明细备注：0 元、全额退款、部分退款、降级单（事实说明，不含判断）。
func Remark(o Order, base, refunded int64) string {
	var parts []string
	switch {
	case o.Receivable == 0 && o.UnlockResult == "超时后成功":
		parts = append(parts, "超时后才报告开锁成功，0 元")
	case o.Receivable == 0:
		parts = append(parts, "0 元订单")
	case base == 0:
		parts = append(parts, "已全额退款，不计")
	case refunded > 0:
		parts = append(parts, "已部分退款，按退款后金额计")
	}
	if o.Degraded {
		parts = append(parts, "降级单")
	}
	return strings.Join(parts, "；")
}

// Apply 出单后更新每单的结算状态（明细与带逐单记录的调整项）。
func Apply(settled map[string]Settled, settlementID string, lines []Line, adjs []AdjDraft, at time.Time) {
	for _, l := range lines {
		settled[l.OrderID] = Settled{Base: l.Base, Fee: l.Fee, At: at, SettlementID: settlementID}
	}
	for _, a := range adjs {
		for _, it := range a.Items {
			prev, ok := settled[it.OrderID]
			sid := settlementID
			if ok {
				sid = prev.SettlementID
			}
			settled[it.OrderID] = Settled{Base: it.NewBase, Fee: prev.Fee + it.FeeDelta, At: at, SettlementID: sid}
		}
	}
}

// Totals 结算单合计；PayableFee = DetailFee + AdjustmentFee。
type Totals struct {
	OrderCount    int   `json:"orderCount"`
	ReceivableSum int64 `json:"receivableSum"`
	BaseSum       int64 `json:"baseSum"`
	DetailFee     int64 `json:"detailFee"`
	AdjustmentFee int64 `json:"adjustmentFee"`
	PayableFee    int64 `json:"payableFee"`
}

// RatioGroup 同一品牌按比例分组。
type RatioGroup struct {
	Ratio             Ratio
	PricingVersionIDs []string
	OrderCount        int
	BaseSum           int64
	Fee               int64
}

// BrandSum 结算单上某品牌的汇总。
type BrandSum struct {
	BrandID        string
	OrderCount     int
	ReceivableSum  int64
	BaseSum        int64
	DetailFee      int64
	RatioBreakdown []RatioGroup
	AdjustmentFee  int64
}

// Summarize 计算合计与品牌汇总（品牌按编号升序，比例分组按比例升序）。
func Summarize(lines []Line, adjs []AdjDraft) (Totals, []BrandSum) {
	var t Totals
	brands := map[string]*BrandSum{}
	groups := map[string]map[Ratio]*RatioGroup{}
	get := func(id string) *BrandSum {
		b, ok := brands[id]
		if !ok {
			b = &BrandSum{BrandID: id}
			brands[id] = b
			groups[id] = map[Ratio]*RatioGroup{}
		}
		return b
	}
	for _, l := range lines {
		t.OrderCount++
		t.ReceivableSum += l.Receivable
		t.BaseSum += l.Base
		t.DetailFee += l.Fee
		b := get(l.BrandID)
		b.OrderCount++
		b.ReceivableSum += l.Receivable
		b.BaseSum += l.Base
		b.DetailFee += l.Fee
		g, ok := groups[l.BrandID][l.Ratio]
		if !ok {
			g = &RatioGroup{Ratio: l.Ratio}
			groups[l.BrandID][l.Ratio] = g
		}
		g.OrderCount++
		g.BaseSum += l.Base
		g.Fee += l.Fee
		if !contains(g.PricingVersionIDs, l.PricingVersionID) {
			g.PricingVersionIDs = append(g.PricingVersionIDs, l.PricingVersionID)
		}
	}
	for _, a := range adjs {
		t.AdjustmentFee += a.FeeDelta
		get(a.BrandID).AdjustmentFee += a.FeeDelta
	}
	t.PayableFee = t.DetailFee + t.AdjustmentFee
	ids := make([]string, 0, len(brands))
	for id := range brands {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]BrandSum, 0, len(ids))
	for _, id := range ids {
		b := brands[id]
		rs := make([]Ratio, 0, len(groups[id]))
		for r := range groups[id] {
			rs = append(rs, r)
		}
		sort.Slice(rs, func(i, j int) bool { return rs[i] < rs[j] })
		b.RatioBreakdown = []RatioGroup{}
		for _, r := range rs {
			g := groups[id][r]
			sort.Strings(g.PricingVersionIDs)
			b.RatioBreakdown = append(b.RatioBreakdown, *g)
		}
		out = append(out, *b)
	}
	return t, out
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// MonthCardCounts 当月（按结束时刻归月）、出单时已收到报告且使用月卡抵扣的订单数，按品牌计。
func MonthCardCounts(orders []Order, month string, at time.Time) map[string]int {
	out := map[string]int{}
	for _, o := range orders {
		if o.HasBill && o.MonthCardDeduct > 0 && o.SettleMonth() == month && !o.ReportReceivedAt.IsZero() && !o.ReportReceivedAt.After(at) {
			out[o.BrandID]++
		}
	}
	return out
}

// SumCounts 单数合计。
func SumCounts(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

// Allocate 按单数把收入分到各品牌（最大余数法到分；余数相同按品牌编号先后）。
func Allocate(income int64, counts map[string]int) map[string]int64 {
	out := map[string]int64{}
	total := int64(SumCounts(counts))
	if total == 0 {
		for k := range counts {
			out[k] = 0
		}
		return out
	}
	type rem struct {
		k string
		r int64
	}
	var rs []rem
	var used int64
	for k, n := range counts {
		q := income * int64(n) / total
		out[k] = q
		used += q
		rs = append(rs, rem{k, income*int64(n) - q*total})
	}
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].r != rs[j].r {
			return rs[i].r > rs[j].r
		}
		return rs[i].k < rs[j].k
	})
	left := income - used
	for i := 0; left > 0 && i < len(rs); i++ {
		out[rs[i].k]++
		left--
	}
	return out
}
