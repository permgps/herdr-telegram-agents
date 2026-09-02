package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/app"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

type supFixture struct {
	proc  *testkit.FakeProcess
	clock *testkit.FakeClock
	sup   *app.Supervisor
}

func newSup(t *testing.T) *supFixture {
	t.Helper()
	clock := testkit.NewFakeClock(t0)
	proc := testkit.NewFakeProcess(clock.Now)
	return &supFixture{proc: proc, clock: clock, sup: app.NewSupervisor(proc, proc, clock, nil)}
}

// drive runs fn on a goroutine and advances the fake clock until it returns.
func (f *supFixture) drive(t *testing.T, fn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			return err
		default:
			f.clock.Advance(200 * time.Millisecond)
			time.Sleep(time.Millisecond)
		}
	}
	t.Fatal("operation did not finish")
	return nil
}

func TestSupervisorStartAndStatus(t *testing.T) {
	f := newSup(t)
	ctx := context.Background()
	if st := f.sup.Status(); st.Running {
		t.Fatalf("Status on empty = %+v", st)
	}
	pid, already, err := f.sup.Start(ctx)
	if err != nil || already || pid == 0 {
		t.Fatalf("Start = %d, %v, %v", pid, already, err)
	}
	if got := f.proc.Spawned(); len(got) != 1 || got[0][0] != "daemon" {
		t.Fatalf("Spawned = %v", got)
	}
	st := f.sup.Status()
	if !st.Running || st.PID != pid || !st.Since.Equal(t0) {
		t.Fatalf("Status = %+v", st)
	}
	if _, already, err := f.sup.Start(ctx); err != nil || !already {
		t.Fatalf("second Start = %v, %v", already, err)
	}
	if len(f.proc.Spawned()) != 1 {
		t.Fatal("second Start spawned again")
	}
	if got := app.Summary(st, t0.Add(90*time.Second)); !strings.Contains(got, "up 1m30s") || !strings.Contains(got, "pid") {
		t.Fatalf("Summary = %q", got)
	}
}

func TestSupervisorStartFailures(t *testing.T) {
	f := newSup(t)
	f.proc.FailSpawn(errors.New("exec failed"))
	if _, _, err := f.sup.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "exec failed") {
		t.Fatalf("Start with failing spawn = %v", err)
	}

	f = newSup(t)
	f.proc.SpawnAcquires = false
	err := f.drive(t, func() error {
		_, _, err := f.sup.Start(context.Background())
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "did not claim the pid file") {
		t.Fatalf("Start without pid claim = %v", err)
	}
}

func TestSupervisorStopGracefulAndKill(t *testing.T) {
	f := newSup(t)
	ctx := context.Background()
	if err := f.sup.Stop(ctx); !errors.Is(err, domain.ErrNotRunning) {
		t.Fatalf("Stop when idle = %v", err)
	}
	pid, _, _ := f.sup.Start(ctx)
	if err := f.sup.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if f.proc.Alive(pid) || f.sup.Status().Running {
		t.Fatalf("still running after Stop: %s", f.proc)
	}
	if got := f.proc.Signals(); len(got) != 1 || got[0] != "stop:1001" {
		t.Fatalf("Signals = %v", got)
	}

	f.proc.IgnoreStop(true)
	pid, _, _ = f.sup.Start(ctx)
	if err := f.drive(t, func() error { return f.sup.Stop(ctx) }); err != nil {
		t.Fatal(err)
	}
	if f.proc.Alive(pid) {
		t.Fatal("daemon alive after escalation")
	}
	sig := f.proc.Signals()
	if sig[len(sig)-1] != "kill:1002" || sig[len(sig)-2] != "stop:1002" {
		t.Fatalf("Signals = %v", sig)
	}
	if f.clock.Now().Sub(t0) < 10*time.Second {
		t.Fatalf("killed too early: %s", f.clock.Now().Sub(t0))
	}
}

func TestSupervisorRestartAndResync(t *testing.T) {
	f := newSup(t)
	ctx := context.Background()
	if err := f.sup.Resync(); !errors.Is(err, domain.ErrNotRunning) {
		t.Fatalf("Resync when idle = %v", err)
	}
	pid, err := f.sup.Restart(ctx)
	if err != nil || pid == 0 {
		t.Fatalf("Restart when idle = %d, %v", pid, err)
	}
	pid2, err := f.sup.Restart(ctx)
	if err != nil || pid2 == pid {
		t.Fatalf("Restart when running = %d (was %d), %v", pid2, pid, err)
	}
	if err := f.sup.Resync(); err != nil {
		t.Fatal(err)
	}
	sig := f.proc.Signals()
	if !equal(sig, []string{"stop:1001", "resync:1002"}) {
		t.Fatalf("Signals = %v", sig)
	}

	f.proc.SetUnsupported(true)
	if err := f.sup.Resync(); !errors.Is(err, domain.ErrUnsupportedPlatform) {
		t.Fatalf("Resync unsupported = %v", err)
	}
	if err := f.sup.Stop(ctx); !errors.Is(err, domain.ErrUnsupportedPlatform) {
		t.Fatalf("Stop unsupported = %v", err)
	}
}

func TestSummaryStale(t *testing.T) {
	if got := app.Summary(app.DaemonStatus{PID: 5}, t0); got != "not running (stale pid file for 5)" {
		t.Fatalf("Summary = %q", got)
	}
	if got := app.Summary(app.DaemonStatus{}, t0); got != "not running" {
		t.Fatalf("Summary = %q", got)
	}
}
