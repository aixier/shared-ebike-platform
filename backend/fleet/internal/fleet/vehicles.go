package fleet

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"bike/fleet/internal/apperr"
)

/* ---------------- 车辆清单查询 ---------------- */

type VehicleFilter struct {
	OperatorID string // 已按数据范围确定后的运营商；空为全部
	BrandID    string
	CodeStatus string
	Status     string
	Q          string
	SortKey    string
	SortDir    string
	Page       int
	PageSize   int
}

var sortColumns = map[string]string{
	"vehicleId": "v.vehicle_id", "plateNo": "v.plate_no", "govCode": "v.gov_code", "brandId": "v.brand_id",
	"listedAt": "v.listed_at", "codeIssuedAt": "v.code_issued_at", "codeStatus": "v.code_status",
	"status": displayStatusExpr, "model": "v.model",
}

func (s *Service) ListVehicles(ctx context.Context, f VehicleFilter) (VehiclePage, error) {
	if f.Page == 0 {
		f.Page = 1
	}
	if f.PageSize == 0 {
		f.PageSize = 20
	}
	if f.Page < 1 || f.PageSize < 1 || f.PageSize > 200 {
		return VehiclePage{}, apperr.Invalid("分页参数不对：page 从 1 起，pageSize 为 1～200")
	}
	order := "v.vehicle_id"
	if f.SortKey != "" {
		col, ok := sortColumns[f.SortKey]
		if !ok {
			return VehiclePage{}, apperr.Invalid("不支持按 " + f.SortKey + " 排序")
		}
		order = col
	}
	dir := "ASC"
	switch f.SortDir {
	case "", "asc":
	case "desc":
		dir = "DESC"
	default:
		return VehiclePage{}, apperr.Invalid("排序方向只能是 asc 或 desc")
	}

	var conds []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	if f.OperatorID != "" {
		add("v.operator_id = ?", f.OperatorID)
	}
	scopeConds, scopeArgs := append([]string{}, conds...), append([]any{}, args...)
	if f.BrandID != "" {
		add("v.brand_id = ?", f.BrandID)
	}
	if f.CodeStatus != "" {
		add("v.code_status = ?", f.CodeStatus)
	}
	if f.Status != "" {
		add(displayStatusExpr+" = ?", f.Status)
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q) + "%"
		add("(v.plate_no ILIKE ? OR v.gov_code ILIKE $X OR v.vehicle_id ILIKE $X OR v.pile_tag ILIKE $X)", like)
		conds[len(conds)-1] = strings.ReplaceAll(conds[len(conds)-1], "$X", fmt.Sprintf("$%d", len(args)))
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	page := VehiclePage{Rows: []Vehicle{}, Page: f.Page, PageSize: f.PageSize}
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM vehicles v`+where, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	sql := vehicleSelect + where + fmt.Sprintf(" ORDER BY %s %s, v.vehicle_id %s LIMIT %d OFFSET %d", order, dir, dir, f.PageSize, (f.Page-1)*f.PageSize)
	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		v, err := scanVehicle(rows)
		if err != nil {
			return page, err
		}
		page.Rows = append(page.Rows, v)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}

	// 数据范围内的码状态统计，不随筛选与分页变化
	sw := ""
	if len(scopeConds) > 0 {
		sw = " WHERE " + strings.Join(scopeConds, " AND ")
	}
	err = s.db.QueryRow(ctx, `SELECT count(*),
		count(*) FILTER (WHERE v.code_status = '已启用'),
		count(*) FILTER (WHERE v.code_status = '待启用'),
		count(*) FILTER (WHERE v.code_status = '已失效') FROM vehicles v`+sw, scopeArgs...).
		Scan(&page.Stats.Total, &page.Stats.Enabled, &page.Stats.Pending, &page.Stats.Invalid)
	return page, err
}

/* ---------------- 桩识别用车辆标识 ---------------- */

var (
	reHex24 = regexp.MustCompile(`^[0-9A-F]{24}$`)
	reHex12 = regexp.MustCompile(`^[0-9A-F]{12}$`)
	rePlate = regexp.MustCompile(`^[A-Z]{2}\d{6}$`)
)

// normalizePileTag 按识别方式规范化并校验车辆标识；返回规范后的值与错误说明（空为通过）。
func normalizePileTag(typ, tag string) (string, string) {
	t := strings.TrimSpace(tag)
	switch typ {
	case "RFID":
		t = strings.ToUpper(strings.ReplaceAll(t, " ", ""))
		if !reHex24.MatchString(t) {
			return t, "RFID 车辆标识应为 24 位十六进制"
		}
	case "蓝牙":
		t = strings.ToUpper(strings.NewReplacer(":", "", "-", "", " ", "").Replace(t))
		if !reHex12.MatchString(t) {
			return t, "蓝牙车辆标识应为 12 位十六进制（MAC 地址）"
		}
	case "视觉车牌":
		t = strings.ToUpper(t)
		if n := utf8.RuneCountInString(t); n == 0 || n > 32 {
			return t, "视觉车牌标识应为 1～32 个字符"
		}
	case "其他":
		if n := utf8.RuneCountInString(t); n == 0 || n > 64 {
			return t, "车辆标识应为 1～64 个字符"
		}
	default:
		return t, "桩识别方式只能是 RFID、蓝牙、视觉车牌、其他"
	}
	return t, ""
}

func validTagType(t string) bool {
	switch t {
	case "RFID", "蓝牙", "视觉车牌", "其他":
		return true
	}
	return false
}

// tagTaken：同一识别方式内，在用（码未失效）的车是否已占用该标识。
func tagTaken(ctx context.Context, q querier, typ, tag, exceptVehicle string) (bool, error) {
	var taken bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM vehicles WHERE pile_tag_type = $1 AND pile_tag = $2
		AND code_status <> '已失效' AND vehicle_id <> $3)`, typ, tag, exceptVehicle).Scan(&taken)
	return taken, err
}

/* ---------------- 提交车辆清单（页面上传与 opgw 转交共用） ---------------- */

type brandInfo struct {
	id, name, status, prefix string
}

type existingVehicle struct {
	id, operatorID, brandID, codeStatus string
	removed                             bool
}

// SubmitVehicleList 整批校验，全对才入库并发码；任一行有错整批拒绝，逐行返回错误。
// 已在清单内的本运营商同品牌车号按更新处理（清单只做新增与更新）。
func (s *Service) SubmitVehicleList(ctx context.Context, operatorID string, rows []VehicleListRow, actor Actor) (VehicleListResult, error) {
	res := VehicleListResult{Vehicles: []Vehicle{}}
	if len(rows) == 0 {
		return res, apperr.Invalid("清单为空")
	}
	now := s.now()
	err := s.tx(ctx, func(tx pgx.Tx) error {
		// 车辆清单写入串行执行，保证车号、标识、编号不撞
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('fleet.vehicle_list'))`); err != nil {
			return err
		}
		var opStatus, opShort string
		err := tx.QueryRow(ctx, `SELECT status, short_name FROM operators WHERE operator_id = $1`, operatorID).Scan(&opStatus, &opShort)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.NotFound("运营商")
		}
		if err != nil {
			return err
		}
		if opStatus == "已退出" {
			return apperr.Readonly()
		}
		brands, err := operatorBrands(ctx, tx, operatorID)
		if err != nil {
			return err
		}

		type plan struct {
			row      VehicleListRow
			existing *existingVehicle
		}
		var plans []plan
		var errs []apperr.RowError
		seenPlate, seenTag := map[string]bool{}, map[string]bool{}
		for i, r := range rows {
			n := i + 1
			bad := func(field, msg string) { errs = append(errs, apperr.RowError{Row: n, Field: field, Message: msg}) }
			b, ok := brands[r.BrandID]
			if !ok {
				bad("brandId", "品牌不属于本运营商")
			} else if b.status == "未接入" || b.status == "已退出" {
				bad("brandId", "品牌"+b.status+"，不能提交车辆")
			}
			r.PlateNo = strings.ToUpper(strings.TrimSpace(r.PlateNo))
			r.Model = strings.TrimSpace(r.Model)
			if r.PileTagType == "" {
				r.PileTagType = "RFID"
			}
			var ex *existingVehicle
			switch {
			case !rePlate.MatchString(r.PlateNo):
				bad("plateNo", "车号应为 2 位字母加 6 位数字")
			case ok && b.prefix != "" && r.PlateNo[:2] != b.prefix:
				bad("plateNo", "该品牌车号以 "+b.prefix+" 开头")
			case seenPlate[r.PlateNo]:
				bad("plateNo", "车号重复")
			default:
				ex, err = findPlate(ctx, tx, r.PlateNo)
				if err != nil {
					return err
				}
				if ex != nil {
					switch {
					case ex.operatorID != operatorID:
						bad("plateNo", "车号重复")
						ex = nil
					case ex.removed:
						bad("plateNo", "该车号已移出清单，不能再次提交")
						ex = nil
					case ex.brandID != r.BrandID:
						bad("plateNo", "该车号已登记在本运营商的其他品牌下")
						ex = nil
					}
				}
			}
			seenPlate[r.PlateNo] = true
			if !validTagType(r.PileTagType) {
				bad("pileTagType", "桩识别方式只能是 RFID、蓝牙、视觉车牌、其他")
			} else {
				tag, msg := normalizePileTag(r.PileTagType, r.PileTag)
				r.PileTag = tag
				except := ""
				if ex != nil {
					except = ex.id
				}
				if msg != "" {
					bad("pileTag", msg)
				} else if seenTag[r.PileTagType+"|"+tag] {
					bad("pileTag", "车辆标识重复")
				} else if taken, err := tagTaken(ctx, tx, r.PileTagType, tag, except); err != nil {
					return err
				} else if taken {
					bad("pileTag", "车辆标识重复")
				}
				seenTag[r.PileTagType+"|"+tag] = true
			}
			if r.Model == "" {
				bad("model", "请填写车型")
			}
			plans = append(plans, plan{row: r, existing: ex})
		}
		if len(errs) > 0 {
			rowsBad := map[int]bool{}
			for _, e := range errs {
				rowsBad[e.Row] = true
			}
			e := apperr.New(400, "VEHICLE_LIST_INVALID", fmt.Sprintf("车辆清单有 %d 行错误，请修改后重新上传", len(rowsBad)))
			e.Errors = errs
			return e
		}

		addedByBrand, updatedByBrand := map[string]int{}, map[string]int{}
		firstCode := map[string]string{}
		var ids []string
		for _, p := range plans {
			r := p.row
			b := brands[r.BrandID]
			if p.existing != nil {
				if _, err := tx.Exec(ctx, `UPDATE vehicles SET model = $2, pile_tag = $3, pile_tag_type = $4 WHERE vehicle_id = $1`,
					p.existing.id, r.Model, r.PileTag, r.PileTagType); err != nil {
					return err
				}
				ids = append(ids, p.existing.id)
				updatedByBrand[b.id]++
				continue
			}
			seq, err := nextSeq(ctx, tx, "vehicle")
			if err != nil {
				return err
			}
			vid := fmt.Sprintf("VH%06d", seq)
			code, err := newGovCode(ctx, tx)
			if err != nil {
				return err
			}
			// 已接入（含暂停可租）品牌的码立即启用；联调中品牌的码待启用，计划启用时刻沿用该品牌已有的计划
			codeStatus, runStatus := "已启用", "可用"
			var enableAt *time.Time
			if b.status == "联调中" {
				codeStatus = "待启用"
				if err := tx.QueryRow(ctx, `SELECT max(code_enable_at) FROM vehicles WHERE brand_id = $1 AND code_status = '待启用'`, b.id).Scan(&enableAt); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO vehicles (vehicle_id, brand_id, operator_id, plate_no, gov_code, pile_tag_type, pile_tag,
				vehicle_type, model, listed_at, code_issued_at, code_status, code_enable_at, run_status)
				SELECT $1, $2, $3, $4, $5, $6, $7, vehicle_type, $8, $9, $9, $10, $11, $12 FROM brands WHERE brand_id = $2`,
				vid, b.id, operatorID, r.PlateNo, code, r.PileTagType, r.PileTag, r.Model, now, codeStatus, enableAt, runStatus); err != nil {
				return err
			}
			if b.prefix == "" {
				if _, err := tx.Exec(ctx, `UPDATE brands SET plate_prefix = $2 WHERE brand_id = $1 AND plate_prefix IS NULL`, b.id, r.PlateNo[:2]); err != nil {
					return err
				}
				b.prefix = r.PlateNo[:2]
				brands[b.id] = b
			}
			if firstCode[b.id] == "" {
				firstCode[b.id] = code
			}
			ids = append(ids, vid)
			addedByBrand[b.id]++
		}

		for _, id := range ids {
			v, err := getVehicle(ctx, tx, `v.vehicle_id = $1`, id)
			if err != nil {
				return err
			}
			if err := s.emit(ctx, tx, "fleet.vehicle.listed", v.VehicleID, actor, now, listedPayload(v)); err != nil {
				return err
			}
			res.Vehicles = append(res.Vehicles, v)
		}
		for _, b := range sortedKeys(brands) {
			a, u := addedByBrand[b], updatedByBrand[b]
			if a+u == 0 {
				continue
			}
			summary := fmt.Sprintf("%s 提交车辆清单：新增 %d 辆，更新 %d 辆", opShort, a, u)
			if err := s.audit(ctx, tx, actor, now, "提交车辆清单", "brand", b, summary, nil, nil, nil); err != nil {
				return err
			}
			if a > 0 {
				summary := fmt.Sprintf("按车辆清单发放政府码 %d 个（%s 等）", a, firstCode[b])
				if err := s.audit(ctx, tx, s.systemActor, now, "发放政府码", "brand", b, summary, nil, nil, nil); err != nil {
					return err
				}
			}
			res.Added += a
			res.Updated += u
		}
		return nil
	})
	return res, err
}

func operatorBrands(ctx context.Context, q querier, operatorID string) (map[string]brandInfo, error) {
	rows, err := q.Query(ctx, `SELECT b.brand_id, b.name, b.access_status,
		COALESCE(b.plate_prefix, (SELECT left(plate_no, 2) FROM vehicles WHERE brand_id = b.brand_id ORDER BY vehicle_id LIMIT 1), '')
		FROM brands b WHERE b.operator_id = $1`, operatorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]brandInfo{}
	for rows.Next() {
		var b brandInfo
		if err := rows.Scan(&b.id, &b.name, &b.status, &b.prefix); err != nil {
			return nil, err
		}
		out[b.id] = b
	}
	return out, rows.Err()
}

func findPlate(ctx context.Context, q querier, plate string) (*existingVehicle, error) {
	e := &existingVehicle{}
	err := q.QueryRow(ctx, `SELECT vehicle_id, operator_id, brand_id, code_status, removed_at IS NOT NULL FROM vehicles WHERE plate_no = $1`, plate).
		Scan(&e.id, &e.operatorID, &e.brandID, &e.codeStatus, &e.removed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return e, err
}

func sortedKeys(m map[string]brandInfo) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func listedPayload(v Vehicle) map[string]any {
	return map[string]any{
		"vehicleId": v.VehicleID, "brandId": v.BrandID, "operatorId": v.OperatorID, "plateNo": v.PlateNo,
		"govCode": v.GovCode, "model": v.Model, "pileTagType": v.PileTagType, "pileTag": v.PileTag,
		"codeStatus": v.CodeStatus, "codeEnableAt": v.CodeEnableAt, "listedAt": v.ListedAt,
	}
}

/* ---------------- 在线编辑 ---------------- */

// requireActiveOperator：运营商范围内的写操作，运营商已退出一律拒绝（READONLY），不依赖令牌里的 readonly 何时刷新。
func requireActiveOperator(ctx context.Context, q querier, operatorID string) error {
	var status string
	err := q.QueryRow(ctx, `SELECT status FROM operators WHERE operator_id = $1`, operatorID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status == "已退出" {
		return apperr.Readonly()
	}
	return nil
}

func (s *Service) UpdateVehicle(ctx context.Context, scope, vehicleID string, p VehiclePatch, actor Actor) (Vehicle, error) {
	if p.Model == nil && p.PileTag == nil && p.PileTagType == nil {
		return Vehicle{}, apperr.Invalid("请至少修改一项")
	}
	var out Vehicle
	now := s.now()
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('fleet.vehicle_list'))`); err != nil {
			return err
		}
		v, err := getVehicle(ctx, tx, `v.vehicle_id = $1`, vehicleID)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.NotFound("车辆")
		}
		if err != nil {
			return err
		}
		if scope != "" && v.OperatorID != scope {
			return apperr.ScopeDenied()
		}
		if err := requireActiveOperator(ctx, tx, v.OperatorID); err != nil {
			return err
		}
		if v.CodeStatus == "已失效" {
			return apperr.Invalid("该车的码已失效，不能编辑")
		}
		model, typ, tag := v.Model, v.PileTagType, v.PileTag
		if p.Model != nil {
			model = strings.TrimSpace(*p.Model)
			if model == "" {
				return apperr.Invalid("请填写车型")
			}
		}
		if p.PileTagType != nil {
			typ = *p.PileTagType
			if !validTagType(typ) {
				return apperr.Invalid("桩识别方式只能是 RFID、蓝牙、视觉车牌、其他")
			}
		}
		if p.PileTag != nil {
			tag = *p.PileTag
		}
		if p.PileTag != nil || p.PileTagType != nil {
			var msg string
			if tag, msg = normalizePileTag(typ, tag); msg != "" {
				return apperr.Invalid(msg)
			}
			taken, err := tagTaken(ctx, tx, typ, tag, v.VehicleID)
			if err != nil {
				return err
			}
			if taken {
				return apperr.Invalid("车辆标识重复")
			}
		}
		before, after := map[string]any{}, map[string]any{}
		diff := func(k string, a, b string) {
			if a != b {
				before[k], after[k] = a, b
			}
		}
		diff("model", v.Model, model)
		diff("pileTagType", v.PileTagType, typ)
		diff("pileTag", v.PileTag, tag)
		if _, err := tx.Exec(ctx, `UPDATE vehicles SET model = $2, pile_tag_type = $3, pile_tag = $4 WHERE vehicle_id = $1`,
			v.VehicleID, model, typ, tag); err != nil {
			return err
		}
		if out, err = getVehicle(ctx, tx, `v.vehicle_id = $1`, v.VehicleID); err != nil {
			return err
		}
		if len(after) == 0 {
			return nil
		}
		if err := s.emit(ctx, tx, "fleet.vehicle.listed", out.VehicleID, actor, now, listedPayload(out)); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, now, "编辑车辆", "brand", out.BrandID, "编辑车辆 "+out.PlateNo, nil, before, after)
	})
	return out, err
}

/* ---------------- 移出清单 ---------------- */

func (s *Service) RemoveVehicle(ctx context.Context, scope, vehicleID, reason string, actor Actor) (Vehicle, error) {
	if !reasonOK(reason) {
		return Vehicle{}, apperr.ReasonRequired()
	}
	reason = strings.TrimSpace(reason)
	var out Vehicle
	now := s.now()
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var runStatus string
		err := tx.QueryRow(ctx, `SELECT run_status FROM vehicles WHERE vehicle_id = $1 FOR UPDATE`, vehicleID).Scan(&runStatus)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.NotFound("车辆")
		}
		if err != nil {
			return err
		}
		v, err := getVehicle(ctx, tx, `v.vehicle_id = $1`, vehicleID)
		if err != nil {
			return err
		}
		if scope != "" && v.OperatorID != scope {
			return apperr.ScopeDenied()
		}
		if err := requireActiveOperator(ctx, tx, v.OperatorID); err != nil {
			return err
		}
		if runStatus == "骑行中" {
			return apperr.Conflict("车辆正在骑行中，还车后再移出")
		}
		if v.CodeStatus == "已失效" {
			return apperr.AlreadyDone("该车的码已失效")
		}
		if _, err := tx.Exec(ctx, `UPDATE vehicles SET code_status = '已失效', code_invalid_at = $2, removed_at = $2, remove_reason = $3
			WHERE vehicle_id = $1`, vehicleID, now, reason); err != nil {
			return err
		}
		if out, err = getVehicle(ctx, tx, `v.vehicle_id = $1`, vehicleID); err != nil {
			return err
		}
		if err := s.emit(ctx, tx, "fleet.vehicle.removed", v.VehicleID, actor, now, map[string]any{
			"vehicleId": v.VehicleID, "brandId": v.BrandID, "operatorId": v.OperatorID, "govCode": v.GovCode, "removedAt": fmtTime(now),
		}); err != nil {
			return err
		}
		if err := s.emitCodeChanged(ctx, tx, actor, now, v.VehicleID, v.BrandID, v.GovCode, v.CodeStatus, "已失效"); err != nil {
			return err
		}
		summary := fmt.Sprintf("从清单移出车辆 %s，政府码 %s 失效", v.PlateNo, v.GovCode)
		return s.audit(ctx, tx, actor, now, "移出车辆", "brand", v.BrandID, summary, &reason,
			map[string]any{"codeStatus": v.CodeStatus}, map[string]any{"codeStatus": "已失效"})
	})
	return out, err
}

func (s *Service) emitCodeChanged(ctx context.Context, tx pgx.Tx, actor Actor, at time.Time, vehicleID, brandID, govCode, from, to string) error {
	return s.emit(ctx, tx, "fleet.code.status_changed", vehicleID, actor, at, map[string]any{
		"vehicleId": vehicleID, "brandId": brandID, "govCode": govCode, "from": from, "to": to, "changedAt": fmtTime(at),
	})
}
