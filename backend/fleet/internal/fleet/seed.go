package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 登记数据：运营商、品牌、车联网服务商由主管部门线下登记后，以文件形式装入（契约里没有登记接口，见交付说明）。
// 文件格式与原型 mockdata/operators.json 一致，另可带品牌的状态历史、暂停记录与车辆。装入只新增，已存在的编号跳过。

type seedActor struct{ Actor }

// UnmarshalJSON 接受操作人对象，或只有账号编号的字符串（按"人"处理）。
func (a *seedActor) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		a.Actor = Actor{Kind: "人", ID: s}
		return nil
	}
	return json.Unmarshal(b, &a.Actor)
}

type seedFile struct {
	Providers []struct {
		ProviderID string   `json:"providerId"`
		Name       string   `json:"name"`
		Contact    *Contact `json:"contact"`
	} `json:"providers"`
	Operators []struct {
		OperatorID        string             `json:"operatorId"`
		Name              string             `json:"name"`
		ShortName         string             `json:"shortName"`
		Type              string             `json:"type"`
		AccessVia         string             `json:"accessVia"`
		ProviderID        *string            `json:"providerId"`
		CreditCode        string             `json:"creditCode"`
		JoinedAt          *string            `json:"joinedAt"`
		Status            string             `json:"status"`
		ExitedAt          *string            `json:"exitedAt"`
		Contacts          []Contact          `json:"contacts"`
		SettlementAccount *SettlementAccount `json:"settlementAccount"`
	} `json:"operators"`
	Brands []struct {
		BrandID       string  `json:"brandId"`
		Name          string  `json:"name"`
		OperatorID    string  `json:"operatorId"`
		VehicleType   string  `json:"vehicleType"`
		Color         string  `json:"color"`
		AccessStatus  string  `json:"accessStatus"`
		StatusSince   *string `json:"statusSince"`
		DisplayName   string  `json:"displayName"`
		PlatePrefix   *string `json:"platePrefix"`
		StatusHistory []struct {
			Status string     `json:"status"`
			At     string     `json:"at"`
			By     *seedActor `json:"by"`
			Note   *string    `json:"note"`
		} `json:"statusHistory"`
	} `json:"brands"`
	Pauses []struct {
		PauseID        string     `json:"pauseId"`
		BrandID        string     `json:"brandId"`
		PausedAt       string     `json:"pausedAt"`
		ResumedAt      *string    `json:"resumedAt"`
		Metric         string     `json:"metric"`
		Value          *float64   `json:"value"`
		Threshold      *float64   `json:"threshold"`
		SustainMinutes *int       `json:"sustainMinutes"`
		By             *seedActor `json:"by"`
		Note           *string    `json:"note"`
	} `json:"pauses"`
	Vehicles []struct {
		VehicleID     string  `json:"vehicleId"`
		BrandID       string  `json:"brandId"`
		OperatorID    string  `json:"operatorId"`
		PlateNo       string  `json:"plateNo"`
		GovCode       string  `json:"govCode"`
		PileTag       string  `json:"pileTag"`
		PileTagType   string  `json:"pileTagType"`
		VehicleType   string  `json:"vehicleType"`
		Model         string  `json:"model"`
		ListedAt      string  `json:"listedAt"`
		CodeIssuedAt  string  `json:"codeIssuedAt"`
		CodeStatus    string  `json:"codeStatus"`
		CodeEnableAt  *string `json:"codeEnableAt"`
		CodeInvalidAt *string `json:"codeInvalidAt"`
		Status        string  `json:"status"`
	} `json:"vehicles"`
}

func optTime(s *string) (*time.Time, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	t, err := parseTime(*s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func actorJSON(a *seedActor) []byte {
	if a == nil {
		return nil
	}
	return mustJSON(a.Actor)
}

// LoadSeed 装入登记数据，返回新增的记录数。新登记的品牌发"品牌已登记"，新入清单的车辆发"车辆已入清单"。
func (s *Service) LoadSeed(ctx context.Context, data []byte) (int, error) {
	var f seedFile
	if err := json.Unmarshal(data, &f); err != nil {
		return 0, fmt.Errorf("登记数据不是合法 JSON: %w", err)
	}
	added := 0
	now := s.now()
	err := s.tx(ctx, func(tx pgx.Tx) error {
		ins := func(sql string, args ...any) (bool, error) {
			tag, err := tx.Exec(ctx, sql, args...)
			return tag.RowsAffected() > 0, err
		}
		for _, p := range f.Providers {
			var contact []byte
			if p.Contact != nil {
				contact = mustJSON(p.Contact)
			}
			ok, err := ins(`INSERT INTO providers (provider_id, name, contact) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, p.ProviderID, p.Name, contact)
			if err != nil {
				return fmt.Errorf("服务商 %s: %w", p.ProviderID, err)
			}
			if ok {
				added++
			}
		}
		for _, o := range f.Operators {
			joined, err := optTime(o.JoinedAt)
			if err != nil {
				return err
			}
			exited, err := optTime(o.ExitedAt)
			if err != nil {
				return err
			}
			var acct []byte
			if o.SettlementAccount != nil {
				acct = mustJSON(o.SettlementAccount)
			}
			if o.Contacts == nil {
				o.Contacts = []Contact{}
			}
			status, via := o.Status, o.AccessVia
			if status == "" {
				status = "在营"
			}
			if via == "" {
				via = "自行接入"
			}
			ok, err := ins(`INSERT INTO operators (operator_id, name, short_name, type, access_via, provider_id, credit_code, joined_at, status, exited_at, contacts, settlement_account)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) ON CONFLICT DO NOTHING`,
				o.OperatorID, o.Name, o.ShortName, o.Type, via, o.ProviderID, o.CreditCode, joined, status, exited, mustJSON(o.Contacts), acct)
			if err != nil {
				return fmt.Errorf("运营商 %s: %w", o.OperatorID, err)
			}
			if ok {
				added++
			}
		}
		for _, b := range f.Brands {
			since, err := optTime(b.StatusSince)
			if err != nil {
				return err
			}
			vt := b.VehicleType
			if vt == "" {
				vt = "电单车"
			}
			ok, err := ins(`INSERT INTO brands (brand_id, name, display_name, color, vehicle_type, operator_id, access_status, status_since, plate_prefix)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT DO NOTHING`,
				b.BrandID, b.Name, b.DisplayName, b.Color, vt, b.OperatorID, b.AccessStatus, since, b.PlatePrefix)
			if err != nil {
				return fmt.Errorf("品牌 %s: %w", b.BrandID, err)
			}
			if !ok {
				continue
			}
			added++
			if len(b.StatusHistory) == 0 {
				at := now
				if since != nil {
					at = *since
				}
				if _, err := tx.Exec(ctx, `INSERT INTO brand_status_history (brand_id, status, at, actor, note) VALUES ($1, $2, $3, $4, '登记')`,
					b.BrandID, b.AccessStatus, at, mustJSON(s.systemActor)); err != nil {
					return err
				}
			}
			for _, h := range b.StatusHistory {
				at, err := parseTime(h.At)
				if err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO brand_status_history (brand_id, status, at, actor, note) VALUES ($1, $2, $3, $4, $5)`,
					b.BrandID, h.Status, at, actorJSON(h.By), h.Note); err != nil {
					return err
				}
			}
			if err := s.emitBrandRegistered(ctx, tx, s.systemActor, now, b.BrandID); err != nil {
				return err
			}
		}
		for _, p := range f.Pauses {
			paused, err := parseTime(p.PausedAt)
			if err != nil {
				return err
			}
			resumed, err := optTime(p.ResumedAt)
			if err != nil {
				return err
			}
			ok, err := ins(`INSERT INTO pauses (pause_id, brand_id, paused_at, resumed_at, metric, value, threshold, sustain_minutes, by_actor, note)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) ON CONFLICT DO NOTHING`,
				p.PauseID, p.BrandID, paused, resumed, p.Metric, p.Value, p.Threshold, p.SustainMinutes, actorJSON(p.By), p.Note)
			if err != nil {
				return fmt.Errorf("暂停记录 %s: %w", p.PauseID, err)
			}
			if ok {
				added++
			}
		}
		var maxVehicle int64
		for _, v := range f.Vehicles {
			listed, err := parseTime(v.ListedAt)
			if err != nil {
				return err
			}
			issued, err := parseTime(v.CodeIssuedAt)
			if err != nil {
				return err
			}
			enable, err := optTime(v.CodeEnableAt)
			if err != nil {
				return err
			}
			invalid, err := optTime(v.CodeInvalidAt)
			if err != nil {
				return err
			}
			// 原型里的展示状态"码未启用""码已失效"由码状态推出，车端运行状态按可用存
			run := v.Status
			switch run {
			case "可用", "骑行中", "低电量", "故障", "离线":
			default:
				run = "可用"
			}
			typ := v.PileTagType
			if typ == "" {
				typ = "RFID"
			}
			vt := v.VehicleType
			if vt == "" {
				vt = "电单车"
			}
			ok, err := ins(`INSERT INTO vehicles (vehicle_id, brand_id, operator_id, plate_no, gov_code, pile_tag_type, pile_tag, vehicle_type, model,
				listed_at, code_issued_at, code_status, code_enable_at, code_invalid_at, run_status)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15) ON CONFLICT DO NOTHING`,
				v.VehicleID, v.BrandID, v.OperatorID, strings.ToUpper(v.PlateNo), v.GovCode, typ, strings.ToUpper(v.PileTag), vt, v.Model,
				listed, issued, v.CodeStatus, enable, invalid, run)
			if err != nil {
				return fmt.Errorf("车辆 %s: %w", v.VehicleID, err)
			}
			if n, err := strconv.ParseInt(strings.TrimPrefix(v.VehicleID, "VH"), 10, 64); err == nil && n > maxVehicle {
				maxVehicle = n
			}
			if !ok {
				continue
			}
			added++
			full, err := getVehicle(ctx, tx, `v.vehicle_id = $1`, v.VehicleID)
			if err != nil {
				return err
			}
			if err := s.emit(ctx, tx, "fleet.vehicle.listed", full.VehicleID, s.systemActor, now, listedPayload(full)); err != nil {
				return err
			}
		}
		if maxVehicle > 0 {
			if err := bumpSeq(ctx, tx, "vehicle", maxVehicle); err != nil {
				return err
			}
		}
		return nil
	})
	return added, err
}
