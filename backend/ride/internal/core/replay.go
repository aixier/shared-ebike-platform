package core

import (
	"context"
	"encoding/json"
	"sync"

	"bike/ride/internal/rules"
)

var replayMu sync.Mutex

// Replay 恢复回放：把本地日志里的订单按订单号去重入库（新版本才覆盖）并补发事件，
// 再让日志里的运营商报告生效，最后重试未匹配的报告（R-34）。数据库仍不可用时什么也不做。
func (s *Service) Replay(ctx context.Context) (int, error) {
	replayMu.Lock()
	defer replayMu.Unlock()
	if err := s.store.pool.Ping(ctx); err != nil {
		return 0, err
	}
	recs, err := s.journal.ReadAll()
	if err != nil || len(recs) == 0 {
		return 0, err
	}
	replayed := map[string]int{}
	var reports []RideEvent
	for _, r := range recs {
		switch r.Kind {
		case "order":
			var jo journalOrder
			if json.Unmarshal(r.Data, &jo) != nil || jo.Order == nil {
				continue
			}
			if err := s.replayOrder(ctx, &jo); err != nil {
				return 0, err
			}
			replayed[jo.Order.OrderID] = jo.Order.Rev
		case "report":
			var e RideEvent
			if json.Unmarshal(r.Data, &e) == nil {
				reports = append(reports, e)
			}
		}
	}
	// 订单都入库后，内存里同版本或更旧的不再需要
	s.mu.Lock()
	for id, rev := range replayed {
		if m, ok := s.mem[id]; ok && m.Rev <= rev {
			delete(s.mem, id)
		}
	}
	s.mu.Unlock()
	for i := range reports {
		e := &reports[i]
		if _, dup, err := s.persistReport(ctx, e); err != nil {
			return 0, err
		} else if !dup {
			_, _ = s.applyReport(ctx, e)
		}
	}
	if err := s.journal.Done(len(recs)); err != nil {
		return 0, err
	}
	s.RetryUnmatchedReports(ctx)
	s.breakers.Report(rules.CompDB, nil)
	return len(recs), nil
}

// replayOrder 一条订单快照入库。唯一约束冲突（数据库恢复前别的实例已为同一辆车或同一人开了单）时，
// 不丢单：改为"待人工处理"入库并发事件，交人处理。
func (s *Service) replayOrder(ctx context.Context, jo *journalOrder) error {
	o := jo.Order
	err := s.store.saveOrderTx(ctx, o, false, true, jo.Events, vehicleLocOf(o))
	if err == nil {
		return nil
	}
	if _, ok := uniqueViolation(err); !ok {
		return err
	}
	o.Status = StManual
	evs := append(jo.Events, evPendingManual(o, []string{"锁车事实"}, s.now()))
	return s.store.saveOrderTx(ctx, o, false, true, evs, nil)
}
