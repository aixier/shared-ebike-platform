package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"bike/site/internal/config"
)

// 控制器登记：管理端登记的控制器存在 controllers 表里，部署配置（PILE_DEVICES）的控制器作为兜底，
// 两者合在一起就是本服务认得的全部控制器；同名时以部署配置为准（登记时不允许重名）。

var deviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{3,64}$`)

const controllerCacheTTL = 5 * time.Second

var accessModes = map[string]bool{"MQTT": true, "HTTP": true, "厂商适配": true}

// loadControllers 读登记表里启用的控制器
func (a *App) loadControllers(ctx context.Context) (map[string]config.Device, error) {
	rows, err := a.db.Query(ctx, `SELECT device_id, coalesce(vendor,''), coalesce(station_id,''), coalesce(pile_id,''), secret_sha256 FROM controllers WHERE status='启用'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]config.Device{}
	for rows.Next() {
		var d config.Device
		if err := rows.Scan(&d.DeviceID, &d.Vendor, &d.StationID, &d.PileID, &d.SecretSHA256); err != nil {
			return nil, err
		}
		d.Registered = true
		out[d.DeviceID] = d
	}
	return out, rows.Err()
}

// registered 登记的控制器（缓存 5 秒；本进程里登记、重置、停用后立即失效）
func (a *App) registered() map[string]config.Device {
	a.ctlMu.Lock()
	defer a.ctlMu.Unlock()
	if a.ctlMap != nil && time.Since(a.ctlAt) < controllerCacheTTL {
		return a.ctlMap
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	m, err := a.loadControllers(ctx)
	if err != nil {
		a.log.Error("读取登记的控制器失败", "err", err)
		if a.ctlMap != nil {
			return a.ctlMap // 沿用旧的
		}
		return map[string]config.Device{}
	}
	a.ctlMap, a.ctlAt = m, time.Now()
	return m
}

func (a *App) invalidateControllers() {
	a.ctlMu.Lock()
	a.ctlMap = nil
	a.ctlMu.Unlock()
}

// device 按编号找控制器：部署配置优先，再查登记
func (a *App) device(id string) (config.Device, bool) {
	if d, ok := a.cfg.Devices[id]; ok {
		return d, true
	}
	d, ok := a.registered()[id]
	return d, ok
}

// allDevices 全部控制器（部署配置 + 登记）
func (a *App) allDevices() []config.Device {
	var out []config.Device
	for _, d := range a.cfg.Devices {
		out = append(out, d)
	}
	for id, d := range a.registered() {
		if _, dup := a.cfg.Devices[id]; !dup {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DeviceID < out[j].DeviceID })
	return out
}

func hashSecret(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func newSecret() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ControllerView 控制器（swagger Controller）
type ControllerView struct {
	DeviceID   string  `json:"deviceId"`
	AccessMode *string `json:"accessMode"`
	Vendor     *string `json:"vendor"`
	StationID  *string `json:"stationId"`
	PileID     *string `json:"pileId"`
	Status     string  `json:"status"`
	Source     string  `json:"source"`
	SecretTail *string `json:"secretTail"`
	CreatedAt  *string `json:"createdAt"`
	CreatedBy  *Actor  `json:"createdBy"`
	DisabledAt *string `json:"disabledAt"`
}

// ControllerWithSecret 登记与重置的响应：密钥只此一次
type ControllerWithSecret struct {
	ControllerView
	Secret string `json:"secret"`
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

const controllerCols = `device_id, access_mode, coalesce(vendor,''), coalesce(station_id,''), coalesce(pile_id,''), secret_tail, status, created_at, created_by, disabled_at`

func (a *App) scanController(r pgx.Row) (ControllerView, error) {
	var v ControllerView
	var mode, vendor, st, pile, tail string
	var created time.Time
	var by *Actor
	var disabled *time.Time
	if err := r.Scan(&v.DeviceID, &mode, &vendor, &st, &pile, &tail, &v.Status, &created, &by, &disabled); err != nil {
		return v, err
	}
	v.AccessMode, v.Vendor, v.StationID, v.PileID, v.SecretTail = &mode, strPtr(vendor), strPtr(st), strPtr(pile), &tail
	c := a.fmtTime(created)
	v.CreatedAt, v.CreatedBy, v.DisabledAt, v.Source = &c, by, a.fmtTimePtr(disabled), "登记"
	return v, nil
}

// ListControllers 控制器列表：登记的加部署配置的
func (a *App) ListControllers(ctx context.Context, stationID, status string) (map[string]any, error) {
	rows, err := a.db.Query(ctx, `SELECT `+controllerCols+` FROM controllers ORDER BY device_id`)
	if err != nil {
		return nil, err
	}
	var all []ControllerView
	for rows.Next() {
		v, err := a.scanController(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, v)
	}
	rows.Close()
	// 桩所在站点（单桩直连的控制器按桩的站点筛）
	pileStation := map[string]string{}
	prow, err := a.db.Query(ctx, `SELECT pile_id, station_id FROM piles`)
	if err != nil {
		return nil, err
	}
	for prow.Next() {
		var p, s string
		if err := prow.Scan(&p, &s); err != nil {
			prow.Close()
			return nil, err
		}
		pileStation[p] = s
	}
	prow.Close()
	for _, d := range a.cfg.Devices {
		v := ControllerView{DeviceID: d.DeviceID, Vendor: strPtr(d.Vendor), StationID: strPtr(d.StationID), PileID: strPtr(d.PileID), Status: "启用", Source: "配置"}
		if d.Vendor != "" {
			m := "厂商适配"
			v.AccessMode = &m
		}
		all = append(all, v)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].DeviceID < all[j].DeviceID })
	rowsOut := []ControllerView{}
	for _, v := range all {
		if status != "" && v.Status != status {
			continue
		}
		if stationID != "" {
			st := ""
			if v.StationID != nil {
				st = *v.StationID
			} else if v.PileID != nil {
				st = pileStation[*v.PileID]
			}
			if st != stationID {
				continue
			}
		}
		rowsOut = append(rowsOut, v)
	}
	return map[string]any{"rows": rowsOut}, nil
}

type controllerCreate struct {
	DeviceID   string `json:"deviceId"`
	AccessMode string `json:"accessMode"`
	Vendor     string `json:"vendor"`
	StationID  string `json:"stationId"`
	PileID     string `json:"pileId"`
	Reason     string `json:"reason"`
}

// controllerTarget 留痕对象：站级控制器记站点，单桩直连记桩
func controllerTarget(v ControllerView) (string, string) {
	if v.PileID != nil {
		return "pile", *v.PileID
	}
	return "station", *v.StationID
}

// CreateController 登记控制器；密钥由平台生成，只在返回值里出现一次
func (a *App) CreateController(ctx context.Context, actor Actor, in controllerCreate) (ControllerWithSecret, error) {
	reason, err := checkReason(in.Reason)
	if err != nil {
		return ControllerWithSecret{}, err
	}
	switch {
	case !deviceIDPattern.MatchString(in.DeviceID):
		return ControllerWithSecret{}, fail("INVALID", "deviceId 只能由字母、数字、下划线、短横线组成，长 3～64 位")
	case !accessModes[in.AccessMode]:
		return ControllerWithSecret{}, fail("INVALID", "accessMode 只能是 MQTT、HTTP 或 厂商适配")
	case (in.StationID == "") == (in.PileID == ""):
		return ControllerWithSecret{}, fail("INVALID", "须且只须给 stationId 或 pileId 之一")
	case in.AccessMode == "厂商适配" && in.Vendor == "":
		return ControllerWithSecret{}, fail("INVALID", "接入方式为厂商适配时须给 vendor")
	case in.AccessMode != "厂商适配" && in.Vendor != "":
		return ControllerWithSecret{}, fail("INVALID", "只有接入方式为厂商适配时才能给 vendor")
	}
	if in.Vendor != "" {
		if _, ok := a.adapters[in.Vendor]; !ok {
			return ControllerWithSecret{}, fail("INVALID", "本市没有启用厂商适配器 "+in.Vendor)
		}
	}
	if _, dup := a.cfg.Devices[in.DeviceID]; dup {
		return ControllerWithSecret{}, fail("CONFLICT", "控制器编号 "+in.DeviceID+" 已在部署配置中使用")
	}
	secret, err := newSecret()
	if err != nil {
		return ControllerWithSecret{}, err
	}
	var out ControllerWithSecret
	err = a.tx(ctx, func(tx pgx.Tx) error {
		var where, what string
		if in.StationID != "" {
			var status string
			err := tx.QueryRow(ctx, `SELECT status FROM stations WHERE station_id=$1`, in.StationID).Scan(&status)
			if errors.Is(err, pgx.ErrNoRows) {
				return fail("NOT_FOUND", "没有找到站点 "+in.StationID)
			}
			if err != nil {
				return err
			}
			if status == statusStopped {
				return fail("INVALID", "该站点已删除，不能登记控制器")
			}
			where, what = in.StationID, "站点 "
		} else {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM piles WHERE pile_id=$1`, in.PileID).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return fail("NOT_FOUND", "没有找到桩 "+in.PileID)
			}
			where, what = in.PileID, "桩 "
		}
		now := a.now()
		tail := secret[len(secret)-4:]
		_, err := tx.Exec(ctx, `INSERT INTO controllers(device_id, access_mode, vendor, station_id, pile_id, secret_sha256, secret_tail, status, created_at, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'启用',$8,$9)`,
			in.DeviceID, in.AccessMode, nullText(in.Vendor), nullText(in.StationID), nullText(in.PileID), hashSecret(secret), tail, now, jsonText(actor))
		if err != nil {
			if isUniqueViolation(err) {
				return fail("CONFLICT", "控制器编号 "+in.DeviceID+" 已登记")
			}
			return err
		}
		v, err := a.scanController(tx.QueryRow(ctx, `SELECT `+controllerCols+` FROM controllers WHERE device_id=$1`, in.DeviceID))
		if err != nil {
			return err
		}
		out = ControllerWithSecret{ControllerView: v, Secret: secret}
		tt, tid := controllerTarget(v)
		return a.audit(ctx, tx, actor, "登记控制器", tt, tid, fmt.Sprintf("%s%s登记控制器 %s（%s）", what, where, in.DeviceID, in.AccessMode), reason, nil,
			map[string]any{"deviceId": in.DeviceID, "accessMode": in.AccessMode, "vendor": nullStr(in.Vendor)})
	})
	if err != nil {
		return ControllerWithSecret{}, err
	}
	a.invalidateControllers()
	return out, nil
}

// registeredForWrite 取登记的控制器并加行锁；部署配置里的只读
func (a *App) registeredForWrite(ctx context.Context, tx pgx.Tx, id string) (ControllerView, error) {
	v, err := a.scanController(tx.QueryRow(ctx, `SELECT `+controllerCols+` FROM controllers WHERE device_id=$1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		if _, ok := a.cfg.Devices[id]; ok {
			return v, fail("CONFLICT", "控制器 "+id+" 来自部署配置，不能在管理端修改")
		}
		return v, fail("NOT_FOUND", "没有找到控制器 "+id)
	}
	return v, err
}

// ResetControllerSecret 重置密钥：旧密钥立即失效，新密钥只返回一次
func (a *App) ResetControllerSecret(ctx context.Context, actor Actor, id, reasonIn string) (ControllerWithSecret, error) {
	reason, err := checkReason(reasonIn)
	if err != nil {
		return ControllerWithSecret{}, err
	}
	secret, err := newSecret()
	if err != nil {
		return ControllerWithSecret{}, err
	}
	var out ControllerWithSecret
	err = a.tx(ctx, func(tx pgx.Tx) error {
		v, err := a.registeredForWrite(ctx, tx, id)
		if err != nil {
			return err
		}
		if v.Status != "启用" {
			return fail("CONFLICT", "控制器 "+id+" 已停用，不能重置密钥")
		}
		if _, err := tx.Exec(ctx, `UPDATE controllers SET secret_sha256=$2, secret_tail=$3 WHERE device_id=$1`, id, hashSecret(secret), secret[len(secret)-4:]); err != nil {
			return err
		}
		tail := secret[len(secret)-4:]
		v.SecretTail = &tail
		out = ControllerWithSecret{ControllerView: v, Secret: secret}
		tt, tid := controllerTarget(v)
		return a.audit(ctx, tx, actor, "重置控制器密钥", tt, tid, "重置控制器 "+id+" 的密钥", reason, nil, nil)
	})
	if err != nil {
		return ControllerWithSecret{}, err
	}
	a.invalidateControllers()
	return out, nil
}

// DisableController 停用控制器
func (a *App) DisableController(ctx context.Context, actor Actor, id, reasonIn string) (ControllerView, error) {
	reason, err := checkReason(reasonIn)
	if err != nil {
		return ControllerView{}, err
	}
	var out ControllerView
	err = a.tx(ctx, func(tx pgx.Tx) error {
		v, err := a.registeredForWrite(ctx, tx, id)
		if err != nil {
			return err
		}
		if v.Status == "停用" {
			return fail("ALREADY_DONE", "该控制器已停用")
		}
		now := a.now()
		if _, err := tx.Exec(ctx, `UPDATE controllers SET status='停用', disabled_at=$2 WHERE device_id=$1`, id, now); err != nil {
			return err
		}
		v.Status = "停用"
		v.DisabledAt = a.fmtTimePtr(&now)
		out = v
		tt, tid := controllerTarget(v)
		return a.audit(ctx, tx, actor, "停用控制器", tt, tid, "停用控制器 "+id, reason, map[string]any{"status": "启用"}, map[string]any{"status": "停用"})
	})
	if err != nil {
		return ControllerView{}, err
	}
	a.invalidateControllers()
	return out, nil
}

func nullText(s string) any {
	if s == "" {
		return nil
	}
	return s
}
