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

type daemonFixture struct {
	herdr   *testkit.FakeHerdr
	tg      *testkit.FakeTelegram
	configs *testkit.MemConfigStore
	store   *testkit.MemMappingStore
	clock   *testkit.FakeClock
	daemon  *app.Daemon
	done    chan error
	cancel  context.CancelCauseFunc
}

func newDaemon(t *testing.T) *daemonFixture {
	t.Helper()
	f := &daemonFixture{
		herdr:   testkit.NewFakeHerdr(nil),
		tg:      testkit.NewFakeTelegram(nil),
		configs: testkit.NewMemConfigStore(),
		store:   testkit.NewMemMappingStore(),
		clock:   testkit.NewFakeClock(t0),
	}
	cfg := domain.Config{Version: 1, BotToken: "t", ChatID: -1, ChatTitle: "Agents", OperatorIDs: []int64{1}}
	f.configs.Set(cfg)
	registry := app.NewRegistry(f.herdr, f.clock, nil)
	reconciler := app.NewReconciler(f.tg, f.store, domain.NewMapping(-1), f.clock, nil)
	f.daemon = app.NewDaemon(cfg, f.herdr, f.tg, registry, reconciler, f.configs, f.clock, nil)
	return f
}

func (f *daemonFixture) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancelCause(context.Background())
	f.cancel = cancel
	f.done = make(chan error, 1)
	go func() { f.done <- f.daemon.Run(ctx) }()
	t.Cleanup(func() { cancel(nil) })
}

func (f *daemonFixture) stop(t *testing.T) error {
	t.Helper()
	f.cancel(nil)
	return f.wait(t)
}

func (f *daemonFixture) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-f.done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not exit")
		return nil
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func (f *daemonFixture) waitCalls(t *testing.T, n int) {
	t.Helper()
	waitFor(t, "telegram calls", func() bool { return len(f.tg.Calls()) >= n })
}

func TestDaemonStartupReconcileAndShutdown(t *testing.T) {
	f := newDaemon(t)
	f.herdr.SetAgents([]domain.Agent{agent("p1", "t1", "reviewer", domain.StatusWorking)})
	f.start(t)
	f.waitCalls(t, 2)
	assertCalls(t, f.tg, "rights", "create:⚙️ reviewer:working")
	if w := f.herdr.WatchCalls(); len(w) != 1 || w[0][0] != "p1" {
		t.Fatalf("WatchPanes = %v", w)
	}

	// A status event flows through registry -> reconciler -> debounce -> edit.
	idle := agent("p1", "", "", domain.StatusIdle)
	f.herdr.Push(domain.HerdrEvent{Kind: domain.PaneAgentStatusChanged, PaneID: "p1", Agent: &idle})
	waitFor(t, "debounce timer", func() bool { return f.clock.Pending() >= 3 }) // registry tick, health, debounce
	f.clock.Advance(3 * time.Second)
	f.waitCalls(t, 3)
	assertCalls(t, f.tg, "rights", "create:⚙️ reviewer:working", "edit:101:name=💤 reviewer,status=idle")

	if err := f.stop(t); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if f.store.SaveCount() != 2 {
		t.Fatalf("saves = %d", f.store.SaveCount())
	}
}

func TestDaemonRightsLostAndRegained(t *testing.T) {
	f := newDaemon(t)
	f.tg.SetRights(domain.Rights{IsForum: true, IsAdmin: true, CanManageTopics: false}, nil)
	f.herdr.SetAgents([]domain.Agent{agent("p1", "t1", "a", domain.StatusWorking)})
	f.start(t)
	f.waitCalls(t, 1)
	waitFor(t, "notification", func() bool { return len(f.herdr.Notifications()) == 1 })
	if n := f.herdr.Notifications()[0]; !strings.Contains(n.Body, "Manage topics") {
		t.Fatalf("notification = %+v", n)
	}
	assertCalls(t, f.tg, "rights")

	f.tg.Push(domain.RightsChanged{CanManageTopics: true})
	f.waitCalls(t, 2)
	assertCalls(t, f.tg, "rights", "create:⚙️ a:working")

	f.tg.Push(domain.RightsChanged{CanManageTopics: false})
	waitFor(t, "second notification", func() bool { return len(f.herdr.Notifications()) == 2 })
	f.herdr.SetAgents([]domain.Agent{agent("p1", "t1", "a", domain.StatusWorking), agent("p2", "t2", "b", domain.StatusIdle)})
	f.daemon.Resync()
	waitFor(t, "resync snapshot", func() bool { return f.herdr.ListCalls() >= 2 })
	time.Sleep(20 * time.Millisecond)
	assertCalls(t, f.tg, "rights", "create:⚙️ a:working")

	f.tg.Push(domain.RightsChanged{CanManageTopics: true})
	f.waitCalls(t, 3)
	assertCalls(t, f.tg, "rights", "create:⚙️ a:working", "create:💤 b:idle")
	if err := f.stop(t); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonResyncHealsDrift(t *testing.T) {
	f := newDaemon(t)
	f.herdr.SetAgents([]domain.Agent{agent("p1", "t1", "a", domain.StatusWorking)})
	f.start(t)
	f.waitCalls(t, 2)

	f.herdr.SetAgents(nil)
	f.daemon.Resync()
	f.waitCalls(t, 4)
	assertCalls(t, f.tg, "rights", "create:⚙️ a:working", "edit:101:name=🏁 a,status=exited", "close:101")
	if err := f.stop(t); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonExitsWhenSocketGone(t *testing.T) {
	f := newDaemon(t)
	f.daemon.SocketGrace = 60 * time.Second
	f.start(t)
	f.waitCalls(t, 1)
	waitFor(t, "initial snapshot", func() bool { return f.herdr.ListCalls() >= 1 })

	f.herdr.FailList(domain.ErrDisconnected)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-f.done:
			if err != nil {
				t.Fatalf("Run = %v, want nil", err)
			}
			if elapsed := f.clock.Now().Sub(t0); elapsed < 60*time.Second || elapsed > 90*time.Second {
				t.Fatalf("exited after %s", elapsed)
			}
			if f.herdr.ListCalls() < 5 {
				t.Fatalf("expected retries while down, got %d list calls", f.herdr.ListCalls())
			}
			return
		default:
			f.clock.Advance(5 * time.Second)
			time.Sleep(2 * time.Millisecond)
		}
	}
	t.Fatal("daemon kept running with a dead socket")
}

func TestDaemonRecoversWhenSocketReturns(t *testing.T) {
	f := newDaemon(t)
	f.start(t)
	f.waitCalls(t, 1)
	f.herdr.FailList(domain.ErrDisconnected)
	for range 4 {
		f.clock.Advance(5 * time.Second)
		time.Sleep(2 * time.Millisecond)
	}
	f.herdr.FailList(nil)
	f.herdr.SetAgents([]domain.Agent{agent("p1", "t1", "a", domain.StatusWorking)})
	f.clock.Advance(5 * time.Second)
	f.waitCalls(t, 2)
	assertCalls(t, f.tg, "rights", "create:⚙️ a:working")
	for range 20 {
		f.clock.Advance(5 * time.Second)
		time.Sleep(2 * time.Millisecond)
	}
	if err := f.stop(t); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonFatalTelegramErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		hint string
	}{
		{"forbidden", domain.ErrForbidden, "removed from"},
		{"unauthorized", domain.ErrBotUnauthorized, "setup action"},
		{"conflict", domain.ErrPollerConflict, "another process"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newDaemon(t)
			f.herdr.SetAgents([]domain.Agent{agent("p1", "t1", "a", domain.StatusWorking)})
			f.tg.FailNext("create", tt.err)
			f.start(t)
			err := f.wait(t)
			if !errors.Is(err, tt.err) {
				t.Fatalf("Run = %v, want %v", err, tt.err)
			}
			n := f.herdr.Notifications()
			if len(n) != 1 || !strings.Contains(n[0].Body, tt.hint) {
				t.Fatalf("notifications = %+v", n)
			}
		})
	}

	t.Run("rights check forbidden", func(t *testing.T) {
		f := newDaemon(t)
		f.tg.SetRights(domain.Rights{}, domain.ErrForbidden)
		f.start(t)
		if err := f.wait(t); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("Run = %v", err)
		}
	})

	t.Run("poller fatal via context cause", func(t *testing.T) {
		f := newDaemon(t)
		f.start(t)
		f.waitCalls(t, 1)
		f.cancel(domain.ErrPollerConflict)
		if err := f.wait(t); !errors.Is(err, domain.ErrPollerConflict) {
			t.Fatalf("Run = %v", err)
		}
	})
}

func TestDaemonChatMigration(t *testing.T) {
	f := newDaemon(t)
	f.herdr.SetAgents([]domain.Agent{agent("p1", "t1", "a", domain.StatusWorking)})
	f.tg.FailNext("create", &domain.ChatMigratedError{NewChatID: -777})
	f.start(t)
	waitFor(t, "config rewrite", func() bool { return f.configs.SaveCount() == 1 })
	cfg, _ := f.configs.Load(context.Background())
	if cfg.ChatID != -777 {
		t.Fatalf("chat id after migration = %d", cfg.ChatID)
	}
	// The daemon keeps running and the next pass retries the create.
	f.daemon.Resync()
	f.waitCalls(t, 3)
	assertCalls(t, f.tg, "rights", "create:⚙️ a:working", "create:⚙️ a:working")
	if err := f.stop(t); err != nil {
		t.Fatalf("Run = %v", err)
	}
}
