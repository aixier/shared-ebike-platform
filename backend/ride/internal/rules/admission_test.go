package rules

import (
	"testing"
	"time"
)

// 样例取自原型 mockdata（SCHEMA 第 7 节、API.md 3.1）：PR0003 现行版本。
func pr0003() *Version {
	v := &Version{VersionID: "PR0003"}
	v.Ride.StartFee, v.Ride.StartMinutes, v.Ride.PerMinute = 150, 3, 50
	v.DispatchFee = 500
	v.Deposit.Amount = 19900
	v.RiskLimits = RiskLimits{PerUserOpenDegradedOrders: 1, Normal: Limits{120, 6000}, Tight: Limits{30, 1500}, HourlyDegradedOrders: 200}
	return v
}

func freeUser(id string) *User {
	return &User{UserID: id, RealName: RNVerified, DepositMode: DepFree}
}

func TestVehicleBlockOrder(t *testing.T) {
	now := time.Date(2026, 12, 1, 12, 0, 0, 0, cst)
	en := now.Add(7 * 24 * time.Hour)
	on := &Brand{BrandID: "BR01", DisplayName: "【青禾】电单车", AccessStatus: AccessOn}
	paused := &Brand{BrandID: "BR02", DisplayName: "【木棉】电单车", AccessStatus: AccessPaused}
	cases := []struct {
		name string
		v    Vehicle
		b    *Brand
		want string
	}{
		{"可租车 VH000004", Vehicle{VehicleID: "VH000004", CodeStatus: CodeEnabled}, on, ""},
		{"码未启用优先于暂停", Vehicle{CodeStatus: CodePending, CodeEnableAt: &en}, paused, "CODE_NOT_ENABLED"},
		{"暂停可租 VH000481", Vehicle{CodeStatus: CodeEnabled}, paused, "BRAND_PAUSED"},
		{"暂停优先于码失效", Vehicle{CodeStatus: CodeInvalid}, paused, "BRAND_PAUSED"},
		{"码已失效 VH001061", Vehicle{CodeStatus: CodeInvalid}, &Brand{AccessStatus: "已退出"}, "CODE_INVALID"},
		{"已移除视为失效", Vehicle{CodeStatus: CodeEnabled, Removed: true}, on, "CODE_INVALID"},
		{"品牌未开放（联调中）", Vehicle{CodeStatus: CodeEnabled}, &Brand{AccessStatus: "联调中"}, "BRAND_NOT_AVAILABLE"},
		{"没有品牌投影", Vehicle{CodeStatus: CodeEnabled}, nil, "BRAND_NOT_AVAILABLE"},
	}
	for _, c := range cases {
		f := VehicleBlock(c.v, c.b)
		got := ""
		if f != nil {
			got = f.Code
		}
		if got != c.want {
			t.Errorf("%s：期望 %q 得 %q", c.name, c.want, got)
		}
	}
	f := VehicleBlock(Vehicle{CodeStatus: CodePending, CodeEnableAt: &en}, on)
	if f.Message != "这辆车的码还未启用，12月8日 12:00 起可骑" || f.Extra["codeEnableAt"] == nil {
		t.Errorf("码未启用文案或附加字段不对：%+v", f)
	}
	if f := VehicleBlock(Vehicle{CodeStatus: CodeEnabled}, paused); f.Message != "【木棉】电单车暂停可租，附近其他品牌的车可以骑" {
		t.Errorf("暂停可租文案：%s", f.Message)
	}
}

func TestUserCheckNormal(t *testing.T) {
	v := pr0003()
	cases := []struct {
		name   string
		u      *User
		riding string
		want   string
	}{
		{"未登录", nil, "", "NOT_LOGGED_IN"},
		{"已注销 U100598", &User{Cancelled: true, Frozen: true}, "", "ACCOUNT_CANCELLED"},
		{"冻结 U100712", &User{Frozen: true, FreezeReason: "欠费追缴", RealName: RNVerified}, "", "ACCOUNT_FROZEN"},
		{"冻结优先于骑行中", &User{Frozen: true}, "OD1", "ACCOUNT_FROZEN"},
		{"已有骑行中", freeUser("U1"), "OD2026120111001", "HAS_RIDING"},
		{"骑行中优先于未实名", &User{RealName: RNNone}, "OD1", "HAS_RIDING"},
		{"未实名 U100127", &User{RealName: RNNone, DepositMode: DepNone}, "", "NEED_ONBOARDING"},
		{"未通过", &User{RealName: RNFailed}, "", "NEED_ONBOARDING"},
		{"常态下待核验 U100026", &User{RealName: RNPending, DepositMode: DepFree}, "", "NEED_ONBOARDING"},
		{"欠费 U100036", &User{RealName: RNVerified, DepositMode: DepFree, ArrearsTotal: 1100}, "", "HAS_ARREARS"},
		{"欠费优先于待支付", &User{RealName: RNVerified, DepositMode: DepPaid, DepositStatus: DepStPaid, ArrearsTotal: 1, Unpaid: map[string]UnpaidItem{"OD1": {Amount: 300}}}, "", "HAS_ARREARS"},
		{"待支付 OD2026113010514", &User{RealName: RNVerified, DepositMode: DepPaid, DepositStatus: DepStPaid, Unpaid: map[string]UnpaidItem{"OD2026113010514": {Amount: 300}}}, "", "UNPAID_ORDER"},
		{"未开通 U100087", &User{RealName: RNVerified, DepositMode: DepNone}, "", "DEPOSIT_REQUIRED"},
		{"押金不足 U100054", &User{RealName: RNVerified, DepositMode: DepPaid, DepositStatus: DepStShort, DepositPaid: 9900, DepositBalance: 9600}, "", "DEPOSIT_INSUFFICIENT"},
		{"退押中 U101097", &User{RealName: RNVerified, DepositMode: DepPaid, DepositStatus: DepStRefunding}, "", "DEPOSIT_REFUNDING"},
		{"已退押", &User{RealName: RNVerified, DepositMode: DepPaid, DepositStatus: DepStRefunded}, "", "DEPOSIT_REQUIRED"},
		{"免押老用户 U100341", freeUser("U100341"), "", ""},
		{"押金老用户 U100004", &User{RealName: RNVerified, DepositMode: DepPaid, DepositStatus: DepStPaid, DepositPaid: 9900, DepositBalance: 9900}, "", ""},
	}
	for _, c := range cases {
		adm, f := UserCheck(UserCheckInput{User: c.u, LoggedIn: c.u != nil, RidingOrderID: c.riding, Version: v})
		got := ""
		if f != nil {
			got = f.Code
		}
		if got != c.want {
			t.Errorf("%s：期望 %q 得 %q", c.name, c.want, got)
		}
		if f == nil && adm != nil {
			t.Errorf("%s：常态不应有降级准入", c.name)
		}
	}
	_, f := UserCheck(UserCheckInput{User: &User{RealName: RNVerified, DepositMode: DepFree, ArrearsTotal: 1100}, LoggedIn: true, Version: v})
	if f.Message != "你有 ¥11.0 未付的骑行费用，补付后即可开锁" || f.Extra["amount"] != int64(1100) {
		t.Errorf("欠费文案：%+v", f)
	}
	_, f = UserCheck(UserCheckInput{User: &User{RealName: RNVerified, DepositMode: DepPaid, DepositStatus: DepStShort, DepositPaid: 9900, DepositBalance: 9600}, LoggedIn: true, Version: v})
	if f.Message != "押金余额 ¥96.0，补缴 ¥3.0 或开通免押后即可骑行" || f.Extra["topupAmount"] != int64(300) {
		t.Errorf("押金不足文案：%+v", f)
	}
	_, f = UserCheck(UserCheckInput{User: &User{RealName: RNVerified, DepositMode: DepNone}, LoggedIn: true, Version: v})
	if f.Message != "开通信用免押或缴押金 ¥199.0 后即可骑行" || f.Extra["depositAmount"] != int64(19900) {
		t.Errorf("未开通文案：%+v", f)
	}
	_, f = UserCheck(UserCheckInput{User: &User{Frozen: true, FreezeReason: "欠费追缴", UnfreezeHow: "补付欠费后自动解冻"}, LoggedIn: true, Version: v})
	if f.Message != "账户已冻结：欠费追缴" || f.Extra["unfreezeHow"] != "补付欠费后自动解冻" {
		t.Errorf("冻结文案：%+v", f)
	}
	// 登录了但快照里还没有这个用户：按新用户走首次引导
	_, f = UserCheck(UserCheckInput{User: nil, LoggedIn: true, Version: v})
	if f == nil || f.Code != "NEED_ONBOARDING" {
		t.Errorf("快照里没有的已登录用户应走首次引导：%+v", f)
	}
}

func TestUserCheckDegraded(t *testing.T) {
	v := pr0003()
	deg := func(d DegradeInput) DegradeInput { d.Active = true; return d }
	cases := []struct {
		name string
		u    *User
		d    DegradeInput
		want string
		cat  string
	}{
		{"老用户正常额度", freeUser("U100341"), deg(DegradeInput{}), "", AdmOld},
		{"押金状态未知的老用户", freeUser("U100341"), deg(DegradeInput{DepositUnknown: true}), "", AdmDepUnknown},
		{"押金未知时未开通也可按紧档开", &User{RealName: RNVerified, DepositMode: DepNone}, deg(DegradeInput{DepositUnknown: true}), "", AdmDepUnknown},
		{"先骑后验新用户", &User{RealName: RNPending, DepositMode: DepNone}, deg(DegradeInput{}), "", AdmNewDefer},
		{"已知欠费", &User{RealName: RNVerified, DepositMode: DepFree, ArrearsTotal: 1100}, deg(DegradeInput{}), "DEGRADE_REJECT_ARREARS", ""},
		{"退押中", &User{RealName: RNVerified, DepositMode: DepPaid, DepositStatus: DepStRefunding}, deg(DegradeInput{}), "DEGRADE_REJECT_REFUNDING", ""},
		{"待支付仍拦", &User{RealName: RNVerified, DepositMode: DepPaid, DepositStatus: DepStPaid, Unpaid: map[string]UnpaidItem{"OD1": {Amount: 1}}}, deg(DegradeInput{}), "UNPAID_ORDER", ""},
		{"值班人停止", freeUser("U1"), deg(DegradeInput{Stopped: true}), "DEGRADE_STOPPED", ""},
		{"收紧不影响老用户", freeUser("U1"), deg(DegradeInput{Tightened: true}), "", AdmOld},
		{"收紧拦押金未知老用户", freeUser("U1"), deg(DegradeInput{Tightened: true, DepositUnknown: true}), "DEGRADE_PAUSED_TIGHT", ""},
		{"收紧拦新用户", &User{RealName: RNPending}, deg(DegradeInput{Tightened: true}), "DEGRADE_NEW_USER_UNAVAILABLE", ""},
		{"每小时总量到上限拦紧档", &User{RealName: RNPending}, deg(DegradeInput{HourCount: 200}), "DEGRADE_NEW_USER_UNAVAILABLE", ""},
		{"每小时总量不拦老用户", freeUser("U1"), deg(DegradeInput{HourCount: 500}), "", AdmOld},
		{"每用户未结降级单", freeUser("U1"), deg(DegradeInput{UserOpenDegraded: 1}), "DEGRADE_USER_LIMIT", ""},
		{"降级时老用户押金不足仍按常态拦", &User{RealName: RNVerified, DepositMode: DepPaid, DepositStatus: DepStShort, DepositPaid: 100, DepositBalance: 50}, deg(DegradeInput{}), "DEPOSIT_INSUFFICIENT", ""},
	}
	for _, c := range cases {
		adm, f := UserCheck(UserCheckInput{User: c.u, LoggedIn: true, Version: v, Degrade: c.d})
		got := ""
		if f != nil {
			got = f.Code
		}
		if got != c.want {
			t.Errorf("%s：期望 %q 得 %q", c.name, c.want, got)
			continue
		}
		if c.want == "" {
			if adm == nil || adm.Category != c.cat {
				t.Errorf("%s：准入类别期望 %s 得 %+v", c.name, c.cat, adm)
			}
		}
	}
	adm, _ := UserCheck(UserCheckInput{User: freeUser("U1"), LoggedIn: true, Version: v, Degrade: DegradeInput{Active: true}})
	if adm.Line != "费用将稍后收取" || adm.Notice != nil || adm.LimitMinutes != 120 || adm.LimitAmount != 6000 {
		t.Errorf("老用户只多一行，且用正常额度：%+v", adm)
	}
	adm, _ = UserCheck(UserCheckInput{User: &User{RealName: RNPending}, LoggedIn: true, Version: v, Degrade: DegradeInput{Active: true}})
	if adm.Notice == nil || *adm.Notice != "实名核验稍后完成，你可以先骑，本次最长 30 分钟或 ¥15.0。核验未通过将冻结账户并追缴本次费用。" || adm.LimitMinutes != 30 || adm.LimitAmount != 1500 {
		t.Errorf("先骑后验须有说明并用紧档：%+v", adm)
	}
}

func TestEstimateRideFee(t *testing.T) {
	v := pr0003()
	if got := EstimateRideFee(v, 60); got != 150 {
		t.Fatalf("1 分钟起步价 150，得 %d", got)
	}
	if got := EstimateRideFee(v, 5*60+1); got != 150+3*50 {
		t.Fatalf("6 分钟应 300，得 %d", got)
	}
}
