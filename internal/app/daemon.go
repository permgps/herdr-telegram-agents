package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

const (
	// socketGrace is how long the Herdr socket may be unreachable before
	// the daemon exits.
	socketGrace = 60 * time.Second
	// healthInterval is how often the daemon checks socket reachability.
	healthInterval = 5 * time.Second
	// flushTimeout bounds the final edits on shutdown.
	flushTimeout = 5 * time.Second
	// notifyTitle is the title of every Herdr notification the daemon shows.
	notifyTitle = "Telegram Agents"
	// generalTimeout bounds one notice posted to the General topic.
	generalTimeout = 5 * time.Second
)

// Daemon is the event loop: registry events go to the reconciler and the
// bridge, debounced edits fire, operator messages and commands go to the
// bridge, Telegram membership changes pause or resume writes, resync
// requests replay the desired state, and socket health decides when to
// give up. Lifecycle notices are posted to the General topic.
type Daemon struct {
	cfg        domain.Config
	herdr      domain.HerdrGateway
	tg         domain.TelegramGateway
	registry   *Registry
	reconciler *Reconciler
	bridge     *Bridge
	capture    *Capture
	configs    domain.ConfigStore
	clock      domain.Clock
	log        *slog.Logger

	SocketGrace    time.Duration
	HealthInterval time.Duration
	// Version is shown in the General topic's started notice.
	Version string

	resync chan struct{}

	// started is when Run began; Stats reports the uptime from it.
	started time.Time
	// lastDropped and lastDropReport rate-limit the overflow warning.
	lastDropped    int64
	lastDropReport time.Time
}

// Stats is the daemon's self-description for the status action, served
// through the control channel. PID is filled in by the caller.
type Stats struct {
	Version           string
	PID               int
	Since             time.Time
	Agents            int
	Dropped           int64
	HerdrOK           bool
	HerdrFailingSince time.Time
}

// Stats snapshots the running daemon. It is safe to call from another
// goroutine: every source is mutex-protected or atomic.
func (d *Daemon) Stats() Stats {
	h := d.registry.Health()
	s := Stats{
		Version: d.Version,
		Since:   d.started,
		Agents:  len(d.registry.Live()),
		Dropped: d.bridge.Dropped(),
		HerdrOK: h.LastErr == nil,
	}
	if h.LastErr != nil {
		s.HerdrFailingSince = h.LastOK
		if s.HerdrFailingSince.IsZero() || s.HerdrFailingSince.Before(d.started) {
			s.HerdrFailingSince = d.started
		}
	}
	d.log.Debug("daemon stats requested", slog.Int("agents", s.Agents), slog.Int64("dropped", s.Dropped), slog.Bool("herdr_ok", s.HerdrOK))
	return s
}

// StatsLine renders Stats as the one-line status reply:
// version=<v> pid=<n> uptime=<s> agents=<n> dropped=<n> herdr=ok|failing since <s>.
func StatsLine(s Stats, now time.Time) string {
	version := s.Version
	if version == "" {
		version = "dev"
	}
	uptime := now.Sub(s.Since).Round(time.Second)
	if s.Since.IsZero() || uptime < 0 {
		uptime = 0
	}
	herdr := "ok"
	if !s.HerdrOK {
		herdr = fmt.Sprintf("failing since %s", now.Sub(s.HerdrFailingSince).Round(time.Second))
	}
	return fmt.Sprintf("version=%s pid=%d uptime=%s agents=%d dropped=%d herdr=%s",
		version, s.PID, uptime, s.Agents, s.Dropped, herdr)
}

// reportDrops warns about bridge jobs lost since the last report, at most
// once per dropReportInterval, so a sustained overflow does not flood the
// log while a single burst is still visible.
func (d *Daemon) reportDrops(now time.Time) {
	n := d.bridge.Dropped()
	if n <= d.lastDropped {
		return
	}
	if !d.lastDropReport.IsZero() && now.Sub(d.lastDropReport) < dropReportInterval {
		d.log.Debug("bridge drops pending report", slog.Int64("delta", n-d.lastDropped), slog.Int64("total", n))
		return
	}
	d.log.Warn("bridge dropped jobs", slog.Int64("delta", n-d.lastDropped), slog.Int64("total", n))
	d.lastDropped = n
	d.lastDropReport = now
}

// NewDaemon wires the loop. The registry, reconciler, bridge and capture
// are built by the caller so the composition root can share the clock and
// logger.
func NewDaemon(cfg domain.Config, herdr domain.HerdrGateway, tg domain.TelegramGateway,
	registry *Registry, reconciler *Reconciler, bridge *Bridge, capture *Capture, configs domain.ConfigStore, clock domain.Clock, log *slog.Logger) *Daemon {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Daemon{
		cfg: cfg, herdr: herdr, tg: tg, registry: registry, reconciler: reconciler, bridge: bridge, capture: capture,
		configs: configs, clock: clock, log: log,
		SocketGrace: socketGrace, HealthInterval: healthInterval,
		resync: make(chan struct{}, 1),
	}
}

// Resync asks the loop for a full reconcile. It never blocks.
func (d *Daemon) Resync() {
	select {
	case d.resync <- struct{}{}:
	default:
	}
}

// Run executes the loop until ctx is done, the Herdr socket stays gone for
// SocketGrace, or a fatal Telegram error occurs. A cancelled context with a
// cause other than plain cancellation (the poller's fatal errors) is
// returned as that cause.
func (d *Daemon) Run(ctx context.Context) error {
	start := d.clock.Now()
	d.started = start
	d.log.Info("daemon started", slog.Int64("chat_id", d.cfg.ChatID), slog.String("chat", d.cfg.ChatTitle))

	if err := d.checkRights(ctx); err != nil {
		return err
	}
	initial, err := d.registry.Snapshot(ctx)
	if err != nil {
		d.log.Warn("initial agent snapshot failed, will retry", slog.String("err", err.Error()))
	}
	if err := d.reconciler.Reconcile(ctx, d.registry.Live()); err != nil {
		if err := d.handleErr(ctx, err); err != nil {
			return err
		}
	}

	loopCtx, cancel := context.WithCancel(ctx)
	events := make(chan AgentEvent, 256)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		_ = d.registry.Run(loopCtx, events)
	}()
	go func() {
		defer wg.Done()
		d.bridge.Run(loopCtx)
	}()
	go func() {
		defer wg.Done()
		d.capture.Run(loopCtx)
	}()
	defer func() {
		cancel()
		wg.Wait()
	}()

	// Agents already blocked when the daemon starts get their screen
	// posted too; the bridge decides per status. The capture learns the
	// starting statuses so its first transitions are seen as such.
	for _, ev := range initial {
		d.capture.Observe(ev)
		d.bridge.Submit(ev)
	}
	d.general(ctx, fmt.Sprintf("▶️ %s started: %s", d.title(), plural(len(d.registry.Live()), "agent")))

	health := d.clock.After(d.HealthInterval)
	for {
		select {
		case <-ctx.Done():
			d.shutdown()
			if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
				d.log.Info("daemon exit", slog.String("reason", cause.Error()))
				return cause
			}
			d.log.Info("daemon exit", slog.String("reason", "stopped"))
			return nil
		case ev := <-events:
			if err := d.reconciler.Handle(ctx, ev); err != nil {
				if err := d.handleErr(ctx, err); err != nil {
					return err
				}
			}
			d.capture.Observe(ev)
			d.bridge.Submit(ev)
		case err := <-d.bridge.Fatal():
			d.shutdown()
			return d.fatal(ctx, err)
		case key := <-d.reconciler.Due():
			if err := d.reconciler.Fire(ctx, key); err != nil {
				if err := d.handleErr(ctx, err); err != nil {
					return err
				}
			}
		case raw, ok := <-d.tg.Events():
			if !ok {
				continue
			}
			if err := d.onTelegramEvent(ctx, raw); err != nil {
				return err
			}
		case <-d.resync:
			d.log.Info("resync requested")
			if err := d.replay(ctx, true); err != nil {
				return err
			}
		case <-health:
			health = d.clock.After(d.HealthInterval)
			d.reportDrops(d.clock.Now())
			if gone := d.socketGone(start); gone {
				d.general(ctx, fmt.Sprintf("⏹ %s: Herdr socket unreachable for %s, exiting", d.title(), d.SocketGrace))
				d.shutdown()
				d.log.Info("daemon exit", slog.String("reason", "herdr socket gone"))
				return nil
			}
		}
	}
}

// checkRights verifies the bot's standing once at start. Missing rights
// put the reconciler in read-only mode; fatal errors end the daemon.
func (d *Daemon) checkRights(ctx context.Context) error {
	rights, err := d.tg.Rights(ctx)
	if err != nil {
		if isFatal(err) {
			return d.fatal(ctx, err)
		}
		d.log.Warn("rights check failed, assuming ok", slog.String("err", err.Error()))
		return nil
	}
	d.log.Info("telegram rights", slog.Bool("forum", rights.IsForum), slog.Bool("admin", rights.IsAdmin),
		slog.Bool("manage_topics", rights.CanManageTopics), slog.Bool("delete_messages", rights.CanDeleteMessages))
	switch {
	case !rights.IsForum:
		d.reconciler.SetReadOnly(true)
		d.notify(ctx, fmt.Sprintf("%q is not a forum group: enable Topics in the group settings", d.cfg.ChatTitle))
	case !rights.CanManageTopics:
		d.reconciler.SetReadOnly(true)
		d.notify(ctx, fmt.Sprintf("the bot cannot manage topics in %q: grant it the \"Manage topics\" right", d.cfg.ChatTitle))
	}
	return nil
}

func (d *Daemon) onTelegramEvent(ctx context.Context, raw domain.Event) error {
	switch ev := raw.(type) {
	case domain.RightsChanged:
		d.log.Info("telegram rights changed", slog.Bool("manage_topics", ev.CanManageTopics))
		if !ev.CanManageTopics {
			d.reconciler.SetReadOnly(true)
			d.notify(ctx, fmt.Sprintf("the bot lost the \"Manage topics\" right in %q; topics are not updated until it is granted again", d.cfg.ChatTitle))
			d.general(ctx, "⚠️ the bot lost the \"Manage topics\" right; topics are not updated until it is granted again")
			return nil
		}
		if d.reconciler.ReadOnly() {
			d.reconciler.SetReadOnly(false)
			d.general(ctx, "✅ \"Manage topics\" right regained, topics are updated again")
			return d.replay(ctx, false)
		}
		return nil
	case domain.TopicMessage, domain.ButtonPressed, domain.GeneralCommand:
		d.bridge.Submit(raw)
		return nil
	case domain.TopicClosed:
		return d.handleErr(ctx, d.reconciler.OnTopicClosed(ctx, ev.ThreadID))
	case domain.TopicReopened:
		return d.handleErr(ctx, d.reconciler.OnTopicReopened(ctx, ev.ThreadID))
	case domain.TopicRenamed:
		snapshot, err := d.reconciler.OnTopicRenamed(ctx, ev.ThreadID, ev.Name)
		if snapshot {
			d.registry.RequestSnapshot()
		}
		return d.handleErr(ctx, err)
	default:
		d.log.Debug("telegram event ignored", slog.String("type", fmt.Sprintf("%T", raw)))
		return nil
	}
}

// replay takes a fresh snapshot, flushes pending edits and runs the drift
// pass, so the mapping matches Herdr again. force rewrites every live topic
// (the resync action); the rights-regained path only heals what differs.
func (d *Daemon) replay(ctx context.Context, force bool) error {
	evs, err := d.registry.Snapshot(ctx)
	if err != nil {
		d.log.Warn("snapshot for replay failed", slog.String("err", err.Error()))
	}
	for _, ev := range evs {
		if err := d.reconciler.Handle(ctx, ev); err != nil {
			if err := d.handleErr(ctx, err); err != nil {
				return err
			}
		}
	}
	if err := d.reconciler.Flush(ctx); err != nil {
		if err := d.handleErr(ctx, err); err != nil {
			return err
		}
	}
	live := d.registry.Live()
	var passErr error
	if force {
		passErr = d.reconciler.Resync(ctx, live)
	} else {
		passErr = d.reconciler.Reconcile(ctx, live)
	}
	if passErr != nil {
		return d.handleErr(ctx, passErr)
	}
	return nil
}

// handleErr deals with errors the reconciler could not absorb: chat
// migration is recorded and survived, everything else is fatal.
func (d *Daemon) handleErr(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var migrated *domain.ChatMigratedError
	if errors.As(err, &migrated) {
		d.log.Warn("chat migrated, updating config; restart the daemon to use the new id",
			slog.Int64("old", d.cfg.ChatID), slog.Int64("new", migrated.NewChatID))
		d.cfg.ChatID = migrated.NewChatID
		if err := d.configs.Save(ctx, d.cfg); err != nil {
			d.log.Error("save migrated config failed", slog.String("err", err.Error()))
		}
		d.notify(ctx, "the Telegram group id changed; restart Telegram Agents to continue")
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	return d.fatal(ctx, err)
}

// fatal notifies the user with a one-line reason and returns the error.
func (d *Daemon) fatal(ctx context.Context, err error) error {
	var reason string
	switch {
	case errors.Is(err, domain.ErrBotUnauthorized):
		reason = "Telegram rejected the bot token. Run the setup action."
	case errors.Is(err, domain.ErrPollerConflict):
		reason = "another process is polling this bot. Stop it, then start Telegram Agents again."
	case errors.Is(err, domain.ErrForbidden):
		reason = fmt.Sprintf("the bot was removed from %q. Add it back as an administrator or run the setup action.", d.cfg.ChatTitle)
	default:
		reason = "stopped: " + err.Error()
	}
	d.log.Error("daemon fatal", slog.String("err", err.Error()))
	d.notify(ctx, reason)
	return err
}

func isFatal(err error) bool {
	return errors.Is(err, domain.ErrBotUnauthorized) || errors.Is(err, domain.ErrPollerConflict) || errors.Is(err, domain.ErrForbidden)
}

// socketGone reports whether snapshots have been failing for longer than
// the grace period. It also asks for a retry so recovery is noticed within
// one health interval rather than one reconcile interval.
func (d *Daemon) socketGone(start time.Time) bool {
	h := d.registry.Health()
	if h.LastErr == nil {
		return false
	}
	since := h.LastOK
	if since.IsZero() || since.Before(start) {
		since = start
	}
	down := d.clock.Now().Sub(since)
	d.log.Warn("herdr socket unreachable", slog.Int64("down_for_ms", down.Milliseconds()), slog.String("err", h.LastErr.Error()))
	if down >= d.SocketGrace {
		return true
	}
	d.registry.RequestSnapshot()
	return false
}

// shutdown posts the stopping notice and flushes pending edits with a
// bounded real-time budget. It uses a fresh context because the daemon's
// own context is already done.
func (d *Daemon) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
	defer cancel()
	d.general(ctx, fmt.Sprintf("⏹ %s stopping", d.title()))
	if err := d.reconciler.Flush(ctx); err != nil {
		d.log.Warn("flush on shutdown failed", slog.String("err", err.Error()))
	}
}

// title names the daemon in General notices, with the version when known.
func (d *Daemon) title() string {
	if d.Version == "" {
		return notifyTitle
	}
	return notifyTitle + " " + d.Version
}

// general posts a silent lifecycle notice to the General topic; failures
// are only logged because the notice is informational.
func (d *Daemon) general(ctx context.Context, text string) {
	gctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), generalTimeout)
	defer cancel()
	if _, err := d.tg.Send(gctx, domain.Outgoing{ThreadID: 0, Text: text}); err != nil {
		d.log.Warn("general notice failed", slog.String("text", text), slog.String("err", err.Error()))
		return
	}
	d.log.Info("general notice", slog.String("text", text))
}

// notify shows a Herdr desktop notification; failures are only logged
// because the socket may be the thing that is broken.
func (d *Daemon) notify(ctx context.Context, body string) {
	nctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := d.herdr.Notify(nctx, notifyTitle, body, domain.NotifySoundDefault); err != nil {
		d.log.Warn("notification failed", slog.String("body", body), slog.String("err", err.Error()))
		return
	}
	d.log.Info("notification shown", slog.String("body", body))
}
