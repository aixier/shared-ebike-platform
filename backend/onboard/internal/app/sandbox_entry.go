package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"bike/onboard/internal/sign"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// verifyOperator 按接入规范校验运营商签名，返回该应用标识对应的联调凭证。
// 顺序：应用标识已登记且未停用 → 时间戳 → 签名 → 随机数未出现过（签名通过才记随机数）。
func (a *App) verifyOperator(r *http.Request, body []byte) (*credRow, *apiError) {
	appID := r.Header.Get("X-App-Id")
	ts := r.Header.Get("X-Timestamp")
	nonce := r.Header.Get("X-Nonce")
	sig := r.Header.Get("X-Signature")
	if appID == "" || ts == "" || nonce == "" || sig == "" {
		return nil, errf(401, "SIGNATURE_INVALID", "缺少签名请求头 X-App-Id、X-Timestamp、X-Nonce、X-Signature")
	}
	ctx := r.Context()
	c, err := scanCred(a.DB.QueryRow(ctx, `SELECT `+credCols+` FROM credentials WHERE app_id=$1`, appID))
	if err != nil {
		return nil, errf(401, "SIGNATURE_INVALID", "应用标识未登记")
	}
	if c.Status != "有效" {
		return nil, errf(403, "CREDENTIAL_DISABLED", "联调凭证已停用，请联系平台公司")
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return nil, errf(401, "TIMESTAMP_EXPIRED", "X-Timestamp 应为 Unix 秒")
	}
	if d := a.now().Sub(time.Unix(sec, 0)); d > a.Cfg.ClockSkew || d < -a.Cfg.ClockSkew {
		return nil, errf(401, "TIMESTAMP_EXPIRED", fmt.Sprintf("时间戳与平台时间相差超过 %d 秒，请校时后用新时间戳重发", int(a.Cfg.ClockSkew.Seconds())))
	}
	if !sign.NonceOK(nonce) {
		return nil, errf(401, "SIGNATURE_INVALID", "X-Nonce 应为 16～64 个字符")
	}
	want := sign.Sign(c.Secret, r.Method, r.URL.Path, appID, ts, nonce, body)
	if !sign.Equal(want, sig) {
		return nil, errf(401, "SIGNATURE_INVALID", "签名不一致，请核对密钥与待签名串")
	}
	tag, err := a.DB.Exec(ctx, `INSERT INTO sandbox_nonces (app_id, nonce, seen_at) VALUES ($1,$2,now()) ON CONFLICT DO NOTHING`, appID, nonce)
	if err != nil {
		return nil, errf(500, "SERVER_BUSY", "平台暂时不可用，请按补报窗口重试")
	}
	if tag.RowsAffected() == 0 {
		return nil, errf(401, "NONCE_REPLAYED", "随机数在时间窗内已出现过，请换随机数重发")
	}
	return c, nil
}

func writeAPIError(w http.ResponseWriter, e *apiError) { writeJSON(w, e.Status, e.body()) }

// RideEvent M-03 报文。
type RideEvent struct {
	EventID    string    `json:"eventId"`
	Type       string    `json:"type"`
	OrderID    string    `json:"orderId"`
	PlateNo    string    `json:"plateNo"`
	OccurredAt time.Time `json:"occurredAt"`
	Unlock     *struct {
		Result     string    `json:"result"`
		FailReason string    `json:"failReason"`
		Location   []float64 `json:"location"`
	} `json:"unlock"`
	Lock *struct {
		Location    []float64 `json:"location"`
		InStation   *bool     `json:"inStation"`
		DurationSec *int      `json:"durationSec"`
		DistanceKm  *float64  `json:"distanceKm"`
	} `json:"lock"`
}

var unlockFailReasons = map[string]bool{"电量不足": true, "车辆故障": true, "车辆被占用": true, "车辆无响应": true}

func (e *RideEvent) validate() string {
	switch {
	case e.EventID == "" || len(e.EventID) > 64:
		return "eventId 必填，不超过 64 个字符"
	case e.Type != "开锁结果" && e.Type != "锁车":
		return "type 只能是 开锁结果 或 锁车"
	case e.OrderID == "":
		return "orderId 必填"
	case e.PlateNo == "":
		return "plateNo 必填"
	case e.OccurredAt.IsZero():
		return "occurredAt 必填，RFC 3339 带时区"
	}
	if e.Type == "开锁结果" {
		if e.Unlock == nil {
			return "type=开锁结果 时 unlock 必填"
		}
		if e.Unlock.Result != "成功" && e.Unlock.Result != "失败" {
			return "unlock.result 只能是 成功 或 失败"
		}
		if e.Unlock.Result == "失败" && !unlockFailReasons[e.Unlock.FailReason] {
			return "unlock.result=失败 时 failReason 必填，取 电量不足、车辆故障、车辆被占用、车辆无响应"
		}
		if e.Unlock.Location != nil && len(e.Unlock.Location) != 2 {
			return "unlock.location 应为 [经度, 纬度]"
		}
	}
	if e.Type == "锁车" {
		if e.Lock == nil {
			return "type=锁车 时 lock 必填"
		}
		if len(e.Lock.Location) != 2 {
			return "lock.location 必填，[经度, 纬度]"
		}
		if e.Lock.InStation == nil {
			return "lock.inStation 必填"
		}
		if e.Lock.DurationSec != nil && *e.Lock.DurationSec < 0 {
			return "lock.durationSec 不能小于 0"
		}
		if e.Lock.DistanceKm != nil && *e.Lock.DistanceKm < 0 {
			return "lock.distanceKm 不能小于 0"
		}
	}
	return ""
}

// trace: sandboxReportRideEvent · R-38 R-39 M-03
func (a *App) hSandboxRideEvent(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeAPIError(w, errf(400, "INVALID", "请求体读取失败"))
		return
	}
	cred, ae := a.verifyOperator(r, body)
	if ae != nil {
		writeAPIError(w, ae)
		return
	}
	if !utf8.Valid(body) {
		writeAPIError(w, errNotUTF8)
		return
	}
	var ev RideEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		writeAPIError(w, errf(400, "INVALID", "报文不是合法的 JSON 或字段类型不对："+err.Error()))
		return
	}
	if msg := ev.validate(); msg != "" {
		writeAPIError(w, errf(400, "INVALID", msg))
		return
	}
	ack, ae := a.receiveRideEvent(r.Context(), cred, &ev, body)
	if ae != nil {
		writeAPIError(w, ae)
		return
	}
	writeJSON(w, 200, ack)
}

type rideEventAck struct {
	Received   bool      `json:"received"`
	EventID    string    `json:"eventId"`
	ReceivedAt time.Time `json:"receivedAt"`
	Duplicate  bool      `json:"duplicate"`
}

func (a *App) receiveRideEvent(ctx context.Context, cred *credRow, ev *RideEvent, body []byte) (*rideEventAck, *apiError) {
	now := a.now()
	var ack *rideEventAck
	var out *apiError
	err := a.inTx(ctx, func(tx pgx.Tx) error {
		var brand, plate string
		var rejectFirst bool
		err := tx.QueryRow(ctx, `SELECT brand_id, plate_no, reject_first_lock FROM sandbox_orders WHERE order_id=$1 FOR UPDATE`, ev.OrderID).Scan(&brand, &plate, &rejectFirst)
		if isNoRows(err) {
			out = errf(404, "NOT_FOUND", "联调环境里没有这个订单号，请核对，不要重试")
			return nil
		}
		if err != nil {
			return err
		}
		if brand != cred.BrandID {
			out = errf(403, "SCOPE_DENIED", "该订单不属于这份联调凭证的品牌")
			return nil
		}
		if plate != ev.PlateNo {
			out = errf(400, "INVALID", fmt.Sprintf("车号 %s 与订单的车号 %s 不符", ev.PlateNo, plate))
			return nil
		}
		insert := func(outcome string) error {
			_, err := tx.Exec(ctx, `INSERT INTO sandbox_deliveries (brand_id, order_id, event_id, type, occurred_at, received_at, outcome, body)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, brand, ev.OrderID, ev.EventID, ev.Type, ev.OccurredAt, now, outcome, body)
			return err
		}
		var dup bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sandbox_deliveries WHERE brand_id=$1 AND event_id=$2 AND outcome='已收到')`, brand, ev.EventID).Scan(&dup); err != nil {
			return err
		}
		if dup {
			ack = &rideEventAck{Received: true, EventID: ev.EventID, ReceivedAt: now, Duplicate: true}
			return insert("重复")
		}
		if ev.Type == "锁车" && rejectFirst {
			var rejected bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sandbox_deliveries WHERE order_id=$1 AND type='锁车' AND outcome='拒收')`, ev.OrderID).Scan(&rejected); err != nil {
				return err
			}
			if !rejected {
				// 模拟车辆"断网"：首次锁车报告平台不确认，运营商须按补报规则重发
				out = errf(500, "SERVER_BUSY", "平台暂时不可用（联调环境模拟），请按补报规则重发，eventId 不变")
				return insert("拒收")
			}
		}
		if err := insert("已收到"); err != nil {
			return err
		}
		if ev.Type == "锁车" {
			// 模拟支付：只在联调环境里记结果，不进生产资金
			var op string
			_ = tx.QueryRow(ctx, `SELECT operator_id FROM integrations WHERE brand_id=$1`, brand).Scan(&op)
			mode := "正常"
			_ = tx.QueryRow(ctx, `SELECT payment_mode FROM sandbox_settings WHERE operator_id=$1`, op).Scan(&mode)
			res := "模拟扣款成功"
			if mode == "余额不足" {
				res = "模拟扣款失败（余额不足），订单转欠费"
			}
			if _, err := tx.Exec(ctx, `UPDATE sandbox_orders SET pay_result=$2 WHERE order_id=$1`, ev.OrderID, res); err != nil {
				return err
			}
		}
		ack = &rideEventAck{Received: true, EventID: ev.EventID, ReceivedAt: now, Duplicate: false}
		return nil
	})
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			// 并发下同一 eventId 同时到达时唯一索引冲突：按重复处理
			return &rideEventAck{Received: true, EventID: ev.EventID, ReceivedAt: now, Duplicate: true}, nil
		}
		return nil, errf(500, "SERVER_BUSY", "平台暂时不可用，请按补报窗口重试")
	}
	if out != nil {
		return nil, out
	}
	return ack, nil
}

// VehicleRow M-04 一行。
type VehicleRow struct {
	BrandID     string `json:"brandId"`
	PlateNo     string `json:"plateNo"`
	Model       string `json:"model"`
	PileTag     string `json:"pileTag"`
	PileTagType string `json:"pileTagType"`
}

// rowError 逐行错误；Rule 只在本模块内部用来归类到用例（必填、取值、重复）。
type rowError struct {
	Row     int    `json:"row"`
	Field   string `json:"field"`
	Message string `json:"message"`
	Rule    string `json:"rule"`
}

var pileTagTypes = map[string]bool{"RFID": true, "蓝牙": true, "视觉车牌": true, "其他": true}

// trace: sandboxSubmitVehicleList · R-38 R-39 M-04
func (a *App) hSandboxVehicleList(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		writeAPIError(w, errf(400, "INVALID", "请求体读取失败"))
		return
	}
	cred, ae := a.verifyOperator(r, body)
	if ae != nil {
		writeAPIError(w, ae)
		return
	}
	if !utf8.Valid(body) {
		writeAPIError(w, errNotUTF8)
		return
	}
	var sub struct {
		Rows []VehicleRow `json:"rows"`
	}
	if err := json.Unmarshal(body, &sub); err != nil {
		writeAPIError(w, errf(400, "INVALID", "报文不是合法的 JSON 或字段类型不对："+err.Error()))
		return
	}
	if len(sub.Rows) < 1 || len(sub.Rows) > 5000 {
		writeAPIError(w, errf(400, "INVALID", "rows 应为 1～5000 行"))
		return
	}
	for _, row := range sub.Rows {
		if row.BrandID != "" && row.BrandID != cred.BrandID {
			writeAPIError(w, errf(403, "SCOPE_DENIED", fmt.Sprintf("品牌 %s 不属于这份联调凭证", row.BrandID)))
			return
		}
	}
	ctx := r.Context()
	errs := a.checkVehicleRows(ctx, cred.BrandID, sub.Rows)
	rowsJSON, _ := json.Marshal(sub.Rows)
	errsJSON, _ := json.Marshal(errs)
	if _, err := a.DB.Exec(ctx, `INSERT INTO vehicle_submissions (brand_id, submitted_at, ok, rows, errors) VALUES ($1,$2,$3,$4,$5)`,
		cred.BrandID, a.now(), len(errs) == 0, rowsJSON, errsJSON); err != nil {
		writeAPIError(w, errf(500, "SERVER_BUSY", "平台暂时不可用，请稍后重试"))
		return
	}
	if len(errs) > 0 {
		pub := make([]map[string]any, 0, len(errs))
		for _, e := range errs {
			pub = append(pub, map[string]any{"row": e.Row, "field": e.Field, "message": e.Message})
		}
		writeAPIError(w, errf(400, "VEHICLE_LIST_INVALID", fmt.Sprintf("车辆清单有 %d 处错误，请改好后整批重发", len(errs))).with("errors", pub))
		return
	}
	// 联调环境不发政府码：已在生产清单里的车返回其编号与码，其余返回空
	vehicles := make([]map[string]any, 0, len(sub.Rows))
	for _, row := range sub.Rows {
		var vid, code, status string
		err := a.DB.QueryRow(ctx, `SELECT vehicle_id, COALESCE(gov_code,''), COALESCE(code_status,'待启用') FROM fleet_vehicles WHERE brand_id=$1 AND plate_no=$2`,
			cred.BrandID, row.PlateNo).Scan(&vid, &code, &status)
		if err != nil {
			vid, code, status = "", "", "待启用"
		}
		vehicles = append(vehicles, map[string]any{"plateNo": row.PlateNo, "vehicleId": vid, "govCode": code, "codeStatus": status})
	}
	writeJSON(w, 200, map[string]any{"accepted": len(sub.Rows), "vehicles": vehicles})
}

func (a *App) checkVehicleRows(ctx context.Context, brandID string, rows []VehicleRow) []rowError {
	var errs []rowError
	plates := map[string]int{}
	tags := map[string][]int{}
	for i, row := range rows {
		n := i + 1
		for _, f := range []struct{ name, val string }{{"brandId", row.BrandID}, {"plateNo", row.PlateNo}, {"model", row.Model}, {"pileTag", row.PileTag}} {
			if f.val == "" {
				errs = append(errs, rowError{n, f.name, f.name + " 必填", "必填"})
			}
		}
		t := row.PileTagType
		if t == "" {
			t = "RFID"
		}
		if !pileTagTypes[t] {
			errs = append(errs, rowError{n, "pileTagType", "pileTagType 只能是 RFID、蓝牙、视觉车牌、其他", "取值"})
		}
		if row.PlateNo != "" {
			if first, ok := plates[row.PlateNo]; ok {
				errs = append(errs, rowError{n, "plateNo", fmt.Sprintf("车号与第 %d 行重复", first), "重复"})
			} else {
				plates[row.PlateNo] = n
			}
		}
		if row.PileTag != "" {
			tags[t+"\x00"+row.PileTag] = append(tags[t+"\x00"+row.PileTag], n)
		}
	}
	for _, ns := range tags {
		if len(ns) > 1 {
			for _, n := range ns[1:] {
				errs = append(errs, rowError{n, "pileTag", fmt.Sprintf("车辆标识与第 %d 行重复，同一识别方式内须全局唯一", ns[0]), "重复"})
			}
		}
	}
	// 与生产清单里其他车辆的标识冲突
	for i, row := range rows {
		if row.PileTag == "" {
			continue
		}
		t := row.PileTagType
		if t == "" {
			t = "RFID"
		}
		var other string
		err := a.DB.QueryRow(ctx, `SELECT plate_no FROM fleet_vehicles WHERE COALESCE(pile_tag_type,'RFID')=$1 AND pile_tag=$2 AND NOT (brand_id=$3 AND plate_no=$4) LIMIT 1`,
			t, row.PileTag, brandID, row.PlateNo).Scan(&other)
		if err == nil {
			errs = append(errs, rowError{i + 1, "pileTag", fmt.Sprintf("车辆标识已被车号 %s 使用，同一识别方式内须全局唯一", other), "重复"})
		}
	}
	return errs
}
