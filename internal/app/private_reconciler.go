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

// PrivateReconciler creates only after durable intent and never closes private
// topics. Unknown creation results require an explicit owner repair.
type PrivateReconciler struct {
	pending    map[domain.Key]AgentEvent
	after      map[domain.Key]string
	retryAt    map[domain.Key]time.Time
	retryIDs   map[domain.Key]map[string]struct{}
	retryAfter map[domain.Key]string
	lifecycle  map[domain.Key]bool
	Automatic  func() bool
	Sharing    *Sharing
	Telegram   domain.DestinationTelegram
	Agent      func(domain.Key) (domain.Agent, bool)
	Now        func() time.Time
	Log        *slog.Logger
	last       map[string]string
}

func (r *PrivateReconciler) Grant(ctx context.Context, g domain.ShareGrant) error {
	a, ok := r.Agent(g.Key)
	if !ok || a.Key != g.Key || a.Status == domain.StatusExited {
		return errors.New("agent session no longer live")
	}
	if g.State == domain.GrantActive {
		return r.card(ctx, g.ID)
	}
	m, err := r.Sharing.PrepareMirror(ctx, g.ID, g.Revision, r.Now())
	if err != nil {
		return err
	}
	topic := domain.Topic{ThreadID: m.Address.ThreadID}
	if topic.ThreadID == 0 {
		topic, err = r.Telegram.CreateTopicAt(ctx, m.Address.ChatID, a.Label(), a.Status, r.Sharing.BindingGuard(g.ID, g.Revision))
		if err != nil {
			_ = r.Sharing.ChangeState(ctx, g.ID, g.Revision, domain.GrantNeedsRepair, r.Now())
			if r.Log != nil {
				r.Log.Warn("private topic creation needs repair", slog.String("grant_id", g.ID))
			}
			return errors.New("private topic creation failed or outcome unknown; repair required")
		}
	}
	if err := r.Sharing.ActivateMirror(ctx, g.ID, g.Revision, topic, r.Now()); err != nil {
		return err
	}
	if r.Log != nil {
		r.Log.Info("private mirror activated", slog.String("grant_id", g.ID), slog.Int64("recipient_id", g.RecipientID))
	}
	return r.card(ctx, g.ID)
}

func (r *PrivateReconciler) card(ctx context.Context, id string) error {
	st, ok := r.Sharing.Snapshot()
	if !ok {
		return ErrSharingUnavailable
	}
	g := st.Grants[id]
	o, ok := r.Sharing.Origin(id)
	if !ok {
		return ErrSharingUnavailable
	}
	text := fmt.Sprintf("Shared agent access: %s. Expires: %s.\nAll new output from this session is shared, including results of owner prompts. Existing history is not replayed. Screen requests may reveal older content still visible in this session.\nControl permits use of this agent's existing filesystem and tools. The owner retains control. Revocation cannot recall running work or dispatched messages.", g.Role, expiryText(g.ExpiresAt))
	_, err := r.Telegram.SendAt(ctx, o.Address, domain.Outgoing{Text: text}, r.Sharing.Guard(o, domain.ShareOutput))
	return err
}

func (r *PrivateReconciler) Observe(ctx context.Context, e AgentEvent) error {
	if r.pending == nil {
		r.pending = map[domain.Key]AgentEvent{}
		r.after = map[domain.Key]string{}
	}
	if e.Kind == AgentAppeared {
		// A resumed session can keep its pane and digest while Herdr assigns a
		// new terminal ID. Delayed lifecycle work for the old key is obsolete.
		for key := range r.pending {
			if key.PaneID == e.Agent.Key.PaneID && key.SessionDigest == e.Agent.Key.SessionDigest && key.TerminalID != e.Agent.Key.TerminalID {
				delete(r.pending, key)
				delete(r.after, key)
				delete(r.retryAt, key)
				delete(r.retryIDs, key)
				delete(r.retryAfter, key)
				delete(r.lifecycle, key)
			}
		}
	}
	r.pending[e.Agent.Key] = e
	delete(r.after, e.Agent.Key)
	delete(r.retryAt, e.Agent.Key)
	delete(r.retryIDs, e.Agent.Key)
	delete(r.retryAfter, e.Agent.Key)
	delete(r.lifecycle, e.Agent.Key)
	return r.flushEvent(ctx, e)
}

func (r *PrivateReconciler) Flush(ctx context.Context) error {
	for _, e := range r.pending {
		return r.flushEvent(ctx, e)
	}
	return nil
}

func (r *PrivateReconciler) flushEvent(ctx context.Context, e AgentEvent) error {
	if r.retryAt[e.Agent.Key].After(r.Now()) {
		return nil
	}
	if !r.lifecycle[e.Agent.Key] {
		if err := r.Sharing.Lifecycle(ctx, e, r.Now()); err != nil {
			return err
		}
		if r.lifecycle == nil {
			r.lifecycle = map[domain.Key]bool{}
		}
		r.lifecycle[e.Agent.Key] = true
	}
	st, ok := r.Sharing.Snapshot()
	if !ok || !st.MatchesBot(domain.BotIdentity{ID: r.Sharing.BotID}) || (r.Automatic != nil && !r.Automatic()) {
		delete(r.pending, e.Agent.Key)
		delete(r.after, e.Agent.Key)
		delete(r.retryAt, e.Agent.Key)
		delete(r.retryIDs, e.Agent.Key)
		delete(r.retryAfter, e.Agent.Key)
		delete(r.lifecycle, e.Agent.Key)
		return nil
	}
	for id := range r.retryIDs[e.Agent.Key] {
		if _, exists := st.Grants[id]; !exists {
			delete(r.retryIDs[e.Agent.Key], id)
		}
	}
	newIDs := make([]string, 0, len(st.Grants))
	retryIDs := make([]string, 0, len(r.retryIDs[e.Agent.Key]))
	for id := range st.Grants {
		if _, retry := r.retryIDs[e.Agent.Key][id]; retry {
			retryIDs = append(retryIDs, id)
		} else if id > r.after[e.Agent.Key] {
			newIDs = append(newIDs, id)
		}
	}
	sort.Strings(newIDs)
	sort.Strings(retryIDs)
	if len(retryIDs) > 1 {
		start := sort.Search(len(retryIDs), func(i int) bool { return retryIDs[i] > r.retryAfter[e.Agent.Key] })
		retryIDs = append(retryIDs[start:], retryIDs[:start]...)
	}
	// New grants get their first attempt promptly; retry attempts rotate so a
	// set of persistent failures cannot consume every capped pass.
	ids := append(newIDs, retryIDs...)
	calls := 0
	for _, id := range ids {
		if calls >= 4 {
			return nil
		}
		if _, retry := r.retryIDs[e.Agent.Key][id]; retry {
			if r.retryAfter == nil {
				r.retryAfter = map[domain.Key]string{}
			}
			r.retryAfter[e.Agent.Key] = id
		}
		g := st.Grants[id]
		if g.Key.PaneID != e.Agent.PaneID || (g.State != domain.GrantActive && g.SuspendReason != "exited") {
			r.advanceCursor(e.Agent.Key, id)
			delete(r.retryIDs[e.Agent.Key], id)
			continue
		}
		m := st.Mirrors[g.ID]
		if e.Agent.Key != g.Key || e.Kind == AgentGone {

			status := domain.StatusExited
			if m.Preferences.Paused {
				delete(r.retryIDs[e.Agent.Key], id)
			} else {
				calls++
				err := r.Telegram.EditTopicAt(ctx, m.Address, domain.TopicPatch{Status: &status}, r.Sharing.BindingGuard(g.ID, g.Revision))
				if err != nil {
					if privateTopicEditRetryable(err) {
						r.queueTopicRetry(e.Agent.Key, id)
					} else {
						if errors.Is(err, domain.ErrForbidden) || errors.Is(err, domain.ErrRecipientUnavailable) {
							_ = r.Sharing.Reachability(ctx, g.RecipientID, true, r.Now())
						}
						delete(r.retryIDs[e.Agent.Key], id)
					}
				} else {
					delete(r.retryIDs[e.Agent.Key], id)
				}
			}
			r.advanceCursor(e.Agent.Key, id)
			continue
		}
		if m.Preferences.Paused {
			r.advanceCursor(e.Agent.Key, id)
			delete(r.retryIDs[e.Agent.Key], id)
			continue
		}
		name := e.Agent.Label()
		if m.Preferences.Alias != "" {
			name = m.Preferences.Alias
		}
		if r.last == nil {
			r.last = map[string]string{}
		}
		signature := fmt.Sprintf("%d:%s:%s", g.Revision, name, e.Agent.Status)
		if r.last[g.ID] == signature {
			r.advanceCursor(e.Agent.Key, id)
			delete(r.retryIDs[e.Agent.Key], id)
			continue
		}
		o, _ := r.Sharing.Origin(g.ID)
		o.Revision, o.Key = g.Revision, g.Key
		calls++
		if err := r.Telegram.EditTopicAt(ctx, m.Address, domain.TopicPatch{Name: &name, Status: &e.Agent.Status}, r.Sharing.Guard(o, domain.ShareOutput)); err != nil {
			if errors.Is(err, domain.ErrTopicGone) {
				r.advanceCursor(e.Agent.Key, id)
				next, saveErr := r.Sharing.MissingMirror(ctx, g.ID, g.Revision, r.Now())
				if saveErr != nil {
					return saveErr
				}
				return r.Grant(ctx, next)
			}
			if errors.Is(err, domain.ErrForbidden) || errors.Is(err, domain.ErrRecipientUnavailable) {
				_ = r.Sharing.Reachability(ctx, g.RecipientID, true, r.Now())
			}
			if privateTopicEditRetryable(err) {
				r.queueTopicRetry(e.Agent.Key, id)
			} else {
				delete(r.retryIDs[e.Agent.Key], id)
			}
			r.advanceCursor(e.Agent.Key, id)
			continue
		}
		r.last[g.ID] = signature
		r.advanceCursor(e.Agent.Key, id)
		delete(r.retryIDs[e.Agent.Key], id)
	}
	if len(r.retryIDs[e.Agent.Key]) > 0 {
		return nil
	}
	delete(r.pending, e.Agent.Key)
	delete(r.after, e.Agent.Key)
	delete(r.retryAt, e.Agent.Key)
	delete(r.retryIDs, e.Agent.Key)
	delete(r.retryAfter, e.Agent.Key)
	delete(r.lifecycle, e.Agent.Key)
	return nil
}

func (r *PrivateReconciler) advanceCursor(key domain.Key, id string) {
	if id > r.after[key] {
		r.after[key] = id
	}
}

func (r *PrivateReconciler) queueTopicRetry(key domain.Key, id string) {
	if r.retryAt == nil {
		r.retryAt = map[domain.Key]time.Time{}
	}
	if r.retryIDs == nil {
		r.retryIDs = map[domain.Key]map[string]struct{}{}
	}
	if r.retryIDs[key] == nil {
		r.retryIDs[key] = map[string]struct{}{}
	}
	r.retryIDs[key][id] = struct{}{}
	r.retryAt[key] = r.Now().Add(time.Second)
}

func privateTopicEditRetryable(err error) bool {
	return !errors.Is(err, domain.ErrTopicGone) &&
		!errors.Is(err, domain.ErrForbidden) &&
		!errors.Is(err, domain.ErrRecipientUnavailable) &&
		!errors.Is(err, domain.ErrBotUnauthorized) &&
		!errors.Is(err, domain.ErrTopicClosed) &&
		!errors.Is(err, domain.ErrPollerConflict) &&
		!errors.Is(err, domain.ErrChatMigrated)
}

// Sweep applies the existing exited-topic retention option without group
// administrator checks. Grants remain as tombstones after topic deletion.
func (r *PrivateReconciler) Sweep(ctx context.Context, maxAge time.Duration) error {
	if maxAge <= 0 || (r.Automatic != nil && !r.Automatic()) {
		return nil
	}
	st, ok := r.Sharing.Snapshot()
	if !ok {
		return nil
	}
	count := 0
	for id, g := range st.Grants {
		if (g.SuspendReason != "exited" && g.SuspendReason != "retention") || g.ExitedAt.IsZero() || r.Now().Sub(g.ExitedAt) < maxAge {
			continue
		}
		m, exists := st.Mirrors[id]
		if !exists || m.Address.ThreadID == 0 {
			continue
		}
		if count >= 4 {
			break
		}
		count++
		if err := r.Sharing.RetireMirror(ctx, id, g.Revision, r.Now()); err != nil {
			return err
		}
		current, _ := r.Sharing.Snapshot()
		g = current.Grants[id]
		err := r.Telegram.DeleteTopicAt(ctx, m.Address, r.Sharing.BindingGuard(id, g.Revision))
		if err == nil || errors.Is(err, domain.ErrTopicGone) {
			if err := r.Sharing.ForgetMirror(ctx, id, g.Revision, r.Now()); err != nil {
				return err
			}
		}
	}
	return nil
}
