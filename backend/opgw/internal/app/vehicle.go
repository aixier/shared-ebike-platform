package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"bike/opgw/internal/clients/fleet"
)

type vehicleListIn struct {
	Rows []fleet.Row `json:"rows"`
}

type vehicleListOut struct {
	Accepted int                `json:"accepted"`
	Vehicles []vehicleListedOut `json:"vehicles"`
}

type vehicleListedOut struct {
	PlateNo    string `json:"plateNo"`
	VehicleID  string `json:"vehicleId"`
	GovCode    string `json:"govCode"`
	CodeStatus string `json:"codeStatus"`
}

// handleVehicleList M-04：验签后转交 fleet，逐行错误原样返回。
func (a *App) handleVehicleList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	o := openFrom(ctx)
	key := r.Header.Get("Idempotency-Key")
	scope := "vehicle-list:" + o.Cred.CredentialID
	if key != "" {
		if len(key) > 64 {
			writeErr(w, http.StatusBadRequest, "INVALID", "Idempotency-Key 最长 64 个字符")
			return
		}
		hit, err := a.idemGet(ctx, scope, key)
		if err != nil {
			serverBusy(w)
			return
		}
		if hit != nil {
			writeRaw(w, hit.Status, hit.Body)
			return
		}
	}
	var in vehicleListIn
	if err := json.Unmarshal(o.Body, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "INVALID", "报文不是合法 JSON")
		return
	}
	if len(in.Rows) < 1 || len(in.Rows) > 5000 {
		writeErr(w, http.StatusBadRequest, "INVALID", "rows 须为 1～5000 行")
		return
	}
	// 按品牌找所属运营商，确认都在这份凭证名下；一次提交只能是一家运营商的车
	ops := map[string]bool{}
	for _, row := range in.Rows {
		var op string
		err := a.DB.QueryRow(ctx, `SELECT operator_id FROM brands WHERE brand_id=$1`, strings.TrimSpace(row.BrandID)).Scan(&op)
		if err != nil || !o.Cred.covers(op) {
			writeErr(w, http.StatusForbidden, "SCOPE_DENIED", "品牌 "+row.BrandID+" 不属于这份凭证名下的运营商")
			return
		}
		ops[op] = true
	}
	if len(ops) > 1 {
		list := make([]string, 0, len(ops))
		for k := range ops {
			list = append(list, k)
		}
		sort.Strings(list)
		writeErr(w, http.StatusBadRequest, "INVALID", "一次只能提交一家运营商的车辆，本次涉及 "+strings.Join(list, "、"))
		return
	}
	var operatorID string
	for k := range ops {
		operatorID = k
	}
	fk := key
	if fk == "" {
		fk = newID("")
	}
	h := sha256.Sum256([]byte(o.Cred.CredentialID + "\n" + fk))
	resp, err := a.Fleet.Submit(ctx, "vl-"+hex.EncodeToString(h[:20]), fleet.Submission{
		OperatorID: operatorID, Rows: in.Rows, Actor: fleet.Actor{Kind: "系统", ID: "opgw", Name: "运营商系统提交（" + o.Cred.AppID + "）"},
	})
	if err != nil {
		a.Log.Warn("车辆清单转交 fleet 失败", "err", err.Error())
		serverBusy(w)
		return
	}
	status, body := resp.Status, resp.Body
	if resp.Result != nil {
		out := vehicleListOut{Accepted: resp.Result.Added, Vehicles: []vehicleListedOut{}}
		for _, v := range resp.Result.Vehicles {
			out.Vehicles = append(out.Vehicles, vehicleListedOut{PlateNo: v.PlateNo, VehicleID: v.VehicleID, GovCode: v.GovCode, CodeStatus: v.CodeStatus})
		}
		body, _ = json.Marshal(out)
	} else if status >= 500 {
		serverBusy(w)
		return
	}
	// 4xx 原样返回（VEHICLE_LIST_INVALID 的逐行错误、READONLY 等）
	if key != "" {
		if err := a.idemPut(ctx, a.DB, scope, key, status, body); err != nil {
			serverBusy(w)
			return
		}
	}
	writeRaw(w, status, body)
}
