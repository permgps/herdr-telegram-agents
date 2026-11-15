package app

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// Reconciler is the single writer of Telegram topics. It turns registry
// events into minimal create/edit/close calls, keeps the mapping in step
// with what Telegram accepted, and can replay the desired state on demand
// (drift healing on start, resync, resume after lost rights).
type Reconciler struct {
	tg      domain.TelegramGateway
	herdr   domain.HerdrGateway // only for agent.rename mirrored from Telegram
	store   domain.MappingStore
	mapping *domain.Mapping
	clock   domain.Clock
	log     *slog.Logger

	deb          *debouncer
	agents       map[domain.Key]domain.Agent // last known agent per key, for fire-time diffs
	reassociated map[domain.Key]reassociatedTopic
	// rightsLost pauses writes while the bot cannot manage topics; paused
	// reads the operator's sync switch; quiet reads the presence tracker
	// (operator at the desk with topic edits held). Any of them blocks
	// every Telegram write.
	rightsLost bool
	paused     func() bool
	quiet      func() bool
	// syncLogged and quietLogged keep the "sync off" and "quiet" skips at
	// one log line per pause each.
	syncLogged  bool
	quietLogged bool
	// sweepRightsWarned keeps the "cannot delete messages" warning of the
	// sweep at one line per daemon run.
	sweepRightsWarned bool
	// sweepOffset rotates the next bounded pass so persistent failures cannot
	// monopolize every batch. It is owned by the daemon loop.
	sweepOffset int
	force       bool // set by Resync for the duration of one pass
	// view is the read-only copy of the mapping the bridge goroutine
	// consults; it is republished after every save.
	view *topicView
	// dirty means Telegram or an in-memory transition succeeded but the
	// resulting mapping has not been persisted yet. Retry it before more
	// external topic writes in this process.
	dirty bool
}

type reassociatedTopic struct {
	from     domain.Key
	threadID int
}

// NewReconciler wires the reconciler around a loaded mapping. herdr is
// used only to mirror operator renames back to the agent.
func NewReconciler(tg domain.TelegramGateway, herdr domain.HerdrGateway, store domain.MappingStore, mapping *domain.Mapping, opts *Options, clock domain.Clock, log *slog.Logger) *Reconciler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	paused := func() bool { return false }
	if opts != nil {
		paused = func() bool { return !opts.SyncEnabled() }
	}
	r := &Reconciler{
		tg:           tg,
		herdr:        herdr,
		store:        store,
		mapping:      mapping,
		clock:        clock,
		log:          log,
		deb:          newDebouncer(clock, editDebounce, log),
		agents:       map[domain.Key]domain.Agent{},
		reassociated: map[domain.Key]reassociatedTopic{},
		paused:       paused,
		quiet:        func() bool { return false },
		view:         newTopicView(),
	}
	r.view.publish(mapping)
	return r
}

// SetQuiet installs the quiet predicate: while it answers true, every
// topic write is deferred and the next Reconcile heals what drifted. nil
// means never quiet.
func (r *Reconciler) SetQuiet(fn func() bool) {
	if fn == nil {
		fn = func() bool { return false }
	}
	r.quiet = fn
}

// topics returns the goroutine-safe read model of the mapping.
func (r *Reconciler) topics() *topicView { return r.view }

// SetDebounce overrides the edit delay (tests).
func (r *Reconciler) SetDebounce(d time.Duration) { r.deb.delay = d }

// Due delivers keys whose debounced edit is ready; call Fire for each.
func (r *Reconciler) Due() <-chan domain.Key { return r.deb.Due() }

// Mapping exposes the aggregate for status output and tests.
func (r *Reconciler) Mapping() *domain.Mapping { return r.mapping }

// DashboardID is the message id of the General dashboard, 0 when none.
func (r *Reconciler) DashboardID() int { return r.mapping.Dashboard }

// SetDashboardID records the dashboard message (0 forgets it) and saves.
func (r *Reconciler) SetDashboardID(ctx context.Context, id int) error {
	if r.mapping.Dashboard == id && !r.mapping.PendingDashboard {
		return r.persistPending(ctx)
	}
	r.log.Debug("dashboard id set", slog.Int("message_id", id), slog.Int("previous", r.mapping.Dashboard))
	r.mapping.Dashboard = id
	r.mapping.PendingDashboard = false
	return r.save(ctx)
}

// ReadOnly reports whether writes are paused because the bot lost the
// "Manage topics" right (the operator's sync switch is a separate reason,
// see blocked).
func (r *Reconciler) ReadOnly() bool { return r.rightsLost }

// Reasons a Telegram write is skipped, for the log.
const (
	skipRights  = "rights"
	skipSyncOff = "sync_off"
	skipQuiet   = "quiet"
)

// blockedReason reports why Telegram writes are off, or "" when they are
// on: lost rights, the operator's sync switch, or quiet mode (the operator
// is at the desk). The sync-off and quiet cases are logged once per pause.
func (r *Reconciler) blockedReason() string {
	if r.rightsLost {
		return skipRights
	}
	if r.paused() {
		if !r.syncLogged {
			r.syncLogged = true
			r.log.Info("reconcile paused: sync is off (/options)")
		}
		return skipSyncOff
	}
	r.syncLogged = false
	if r.quiet() {
		if !r.quietLogged {
			r.quietLogged = true
			r.log.Info("reconcile deferred: operator at the desk (quiet)")
		}
		return skipQuiet
	}
	r.quietLogged = false
	return ""
}

// blocked reports whether Telegram writes are off for any reason.
func (r *Reconciler) blocked() bool { return r.blockedReason() != "" }

// SetReadOnly pauses or resumes Telegram writes for the rights path.
// Resuming does not replay anything by itself; the caller runs Reconcile
// with the live agents.
func (r *Reconciler) SetReadOnly(ro bool) {
	if r.rightsLost == ro {
		return
	}
	r.rightsLost = ro
	if ro {
		r.log.Info("reconcile paused: bot cannot manage topics")
	} else {
		r.log.Info("reconcile resumed")
	}
}

// Handle applies one registry event.
func (r *Reconciler) Handle(ctx context.Context, ev AgentEvent) error {
	if err := r.persistPending(ctx); err != nil {
		return err
	}
	r.reassociated = map[domain.Key]reassociatedTopic{}
	key := ev.Agent.Key
	r.log.Debug("reconcile event", slog.String("kind", string(ev.Kind)), slog.String("key", key.String()),
		slog.String("label", ev.Agent.Label()), slog.String("status", string(ev.Agent.Status)))
	switch ev.Kind {
	case AgentAppeared, AgentChanged:
		r.agents[key] = ev.Agent
		if err := r.reassociate(ctx, []domain.Agent{ev.Agent}); err != nil {
			return err
		}
		entry, ok := r.mapping.TopicFor(key)
		if !ok || !entry.Status.Live() {
			return r.create(ctx, ev.Agent)
		}
		if ev.Kind == AgentAppeared {
			// Known topic: heal any drift now instead of waiting.
			return r.edit(ctx, key)
		}
		r.deb.Schedule(key)
		return nil
	case AgentGone:
		r.deb.Cancel(key)
		delete(r.agents, key)
		return r.exit(ctx, key)
	default:
		return nil
	}
}

// TakeReassociatedFrom returns the old key when Handle moved that agent's
// existing topic and kept the same Telegram thread. A recreated/missing
// topic does not qualify.
func (r *Reconciler) TakeReassociatedFrom(key domain.Key) (domain.Key, bool) {
	link, ok := r.reassociated[key]
	delete(r.reassociated, key)
	if !ok {
		return domain.Key{}, false
	}
	entry, ok := r.mapping.TopicFor(key)
	if !ok || entry.ThreadID != link.threadID {
		return domain.Key{}, false
	}
	return link.from, true
}

// Fire runs the debounced edit for key.
func (r *Reconciler) Fire(ctx context.Context, key domain.Key) error {
	return r.edit(ctx, key)
}

// Flush fires every pending edit now (shutdown, resync).
func (r *Reconciler) Flush(ctx context.Context) error {
	for _, key := range r.deb.Drain() {
		if err := r.edit(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

// Reconcile replays the desired state for the live agents: duplicates and
// orphans are cleaned, missing topics created, drifted names fixed,
// unclosed exited topics closed, old entries pruned. It is idempotent.
// Resync is Reconcile with every live topic rewritten: name and icon are
// sent again even when the mapping thinks they match. That heals edits
// made by hand in Telegram and icon changes between releases, which the
// mapping cannot see because it stores statuses, not emoji ids. It is the
// resync action and costs one editForumTopic per live topic.
func (r *Reconciler) Resync(ctx context.Context, live []domain.Agent) error {
	r.force = true
	defer func() { r.force = false }()
	return r.Reconcile(ctx, live)
}

func (r *Reconciler) Reconcile(ctx context.Context, live []domain.Agent) error {
	if err := r.persistPending(ctx); err != nil {
		return err
	}
	if r.blocked() {
		// Lost rights are worth a line per pass; the operator's own switch
		// was logged once when it took effect.
		level := slog.LevelDebug
		if r.rightsLost {
			level = slog.LevelInfo
		}
		r.log.Log(ctx, level, "reconcile skipped, writes paused", slog.Int("agents", len(live)), slog.String("reason", r.blockedReason()))
		return nil
	}
	r.log.Info("reconcile pass", slog.Int("agents", len(live)), slog.Int("entries", len(r.mapping.Topics)))
	if n := r.mapping.DedupeThreads(); n > 0 {
		r.log.Warn("duplicate topic entries dropped", slog.Int("count", n))
		if err := r.save(ctx); err != nil {
			return err
		}
	}
	liveSet := make(map[domain.Key]struct{}, len(live))
	r.agents = make(map[domain.Key]domain.Agent, len(live))
	for _, a := range live {
		liveSet[a.Key] = struct{}{}
		r.agents[a.Key] = a
	}
	r.reassociated = map[domain.Key]reassociatedTopic{}
	reassociateErr := r.reassociate(ctx, live)
	r.reassociated = map[domain.Key]reassociatedTopic{}
	if reassociateErr != nil {
		return reassociateErr
	}
	for _, key := range r.mapping.Orphans(liveSet) {
		r.deb.Cancel(key)
		if err := r.exit(ctx, key); err != nil {
			return err
		}
	}
	for _, key := range r.mapping.Unclosed() {
		if err := r.close(ctx, key); err != nil {
			return err
		}
	}
	sort.Slice(live, func(i, j int) bool { return live[i].Key.String() < live[j].Key.String() })
	for _, a := range live {
		entry, ok := r.mapping.TopicFor(a.Key)
		var err error
		if !ok || !entry.Status.Live() {
			err = r.create(ctx, a)
		} else {
			r.deb.Cancel(a.Key)
			err = r.edit(ctx, a.Key)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *Reconciler) create(ctx context.Context, a domain.Agent) error {
	if reason := r.blockedReason(); reason != "" {
		// An unmapped agent re-enters here on every status change, so the
		// quiet case (expected, frequent) stays at debug.
		level := slog.LevelInfo
		if reason == skipQuiet {
			level = slog.LevelDebug
		}
		r.log.Log(ctx, level, "topic create skipped, writes paused", slog.String("key", a.Key.String()), slog.String("reason", reason))
		return nil
	}
	if entry, ok := r.mapping.TopicFor(a.Key); ok && !entry.Status.Live() {
		return r.revive(ctx, a, entry)
	}
	return r.createTopic(ctx, a)
}

func (r *Reconciler) reassociate(ctx context.Context, live []domain.Agent) error {
	plan := r.mapping.PlanReassociation(live)
	for _, ambiguity := range plan.Ambiguous {
		r.log.Warn("topic reassociation ambiguous",
			slog.String("pane", ambiguity.LiveKey.PaneID),
			slog.Int("candidate_count", ambiguity.CandidateCount),
			slog.String("reason", string(ambiguity.Reason)))
	}
	if len(plan.Assignments) == 0 {
		return nil
	}
	if err := r.mapping.ApplyReassociation(plan); err != nil {
		r.log.Error("[FIX] topic reassociation could not be applied", slog.String("err", err.Error()))
		return fmt.Errorf("apply topic reassociation: %w", err)
	}

	liveByKey := make(map[domain.Key]domain.Agent, len(live))
	for _, a := range live {
		liveByKey[a.Key] = a
	}
	changed := false
	for _, assignment := range plan.Assignments {
		if assignment.From != assignment.To {
			r.deb.Cancel(assignment.From)
			delete(r.agents, assignment.From)
		}
		entry, ok := r.mapping.TopicFor(assignment.To)
		if !ok {
			continue
		}
		if assignment.From != assignment.To {
			r.reassociated[assignment.To] = reassociatedTopic{from: assignment.From, threadID: entry.ThreadID}
		}
		r.log.Debug("[FIX] topic reassociation candidate selected",
			slog.String("reason", string(assignment.Reason)),
			slog.String("old_key", assignment.From.String()),
			slog.String("new_key", assignment.To.String()),
			slog.Int("thread_id", entry.ThreadID))
		if assignment.From != assignment.To {
			changed = true
		}
		if a, ok := liveByKey[assignment.To]; ok && r.mapping.UpdateMetadata(assignment.To, a) {
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if err := r.save(ctx); err != nil {
		for _, assignment := range plan.Assignments {
			if assignment.From == assignment.To {
				continue
			}
			entry, _ := r.mapping.TopicFor(assignment.To)
			r.log.Warn("topic reassociation is not durable",
				slog.String("old_key", assignment.From.String()),
				slog.String("new_key", assignment.To.String()),
				slog.String("reason", string(assignment.Reason)),
				slog.Int("thread_id", entry.ThreadID))
		}
		return err
	}
	for _, assignment := range plan.Assignments {
		if assignment.From == assignment.To {
			continue
		}
		entry, _ := r.mapping.TopicFor(assignment.To)
		r.log.Info("[FIX] topic adopted after agent reassociation",
			slog.String("old_key", assignment.From.String()),
			slog.String("new_key", assignment.To.String()),
			slog.String("reason", string(assignment.Reason)),
			slog.Int("thread_id", entry.ThreadID))
	}
	return nil
}
