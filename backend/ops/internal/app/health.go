package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"bike/ops/internal/httpx"
	"bike/ops/internal/metrics"
)

// BrandMetricPoint 品牌健康指标的一个点。
type BrandMetricPoint struct {
	At                *string  `json:"at,omitempty"`
	Date              *string  `json:"date,omitempty"`
	BrandID           string   `json:"brandId"`
	Paused            *bool    `json:"paused,omitempty"`
	UnlockSuccessRate *float64 `json:"unlockSuccessRate"`
	Availability      *float64 `json:"availability"`
}

// PlatformDaily 平台可用性日统计；没有样本的项为 null。
type PlatformDaily struct {
	Date              string   `json:"date"`
	EntryAvailability *float64 `json:"entryAvailability"`
	UnlockP90Sec      *float64 `json:"unlockP90Sec"`
	BillPushP90Sec    *float64 `json:"billPushP90Sec"`
}

// ProbeEntry 采样骑行入口可用性（GET ride /healthz），每分钟一次。
func (a *App) ProbeEntry(ctx context.Context) error {
	ok := a.Ride != nil && a.Ride.Healthy(ctx)
	at := a.now().Truncate(time.Minute)
	_, err := a.DB.Exec(ctx, `INSERT INTO entry_probes (at, ok) VALUES ($1,$2) ON CONFLICT (at) DO UPDATE SET ok = EXCLUDED.ok`, at, ok)
	return err
}

// metricBrands 需要算指标的品牌：已接入或暂停可租的品牌，加上区间内有开锁结果的品牌。
func metricBrands(ctx context.Context, tx pgx.Tx, from, to time.Time) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT brand_id FROM brands WHERE access_status IN ('已接入','暂停可租')
		UNION SELECT DISTINCT brand_id FROM orders WHERE brand_id IS NOT NULL AND
		((start_at >= $1 AND start_at < $2) OR (unlock_failed_at >= $1 AND unlock_failed_at < $2)) ORDER BY 1`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// brandWindow 区间内某品牌的开锁成功数、失败数与可用率（运营商通道可达时长占比）。
func brandWindow(ctx context.Context, tx pgx.Tx, brandID string, from, to time.Time) (succ, fail int64, avail float64, err error) {
	if err = tx.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE start_at >= $2 AND start_at < $3),
			count(*) FILTER (WHERE start_at IS NULL AND unlock_failed_at >= $2 AND unlock_failed_at < $3)
		FROM orders WHERE brand_id = $1`, brandID, from, to).Scan(&succ, &fail); err != nil {
		return
	}
	rows, err := tx.Query(ctx, `SELECT since, recovered_at FROM operator_outages WHERE $1 = ANY(brand_ids) AND since < $3 AND (recovered_at IS NULL OR recovered_at > $2)`, brandID, from, to)
	if err != nil {
		return
	}
	var outs []metrics.Interval
	for rows.Next() {
		var s time.Time
		var e *time.Time
		if err = rows.Scan(&s, &e); err != nil {
			rows.Close()
			return
		}
		iv := metrics.Interval{Start: s}
		if e != nil {
			iv.End = *e
		}
		outs = append(outs, iv)
	}
	rows.Close()
	avail = metrics.ReachableFraction(from, to, outs)
	return
}

func below(th PauseThreshold, rate *float64, avail float64) bool {
	return (th.UnlockSuccessRate > 0 && rate != nil && *rate < th.UnlockSuccessRate) || (th.Availability > 0 && avail < th.Availability)
}

// RefreshMetrics 重算上一小时与本小时的品牌小时指标、昨天与今天的品牌日指标与平台日统计。
func (a *App) RefreshMetrics(ctx context.Context) error {
	now := a.now()
	return a.tx(ctx, func(tx pgx.Tx) error {
		th, err := a.thresholds(ctx, tx)
		if err != nil {
			return err
		}
		hour := now.Truncate(time.Hour)
		for _, h := range []time.Time{hour.Add(-time.Hour), hour} {
			end := h.Add(time.Hour)
			if end.After(now) {
				end = now
			}
			brands, err := metricBrands(ctx, tx, h, end)
			if err != nil {
				return err
			}
			for _, b := range brands {
				succ, fail, avail, err := brandWindow(ctx, tx, b, h, end)
				if err != nil {
					return err
				}
				rate := metrics.Ratio(succ, succ+fail)
				if _, err := tx.Exec(ctx, `INSERT INTO brand_metrics_hourly (brand_id, at, requests, succeeded, unlock_success_rate, availability, below)
					VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (brand_id, at) DO UPDATE SET requests = EXCLUDED.requests, succeeded = EXCLUDED.succeeded,
					unlock_success_rate = EXCLUDED.unlock_success_rate, availability = EXCLUDED.availability, below = EXCLUDED.below`,
					b, h, succ+fail, succ, rate, avail, below(th.Pause, rate, avail)); err != nil {
					return err
				}
			}
		}
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, a.Loc)
		for _, d := range []time.Time{today.AddDate(0, 0, -1), today} {
			end := d.AddDate(0, 0, 1)
			if end.After(now) {
				end = now
			}
			brands, err := metricBrands(ctx, tx, d, end)
			if err != nil {
				return err
			}
			date := d.Format("2006-01-02")
			for _, b := range brands {
				succ, fail, avail, err := brandWindow(ctx, tx, b, d, end)
				if err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO brand_metrics_daily (brand_id, date, unlock_success_rate, availability) VALUES ($1,$2,$3,$4)
					ON CONFLICT (brand_id, date) DO UPDATE SET unlock_success_rate = EXCLUDED.unlock_success_rate, availability = EXCLUDED.availability`,
					b, date, metrics.Ratio(succ, succ+fail), avail); err != nil {
					return err
				}
			}
			if err := a.refreshPlatformDay(ctx, tx, date, d, end); err != nil {
				return err
			}
		}
		return nil
	})
}

func (a *App) refreshPlatformDay(ctx context.Context, tx pgx.Tx, date string, from, to time.Time) error {
	var okN, total int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE ok), count(*) FROM entry_probes WHERE at >= $1 AND at < $2`, from, to).Scan(&okN, &total); err != nil {
		return err
	}
	entry := metrics.Ratio(okN, total)
	collect := func(sql string) ([]float64, error) {
		rows, err := tx.Query(ctx, sql, from, to)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var xs []float64
		for rows.Next() {
			var v float64
			if err := rows.Scan(&v); err != nil {
				return nil, err
			}
			xs = append(xs, v)
		}
		return xs, rows.Err()
	}
	unl, err := collect(`SELECT EXTRACT(EPOCH FROM (start_at - request_at))::float8 FROM orders WHERE start_at >= $1 AND start_at < $2 AND request_at IS NOT NULL AND start_at >= request_at`)
	if err != nil {
		return err
	}
	bp, err := collect(`SELECT EXTRACT(EPOCH FROM (bill_pushed_at - order_end_at))::float8 FROM orders WHERE bill_pushed_at >= $1 AND bill_pushed_at < $2 AND order_end_at IS NOT NULL AND bill_pushed_at >= order_end_at`)
	if err != nil {
		return err
	}
	var up, bpp *float64
	if v, ok := metrics.P90(unl); ok {
		v = metrics.Round4(v)
		up = &v
	}
	if v, ok := metrics.P90(bp); ok {
		v = metrics.Round4(v)
		bpp = &v
	}
	if entry == nil && up == nil && bpp == nil {
		return nil
	}
	_, err = tx.Exec(ctx, `INSERT INTO platform_daily (date, entry_availability, unlock_p90_sec, bill_push_p90_sec) VALUES ($1,$2,$3,$4)
		ON CONFLICT (date) DO UPDATE SET entry_availability = EXCLUDED.entry_availability, unlock_p90_sec = EXCLUDED.unlock_p90_sec,
		bill_push_p90_sec = EXCLUDED.bill_push_p90_sec`, date, entry, up, bpp)
	return err
}

// BrandHealth 某品牌当前健康情况（运行智能体的"读健康指标"工具也用它）。
type BrandHealth struct {
	BrandID        string            `json:"brandId"`
	AccessStatus   string            `json:"accessStatus"`
	Latest         *BrandMetricPoint `json:"latest"`
	Below          bool              `json:"below"`
	BelowSince     *string           `json:"belowSince"`
	SustainMinutes int               `json:"sustainMinutes"`
	// LowMetric 当前低于门槛的指标（开锁成功率优先），不低于时为空
	LowMetric      string   `json:"lowMetric"`
	LowValue       *float64 `json:"lowValue"`
	LowThreshold   *float64 `json:"lowThreshold"`
	OperatorDown   bool     `json:"operatorDown"`
	OpenDependency []string `json:"openDependency"`
}

// brandHealth 由小时序列算出持续低于门槛的起点与分钟数。
func (a *App) brandHealth(ctx context.Context, tx pgx.Tx, brandID string, th Thresholds, now time.Time) (BrandHealth, error) {
	h := BrandHealth{BrandID: brandID, OpenDependency: []string{}}
	var access *string
	_ = tx.QueryRow(ctx, `SELECT access_status FROM brands WHERE brand_id = $1`, brandID).Scan(&access)
	if access != nil {
		h.AccessStatus = *access
	}
	rows, err := tx.Query(ctx, `SELECT at, unlock_success_rate, availability, below FROM brand_metrics_hourly WHERE brand_id = $1 ORDER BY at DESC LIMIT 720`, brandID)
	if err != nil {
		return h, err
	}
	var since *time.Time
	first := true
	for rows.Next() {
		var at time.Time
		var rate, avail *float64
		var b bool
		if err := rows.Scan(&at, &rate, &avail, &b); err != nil {
			rows.Close()
			return h, err
		}
		if first {
			s := a.fmtTime(at)
			bb := b
			h.Latest = &BrandMetricPoint{At: &s, BrandID: brandID, Paused: &bb, UnlockSuccessRate: rate, Availability: avail}
			h.Below = b
			first = false
			if b {
				switch {
				case th.Pause.UnlockSuccessRate > 0 && rate != nil && *rate < th.Pause.UnlockSuccessRate:
					h.LowMetric, h.LowValue = "开锁成功率", rate
					v := th.Pause.UnlockSuccessRate
					h.LowThreshold = &v
				case avail != nil:
					h.LowMetric, h.LowValue = "可用率", avail
					v := th.Pause.Availability
					h.LowThreshold = &v
				}
			}
		}
		if !b {
			break
		}
		t := at
		since = &t
	}
	rows.Close()
	if h.Below && since != nil {
		h.BelowSince = a.fmtTimePtr(since)
		h.SustainMinutes = int(now.Sub(*since).Minutes())
	}
	var down int
	_ = tx.QueryRow(ctx, `SELECT count(*) FROM operator_outages WHERE $1 = ANY(brand_ids) AND recovered_at IS NULL`, brandID).Scan(&down)
	h.OperatorDown = down > 0
	drows, err := tx.Query(ctx, `SELECT DISTINCT component FROM dependency_states WHERE state = '熔断' ORDER BY 1`)
	if err != nil {
		return h, err
	}
	for drows.Next() {
		var c string
		_ = drows.Scan(&c)
		h.OpenDependency = append(h.OpenDependency, c)
	}
	drows.Close()
	return h, nil
}

// CheckBrandHealth 品牌持续低于门槛时告警（运行智能体未接入：只告警，由运维人工暂停）；
// 指标恢复后：待确认建议失效，因指标暂停的品牌调 fleet 恢复可租。
func (a *App) CheckBrandHealth(ctx context.Context) error {
	now := a.now()
	type resume struct {
		brandID, pauseID, metric string
	}
	var resumes []resume
	err := a.tx(ctx, func(tx pgx.Tx) error {
		th, err := a.thresholds(ctx, tx)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT brand_id FROM brands WHERE access_status IN ('已接入','暂停可租') ORDER BY brand_id`)
		if err != nil {
			return err
		}
		var brands []string
		for rows.Next() {
			var b string
			_ = rows.Scan(&b)
			brands = append(brands, b)
		}
		rows.Close()
		for _, b := range brands {
			h, err := a.brandHealth(ctx, tx, b, th, now)
			if err != nil {
				return err
			}
			if h.Latest == nil {
				continue
			}
			if h.Below {
				since, _ := time.Parse(time.RFC3339, *h.BelowSince)
				if _, err := tx.Exec(ctx, `INSERT INTO brand_low_state (brand_id, since) VALUES ($1,$2)
					ON CONFLICT (brand_id) DO UPDATE SET since = EXCLUDED.since WHERE brand_low_state.alerted_at IS NULL`, b, since); err != nil {
					return err
				}
				var alertedAt *time.Time
				_ = tx.QueryRow(ctx, `SELECT alerted_at FROM brand_low_state WHERE brand_id = $1`, b).Scan(&alertedAt)
				if alertedAt == nil && h.AccessStatus == "已接入" && th.Pause.SustainMinutes > 0 && h.SustainMinutes >= th.Pause.SustainMinutes && h.LowValue != nil {
					bid := b
					text := fmt.Sprintf("%s%s %.1f%%（门槛 %.1f%%）已持续 %d 分钟低于门槛；运行智能体未接入，请运维判断是品牌自身问题还是公共问题，再决定是否暂停可租",
						a.brandName(ctx, tx, b), h.LowMetric, *h.LowValue*100, *h.LowThreshold*100, h.SustainMinutes)
					if err := a.raiseAlert(ctx, tx, "警告", AlertBrandBelow, text, &bid, nil, ""); err != nil {
						return err
					}
					if _, err := tx.Exec(ctx, `UPDATE brand_low_state SET alerted_at = $2 WHERE brand_id = $1`, b, now); err != nil {
						return err
					}
				}
				continue
			}
			// 已恢复到门槛以上
			if _, err := tx.Exec(ctx, `DELETE FROM brand_low_state WHERE brand_id = $1`, b); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE suspend_suggestions SET status = '已失效', decided_by = $2, decided_by_name = $3, decided_at = $4,
				decision_reason = '指标已恢复到门槛以上' WHERE brand_id = $1 AND status = '待确认'`, b, SystemActor.ID, SystemActor.Name, now); err != nil {
				return err
			}
			if h.AccessStatus != "暂停可租" {
				continue
			}
			var pid, metric string
			err = tx.QueryRow(ctx, `SELECT id, metric FROM pauses WHERE brand_id = $1 AND resumed_at IS NULL ORDER BY paused_at DESC LIMIT 1`, b).Scan(&pid, &metric)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			// 只自动恢复因可用率暂停的。因开锁成功率暂停的不得自动恢复（例外条款）：
			// 暂停期间没有订单时开锁成功率为空，不提示；指标回升到门槛以上只提示一次，由运维人工恢复。
			if !autoResumeFor(metric) {
				if metric == "开锁成功率" && h.Latest.UnlockSuccessRate != nil {
					bid := b
					text := fmt.Sprintf("%s开锁成功率已回升到门槛以上；因开锁成功率暂停的品牌不会自动恢复，请运维确认后人工恢复可租", a.brandName(ctx, tx, b))
					if err := a.raiseAlert(ctx, tx, "提示", AlertBrandRecovered, text, &bid, nil, "brand-recovered:"+pid); err != nil {
						return err
					}
				}
				continue
			}
			resumes = append(resumes, resume{b, pid, metric})
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, r := range resumes {
		if _, err := a.resumeBrand(ctx, SystemActor, r.brandID, "指标恢复到门槛以上，自动回到已接入", derivedKey("auto-resume", r.pauseID)); err != nil {
			slog.Warn("自动恢复可租失败", "brandId", r.brandID, "err", err)
		}
	}
	return nil
}

// ===== 监控视图 =====

// MonitorBrand 有指标的品牌。
type MonitorBrand struct {
	BrandID string            `json:"brandId"`
	Latest  *BrandMetricPoint `json:"latest"`
	Pause   *Pause            `json:"pause"`
}

// Today 今日开锁情况。
type Today struct {
	Requests          int      `json:"requests"`
	UnlockFailed      int      `json:"unlockFailed"`
	UnlockSuccessRate *float64 `json:"unlockSuccessRate"`
	Riding            int      `json:"riding"`
}

// Monitor 运行监控（完整版）。
type Monitor struct {
	AsOf               string              `json:"asOf"`
	Degrade            DegradeStatus       `json:"degrade"`
	Brands             []MonitorBrand      `json:"brands"`
	Hourly             []BrandMetricPoint  `json:"hourly"`
	Daily              []BrandMetricPoint  `json:"daily"`
	PlatformDaily      []PlatformDaily     `json:"platformDaily"`
	Today              Today               `json:"today"`
	Thresholds         Thresholds          `json:"thresholds"`
	Alerts             []Alert             `json:"alerts"`
	SuspendSuggestions []SuspendSuggestion `json:"suspendSuggestions"`
	AutoPauseByAgent   bool                `json:"autoPauseByAgent"`
}

func (a *App) hourly(ctx context.Context, tx pgx.Tx, since time.Time, brandFilter []string) ([]BrandMetricPoint, error) {
	rows, err := tx.Query(ctx, `SELECT brand_id, at, unlock_success_rate, availability, below FROM brand_metrics_hourly
		WHERE at >= $1 AND ($2::text[] IS NULL OR brand_id = ANY($2)) ORDER BY at, brand_id`, since, brandFilter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BrandMetricPoint{}
	for rows.Next() {
		var p BrandMetricPoint
		var at time.Time
		var b bool
		if err := rows.Scan(&p.BrandID, &at, &p.UnlockSuccessRate, &p.Availability, &b); err != nil {
			return nil, err
		}
		s := a.fmtTime(at)
		p.At, p.Paused = &s, &b
		out = append(out, p)
	}
	return out, rows.Err()
}

// Monitor 运行监控。
func (a *App) Monitor(ctx context.Context, hours, days int) (Monitor, error) {
	now := a.now()
	m := Monitor{AsOf: a.fmtTime(now)}
	err := a.read(ctx, func(tx pgx.Tx) error {
		var err error
		if m.Degrade, err = a.status(ctx, tx); err != nil {
			return err
		}
		if m.Thresholds, err = a.thresholds(ctx, tx); err != nil {
			return err
		}
		if m.Hourly, err = a.hourly(ctx, tx, now.Add(-time.Duration(hours)*time.Hour), nil); err != nil {
			return err
		}
		// 有指标的品牌与最近一次小时指标
		rows, err := tx.Query(ctx, `SELECT DISTINCT ON (brand_id) brand_id, at, unlock_success_rate, availability, below
			FROM brand_metrics_hourly ORDER BY brand_id, at DESC`)
		if err != nil {
			return err
		}
		m.Brands = []MonitorBrand{}
		for rows.Next() {
			var p BrandMetricPoint
			var at time.Time
			var b bool
			if err := rows.Scan(&p.BrandID, &at, &p.UnlockSuccessRate, &p.Availability, &b); err != nil {
				rows.Close()
				return err
			}
			s := a.fmtTime(at)
			p.At, p.Paused = &s, &b
			pp := p
			m.Brands = append(m.Brands, MonitorBrand{BrandID: p.BrandID, Latest: &pp})
		}
		rows.Close()
		for i := range m.Brands {
			ps, err := a.queryPauses(ctx, tx, `WHERE brand_id = $1 AND resumed_at IS NULL ORDER BY paused_at DESC LIMIT 1`, m.Brands[i].BrandID)
			if err != nil {
				return err
			}
			if len(ps) > 0 {
				m.Brands[i].Pause = &ps[0]
			}
		}
		startDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, a.Loc).AddDate(0, 0, -(days - 1))
		drows, err := tx.Query(ctx, `SELECT brand_id, date, unlock_success_rate, availability FROM brand_metrics_daily WHERE date >= $1 ORDER BY date, brand_id`, startDay.Format("2006-01-02"))
		if err != nil {
			return err
		}
		m.Daily = []BrandMetricPoint{}
		for drows.Next() {
			var p BrandMetricPoint
			var d time.Time
			if err := drows.Scan(&p.BrandID, &d, &p.UnlockSuccessRate, &p.Availability); err != nil {
				drows.Close()
				return err
			}
			s := d.Format("2006-01-02")
			p.Date = &s
			m.Daily = append(m.Daily, p)
		}
		drows.Close()
		prows, err := tx.Query(ctx, `SELECT date, entry_availability, unlock_p90_sec, bill_push_p90_sec FROM platform_daily WHERE date >= $1 ORDER BY date`, startDay.Format("2006-01-02"))
		if err != nil {
			return err
		}
		m.PlatformDaily = []PlatformDaily{}
		for prows.Next() {
			var p PlatformDaily
			var d time.Time
			if err := prows.Scan(&d, &p.EntryAvailability, &p.UnlockP90Sec, &p.BillPushP90Sec); err != nil {
				prows.Close()
				return err
			}
			p.Date = d.Format("2006-01-02")
			m.PlatformDaily = append(m.PlatformDaily, p)
		}
		prows.Close()
		dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, a.Loc)
		if err := tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE status = '开锁失败') FROM orders WHERE request_at >= $1 AND request_at < $2`,
			dayStart, dayStart.AddDate(0, 0, 1)).Scan(&m.Today.Requests, &m.Today.UnlockFailed); err != nil {
			return err
		}
		m.Today.UnlockSuccessRate = metrics.Ratio(int64(m.Today.Requests-m.Today.UnlockFailed), int64(m.Today.Requests))
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM orders WHERE status = '骑行中'`).Scan(&m.Today.Riding); err != nil {
			return err
		}
		if m.Alerts, err = a.unackedAlerts(ctx, tx, 20); err != nil {
			return err
		}
		if m.SuspendSuggestions, err = a.querySuggestions(ctx, tx, `WHERE status = '待确认' ORDER BY at DESC`); err != nil {
			return err
		}
		m.AutoPauseByAgent, err = a.autoPause(ctx, tx)
		return err
	})
	return m, err
}

// LimitedBrand 精简版品牌。
type LimitedBrand struct {
	BrandID string `json:"brandId"`
	Latest  *struct {
		At                string  `json:"at"`
		UnlockSuccessRate float64 `json:"unlockSuccessRate"`
	} `json:"latest"`
}

// LimitedPoint 精简版小时点。
type LimitedPoint struct {
	At                string   `json:"at"`
	BrandID           string   `json:"brandId"`
	UnlockSuccessRate *float64 `json:"unlockSuccessRate"`
}

// MonitorLimited 主管部门精简版。
type MonitorLimited struct {
	AsOf    string         `json:"asOf"`
	Limited bool           `json:"limited"`
	Brands  []LimitedBrand `json:"brands"`
	Hourly  []LimitedPoint `json:"hourly"`
}

// latestRates 各品牌最近一次非空开锁成功率。
func (a *App) latestRates(ctx context.Context, tx pgx.Tx) (map[string]struct {
	at   time.Time
	rate float64
}, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (brand_id) brand_id, at, unlock_success_rate FROM brand_metrics_hourly
		WHERE unlock_success_rate IS NOT NULL ORDER BY brand_id, at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct {
		at   time.Time
		rate float64
	}{}
	for rows.Next() {
		var b string
		var at time.Time
		var r float64
		if err := rows.Scan(&b, &at, &r); err != nil {
			return nil, err
		}
		out[b] = struct {
			at   time.Time
			rate float64
		}{at, r}
	}
	return out, rows.Err()
}

// scopeBrands 数据范围内的品牌：scope 为空时返回 nil（不限）。
func (a *App) scopeBrands(ctx context.Context, tx pgx.Tx, scope string) ([]string, error) {
	if scope == "" {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT brand_id FROM brands WHERE operator_id = $1 ORDER BY brand_id`, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var b string
		_ = rows.Scan(&b)
		out = append(out, b)
	}
	return out, rows.Err()
}

// MonitorLimitedView 各品牌最近开锁成功率与小时序列。
func (a *App) MonitorLimitedView(ctx context.Context, scope string, hours int) (MonitorLimited, error) {
	now := a.now()
	out := MonitorLimited{AsOf: a.fmtTime(now), Limited: true, Brands: []LimitedBrand{}, Hourly: []LimitedPoint{}}
	err := a.read(ctx, func(tx pgx.Tx) error {
		inScope, err := a.scopeBrands(ctx, tx, scope)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT brand_id FROM brands WHERE access_status IN ('已接入','暂停可租')
			UNION SELECT DISTINCT brand_id FROM brand_metrics_hourly ORDER BY 1`)
		if err != nil {
			return err
		}
		var brands []string
		for rows.Next() {
			var b string
			_ = rows.Scan(&b)
			if inScope == nil || contains(inScope, b) {
				brands = append(brands, b)
			}
		}
		rows.Close()
		latest, err := a.latestRates(ctx, tx)
		if err != nil {
			return err
		}
		for _, b := range brands {
			lb := LimitedBrand{BrandID: b}
			if l, ok := latest[b]; ok {
				lb.Latest = &struct {
					At                string  `json:"at"`
					UnlockSuccessRate float64 `json:"unlockSuccessRate"`
				}{a.fmtTime(l.at), l.rate}
			}
			out.Brands = append(out.Brands, lb)
		}
		pts, err := a.hourly(ctx, tx, now.Add(-time.Duration(hours)*time.Hour), inScope)
		if err != nil {
			return err
		}
		for _, p := range pts {
			out.Hourly = append(out.Hourly, LimitedPoint{At: *p.At, BrandID: p.BrandID, UnlockSuccessRate: p.UnlockSuccessRate})
		}
		return nil
	})
	return out, err
}

// BrandUnlockRate 各品牌最近开锁成功率。
type BrandUnlockRate struct {
	BrandID           string   `json:"brandId"`
	UnlockSuccessRate *float64 `json:"unlockSuccessRate"`
	UnlockRateAt      *string  `json:"unlockRateAt"`
}

// BrandUnlockRates 运营商账号只能查本运营商品牌，越界返回 SCOPE_DENIED。
func (a *App) BrandUnlockRates(ctx context.Context, scope string, brandIDs []string) ([]BrandUnlockRate, error) {
	out := []BrandUnlockRate{}
	err := a.read(ctx, func(tx pgx.Tx) error {
		inScope, err := a.scopeBrands(ctx, tx, scope)
		if err != nil {
			return err
		}
		ids := brandIDs
		if len(ids) == 0 {
			if inScope != nil {
				ids = inScope
			} else {
				rows, err := tx.Query(ctx, `SELECT brand_id FROM brands UNION SELECT DISTINCT brand_id FROM brand_metrics_hourly ORDER BY 1`)
				if err != nil {
					return err
				}
				for rows.Next() {
					var b string
					_ = rows.Scan(&b)
					ids = append(ids, b)
				}
				rows.Close()
			}
		} else if inScope != nil {
			for _, b := range ids {
				if !contains(inScope, b) {
					return httpx.New(403, "SCOPE_DENIED", "只能查看本运营商的品牌")
				}
			}
		}
		latest, err := a.latestRates(ctx, tx)
		if err != nil {
			return err
		}
		for _, b := range ids {
			r := BrandUnlockRate{BrandID: b}
			if l, ok := latest[b]; ok {
				v := l.rate
				s := a.fmtTime(l.at)
				r.UnlockSuccessRate, r.UnlockRateAt = &v, &s
			}
			out = append(out, r)
		}
		return nil
	})
	return out, err
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

