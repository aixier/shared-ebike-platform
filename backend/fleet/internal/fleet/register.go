package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"bike/fleet/internal/apperr"
)

// 登记：运营商、品牌、车联网服务商由主管部门经管理端接口登记（启动时的登记数据文件仍可兜底，两者只新增、互不覆盖）。

var (
	reProviderID = regexp.MustCompile(`^SP\d{2}$`)
	rePrefix     = regexp.MustCompile(`^[A-Z]{2}$`)
)

type ProviderDetail struct {
	Provider
	OperatorIDs []string `json:"operatorIds"`
}

type RegisterOperatorReq struct {
	OperatorID        string             `json:"operatorId"`
	Name              string             `json:"name"`
	ShortName         string             `json:"shortName"`
	Type              string             `json:"type"`
	ProviderID        string             `json:"providerId"`
	CreditCode        string             `json:"creditCode"`
	Contacts          []Contact          `json:"contacts"`
	SettlementAccount *SettlementAccount `json:"settlementAccount"`
	Reason            string             `json:"reason"`
}

type RegisterBrandReq struct {
	BrandID     string `json:"brandId"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Color       string `json:"color"`
	VehicleType string `json:"vehicleType"`
	OperatorID  string `json:"operatorId"`
	ProviderID  string `json:"providerId"`
	PlatePrefix string `json:"platePrefix"`
	Reason      string `json:"reason"`
}

type RegisterProviderReq struct {
	ProviderID  string   `json:"providerId"`
	Name        string   `json:"name"`
	Contact     *Contact `json:"contact"`
	OperatorIDs []string `json:"operatorIds"`
	Reason      string   `json:"reason"`
}

func isUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// allocID 取一个编号：explicit 为空按"前缀 + 两位数"顺延（取现有最大号加一），否则检查格式与是否已存在。
func allocID(ctx context.Context, tx pgx.Tx, table, col, prefix, what, explicit string, re *regexp.Regexp) (string, error) {
	if explicit != "" {
		if !re.MatchString(explicit) {
			return "", apperr.Invalid(fmt.Sprintf("%s编号格式应为 %s 加两位数字", what, prefix))
		}
		var exists bool
		if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE %s = $1)`, table, col), explicit).Scan(&exists); err != nil {
			return "", err
		}
		if exists {
			return "", apperr.AlreadyDone(fmt.Sprintf("%s %s 已登记", what, explicit))
		}
		return explicit, nil
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "fleet-id:"+table); err != nil {
		return "", err
	}
	var max int
	if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT COALESCE(max(substr(%s, 3)::int), 0) FROM %s WHERE %s ~ '^%s[0-9]+$'`, col, table, col, prefix)).Scan(&max); err != nil {
		return "", err
	}
	if max+1 > 99 {
		return "", apperr.Conflict(fmt.Sprintf("%s编号已用尽（%s01 至 %s99），请联系平台处理", what, prefix, prefix))
	}
	return fmt.Sprintf("%s%02d", prefix, max+1), nil
}

func cleanText(s string, max int) (string, bool) {
	s = strings.TrimSpace(s)
	return s, s != "" && utf8.RuneCountInString(s) <= max
}

/* ---------------- 车联网服务商 ---------------- */

const providerSelect = `SELECT p.provider_id, p.name, p.contact,
	COALESCE((SELECT array_agg(operator_id ORDER BY operator_id) FROM operators WHERE provider_id = p.provider_id), '{}')
	FROM providers p`

func scanProvider(row pgx.Row) (ProviderDetail, error) {
	var p ProviderDetail
	var contact []byte
	if err := row.Scan(&p.ProviderID, &p.Name, &contact, &p.OperatorIDs); err != nil {
		return p, err
	}
	if len(contact) > 0 && string(contact) != "null" {
		p.Contact = &Contact{}
		_ = json.Unmarshal(contact, p.Contact)
	}
	return p, nil
}

func getProvider(ctx context.Context, q querier, id string) (ProviderDetail, error) {
	p, err := scanProvider(q.QueryRow(ctx, providerSelect+` WHERE p.provider_id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return p, apperr.NotFound("车联网服务商")
	}
	return p, err
}

func (s *Service) ListProviders(ctx context.Context) ([]ProviderDetail, error) {
	rows, err := s.db.Query(ctx, providerSelect+` ORDER BY p.provider_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProviderDetail{}
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Service) GetProvider(ctx context.Context, id string) (ProviderDetail, error) {
	return getProvider(ctx, s.db, id)
}

func (s *Service) validateProviderReq(r *RegisterProviderReq) error {
	var ok bool
	if r.Name, ok = cleanText(r.Name, 100); !ok {
		return apperr.Invalid("请填写服务商名称（100 字以内）")
	}
	if !reasonOK(r.Reason) {
		return apperr.ReasonRequired()
	}
	r.Reason = strings.TrimSpace(r.Reason)
	seen := map[string]bool{}
	ids := []string{}
	for _, id := range r.OperatorIDs {
		if !reOperatorID.MatchString(id) {
			return apperr.Invalid("覆盖的运营商编号格式应为 OP 加两位数字：" + id)
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	r.OperatorIDs = ids
	return nil
}

// applyCoverage 把服务商覆盖的运营商整体设为 ids：新增的改为代接，原来覆盖而这次没列的改回自行接入。
// 覆盖有变的运营商各发一条"运营商已登记"。返回变动的运营商编号。
func (s *Service) applyCoverage(ctx context.Context, tx pgx.Tx, providerID string, ids []string, actor Actor, at time.Time) ([]string, error) {
	var changed []string
	for _, id := range ids {
		var status string
		var cur *string
		err := tx.QueryRow(ctx, `SELECT status, provider_id FROM operators WHERE operator_id = $1 FOR UPDATE`, id).Scan(&status, &cur)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.NotFound("运营商 " + id)
		}
		if err != nil {
			return nil, err
		}
		if cur != nil && *cur == providerID {
			continue
		}
		if status == "已退出" {
			return nil, apperr.Conflict("运营商 " + id + " 已退出，不能设为代接")
		}
		if cur != nil {
			return nil, apperr.Conflict(fmt.Sprintf("运营商 %s 已由 %s 代接，请先在对方服务商处去掉", id, *cur))
		}
		if _, err := tx.Exec(ctx, `UPDATE operators SET provider_id = $2, access_via = '车联网服务商代接' WHERE operator_id = $1`, id, providerID); err != nil {
			return nil, err
		}
		changed = append(changed, id)
	}
	rows, err := tx.Query(ctx, `UPDATE operators SET provider_id = NULL, access_via = '自行接入'
		WHERE provider_id = $1 AND NOT (operator_id = ANY($2)) RETURNING operator_id`, providerID, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		changed = append(changed, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range changed {
		if err := s.emitOperatorRegistered(ctx, tx, actor, at, id); err != nil {
			return nil, err
		}
	}
	return changed, nil
}

func (s *Service) RegisterProvider(ctx context.Context, req RegisterProviderReq, actor Actor) (ProviderDetail, error) {
	var out ProviderDetail
	if err := s.validateProviderReq(&req); err != nil {
		return out, err
	}
	now := s.now()
	err := s.tx(ctx, func(tx pgx.Tx) error {
		id, err := allocID(ctx, tx, "providers", "provider_id", "SP", "车联网服务商", req.ProviderID, reProviderID)
		if err != nil {
			return err
		}
		var contact []byte
		if req.Contact != nil {
			contact = mustJSON(req.Contact)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO providers (provider_id, name, contact) VALUES ($1, $2, $3)`, id, req.Name, contact); err != nil {
			return err
		}
		if _, err := s.applyCoverage(ctx, tx, id, req.OperatorIDs, actor, now); err != nil {
			return err
		}
		reason := req.Reason
		if err := s.audit(ctx, tx, actor, now, "登记服务商", "provider", id, "登记车联网服务商 "+req.Name, &reason,
			nil, map[string]any{"name": req.Name, "operatorIds": req.OperatorIDs}); err != nil {
			return err
		}
		out, err = getProvider(ctx, tx, id)
		return err
	})
	if isUniqueViolation(err) {
		return ProviderDetail{}, apperr.AlreadyDone("该编号已登记，请重试")
	}
	return out, err
}

func (s *Service) UpdateProvider(ctx context.Context, id string, req RegisterProviderReq, actor Actor) (ProviderDetail, error) {
	var out ProviderDetail
	if err := s.validateProviderReq(&req); err != nil {
		return out, err
	}
	now := s.now()
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var oldName string
		err := tx.QueryRow(ctx, `SELECT name FROM providers WHERE provider_id = $1 FOR UPDATE`, id).Scan(&oldName)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.NotFound("车联网服务商")
		}
		if err != nil {
			return err
		}
		before, err := getProvider(ctx, tx, id)
		if err != nil {
			return err
		}
		var contact []byte
		if req.Contact != nil {
			contact = mustJSON(req.Contact)
		}
		if _, err := tx.Exec(ctx, `UPDATE providers SET name = $2, contact = $3 WHERE provider_id = $1`, id, req.Name, contact); err != nil {
			return err
		}
		if _, err := s.applyCoverage(ctx, tx, id, req.OperatorIDs, actor, now); err != nil {
			return err
		}
		reason := req.Reason
		if err := s.audit(ctx, tx, actor, now, "更新服务商", "provider", id, "更新车联网服务商 "+req.Name, &reason,
			map[string]any{"name": before.Name, "operatorIds": before.OperatorIDs},
			map[string]any{"name": req.Name, "operatorIds": req.OperatorIDs}); err != nil {
			return err
		}
		out, err = getProvider(ctx, tx, id)
		return err
	})
	return out, err
}

/* ---------------- 运营商 ---------------- */

func (s *Service) emitOperatorRegistered(ctx context.Context, tx pgx.Tx, actor Actor, at time.Time, operatorID string) error {
	o, err := getOperator(ctx, tx, operatorID)
	if err != nil {
		return err
	}
	var provID, provName *string
	if o.Provider != nil {
		provID, provName = &o.Provider.ProviderID, &o.Provider.Name
	}
	return s.emit(ctx, tx, "fleet.operator.registered", operatorID, actor, at, map[string]any{
		"operatorId": o.OperatorID, "name": o.Name, "shortName": o.ShortName, "type": o.Type, "accessVia": o.AccessVia,
		"providerId": provID, "providerName": provName, "status": o.Status,
	})
}

func (s *Service) RegisterOperator(ctx context.Context, req RegisterOperatorReq, actor Actor) (Operator, error) {
	var out Operator
	var ok bool
	if req.Name, ok = cleanText(req.Name, 100); !ok {
		return out, apperr.Invalid("请填写运营商全称（100 字以内）")
	}
	if req.ShortName, ok = cleanText(req.ShortName, 30); !ok {
		return out, apperr.Invalid("请填写运营商简称（30 字以内）")
	}
	if req.Type != "运营商" && req.Type != "政府自有车队" {
		return out, apperr.Invalid("类型 type 只能是 运营商 或 政府自有车队")
	}
	if !reasonOK(req.Reason) {
		return out, apperr.ReasonRequired()
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Contacts == nil {
		req.Contacts = []Contact{}
	}
	now := s.now()
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var provID *string
		via := "自行接入"
		if req.ProviderID != "" {
			if !reProviderID.MatchString(req.ProviderID) {
				return apperr.Invalid("车联网服务商编号格式应为 SP 加两位数字")
			}
			if _, err := getProvider(ctx, tx, req.ProviderID); err != nil {
				return err
			}
			provID, via = &req.ProviderID, "车联网服务商代接"
		}
		id, err := allocID(ctx, tx, "operators", "operator_id", "OP", "运营商", req.OperatorID, reOperatorID)
		if err != nil {
			return err
		}
		var acct []byte
		if req.SettlementAccount != nil {
			acct = mustJSON(req.SettlementAccount)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO operators (operator_id, name, short_name, type, access_via, provider_id, credit_code, joined_at, status, contacts, settlement_account)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, '在营', $9, $10)`,
			id, req.Name, req.ShortName, req.Type, via, provID, strings.TrimSpace(req.CreditCode), now, mustJSON(req.Contacts), acct); err != nil {
			return err
		}
		if err := s.emitOperatorRegistered(ctx, tx, actor, now, id); err != nil {
			return err
		}
		reason := req.Reason
		if err := s.audit(ctx, tx, actor, now, "登记运营商", "operator", id, "登记运营商 "+req.ShortName, &reason,
			nil, map[string]any{"name": req.Name, "type": req.Type, "accessVia": via, "providerId": provID}); err != nil {
			return err
		}
		out, err = getOperator(ctx, tx, id)
		return err
	})
	if isUniqueViolation(err) {
		return Operator{}, apperr.AlreadyDone("该编号已登记，请重试")
	}
	return out, err
}

/* ---------------- 品牌 ---------------- */

func (s *Service) RegisterBrand(ctx context.Context, req RegisterBrandReq, actor Actor) (BrandDetail, error) {
	var out BrandDetail
	var ok bool
	if req.Name, ok = cleanText(req.Name, 50); !ok {
		return out, apperr.Invalid("请填写品牌名（50 字以内）")
	}
	if !reOperatorID.MatchString(req.OperatorID) {
		return out, apperr.Invalid("请填写所属运营商编号（OP 加两位数字）")
	}
	if req.PlatePrefix != "" && !rePrefix.MatchString(req.PlatePrefix) {
		return out, apperr.Invalid("车牌前缀应为两位大写字母")
	}
	if req.ProviderID != "" && !reProviderID.MatchString(req.ProviderID) {
		return out, apperr.Invalid("车联网服务商编号格式应为 SP 加两位数字")
	}
	if !reasonOK(req.Reason) {
		return out, apperr.ReasonRequired()
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.VehicleType = strings.TrimSpace(req.VehicleType); req.VehicleType == "" {
		req.VehicleType = "电单车"
	}
	if req.DisplayName = strings.TrimSpace(req.DisplayName); req.DisplayName == "" {
		req.DisplayName = "【" + req.Name + "】" + req.VehicleType
	}
	now := s.now()
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var opStatus string
		err := tx.QueryRow(ctx, `SELECT status FROM operators WHERE operator_id = $1 FOR SHARE`, req.OperatorID).Scan(&opStatus)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.NotFound("运营商")
		}
		if err != nil {
			return err
		}
		if opStatus == "已退出" {
			return apperr.Conflict("运营商已退出，不能再登记品牌")
		}
		var provID *string
		if req.ProviderID != "" {
			if _, err := getProvider(ctx, tx, req.ProviderID); err != nil {
				return err
			}
			provID = &req.ProviderID
		}
		id, err := allocID(ctx, tx, "brands", "brand_id", "BR", "品牌", req.BrandID, reBrandID)
		if err != nil {
			return err
		}
		var prefix *string
		if req.PlatePrefix != "" {
			prefix = &req.PlatePrefix
		}
		if _, err := tx.Exec(ctx, `INSERT INTO brands (brand_id, name, display_name, color, vehicle_type, operator_id, access_status, status_since, plate_prefix, provider_id)
			VALUES ($1, $2, $3, $4, $5, $6, '未接入', $7, $8, $9)`,
			id, req.Name, req.DisplayName, strings.TrimSpace(req.Color), req.VehicleType, req.OperatorID, now, prefix, provID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO brand_status_history (brand_id, status, at, actor, note) VALUES ($1, '未接入', $2, $3, '登记')`,
			id, now, mustJSON(actor)); err != nil {
			return err
		}
		if err := s.emitBrandRegistered(ctx, tx, actor, now, id); err != nil {
			return err
		}
		reason := req.Reason
		if err := s.audit(ctx, tx, actor, now, "登记品牌", "brand", id, "登记品牌 "+req.Name, &reason,
			nil, map[string]any{"name": req.Name, "operatorId": req.OperatorID, "providerId": provID, "platePrefix": prefix}); err != nil {
			return err
		}
		b, err := getBrand(ctx, tx, id)
		if err != nil {
			return err
		}
		out, err = brandDetail(ctx, tx, b)
		return err
	})
	if isUniqueViolation(err) {
		return BrandDetail{}, apperr.AlreadyDone("该编号已登记，请重试")
	}
	return out, err
}

// emitBrandRegistered 按品牌当前登记内容发"品牌已登记"（登记接口与启动装入共用）。
func (s *Service) emitBrandRegistered(ctx context.Context, tx pgx.Tx, actor Actor, at time.Time, brandID string) error {
	b, err := getBrand(ctx, tx, brandID)
	if err != nil {
		return err
	}
	var name, short, typ string
	if err := tx.QueryRow(ctx, `SELECT name, short_name, type FROM operators WHERE operator_id = $1`, b.OperatorID).Scan(&name, &short, &typ); err != nil {
		return err
	}
	var prefix *string
	if b.PlatePrefix != "" {
		prefix = &b.PlatePrefix
	}
	return s.emit(ctx, tx, "fleet.brand.registered", b.BrandID, actor, at, map[string]any{
		"brandId": b.BrandID, "operatorId": b.OperatorID, "name": b.Name, "displayName": b.DisplayName,
		"operatorName": name, "operatorShortName": short, "operatorType": typ,
		"vehicleType": b.VehicleType, "color": b.Color, "accessStatus": b.AccessStatus,
		"providerId": b.ProviderID, "platePrefix": prefix,
	})
}
