package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// revive continues a returning agent in its old topic. A matching session
// can be reassociated across terminal changes; its finished topic is reopened
// and refreshed rather than duplicated. Only a topic Telegram no longer has
// is replaced by a new one.
func (r *Reconciler) revive(ctx context.Context, a domain.Agent, entry *domain.TopicEntry) error {
	key := a.Key
	if entry.Muted {
		r.log.Info("agent returned, topic remains muted", slog.String("key", key.String()), slog.Int("thread", entry.ThreadID))
		return nil
	}
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
	r.mapping.MarkReopened(key, now)
	if err := r.save(ctx); err != nil {
		return err
	}
	// The entry still says exited, which Diff treats as final, so the
	// patch is built here: the live icon always, the name when it moved.
	name, status := domain.Desired(a)
	patch := domain.TopicPatch{Status: &status}
	if name != entry.Name {
		patch.Name = &name
	}
	if err := r.tg.EditTopic(ctx, entry.ThreadID, patch); err != nil {
		if errors.Is(err, domain.ErrTopicGone) {
			r.log.Warn("old topic gone, creating a new one", slog.String("key", key.String()), slog.Int("thread", entry.ThreadID))
			r.mapping.Forget(key)
			if err := r.save(ctx); err != nil {
				return err
			}
			return r.createTopic(ctx, a)
		}
		return r.fail(ctx, key, "editForumTopic", err)
	}
	r.mapping.Apply(key, patch, r.clock.Now())
	if err := r.save(ctx); err != nil {
		return err
	}
	r.log.Info("topic revived", slog.String("key", key.String()), slog.Int("thread", entry.ThreadID), slog.String("status", string(status)))
	return nil
}

func (r *Reconciler) createTopic(ctx context.Context, a domain.Agent) error {
	key := a.Key.String()
	if r.mapping.PendingCreates[key] {
		r.log.Error("[FIX] topic create blocked by unresolved intent", slog.String("key", key))
		return fmt.Errorf("topic creation for %s has unresolved durable intent", key)
	}
	if r.mapping.PendingCreates == nil {
		r.mapping.PendingCreates = make(map[string]bool)
	}
	r.mapping.PendingCreates[key] = true
	if err := r.save(ctx); err != nil {
		// Telegram has not been called yet. Roll back only the in-memory
		// intent; the next pass may retry after storage recovers.
		delete(r.mapping.PendingCreates, key)
		r.log.Error("[FIX] topic create intent save failed", slog.String("key", key), slog.String("err", err.Error()))
		return fmt.Errorf("persist topic create intent for %s: %w", key, err)
	}
	r.log.Debug("[FIX] topic create intent saved", slog.String("key", key))
	name, status := domain.Desired(a)
	topic, err := r.tg.CreateTopic(ctx, name, status)
	if err != nil {
		// A transport failure may have happened after Telegram created the
		// topic but before its response arrived. Keep the durable intent so
		// a restart cannot create a duplicate with an unknown thread id.
		if errors.Is(err, domain.ErrForbidden) || errors.Is(err, domain.ErrBotUnauthorized) || errors.Is(err, domain.ErrPollerConflict) || errors.Is(err, domain.ErrChatMigrated) {
			delete(r.mapping.PendingCreates, key)
			if saveErr := r.save(ctx); saveErr != nil {
				return fmt.Errorf("clear topic create intent for %s after rejected create: %w", key, saveErr)
			}
			return r.fail(ctx, a.Key, "createForumTopic", err)
		}
		r.log.Error("[FIX] topic create outcome unknown; durable intent retained", slog.String("key", key), slog.String("err", err.Error()))
		return fmt.Errorf("create topic %s outcome unknown, inspect Telegram before clearing pending intent: %w", key, err)
	}
	r.mapping.Link(a.Key, topic, a, r.clock.Now())
	delete(r.mapping.PendingCreates, key)
	if err := r.save(ctx); err != nil {
		r.log.Error("[FIX] created topic mapping save failed", slog.String("key", key), slog.Int("thread_id", topic.ThreadID), slog.String("err", err.Error()))
		return fmt.Errorf("persist created topic %s thread %d: %w", key, topic.ThreadID, err)
	}
	r.log.Debug("[FIX] created topic mapping saved", slog.String("key", key), slog.Int("thread_id", topic.ThreadID))
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
	if reason := r.blockedReason(); reason != "" {
		r.log.Info("topic edit skipped, writes paused", slog.String("key", key.String()), slog.String("reason", reason))
		return nil
	}
	r.log.Debug("topic edit", slog.String("key", key.String()), slog.Int("thread", entry.ThreadID),
		slog.Bool("name", patch.Name != nil), slog.Bool("status", patch.Status != nil))
	if err := r.tg.EditTopic(ctx, entry.ThreadID, patch); err != nil {
		return r.fail(ctx, key, "editForumTopic", err)
	}
	r.mapping.Apply(key, patch, r.clock.Now())
	return r.save(ctx)
}

// exit writes the finished marker and closes the topic. Either half may
// fail; the mapping records how far it got so Reconcile can finish later.
func (r *Reconciler) exit(ctx context.Context, key domain.Key) error {
	entry, ok := r.mapping.TopicFor(key)
	if !ok {
		return nil
	}
	if reason := r.blockedReason(); reason != "" {
		r.log.Info("topic exit skipped, writes paused", slog.String("key", key.String()), slog.String("reason", reason))
		return nil
	}
	if entry.Muted {
		// The operator closed the topic by hand; leave it exactly as it is
		// and only remember that the agent is gone.
		r.mapping.MarkExited(key, r.clock.Now())
		r.mapping.MarkClosed(key, r.clock.Now())
		if err := r.save(ctx); err != nil {
			return err
		}
		r.log.Info("topic muted, exit recorded without telegram calls", slog.String("key", key.String()), slog.Int("thread", entry.ThreadID))
		return nil
	}
	if entry.Status.Live() {
		status := domain.StatusExited
		if err := r.tg.EditTopic(ctx, entry.ThreadID, domain.TopicPatch{Status: &status}); err != nil {
			return r.fail(ctx, key, "editForumTopic", err)
		}
		r.mapping.MarkExited(key, r.clock.Now())
		if err := r.save(ctx); err != nil {
			return err
		}
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
	if err := r.save(ctx); err != nil {
		return err
	}
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
		if err := r.save(ctx); err != nil {
			return err
		}
		r.log.Debug("exited topic closed by operator", slog.String("key", key.String()), slog.Int("thread", threadID))
		return nil
	}
	r.mapping.Mute(key, r.clock.Now())
	if err := r.save(ctx); err != nil {
		return err
	}
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
	entry, _ := r.mapping.TopicFor(key)
	r.mapping.Unmute(key, r.clock.Now())
	r.mapping.MarkReopened(key, r.clock.Now())
	if err := r.save(ctx); err != nil {
		return err
	}
	r.log.Info("topic unmuted", slog.String("key", key.String()), slog.Int("thread_id", threadID))
	if a, live := r.agents[key]; live {
		r.deb.Cancel(key)
		if !entry.Status.Live() {
			return r.revive(ctx, a, entry)
		}
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
	if err := r.save(ctx); err != nil {
		return err
	}
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
	if err := r.save(ctx); err != nil {
		return false, err
	}
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
		if err := r.save(ctx); err != nil {
			return false, err
		}
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
	if err := r.save(ctx); err != nil {
		return false, err
	}
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
		if saveErr := r.save(ctx); saveErr != nil {
			return saveErr
		}
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

func (r *Reconciler) save(ctx context.Context) error {
	if err := r.store.Save(ctx, r.mapping); err != nil {
		r.dirty = true
		r.log.Error("[FIX] mapping save failed; in-memory state retained", slog.String("err", err.Error()), slog.Int("entries", len(r.mapping.Topics)), slog.Int("dashboard_id", r.mapping.Dashboard))
		r.view.publish(r.mapping)
		return fmt.Errorf("save topic mapping: %w", err)
	}
	r.dirty = false
	r.log.Debug("[FIX] mapping saved", slog.Int("entries", len(r.mapping.Topics)), slog.Int("dashboard_id", r.mapping.Dashboard))
	r.view.publish(r.mapping)
	return nil
}

func (r *Reconciler) persistPending(ctx context.Context) error {
	if !r.dirty {
		return nil
	}
	r.log.Info("[FIX] retrying pending mapping write")
	return r.save(ctx)
}

func sortKeys(keys []domain.Key) {
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
}

// KeyForThread returns the current owner topic binding for sharing panels.
func (r *Reconciler) KeyForThread(thread int) (domain.Key, bool) {
	return r.topics().KeyForThread(thread)
}
