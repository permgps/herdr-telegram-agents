package app

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// Bridge is the goroutine that carries messages between Herdr and the
// topics: screens out on blocked and done, operator prompts, keys and
// commands in. It is fed by the daemon loop through Submit so the loop
// never waits behind a screen read; both sides still share the serial
// Telegram queue. Fatal Telegram errors are reported through Fatal.
type Bridge struct {
	out  *outbound
	in   *inbound
	jobs chan any
	// fatal carries the first fatal error; the daemon reads it once.
	fatal   chan error
	dropped atomic.Int64
	log     *slog.Logger

	// CallTimeout bounds one job; tests shorten it.
	CallTimeout time.Duration
}

// NewBridge wires the outbound and inbound use cases around the registry,
// the reconciler's read model of the mapping and the screen capture.
func NewBridge(cfg domain.Config, herdr domain.HerdrGateway, tg domain.TelegramGateway,
	registry *Registry, reconciler *Reconciler, capture *Capture, clock domain.Clock, log *slog.Logger) *Bridge {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	topics := reconciler.topics()
	out := newOutbound(herdr, tg, topics, registry.Agent, capture, clock, log)
	in := newInbound(herdr, tg, topics, registry.Agent, registry.Live, out, cfg.ChatID, cfg.BotUsername, clock, log)
	return &Bridge{
		out:         out,
		in:          in,
		jobs:        make(chan any, bridgeBuffer),
		fatal:       make(chan error, 1),
		log:         log,
		CallTimeout: bridgeCallTimeout,
	}
}

// SetSettle overrides the screen and command settle delays (tests).
func (b *Bridge) SetSettle(d time.Duration) {
	b.out.deb.delay = d
	b.in.deb.delay = d
}

// Fatal delivers the first fatal Telegram error met by a job.
func (b *Bridge) Fatal() <-chan error { return b.fatal }

// Submit queues a job without blocking: an AgentEvent, a TopicMessage or a
// GeneralCommand. When the buffer is full the job is dropped with a
// warning; the next event or a resync brings the state back.
func (b *Bridge) Submit(job any) {
	switch job.(type) {
	case AgentEvent, domain.TopicMessage, domain.GeneralCommand:
	default:
		b.log.Warn("bridge job of unknown type dropped", slog.String("type", fmt.Sprintf("%T", job)))
		return
	}
	select {
	case b.jobs <- job:
	default:
		n := b.dropped.Add(1)
		b.log.Warn("bridge overflow, job dropped", slog.String("type", fmt.Sprintf("%T", job)), slog.Int64("dropped", n))
	}
}

// Dropped returns how many jobs were lost to overflow.
func (b *Bridge) Dropped() int64 { return b.dropped.Load() }

// Run serves jobs, screen settle timers and command follow-up timers until
// ctx is done. Errors never end the loop; fatal ones are reported through
// Fatal and the daemon decides.
func (b *Bridge) Run(ctx context.Context) {
	b.log.Info("bridge started")
	defer b.log.Info("bridge stopped")
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-b.jobs:
			b.handle(ctx, job)
		case key := <-b.out.Due():
			b.run(ctx, "screen", func(ctx context.Context) error { return b.out.Fire(ctx, key) })
		case key := <-b.in.Due():
			b.run(ctx, "command", func(ctx context.Context) error { return b.in.Fire(ctx, key) })
		}
	}
}

func (b *Bridge) handle(ctx context.Context, job any) {
	switch j := job.(type) {
	case AgentEvent:
		b.log.Debug("bridge job", slog.String("kind", "agent_event"), slog.String("event", string(j.Kind)), slog.String("key", j.Agent.Key.String()))
		b.out.Observe(j)
	case domain.TopicMessage:
		b.log.Debug("bridge job", slog.String("kind", "topic_message"), slog.Int("thread_id", j.ThreadID), slog.Int("message_id", j.MessageID))
		b.run(ctx, "topic_message", func(ctx context.Context) error { return b.in.HandleTopic(ctx, j) })
	case domain.GeneralCommand:
		b.log.Debug("bridge job", slog.String("kind", "general_command"), slog.Int("message_id", j.MessageID))
		b.run(ctx, "general_command", func(ctx context.Context) error { return b.in.HandleGeneral(ctx, j) })
	}
}

// run executes one job under the call timeout and applies the error policy.
func (b *Bridge) run(ctx context.Context, kind string, fn func(context.Context) error) {
	jctx, cancel := context.WithTimeout(ctx, b.CallTimeout)
	defer cancel()
	start := time.Now()
	err := fn(jctx)
	b.log.Debug("bridge job done", slog.String("kind", kind), slog.Int64("dur_ms", time.Since(start).Milliseconds()), slog.Bool("ok", err == nil))
	if err == nil {
		return
	}
	if isFatal(err) {
		select {
		case b.fatal <- err:
		default:
		}
		return
	}
	b.log.Warn("bridge job failed", slog.String("kind", kind), slog.String("err", err.Error()))
}
