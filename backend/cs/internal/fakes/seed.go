package fakes

import "time"

// 测试数据：取自原型 原型/mockdata 中与客服相关的样例（SCHEMA.md 第 7 节），金额由元换算为分。
// trace: backend/cs/任务列表.md 第 14 项

var cst = time.FixedZone("CST", 8*3600)

func at(s string) *time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", s, cst)
	if err != nil {
		panic(err)
	}
	return &t
}

func i64(v int64) *int64     { return &v }
func f64(v float64) *float64 { return &v }
func boolp(v bool) *bool     { return &v }
func strp(v string) *string  { return &v }

// Seed 放入原型样例。
func (w *World) Seed() {
	w.mu.Lock()
	defer w.mu.Unlock()
	// 订单无法结束、待客服强制结束：OD2026120111070（工单 TK202612010033）
	w.Orders["OD2026120111070"] = &Order{OrderID: "OD2026120111070", UserID: "U100123", OperatorID: "OP02", VehicleID: "VH001028", BrandID: "BR03", PlateNo: "MM1028",
		Status: "骑行中", RequestAt: *at("2026-12-01 07:41:17"), StartAt: at("2026-12-01 07:41:18")}
	// 站外还车收调度费，客服退调度费 ¥3.0：OD2026090110600（工单 TK202609010031），这里取退款前的状态
	w.Orders["OD2026090110600"] = &Order{OrderID: "OD2026090110600", UserID: "U100484", OperatorID: "OP01", VehicleID: "VH000194", BrandID: "BR01", PlateNo: "QH0194",
		Status: "已结束", RequestAt: *at("2026-09-01 06:38:39"), StartAt: at("2026-09-01 06:38:42"), EndAt: at("2026-09-01 06:57:50"),
		DistanceKm: f64(3.3), InStation: boolp(false), InStationBasis: strp("站外")}
	w.Bills["OD2026090110600"] = &Bill{OrderID: "OD2026090110600", UserID: "U100484", BilledMinutes: i64(20), HasFee: true, Receivable: 1130, Payable: 930, ChannelPaid: 930}
	// 超额退款：OD2026113010347（工单 TK202612010032，提议退 ¥21.0，超过额度 ¥20.0）
	w.Orders["OD2026113010347"] = &Order{OrderID: "OD2026113010347", UserID: "U101601", OperatorID: "OP01", VehicleID: "VH000300", BrandID: "BR01", PlateNo: "QH0300",
		Status: "已结束", RequestAt: *at("2026-11-30 17:21:48"), StartAt: at("2026-11-30 17:21:49"), EndAt: at("2026-11-30 18:09:38"),
		DistanceKm: f64(8.0), InStation: boolp(true), InStationBasis: strp("围栏")}
	w.Bills["OD2026113010347"] = &Bill{OrderID: "OD2026113010347", UserID: "U101601", BilledMinutes: i64(48), HasFee: true, Receivable: 2400, Payable: 2400, ChannelPaid: 2400}
	// 退款原路失败待人工转账：OD2026112714351（工单 TK202611290031）
	w.Orders["OD2026112714351"] = &Order{OrderID: "OD2026112714351", UserID: "U100484", OperatorID: "OP01", VehicleID: "VH000592", BrandID: "BR01", PlateNo: "QH0592",
		Status: "已结束", RequestAt: *at("2026-11-27 18:11:01"), StartAt: at("2026-11-27 18:11:02"), EndAt: at("2026-11-27 18:22:13"),
		DistanceKm: f64(2.1), InStation: boolp(true), InStationBasis: strp("运营商车端")}
	w.Bills["OD2026112714351"] = &Bill{OrderID: "OD2026112714351", UserID: "U100484", BilledMinutes: i64(12), HasFee: true, Receivable: 600, Payable: 300, ChannelPaid: 300, RefundMode: "channel"}

	w.Users["U100123"] = &User{UserID: "U100123", Phone: "13800001123", AccountStatus: "正常"}
	w.Users["U100484"] = &User{UserID: "U100484", Phone: "13900004484", AccountStatus: "正常"}
	w.Users["U101601"] = &User{UserID: "U101601", Phone: "13700001601", AccountStatus: "正常"}
	// 老用户押金 ¥99：U100004
	w.Users["U100004"] = &User{UserID: "U100004", Phone: "13811110004", AccountStatus: "正常", Deposit: "ok", DepositAmount: 9900}
	// 冻结·欠费追缴：U100712
	w.Users["U100712"] = &User{UserID: "U100712", Phone: "13622220712", AccountStatus: "冻结"}
	// 有欠费、注销被提示先结清：U100279
	w.Users["U100279"] = &User{UserID: "U100279", Phone: "13533330279", AccountStatus: "正常",
		CancelBlockers: []map[string]any{{"code": "HAS_ARREARS", "message": "有欠费 ¥14.5，结清后再注销", "amount": 1450}}}
	// 已注销：U100598
	w.Users["U100598"] = &User{UserID: "U100598", Phone: "13444440598", AccountStatus: "已注销"}
	// 骑行中、退押被阻：U100123 的押金
	w.Users["U100123"].Deposit = "blocked"
}
