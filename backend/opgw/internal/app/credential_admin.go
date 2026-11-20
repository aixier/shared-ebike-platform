package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// 管理端录入与修改运营商接入凭证（地址、出口白名单、服务商覆盖）。

type credCreateBody struct {
	HolderType      string   `json:"holderType"`
	HolderID        string   `json:"holderId"`
	HolderName      string   `json:"holderName"`
	OperatorIDs     []string `json:"operatorIds"`
	OperatorBaseURL string   `json:"operatorBaseUrl"`
	IPWhitelist     []string `json:"ipWhitelist"`
	Reason          string   `json:"reason"`
}

// validBaseURL 运营商接口基础地址：http 或 https 绝对地址，不带用户信息、查询串与片段。
func validBaseURL(s string) string {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "operatorBaseUrl 须为 http 或 https 的绝对地址，不带用户信息、查询串和片段"
	}
	return ""
}

// cleanWhitelist 逐项校验 IP 或 CIDR，去空白与重复。
func cleanWhitelist(in []string) ([]string, string) {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, err := parsePrefix(s); err != nil {
			return nil, "出口 IP 白名单里 " + s + " 不是合法的 IP 或 CIDR"
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, ""
}

// cleanOperatorIDs 去空白与重复；不允许空项。
func cleanOperatorIDs(in []string) ([]string, string) {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || len(s) > 64 {
			return nil, "operatorIds 里有空项或超长项"
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, ""
}

func (a *App) handleCreateCredential(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	claims := adminFrom(ctx)
	key := r.Header.Get("Idempotency-Key")
	scope := "create:" + claims.Subject
	if a.idemReplay(w, r, "create", scope, key) {
		return
	}
	raw, _ := readBody(r)
	var b credCreateBody
	if err := json.Unmarshal(raw, &b); err != nil {
		writeErr(w, http.StatusBadRequest, "INVALID", "报文不是合法 JSON")
		return
	}
	b.HolderID = strings.TrimSpace(b.HolderID)
	b.HolderName = strings.TrimSpace(b.HolderName)
	b.OperatorBaseURL = strings.TrimSpace(b.OperatorBaseURL)
	b.Reason = strings.TrimSpace(b.Reason)
	if b.HolderType != "运营商" && b.HolderType != "车联网服务商" {
		writeErr(w, http.StatusBadRequest, "INVALID", "holderType 取值 运营商｜车联网服务商")
		return
	}
	if b.HolderID == "" || len(b.HolderID) > 64 {
		writeErr(w, http.StatusBadRequest, "INVALID", "holderId 必填，最长 64 个字符")
		return
	}
	if msg := validBaseURL(b.OperatorBaseURL); msg != "" {
		writeErr(w, http.StatusBadRequest, "INVALID", msg)
		return
	}
	wl, msg := cleanWhitelist(b.IPWhitelist)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, "INVALID", msg)
		return
	}
	if b.Reason != "" && utf8.RuneCountInString(b.Reason) < 4 {
		writeErr(w, http.StatusBadRequest, "REASON_REQUIRED", "事由至少 4 个字")
		return
	}
	ops, msg := cleanOperatorIDs(b.OperatorIDs)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, "INVALID", msg)
		return
	}
	if b.HolderType == "运营商" {
		if len(ops) == 0 {
			ops = []string{b.HolderID}
		}
		if len(ops) != 1 || ops[0] != b.HolderID {
			writeErr(w, http.StatusBadRequest, "INVALID", "运营商自行接入时 operatorIds 只能是 holderId 自己")
			return
		}
	} else if len(ops) == 0 {
		writeErr(w, http.StatusBadRequest, "INVALID", "车联网服务商须在 operatorIds 里写明覆盖的运营商")
		return
	}
	if claims.Scope != nil && (b.HolderType != "运营商" || b.HolderID != *claims.Scope) {
		writeErr(w, http.StatusForbidden, "SCOPE_DENIED", "只能为本运营商录入凭证")
		return
	}
	name := b.HolderName
	if name == "" {
		name = b.HolderID
	}

	tx, err := a.DB.Begin(ctx)
	if err != nil {
		serverBusy(w)
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(830303)`); err != nil {
		serverBusy(w)
		return
	}
	conflict := func(msg string) {
		body, _ := json.Marshal(apiError{Code: "CONFLICT", Message: msg})
		_ = a.idemStore(ctx, tx, "create", scope, key, http.StatusConflict, body)
		_ = tx.Commit(ctx)
		writeRaw(w, http.StatusConflict, body)
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM credentials WHERE holder_id=$1`, b.HolderID).Scan(&n); err != nil {
		serverBusy(w)
		return
	}
	if n > 0 {
		conflict("持有方 " + b.HolderID + " 已有接入凭证；要改地址或白名单请用修改接口，要换密钥请轮换")
		return
	}
	for _, o := range ops {
		cs, err := a.credsCovering(ctx, tx, o)
		if err != nil {
			serverBusy(w)
			return
		}
		if len(cs) > 0 {
			conflict("运营商 " + o + " 已被凭证 " + cs[0].CredentialID + " 覆盖")
			return
		}
	}
	var seq int64
	if err := tx.QueryRow(ctx, `SELECT nextval('credential_seq')`).Scan(&seq); err != nil {
		serverBusy(w)
		return
	}
	credID := fmt.Sprintf("CRD%04d", seq)
	secret := newSecret()
	appID := newAppID()
	if _, err := tx.Exec(ctx, `INSERT INTO credentials (credential_id, app_id, holder_type, holder_id, holder_name, operator_ids, base_url, secret_enc, key_last4,
		ip_whitelist, rate_limit, status, issued_at, settings_managed) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'有效',$12,true)`,
		credID, appID, b.HolderType, b.HolderID, name, ops, b.OperatorBaseURL, a.seal([]byte(secret)), secret[len(secret)-4:],
		wl, a.Cfg.RateLimitPerMinute, a.Now()); err != nil {
		serverBusy(w)
		return
	}
	var reason *string
	if b.Reason != "" {
		reason = &b.Reason
	}
	if err := a.audit(ctx, tx, adminActor(claims), "录入接入凭证", credID, "录入 "+b.HolderType+" "+b.HolderID+" 的接入凭证", reason, nil,
		map[string]any{"appId": appID, "holderType": b.HolderType, "holderId": b.HolderID, "operatorIds": ops, "operatorBaseUrl": b.OperatorBaseURL, "ipWhitelist": wl, "keyMasked": "****" + secret[len(secret)-4:]}); err != nil {
		serverBusy(w)
		return
	}
	nc, err := a.credByID(ctx, tx, credID, false)
	if err != nil || nc == nil {
		serverBusy(w)
		return
	}
	body, _ := json.Marshal(map[string]any{"secret": secret, "credential": nc})
	if err := a.idemStore(ctx, tx, "create", scope, key, http.StatusOK, body); err != nil {
		serverBusy(w)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		serverBusy(w)
		return
	}
	writeRaw(w, http.StatusOK, body)
}

type credUpdateBody struct {
	HolderName      *string   `json:"holderName"`
	OperatorBaseURL *string   `json:"operatorBaseUrl"`
	IPWhitelist     *[]string `json:"ipWhitelist"`
	OperatorIDs     *[]string `json:"operatorIds"`
}

func bad(msg string) (int, any, error) {
	return http.StatusBadRequest, apiError{Code: "INVALID", Message: msg}, nil
}

func (a *App) handleUpdateCredential(w http.ResponseWriter, r *http.Request) {
	a.credWrite(w, r, "update", func(ctx context.Context, tx pgx.Tx, c *Credential, actor Actor, reason string, raw []byte) (int, any, error) {
		var b credUpdateBody
		if err := json.Unmarshal(raw, &b); err != nil {
			return bad("报文不是合法 JSON")
		}
		if c.Status == "停用" {
			return http.StatusConflict, apiError{Code: "CREDENTIAL_DISABLED", Message: "凭证已停用，不能修改"}, nil
		}
		if b.HolderName == nil && b.OperatorBaseURL == nil && b.IPWhitelist == nil && b.OperatorIDs == nil {
			return bad("没有要修改的字段")
		}
		before, after := map[string]any{}, map[string]any{}
		name, baseURL, wl, ops := c.HolderName, c.BaseURL, c.IPWhitelist, c.OperatorIDs
		if b.HolderName != nil {
			n := strings.TrimSpace(*b.HolderName)
			if n == "" || len(n) > 200 {
				return bad("holderName 不能为空")
			}
			if n != name {
				before["holderName"], after["holderName"] = name, n
				name = n
			}
		}
		if b.OperatorBaseURL != nil {
			u := strings.TrimSpace(*b.OperatorBaseURL)
			if msg := validBaseURL(u); msg != "" {
				return bad(msg)
			}
			if u != baseURL {
				before["operatorBaseUrl"], after["operatorBaseUrl"] = baseURL, u
				baseURL = u
			}
		}
		if b.IPWhitelist != nil {
			list, msg := cleanWhitelist(*b.IPWhitelist)
			if msg != "" {
				return bad(msg)
			}
			before["ipWhitelist"], after["ipWhitelist"] = wl, list
			wl = list
		}
		if b.OperatorIDs != nil {
			if c.HolderType != "车联网服务商" {
				return bad("只有车联网服务商的凭证可以调整覆盖的运营商")
			}
			if adminFrom(ctx).Scope != nil {
				return http.StatusForbidden, apiError{Code: "SCOPE_DENIED", Message: "带运营商范围的账号不能调整覆盖范围"}, nil
			}
			list, msg := cleanOperatorIDs(*b.OperatorIDs)
			if msg != "" {
				return bad(msg)
			}
			if len(list) == 0 {
				return bad("operatorIds 不能为空；不再代接请停用凭证")
			}
			for _, o := range list {
				cs, err := a.credsCovering(ctx, tx, o)
				if err != nil {
					return 0, nil, err
				}
				for _, other := range cs {
					if other.CredentialID != c.CredentialID {
						return http.StatusConflict, apiError{Code: "CONFLICT", Message: "运营商 " + o + " 已被凭证 " + other.CredentialID + " 覆盖"}, nil
					}
				}
			}
			before["operatorIds"], after["operatorIds"] = ops, list
			ops = list
		}
		if _, err := tx.Exec(ctx, `UPDATE credentials SET holder_name=$2, base_url=$3, ip_whitelist=$4, operator_ids=$5, settings_managed=true WHERE credential_id=$1`,
			c.CredentialID, name, baseURL, wl, ops); err != nil {
			return 0, nil, err
		}
		action, summary := "修改接入凭证", "修改接入凭证 "+c.AppID
		if len(after) == 1 && after["operatorIds"] != nil {
			action, summary = "调整凭证范围", "调整凭证 "+c.AppID+" 覆盖的运营商"
		}
		if err := a.audit(ctx, tx, actor, action, c.CredentialID, summary, &reason, before, after); err != nil {
			return 0, nil, err
		}
		nc, err := a.credByID(ctx, tx, c.CredentialID, false)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, nc, nil
	})
}
