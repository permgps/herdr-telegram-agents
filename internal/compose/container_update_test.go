package compose

import (
	"context"
	"os"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/adapters/state"
	"github.com/permgps/herdr-telegram-agents/internal/adapters/system"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func TestRecoverUpdateDoesNotRewriteLiveWorkerJournal(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	store := state.NewUpdateStore(stateDir, nil)
	job := domain.UpdateJob{ID: "live", Phase: "queued"}
	if err := store.Save(ctx, job); err != nil {
		t.Fatal(err)
	}
	worker := system.NewUpdateLock(stateDir, func(pid int) bool { return pid == os.Getpid() }, nil)
	if err := worker.Acquire(); err != nil {
		t.Fatal(err)
	}
	defer worker.Release()

	got, err := RecoverUpdate(ctx, PluginEnv{StateDir: stateDir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != "queued" || got.ErrorCode != "" {
		t.Fatalf("live worker journal = %+v, want unchanged queued job", got)
	}
}
