// Package bus 是本服务对 NATS JetStream 的薄封装：确保流、带消息编号发布、持久消费者订阅。
// 只做收发，不含业务判断。
package bus

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// EnsureStream 确保流存在（文件存储、按时间保留）；已存在时更新主题与保留期。
func EnsureStream(ctx context.Context, js jetstream.JetStream, name string, subjects []string, maxAge time.Duration) error {
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      name,
		Subjects:  subjects,
		Storage:   jetstream.FileStorage,
		Retention: jetstream.LimitsPolicy,
		MaxAge:    maxAge,
	})
	return err
}

// Publish 发布一条消息，消息头 Nats-Msg-Id 填事件编号（总线侧去重）。
func Publish(ctx context.Context, js jetstream.JetStream, subject, msgID string, body []byte) error {
	msg := nats.NewMsg(subject)
	msg.Data = body
	msg.Header.Set(jetstream.MsgIDHeader, msgID)
	_, err := js.PublishMsg(ctx, msg)
	return err
}

// Subscription 是一个流上的订阅：流名与过滤主题。
type Subscription struct {
	Stream  string
	Filters []string
}

// Handler 处理一条消息；返回错误则不确认，等总线重投。
type Handler func(ctx context.Context, subject string, data []byte) error

// Subscriber 为每个流建一个持久消费者，顺序处理。流不存在时定期重试，不报错退出。
type Subscriber struct {
	js      jetstream.JetStream
	durable string
	retry   time.Duration
	handle  Handler
	log     *slog.Logger

	mu     sync.Mutex
	active map[string]bool
}

// NewSubscriber 创建订阅器；durable 为消费者名（订阅方模块名）。
func NewSubscriber(js jetstream.JetStream, durable string, retry time.Duration, h Handler, log *slog.Logger) *Subscriber {
	if log == nil {
		log = slog.Default()
	}
	return &Subscriber{js: js, durable: durable, retry: retry, handle: h, log: log, active: map[string]bool{}}
}

// Active 报告某个流的消费者当前是否在工作。
func (s *Subscriber) Active(stream string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[stream]
}

func (s *Subscriber) setActive(stream string, v bool) {
	s.mu.Lock()
	s.active[stream] = v
	s.mu.Unlock()
}

// Run 启动全部订阅，阻塞到 ctx 结束。
func (s *Subscriber) Run(ctx context.Context, subs []Subscription) {
	var wg sync.WaitGroup
	for _, sub := range subs {
		wg.Add(1)
		go func(sub Subscription) {
			defer wg.Done()
			s.loop(ctx, sub)
		}(sub)
	}
	wg.Wait()
}

func (s *Subscriber) loop(ctx context.Context, sub Subscription) {
	for ctx.Err() == nil {
		err := s.consume(ctx, sub)
		s.setActive(sub.Stream, false)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, jetstream.ErrStreamNotFound) {
			s.log.Debug("流尚不存在，稍后重试", "stream", sub.Stream)
		} else if err != nil {
			s.log.Warn("订阅中断，稍后重试", "stream", sub.Stream, "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.retry):
		}
	}
}

func (s *Subscriber) consume(ctx context.Context, sub Subscription) error {
	cfg := jetstream.ConsumerConfig{
		Durable:       s.durable,
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		MaxAckPending: 1,
		AckWait:       30 * time.Second,
	}
	if len(sub.Filters) == 1 {
		cfg.FilterSubject = sub.Filters[0]
	} else {
		cfg.FilterSubjects = sub.Filters
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	cons, err := s.js.CreateOrUpdateConsumer(cctx, sub.Stream, cfg)
	cancel()
	if err != nil {
		return err
	}
	it, err := cons.Messages()
	if err != nil {
		return err
	}
	defer it.Stop()
	stop := context.AfterFunc(ctx, it.Stop)
	defer stop()
	s.setActive(sub.Stream, true)
	for {
		msg, err := it.Next()
		if err != nil {
			return err
		}
		if herr := s.handle(ctx, msg.Subject(), msg.Data()); herr != nil {
			s.log.Warn("事件处理失败，等待重投", "subject", msg.Subject(), "err", herr)
			_ = msg.NakWithDelay(s.retry)
			continue
		}
		if err := msg.Ack(); err != nil {
			s.log.Warn("确认失败", "subject", msg.Subject(), "err", err)
		}
	}
}
