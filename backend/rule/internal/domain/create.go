package domain

import (
	"fmt"
	"regexp"
	"sort"
	"time"
)

// 取值范围，与契约 rule.yaml 的 PricingVersionCreate 一致。
var (
	rStartFee     = intRange{0, 2000, 10, "分"}
	rStartMinutes = intRange{0, 60, 0, "分钟"}
	rPerMinute    = intRange{0, 500, 10, "分"}
	rDispatchFee  = intRange{0, 10000, 10, "分"}
	rCardPrice    = intRange{0, 50000, 0, "分"}
	rCardDays     = intRange{1, 365, 0, "天"}
	rCardCap      = intRange{0, 5000, 0, "分"}
	rCouponAmount = intRange{0, 5000, 0, "分"}
	rCouponDays   = intRange{1, 90, 0, "天"}
	rPerUser      = intRange{1, -1, 0, "张"}
	rDeposit      = intRange{0, 100000, 0, "分"}
	rThreshold    = intRange{350, 950, 0, "分"}
	rPerUserOpen  = intRange{1, 5, 0, "单"}
	rMaxMinutes   = intRange{1, 1440, 0, "分钟"}
	rMaxAmount    = intRange{0, 100000, 0, "分"}
	rHourly       = intRange{1, 100000, 0, "单"}
	rAlertMinutes = intRange{1, 1440, 0, "分钟"}
)

var brandIDPattern = regexp.MustCompile(`^BR\d{2}$`)

var serviceFeeBases = map[string]bool{"订单应收": true, "订单实收": true}

// effectiveSkew 客户端与服务器的时钟偏差容忍：早于现在不超过这么多的生效时间按"立即"处理。
const effectiveSkew = time.Minute

// CreateInput 校验通过的新增请求：只含要改的项。
type CreateInput struct {
	Name        string
	EffectiveAt *string
	Note        *string

	rideStartFee, rideStartMinutes, ridePerMinute *int64
	dispatchFee                                   *int64
	cardPrice, cardDays, cardCap                  *int64
	cardAllocation                                *string
	coupons                                       []CouponTemplate
	couponsSet                                    bool
	depositAmount                                 *int64
	threshold                                     *int64
	basis                                         *string
	ratios                                        map[string]float64
	perUserOpen, hourly, alertMinutes             *int64
	normalMinutes, normalAmount                   *int64
	tightMinutes, tightAmount                     *int64
}

// ParseCreate 校验新增版本的请求体（不含事由，事由另由 CheckReason 校验），逐项返回错误。
func ParseCreate(m map[string]any) (*CreateInput, []FieldError) {
	c := &checker{}
	in := &CreateInput{}
	if s := c.str(m, "name", "name", "版本名称", true, nil); s != nil {
		in.Name = *s
	}
	if v, ok := m["effectiveAt"]; ok && v != nil {
		if s, ok := v.(string); ok {
			in.EffectiveAt = &s
		} else {
			c.add(nil, "effectiveAt", "生效时间格式不正确")
		}
	}
	if s := c.str(m, "note", "note", "版本说明", false, nil); s != nil && *s != "" {
		in.Note = s
	}

	if o, ok := c.obj(m, "ride", "ride", "骑行单价"); ok {
		in.rideStartFee = c.int(o, "startFee", "ride.startFee", "起步价", rStartFee, nil)
		in.rideStartMinutes = c.int(o, "startMinutes", "ride.startMinutes", "起步含分钟", rStartMinutes, nil)
		in.ridePerMinute = c.int(o, "perMinute", "ride.perMinute", "每分钟价", rPerMinute, nil)
	}
	in.dispatchFee = c.int(m, "dispatchFee", "dispatchFee", "调度费", rDispatchFee, nil)
	if o, ok := c.obj(m, "monthCard", "monthCard", "月卡规则"); ok {
		in.cardPrice = c.int(o, "price", "monthCard.price", "月卡价格", rCardPrice, nil)
		in.cardDays = c.int(o, "days", "monthCard.days", "月卡天数", rCardDays, nil)
		in.cardCap = c.int(o, "perOrderCap", "monthCard.perOrderCap", "月卡每单抵扣上限", rCardCap, nil)
		if _, has := o["allocation"]; has {
			in.cardAllocation = c.str(o, "allocation", "monthCard.allocation", "月卡收入分配规则", true, nil)
		}
	}
	if v, ok := m["coupons"]; ok && v != nil {
		in.coupons, in.couponsSet = parseCoupons(c, v), true
	}
	if o, ok := c.obj(m, "deposit", "deposit", "押金"); ok {
		in.depositAmount = c.int(o, "amount", "deposit.amount", "押金金额", rDeposit, nil)
	}
	if o, ok := c.obj(m, "creditFree", "creditFree", "信用免押"); ok {
		in.threshold = c.int(o, "threshold", "creditFree.threshold", "免押门槛", rThreshold, nil)
	}
	if o, ok := c.obj(m, "serviceFee", "serviceFee", "服务费"); ok {
		if s := c.str(o, "basis", "serviceFee.basis", "服务费基数口径", false, nil); s != nil {
			if serviceFeeBases[*s] {
				in.basis = s
			} else {
				c.add(nil, "serviceFee.basis", "服务费基数口径只能是「订单应收」或「订单实收」")
			}
		}
		if r, ok := c.obj(o, "ratios", "serviceFee.ratios", "服务费比例"); ok {
			in.ratios = map[string]float64{}
			keys := make([]string, 0, len(r))
			for k := range r {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, b := range keys {
				path := "serviceFee.ratios." + b
				if !brandIDPattern.MatchString(b) {
					c.add(nil, path, fmt.Sprintf("品牌 %s 不存在（品牌编号形如 BR01）", b))
					continue
				}
				if f := c.decimal(r, b, path, b+" 服务费比例", 0, 1, 4); f != nil {
					in.ratios[b] = *f
				}
			}
		}
	}
	if o, ok := c.obj(m, "riskLimits", "riskLimits", "风控限额"); ok {
		in.perUserOpen = c.int(o, "perUserOpenDegradedOrders", "riskLimits.perUserOpenDegradedOrders", "每用户未结降级单数", rPerUserOpen, nil)
		in.hourly = c.int(o, "hourlyDegradedOrders", "riskLimits.hourlyDegradedOrders", "每小时降级单总量", rHourly, nil)
		in.alertMinutes = c.int(o, "degradeDurationAlertMinutes", "riskLimits.degradeDurationAlertMinutes", "降级持续告警分钟", rAlertMinutes, nil)
		if n, ok := c.obj(o, "normal", "riskLimits.normal", "正常档"); ok {
			in.normalMinutes = c.int(n, "maxMinutes", "riskLimits.normal.maxMinutes", "正常档单笔最长分钟", rMaxMinutes, nil)
			in.normalAmount = c.int(n, "maxAmount", "riskLimits.normal.maxAmount", "正常档单笔最高金额", rMaxAmount, nil)
		}
		if n, ok := c.obj(o, "tight", "riskLimits.tight", "紧档"); ok {
			in.tightMinutes = c.int(n, "maxMinutes", "riskLimits.tight.maxMinutes", "紧档单笔最长分钟", rMaxMinutes, nil)
			in.tightAmount = c.int(n, "maxAmount", "riskLimits.tight.maxAmount", "紧档单笔最高金额", rMaxAmount, nil)
		}
	}
	if len(c.errs) > 0 {
		return nil, c.errs
	}
	return in, nil
}

func parseCoupons(c *checker, v any) []CouponTemplate {
	arr, ok := v.([]any)
	if !ok {
		c.add(nil, "coupons", "券模板须为数组")
		return nil
	}
	out := make([]CouponTemplate, 0, len(arr))
	seen := map[string]bool{}
	for i, x := range arr {
		row := i + 1
		rp := &row
		p := fmt.Sprintf("coupons[%d]", i)
		o, ok := x.(map[string]any)
		if !ok {
			c.add(rp, p, "券模板格式不正确")
			continue
		}
		name := c.str(o, "name", p+".name", "券名", true, rp)
		label := fmt.Sprintf("第 %d 张券", row)
		if name != nil {
			label = "券「" + *name + "」"
		}
		ct := CouponTemplate{}
		if id := c.str(o, "templateId", p+".templateId", label+"的模板编号", true, rp); id != nil {
			if seen[*id] {
				c.add(rp, p+".templateId", fmt.Sprintf("券模板编号 %s 重复", *id))
			}
			seen[*id] = true
			ct.TemplateID = *id
		}
		if name != nil {
			ct.Name = *name
		}
		if a := c.int(o, "amount", p+".amount", label+"面额", rCouponAmount, rp); a != nil {
			ct.Amount = *a
		} else if _, has := o["amount"]; !has {
			c.add(rp, p+".amount", "请填写"+label+"面额")
		}
		if d := c.int(o, "validDays", p+".validDays", label+"有效天数", rCouponDays, rp); d != nil {
			ct.ValidDays = *d
		} else if _, has := o["validDays"]; !has {
			c.add(rp, p+".validDays", "请填写"+label+"有效天数")
		}
		ct.Audience = c.str(o, "audience", p+".audience", label+"发放对象", false, rp)
		ct.Issue = c.str(o, "issue", p+".issue", label+"发放方式", false, rp)
		ct.PerUser = c.int(o, "perUser", p+".perUser", label+"每人限领张数", rPerUser, rp)
		out = append(out, ct)
	}
	return out
}

// RatioBrands 本次请求里给出了服务费比例的品牌编号（升序）。
func (in *CreateInput) RatioBrands() []string {
	out := make([]string, 0, len(in.ratios))
	for b := range in.ratios {
		out = append(out, b)
	}
	sort.Strings(out)
	return out
}

// Apply 以前一版本 base 为底、套上改动项，生成完整的新版本内容；base 为 nil（首个版本）时各必填项须在请求里给出。
// base 不会被改动。
func (in *CreateInput) Apply(base *Content) (Content, []FieldError) {
	var out Content
	if base != nil {
		out = cloneContent(*base)
	} else {
		c := &checker{}
		need := func(p *int64, field, label string) {
			if p == nil {
				c.add(nil, field, "首个版本须填写"+label)
			}
		}
		need(in.rideStartFee, "ride.startFee", "起步价")
		need(in.rideStartMinutes, "ride.startMinutes", "起步含分钟")
		need(in.ridePerMinute, "ride.perMinute", "每分钟价")
		need(in.dispatchFee, "dispatchFee", "调度费")
		need(in.cardPrice, "monthCard.price", "月卡价格")
		need(in.cardDays, "monthCard.days", "月卡天数")
		need(in.cardCap, "monthCard.perOrderCap", "月卡每单抵扣上限")
		if in.cardAllocation == nil {
			c.add(nil, "monthCard.allocation", "首个版本须填写月卡收入分配规则")
		}
		need(in.depositAmount, "deposit.amount", "押金金额")
		need(in.threshold, "creditFree.threshold", "免押门槛")
		if in.basis == nil {
			c.add(nil, "serviceFee.basis", "首个版本须填写服务费基数口径")
		}
		need(in.perUserOpen, "riskLimits.perUserOpenDegradedOrders", "每用户未结降级单数")
		need(in.normalMinutes, "riskLimits.normal.maxMinutes", "正常档单笔最长分钟")
		need(in.normalAmount, "riskLimits.normal.maxAmount", "正常档单笔最高金额")
		need(in.tightMinutes, "riskLimits.tight.maxMinutes", "紧档单笔最长分钟")
		need(in.tightAmount, "riskLimits.tight.maxAmount", "紧档单笔最高金额")
		need(in.hourly, "riskLimits.hourlyDegradedOrders", "每小时降级单总量")
		need(in.alertMinutes, "riskLimits.degradeDurationAlertMinutes", "降级持续告警分钟")
		if len(c.errs) > 0 {
			return Content{}, c.errs
		}
		out.Coupons = []CouponTemplate{}
		out.ServiceFee.Ratios = map[string]float64{}
	}
	set := func(dst *int64, src *int64) {
		if src != nil {
			*dst = *src
		}
	}
	set(&out.Ride.StartFee, in.rideStartFee)
	set(&out.Ride.StartMinutes, in.rideStartMinutes)
	set(&out.Ride.PerMinute, in.ridePerMinute)
	set(&out.DispatchFee, in.dispatchFee)
	set(&out.MonthCard.Price, in.cardPrice)
	set(&out.MonthCard.Days, in.cardDays)
	set(&out.MonthCard.PerOrderCap, in.cardCap)
	if in.cardAllocation != nil {
		out.MonthCard.Allocation = *in.cardAllocation
	}
	if in.couponsSet {
		out.Coupons = append([]CouponTemplate{}, in.coupons...)
	}
	set(&out.Deposit.Amount, in.depositAmount)
	set(&out.CreditFree.Threshold, in.threshold)
	if in.basis != nil {
		out.ServiceFee.Basis = *in.basis
	}
	for b, r := range in.ratios {
		out.ServiceFee.Ratios[b] = r
	}
	set(&out.RiskLimits.PerUserOpenDegradedOrders, in.perUserOpen)
	set(&out.RiskLimits.HourlyDegradedOrders, in.hourly)
	set(&out.RiskLimits.DegradeDurationAlertMinutes, in.alertMinutes)
	set(&out.RiskLimits.Normal.MaxMinutes, in.normalMinutes)
	set(&out.RiskLimits.Normal.MaxAmount, in.normalAmount)
	set(&out.RiskLimits.Tight.MaxMinutes, in.tightMinutes)
	set(&out.RiskLimits.Tight.MaxAmount, in.tightAmount)
	return out, nil
}

// cloneContent 深拷贝，保证新版本与前一版本不共享切片与映射。
func cloneContent(c Content) Content {
	out := c
	out.Coupons = append([]CouponTemplate{}, c.Coupons...)
	if c.CreditFree.Providers != nil {
		out.CreditFree.Providers = append([]string{}, c.CreditFree.Providers...)
	}
	out.ServiceFee.Ratios = make(map[string]float64, len(c.ServiceFee.Ratios))
	for k, v := range c.ServiceFee.Ratios {
		out.ServiceFee.Ratios[k] = v
	}
	return out
}

// ResolveEffectiveAt 解析生效时间：缺省为当前时刻；不得早于当前时刻（容忍一分钟时钟偏差，按立即处理）。结果精确到秒。
func ResolveEffectiveAt(s *string, now time.Time) (time.Time, *FieldError) {
	now = now.Truncate(time.Second)
	if s == nil || *s == "" {
		return now, nil
	}
	t, err := time.Parse(time.RFC3339, *s)
	if err != nil {
		return time.Time{}, &FieldError{Field: "effectiveAt", Message: "生效时间格式不正确，应为带时区的时刻，如 2027-01-01T00:00:00+08:00"}
	}
	t = t.Truncate(time.Second)
	if t.Before(now) {
		if now.Sub(t) <= effectiveSkew {
			return now, nil
		}
		return time.Time{}, &FieldError{Field: "effectiveAt", Message: "生效时间不能早于现在，改动只影响生效之后开锁的订单"}
	}
	return t, nil
}
