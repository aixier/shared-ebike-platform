package app

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 政府码格式见 contracts/kernel.md：SF + 8 位数字
var govCodeRe = regexp.MustCompile(`(?i)SF\d{8}`)

// extractGovCode 从扫到的内容（码本身或含码的网址）里取政府码，取不到返回空串。
func extractGovCode(s string) string {
	return strings.ToUpper(govCodeRe.FindString(s))
}

type scanVehicle struct {
	VehicleID         string  `json:"vehicleId"`
	GovCode           string  `json:"govCode"`
	PlateNo           string  `json:"plateNo"`
	Model             *string `json:"model"`
	CodeStatus        string  `json:"codeStatus"`
	CodeIssuedAt      any     `json:"codeIssuedAt"`
	CodeEnableAt      any     `json:"codeEnableAt"`
	CodeInvalidAt     any     `json:"codeInvalidAt"`
	BrandID           string  `json:"brandId"`
	BrandName         string  `json:"brandName"`
	OperatorID        string  `json:"operatorId"`
	OperatorName      string  `json:"operatorName"`
	OperatorShortName string  `json:"operatorShortName"`
	OperatorStatus    string  `json:"operatorStatus"`
	AccessStatus      string  `json:"accessStatus"`
}

// lookupVehicle 按政府码查车码投影；同码多条时取未移除、最近更新的。
func lookupVehicle(ctx context.Context, q pgx.Tx, govCode string) (*scanVehicle, error) {
	var v scanVehicle
	var issued, enable, invalid *time.Time
	// 车辆已移出清单或所属运营商已退出：码状态一律显示"已失效"，不随之后到达的码状态事件恢复
	err := q.QueryRow(ctx, `SELECT v.vehicle_id, v.gov_code, v.plate_no, v.model,
			CASE WHEN v.removed OR o.status = '已退出' THEN '已失效' ELSE v.code_status END,
			v.code_issued_at, v.code_enable_at,
			COALESCE(v.code_invalid_at, CASE WHEN o.status = '已退出' THEN o.exited_at END),
			v.brand_id, COALESCE(b.name,''), COALESCE(o.operator_id, v.operator_id), COALESCE(o.name,''), COALESCE(o.short_name,''), COALESCE(o.status,'在营'), COALESCE(b.access_status,'未接入')
		FROM vehicles v
		LEFT JOIN brands b ON b.brand_id = v.brand_id
		LEFT JOIN operators o ON o.operator_id = COALESCE(NULLIF(v.operator_id,''), b.operator_id)
		WHERE v.gov_code = $1
		ORDER BY v.removed, v.updated_at DESC LIMIT 1`, govCode).
		Scan(&v.VehicleID, &v.GovCode, &v.PlateNo, &v.Model, &v.CodeStatus, &issued, &enable, &invalid,
			&v.BrandID, &v.BrandName, &v.OperatorID, &v.OperatorName, &v.OperatorShortName, &v.OperatorStatus, &v.AccessStatus)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v.CodeIssuedAt, v.CodeEnableAt, v.CodeInvalidAt = fmtTimePtr(issued), fmtTimePtr(enable), fmtTimePtr(invalid)
	return &v, nil
}

// 码状态到扫码结果的对应（契约枚举之间的一一映射）
var scanResultByCode = map[string]string{
	"已启用": "有效政府码",
	"待启用": "码未启用",
	"已失效": "码已失效",
}

func scanText(result string, v *scanVehicle) string {
	switch result {
	case "有效政府码":
		return "政府码有效，属于" + v.OperatorShortName + "（" + v.BrandName + "），接入状态：" + v.AccessStatus
	case "码未启用":
		return "政府码尚未启用，属于" + v.OperatorShortName + "（" + v.BrandName + "），接入状态：" + v.AccessStatus
	case "码已失效":
		if v.OperatorStatus == "已退出" {
			return "政府码已失效，" + v.OperatorShortName + "已退出，可拍照留证"
		}
		return "政府码已失效，" + v.OperatorShortName + "已将该车移出清单，可拍照留证"
	}
	return "未识别到政府码，请拍照取证"
}

func (a *App) handleScan(w http.ResponseWriter, r *http.Request) error {
	adm, err := a.officer(r, false)
	if err != nil {
		return err
	}
	var body struct {
		Code *string `json:"code"`
	}
	if err := decodeJSON(r, &body); err != nil {
		return err
	}
	if body.Code == nil {
		return errInvalid("请扫描车上的码")
	}
	raw := *body.Code
	return a.runIdem(w, r, "scan:"+adm.AccountID, func(tx pgx.Tx) (int, any, error) {
		ctx := r.Context()
		code := extractGovCode(raw)
		var v *scanVehicle
		if code != "" {
			if v, err = lookupVehicle(ctx, tx, code); err != nil {
				return 0, nil, err
			}
		}
		res := map[string]any{"isGovCode": false, "scanResult": "无政府码", "needCapture": true, "text": scanText("无政府码", nil), "vehicle": nil}
		var vehicleID, gov *string
		if v != nil {
			result := scanResultByCode[v.CodeStatus]
			if result == "" {
				result = "码已失效"
			}
			res = map[string]any{"isGovCode": true, "scanResult": result, "needCapture": result != "有效政府码", "text": scanText(result, v), "vehicle": v}
			vehicleID, gov = &v.VehicleID, &v.GovCode
		} else if code != "" {
			gov = &code
		}
		if _, err := tx.Exec(ctx, `INSERT INTO scan_records (officer_id, raw_code, gov_code, scan_result, vehicle_id, scanned_at) VALUES ($1,$2,$3,$4,$5,$6)`,
			adm.AccountID, raw, gov, res["scanResult"], vehicleID, a.now()); err != nil {
			return 0, nil, err
		}
		return http.StatusOK, res, nil
	})
}
