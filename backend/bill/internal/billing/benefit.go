package billing

import (
	"regexp"
	"strings"
	"time"
)

// 月卡与优惠券的时间算术（R-02）。

// CouponExpireAt 到期时刻 = 发放当天（本地日期）起第 validDays 天的 23:59:59
func CouponExpireAt(issuedAt time.Time, validDays int, loc *time.Location) time.Time {
	d := issuedAt.In(loc)
	if validDays < 1 {
		validDays = 1
	}
	day := time.Date(d.Year(), d.Month(), d.Day(), 23, 59, 59, 0, loc)
	return day.AddDate(0, 0, validDays-1)
}

// ClaimWindow 领取窗口
type ClaimWindow struct {
	Open  bool
	Why   string    // 不开放的原因
	Start time.Time // 本期起点（最近一个周五 17:00）
}

// ClaimWindowAt "用户领取"类券每周五 17:00 至周日开放；模板的发放方式写明"领取"才可领
func ClaimWindowAt(tpl CouponTemplate, now time.Time, loc *time.Location) ClaimWindow {
	if !strings.Contains(tpl.Issue, "领取") {
		return ClaimWindow{Why: "该券由系统自动发放"}
	}
	d := now.In(loc)
	w := d.Weekday()
	open := (w == time.Friday && d.Hour() >= 17) || w == time.Saturday || w == time.Sunday
	back := (int(w) + 2) % 7 // 距最近一个周五的天数
	fri := time.Date(d.Year(), d.Month(), d.Day(), 17, 0, 0, 0, loc).AddDate(0, 0, -back)
	cw := ClaimWindow{Open: open, Start: fri}
	if !open {
		cw.Why = "每周五 17:00 开放领取"
	}
	return cw
}

// CareTemplate 开锁失败关怀券：发放方式写明"开锁失败"的模板
func CareTemplate(ts []CouponTemplate) *CouponTemplate {
	for i := range ts {
		if strings.Contains(ts[i].Issue, "开锁失败") {
			return &ts[i]
		}
	}
	return nil
}

// MonthCardSpan 新月卡的起止：有未到期的卡时从最晚结束时刻顺延，否则立即生效
func MonthCardSpan(now time.Time, existingEnds []time.Time, days int) (time.Time, time.Time) {
	start := now
	for _, e := range existingEnds {
		if e.After(start) {
			start = e
		}
	}
	return start, start.AddDate(0, 0, days)
}

// MonthCardStatus 月卡在 now 的状态
func MonthCardStatus(start, end, now time.Time) string {
	switch {
	case now.Before(start):
		return "未生效"
	case now.Before(end):
		return "生效中"
	default:
		return "已过期"
	}
}

// CouponStatus 券在 now 的状态
func CouponStatus(used bool, expireAt, now time.Time) string {
	switch {
	case used:
		return "已使用"
	case now.After(expireAt):
		return "已过期"
	default:
		return "未使用"
	}
}

// 券模板的发放类别（contracts/openapi/bill.yaml CouponTemplate.kind）
const (
	KindClaim     = "领取券"
	KindRegister  = "注册券"
	KindChannel   = "渠道券"
	KindCare      = "关怀券"
	KindSystem    = "系统券"
	KindScheduled = "定时券"
	KindOther     = "其他"
)

var scheduleRe = regexp.MustCompile(`(\d{4})-(\d{2})-(\d{2})(?:[ T](\d{2}):(\d{2}))?`)

// ScheduledAt 定时券的发放时刻：发放方式里写的日期时间，只写日期按当天 00:00；本市时区
func ScheduledAt(tpl CouponTemplate, loc *time.Location) (time.Time, bool) {
	m := scheduleRe.FindStringSubmatch(tpl.Issue)
	if m == nil {
		return time.Time{}, false
	}
	layout, text := "2006-01-02", m[1]+"-"+m[2]+"-"+m[3]
	if m[4] != "" {
		layout, text = "2006-01-02 15:04", text+" "+m[4]+":"+m[5]
	}
	t, err := time.ParseInLocation(layout, text, loc)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// ChannelOf 渠道券限定的付款渠道（发放对象含"微信"或"支付宝"）；不限渠道返回空
func ChannelOf(tpl CouponTemplate) string {
	switch {
	case strings.Contains(tpl.Audience, "微信"):
		return "微信"
	case strings.Contains(tpl.Audience, "支付宝"):
		return "支付宝"
	}
	return ""
}

// KindOf 按发放方式文字判定类别，先到先得
func KindOf(tpl CouponTemplate, loc *time.Location) string {
	switch {
	case strings.Contains(tpl.Issue, "开锁失败"):
		return KindCare
	case strings.Contains(tpl.Issue, "领取"):
		return KindClaim
	case strings.Contains(tpl.Issue, "系统一次性发放"):
		return KindSystem
	}
	if _, ok := ScheduledAt(tpl, loc); ok {
		return KindScheduled
	}
	if strings.Contains(tpl.Issue, "注册") {
		if ChannelOf(tpl) != "" {
			return KindChannel
		}
		return KindRegister
	}
	return KindOther
}
