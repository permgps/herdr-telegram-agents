package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// Sweep deletes the closed topics of exited agents that have not changed
// for longer than maxAge and forgets their mapping entries. It runs on the
// daemon loop once at start, once a day and when the option changes. A
// zero maxAge, paused writes (sync off or rights lost) and a bot without
// the can_delete_messages right make it a no-op; the last case is logged
// at warn once per daemon run. At most sweepBatch topics go per pass, the
// rest wait for the next one. It returns how many topics were deleted.
func (r *Reconciler) Sweep(ctx context.Context, maxAge time.Duration, rights domain.Rights) (int, error) {
	if maxAge <= 0 {
		r.log.Debug("sweep skipped", slog.String("reason", "off"))
		return 0, nil
	}
	if r.blocked() {
		r.log.Debug("sweep skipped", slog.String("reason", "paused"))
		return 0, nil
	}
	if !rights.CanDeleteMessages {
		if !r.sweepRightsWarned {
			r.sweepRightsWarned = true
			r.log.Warn("sweep skipped, bot cannot delete messages", slog.String("hint", "grant the Delete messages right"))
		}
		return 0, nil
	}
	r.sweepRightsWarned = false
	now := r.clock.Now()
	keys := r.mapping.Stale(now, maxAge)
	candidates := len(keys)
	if candidates == 0 {
		r.log.Debug("sweep skipped", slog.String("reason", "no_candidates"), slog.Int("max_age_days", days(maxAge)))
		return 0, nil
	}
	cut := false
	if len(keys) > sweepBatch {
		keys = keys[:sweepBatch]
		cut = true
	}
	deleted, failed := 0, 0
	var stop error
	for _, key := range keys {
		entry, ok := r.mapping.TopicFor(key)
		if !ok {
			continue
		}
		age := days(now.Sub(entry.UpdatedAt))
		err := r.tg.DeleteTopic(ctx, entry.ThreadID)
		switch {
		case err == nil, errors.Is(err, domain.ErrTopicGone):
			r.mapping.Forget(key)
			r.save(ctx)
			deleted++
			r.log.Info("topic deleted", slog.String("key", key.String()), slog.Int("thread_id", entry.ThreadID),
				slog.Int("age_days", age), slog.Bool("already_gone", err != nil))
		case errors.Is(err, domain.ErrForbidden):
			failed++
			r.log.Warn("topic delete forbidden, sweep stopped", slog.String("key", key.String()),
				slog.Int("thread_id", entry.ThreadID), slog.String("err", err.Error()))
			stop = err
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			failed++
			stop = err
		default:
			failed++
			r.log.Warn("topic delete failed", slog.String("key", key.String()), slog.Int("thread_id", entry.ThreadID), slog.String("err", err.Error()))
		}
		if stop != nil {
			break
		}
	}
	r.log.Info("stale topics sweep", slog.Int("max_age_days", days(maxAge)), slog.Int("candidates", candidates),
		slog.Int("deleted", deleted), slog.Int("failed", failed), slog.Bool("batch_cut", cut))
	if errors.Is(stop, context.Canceled) || errors.Is(stop, context.DeadlineExceeded) {
		return deleted, stop
	}
	return deleted, nil
}

func days(d time.Duration) int { return int(d / (24 * time.Hour)) }
