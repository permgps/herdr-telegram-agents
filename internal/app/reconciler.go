package app

import (
	"context"
	"errors"
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
	store   domain.MappingStore
	mapping *domain.Mapping
	clock   domain.Clock
	log     *slog.Logger

	deb      *debouncer
	agents   map[domain.Key]domain.Agent // last known agent per key, for fire-time diffs
	readOnly bool
	force    bool // set by Resync for the duration of one pass
}

// NewReconciler wires the reconciler around a loaded mapping.
func NewReconciler(tg domain.TelegramGateway, store domain.MappingStore, mapping *domain.Mapping, clock domain.Clock, log *slog.Logger) *Reconciler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Reconciler{
		tg:      tg,
		store:   store,
		mapping: mapping,
		clock:   clock,
		log:     log,
		deb:     newDebouncer(clock, editDebounce, log),
		agents:  map[domain.Key]domain.Agent{},
	}
}

// SetDebounce overrides the edit delay (tests).
func (r *Reconciler) SetDebounce(d time.Duration) { r.deb.delay = d }

// Due delivers keys whose debounced edit is ready; call Fire for each.
func (r *Reconciler) Due() <-chan domain.Key { return r.deb.Due() }

// Mapping exposes the aggregate for status output and tests.
func (r *Reconciler) Mapping() *domain.Mapping { return r.mapping }

// ReadOnly reports whether writes are paused.
func (r *Reconciler) ReadOnly() bool { return r.readOnly }

// SetReadOnly pauses or resumes Telegram writes. Resuming does not replay
// anything by itself; the caller runs Reconcile with the live agents.
func (r *Reconciler) SetReadOnly(ro bool) {
	if r.readOnly == ro {
		return
	}
	r.readOnly = ro
	if ro {
		r.log.Info("reconcile paused: bot cannot manage topics")
	} else {
		r.log.Info("reconcile resumed")
	}
}

// Handle applies one registry event.
func (r *Reconciler) Handle(ctx context.Context, ev AgentEvent) error {
	key := ev.Agent.Key
	r.log.Debug("reconcile event", slog.String("kind", string(ev.Kind)), slog.String("key", key.String()),
		slog.String("label", ev.Agent.Label()), slog.String("status", string(ev.Agent.Status)))
	switch ev.Kind {
	case AgentAppeared, AgentChanged:
		r.agents[key] = ev.Agent
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
	if r.readOnly {
		r.log.Info("reconcile skipped, writes paused", slog.Int("agents", len(live)))
		return nil
	}
	r.log.Info("reconcile pass", slog.Int("agents", len(live)), slog.Int("entries", len(r.mapping.Topics)))
	if n := r.mapping.DedupeThreads(); n > 0 {
		r.log.Warn("duplicate topic entries dropped", slog.Int("count", n))
		r.save(ctx)
	}
	liveSet := make(map[domain.Key]struct{}, len(live))
	r.agents = make(map[domain.Key]domain.Agent, len(live))
	for _, a := range live {
		liveSet[a.Key] = struct{}{}
		r.agents[a.Key] = a
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
	if n := r.mapping.Prune(r.clock.Now(), mappingPruneAge, mappingMaxEntries); n > 0 {
		r.log.Info("mapping pruned", slog.Int("removed", n))
		r.save(ctx)
	}
	return nil
}

func (r *Reconciler) create(ctx context.Context, a domain.Agent) error {
	if r.readOnly {
		r.log.Info("topic create skipped, writes paused", slog.String("key", a.Key.String()))
		return nil
	}
	if entry, ok := r.mapping.TopicFor(a.Key); ok && !entry.Status.Live() {
		// A key never comes back after exiting; a stale exited entry means
		// the mapping is ahead of reality. Start over for this key.
		r.log.Warn("exited entry for a live agent, recreating", slog.String("key", a.Key.String()))
		r.mapping.Forget(a.Key)
	}
	name, status := domain.Desired(a)
	topic, err := r.tg.CreateTopic(ctx, name, status)
	if err != nil {
		return r.fail(ctx, a.Key, "createForumTopic", err)
	}
	r.mapping.Link(a.Key, topic, a, r.clock.Now())
	r.save(ctx)
	r.log.Info("topic created", slog.String("key", a.Key.String()), slog.Int("thread", topic.ThreadID), slog.String("name", name))
	return nil
}

func (r *Reconciler) edit(ctx context.Context, key domain.Key) error {
	a, ok := r.agents[key]
	if !ok {
		return nil
	}
	entry, ok := r.mapping.TopicFor(key)
	if !ok {
		return r.create(ctx, a)
	}
	patch, changed := r.mapping.Diff(key, a)
	if r.force && entry.Status.Live() {
		name, status := domain.Desired(a)
		patch, changed = domain.TopicPatch{Name: &name, Status: &status}, true
	}
	if !changed {
		return nil
	}
	if r.readOnly {
		r.log.Info("topic edit skipped, writes paused", slog.String("key", key.String()))
		return nil
	}
	r.log.Debug("topic edit", slog.String("key", key.String()), slog.Int("thread", entry.ThreadID),
		slog.Bool("name", patch.Name != nil), slog.Bool("status", patch.Status != nil))
	if err := r.tg.EditTopic(ctx, entry.ThreadID, patch); err != nil {
		return r.fail(ctx, key, "editForumTopic", err)
	}
	r.mapping.Apply(key, patch, r.clock.Now())
	r.save(ctx)
	return nil
}

// exit writes the finished marker and closes the topic. Either half may
// fail; the mapping records how far it got so Reconcile can finish later.
func (r *Reconciler) exit(ctx context.Context, key domain.Key) error {
	entry, ok := r.mapping.TopicFor(key)
	if !ok {
		return nil
	}
	if r.readOnly {
		r.log.Info("topic exit skipped, writes paused", slog.String("key", key.String()))
		return nil
	}
	if entry.Status.Live() {
		status := domain.StatusExited
		if err := r.tg.EditTopic(ctx, entry.ThreadID, domain.TopicPatch{Status: &status}); err != nil {
			return r.fail(ctx, key, "editForumTopic", err)
		}
		r.mapping.MarkExited(key, r.clock.Now())
		r.save(ctx)
		r.log.Info("topic marked exited", slog.String("key", key.String()), slog.Int("thread", entry.ThreadID))
	}
	return r.close(ctx, key)
}

func (r *Reconciler) close(ctx context.Context, key domain.Key) error {
	entry, ok := r.mapping.TopicFor(key)
	if !ok || entry.Closed {
		return nil
	}
	if r.readOnly {
		return nil
	}
	if err := r.tg.CloseTopic(ctx, entry.ThreadID); err != nil {
		return r.fail(ctx, key, "closeForumTopic", err)
	}
	r.mapping.MarkClosed(key, r.clock.Now())
	r.save(ctx)
	r.log.Info("topic closed", slog.String("key", key.String()), slog.Int("thread", entry.ThreadID))
	return nil
}

// fail applies the error policy: gone topics are forgotten, closed topics
// skipped, fatal bot errors returned, everything else logged and retried
// by the next pass.
func (r *Reconciler) fail(ctx context.Context, key domain.Key, method string, err error) error {
	attrs := []any{slog.String("key", key.String()), slog.String("method", method), slog.String("err", err.Error())}
	switch {
	case errors.Is(err, domain.ErrTopicGone):
		r.mapping.Forget(key)
		r.save(ctx)
		r.log.Warn("topic gone, entry dropped", attrs...)
		return nil
	case errors.Is(err, domain.ErrTopicClosed):
		r.log.Warn("topic closed in telegram, edit skipped", attrs...)
		return nil
	case errors.Is(err, domain.ErrForbidden), errors.Is(err, domain.ErrBotUnauthorized), errors.Is(err, domain.ErrPollerConflict):
		r.log.Error("fatal telegram error", attrs...)
		return fmt.Errorf("%s: %w", method, err)
	case errors.Is(err, domain.ErrChatMigrated):
		// The daemon rewrites the config; the entry is retried afterwards.
		r.log.Warn("chat migrated", attrs...)
		return fmt.Errorf("%s: %w", method, err)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%s: %w", method, err)
	default:
		r.log.Warn("telegram call failed, will retry on next pass", attrs...)
		return nil
	}
}

func (r *Reconciler) save(ctx context.Context) {
	if err := r.store.Save(ctx, r.mapping); err != nil {
		r.log.Error("mapping save failed", slog.String("err", err.Error()))
	}
}

func sortKeys(keys []domain.Key) {
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
}
