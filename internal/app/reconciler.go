package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
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

	deb    *debouncer
	agents map[domain.Key]domain.Agent // last known agent per key, for fire-time diffs
	// rightsLost pauses writes while the bot cannot manage topics; paused
	// reads the operator's sync switch. Either blocks every Telegram write.
	rightsLost bool
	paused     func() bool
	// pausedLogged keeps the "sync off" skip at one log line per pause.
	pausedLogged bool
	force        bool // set by Resync for the duration of one pass
	// view is the read-only copy of the mapping the bridge goroutine
	// consults; it is republished after every save.
	view *topicView
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
		tg:      tg,
		herdr:   herdr,
		store:   store,
		mapping: mapping,
		clock:   clock,
		log:     log,
		deb:     newDebouncer(clock, editDebounce, log),
		agents:  map[domain.Key]domain.Agent{},
		paused:  paused,
		view:    newTopicView(),
	}
	r.view.publish(mapping)
	return r
}

// topics returns the goroutine-safe read model of the mapping.
func (r *Reconciler) topics() *topicView { return r.view }

// SetDebounce overrides the edit delay (tests).
func (r *Reconciler) SetDebounce(d time.Duration) { r.deb.delay = d }

// Due delivers keys whose debounced edit is ready; call Fire for each.
func (r *Reconciler) Due() <-chan domain.Key { return r.deb.Due() }

// Mapping exposes the aggregate for status output and tests.
func (r *Reconciler) Mapping() *domain.Mapping { return r.mapping }

// ReadOnly reports whether writes are paused because the bot lost the
// "Manage topics" right (the operator's sync switch is a separate reason,
// see blocked).
func (r *Reconciler) ReadOnly() bool { return r.rightsLost }

// blocked reports whether Telegram writes are off for any reason: lost
// rights or the operator's sync switch. The sync-off case is logged once
// per pause.
func (r *Reconciler) blocked() bool {
	if r.rightsLost {
		return true
	}
	if !r.paused() {
		r.pausedLogged = false
		return false
	}
	if !r.pausedLogged {
		r.pausedLogged = true
		r.log.Info("reconcile paused: sync is off (/options)")
	}
	return true
}

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
	if r.blocked() {
		// Lost rights are worth a line per pass; the operator's own switch
		// was logged once when it took effect.
		level := slog.LevelDebug
		if r.rightsLost {
			level = slog.LevelInfo
		}
		r.log.Log(ctx, level, "reconcile skipped, writes paused", slog.Int("agents", len(live)), slog.Bool("rights_lost", r.rightsLost))
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
	if r.blocked() {
		r.log.Info("topic create skipped, writes paused", slog.String("key", a.Key.String()))
		return nil
	}
	if entry, ok := r.mapping.TopicFor(a.Key); ok && !entry.Status.Live() {
		return r.revive(ctx, a, entry)
	}
	return r.createTopic(ctx, a)
}

// revive continues a returning agent in its old topic. The key is pane plus
// terminal, so the same key after an exit means the agent was restarted in
// place (claude --resume): the finished topic is reopened, unmuted and
// refreshed rather than duplicated. Only a topic Telegram no longer has is
// replaced by a new one.
func (r *Reconciler) revive(ctx context.Context, a domain.Agent, entry *domain.TopicEntry) error {
	key := a.Key
	r.log.Info("agent returned, reusing its topic", slog.String("key", key.String()), slog.Int("thread", entry.ThreadID),
		slog.Bool("closed", entry.Closed), slog.Bool("muted", entry.Muted))
	if entry.Closed {
		if err := r.tg.ReopenTopic(ctx, entry.ThreadID); err != nil {
			if errors.Is(err, domain.ErrTopicGone) {
				r.log.Warn("old topic gone, creating a new one", slog.String("key", key.String()), slog.Int("thread", entry.ThreadID))
				r.mapping.Forget(key)
				return r.createTopic(ctx, a)
			}
			return r.fail(ctx, key, "reopenForumTopic", err)
		}
	}
	now := r.clock.Now()
	r.mapping.Unmute(key, now)
	r.mapping.MarkReopened(key, now)
	r.save(ctx)
	// The entry still says exited, which Diff treats as final, so the
	// patch is built here: the live icon always, the name when it moved.
	name, status := domain.Desired(a)
	patch := domain.TopicPatch{Status: &status}
	if name != entry.Name {
		patch.Name = &name
	}
	if err := r.tg.EditTopic(ctx, entry.ThreadID, patch); err != nil {
		return r.fail(ctx, key, "editForumTopic", err)
	}
	r.mapping.Apply(key, patch, r.clock.Now())
	r.save(ctx)
	r.log.Info("topic revived", slog.String("key", key.String()), slog.Int("thread", entry.ThreadID), slog.String("status", string(status)))
	return nil
}

func (r *Reconciler) createTopic(ctx context.Context, a domain.Agent) error {
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
	if entry.Muted && !r.force {
		r.log.Debug("topic muted, edit skipped", slog.String("key", key.String()), slog.Int("thread", entry.ThreadID))
		return nil
	}
	patch, changed := r.mapping.Diff(key, a)
	if r.force && entry.Status.Live() {
		name, status := domain.Desired(a)
		patch, changed = domain.TopicPatch{Name: &name, Status: &status}, true
	}
	if !changed {
		return nil
	}
	if r.blocked() {
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
	if r.blocked() {
		r.log.Info("topic exit skipped, writes paused", slog.String("key", key.String()))
		return nil
	}
	if entry.Muted {
		// The operator closed the topic by hand; leave it exactly as it is
		// and only remember that the agent is gone.
		r.mapping.MarkExited(key, r.clock.Now())
		r.mapping.MarkClosed(key, r.clock.Now())
		r.save(ctx)
		r.log.Info("topic muted, exit recorded without telegram calls", slog.String("key", key.String()), slog.Int("thread", entry.ThreadID))
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
	if r.blocked() {
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

// OnTopicClosed handles an operator closing a topic by hand: the entry is
// muted so the mirror stops editing and posting until the topic is
// reopened. Closing an already exited topic only records the closure.
func (r *Reconciler) OnTopicClosed(ctx context.Context, threadID int) error {
	key, ok := r.mapping.KeyForThread(threadID)
	if !ok {
		r.log.Debug("topic closed for unknown thread", slog.Int("thread", threadID))
		return nil
	}
	entry, _ := r.mapping.TopicFor(key)
	if !entry.Status.Live() {
		r.mapping.MarkClosed(key, r.clock.Now())
		r.save(ctx)
		r.log.Debug("exited topic closed by operator", slog.String("key", key.String()), slog.Int("thread", threadID))
		return nil
	}
	r.mapping.Mute(key, r.clock.Now())
	r.save(ctx)
	r.log.Info("topic muted", slog.String("key", key.String()), slog.Int("thread_id", threadID))
	return nil
}

// OnTopicReopened lifts the mute: a live agent gets its name and icon
// rewritten so the topic is current again, an exited one is marked with
// the finish flag and closed again.
func (r *Reconciler) OnTopicReopened(ctx context.Context, threadID int) error {
	key, ok := r.mapping.KeyForThread(threadID)
	if !ok {
		r.log.Debug("topic reopened for unknown thread", slog.Int("thread", threadID))
		return nil
	}
	r.mapping.Unmute(key, r.clock.Now())
	r.mapping.MarkReopened(key, r.clock.Now())
	r.save(ctx)
	r.log.Info("topic unmuted", slog.String("key", key.String()), slog.Int("thread_id", threadID))
	if _, live := r.agents[key]; live {
		r.deb.Cancel(key)
		return r.forced(func() error { return r.edit(ctx, key) })
	}
	return r.finish(ctx, key)
}

// finish writes the exited icon regardless of what the mapping believes
// (a muted exit never reached Telegram) and closes the topic.
func (r *Reconciler) finish(ctx context.Context, key domain.Key) error {
	entry, ok := r.mapping.TopicFor(key)
	if !ok || r.blocked() {
		return nil
	}
	status := domain.StatusExited
	if err := r.tg.EditTopic(ctx, entry.ThreadID, domain.TopicPatch{Status: &status}); err != nil {
		return r.fail(ctx, key, "editForumTopic", err)
	}
	r.mapping.MarkExited(key, r.clock.Now())
	r.save(ctx)
	r.log.Info("topic marked exited after reopen", slog.String("key", key.String()), slog.Int("thread", entry.ThreadID))
	return r.close(ctx, key)
}

// forced runs fn with the resync flag set so edits bypass the diff and the
// mute check.
func (r *Reconciler) forced(fn func() error) error {
	was := r.force
	r.force = true
	defer func() { r.force = was }()
	return fn()
}

// OnTopicRenamed mirrors an operator's topic rename back to Herdr. The
// "<workspace> · " prefix is stripped when present. The remainder goes to
// whatever forms the agent part of the label: the tab label for an agent
// without a custom name (tab.rename; an empty remainder or the current
// label is ignored), the custom name otherwise (agent.rename; an empty
// remainder, the tab label or the agent kind clears it). The mapping keeps
// the new topic name so no edit is sent for the name the operator just
// typed; a snapshot, requested by the caller when Herdr was changed, or a
// scheduled edit settles the topic on the canonical form. It reports
// whether that snapshot is wanted.
func (r *Reconciler) OnTopicRenamed(ctx context.Context, threadID int, name string) (bool, error) {
	key, ok := r.mapping.KeyForThread(threadID)
	if !ok {
		r.log.Debug("topic renamed for unknown thread", slog.Int("thread", threadID), slog.String("name", name))
		return false, nil
	}
	entry, _ := r.mapping.TopicFor(key)
	a, live := r.agents[key]
	if !live || !entry.Status.Live() {
		r.log.Debug("rename of exited topic ignored", slog.String("key", key.String()), slog.Int("thread", threadID), slog.String("name", name))
		return false, nil
	}
	rest := agentPart(a, name)
	if a.Name == "" && a.TabID != "" {
		return r.renameTab(ctx, key, a, entry, name, rest)
	}
	var wire *string
	if rest != "" && rest != a.TabLabel && rest != a.Kind {
		wire = &rest
	} else {
		rest = ""
	}
	if err := r.herdr.Rename(ctx, key.PaneID, wire); err != nil {
		return false, r.renameFailed(ctx, key, entry, name, "agent", err)
	}
	a.Name = rest
	r.agents[key] = a
	entry.Name = name
	entry.UpdatedAt = r.clock.Now()
	r.save(ctx)
	r.log.Info("agent renamed from telegram", slog.String("key", key.String()), slog.Int("thread_id", threadID),
		slog.String("name", name), slog.String("agent_name", rest))
	return true, nil
}

// renameTab is the tab-label half of OnTopicRenamed. An ignored rename
// still records the typed name and schedules an edit, so a topic renamed
// to something the tab already means goes back to the canonical form.
func (r *Reconciler) renameTab(ctx context.Context, key domain.Key, a domain.Agent, entry *domain.TopicEntry, name, label string) (bool, error) {
	if label == "" || label == a.TabLabel {
		entry.Name = name
		entry.UpdatedAt = r.clock.Now()
		r.save(ctx)
		r.deb.Schedule(key)
		r.log.Debug("topic rename ignored, tab label unchanged", slog.String("key", key.String()),
			slog.Int("thread", entry.ThreadID), slog.String("name", name))
		return false, nil
	}
	if err := r.herdr.RenameTab(ctx, a.TabID, label); err != nil {
		return false, r.renameFailed(ctx, key, entry, name, "tab", err)
	}
	a.TabLabel = label
	r.agents[key] = a
	entry.Name = name
	entry.UpdatedAt = r.clock.Now()
	r.save(ctx)
	r.deb.Schedule(key)
	r.log.Info("tab renamed from telegram", slog.String("key", key.String()), slog.Int("thread_id", entry.ThreadID),
		slog.String("tab_id", a.TabID), slog.String("name", name), slog.String("label", label))
	return true, nil
}

// renameFailed logs a Herdr rename failure and tells the operator why.
func (r *Reconciler) renameFailed(ctx context.Context, key domain.Key, entry *domain.TopicEntry, name, what string, err error) error {
	r.log.Warn(what+" rename from telegram failed", slog.String("key", key.String()), slog.Int("thread_id", entry.ThreadID),
		slog.String("name", name), slog.String("err", err.Error()))
	if _, sendErr := r.tg.Send(ctx, domain.Outgoing{ThreadID: entry.ThreadID, Text: "rename failed: " + failureReason(err)}); sendErr != nil {
		r.log.Warn("rename failure note not sent", slog.String("key", key.String()), slog.String("err", sendErr.Error()))
	}
	return nil
}

// agentPart strips the workspace prefix Herdr's panel shows from a topic
// name typed by an operator and trims the remainder.
func agentPart(a domain.Agent, name string) string {
	ws := a.WorkspaceLabel
	if ws == "" {
		ws = a.WorkspaceID
	}
	rest := strings.TrimSpace(domain.StripPrefix(name))
	if ws != "" {
		if cut, ok := strings.CutPrefix(rest, ws); ok {
			trimmed := strings.TrimLeft(cut, " ")
			if trimmed == "" || strings.HasPrefix(trimmed, "·") {
				rest = strings.TrimSpace(strings.TrimPrefix(trimmed, "·"))
			}
		}
	}
	return rest
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
	r.view.publish(r.mapping)
	if err := r.store.Save(ctx, r.mapping); err != nil {
		r.log.Error("mapping save failed", slog.String("err", err.Error()))
	}
}

func sortKeys(keys []domain.Key) {
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
}
