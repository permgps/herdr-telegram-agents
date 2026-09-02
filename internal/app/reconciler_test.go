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

type recFixture struct {
	tg    *testkit.FakeTelegram
	herdr *testkit.FakeHerdr
	store *testkit.MemMappingStore
	clock *testkit.FakeClock
	rec   *app.Reconciler
	ctx   context.Context
}

func newRec(t *testing.T) *recFixture {
	t.Helper()
	f := &recFixture{
		tg:    testkit.NewFakeTelegram(nil),
		herdr: testkit.NewFakeHerdr(nil),
		store: testkit.NewMemMappingStore(),
		clock: testkit.NewFakeClock(t0),
		ctx:   context.Background(),
	}
	f.rec = app.NewReconciler(f.tg, f.herdr, f.store, domain.NewMapping(-1), f.clock, nil)
	return f
}

func (f *recFixture) handle(t *testing.T, kind app.AgentEventKind, a domain.Agent) {
	t.Helper()
	if err := f.rec.Handle(f.ctx, app.AgentEvent{Kind: kind, Agent: a}); err != nil {
		t.Fatalf("Handle(%s) = %v", kind, err)
	}
}

// fireDue advances the clock past the debounce and fires every due key.
func (f *recFixture) fireDue(t *testing.T, want int) {
	t.Helper()
	f.clock.Advance(3 * time.Second)
	for i := 0; i < want; i++ {
		select {
		case key := <-f.rec.Due():
			if err := f.rec.Fire(f.ctx, key); err != nil {
				t.Fatalf("Fire = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("due edit %d did not fire", i+1)
		}
	}
	select {
	case key := <-f.rec.Due():
		t.Fatalf("unexpected extra due key %v", key)
	case <-time.After(50 * time.Millisecond):
	}
}

func assertCalls(t *testing.T, tg *testkit.FakeTelegram, want ...string) {
	t.Helper()
	got := tg.Calls()
	if !equal(got, want) {
		t.Fatalf("calls = %v\nwant    %v", got, want)
	}
}

func TestReconcilerCreatesOnceForRepeatedAppeared(t *testing.T) {
	f := newRec(t)
	a := agent("p1", "t1", "reviewer", domain.StatusWorking)
	f.handle(t, app.AgentAppeared, a)
	f.handle(t, app.AgentAppeared, a)
	assertCalls(t, f.tg, "create:reviewer:working")
	if f.store.SaveCount() != 1 {
		t.Fatalf("saves = %d, want 1", f.store.SaveCount())
	}
	entry, ok := f.rec.Mapping().TopicFor(a.Key)
	if !ok || entry.ThreadID != 101 || entry.Name != "reviewer" {
		t.Fatalf("entry = %+v, %v", entry, ok)
	}
}

func TestReconcilerDebouncesChanges(t *testing.T) {
	f := newRec(t)
	a := agent("p1", "t1", "reviewer", domain.StatusWorking)
	f.handle(t, app.AgentAppeared, a)
	f.handle(t, app.AgentChanged, agent("p1", "t1", "reviewer", domain.StatusIdle))
	f.handle(t, app.AgentChanged, agent("p1", "t1", "reviewer", domain.StatusWorking))
	f.handle(t, app.AgentChanged, agent("p1", "t1", "fixer", domain.StatusBlocked))
	assertCalls(t, f.tg, "create:reviewer:working")
	f.fireDue(t, 1)
	assertCalls(t, f.tg, "create:reviewer:working", "edit:101:name=fixer,status=blocked")
	if f.store.SaveCount() != 2 {
		t.Fatalf("saves = %d, want 2", f.store.SaveCount())
	}

	// A change that nets out to the current state fires nothing.
	f.handle(t, app.AgentChanged, agent("p1", "t1", "fixer", domain.StatusIdle))
	f.handle(t, app.AgentChanged, agent("p1", "t1", "fixer", domain.StatusBlocked))
	f.fireDue(t, 1)
	assertCalls(t, f.tg, "create:reviewer:working", "edit:101:name=fixer,status=blocked")
}

func TestReconcilerGoneCancelsEditAndCloses(t *testing.T) {
	f := newRec(t)
	a := agent("p1", "t1", "reviewer", domain.StatusWorking)
	f.handle(t, app.AgentAppeared, a)
	f.handle(t, app.AgentChanged, agent("p1", "t1", "reviewer", domain.StatusIdle))
	f.handle(t, app.AgentGone, agent("p1", "t1", "reviewer", domain.StatusExited))
	assertCalls(t, f.tg, "create:reviewer:working", "edit:101:status=exited", "close:101")
	f.clock.Advance(5 * time.Second)
	select {
	case key := <-f.rec.Due():
		t.Fatalf("cancelled edit fired for %v", key)
	case <-time.After(50 * time.Millisecond):
	}
	entry, _ := f.rec.Mapping().TopicFor(a.Key)
	if entry.Status != domain.StatusExited || !entry.Closed || entry.Name != "reviewer" {
		t.Fatalf("entry = %+v", *entry)
	}
	if got, _ := f.tg.Topic(101); !got.Closed {
		t.Fatal("fake topic not closed")
	}
	// A late status event for a gone key is ignored.
	f.handle(t, app.AgentGone, agent("p1", "t1", "reviewer", domain.StatusExited))
	assertCalls(t, f.tg, "create:reviewer:working", "edit:101:status=exited", "close:101")
}

func TestReconcilerRepairsFailedClose(t *testing.T) {
	f := newRec(t)
	a := agent("p1", "t1", "reviewer", domain.StatusWorking)
	f.handle(t, app.AgentAppeared, a)
	f.tg.FailNext("close", errors.New("boom"))
	f.handle(t, app.AgentGone, agent("p1", "t1", "reviewer", domain.StatusExited))
	entry, _ := f.rec.Mapping().TopicFor(a.Key)
	if entry.Status != domain.StatusExited || entry.Closed {
		t.Fatalf("after failed close: %+v", *entry)
	}
	if err := f.rec.Reconcile(f.ctx, nil); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, f.tg, "create:reviewer:working", "edit:101:status=exited", "close:101", "close:101")
	if !entry.Closed {
		t.Fatal("close not repaired")
	}
}

func TestReconcilerForgetsGoneTopicAndRecreates(t *testing.T) {
	f := newRec(t)
	a := agent("p1", "t1", "reviewer", domain.StatusWorking)
	f.handle(t, app.AgentAppeared, a)
	f.tg.FailNext("edit", domain.ErrTopicGone)
	f.handle(t, app.AgentAppeared, agent("p1", "t1", "reviewer", domain.StatusIdle))
	if _, ok := f.rec.Mapping().TopicFor(a.Key); ok {
		t.Fatal("entry kept after ErrTopicGone")
	}
	if err := f.rec.Reconcile(f.ctx, []domain.Agent{agent("p1", "t1", "reviewer", domain.StatusIdle)}); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, f.tg, "create:reviewer:working", "edit:101:status=idle", "create:reviewer:idle")
	entry, _ := f.rec.Mapping().TopicFor(a.Key)
	if entry.ThreadID != 102 {
		t.Fatalf("recreated thread = %d", entry.ThreadID)
	}
}

func TestReconcilerKeepsEntryOnClosedTopic(t *testing.T) {
	f := newRec(t)
	a := agent("p1", "t1", "reviewer", domain.StatusWorking)
	f.handle(t, app.AgentAppeared, a)
	f.tg.FailNext("edit", domain.ErrTopicClosed)
	f.handle(t, app.AgentAppeared, agent("p1", "t1", "reviewer", domain.StatusIdle))
	entry, ok := f.rec.Mapping().TopicFor(a.Key)
	if !ok || entry.Status != domain.StatusWorking {
		t.Fatalf("entry = %+v, %v", entry, ok)
	}
}

func TestReconcilerFatalErrorsPropagate(t *testing.T) {
	for _, fatal := range []error{domain.ErrForbidden, domain.ErrBotUnauthorized, domain.ErrPollerConflict} {
		f := newRec(t)
		f.tg.FailNext("create", fatal)
		err := f.rec.Handle(f.ctx, app.AgentEvent{Kind: app.AgentAppeared, Agent: agent("p1", "t1", "a", domain.StatusIdle)})
		if !errors.Is(err, fatal) {
			t.Fatalf("Handle with %v = %v", fatal, err)
		}
	}
	f := newRec(t)
	f.tg.FailNext("create", errors.New("network"))
	f.handle(t, app.AgentAppeared, agent("p1", "t1", "a", domain.StatusIdle))
	if _, ok := f.rec.Mapping().TopicFor(domain.Key{PaneID: "p1", TerminalID: "t1"}); ok {
		t.Fatal("entry linked after failed create")
	}
}

func TestReconcilerReadOnlyMode(t *testing.T) {
	f := newRec(t)
	f.rec.SetReadOnly(true)
	a := agent("p1", "t1", "reviewer", domain.StatusWorking)
	f.handle(t, app.AgentAppeared, a)
	f.handle(t, app.AgentChanged, agent("p1", "t1", "reviewer", domain.StatusIdle))
	f.handle(t, app.AgentGone, agent("p1", "t1", "reviewer", domain.StatusExited))
	if err := f.rec.Reconcile(f.ctx, []domain.Agent{a}); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, f.tg)
	f.rec.SetReadOnly(false)
	if err := f.rec.Reconcile(f.ctx, []domain.Agent{a}); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, f.tg, "create:reviewer:working")
}

func TestReconcileHealsDrift(t *testing.T) {
	f := newRec(t)
	m := f.rec.Mapping()
	mk := func(a domain.Agent, at time.Time) domain.Topic {
		topic, err := f.tg.CreateTopic(f.ctx, domain.DisplayName(a.Label()), a.Status)
		if err != nil {
			t.Fatal(err)
		}
		m.Link(a.Key, topic, a, at)
		return topic
	}
	// Entry for an agent that exited while the daemon was down.
	orphan := agent("p0", "t0", "old", domain.StatusWorking)
	orphanTopic := mk(orphan, t0) // 101
	// Entry whose name drifted from the current label.
	drifted := agent("p1", "t1", "before", domain.StatusWorking)
	driftedTopic := mk(drifted, t0) // 102
	// Exited entry older than the prune age.
	stale := agent("p2", "t2", "stale", domain.StatusWorking)
	mk(stale, t0.Add(-8*24*time.Hour)) // 103
	m.MarkExited(stale.Key, t0.Add(-8*24*time.Hour))
	m.MarkClosed(stale.Key, t0.Add(-8*24*time.Hour))
	// Duplicate thread id: the newer entry wins.
	dupOld := agent("p3", "t3", "dup", domain.StatusWorking)
	dupNew := agent("p3", "t4", "dup", domain.StatusWorking)
	dupTopic := mk(dupOld, t0.Add(-time.Hour)) // 104
	m.Link(dupNew.Key, dupTopic, dupNew, t0)
	f.tg.Reset()

	live := []domain.Agent{
		agent("p1", "t1", "after", domain.StatusIdle),
		dupNew,
		agent("p4", "t5", "new", domain.StatusBlocked),
	}
	if err := f.rec.Reconcile(f.ctx, live); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, f.tg,
		"edit:101:status=exited", "close:101",
		"edit:102:name=after,status=idle",
		"create:new:blocked",
	)
	if e, _ := m.TopicFor(orphan.Key); e.ThreadID != orphanTopic.ThreadID || !e.Closed {
		t.Fatalf("orphan entry = %+v", e)
	}
	if e, _ := m.TopicFor(drifted.Key); e.ThreadID != driftedTopic.ThreadID || e.Name != "after" {
		t.Fatalf("drifted entry = %+v", e)
	}
	if _, ok := m.TopicFor(stale.Key); ok {
		t.Fatal("stale entry not pruned")
	}
	if _, ok := m.TopicFor(dupOld.Key); ok {
		t.Fatal("older duplicate kept")
	}
	if len(m.Topics) != 4 {
		t.Fatalf("entries = %d, want 4 (%v)", len(m.Topics), m.Keys())
	}
	// Second pass is a no-op.
	f.tg.Reset()
	if err := f.rec.Reconcile(f.ctx, live); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, f.tg)
}

func TestReconcilerFlushFiresPendingEdits(t *testing.T) {
	f := newRec(t)
	f.handle(t, app.AgentAppeared, agent("p1", "t1", "a", domain.StatusWorking))
	f.handle(t, app.AgentAppeared, agent("p2", "t2", "b", domain.StatusWorking))
	f.handle(t, app.AgentChanged, agent("p2", "t2", "b", domain.StatusDone))
	f.handle(t, app.AgentChanged, agent("p1", "t1", "a", domain.StatusIdle))
	if err := f.rec.Flush(f.ctx); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, f.tg, "create:a:working", "create:b:working",
		"edit:101:status=idle", "edit:102:status=done")
	f.clock.Advance(5 * time.Second)
	select {
	case key := <-f.rec.Due():
		t.Fatalf("drained edit fired for %v", key)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestResyncRewritesEveryLiveTopic(t *testing.T) {
	f := newRec(t)
	a := agent("p1", "t1", "reviewer", domain.StatusWorking)
	gone := agent("p2", "t2", "old", domain.StatusIdle)
	if err := f.rec.Reconcile(f.ctx, []domain.Agent{a, gone}); err != nil {
		t.Fatal(err)
	}
	if err := f.rec.Reconcile(f.ctx, []domain.Agent{a, gone}); err != nil {
		t.Fatal(err)
	}
	// A plain pass with nothing changed writes nothing.
	assertCalls(t, f.tg, "create:reviewer:working", "create:old:idle")

	// Resync sends name and icon again for live topics only; the vanished
	// agent takes the normal exit path.
	if err := f.rec.Resync(f.ctx, []domain.Agent{a}); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, f.tg, "create:reviewer:working", "create:old:idle",
		"edit:102:status=exited", "close:102",
		"edit:101:name=reviewer,status=working")

	// The force flag does not leak into the next plain pass.
	if err := f.rec.Reconcile(f.ctx, []domain.Agent{a}); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, f.tg, "create:reviewer:working", "create:old:idle",
		"edit:102:status=exited", "close:102",
		"edit:101:name=reviewer,status=working")
}

// wsAgent is an agent with a workspace label, so its label carries the
// "<ws> · <name>" prefix the panel shows.
func wsAgent(pane, term, name string, st domain.Status) domain.Agent {
	a := agent(pane, term, name, st)
	a.WorkspaceLabel = "V3Jobs"
	a.TabLabel = "claude"
	a.Kind = "claude"
	return a
}

func TestReconcilerMuteOnCloseAndReopen(t *testing.T) {
	f := newRec(t)
	a := agent("p1", "t1", "reviewer", domain.StatusWorking)
	f.handle(t, app.AgentAppeared, a)
	if err := f.rec.OnTopicClosed(f.ctx, 101); err != nil {
		t.Fatal(err)
	}
	if e, _ := f.rec.Mapping().TopicFor(a.Key); !e.Muted {
		t.Fatal("entry not muted")
	}
	// Status changes are swallowed while muted.
	f.handle(t, app.AgentChanged, agent("p1", "t1", "reviewer", domain.StatusIdle))
	f.fireDue(t, 1)
	assertCalls(t, f.tg, "create:reviewer:working")
	// Reconcile leaves the muted topic alone, Resync rewrites it.
	if err := f.rec.Reconcile(f.ctx, []domain.Agent{agent("p1", "t1", "reviewer", domain.StatusIdle)}); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, f.tg, "create:reviewer:working")
	if err := f.rec.Resync(f.ctx, []domain.Agent{agent("p1", "t1", "reviewer", domain.StatusIdle)}); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, f.tg, "create:reviewer:working", "edit:101:name=reviewer,status=idle")

	// Reopen lifts the mute and forces name and icon.
	f.tg.Reset()
	f.handle(t, app.AgentChanged, agent("p1", "t1", "reviewer", domain.StatusBlocked))
	if err := f.rec.OnTopicReopened(f.ctx, 101); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, f.tg, "edit:101:name=reviewer,status=blocked")
	if e, _ := f.rec.Mapping().TopicFor(a.Key); e.Muted || e.Status != domain.StatusBlocked {
		t.Fatalf("entry after reopen = %+v", *e)
	}
	// Unknown threads are ignored.
	if err := f.rec.OnTopicClosed(f.ctx, 999); err != nil {
		t.Fatal(err)
	}
	if err := f.rec.OnTopicReopened(f.ctx, 999); err != nil {
		t.Fatal(err)
	}
}

func TestReconcilerExitWhileMuted(t *testing.T) {
	f := newRec(t)
	a := agent("p1", "t1", "reviewer", domain.StatusWorking)
	f.handle(t, app.AgentAppeared, a)
	if err := f.rec.OnTopicClosed(f.ctx, 101); err != nil {
		t.Fatal(err)
	}
	f.handle(t, app.AgentGone, agent("p1", "t1", "reviewer", domain.StatusExited))
	assertCalls(t, f.tg, "create:reviewer:working")
	e, _ := f.rec.Mapping().TopicFor(a.Key)
	if e.Status != domain.StatusExited || !e.Closed || !e.Muted {
		t.Fatalf("entry after muted exit = %+v", *e)
	}
	// Reopening an exited topic writes the finish flag and closes again.
	if err := f.rec.OnTopicReopened(f.ctx, 101); err != nil {
		t.Fatal(err)
	}
	assertCalls(t, f.tg, "create:reviewer:working", "edit:101:status=exited", "close:101")
	if e, _ := f.rec.Mapping().TopicFor(a.Key); e.Muted || !e.Closed {
		t.Fatalf("entry after reopen of exited = %+v", *e)
	}
	// Closing an exited topic by hand only records the closure.
	f.rec.Mapping().MarkReopened(a.Key, t0)
	if err := f.rec.OnTopicClosed(f.ctx, 101); err != nil {
		t.Fatal(err)
	}
	if e, _ := f.rec.Mapping().TopicFor(a.Key); e.Muted || !e.Closed {
		t.Fatalf("entry after closing exited = %+v", *e)
	}
}

func TestReconcilerRenameFromTelegram(t *testing.T) {
	tests := []struct {
		name     string
		typed    string
		wantName *string // nil = clear
	}{
		{"with prefix", "V3Jobs · fixer", ptr("fixer")},
		{"without prefix", "fixer", ptr("fixer")},
		{"prefix and spaces", "  V3Jobs ·   fixer  ", ptr("fixer")},
		{"tab label clears", "V3Jobs · claude", nil},
		{"kind clears", "claude", nil},
		{"only workspace clears", "V3Jobs", nil},
		{"empty clears", "   ", nil},
		{"legacy status prefix stripped", "🏁 V3Jobs · fixer", ptr("fixer")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newRec(t)
			a := wsAgent("p1", "t1", "reviewer", domain.StatusWorking)
			f.handle(t, app.AgentAppeared, a)
			// An edit is already pending when the operator renames the topic.
			f.handle(t, app.AgentChanged, wsAgent("p1", "t1", "reviewer", domain.StatusIdle))
			snapshot, err := f.rec.OnTopicRenamed(f.ctx, 101, tt.typed)
			if err != nil || !snapshot {
				t.Fatalf("OnTopicRenamed = %v, %v", snapshot, err)
			}
			r := f.herdr.Renames()
			if len(r) != 1 || r[0].Target != "p1" {
				t.Fatalf("Renames = %+v", r)
			}
			switch {
			case tt.wantName == nil && r[0].Name != nil:
				t.Fatalf("Rename name = %q, want clear", *r[0].Name)
			case tt.wantName != nil && (r[0].Name == nil || *r[0].Name != *tt.wantName):
				t.Fatalf("Rename name = %v, want %q", r[0].Name, *tt.wantName)
			}
			e, _ := f.rec.Mapping().TopicFor(a.Key)
			if e.Name != tt.typed {
				t.Fatalf("entry name = %q, want the typed name %q", e.Name, tt.typed)
			}
			// The pending edit does not flap the topic back: the reconciler's
			// agent now carries the operator's name.
			f.fireDue(t, 1)
			calls := f.tg.Calls()
			last := calls[len(calls)-1]
			if !strings.HasPrefix(last, "edit:101:") || strings.Contains(last, "name=V3Jobs · reviewer") {
				t.Fatalf("edit after rename = %q", last)
			}
		})
	}
}

func TestReconcilerRenameIgnoredOrFailed(t *testing.T) {
	f := newRec(t)
	a := wsAgent("p1", "t1", "reviewer", domain.StatusWorking)
	f.handle(t, app.AgentAppeared, a)

	if snapshot, err := f.rec.OnTopicRenamed(f.ctx, 999, "x"); err != nil || snapshot {
		t.Fatalf("unknown thread: %v, %v", snapshot, err)
	}
	f.herdr.FailNext("rename", domain.ErrAgentGone)
	if snapshot, err := f.rec.OnTopicRenamed(f.ctx, 101, "fixer"); err != nil || snapshot {
		t.Fatalf("failed rename: %v, %v", snapshot, err)
	}
	assertCalls(t, f.tg, "create:V3Jobs · reviewer:working", "send:101:rename failed: agent is gone")
	if e, _ := f.rec.Mapping().TopicFor(a.Key); e.Name != "V3Jobs · reviewer" {
		t.Fatalf("entry renamed despite failure: %q", e.Name)
	}

	f.handle(t, app.AgentGone, wsAgent("p1", "t1", "reviewer", domain.StatusExited))
	if snapshot, err := f.rec.OnTopicRenamed(f.ctx, 101, "fixer"); err != nil || snapshot {
		t.Fatalf("exited rename: %v, %v", snapshot, err)
	}
	if n := len(f.herdr.Renames()); n != 1 {
		t.Fatalf("renames = %d, want only the failed one", n)
	}
}

func ptr(s string) *string { return &s }

// TestReconcilerReusesTopicWhenKeyReturns: an agent that exits and comes
// back under the same key (claude --resume in the same pane) continues in
// its old topic, reopened and refreshed, instead of getting a second one.
func TestReconcilerReusesTopicWhenKeyReturns(t *testing.T) {
	f := newRec(t)
	a := agent("p1", "t1", "reviewer", domain.StatusWorking)
	f.handle(t, app.AgentAppeared, a)
	f.handle(t, app.AgentGone, agent("p1", "t1", "reviewer", domain.StatusExited))
	assertCalls(t, f.tg, "create:reviewer:working", "edit:101:status=exited", "close:101")

	f.tg.Reset()
	f.handle(t, app.AgentAppeared, agent("p1", "t1", "reviewer", domain.StatusIdle))
	assertCalls(t, f.tg, "reopen:101", "edit:101:status=idle")
	entry, _ := f.rec.Mapping().TopicFor(a.Key)
	if entry.ThreadID != 101 || entry.Status != domain.StatusIdle || entry.Closed || entry.Muted {
		t.Fatalf("entry after return = %+v", *entry)
	}
	if got, _ := f.tg.Topic(101); got.Closed {
		t.Fatal("fake topic still closed")
	}

	// The same after a muted exit: the operator closed the topic by hand,
	// then the pane closed, then the agent came back.
	f.tg.Reset()
	if err := f.rec.OnTopicClosed(f.ctx, 101); err != nil {
		t.Fatal(err)
	}
	f.handle(t, app.AgentGone, agent("p1", "t1", "reviewer", domain.StatusExited))
	assertCalls(t, f.tg)
	f.handle(t, app.AgentAppeared, agent("p1", "t1", "reviewer", domain.StatusWorking))
	assertCalls(t, f.tg, "reopen:101", "edit:101:status=working")
	if entry, _ := f.rec.Mapping().TopicFor(a.Key); entry.Muted || entry.Closed || entry.Status != domain.StatusWorking {
		t.Fatalf("entry after muted return = %+v", *entry)
	}

	// Only when Telegram lost the old topic is a new one created.
	f.tg.Reset()
	f.handle(t, app.AgentGone, agent("p1", "t1", "reviewer", domain.StatusExited))
	f.tg.FailNext("reopen", domain.ErrTopicGone)
	f.handle(t, app.AgentAppeared, agent("p1", "t1", "reviewer", domain.StatusIdle))
	assertCalls(t, f.tg, "edit:101:status=exited", "close:101", "reopen:101", "create:reviewer:idle")
	if entry, _ := f.rec.Mapping().TopicFor(a.Key); entry.ThreadID != 102 {
		t.Fatalf("entry after lost topic = %+v", *entry)
	}
}
