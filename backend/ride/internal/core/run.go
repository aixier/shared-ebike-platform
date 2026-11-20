package core

import (
	"context"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/robfig/cron/v3"
)

// 定时任务锁编号（pg_try_advisory_lock）。
const (
	lockSweepStale = 730201
	lockOverLimit  = 730202
	lockPileNoLock = 730203
	lockUnmatched  = 730204
)

// RunBackground 后台任务：熔断探测、快照刷新、本地日志回放、发件表发布、定时清理。
func (s *Service) RunBackground(ctx context.Context, js jetstream.JetStream) {
	every := func(d time.Duration, fn func()) {
		go func() {
			t := time.NewTicker(d)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					fn()
				}
			}
		}()
	}
	every(2*time.Second, func() { s.breakers.ProbeAll(ctx) })
	every(s.opt.SnapshotRefresh, func() {
		if err := s.snap.refresh(ctx, s.store, false); err != nil {
			s.log.Debug("快照刷新失败，沿用内存内容", "err", err)
		}
	})
	every(5*time.Minute, func() { _ = s.snap.refresh(ctx, s.store, true) })
	every(3*time.Second, func() {
		if s.dbOK() && s.journal.Pending() > 0 {
			if n, err := s.Replay(ctx); err != nil {
				s.log.Warn("本地日志回放失败，稍后重试", "err", err)
			} else if n > 0 {
				s.log.Info("本地日志已回放", "records", n)
			}
		}
	})
	if js != nil {
		every(500*time.Millisecond, func() {
			if s.dbOK() {
				_, _ = s.PublishOutbox(ctx, js)
			}
		})
	}
	c := cron.New(cron.WithSeconds())
	locked := func(key int64, fn func(context.Context)) func() {
		return func() {
			if !s.dbOK() {
				return
			}
			_, _ = s.store.withAdvisoryLock(ctx, key, func(c context.Context) error { fn(c); return nil })
		}
	}
	_, _ = c.AddFunc("*/5 * * * * *", locked(lockPileNoLock, s.SweepPileNoLock))
	_, _ = c.AddFunc("*/30 * * * * *", locked(lockOverLimit, s.SweepOverLimit))
	_, _ = c.AddFunc("0 * * * * *", locked(lockSweepStale, s.SweepStale))
	_, _ = c.AddFunc("15 * * * * *", locked(lockUnmatched, s.RetryUnmatchedReports))
	c.Start()
	go func() { <-ctx.Done(); c.Stop() }()
}

// LoadSnapshot 启动时全量载入准入快照。
func (s *Service) LoadSnapshot(ctx context.Context) error { return s.snap.refresh(ctx, s.store, true) }
