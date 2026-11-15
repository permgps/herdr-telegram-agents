package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

type stoppedPhaseFailureStore struct {
	memoryUpdateStore
	cancel       context.CancelFunc
	failRecovery bool
}

func (s *stoppedPhaseFailureStore) Save(ctx context.Context, j domain.UpdateJob) error {
	if j.Phase == "daemon_stopped" {
		if s.cancel != nil {
			s.cancel()
		}
		return errors.New("injected journal failure")
	}
	if s.failRecovery && (j.Phase == "failed" || j.Phase == "stuck") {
		return errors.New("injected recovery journal failure")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return s.memoryUpdateStore.Save(ctx, j)
}

func TestUpdateWorkerStoppedJournalFailure(t *testing.T) {
	for _, tc := range []struct {
		name                                      string
		running, cancel, spawnFails, failRecovery bool
	}{
		{"running", true, false, false, false},
		{"cancelled", true, true, false, false},
		{"restart fails", true, false, true, false},
		{"previously stopped", false, false, false, false},
		{"recovery journal fails", true, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := testkit.NewFakeClock(time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC))
			proc := testkit.NewFakeProcess(clock.Now)
			if tc.running {
				if _, err := proc.Spawn(context.Background(), []string{"daemon"}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.spawnFails {
				proc.FailSpawn(errors.New("injected spawn failure"))
			}
			proc.SetStatusLine("version=1.0.0 pid=1002 herdr=ok telegram=ready")
			reader := &changingReader{value: domain.PluginInstallation{Root: "/old", ManifestVersion: "1.0.0", BinaryVersion: "1.0.0"}}
			installer := &fakeUpdateInstaller{reader: reader, proc: proc}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := &stoppedPhaseFailureStore{memoryUpdateStore: memoryUpdateStore{job: domain.UpdateJob{ID: "job", Phase: "queued", OldVersion: "1.0.0", TargetVersion: "1.2.0", SourceRoot: "/old", PriorRunning: tc.running}}}
			store.failRecovery = tc.failRecovery
			if tc.cancel {
				store.cancel = cancel
			}
			worker := &UpdateWorker{Store: store, Lock: &memoryUpdateLock{}, Installer: installer, Reader: reader, Supervisor: NewSupervisor(proc, proc, clock, nil)}
			job, err := worker.Run(ctx, "job")
			if err == nil || !strings.Contains(err.Error(), "journal") {
				t.Fatalf("job=%+v err=%v", job, err)
			}
			if got := proc.Held() != 0; got != (tc.running && !tc.spawnFails) {
				t.Fatalf("running=%v; job=%+v err=%v", got, job, err)
			}
			if tc.spawnFails && !strings.Contains(err.Error(), "spawn") {
				t.Fatalf("restart error missing: %v", err)
			}
			if !tc.spawnFails && !tc.failRecovery && (!job.Terminal() || !store.job.Terminal()) {
				t.Fatalf("recovery is not terminal: job=%+v saved=%+v", job, store.job)
			}
			if tc.failRecovery && !strings.Contains(err.Error(), "injected recovery journal failure") {
				t.Fatalf("recovery journal error missing: %v", err)
			}
			if len(reader.calls) != 0 {
				t.Fatalf("installation unexpectedly invoked: %v", reader.calls)
			}
		})
	}
}
