package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
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
	// runCtx is the context of Run; spawn derives its goroutines from it
	// and wg counts them so Run returns only when they are done.
	runCtx context.Context
	wg     sync.WaitGroup

	// CallTimeout bounds one job; tests shorten it.
	CallTimeout time.Duration
}

// NewBridge wires the outbound and inbound use cases around the registry,
// the reconciler's read model of the mapping and the screen capture.
// replies supplies the agent's last reply for the done post when the
// posts.done option asks for it; nil keeps every post screen-based.
func NewBridge(cfg domain.Config, herdr domain.HerdrGateway, tg domain.TelegramGateway,
	registry *Registry, reconciler *Reconciler, capture *Capture, opts *Options, replies domain.ReplySource, clock domain.Clock, log *slog.Logger) *Bridge {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if opts == nil {
		opts = NewOptions(domain.DefaultOptions(), nil, nil, log)
	}
	topics := reconciler.topics()
	// Every post of the bridge passes the redactor; the reconciler keeps
	// the raw gateway because topic names are agent labels.
	tg = newRedactingGateway(tg, domain.NewRedactor(cfg.BotToken), opts.RedactEnabled, log)
	out := newOutbound(herdr, tg, topics, registry.Agent, registry.Live, capture, opts, replies, clock, log)
	in := newInbound(herdr, tg, topics, registry.Agent, registry.Live, out, opts, cfg.ChatID, cfg.BotUsername, clock, log)
	b := &Bridge{
		out:         out,
		in:          in,
		jobs:        make(chan any, bridgeBuffer),
		fatal:       make(chan error, 1),
		log:         log,
		CallTimeout: bridgeCallTimeout,
	}
	// A slow agent.start runs off the loop and reports back as a job, so
	// the bridge stays the only writer to Telegram.
	in.async = func(run func(context.Context) startResult) {
		b.spawn(func(ctx context.Context) { b.Submit(run(ctx)) })
	}
	return b
}

// startResult is the job a background agent start submits when it is
// over: what was asked, what Herdr answered and when it began.
type startResult struct {
	messageID int
	workspace string
	kind      string
	name      string
	paneID    string
	agent     domain.Agent
	err       error
	started   time.Time
}

// spawn runs fn on its own goroutine with a context bound to Run and the
// start timeout plus grace; Run waits for every spawned goroutine.
func (b *Bridge) spawn(fn func(context.Context)) {
	parent := b.runCtx
	if parent == nil {
		parent = context.Background()
	}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		ctx, cancel := context.WithTimeout(parent, agentStartTimeout+agentStartGrace)
		defer cancel()
		fn(ctx)
	}()
}

// presenceAway is the job the daemon submits when quiet mode ends: the
// outbound posts the questions that are still waiting.
type presenceAway struct{}

// SetPresence wires quiet mode into the outbound (posts mode, catch-up)
// and the inbound (/away, /here, the /status header).
func (b *Bridge) SetPresence(p *Presence, opts *Options) {
	if p == nil {
		return
	}
	b.out.SetPresence(p.Quiet, opts)
	b.in.SetPresence(p)
}

// SetSettle overrides the screen and command settle delays (tests).
func (b *Bridge) SetSettle(d time.Duration) {
	b.out.deb.delay = d
	b.in.deb.delay = d
}

// Fatal delivers the first fatal Telegram error met by a job.
func (b *Bridge) Fatal() <-chan error { return b.fatal }

// Submit queues a job without blocking: an AgentEvent, a TopicMessage, a
// ButtonPressed or a GeneralCommand. When the buffer is full the job is dropped and counted;
// the daemon reports the count at most once per dropReportInterval and the
// next event or a resync brings the state back.
func (b *Bridge) Submit(job any) {
	switch job.(type) {
	case AgentEvent, domain.TopicMessage, domain.ButtonPressed, domain.GeneralCommand, presenceAway, startResult:
	default:
		b.log.Warn("bridge job of unknown type dropped", slog.String("type", fmt.Sprintf("%T", job)))
		return
	}
	select {
	case b.jobs <- job:
	default:
		n := b.dropped.Add(1)
		b.log.Debug("bridge overflow, job dropped", slog.String("type", fmt.Sprintf("%T", job)), slog.Int64("dropped", n))
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
	b.runCtx = ctx
	defer b.wg.Wait()
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
		case key := <-b.out.TurnDue():
			b.run(ctx, "turn", func(ctx context.Context) error { return b.out.EndTurn(ctx, key) })
		}
	}
}

func (b *Bridge) handle(ctx context.Context, job any) {
	switch j := job.(type) {
	case AgentEvent:
		b.log.Debug("bridge job", slog.String("kind", "agent_event"), slog.String("event", string(j.Kind)), slog.String("key", j.Agent.Key.String()))
		b.out.Observe(j)
		if j.Kind == AgentGone {
			b.in.Forget(j.Agent.Key)
			b.run(ctx, "forget", func(ctx context.Context) error { return b.out.Forget(ctx, j.Agent.Key) })
		}
	case domain.ButtonPressed:
		b.log.Debug("bridge job", slog.String("kind", "button"), slog.Int("thread_id", j.ThreadID), slog.Int("message_id", j.MessageID))
		if strings.HasPrefix(j.Data, panelPrefix) {
			b.run(ctx, "options_button", func(ctx context.Context) error { return b.in.PressPanel(ctx, j) })
			return
		}
		if strings.HasPrefix(j.Data, closePrefix) {
			b.run(ctx, "close_button", func(ctx context.Context) error { return b.in.PressClose(ctx, j) })
			return
		}
		b.run(ctx, "button", func(ctx context.Context) error { return b.out.Press(ctx, j) })
	case domain.TopicMessage:
		b.log.Debug("bridge job", slog.String("kind", "topic_message"), slog.Int("thread_id", j.ThreadID), slog.Int("message_id", j.MessageID))
		b.run(ctx, "topic_message", func(ctx context.Context) error { return b.in.HandleTopic(ctx, j) })
	case domain.GeneralCommand:
		b.log.Debug("bridge job", slog.String("kind", "general_command"), slog.Int("message_id", j.MessageID))
		b.run(ctx, "general_command", func(ctx context.Context) error { return b.in.HandleGeneral(ctx, j) })
	case presenceAway:
		b.log.Debug("bridge job", slog.String("kind", "catch_up"))
		b.run(ctx, "catch_up", b.out.CatchUp)
	case startResult:
		b.log.Debug("bridge job", slog.String("kind", "start_result"), slog.Int("message_id", j.messageID), slog.Bool("ok", j.err == nil))
		b.run(ctx, "start_result", func(ctx context.Context) error { return b.in.StartFinished(ctx, j) })
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
