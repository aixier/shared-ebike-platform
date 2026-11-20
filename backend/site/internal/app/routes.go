package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"bike/site/internal/pilemsg"
)

// Handler 全部 HTTP 入口（contracts/openapi/site.yaml 与桩接入规范第 4 节）
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]bool{"ok": true}) })

	// 页面
	mux.HandleFunc("GET /api/site/map", a.handle(func(r *http.Request) (any, error) { return a.StationMap(r.Context()) }))
	listPerms := []string{"station.edit", "pile.edit", "ops.monitor"}
	mux.HandleFunc("GET /api/site/admin/stations", a.adminOnly(listPerms, false, a.handle(a.hListStations)))
	mux.HandleFunc("POST /api/site/admin/stations", a.adminOnly([]string{"station.edit"}, true, a.handle(func(r *http.Request) (any, error) {
		var in stationCreate
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return a.CreateStation(r.Context(), actorOf(adminFrom(r.Context())), in)
	})))
	mux.HandleFunc("GET /api/site/admin/stations/all", a.adminOnly(nil, false, a.handle(func(r *http.Request) (any, error) { return a.AllStations(r.Context()) })))
	mux.HandleFunc("GET /api/site/admin/stations/{stationId}", a.adminOnly(nil, false, a.handle(func(r *http.Request) (any, error) {
		return a.GetStation(r.Context(), r.PathValue("stationId"))
	})))
	mux.HandleFunc("PATCH /api/site/admin/stations/{stationId}", a.adminOnly([]string{"station.edit"}, true, a.handle(func(r *http.Request) (any, error) {
		var in stationUpdate
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return a.UpdateStation(r.Context(), actorOf(adminFrom(r.Context())), r.PathValue("stationId"), in)
	})))
	mux.HandleFunc("POST /api/site/admin/stations/{stationId}/remove", a.adminOnly([]string{"station.edit"}, true, a.handle(func(r *http.Request) (any, error) {
		var in struct {
			Reason string `json:"reason"`
		}
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return a.RemoveStation(r.Context(), actorOf(adminFrom(r.Context())), r.PathValue("stationId"), in.Reason)
	})))
	mux.HandleFunc("GET /api/site/admin/piles", a.adminOnly([]string{"pile.edit", "station.edit", "ops.monitor"}, false, a.handle(a.hListPiles)))
	mux.HandleFunc("POST /api/site/admin/piles", a.adminOnly([]string{"pile.edit"}, true, a.handle(func(r *http.Request) (any, error) {
		var in pileCreate
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return a.CreatePile(r.Context(), actorOf(adminFrom(r.Context())), in)
	})))
	mux.HandleFunc("POST /api/site/admin/piles/{pileId}/status", a.adminOnly([]string{"pile.edit"}, true, a.handle(func(r *http.Request) (any, error) {
		var in pileStatusUpdate
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return a.SetPileStatus(r.Context(), actorOf(adminFrom(r.Context())), r.PathValue("pileId"), in)
	})))

	// 控制器登记（响应里有一次性密钥，不做幂等重放）
	mux.HandleFunc("GET /api/site/admin/controllers", a.adminOnly(listPerms, false, a.handle(func(r *http.Request) (any, error) {
		st := r.URL.Query().Get("status")
		if st != "" && st != "启用" && st != "停用" {
			return nil, fail("INVALID", "status 只能是启用或停用")
		}
		return a.ListControllers(r.Context(), r.URL.Query().Get("stationId"), st)
	})))
	mux.HandleFunc("POST /api/site/admin/controllers", a.adminGuard([]string{"pile.edit"}, true, true, a.handle(func(r *http.Request) (any, error) {
		var in controllerCreate
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return a.CreateController(r.Context(), actorOf(adminFrom(r.Context())), in)
	})))
	mux.HandleFunc("POST /api/site/admin/controllers/{deviceId}/reset-secret", a.adminGuard([]string{"pile.edit"}, true, true, a.handle(func(r *http.Request) (any, error) {
		var in struct {
			Reason string `json:"reason"`
		}
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return a.ResetControllerSecret(r.Context(), actorOf(adminFrom(r.Context())), r.PathValue("deviceId"), in.Reason)
	})))
	mux.HandleFunc("POST /api/site/admin/controllers/{deviceId}/disable", a.adminOnly([]string{"pile.edit"}, true, a.handle(func(r *http.Request) (any, error) {
		var in struct {
			Reason string `json:"reason"`
		}
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return a.DisableController(r.Context(), actorOf(adminFrom(r.Context())), r.PathValue("deviceId"), in.Reason)
	})))

	// 桩设备（按本市配置启用 HTTP 接入）
	if a.cfg.AccessHTTP {
		for _, k := range []pilemsg.Kind{pilemsg.KindOccupancy, pilemsg.KindHeartbeat, pilemsg.KindSnapshot} {
			mux.HandleFunc("POST /device/v1/"+string(k), a.handle(a.hStandard(k)))
		}
		mux.HandleFunc("POST /device/v1/pile-presence", a.handle(func(r *http.Request) (any, error) {
			var in legacyPresence
			if err := decode(r, &in); err != nil {
				return nil, err
			}
			return a.IngestLegacyPresence(r.Context(), r.Header.Get("X-Device-Key"), in)
		}))
		mux.HandleFunc("POST /device/v1/pile-heartbeat", a.handle(func(r *http.Request) (any, error) {
			raw, err := readBody(r)
			if err != nil {
				return nil, err
			}
			var in legacyHeartbeat
			var probe map[string]json.RawMessage
			if json.Unmarshal(raw, &in) != nil || json.Unmarshal(raw, &probe) != nil {
				return nil, fail("INVALID", "请求体不是合法的 JSON")
			}
			_, in.hasOccupied = probe["occupiedPileTag"]
			return a.IngestLegacyHeartbeat(r.Context(), r.Header.Get("X-Device-Key"), in)
		}))
		mux.HandleFunc("POST /device/v1/vendor/{vendor}", a.handle(a.hVendor))
	}
	return mux
}

// handle 统一输出：成功 200 + JSON，失败按 Error 结构
func (a *App) handle(fn func(*http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v, err := fn(r)
		if err != nil {
			a.writeErr(w, err)
			return
		}
		writeJSON(w, 200, v)
	}
}

// parsePage 分页与排序参数
func parsePage(r *http.Request) (pageQuery, error) {
	q := pageQuery{Page: 1, PageSize: 20, SortKey: r.URL.Query().Get("sortKey")}
	if v := r.URL.Query().Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return q, fail("INVALID", "page 应为不小于 1 的整数")
		}
		q.Page = n
	}
	if v := r.URL.Query().Get("pageSize"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			return q, fail("INVALID", "pageSize 应在 1～200 之间")
		}
		q.PageSize = n
	}
	switch r.URL.Query().Get("sortDir") {
	case "", "asc":
	case "desc":
		q.Desc = true
	default:
		return q, fail("INVALID", "sortDir 只能是 asc 或 desc")
	}
	return q, nil
}

func (a *App) hListStations(r *http.Request) (any, error) {
	q, err := parsePage(r)
	if err != nil {
		return nil, err
	}
	v := r.URL.Query()
	f := stationFilter{District: v.Get("district"), Type: v.Get("type"), Q: v.Get("q")}
	if f.Type != "" && f.Type != "有桩" && f.Type != "无桩" {
		return nil, fail("INVALID", "type 只能是有桩或无桩")
	}
	if s := v.Get("includeRemoved"); s != "" {
		b, err := strconv.ParseBool(s)
		if err != nil {
			return nil, fail("INVALID", "includeRemoved 应为 true 或 false")
		}
		f.IncludeRemoved = b
	}
	return a.ListStations(r.Context(), f, q)
}

func (a *App) hListPiles(r *http.Request) (any, error) {
	q, err := parsePage(r)
	if err != nil {
		return nil, err
	}
	st := r.URL.Query().Get("status")
	if st != "" && st != "在线" && st != "离线" && st != "故障" {
		return nil, fail("INVALID", "status 只能是在线、离线或故障")
	}
	return a.ListPiles(r.Context(), r.URL.Query().Get("stationId"), st, q)
}

// hStandard 标准桩消息的 HTTP 入口：/device/v1/occupancy｜heartbeat｜snapshot
func (a *App) hStandard(kind pilemsg.Kind) func(*http.Request) (any, error) {
	return func(r *http.Request) (any, error) {
		raw, err := readBody(r)
		if err != nil {
			return nil, err
		}
		m, err := pilemsg.Parse(kind, raw)
		if err != nil {
			return nil, fail("INVALID", err.Error())
		}
		dev, ok := a.device(m.DeviceID)
		if !ok || !a.deviceKeyOK(dev, r.Header.Get("X-Device-Key")) {
			return nil, fail("DEVICE_UNAUTHORIZED", "")
		}
		return a.ingestReply(r, m)
	}
}

func (a *App) ingestReply(r *http.Request, m pilemsg.Message) (any, error) {
	res, err := a.Ingest(r.Context(), m)
	if errors.Is(err, errUnknownDevice) {
		return nil, fail("DEVICE_UNAUTHORIZED", "")
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"received": true, "duplicate": res.Duplicate, "recognized": res.Recognized, "unknownSlots": res.UnknownSlots, "serverTime": a.fmtTime(a.now())}, nil
}

// hVendor 厂商云平台 HTTP 回调：/device/v1/vendor/{厂商}，由该厂商适配器转成标准桩消息
func (a *App) hVendor(r *http.Request) (any, error) {
	vendor := r.PathValue("vendor")
	ad, ok := a.adapters[vendor]
	if !ok {
		return nil, fail("NOT_FOUND", "本市没有启用该厂商的接入")
	}
	raw, err := readBody(r)
	if err != nil {
		return nil, err
	}
	ms, err := ad.TranslateHTTP(raw)
	if err != nil {
		return nil, fail("INVALID", err.Error())
	}
	key := r.Header.Get("X-Device-Key")
	var last any
	for _, m := range ms {
		dev, ok := a.device(m.DeviceID)
		if !ok || dev.Vendor != vendor || !a.deviceKeyOK(dev, key) {
			return nil, fail("DEVICE_UNAUTHORIZED", "")
		}
		if last, err = a.ingestReply(r, m); err != nil {
			return nil, err
		}
	}
	if last == nil {
		last = map[string]any{"received": true, "serverTime": a.fmtTime(a.now())}
	}
	return last, nil
}
