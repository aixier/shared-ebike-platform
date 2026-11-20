package app

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Credential 生产凭证（opgw.yaml 的 Credential）。
type Credential struct {
	CredentialID       string   `json:"credentialId"`
	AppID              string   `json:"appId"`
	HolderType         string   `json:"holderType"`
	HolderName         string   `json:"holderName"`
	OperatorIDs        []string `json:"operatorIds"`
	OperatorBaseURL    string   `json:"operatorBaseUrl"`
	BaseURL            string   `json:"baseUrl"` // 旧字段名，值同 operatorBaseUrl
	KeyMasked          string   `json:"keyMasked"`
	IPWhitelist        []string `json:"ipWhitelist"`
	RateLimitPerMinute int      `json:"rateLimitPerMinute"`
	Status             string   `json:"status"`
	IssuedAt           string   `json:"issuedAt"`
	RotatedAt          *string  `json:"rotatedAt"`
	RotatedBy          *Actor   `json:"rotatedBy"`
	DisabledAt         *string  `json:"disabledAt"`
	DisabledBy         *Actor   `json:"disabledBy"`
	DisabledReason     *string  `json:"disabledReason"`
	LastSeenAt         *string  `json:"lastSeenAt"`

	holderID  string
	secretEnc string
	managed   bool // 管理端录入或修改过地址、白名单、名称
}

const credCols = `credential_id, app_id, holder_type, holder_id, holder_name, operator_ids, base_url, secret_enc, key_last4,
	ip_whitelist, rate_limit, status, issued_at, rotated_at, rotated_by, disabled_at, disabled_by, disabled_reason, last_seen_at, settings_managed`

func scanCred(row pgx.Row) (*Credential, error) {
	var c Credential
	var last4 string
	var issued time.Time
	var rotAt, disAt, seen *time.Time
	var rotBy, disBy []byte
	err := row.Scan(&c.CredentialID, &c.AppID, &c.HolderType, &c.holderID, &c.HolderName, &c.OperatorIDs, &c.BaseURL, &c.secretEnc, &last4,
		&c.IPWhitelist, &c.RateLimitPerMinute, &c.Status, &issued, &rotAt, &rotBy, &disAt, &disBy, &c.DisabledReason, &seen, &c.managed)
	if err != nil {
		return nil, err
	}
	c.KeyMasked = "****" + last4
	c.OperatorBaseURL = c.BaseURL
	c.IssuedAt = rfc3339(issued)
	c.RotatedAt = tptr(rotAt)
	c.DisabledAt = tptr(disAt)
	c.LastSeenAt = tptr(seen)
	c.RotatedBy = actorPtr(rotBy)
	c.DisabledBy = actorPtr(disBy)
	if c.IPWhitelist == nil {
		c.IPWhitelist = []string{}
	}
	return &c, nil
}

func tptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := rfc3339(*t)
	return &s
}

func actorPtr(b []byte) *Actor {
	if len(b) == 0 {
		return nil
	}
	var a Actor
	if json.Unmarshal(b, &a) != nil {
		return nil
	}
	return &a
}

// ---- 密钥加密存放 ----

func (a *App) aead() cipher.AEAD {
	blk, _ := aes.NewCipher(a.Cfg.SecretKey)
	g, _ := cipher.NewGCM(blk)
	return g
}

func (a *App) seal(plain []byte) string {
	g := a.aead()
	nonce := make([]byte, g.NonceSize())
	_, _ = rand.Read(nonce)
	return base64.StdEncoding.EncodeToString(g.Seal(nonce, nonce, plain, nil))
}

func (a *App) open(enc string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return nil, err
	}
	g := a.aead()
	if len(b) < g.NonceSize() {
		return nil, errors.New("密文过短")
	}
	return g.Open(nil, b[:g.NonceSize()], b[g.NonceSize():], nil)
}

func newSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return "sk_" + hex.EncodeToString(b)
}

func newAppID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "app_" + hex.EncodeToString(b)
}

// ---- 查询 ----

func (a *App) credByID(ctx context.Context, q querier, id string, forUpdate bool) (*Credential, error) {
	sql := `SELECT ` + credCols + ` FROM credentials WHERE credential_id=$1`
	if forUpdate {
		sql += ` FOR UPDATE`
	}
	c, err := scanCred(q.QueryRow(ctx, sql, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

func (a *App) credByAppID(ctx context.Context, appID string) (*Credential, error) {
	c, err := scanCred(a.DB.QueryRow(ctx, `SELECT `+credCols+` FROM credentials WHERE app_id=$1`, appID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

// credsCovering 覆盖某运营商的凭证（有效的排在前）。
func (a *App) credsCovering(ctx context.Context, q querier, operatorID string) ([]*Credential, error) {
	rows, err := q.Query(ctx, `SELECT `+credCols+` FROM credentials WHERE $1 = ANY(operator_ids) ORDER BY status='有效' DESC, credential_id`, operatorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Credential
	for rows.Next() {
		c, err := scanCred(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (c *Credential) covers(op string) bool {
	for _, o := range c.OperatorIDs {
		if o == op {
			return true
		}
	}
	return false
}

// ---- 生成、覆盖、停用（事件驱动） ----

// ensureCoverage 运营商首次已接入：没有凭证覆盖它时生成；服务商代接的并入服务商那份凭证。
func (a *App) ensureCoverage(ctx context.Context, q querier, operatorID string) error {
	if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(830303)`); err != nil { // 与管理端录入互斥
		return err
	}
	existing, err := a.credsCovering(ctx, q, operatorID)
	if err != nil || len(existing) > 0 {
		return err
	}
	holderID, holderType := operatorID, "运营商"
	if sp := a.Cfg.ProviderOf(operatorID); sp != "" {
		holderID, holderType = sp, "车联网服务商"
	}
	// 服务商已有凭证：把运营商加入覆盖范围
	var credID string
	var ops []string
	err = q.QueryRow(ctx, `SELECT credential_id, operator_ids FROM credentials WHERE holder_id=$1 FOR UPDATE`, holderID).Scan(&credID, &ops)
	if err == nil {
		after := append(append([]string{}, ops...), operatorID)
		if _, err := q.Exec(ctx, `UPDATE credentials SET operator_ids=$2 WHERE credential_id=$1`, credID, after); err != nil {
			return err
		}
		return a.audit(ctx, q, systemActor, "调整凭证范围", credID, "运营商 "+operatorID+" 首次接入，并入服务商 "+holderID+" 的生产凭证", nil,
			map[string]any{"operatorIds": ops}, map[string]any{"operatorIds": after})
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var seq int64
	if err := q.QueryRow(ctx, `SELECT nextval('credential_seq')`).Scan(&seq); err != nil {
		return err
	}
	credID = fmt.Sprintf("CRD%04d", seq)
	secret := newSecret()
	name := a.Cfg.HolderNames[holderID]
	if name == "" {
		name = holderID
	}
	wl := a.Cfg.IPWhitelists[holderID]
	if wl == nil {
		wl = []string{}
	}
	appID := newAppID()
	_, err = q.Exec(ctx, `INSERT INTO credentials (credential_id, app_id, holder_type, holder_id, holder_name, operator_ids, base_url, secret_enc, key_last4,
		ip_whitelist, rate_limit, status, issued_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'有效',$12)`,
		credID, appID, holderType, holderID, name, []string{operatorID}, a.Cfg.BaseURLs[holderID], a.seal([]byte(secret)), secret[len(secret)-4:],
		wl, a.Cfg.RateLimitPerMinute, a.Now())
	if err != nil {
		return err
	}
	return a.audit(ctx, q, systemActor, "生成生产凭证", credID, "运营商 "+operatorID+" 首次已接入，自动生成生产凭证", nil, nil,
		map[string]any{"appId": appID, "holderType": holderType, "holderId": holderID, "operatorIds": []string{operatorID}, "status": "有效"})
}

// removeCoverage 运营商退出：自有凭证停用；服务商凭证把它移出覆盖范围，名下没有运营商时停用。
func (a *App) removeCoverage(ctx context.Context, q querier, operatorID, reason string) error {
	creds, err := a.credsCovering(ctx, q, operatorID)
	if err != nil {
		return err
	}
	if reason == "" {
		reason = "运营商已退出"
	}
	for _, c := range creds {
		var remain []string
		for _, o := range c.OperatorIDs {
			if o != operatorID {
				remain = append(remain, o)
			}
		}
		if c.HolderType == "车联网服务商" && len(remain) > 0 {
			if _, err := q.Exec(ctx, `UPDATE credentials SET operator_ids=$2 WHERE credential_id=$1`, c.CredentialID, remain); err != nil {
				return err
			}
			if err := a.audit(ctx, q, systemActor, "调整凭证范围", c.CredentialID, "运营商 "+operatorID+" 已退出，移出服务商凭证的覆盖范围", &reason,
				map[string]any{"operatorIds": c.OperatorIDs}, map[string]any{"operatorIds": remain}); err != nil {
				return err
			}
			continue
		}
		if c.Status == "停用" {
			continue
		}
		if err := a.disableTx(ctx, q, c, systemActor, reason); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) disableTx(ctx context.Context, q querier, c *Credential, actor Actor, reason string) error {
	ab, _ := json.Marshal(actor)
	if _, err := q.Exec(ctx, `UPDATE credentials SET status='停用', disabled_at=$2, disabled_by=$3, disabled_reason=$4 WHERE credential_id=$1`,
		c.CredentialID, a.Now(), ab, reason); err != nil {
		return err
	}
	return a.audit(ctx, q, actor, "停用生产凭证", c.CredentialID, "停用生产凭证 "+c.AppID, &reason,
		map[string]any{"status": c.Status}, map[string]any{"status": "停用"})
}

func (a *App) onAccessChanged(ctx context.Context, q querier, brandID, operatorID, to string, reason *string) error {
	if _, err := q.Exec(ctx, `INSERT INTO brands (brand_id, operator_id, access_status, updated_at) VALUES ($1,$2,$3,$4)
		ON CONFLICT (brand_id) DO UPDATE SET operator_id=EXCLUDED.operator_id, access_status=EXCLUDED.access_status, updated_at=EXCLUDED.updated_at`,
		brandID, operatorID, to, a.Now()); err != nil {
		return err
	}
	switch to {
	case "已接入":
		return a.ensureCoverage(ctx, q, operatorID)
	case "已退出":
		// 只有该运营商名下品牌全部退出才停用
		var alive int
		if err := q.QueryRow(ctx, `SELECT count(*) FROM brands WHERE operator_id=$1 AND access_status<>'已退出'`, operatorID).Scan(&alive); err != nil {
			return err
		}
		if alive == 0 {
			r := ""
			if reason != nil {
				r = *reason
			}
			return a.removeCoverage(ctx, q, operatorID, r)
		}
	}
	return nil
}

func (a *App) onOperatorExited(ctx context.Context, q querier, operatorID string, brandIDs []string, reason string) error {
	if _, err := q.Exec(ctx, `INSERT INTO exited_operators (operator_id, exited_at, reason) VALUES ($1,$2,$3) ON CONFLICT (operator_id) DO NOTHING`,
		operatorID, a.Now(), reason); err != nil {
		return err
	}
	for _, b := range brandIDs {
		if _, err := q.Exec(ctx, `INSERT INTO brands (brand_id, operator_id, access_status, updated_at) VALUES ($1,$2,'已退出',$3)
			ON CONFLICT (brand_id) DO UPDATE SET access_status='已退出', updated_at=EXCLUDED.updated_at`, b, operatorID, a.Now()); err != nil {
			return err
		}
	}
	return a.removeCoverage(ctx, q, operatorID, reason)
}

// SyncHolderSettings 启动时按环境变量更新已有凭证的名称、地址、白名单、限流。
// 环境变量只是兜底：管理端录入或修改过的凭证（settings_managed）不再被覆盖；限流始终按配置。
func (a *App) SyncHolderSettings(ctx context.Context) error {
	for id, name := range a.Cfg.HolderNames {
		if _, err := a.DB.Exec(ctx, `UPDATE credentials SET holder_name=$2 WHERE holder_id=$1 AND NOT settings_managed`, id, name); err != nil {
			return err
		}
	}
	for id, u := range a.Cfg.BaseURLs {
		if _, err := a.DB.Exec(ctx, `UPDATE credentials SET base_url=$2 WHERE holder_id=$1 AND NOT settings_managed`, id, u); err != nil {
			return err
		}
	}
	for id, wl := range a.Cfg.IPWhitelists {
		if _, err := a.DB.Exec(ctx, `UPDATE credentials SET ip_whitelist=$2 WHERE holder_id=$1 AND NOT settings_managed`, id, wl); err != nil {
			return err
		}
	}
	_, err := a.DB.Exec(ctx, `UPDATE credentials SET rate_limit=$1`, a.Cfg.RateLimitPerMinute)
	return err
}

// ---- 管理端接口 ----

func scopeOK(c *AdminClaims, cred *Credential) bool {
	return c.Scope == nil || cred.covers(*c.Scope)
}

func (a *App) handleListCredentials(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("pageSize"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 20
	}
	op := q.Get("operatorId")
	if sc := adminFrom(r.Context()).Scope; sc != nil {
		if op != "" && op != *sc {
			writeErr(w, http.StatusForbidden, "SCOPE_DENIED", "只能查看本运营商的凭证")
			return
		}
		op = *sc
	}
	status := q.Get("status")
	where := ` WHERE ($1 = '' OR $1 = ANY(operator_ids)) AND ($2 = '' OR status = $2)`
	var total int
	if err := a.DB.QueryRow(r.Context(), `SELECT count(*) FROM credentials`+where, op, status).Scan(&total); err != nil {
		serverBusy(w)
		return
	}
	rows, err := a.DB.Query(r.Context(), `SELECT `+credCols+` FROM credentials`+where+` ORDER BY credential_id LIMIT $3 OFFSET $4`, op, status, size, (page-1)*size)
	if err != nil {
		serverBusy(w)
		return
	}
	defer rows.Close()
	out := []*Credential{}
	for rows.Next() {
		c, err := scanCred(rows)
		if err != nil {
			serverBusy(w)
			return
		}
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": out, "total": total, "page": page, "pageSize": size})
}

func (a *App) handleGetCredential(w http.ResponseWriter, r *http.Request) {
	c, err := a.credByID(r.Context(), a.DB, r.PathValue("credentialId"), false)
	if err != nil {
		serverBusy(w)
		return
	}
	if c == nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "没有找到这份凭证")
		return
	}
	if !scopeOK(adminFrom(r.Context()), c) {
		writeErr(w, http.StatusForbidden, "SCOPE_DENIED", "只能查看本运营商的凭证")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func adminActor(c *AdminClaims) Actor { return Actor{Kind: "人", ID: c.Subject, Name: c.Name} }

// reasonOf 从请求体取事由，至少 4 个字。
func reasonOf(raw []byte) (string, bool) {
	var b struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(raw, &b)
	b.Reason = strings.TrimSpace(b.Reason)
	return b.Reason, utf8.RuneCountInString(b.Reason) >= 4
}

// 响应里含密钥明文的操作，幂等记录加密保存
var sealedOps = map[string]bool{"rotate": true, "create": true}

// idemReplay 幂等键命中时重放第一次的结果；返回是否已处理。
func (a *App) idemReplay(w http.ResponseWriter, r *http.Request, op, scope, key string) bool {
	if key == "" {
		return false
	}
	hit, err := a.idemGet(r.Context(), scope, key)
	if err != nil {
		serverBusy(w)
		return true
	}
	if hit == nil {
		return false
	}
	body := hit.Body
	if sealedOps[op] && hit.Status == http.StatusOK {
		if body, err = a.open(string(hit.Body)); err != nil {
			serverBusy(w)
			return true
		}
	}
	writeRaw(w, hit.Status, body)
	return true
}

// idemStore 保存结果（事务内）；含密钥的 200 加密，400 不存（改好后可用原键重发）。
func (a *App) idemStore(ctx context.Context, tx pgx.Tx, op, scope, key string, status int, body []byte) error {
	if key == "" || status == http.StatusBadRequest {
		return nil
	}
	stored := body
	if sealedOps[op] && status == http.StatusOK {
		stored = []byte(a.seal(body))
	}
	return a.idemPut(ctx, tx, scope, key, status, stored)
}

// credWrite 轮换、停用、修改的公共流程：幂等、事由、加锁、范围检查。
func (a *App) credWrite(w http.ResponseWriter, r *http.Request, op string, do func(ctx context.Context, tx pgx.Tx, c *Credential, actor Actor, reason string, raw []byte) (int, any, error)) {
	ctx := r.Context()
	claims := adminFrom(ctx)
	id := r.PathValue("credentialId")
	key := r.Header.Get("Idempotency-Key")
	scope := op + ":" + id + ":" + claims.Subject
	if a.idemReplay(w, r, op, scope, key) {
		return
	}
	raw, _ := readBody(r)
	reason, ok := reasonOf(raw)
	if !ok {
		writeErr(w, http.StatusBadRequest, "REASON_REQUIRED", "请填写事由，至少 4 个字")
		return
	}
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		serverBusy(w)
		return
	}
	defer tx.Rollback(ctx)
	c, err := a.credByID(ctx, tx, id, true)
	if err != nil {
		serverBusy(w)
		return
	}
	if c == nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "没有找到这份凭证")
		return
	}
	if !scopeOK(claims, c) {
		writeErr(w, http.StatusForbidden, "SCOPE_DENIED", "只能操作本运营商的凭证")
		return
	}
	status, resp, err := do(ctx, tx, c, adminActor(claims), reason, raw)
	if err != nil {
		serverBusy(w)
		return
	}
	body, _ := json.Marshal(resp)
	if status >= 400 {
		// 失败不改任何数据；409 等业务结果照旧登记幂等键，400 不登记
		if err := a.idemStore(ctx, tx, op, scope, key, status, body); err != nil {
			serverBusy(w)
			return
		}
		if err := tx.Commit(ctx); err != nil {
			serverBusy(w)
			return
		}
		writeRaw(w, status, body)
		return
	}
	if err := a.idemStore(ctx, tx, op, scope, key, status, body); err != nil {
		serverBusy(w)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		serverBusy(w)
		return
	}
	writeRaw(w, status, body)
}

func (a *App) handleRotate(w http.ResponseWriter, r *http.Request) {
	a.credWrite(w, r, "rotate", func(ctx context.Context, tx pgx.Tx, c *Credential, actor Actor, reason string, _ []byte) (int, any, error) {
		if c.Status == "停用" {
			return http.StatusConflict, apiError{Code: "CREDENTIAL_DISABLED", Message: "凭证已停用，不能轮换"}, nil
		}
		secret := newSecret()
		ab, _ := json.Marshal(actor)
		if _, err := tx.Exec(ctx, `UPDATE credentials SET secret_enc=$2, key_last4=$3, rotated_at=$4, rotated_by=$5 WHERE credential_id=$1`,
			c.CredentialID, a.seal([]byte(secret)), secret[len(secret)-4:], a.Now(), ab); err != nil {
			return 0, nil, err
		}
		if err := a.audit(ctx, tx, actor, "轮换生产凭证", c.CredentialID, "轮换生产密钥 "+c.AppID+"，旧密钥立即失效", &reason,
			map[string]any{"keyMasked": c.KeyMasked}, map[string]any{"keyMasked": "****" + secret[len(secret)-4:]}); err != nil {
			return 0, nil, err
		}
		nc, err := a.credByID(ctx, tx, c.CredentialID, false)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, map[string]any{"secret": secret, "credential": nc}, nil
	})
}

func (a *App) handleDisable(w http.ResponseWriter, r *http.Request) {
	a.credWrite(w, r, "disable", func(ctx context.Context, tx pgx.Tx, c *Credential, actor Actor, reason string, _ []byte) (int, any, error) {
		if c.Status == "停用" {
			return http.StatusConflict, apiError{Code: "ALREADY_DONE", Message: "凭证已经停用"}, nil
		}
		if err := a.disableTx(ctx, tx, c, actor, reason); err != nil {
			return 0, nil, err
		}
		nc, err := a.credByID(ctx, tx, c.CredentialID, false)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, nc, nil
	})
}

// secretOf 解出凭证密钥明文。
func (a *App) secretOf(c *Credential) (string, error) {
	b, err := a.open(c.secretEnc)
	return string(b), err
}
