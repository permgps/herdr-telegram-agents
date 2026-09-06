package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// dashboardKey is the single debouncer key of the dashboard.
var dashboardKey = domain.Key{PaneID: "dashboard", TerminalID: "general"}

// Dashboard keeps one pinned message in General that shows the /status
// text plus how long each agent has been in its status and an "updated
// HH:MM" footer. It is created silently, pinned silently, edited in place
// (an edit never notifies) and recreated when Telegram lost it. Edits are
// coalesced by dashboardSettle, repeated on the minute tick while a
// duration label moved, and skipped when the body is unchanged. It runs
// on the daemon loop; only Since and Schedule are safe from elsewhere.
type Dashboard struct {
	tg       domain.TelegramGateway
	rec      *Reconciler
	opts     *Options
	topics   *topicView
	live     func() []domain.Agent
	presence *Presence
	chatID   int64
	clock    domain.Clock
	log      *slog.Logger

	deb *debouncer
	// mu guards since and last: Observe writes them on the daemon loop,
	// Since reads them for /status on the bridge goroutine.
	mu    sync.Mutex
	since map[domain.Key]time.Time
	last  map[domain.Key]domain.Status
	// lastHash is the body last sent; an equal body skips the edit.
	lastHash string
	// pinWarned keeps the missing pin right at one warning per daemon.
	pinWarned bool
	// hasMessage mirrors "DashboardID != 0" for Schedule, which may run
	// off the daemon loop: with the option off and no message to remove
	// there is nothing to refresh.
	hasMessage atomic.Bool
}

// NewDashboard wires the dashboard. live lists the agents to show (the
// registry), topics links them to their topics, presence supplies the
// quiet header (nil for none).
func NewDashboard(tg domain.TelegramGateway, rec *Reconciler, opts *Options, topics *topicView, live func() []domain.Agent,
	presence *Presence, chatID int64, clock domain.Clock, log *slog.Logger) *Dashboard {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if opts == nil {
		opts = NewOptions(domain.DefaultOptions(), nil, nil, log)
	}
	if live == nil {
		live = func() []domain.Agent { return nil }
	}
	b := &Dashboard{
		tg: tg, rec: rec, opts: opts, topics: topics, live: live, presence: presence, chatID: chatID, clock: clock, log: log,
		deb:   newDebouncer(clock, dashboardSettle, log),
		since: map[domain.Key]time.Time{},
		last:  map[domain.Key]domain.Status{},
	}
	b.hasMessage.Store(rec.DashboardID() != 0)
	return b
}

// SetSettle overrides the coalescing delay (tests).
func (b *Dashboard) SetSettle(d time.Duration) { b.deb.delay = d }

// Observe records when an agent entered its status: an appearance records
// the status without a start (the daemon cannot know when it began), a
// change to another status starts the clock, a gone agent is dropped. It
// then schedules a refresh.
func (b *Dashboard) Observe(ev AgentEvent) {
	key := ev.Agent.Key
	b.mu.Lock()
	switch ev.Kind {
	case AgentGone:
		delete(b.since, key)
		delete(b.last, key)
	case AgentAppeared:
		delete(b.since, key)
		b.last[key] = ev.Agent.Status
	case AgentChanged:
		if prev, ok := b.last[key]; !ok || prev != ev.Agent.Status {
			b.since[key] = b.clock.Now()
		}
		b.last[key] = ev.Agent.Status
	}
	b.mu.Unlock()
	b.Schedule("event")
}

// Since returns a copy of the per-agent status start times, for /status.
// Safe from any goroutine.
func (b *Dashboard) Since() map[domain.Key]time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[domain.Key]time.Time, len(b.since))
	for k, v := range b.since {
		out[k] = v
	}
	return out
}

// Schedule arms the coalescing timer; reason is for the log (event,
// presence, option, start). Safe from any goroutine.
func (b *Dashboard) Schedule(reason string) {
	if !b.opts.DashboardEnabled() && !b.hasMessage.Load() {
		b.log.Debug("dashboard refresh skipped", slog.String("reason", reason), slog.String("state", "off"))
		return
	}
	b.log.Debug("dashboard scheduled", slog.String("reason", reason))
	b.deb.Schedule(dashboardKey)
}

// setID records the message id through the reconciler and keeps the
// hasMessage mirror in step.
func (b *Dashboard) setID(ctx context.Context, id int) {
	b.rec.SetDashboardID(ctx, id)
	b.hasMessage.Store(id != 0)
}

// Due fires when the coalescing timer ran out; call Fire.
func (b *Dashboard) Due() <-chan domain.Key { return b.deb.Due() }

// Fire refreshes the message after the settle: the edit when the body
// changed, the create-and-pin when there is no message, the removal when
// the option was switched off. Only fatal Telegram errors are returned.
func (b *Dashboard) Fire(ctx context.Context) error { return b.refresh(ctx, "fire") }

// Tick is the minute timer: the same refresh, so a duration label that
// crossed a minute is edited in. Only fatal Telegram errors are returned.
func (b *Dashboard) Tick(ctx context.Context) error { return b.refresh(ctx, "tick") }

// Enable applies the option now: off removes the message, on creates or
// edits it. The daemon's option hook only schedules; this is for callers
// on the daemon loop. Only fatal Telegram errors are returned.
func (b *Dashboard) Enable(ctx context.Context, on bool) error {
	b.log.Info("dashboard set", slog.Bool("enabled", on))
	return b.refresh(ctx, "option")
}

// Stop writes the final edit with the "stopped" footer on shutdown; a
// missing message is left alone.
func (b *Dashboard) Stop(ctx context.Context) {
	id := b.rec.DashboardID()
	if id == 0 || !b.opts.DashboardEnabled() {
		b.log.Debug("dashboard stop skipped", slog.Int("message_id", id))
		return
	}
	b.deb.Cancel(dashboardKey)
	v := b.view()
	v.footer = "<i>⏹ stopped " + wallClock(b.clock, b.clock.Now()) + "</i>"
	if err := b.tg.EditText(ctx, id, v.render(), true, nil); err != nil {
		b.log.Warn("dashboard stop edit failed", slog.Int("message_id", id), slog.String("err", err.Error()))
		return
	}
	b.log.Info("dashboard stopped", slog.Int("message_id", id))
}

// view assembles the renderer from the live state.
func (b *Dashboard) view() statusView {
	return statusView{
		agents:    b.live(),
		topics:    b.topics,
		icons:     b.opts.StatusIcons(),
		syncOff:   !b.opts.SyncEnabled(),
		presence:  presenceHeaderText(b.presence, b.opts.QuietEnabled(), b.clock),
		chatID:    b.chatID,
		since:     b.Since(),
		now:       b.clock.Now(),
		maxAgents: dashboardMaxAgents,
	}
}

// refresh is the one write path: remove, edit or create as the option and
// the message state demand.
func (b *Dashboard) refresh(ctx context.Context, reason string) error {
	id := b.rec.DashboardID()
	if !b.opts.DashboardEnabled() {
		if id == 0 {
			return nil
		}
		return b.remove(ctx, id)
	}
	v := b.view()
	body := v.body()
	hash := hashText(body)
	if id != 0 && hash == b.lastHash {
		b.log.Debug("dashboard unchanged", slog.Int("message_id", id), slog.String("reason", reason))
		return nil
	}
	v.footer = "<i>updated " + wallClock(b.clock, v.now) + "</i>"
	text := v.render()
	agents := len(v.agents)
	if id != 0 {
		err := b.tg.EditText(ctx, id, text, true, nil)
		switch {
		case err == nil:
			b.lastHash = hash
			b.log.Debug("dashboard edited", slog.Int("message_id", id), slog.Int("agents", agents), slog.String("reason", reason))
			return nil
		case errors.Is(err, domain.ErrMessageGone):
			b.log.Info("dashboard recreated: message gone", slog.Int("message_id", id))
		default:
			return b.failed("dashboard edit failed", id, err)
		}
	}
	newID, err := b.tg.Send(ctx, domain.Outgoing{ThreadID: 0, Text: text, HTML: true, Notify: false})
	if err != nil {
		return b.failed("dashboard create failed", id, err)
	}
	pinned := b.pin(ctx, newID)
	b.setID(ctx, newID)
	b.lastHash = hash
	b.log.Info("dashboard created", slog.Int("message_id", newID), slog.Bool("pinned", pinned), slog.Int("agents", agents), slog.String("reason", reason))
	return nil
}

// pin pins the message silently and reports whether it worked; a missing
// right is warned about once per daemon and the dashboard goes on
// unpinned.
func (b *Dashboard) pin(ctx context.Context, id int) bool {
	err := b.tg.Pin(ctx, id)
	if err == nil {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		b.log.Debug("dashboard pin cancelled", slog.Int("message_id", id))
		return false
	}
	if !b.pinWarned {
		b.pinWarned = true
		b.log.Warn(`dashboard not pinned: grant the bot the "Pin messages" right`, slog.Int("message_id", id), slog.String("err", err.Error()))
	} else {
		b.log.Debug("dashboard pin failed", slog.Int("message_id", id), slog.String("err", err.Error()))
	}
	return false
}

// Repin pins the stored message again at daemon start (a no-op in
// Telegram for a message that is still pinned) so the dashboard is the
// visible pin after a restart. A gone message is forgotten so the next
// refresh creates a new one.
func (b *Dashboard) Repin(ctx context.Context) {
	id := b.rec.DashboardID()
	if id == 0 || !b.opts.DashboardEnabled() {
		return
	}
	err := b.tg.Pin(ctx, id)
	switch {
	case err == nil:
		b.log.Info("dashboard re-pinned", slog.Int("message_id", id))
	case errors.Is(err, domain.ErrMessageGone):
		b.log.Info("dashboard recreated: message gone", slog.Int("message_id", id))
		b.setID(ctx, 0)
		b.lastHash = ""
	default:
		_ = b.pin(ctx, id)
	}
}

// dashboardOffText replaces a dashboard message Telegram refused to delete
// (a bot may delete its own group messages only within 48 h unless it
// holds "Delete messages"), so what stays behind is inert.
const dashboardOffText = "<i>dashboard is off (/options → Sync)</i>"

// remove unpins and deletes the message when the option is off; the id is
// cleared even when Telegram already lost the message. A message that
// cannot be deleted is edited to a one-line "off" note before it is
// forgotten, so switching the option on again never leaves a live-looking
// orphan behind.
func (b *Dashboard) remove(ctx context.Context, id int) error {
	if err := b.tg.Unpin(ctx, id); err != nil && !errors.Is(err, domain.ErrMessageGone) {
		b.log.Debug("dashboard unpin failed", slog.Int("message_id", id), slog.String("err", err.Error()))
	}
	if err := b.tg.DeleteMessage(ctx, id); err != nil && !errors.Is(err, domain.ErrMessageGone) {
		if err := b.failed("dashboard delete failed", id, err); err != nil {
			return err
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		if err := b.tg.EditText(ctx, id, dashboardOffText, true, nil); err != nil && !errors.Is(err, domain.ErrMessageGone) {
			if err := b.failed("dashboard off note failed", id, err); err != nil {
				return err
			}
		} else {
			b.log.Info("dashboard not deleted, marked off instead", slog.Int("message_id", id))
		}
	}
	b.setID(ctx, 0)
	b.lastHash = ""
	b.log.Info("dashboard removed", slog.Int("message_id", id))
	return nil
}

// failed applies the error policy: fatal bot errors end the daemon, a
// cancelled context is quiet, anything else is warned about and retried
// on the next refresh.
func (b *Dashboard) failed(msg string, id int, err error) error {
	switch {
	case isFatal(err):
		b.log.Error(msg+" with a fatal telegram error", slog.Int("message_id", id), slog.String("err", err.Error()))
		return err
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		b.log.Debug(msg+", cancelled", slog.Int("message_id", id))
	default:
		b.log.Warn(msg, slog.Int("message_id", id), slog.String("err", err.Error()))
	}
	return nil
}
