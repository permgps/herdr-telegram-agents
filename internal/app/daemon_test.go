package app_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/app"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

type daemonFixture struct {
	herdr    *testkit.FakeHerdr
	tg       *testkit.FakeTelegram
	configs  *testkit.MemConfigStore
	store    *testkit.MemMappingStore
	clock    *testkit.FakeClock
	capture  *app.Capture
	options  *testkit.MemOptionsStore
	opts     *app.Options
	rec      *app.Reconciler
	idle     *testkit.FakeIdle
	presence *app.Presence
	daemon   *app.Daemon
	done     chan error
	cancel   context.CancelCauseFunc
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
	f.options = testkit.NewMemOptionsStore()
	f.opts = app.NewOptions(f.options.Stored(), f.options, func(string) []string { return f.tg.IconPack() }, nil)
	reconciler := app.NewReconciler(f.tg, f.herdr, f.store, domain.NewMapping(-1), f.opts, f.clock, nil)
	f.rec = reconciler
	f.capture = app.NewCapture(f.herdr, registry.Live, f.clock, nil)
	bridge := app.NewBridge(cfg, f.herdr, f.tg, registry, reconciler, f.capture, f.opts, nil, f.clock, nil)
	f.idle = testkit.NewFakeIdle(0)
	f.idle.Unsupported() // quiet mode stays off unless a test sets an idle time
	f.presence = app.NewPresence(f.idle, f.opts, f.clock, nil)
	f.daemon = app.NewDaemon(cfg, f.herdr, f.tg, registry, reconciler, bridge, f.capture, f.configs, f.opts, f.presence, f.clock, nil)
	f.daemon.Version = "1.2.3"
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

const (
	started1 = "send:0:▶️ Telegram Agents 1.2.3 started: 1 agent"
	started0 = "send:0:▶️ Telegram Agents 1.2.3 started: 0 agents"
	stopping = "send:0:⏹ Telegram Agents 1.2.3 stopping"
)

func (f *daemonFixture) waitCalls(t *testing.T, n int) {
	t.Helper()
	waitFor(t, "telegram calls", func() bool { return len(f.tg.Calls()) >= n })
}

func TestDaemonStartupReconcileAndShutdown(t *testing.T) {
	f := newDaemon(t)
	f.herdr.SetAgents([]domain.Agent{agent("p1", "t1", "reviewer", domain.StatusWorking)})
	f.start(t)
	f.waitCalls(t, 3)
	assertCalls(t, f.tg, "rights", "create:reviewer:working", started1)
	if w := f.herdr.WatchCalls(); len(w) != 1 || w[0][0] != "p1" {
		t.Fatalf("WatchPanes = %v", w)
	}

	// A status event flows through registry -> reconciler -> debounce -> edit.
	idle := agent("p1", "", "", domain.StatusIdle)
	f.herdr.Push(domain.HerdrEvent{Kind: domain.PaneAgentStatusChanged, PaneID: "p1", Agent: &idle})
	waitFor(t, "debounce timer", func() bool { return f.clock.Pending() >= 6 }) // registry tick, health, sweep, capture, debounce
	f.clock.Advance(3 * time.Second)
	f.waitCalls(t, 4)
	assertCalls(t, f.tg, "rights", "create:reviewer:working", started1, "edit:101:status=idle")

	if err := f.stop(t); err != nil {
		t.Fatalf("Run = %v", err)
	}
	assertCalls(t, f.tg, "rights", "create:reviewer:working", started1, "edit:101:status=idle", stopping)
	if f.store.SaveCount() != 2 {
		t.Fatalf("saves = %d", f.store.SaveCount())
	}
}

func TestDaemonRightsLostAndRegained(t *testing.T) {
	f := newDaemon(t)
	f.tg.SetRights(domain.Rights{IsForum: true, IsAdmin: true, CanManageTopics: false}, nil)
	f.herdr.SetAgents([]domain.Agent{agent("p1", "t1", "a", domain.StatusWorking)})
	f.start(t)
	f.waitCalls(t, 2)
	waitFor(t, "notification", func() bool { return len(f.herdr.Notifications()) == 1 })
	if n := f.herdr.Notifications()[0]; !strings.Contains(n.Body, "Manage topics") {
		t.Fatalf("notification = %+v", n)
	}
	assertCalls(t, f.tg, "rights", started1)

	regained := "send:0:✅ \"Manage topics\" right regained, topics are updated again"
	lost := "send:0:⚠️ the bot lost the \"Manage topics\" right; topics are not updated until it is granted again"
	f.tg.Push(domain.RightsChanged{CanManageTopics: true})
	f.waitCalls(t, 4)
	assertCalls(t, f.tg, "rights", started1, regained, "create:a:working")

	f.tg.Push(domain.RightsChanged{CanManageTopics: false})
	waitFor(t, "second notification", func() bool { return len(f.herdr.Notifications()) == 2 })
	f.waitCalls(t, 5)
	f.herdr.SetAgents([]domain.Agent{agent("p1", "t1", "a", domain.StatusWorking), agent("p2", "t2", "b", domain.StatusIdle)})
	f.daemon.Resync()
	waitFor(t, "resync snapshot", func() bool { return f.herdr.ListCalls() >= 2 })
	time.Sleep(20 * time.Millisecond)
	assertCalls(t, f.tg, "rights", started1, regained, "create:a:working", lost)

	f.tg.Push(domain.RightsChanged{CanManageTopics: true})
	f.waitCalls(t, 7)
	assertCalls(t, f.tg, "rights", started1, regained, "create:a:working", lost, regained, "create:b:idle")
	if err := f.stop(t); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonResyncHealsDrift(t *testing.T) {
	f := newDaemon(t)
	f.herdr.SetAgents([]domain.Agent{agent("p1", "t1", "a", domain.StatusWorking)})
	f.start(t)
	f.waitCalls(t, 3)

	f.herdr.SetAgents(nil)
	f.daemon.Resync()
	f.waitCalls(t, 5)
	assertCalls(t, f.tg, "rights", "create:a:working", started1, "edit:101:status=exited", "close:101")
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
	f.waitCalls(t, 3)
	assertCalls(t, f.tg, "rights", started0, "create:a:working")
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
	f.waitCalls(t, 4)
	assertCalls(t, f.tg, "rights", "create:a:working", started1, "create:a:working")
	if err := f.stop(t); err != nil {
		t.Fatalf("Run = %v", err)
	}
}

func TestDaemonBlockedScreenIsPosted(t *testing.T) {
	f := newDaemon(t)
	f.herdr.SetAgents([]domain.Agent{agent("p1", "t1", "reviewer", domain.StatusWorking)})
	f.herdr.SetScreen("p1", "Allow Bash?\n1. Yes\n2. No")
	f.start(t)
	f.waitCalls(t, 3)

	blocked := agent("p1", "", "", domain.StatusBlocked)
	f.herdr.Push(domain.HerdrEvent{Kind: domain.PaneAgentStatusChanged, PaneID: "p1", Agent: &blocked})
	// registry tick, health, capture, edit debounce, screen settle
	waitFor(t, "settle timer", func() bool { return f.clock.Pending() >= 7 })
	f.clock.Advance(1500 * time.Millisecond)
	f.waitCalls(t, 4)
	sent := f.tg.Sent()
	if len(sent) != 2 || sent[1].ThreadID != 101 || sent[1].Text != "Allow Bash?\n1. Yes\n2. No" || !sent[1].Notify || !sent[1].Code {
		t.Fatalf("Sent = %+v", sent)
	}

	// The operator answers from the phone: a short reply becomes keys.
	f.tg.Push(domain.TopicMessage{ThreadID: 101, MessageID: 9, FromID: 1, Text: "1"})
	waitFor(t, "keys", func() bool { return len(f.herdr.Keys()) == 1 })
	if k := f.herdr.Keys()[0]; k.Target != "p1" || len(k.Keys) != 1 || k.Keys[0] != "1" {
		t.Fatalf("Keys = %+v", k)
	}
	// Delivery is silent: the keys produce no Telegram call.
	for _, c := range f.tg.Calls() {
		if strings.HasPrefix(c, "react:") {
			t.Fatalf("unexpected reaction %q", c)
		}
	}
	if err := f.stop(t); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonTopicMessageAndGeneralStatus(t *testing.T) {
	f := newDaemon(t)
	f.herdr.SetAgents([]domain.Agent{agent("p1", "t1", "reviewer", domain.StatusIdle)})
	f.start(t)
	f.waitCalls(t, 3)

	f.tg.Push(domain.TopicMessage{ThreadID: 101, MessageID: 5, FromID: 1, Text: "run the tests"})
	waitFor(t, "prompt", func() bool { return len(f.herdr.Prompts()) == 1 })
	if p := f.herdr.Prompts()[0]; p != "p1: run the tests" {
		t.Fatalf("prompt = %q", p)
	}

	f.tg.Push(domain.GeneralCommand{MessageID: 6, FromID: 1, Text: "/status"})
	f.waitCalls(t, 5) // the prompt's 👀, then the status reply
	sent := f.tg.Sent()
	last := sent[len(sent)-1]
	if last.ThreadID != 0 || !last.HTML || last.ReplyTo != 6 || !strings.Contains(last.Text, "1 agent\n✅ <a href=\"https://t.me/c/1/101\">reviewer</a>") {
		t.Fatalf("status reply = %+v", last)
	}
	if err := f.stop(t); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonTopicRenameCloseReopen(t *testing.T) {
	f := newDaemon(t)
	f.herdr.SetAgents([]domain.Agent{agent("p1", "t1", "reviewer", domain.StatusIdle)})
	f.start(t)
	f.waitCalls(t, 3)
	lists := f.herdr.ListCalls()

	f.tg.Push(domain.TopicRenamed{ThreadID: 101, Name: "fixer"})
	waitFor(t, "rename", func() bool { return len(f.herdr.Renames()) == 1 })
	if r := f.herdr.Renames()[0]; r.Target != "p1" || r.Name == nil || *r.Name != "fixer" {
		t.Fatalf("Renames = %+v", r)
	}
	waitFor(t, "snapshot after rename", func() bool { return f.herdr.ListCalls() > lists })
	// A pane.updated event, which never carries the name, must not flap
	// the topic back to the old label.
	stale := agent("p1", "t1", "", domain.StatusIdle)
	f.herdr.Push(domain.HerdrEvent{Kind: domain.PaneUpdated, PaneID: "p1", Agent: &stale})
	time.Sleep(50 * time.Millisecond)
	f.clock.Advance(4 * time.Second)
	time.Sleep(50 * time.Millisecond)
	for _, c := range f.tg.Calls() {
		if strings.Contains(c, "name=reviewer") {
			t.Fatalf("topic flapped back after rename: %v", f.tg.Calls())
		}
	}

	f.tg.Push(domain.TopicClosed{ThreadID: 101})
	waitFor(t, "mute", func() bool {
		e, ok := f.store.Saved().TopicFor(domain.Key{PaneID: "p1", TerminalID: "t1"})
		return ok && e.Muted
	})
	f.tg.Push(domain.TopicReopened{ThreadID: 101})
	f.waitCalls(t, 4)
	if calls := f.tg.Calls(); !strings.HasPrefix(calls[3], "edit:101:name=") {
		t.Fatalf("reopen did not force an edit: %v", calls)
	}
	if err := f.stop(t); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonBridgeFatalStopsDaemon(t *testing.T) {
	f := newDaemon(t)
	f.herdr.SetAgents([]domain.Agent{agent("p1", "t1", "reviewer", domain.StatusIdle)})
	f.start(t)
	f.waitCalls(t, 3)
	f.tg.FailNext("send", domain.ErrForbidden)
	f.tg.Push(domain.TopicMessage{ThreadID: 101, MessageID: 5, FromID: 1, Text: "/help"})
	if err := f.wait(t); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("Run = %v, want ErrForbidden", err)
	}
}

func TestDaemonMarksHistoryOnWorking(t *testing.T) {
	f := newDaemon(t)
	f.herdr.SetAgents([]domain.Agent{agent("p1", "t1", "reviewer", domain.StatusIdle)})
	f.herdr.SetScreen("p1", "prompt typed in herdr")
	f.start(t)
	f.waitCalls(t, 3)
	key := domain.Key{PaneID: "p1", TerminalID: "t1"}
	if _, marked, err := f.capture.Since(context.Background(), key); err != nil || marked {
		t.Fatalf("Since before working = marked %v, err %v", marked, err)
	}
	working := agent("p1", "", "", domain.StatusWorking)
	f.herdr.Push(domain.HerdrEvent{Kind: domain.PaneAgentStatusChanged, PaneID: "p1", Agent: &working})
	waitFor(t, "history mark", func() bool {
		_, marked, _ := f.capture.Since(context.Background(), key)
		return marked
	})
	if err := f.stop(t); err != nil {
		t.Fatalf("Run = %v", err)
	}
}

func TestDaemonStatsWhileRunning(t *testing.T) {
	f := newDaemon(t)
	f.herdr.SetAgents([]domain.Agent{agent("p1", "t1", "reviewer", domain.StatusIdle)})
	f.start(t)
	f.waitCalls(t, 3)
	f.clock.Advance(90 * time.Second)
	st := f.daemon.Stats()
	if st.Agents != 1 || st.Dropped != 0 || !st.HerdrOK || st.Version != "1.2.3" || !st.Since.Equal(t0) {
		t.Fatalf("Stats = %+v", st)
	}
	if line := app.StatsLine(st, f.clock.Now()); line != "version=1.2.3 pid=0 uptime=1m30s agents=1 dropped=0 herdr=ok sync=on cleanup=30d quiet=off" {
		t.Fatalf("StatsLine = %q", line)
	}
	if err := f.stop(t); err != nil {
		t.Fatal(err)
	}
}

// manyAgents builds n idle agents named a00..a<n-1> on panes p00..p<n-1>.
func manyAgents(n int, st domain.Status) []domain.Agent {
	out := make([]domain.Agent, 0, n)
	for i := range n {
		pane := fmt.Sprintf("p%02d", i)
		out = append(out, agent(pane, "t"+pane, fmt.Sprintf("a%02d", i), st))
	}
	return out
}

func countCalls(calls []string, prefix string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// TestDaemonManyAgentsBurst drives 40 agents through repeated status
// changes: the debounce must coalesce the edits, no job may be dropped and
// a blocked screen must be posted once per agent.
func TestDaemonManyAgentsBurst(t *testing.T) {
	const n = 40
	f := newDaemon(t)
	f.herdr.SetAgents(manyAgents(n, domain.StatusIdle))
	for i := range n {
		f.herdr.SetScreen(fmt.Sprintf("p%02d", i), fmt.Sprintf("Allow Bash on p%02d?\n1. Yes\n2. No", i))
	}
	f.start(t)
	waitFor(t, "topics created", func() bool { return countCalls(f.tg.Calls(), "create:") == n })

	push := func(i int, st domain.Status) {
		a := agent(fmt.Sprintf("p%02d", i), "", "", st)
		f.herdr.Push(domain.HerdrEvent{Kind: domain.PaneAgentStatusChanged, PaneID: a.Key.PaneID, Agent: &a})
	}
	for range 5 {
		for i := range n {
			push(i, domain.StatusWorking)
			push(i, domain.StatusIdle)
		}
		f.clock.Advance(time.Second)
	}
	// The snapshot must agree with the events: the reconcile ticker fires
	// while the clock is advanced and would otherwise revert the statuses.
	f.herdr.SetAgents(manyAgents(n, domain.StatusBlocked))
	for i := range n {
		push(i, domain.StatusBlocked)
	}
	// Let the debounce and the screen settle timers fire. Timers are armed
	// as jobs are served, so the clock is advanced repeatedly.
	deadline := time.Now().Add(3 * time.Second)
	for len(f.tg.Sent()) < n+1 && time.Now().Before(deadline) {
		f.clock.Advance(5 * time.Second)
		time.Sleep(2 * time.Millisecond)
	}
	if len(f.tg.Sent()) < n+1 {
		t.Fatalf("blocked screens posted = %d, want %d", len(f.tg.Sent())-1, n)
	}

	if dropped := f.daemon.Stats().Dropped; dropped != 0 {
		t.Fatalf("dropped %d jobs under a 40-agent burst", dropped)
	}
	calls := f.tg.Calls()
	// Ten status flips per agent must not cost ten edits per agent.
	if edits := countCalls(calls, "edit:"); edits > 3*n {
		t.Fatalf("edits = %d for %d agents, debounce did not coalesce", edits, n)
	}
	// Every agent's blocked screen is posted exactly once (the started
	// notice is the extra message in the topic-0 thread).
	posts := map[int]int{}
	for _, s := range f.tg.Sent() {
		if s.ThreadID != 0 && strings.HasPrefix(s.Text, "Allow Bash") {
			posts[s.ThreadID]++
		}
	}
	if len(posts) != n {
		t.Fatalf("agents with a screen post = %d, want %d", len(posts), n)
	}
	for thread, count := range posts {
		if count != 1 {
			t.Fatalf("thread %d got %d screen posts, want 1", thread, count)
		}
	}
	if err := f.stop(t); err != nil {
		t.Fatal(err)
	}
}

// TestDaemonManyAgentsSnapshotChurn exits agents in waves and checks that
// every topic is closed once and no topic is created twice.
func TestDaemonManyAgentsSnapshotChurn(t *testing.T) {
	const n = 30
	f := newDaemon(t)
	all := manyAgents(n, domain.StatusIdle)
	f.herdr.SetAgents(all)
	f.start(t)
	waitFor(t, "topics created", func() bool { return countCalls(f.tg.Calls(), "create:") == n })

	for wave := 1; wave <= 3; wave++ {
		f.herdr.SetAgents(all[wave*10:])
		f.daemon.Resync()
		want := wave * 10
		waitFor(t, fmt.Sprintf("wave %d closed", wave), func() bool {
			return countCalls(f.tg.Calls(), "close:") >= want
		})
	}
	calls := f.tg.Calls()
	if created := countCalls(calls, "create:"); created != n {
		t.Fatalf("created = %d, want %d (topics recreated)", created, n)
	}
	if closed := countCalls(calls, "close:"); closed != n {
		t.Fatalf("closed = %d, want %d", closed, n)
	}
	if dropped := f.daemon.Stats().Dropped; dropped != 0 {
		t.Fatalf("dropped %d jobs during churn", dropped)
	}
	if err := f.stop(t); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonSyncOffStartAndResume(t *testing.T) {
	f := newDaemon(t)
	ctx := context.Background()
	if err := f.opts.Set(ctx, domain.OptionSyncEnabled, "false", 1); err != nil {
		t.Fatal(err)
	}
	f.herdr.SetAgents([]domain.Agent{agent("p1", "t1", "reviewer", domain.StatusWorking)})
	f.start(t)
	f.waitCalls(t, 2)
	assertCalls(t, f.tg, "rights", started1+" (sync off, see /options)")
	if st := f.daemon.Stats(); !st.SyncOff || !strings.Contains(app.StatsLine(st, f.clock.Now()), "sync=off") {
		t.Fatalf("Stats = %+v", st)
	}
	if f.tg.Icons() != domain.DefaultStatusIcons() {
		t.Errorf("icons not applied at start: %+v", f.tg.Icons())
	}

	// Switching sync on requests a resync, which creates the topic.
	if err := f.opts.Set(ctx, domain.OptionSyncEnabled, "true", 1); err != nil {
		t.Fatal(err)
	}
	f.waitCalls(t, 3)
	assertCalls(t, f.tg, "rights", started1+" (sync off, see /options)", "create:reviewer:working")
	if st := f.daemon.Stats(); st.SyncOff {
		t.Fatalf("Stats after resume = %+v", st)
	}

	// An icon change reaches the gateway and repaints through a resync.
	lists := f.herdr.ListCalls()
	if err := f.opts.Set(ctx, domain.IconKey(domain.StatusWorking), "🔥", 1); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "icons applied", func() bool { return f.tg.Icons().Working == "🔥" })
	waitFor(t, "resync after icon change", func() bool { return f.herdr.ListCalls() > lists })
	if err := f.stop(t); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonIconChangeWhilePausedSkipsResync(t *testing.T) {
	f := newDaemon(t)
	ctx := context.Background()
	_ = f.opts.Set(ctx, domain.OptionSyncEnabled, "false", 1)
	f.start(t)
	f.waitCalls(t, 2)
	lists := f.herdr.ListCalls()
	if err := f.opts.Set(ctx, domain.IconKey(domain.StatusIdle), "🧠", 1); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "icons applied", func() bool { return f.tg.Icons().Idle == "🧠" })
	time.Sleep(20 * time.Millisecond)
	if f.herdr.ListCalls() != lists {
		t.Errorf("resync ran while sync is off: %d -> %d", lists, f.herdr.ListCalls())
	}
	if err := f.stop(t); err != nil {
		t.Fatal(err)
	}
}

// stale seeds a closed topic of an exited agent, closed at the given time.
func (f *daemonFixture) stale(t *testing.T, pane string, closedAt time.Time) {
	t.Helper()
	a := agent(pane, "t", pane, domain.StatusWorking)
	topic, err := f.tg.CreateTopic(context.Background(), a.Label(), domain.StatusExited)
	if err != nil {
		t.Fatal(err)
	}
	m := f.rec.Mapping()
	m.Link(a.Key, topic, a, closedAt)
	m.MarkExited(a.Key, closedAt)
	m.MarkClosed(a.Key, closedAt)
}

func TestDaemonSweepsAtStartDailyAndOnOptionChange(t *testing.T) {
	f := newDaemon(t)
	f.stale(t, "old", t0.Add(-40*day))            // stale at start
	f.stale(t, "soon", t0.Add(-30*day+time.Hour)) // stale after the daily pass
	f.stale(t, "later", t0.Add(-10*day))          // stale once the option drops to 7 days
	f.tg.Reset()
	f.start(t)
	f.waitCalls(t, 3)
	assertCalls(t, f.tg, "rights", started0, "delete:101")

	waitFor(t, "timers", func() bool { return f.clock.Pending() >= 5 }) // registry tick, health, sweep, capture
	f.clock.Advance(app.SweepIntervalForTest)
	f.waitCalls(t, 4)
	if calls := f.tg.Calls(); calls[len(calls)-1] != "delete:102" {
		t.Fatalf("calls after a day = %v", calls)
	}

	if err := f.opts.Set(context.Background(), domain.OptionDeleteAfterDays, "7", 1); err != nil {
		t.Fatal(err)
	}
	f.waitCalls(t, 5)
	if calls := f.tg.Calls(); calls[len(calls)-1] != "delete:103" {
		t.Fatalf("calls after the option change = %v", calls)
	}
	if st := f.daemon.Stats(); st.DeleteAfterDays != 7 || !strings.Contains(app.StatsLine(st, f.clock.Now()), "cleanup=7d") {
		t.Fatalf("Stats = %+v", st)
	}
	if err := f.stop(t); err != nil {
		t.Fatal(err)
	}
	if len(f.rec.Mapping().Topics) != 0 {
		t.Fatalf("entries left: %v", f.rec.Mapping().Keys())
	}
}

func TestDaemonSweepOffAndWithoutDeleteRight(t *testing.T) {
	f := newDaemon(t)
	f.tg.SetRights(domain.Rights{IsForum: true, IsAdmin: true, CanManageTopics: true}, nil)
	f.stale(t, "old", t0.Add(-40*day))
	f.tg.Reset()
	f.start(t)
	f.waitCalls(t, 2)
	assertCalls(t, f.tg, "rights", started0)
	if err := f.opts.Set(context.Background(), domain.OptionDeleteAfterDays, "0", 1); err != nil {
		t.Fatal(err)
	}
	if err := f.stop(t); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, f.tg, "rights", started0, stopping)
	if len(f.rec.Mapping().Topics) != 1 {
		t.Fatal("entry dropped without the delete right")
	}
}

// hasCall reports whether the fake Telegram recorded a call with the prefix.
func (f *daemonFixture) hasCall(prefix string) bool {
	for _, c := range f.tg.Calls() {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// tick advances the fake clock by one presence interval on every poll of
// waitFor until cond holds, so timers armed by the daemon goroutine fire.
func (f *daemonFixture) tick(t *testing.T, what string, cond func() bool) {
	t.Helper()
	waitFor(t, what, func() bool {
		if cond() {
			return true
		}
		f.clock.Advance(10 * time.Second)
		return false
	})
}

func TestDaemonQuietDefersUntilOperatorLeaves(t *testing.T) {
	f := newDaemon(t)
	f.idle.Set(time.Second) // at the desk from the start
	f.herdr.SetAgents([]domain.Agent{agent("p1", "t1", "reviewer", domain.StatusWorking)})
	f.herdr.SetScreen("p1", "Allow Bash?\n1. Yes\n2. No")
	f.start(t)
	f.waitCalls(t, 2)
	// No topic is created while quiet; the started notice still posts.
	assertCalls(t, f.tg, "rights", started1)
	if st := f.daemon.Stats(); st.Quiet != "on" || !strings.HasSuffix(app.StatsLine(st, f.clock.Now()), "quiet=on") {
		t.Fatalf("Stats = %+v", st)
	}

	// The agent asks a question: no topic, so nothing is posted yet.
	blocked := agent("p1", "", "", domain.StatusBlocked)
	f.herdr.Push(domain.HerdrEvent{Kind: domain.PaneAgentStatusChanged, PaneID: "p1", Agent: &blocked})
	f.herdr.SetAgents([]domain.Agent{agent("p1", "t1", "reviewer", domain.StatusBlocked)})
	waitFor(t, "settle timer", func() bool { return f.clock.Pending() >= 6 }) // registry tick, health, sweep, capture, presence, settle (no topic, so no edit debounce)
	f.clock.Advance(1500 * time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	assertCalls(t, f.tg, "rights", started1)

	// The operator leaves: the topic is created with the current status and
	// the waiting question is posted once, with sound.
	f.idle.Set(time.Hour)
	f.tick(t, "catch-up", func() bool { return f.hasCall("create:reviewer:blocked") && len(f.tg.Sent()) == 2 })
	sent := f.tg.Sent()
	if sent[1].ThreadID != 101 || !sent[1].Notify || sent[1].Text != "Allow Bash?\n1. Yes\n2. No" {
		t.Fatalf("catch-up post = %+v", sent[1])
	}
	if st := f.daemon.Stats(); st.Quiet != "away" {
		t.Fatalf("Stats after leaving = %+v", st)
	}

	// Back for a moment, away again: same question, no second sound.
	f.idle.Set(time.Second)
	f.tick(t, "quiet on again", func() bool { return f.daemon.Stats().Quiet == "on" })
	f.idle.Set(time.Hour)
	f.tick(t, "quiet off again", func() bool { return f.daemon.Stats().Quiet == "away" })
	time.Sleep(30 * time.Millisecond)
	if got := len(f.tg.Sent()); got != 2 {
		t.Fatalf("flapping produced %d posts: %+v", got, f.tg.Sent())
	}
	if f.hasCall("edit:") {
		t.Fatalf("flapping edited topics: %v", f.tg.Calls())
	}
	if err := f.stop(t); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonQuietSilentPostAndIconDrift(t *testing.T) {
	f := newDaemon(t)
	f.idle.Set(time.Hour) // supported source, operator away at start
	f.herdr.SetAgents([]domain.Agent{agent("p1", "t1", "reviewer", domain.StatusWorking)})
	f.herdr.SetScreen("p1", "Allow Bash?\n1. Yes\n2. No")
	f.start(t)
	f.waitCalls(t, 3) // rights, create, started: away at start
	assertCalls(t, f.tg, "rights", "create:reviewer:working", started1)

	// The operator sits down: the next sample turns quiet on.
	f.idle.Set(time.Second)
	f.tick(t, "quiet on", func() bool { return f.daemon.Stats().Quiet == "on" })
	blocked := agent("p1", "", "", domain.StatusBlocked)
	f.herdr.Push(domain.HerdrEvent{Kind: domain.PaneAgentStatusChanged, PaneID: "p1", Agent: &blocked})
	f.herdr.SetAgents([]domain.Agent{agent("p1", "t1", "reviewer", domain.StatusBlocked)})
	f.tick(t, "silent post", func() bool { return len(f.tg.Sent()) == 2 })
	if sent := f.tg.Sent(); sent[1].Notify || sent[1].ThreadID != 101 {
		t.Fatalf("post while quiet = %+v", sent[1])
	}
	if f.hasCall("edit:") {
		t.Fatalf("icon edited while quiet: %v", f.tg.Calls())
	}

	// Leaving heals the icon with one edit and rings once for the question.
	f.idle.Set(time.Hour)
	f.tick(t, "catch-up", func() bool { return f.hasCall("edit:101:status=blocked") && len(f.tg.Sent()) == 3 })
	if sent := f.tg.Sent(); !sent[2].Notify {
		t.Fatalf("catch-up post = %+v", sent[2])
	}
	edits := 0
	for _, c := range f.tg.Calls() {
		if strings.HasPrefix(c, "edit:") {
			edits++
		}
	}
	if edits != 1 {
		t.Fatalf("catch-up edits = %d: %v", edits, f.tg.Calls())
	}
	if err := f.stop(t); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonAwayCommandAndQuietOption(t *testing.T) {
	f := newDaemon(t)
	f.idle.Set(time.Second)
	f.herdr.SetAgents([]domain.Agent{agent("p1", "t1", "reviewer", domain.StatusWorking)})
	f.start(t)
	f.waitCalls(t, 2)
	assertCalls(t, f.tg, "rights", started1)

	// /away from the phone catches up at once and answers.
	f.tg.Push(domain.GeneralCommand{MessageID: 9, FromID: 1, Text: "/away 1h"})
	f.tick(t, "away catch-up", func() bool { return f.hasCall("create:reviewer:working") && len(f.tg.Sent()) == 2 })
	if sent := f.tg.Sent(); !strings.HasPrefix(sent[1].Text, "🏃 away until 13:00") || sent[1].ReplyTo != 9 {
		t.Fatalf("/away reply = %+v", sent[1])
	}
	if st := f.daemon.Stats(); st.Quiet != "away-manual" {
		t.Fatalf("Stats after /away = %+v", st)
	}
	// /here returns to the automatic verdict: quiet again.
	f.tg.Push(domain.GeneralCommand{MessageID: 10, FromID: 1, Text: "/here"})
	f.tick(t, "quiet after /here", func() bool { return f.daemon.Stats().Quiet == "on" })
	if sent := f.tg.Sent(); len(sent) != 3 || sent[2].Text != "🖥 presence is automatic again: at the desk, quiet on" {
		t.Fatalf("/here reply = %+v", sent)
	}

	// A status change now waits; switching quiet off in the options heals it.
	idle := agent("p1", "", "", domain.StatusIdle)
	f.herdr.Push(domain.HerdrEvent{Kind: domain.PaneAgentStatusChanged, PaneID: "p1", Agent: &idle})
	f.herdr.SetAgents([]domain.Agent{agent("p1", "t1", "reviewer", domain.StatusIdle)})
	f.tick(t, "edit debounce", func() bool { return f.clock.Pending() >= 6 })
	time.Sleep(20 * time.Millisecond)
	if f.hasCall("edit:") {
		t.Fatalf("edited while quiet: %v", f.tg.Calls())
	}
	if err := f.opts.Set(context.Background(), domain.OptionQuietEnabled, "false", 1); err != nil {
		t.Fatal(err)
	}
	f.tick(t, "heal after option change", func() bool { return f.hasCall("edit:101:status=idle") })
	if st := f.daemon.Stats(); st.Quiet != "off" {
		t.Fatalf("Stats with quiet disabled = %+v", st)
	}
	if err := f.stop(t); err != nil {
		t.Fatal(err)
	}
}
