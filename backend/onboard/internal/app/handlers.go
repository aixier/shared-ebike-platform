package app

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"bike/onboard/internal/auth"
	"bike/onboard/internal/clients/fleet"
	"bike/onboard/internal/content"

	"github.com/jackc/pgx/v5"
)

func adminActor(adm *auth.Admin) Actor { return Actor{Kind: "人", ID: adm.AccountID, Name: adm.Name} }

// ownOperator 运营商账号只能操作本运营商的数据。
func ownOperator(adm *auth.Admin, operatorID string) error {
	if adm.Scope != "" && adm.Scope != operatorID {
		return errScope
	}
	return nil
}

// trace: listMyIntegrations · R-38 R-39 R-40
func (a *App) hListMyIntegrations(r *http.Request, adm *auth.Admin) (any, error) {
	list, err := a.listIntegrations(r.Context(), adm.Scope)
	if err != nil {
		return nil, err
	}
	out := []*IntegrationView{}
	for _, in := range list {
		v, err := a.integrationView(r.Context(), in)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// trace: getIntegrationSpec · R-36
func (a *App) hGetSpec(r *http.Request, adm *auth.Admin) (any, error) { return content.LoadSpec(), nil }

type sandboxView struct {
	BaseURL     string               `json:"baseUrl"`
	Note        string               `json:"note"`
	SimVehicles []content.SimVehicle `json:"simVehicles"`
	SimUsers    []content.SimUser    `json:"simUsers"`
	SimPayment  string               `json:"simPayment"`
	PaymentMode string               `json:"paymentMode"`
	Credentials []CredentialView     `json:"credentials"`
}

// trace: getSandbox · R-38
func (a *App) hGetSandbox(r *http.Request, adm *auth.Admin) (any, error) {
	ctx := r.Context()
	sb := content.LoadSandbox()
	v := sandboxView{BaseURL: a.Cfg.SandboxBaseURL, Note: sb.Note, SimVehicles: sb.SimVehicles, SimUsers: sb.SimUsers,
		SimPayment: sb.SimPayment, PaymentMode: a.paymentMode(ctx, adm.Scope), Credentials: []CredentialView{}}
	rows, err := a.DB.Query(ctx, `SELECT `+prefixed("c.", credCols)+` FROM credentials c JOIN integrations i ON i.brand_id=c.brand_id
		WHERE ($1='' OR i.operator_id=$1) ORDER BY c.brand_id`, adm.Scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		c, err := scanCred(rows)
		if err != nil {
			return nil, err
		}
		cv := c.view()
		cv.BrandID = c.BrandID
		v.Credentials = append(v.Credentials, cv)
	}
	return v, rows.Err()
}

func prefixed(p, cols string) string {
	out := ""
	for i, c := range splitCols(cols) {
		if i > 0 {
			out += ", "
		}
		out += p + c
	}
	return out
}

func splitCols(cols string) []string {
	var out []string
	cur := ""
	for _, ch := range cols {
		switch ch {
		case ',':
			out = append(out, cur)
			cur = ""
		case ' ', '\n', '\t':
		default:
			cur += string(ch)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func (a *App) paymentMode(ctx context.Context, operatorID string) string {
	mode := "正常"
	if operatorID != "" {
		_ = a.DB.QueryRow(ctx, `SELECT payment_mode FROM sandbox_settings WHERE operator_id=$1`, operatorID).Scan(&mode)
	}
	return mode
}

// trace: setMockPayment · R-38
func (a *App) hSetPaymentMode(r *http.Request, adm *auth.Admin) (any, error) {
	var body struct {
		Mode   string  `json:"mode"`
		Reason *string `json:"reason"`
	}
	if err := decodeBody(r, &body, true); err != nil {
		return nil, err
	}
	if body.Mode != "正常" && body.Mode != "余额不足" {
		return nil, errf(400, "INVALID", `模拟支付只能是"正常"或"余额不足"`)
	}
	if body.Reason != nil {
		if err := checkReason(*body.Reason); err != nil {
			return nil, err
		}
	}
	if adm.Scope == "" {
		return nil, errf(403, "FORBIDDEN", "模拟支付按运营商切换，只有运营商账号可以操作")
	}
	ctx := r.Context()
	before := a.paymentMode(ctx, adm.Scope)
	err := a.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO sandbox_settings (operator_id, payment_mode) VALUES ($1,$2)
			ON CONFLICT (operator_id) DO UPDATE SET payment_mode=$2`, adm.Scope, body.Mode); err != nil {
			return err
		}
		return a.audit(ctx, tx, adminActor(adm), "设置联调环境", "operator", adm.Scope,
			fmt.Sprintf("运营商 %s 联调环境模拟支付切换为\"%s\"", adm.Scope, body.Mode), body.Reason,
			map[string]any{"paymentMode": before}, map[string]any{"paymentMode": body.Mode})
	})
	if err != nil {
		return nil, err
	}
	return map[string]string{"paymentMode": body.Mode}, nil
}

type reasonBody struct {
	Reason string `json:"reason"`
}

// trace: rotateCredential · R-38；首次发放时调 fleet 置为联调中（R-42）
func (a *App) hRotate(r *http.Request, adm *auth.Admin) (any, error) {
	return a.deliverSecret(r, adm, "rotate")
}

// trace: issueCredential · R-38 R-42
func (a *App) hIssue(r *http.Request, adm *auth.Admin) (any, error) {
	return a.deliverSecret(r, adm, "issue")
}

// deliverSecret 签发或轮换联调密钥。issue 只允许第一次（密钥还没交出过），rotate 不限；
// 凭证第一次交出视为发放联调凭证，调 fleet 置为联调中，失败由定时任务重试。新密钥只在返回里出现一次。
func (a *App) deliverSecret(r *http.Request, adm *auth.Admin, mode string) (any, error) {
	var body reasonBody
	if err := decodeBody(r, &body, true); err != nil {
		return nil, err
	}
	if err := checkReason(body.Reason); err != nil {
		return nil, err
	}
	ctx := r.Context()
	brandID := r.PathValue("brandId")
	var secret string
	var cred *credRow
	var needStart bool
	err := a.inTx(ctx, func(tx pgx.Tx) error {
		in, err := a.integByBrand(ctx, tx, brandID)
		if err != nil {
			return err
		}
		if err := ownOperator(adm, in.OperatorID); err != nil {
			return err
		}
		c, err := scanCred(tx.QueryRow(ctx, `SELECT `+credCols+` FROM credentials WHERE brand_id=$1 FOR UPDATE`, brandID))
		if isNoRows(err) {
			return errf(404, "NOT_FOUND", "该品牌还没有联调凭证")
		}
		if err != nil {
			return err
		}
		if c.Status != "有效" {
			verb := "轮换"
			if mode == "issue" {
				verb = "签发"
			}
			return errf(409, "CREDENTIAL_DISABLED", "联调凭证已停用，不能"+verb+"；请联系平台接入负责人")
		}
		if mode == "issue" && c.Delivered {
			return errf(409, "CONFLICT", "联调密钥已经签发过，需要新密钥请用轮换")
		}
		_, secret = newCredential(brandID)
		now := a.now()
		needStart = !c.Delivered && in.Status == "未接入"
		if _, err := tx.Exec(ctx, `UPDATE credentials SET secret=$2, issued_at=$3, rotated_at=$3, rotated_by=$4, delivered=true WHERE brand_id=$1`,
			brandID, secret, now, adm.AccountID); err != nil {
			return err
		}
		if needStart {
			if _, err := tx.Exec(ctx, `UPDATE integrations SET pending_start=true WHERE brand_id=$1`, brandID); err != nil {
				return err
			}
		}
		cred, err = scanCred(tx.QueryRow(ctx, `SELECT `+credCols+` FROM credentials WHERE brand_id=$1`, brandID))
		if err != nil {
			return err
		}
		action, summary := "轮换联调凭证", fmt.Sprintf("品牌 %s 联调密钥轮换，旧密钥失效", brandID)
		if mode == "issue" {
			action, summary = "签发联调凭证", fmt.Sprintf("品牌 %s 首次签发联调密钥", brandID)
		}
		return a.audit(ctx, tx, adminActor(adm), action, "credential", c.AppID, summary, &body.Reason,
			map[string]any{"sandboxKeyMasked": maskKey(c.Secret)}, map[string]any{"sandboxKeyMasked": maskKey(secret)})
	})
	if err != nil {
		return nil, err
	}
	if needStart {
		// 失败不影响本次签发；定时任务会重试
		_ = a.startIntegration(ctx, brandID, adminActor(adm))
	}
	return map[string]any{"secret": secret, "credentials": cred.view()}, nil
}

// trace: restoreCredential · R-38。人确认点：只有接入负责人（acceptance.confirm）可以恢复被吊销的凭证。
func (a *App) hRestore(r *http.Request, adm *auth.Admin) (any, error) {
	var body reasonBody
	if err := decodeBody(r, &body, true); err != nil {
		return nil, err
	}
	if err := checkReason(body.Reason); err != nil {
		return nil, err
	}
	ctx := r.Context()
	brandID := r.PathValue("brandId")
	var secret string
	var cred *credRow
	var needStart bool
	err := a.inTx(ctx, func(tx pgx.Tx) error {
		in, err := a.integByBrand(ctx, tx, brandID)
		if err != nil {
			return err
		}
		if err := ownOperator(adm, in.OperatorID); err != nil {
			return err
		}
		c, err := scanCred(tx.QueryRow(ctx, `SELECT `+credCols+` FROM credentials WHERE brand_id=$1 FOR UPDATE`, brandID))
		if isNoRows(err) {
			return errf(404, "NOT_FOUND", "该品牌还没有联调凭证")
		}
		if err != nil {
			return err
		}
		if c.Status == "有效" {
			return errf(409, "ALREADY_DONE", "该凭证仍然有效，不需要恢复")
		}
		var exitedN int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM exited_operators WHERE operator_id=$1`, in.OperatorID).Scan(&exitedN); err != nil {
			return err
		}
		if in.Status == "已退出" || exitedN > 0 {
			return errf(409, "CONFLICT", "品牌或运营商已退出，联调凭证不能恢复")
		}
		_, secret = newCredential(brandID)
		now := a.now()
		needStart = !c.Delivered && in.Status == "未接入"
		if _, err := tx.Exec(ctx, `UPDATE credentials SET secret=$2, status='有效', issued_at=$3, rotated_at=$3, rotated_by=$4,
			disabled_at=NULL, disabled_by=NULL, delivered=true WHERE brand_id=$1`, brandID, secret, now, adm.AccountID); err != nil {
			return err
		}
		if needStart {
			if _, err := tx.Exec(ctx, `UPDATE integrations SET pending_start=true WHERE brand_id=$1`, brandID); err != nil {
				return err
			}
		}
		cred, err = scanCred(tx.QueryRow(ctx, `SELECT `+credCols+` FROM credentials WHERE brand_id=$1`, brandID))
		if err != nil {
			return err
		}
		return a.audit(ctx, tx, adminActor(adm), "恢复联调凭证", "credential", c.AppID,
			fmt.Sprintf("品牌 %s 联调凭证 %s 已恢复，生成新密钥", brandID, c.AppID), &body.Reason,
			map[string]any{"status": "停用"}, map[string]any{"status": "有效", "sandboxKeyMasked": maskKey(secret)})
	})
	if err != nil {
		return nil, err
	}
	if needStart {
		_ = a.startIntegration(ctx, brandID, adminActor(adm))
	}
	return map[string]any{"secret": secret, "credentials": cred.view()}, nil
}

// validateBaseURL 运营商联调地址应为 http 或 https 地址。
func validateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return errf(400, "INVALID", "运营商联调地址应为 http 或 https 地址")
	}
	return nil
}

// registerBaseURL 登记运营商联调地址并留痕；与已登记的相同则什么也不做。
func (a *App) registerBaseURL(ctx context.Context, actor Actor, in *integRow, baseURL string) error {
	if in.OperatorBaseURL != nil && *in.OperatorBaseURL == baseURL {
		return nil
	}
	return a.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE integrations SET operator_base_url=$2 WHERE brand_id=$1`, in.BrandID, baseURL); err != nil {
			return err
		}
		return a.audit(ctx, tx, actor, "登记联调地址", "integration", in.IntegrationID,
			fmt.Sprintf("品牌 %s 登记运营商联调地址", in.BrandID), nil,
			map[string]any{"operatorBaseUrl": in.OperatorBaseURL}, map[string]any{"operatorBaseUrl": baseURL})
	})
}

// trace: setOperatorBaseUrl · R-38 R-39
func (a *App) hSetOperatorBaseURL(r *http.Request, adm *auth.Admin) (any, error) {
	var body struct {
		OperatorBaseURL string `json:"operatorBaseUrl"`
	}
	if err := decodeBody(r, &body, true); err != nil {
		return nil, err
	}
	if err := validateBaseURL(body.OperatorBaseURL); err != nil {
		return nil, err
	}
	ctx := r.Context()
	in, err := a.ownedInteg(ctx, adm, r.PathValue("brandId"))
	if err != nil {
		return nil, err
	}
	if in.Status == "已退出" {
		return nil, errf(409, "CONFLICT", "品牌已退出，不能登记联调地址")
	}
	if err := a.registerBaseURL(ctx, adminActor(adm), in, body.OperatorBaseURL); err != nil {
		return nil, err
	}
	return map[string]string{"operatorBaseUrl": body.OperatorBaseURL}, nil
}

// startIntegration 调 fleet 品牌开始联调，成功后更新本地投影。
func (a *App) startIntegration(ctx context.Context, brandID string, actor Actor) error {
	res, err := a.Fleet.StartIntegration(ctx, brandID, "onboard-start-"+brandID, fleet.Actor{Kind: actor.Kind, ID: actor.ID, Name: actor.Name})
	if err != nil {
		a.logger().Warn("调 fleet 开始联调失败，稍后重试", "brand", brandID, "err", err)
		return err
	}
	return a.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE integrations SET pending_start=false WHERE brand_id=$1`, brandID); err != nil {
			return err
		}
		note := "首次发放联调凭证"
		at := res.StatusSince
		if at.IsZero() {
			at = a.now()
		}
		if err := a.setStatus(ctx, tx, brandID, res.AccessStatus, at, actor.ID, &note); err != nil {
			return err
		}
		return a.audit(ctx, tx, actor, "开始联调", "integration", "IN-"+brandID,
			fmt.Sprintf("品牌 %s 首次发放联调凭证，接入状态置为%s", brandID, res.AccessStatus), nil,
			map[string]any{"accessStatus": "未接入"}, map[string]any{"accessStatus": res.AccessStatus})
	})
}

// RetryPendingStarts 定时任务：重试未成功的开始联调。
func (a *App) RetryPendingStarts(ctx context.Context) {
	rows, err := a.DB.Query(ctx, `SELECT brand_id FROM integrations WHERE pending_start`)
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		_ = a.startIntegration(ctx, id, Actor{Kind: "系统", ID: "onboard", Name: "接入验收服务"})
	}
}

// trace: revokeCredential · R-38
func (a *App) hRevoke(r *http.Request, adm *auth.Admin) (any, error) {
	var body reasonBody
	if err := decodeBody(r, &body, true); err != nil {
		return nil, err
	}
	if err := checkReason(body.Reason); err != nil {
		return nil, err
	}
	ctx := r.Context()
	brandID := r.PathValue("brandId")
	var cred *credRow
	err := a.inTx(ctx, func(tx pgx.Tx) error {
		in, err := a.integByBrand(ctx, tx, brandID)
		if err != nil {
			return err
		}
		if err := ownOperator(adm, in.OperatorID); err != nil {
			return err
		}
		c, err := scanCred(tx.QueryRow(ctx, `SELECT `+credCols+` FROM credentials WHERE brand_id=$1 FOR UPDATE`, brandID))
		if isNoRows(err) {
			return errf(404, "NOT_FOUND", "该品牌还没有联调凭证")
		}
		if err != nil {
			return err
		}
		if c.Status != "有效" {
			return errf(409, "ALREADY_DONE", "该凭证已停用")
		}
		if _, err := tx.Exec(ctx, `UPDATE credentials SET status='停用', disabled_at=$2, disabled_by=$3 WHERE brand_id=$1`, brandID, a.now(), adm.AccountID); err != nil {
			return err
		}
		cred, err = scanCred(tx.QueryRow(ctx, `SELECT `+credCols+` FROM credentials WHERE brand_id=$1`, brandID))
		if err != nil {
			return err
		}
		return a.audit(ctx, tx, adminActor(adm), "吊销联调凭证", "credential", c.AppID,
			fmt.Sprintf("品牌 %s 联调凭证 %s 已吊销", brandID, c.AppID), &body.Reason,
			map[string]any{"status": "有效"}, map[string]any{"status": "停用"})
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"credentials": cred.view()}, nil
}

// trace: listConformanceRuns · R-39
func (a *App) hListRuns(r *http.Request, adm *auth.Admin) (any, error) {
	ctx := r.Context()
	brandID := r.URL.Query().Get("brandId")
	if brandID != "" && adm.Scope != "" {
		in, err := a.integByBrand(ctx, a.DB, brandID)
		if err == nil {
			if err := ownOperator(adm, in.OperatorID); err != nil {
				return nil, err
			}
		}
	}
	rows, err := a.DB.Query(ctx, `SELECT r.run_id FROM runs r JOIN integrations i ON i.integration_id=r.integration_id
		WHERE ($1='' OR i.operator_id=$1) AND ($2='' OR r.brand_id=$2) ORDER BY r.at DESC, r.run_id DESC`, adm.Scope, brandID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	out := []*Run{}
	for _, id := range ids {
		run, err := a.loadRun(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, nil
}
