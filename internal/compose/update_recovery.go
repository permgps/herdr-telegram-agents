package compose

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/permgps/herdr-telegram-agents/internal/adapters/state"
	"github.com/permgps/herdr-telegram-agents/internal/adapters/system"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// RecoverUpdate serializes startup recovery against a worker advancing its
// journal, and only recovers a job when it owns the cross-process lock.
func RecoverUpdate(ctx context.Context, env PluginEnv, log *slog.Logger) (domain.UpdateJob, error) {
	proc := system.NewProcess(env.StateDir, log)
	lock := system.NewUpdateLock(env.StateDir, proc.Alive, log)
	store := state.NewUpdateStore(env.StateDir, log)
	if err := lock.Acquire(); err != nil {
		if errors.Is(err, system.ErrUpdateLocked) {
			return store.Recover(ctx, true)
		}
		return domain.UpdateJob{}, fmt.Errorf("acquire update recovery lock: %w", err)
	}
	job, recoverErr := store.Recover(ctx, false)
	return job, errors.Join(recoverErr, lock.Release())
}
