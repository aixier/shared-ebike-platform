package app

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"bike/site/internal/auth"
	"bike/site/internal/config"
	"bike/site/internal/pilemsg"
)

// 设备侧错误
var (
	errUnknownDevice = errors.New("未登记的控制器")
)

// IngestResult 一条标准桩消息的处理结果
type IngestResult struct {
	Duplicate    bool  `json:"duplicate"`    // 重复到达，未再次生效
	Recognized   bool  `json:"recognized"`   // 在位的车辆标识都对应到了清单内车辆
	UnknownSlots []int `json:"unknownSlots"` // 平台没有登记的桩位号（已忽略）
}

// effectiveAt 设备时刻与收到时刻相差超过阈值时取收到时刻并告警
func (a *App) effectiveAt(deviceID string, ts, recv time.Time) time.Time {
	d := ts.Sub(recv)
	if d < 0 {
		d = -d
	}
	if ts.IsZero() || d > a.cfg.ClockSkew {
		a.log.Warn("设备时钟偏差过大，以收到时刻为准", "deviceId", deviceID, "deviceTs", ts, "receivedAt", recv)
		return recv
	}
	return ts
}

// devicePiles 控制器下的桩：桩位号 → 桩（加行锁）
func devicePiles(ctx context.Context, tx pgx.Tx, dev config.Device) (map[int]*pileRow, error) {
	var rows pgx.Rows
	var err error
	if dev.PileID != "" {
		rows, err = tx.Query(ctx, `SELECT `+pileCols+` FROM piles WHERE pile_id=$1 ORDER BY pile_id FOR UPDATE`, dev.PileID)
	} else {
		rows, err = tx.Query(ctx, `SELECT `+pileCols+` FROM piles WHERE station_id=$1 ORDER BY pile_id FOR UPDATE`, dev.StationID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]*pileRow{}
	for rows.Next() {
		p, err := scanPile(rows)
		if err != nil {
			return nil, err
		}
		if dev.PileID != "" {
			out[1] = &p // 单桩直连：只有 1 号桩位
		} else {
			out[p.No] = &p
		}
	}
	return out, rows.Err()
}

// Ingest 处理一条标准桩消息（MQTT、HTTP、厂商适配器共用）
func (a *App) Ingest(ctx context.Context, m pilemsg.Message) (IngestResult, error) {
	res := IngestResult{Recognized: true, UnknownSlots: []int{}}
	dev, ok := a.device(m.DeviceID)
	if !ok {
		return res, errUnknownDevice
	}
	recv := a.now()
	err := a.tx(ctx, func(tx pgx.Tx) error {
		if m.Kind != pilemsg.KindOffline {
			tag, err := tx.Exec(ctx, `INSERT INTO pile_msgs(device_id, msg_id, kind, received_at) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`, m.DeviceID, m.MsgID, string(m.Kind), recv)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				res.Duplicate = true
				return nil
			}
		}
		piles, err := devicePiles(ctx, tx, dev)
		if err != nil {
			return err
		}
		switch m.Kind {
		case pilemsg.KindOffline:
			note := "控制器断线"
			for _, no := range sortedSlots(piles) {
				p := piles[no]
				if p.Status != "离线" && !p.ManualFault {
					if err := a.changePileStatus(ctx, tx, p, "离线", &note, false, systemActor); err != nil {
						return err
					}
				}
			}
			return nil
		case pilemsg.KindHeartbeat:
			if len(m.Slots) == 0 { // 不带桩位：视为全部桩位正常
				for _, no := range sortedSlots(piles) {
					if err := a.applyHeartbeat(ctx, tx, piles[no], pilemsg.Slot{SlotNo: no, Status: "正常"}, recv); err != nil {
						return err
					}
				}
				return nil
			}
			for _, s := range m.Slots {
				p, ok := piles[s.SlotNo]
				if !ok {
					res.UnknownSlots = append(res.UnknownSlots, s.SlotNo)
					continue
				}
				if err := a.applyHeartbeat(ctx, tx, p, s, recv); err != nil {
					return err
				}
			}
			return nil
		default: // 在位、全量：按桩位逐个对齐
			at := a.effectiveAt(m.DeviceID, m.TS, recv)
			for _, s := range m.Slots {
				p, ok := piles[s.SlotNo]
				if !ok {
					res.UnknownSlots = append(res.UnknownSlots, s.SlotNo)
					continue
				}
				rec, err := a.applySlot(ctx, tx, m.DeviceID, p, s, at, recv)
				if err != nil {
					return err
				}
				res.Recognized = res.Recognized && rec
			}
			return nil
		}
	})
	if len(res.UnknownSlots) > 0 {
		a.log.Warn("桩消息含未登记的桩位，已忽略", "deviceId", m.DeviceID, "slots", res.UnknownSlots)
	}
	return res, err
}

func sortedSlots(m map[int]*pileRow) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// applyHeartbeat 记心跳；自检故障转故障，自检正常恢复在线；人工故障不动
func (a *App) applyHeartbeat(ctx context.Context, tx pgx.Tx, p *pileRow, s pilemsg.Slot, recv time.Time) error {
	if _, err := tx.Exec(ctx, `UPDATE piles SET last_heartbeat_at=$2 WHERE pile_id=$1`, p.ID, recv); err != nil {
		return err
	}
	p.LastHB = &recv
	if p.ManualFault {
		return nil
	}
	if s.Status == "故障" {
		note := s.Note
		if note == "" {
			note = "设备自检故障"
		}
		if p.Status == "故障" && p.FaultNote != nil && *p.FaultNote == note {
			return nil
		}
		return a.changePileStatus(ctx, tx, p, "故障", &note, false, systemActor)
	}
	if p.Status != "在线" {
		return a.changePileStatus(ctx, tx, p, "在线", nil, false, systemActor)
	}
	return nil
}

func strEq(p *string, s string) bool { return p != nil && *p == s }

// lookupVehicle 按识别方式 + 车辆标识找车辆编号；找不到返回空
func lookupVehicle(ctx context.Context, tx pgx.Tx, tagType, tag string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT vehicle_id FROM vehicle_tags WHERE tag_type=$1 AND tag=$2`, tagType, tag).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// applySlot 把一个桩位对齐到设备报告的状态；有变化且车辆能识别时发桩在位事件。返回车辆标识是否识别。
func (a *App) applySlot(ctx context.Context, tx pgx.Tx, deviceID string, p *pileRow, s pilemsg.Slot, at, recv time.Time) (bool, error) {
	vid := ""
	if s.Present {
		var err error
		if vid, err = lookupVehicle(ctx, tx, s.TagType, s.Tag); err != nil {
			return false, err
		}
	}
	if err := a.reconcileMarks(ctx, tx, p, s.Present, vid, at); err != nil {
		return false, err
	}
	if !s.Present {
		if !p.Occupied {
			return true, nil
		}
		return true, a.clearSlot(ctx, tx, p, at, recv)
	}
	same := p.Occupied && strEq(p.TagType, s.TagType) && strEq(p.Tag, s.Tag)
	if same && ((vid == "" && p.VehicleID == nil) || strEq(p.VehicleID, vid)) {
		return vid != "", nil // 状态没变
	}
	// 一个桩位同一时刻最多一辆车：原来在位的车视为离位
	if p.Occupied {
		if err := a.clearSlot(ctx, tx, p, at, recv); err != nil {
			return false, err
		}
	}
	if vid == "" {
		// 识别不出：只记录，不发事件
		if _, err := tx.Exec(ctx, `INSERT INTO pile_unrecognized(pile_id, device_id, tag_type, tag, present, at, received_at) VALUES ($1,$2,$3,$4,true,$5,$6)`,
			p.ID, deviceID, s.TagType, s.Tag, at, recv); err != nil {
			return false, err
		}
	} else {
		// 一辆车只在一个桩位：在别的桩位的记录先离位
		other, err := scanPile(tx.QueryRow(ctx, `SELECT `+pileCols+` FROM piles WHERE occupied_vehicle_id=$1 AND pile_id<>$2 FOR UPDATE`, vid, p.ID))
		if err == nil {
			if err := a.clearSlot(ctx, tx, &other, at, recv); err != nil {
				return false, err
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
	}
	var vptr *string
	if vid != "" {
		vptr = &vid
	}
	if _, err := tx.Exec(ctx, `UPDATE piles SET occupied=true, occupied_tag_type=$2, occupied_tag=$3, occupied_vehicle_id=$4, occupied_at=$5 WHERE pile_id=$1`,
		p.ID, s.TagType, s.Tag, vptr, at); err != nil {
		return false, err
	}
	tt, tg := s.TagType, s.Tag
	p.Occupied, p.TagType, p.Tag, p.VehicleID, p.OccupiedAt = true, &tt, &tg, vptr, &at
	if vid != "" {
		return true, a.emitOccupancy(ctx, tx, p, vid, true, at, recv)
	}
	return false, nil
}

// reconcileMarks 桩报告到达时处理骑行事件留下的临时判断：
// 已离开：桩报告空位、或换了别的车、或报告同一辆车在位且报告时刻晚于开锁时刻 → 清除；报告时刻不晚于开锁时刻的迟到报告不推翻它。
// 已回来：桩报告了那辆车在位、或报告时刻晚于还车时刻的任何别的状态 → 清除；更早的迟到报告不推翻它。
// vehicleID 为空表示空位或识别不出的车辆标识。
func (a *App) reconcileMarks(ctx context.Context, tx pgx.Tx, p *pileRow, present bool, vehicleID string, at time.Time) error {
	if p.LeftVehicle != nil && p.LeftAt != nil {
		clear := false
		switch {
		case !present:
			clear = true
		case vehicleID != *p.LeftVehicle:
			clear = true
		case at.After(*p.LeftAt):
			clear = true
		}
		if clear {
			if _, err := tx.Exec(ctx, `UPDATE piles SET ride_left_order=NULL, ride_left_vehicle_id=NULL, ride_left_at=NULL WHERE pile_id=$1`, p.ID); err != nil {
				return err
			}
			p.LeftOrder, p.LeftVehicle, p.LeftAt = nil, nil, nil
		}
	}
	if p.BackVehicle != nil && p.BackAt != nil {
		clear := false
		switch {
		case present && vehicleID == *p.BackVehicle:
			clear = true
		case at.After(*p.BackAt):
			clear = true
		}
		if clear {
			if _, err := tx.Exec(ctx, `UPDATE piles SET ride_back_order=NULL, ride_back_vehicle_id=NULL, ride_back_at=NULL WHERE pile_id=$1`, p.ID); err != nil {
				return err
			}
			p.BackOrder, p.BackVehicle, p.BackAt = nil, nil, nil
		}
	}
	return nil
}

// clearSlot 桩位清空；原来在位的车能识别时发离位事件
func (a *App) clearSlot(ctx context.Context, tx pgx.Tx, p *pileRow, at, recv time.Time) error {
	prev := p.VehicleID
	if _, err := tx.Exec(ctx, `UPDATE piles SET occupied=false, occupied_tag_type=NULL, occupied_tag=NULL, occupied_vehicle_id=NULL, occupied_at=NULL WHERE pile_id=$1`, p.ID); err != nil {
		return err
	}
	p.Occupied, p.TagType, p.Tag, p.VehicleID, p.OccupiedAt = false, nil, nil, nil, nil
	if prev != nil {
		return a.emitOccupancy(ctx, tx, p, *prev, false, at, recv)
	}
	return nil
}

func (a *App) emitOccupancy(ctx context.Context, tx pgx.Tx, p *pileRow, vehicleID string, present bool, at, recv time.Time) error {
	return a.emit(ctx, tx, "site.pile.occupancy", p.ID, systemActor, at, map[string]any{
		"pileId": p.ID, "stationId": p.StationID, "vehicleId": vehicleID, "present": present,
		"at": a.fmtTime(at), "receivedAt": a.fmtTime(recv),
	})
}

// ScanOffline 心跳超时的在线桩记为离线；多实例时用事务级咨询锁保证只跑一份
func (a *App) ScanOffline(ctx context.Context) (int, error) {
	n := 0
	err := a.tx(ctx, func(tx pgx.Tx) error {
		var got bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(1830601)`).Scan(&got); err != nil || !got {
			return err
		}
		cut := a.now().Add(-a.cfg.OfflineAfter)
		rows, err := tx.Query(ctx, `SELECT `+pileCols+` FROM piles WHERE status='在线' AND (last_heartbeat_at IS NULL OR last_heartbeat_at < $1) ORDER BY pile_id FOR UPDATE`, cut)
		if err != nil {
			return err
		}
		var ps []pileRow
		for rows.Next() {
			p, err := scanPile(rows)
			if err != nil {
				rows.Close()
				return err
			}
			ps = append(ps, p)
		}
		rows.Close()
		note := "心跳超时"
		for i := range ps {
			if err := a.changePileStatus(ctx, tx, &ps[i], "离线", &note, false, systemActor); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// ---------- 设备鉴权 ----------

// deviceKeyOK 控制器自己的密钥，或其厂商云平台的密钥
func (a *App) deviceKeyOK(dev config.Device, key string) bool {
	own := auth.SecretEqual(key, dev.Secret)
	if dev.SecretSHA256 != "" { // 管理端登记的控制器只存摘要
		own = key != "" && auth.SecretEqual(hashSecret(key), dev.SecretSHA256)
	}
	return own || (dev.Vendor != "" && auth.SecretEqual(key, a.cfg.VendorKeys[dev.Vendor]))
}

// devicesForPile 覆盖某个桩的控制器（站级或单桩直连）
func (a *App) devicesForPile(p pileRow) []config.Device {
	var out []config.Device
	for _, d := range a.allDevices() {
		if d.StationID == p.StationID || d.PileID == p.ID {
			out = append(out, d)
		}
	}
	return out
}

// ---------- swagger 里的旧 HTTP 入口（兼容） ----------

type legacyPresence struct {
	PileID      string `json:"pileId"`
	PileTag     string `json:"pileTag"`
	PileTagType string `json:"pileTagType"`
	Event       string `json:"event"`
	At          string `json:"at"`
	Seq         string `json:"seq"`
}

// IngestLegacyPresence /device/v1/pile-presence：按桩编号上报单个车辆入位或离位
func (a *App) IngestLegacyPresence(ctx context.Context, key string, in legacyPresence) (map[string]any, error) {
	if in.PileID == "" || in.Seq == "" || len(in.Seq) > 64 || (in.Event != "入位" && in.Event != "离位") || (in.Event == "入位" && in.PileTag == "") {
		return nil, fail("INVALID", "pileId、pileTag、event（入位｜离位）、at、seq 必填")
	}
	if in.PileTagType == "" {
		in.PileTagType = "RFID"
	}
	if !pilemsg.TagTypes[in.PileTagType] {
		return nil, fail("INVALID", "pileTagType 只能是 RFID、蓝牙、视觉车牌、其他")
	}
	ts, err := time.Parse(time.RFC3339Nano, in.At)
	if err != nil {
		return nil, fail("INVALID", "at 应为 RFC 3339 时刻")
	}
	dev, err := a.authPileKey(ctx, in.PileID, key)
	if err != nil {
		return nil, err
	}
	recv := a.now()
	ack := map[string]any{"received": true, "recognized": false, "duplicate": false}
	err = a.tx(ctx, func(tx pgx.Tx) error {
		p, err := scanPile(tx.QueryRow(ctx, `SELECT `+pileCols+` FROM piles WHERE pile_id=$1 FOR UPDATE`, in.PileID))
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO pile_msgs(device_id, msg_id, kind, received_at) VALUES ($1,$2,'pile-presence',$3) ON CONFLICT DO NOTHING`, "pile:"+in.PileID, in.Seq, recv)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			ack["duplicate"] = true
			ack["recognized"] = p.VehicleID != nil
			return nil
		}
		if in.Event == "入位" && p.Occupied && !(strEq(p.TagType, in.PileTagType) && strEq(p.Tag, in.PileTag)) {
			return fail("CONFLICT", "该桩位已有车辆在位，请先上报离位")
		}
		at := a.effectiveAt(dev.DeviceID, ts, recv)
		rec, err := a.applySlot(ctx, tx, dev.DeviceID, &p, pilemsg.Slot{SlotNo: p.No, Present: in.Event == "入位", Tag: in.PileTag, TagType: in.PileTagType}, at, recv)
		ack["recognized"] = rec
		return err
	})
	if err != nil {
		return nil, err
	}
	return ack, nil
}

// authPileKey 校验桩编号与设备密钥；返回覆盖该桩、密钥匹配的控制器
func (a *App) authPileKey(ctx context.Context, pileID, key string) (config.Device, error) {
	p, err := scanPile(a.db.QueryRow(ctx, `SELECT `+pileCols+` FROM piles WHERE pile_id=$1`, pileID))
	if errors.Is(err, pgx.ErrNoRows) {
		return config.Device{}, fail("NOT_FOUND", "没有找到桩 "+pileID)
	}
	if err != nil {
		return config.Device{}, err
	}
	for _, d := range a.devicesForPile(p) {
		if a.deviceKeyOK(d, key) {
			return d, nil
		}
	}
	return config.Device{}, fail("DEVICE_UNAUTHORIZED", "")
}

type legacyHeartbeat struct {
	PileID          string  `json:"pileId"`
	At              string  `json:"at"`
	SelfCheck       string  `json:"selfCheck"`
	FaultNote       string  `json:"faultNote"`
	OccupiedPileTag *string `json:"occupiedPileTag"`
	hasOccupied     bool
}

// IngestLegacyHeartbeat /device/v1/pile-heartbeat：单桩心跳，可带当前在位标识用于对齐
func (a *App) IngestLegacyHeartbeat(ctx context.Context, key string, in legacyHeartbeat) (map[string]any, error) {
	if in.PileID == "" {
		return nil, fail("INVALID", "pileId 必填")
	}
	ts, err := time.Parse(time.RFC3339Nano, in.At)
	if err != nil {
		return nil, fail("INVALID", "at 应为 RFC 3339 时刻")
	}
	if in.SelfCheck == "" {
		in.SelfCheck = "正常"
	}
	if in.SelfCheck != "正常" && in.SelfCheck != "故障" {
		return nil, fail("INVALID", "selfCheck 只能是正常或故障")
	}
	dev, err := a.authPileKey(ctx, in.PileID, key)
	if err != nil {
		return nil, err
	}
	recv := a.now()
	err = a.tx(ctx, func(tx pgx.Tx) error {
		p, err := scanPile(tx.QueryRow(ctx, `SELECT `+pileCols+` FROM piles WHERE pile_id=$1 FOR UPDATE`, in.PileID))
		if err != nil {
			return err
		}
		if err := a.applyHeartbeat(ctx, tx, &p, pilemsg.Slot{SlotNo: p.No, Status: in.SelfCheck, Note: in.FaultNote}, recv); err != nil {
			return err
		}
		if !in.hasOccupied {
			return nil
		}
		s := pilemsg.Slot{SlotNo: p.No}
		if in.OccupiedPileTag != nil && *in.OccupiedPileTag != "" {
			s.Present, s.Tag, s.TagType = true, *in.OccupiedPileTag, "RFID"
			if p.TagType != nil {
				s.TagType = *p.TagType
			}
		}
		_, err = a.applySlot(ctx, tx, dev.DeviceID, &p, s, a.effectiveAt(dev.DeviceID, ts, recv), recv)
		return err
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"received": true, "serverTime": a.fmtTime(recv)}, nil
}
