package app_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/app"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

var t0 = time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

func agent(pane, term, name string, st domain.Status) domain.Agent {
	return domain.Agent{Key: domain.Key{PaneID: pane, TerminalID: term}, Kind: "claude", Name: name, Status: st}
}

func sessionAgent(pane, term, value string) domain.Agent {
	a := agent(pane, term, "reviewer", domain.StatusWorking)
	a.Key.SessionDigest = (domain.SessionTuple{Source: "test", Agent: "claude", Kind: "id", Value: value}).Digest()
	a.Cwd = "/work/repo"
	return a
}

func kinds(evs []app.AgentEvent) []string {
	out := make([]string, len(evs))
	for i, ev := range evs {
		out[i] = string(ev.Kind) + ":" + ev.Agent.Key.String()
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRegistrySnapshotDiff(t *testing.T) {
	h := testkit.NewFakeHerdr(nil)
	r := app.NewRegistry(h, testkit.NewFakeClock(t0), nil)
	ctx := context.Background()

	h.SetAgents([]domain.Agent{agent("p1", "t1", "a", domain.StatusWorking), agent("p2", "t2", "b", domain.StatusIdle)})
	evs, err := r.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds(evs); !equal(got, []string{"appeared:p1/t1", "appeared:p2/t2"}) {
		t.Fatalf("first snapshot = %v", got)
	}
	if w := h.WatchCalls(); len(w) != 1 || !equal(w[0], []string{"p1", "p2"}) {
		t.Fatalf("WatchPanes = %v", w)
	}

	evs, _ = r.Snapshot(ctx)
	if len(evs) != 0 {
		t.Fatalf("unchanged snapshot emitted %v", kinds(evs))
	}

	// A working-directory change refreshes mapping metadata without changing
	// the topic name or status.
	withCwd := agent("p1", "t1", "a", domain.StatusWorking)
	withCwd.Cwd = "/home/op/proj"
	h.SetAgents([]domain.Agent{withCwd, agent("p2", "t2", "b", domain.StatusIdle)})
	evs, _ = r.Snapshot(ctx)
	if got := kinds(evs); !equal(got, []string{"changed:p1/t1"}) {
		t.Fatalf("cwd-only snapshot emitted %v", got)
	}
	if a, _ := r.Agent(domain.Key{PaneID: "p1", TerminalID: "t1"}); a.Cwd != "/home/op/proj" {
		t.Fatalf("Cwd after snapshot = %q", a.Cwd)
	}

	h.SetAgents([]domain.Agent{agent("p1", "t1", "renamed", domain.StatusWorking), agent("p2", "t3", "b", domain.StatusIdle)})
	evs, _ = r.Snapshot(ctx)
	if got := kinds(evs); !equal(got, []string{"gone:p2/t2", "appeared:p2/t3", "changed:p1/t1"}) {
		t.Fatalf("replace + rename = %v", got)
	}
	if evs[0].Agent.Status != domain.StatusExited || evs[0].Agent.Label() != "b" {
		t.Fatalf("gone event = %+v", evs[0].Agent)
	}

	h.SetAgents(nil)
	evs, _ = r.Snapshot(ctx)
	if got := kinds(evs); !equal(got, []string{"gone:p1/t1", "gone:p2/t3"}) {
		t.Fatalf("all gone = %v", got)
	}
	if len(r.Live()) != 0 {
		t.Fatalf("Live after all gone = %v", r.Live())
	}
}

func TestRegistryOrdersSameAgentReplacementBeforeGone(t *testing.T) {
	for _, tc := range []struct {
		name     string
		previous domain.Agent
		next     domain.Agent
		adopt    bool
	}{
		{
			name:     "same known session",
			previous: sessionAgent("p1", "term-old", "session-1"),
			next:     sessionAgent("p1", "term-new", "session-1"),
			adopt:    true,
		},
		{
			name: "sessionless metadata fallback",
			previous: func() domain.Agent {
				a := agent("p1", "term-old", "reviewer", domain.StatusWorking)
				a.Cwd = "/work/repo"
				return a
			}(),
			next: func() domain.Agent {
				a := agent("p1", "term-new", "reviewer", domain.StatusWorking)
				a.Cwd = "/work/repo"
				return a
			}(),
			adopt: true,
		},
		{
			name:     "different known session",
			previous: sessionAgent("p1", "term-old", "session-1"),
			next:     sessionAgent("p1", "term-new", "session-2"),
			adopt:    false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := testkit.NewFakeHerdr(nil)
			r := app.NewRegistry(h, testkit.NewFakeClock(t0), nil)
			h.SetAgents([]domain.Agent{tc.previous})
			if _, err := r.Snapshot(context.Background()); err != nil {
				t.Fatal(err)
			}
			h.SetAgents([]domain.Agent{tc.next})
			evs, err := r.Snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"gone:" + tc.previous.Key.String(), "appeared:" + tc.next.Key.String()}
			if tc.adopt {
				want = []string{"appeared:" + tc.next.Key.String(), "gone:" + tc.previous.Key.String()}
			}
			if got := kinds(evs); !equal(got, want) {
				t.Fatalf("replacement events = %v, want %v", got, want)
			}
		})
	}
}

func TestRegistrySnapshotIgnoresNonAgentPanesAndReportsErrors(t *testing.T) {
	h := testkit.NewFakeHerdr(nil)
	clock := testkit.NewFakeClock(t0)
	r := app.NewRegistry(h, clock, nil)
	h.SetAgents([]domain.Agent{{Key: domain.Key{PaneID: "shell", TerminalID: "t"}}})
	evs, err := r.Snapshot(context.Background())
	if err != nil || len(evs) != 0 {
		t.Fatalf("non-agent pane produced %v, %v", kinds(evs), err)
	}
	h.FailList(domain.ErrDisconnected)
	clock.Advance(time.Second)
	if _, err := r.Snapshot(context.Background()); !errors.Is(err, domain.ErrDisconnected) {
		t.Fatalf("Snapshot with dead socket = %v", err)
	}
	health := r.Health()
	if !health.LastOK.Equal(t0) || !errors.Is(health.LastErr, domain.ErrDisconnected) || !health.LastErrAt.Equal(t0.Add(time.Second)) {
		t.Fatalf("Health = %+v", health)
	}
}

func TestRegistryApplyStatusAndUpdate(t *testing.T) {
	h := testkit.NewFakeHerdr(nil)
	r := app.NewRegistry(h, testkit.NewFakeClock(t0), nil)
	first := agent("p1", "t1", "a", domain.StatusWorking)
	first.Cwd = "/home/op/proj"
	h.SetAgents([]domain.Agent{first})
	if _, err := r.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}

	idle := agent("p1", "", "", domain.StatusIdle)
	evs, structural := r.Apply(domain.HerdrEvent{Kind: domain.PaneAgentStatusChanged, PaneID: "p1", Agent: &idle})
	if structural || !equal(kinds(evs), []string{"changed:p1/t1"}) || evs[0].Agent.Status != domain.StatusIdle || evs[0].Agent.Name != "a" {
		t.Fatalf("status change = %v, structural=%v", evs, structural)
	}
	evs, structural = r.Apply(domain.HerdrEvent{Kind: domain.PaneAgentStatusChanged, PaneID: "p1", Agent: &idle})
	if structural || len(evs) != 0 {
		t.Fatalf("repeated status emitted %v", kinds(evs))
	}
	unknown := agent("p9", "", "", domain.StatusIdle)
	if _, structural = r.Apply(domain.HerdrEvent{Kind: domain.PaneAgentStatusChanged, PaneID: "p9", Agent: &unknown}); !structural {
		t.Fatal("status for unknown pane should schedule a snapshot")
	}

	same := agent("p1", "t1", "a", domain.StatusIdle)
	if evs, structural = r.Apply(domain.HerdrEvent{Kind: domain.PaneUpdated, PaneID: "p1", Agent: &same}); structural || len(evs) != 0 {
		t.Fatalf("identical pane.updated emitted %v", kinds(evs))
	}
	// pane.updated carries no name (verified against Herdr 0.7.5): the
	// name from the last snapshot stays, whatever the event says.
	unnamed := agent("p1", "t1", "", domain.StatusIdle)
	if evs, _ = r.Apply(domain.HerdrEvent{Kind: domain.PaneUpdated, PaneID: "p1", Agent: &unnamed}); len(evs) != 0 {
		t.Fatalf("nameless pane.updated emitted %v", evs)
	}
	if a, _ := r.Agent(domain.Key{PaneID: "p1", TerminalID: "t1"}); a.Name != "a" || a.Cwd != "/home/op/proj" {
		t.Fatalf("name/cwd after nameless pane.updated = %q %q", a.Name, a.Cwd)
	}
	replacement := agent("p1", "t2", "c", domain.StatusWorking)
	if evs, structural = r.Apply(domain.HerdrEvent{Kind: domain.PaneUpdated, PaneID: "p1", Agent: &replacement}); !structural || len(evs) != 0 {
		t.Fatalf("replacement should be structural: %v %v", kinds(evs), structural)
	}
	shell := domain.Agent{Key: domain.Key{PaneID: "p5", TerminalID: "t5"}}
	if evs, structural = r.Apply(domain.HerdrEvent{Kind: domain.PaneUpdated, PaneID: "p5", Agent: &shell}); structural || len(evs) != 0 {
		t.Fatal("non-agent pane.updated should be ignored")
	}
	for _, kind := range []domain.HerdrEventKind{domain.PaneAgentDetected, domain.PaneClosed, domain.PaneExited, domain.TabRenamed, domain.StreamReset} {
		if _, structural = r.Apply(domain.HerdrEvent{Kind: kind, PaneID: "p1"}); !structural {
			t.Fatalf("%s should be structural", kind)
		}
	}
}

// runRegistry starts Run and returns the output channel plus a stop func.
func runRegistry(t *testing.T, r *app.Registry) (<-chan app.AgentEvent, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan app.AgentEvent, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = r.Run(ctx, out)
	}()
	return out, func() {
		cancel()
		<-done
	}
}

func recv(t *testing.T, out <-chan app.AgentEvent) app.AgentEvent {
	t.Helper()
	select {
	case ev := <-out:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("no event within 2s")
		return app.AgentEvent{}
	}
}

func waitListCalls(t *testing.T, h *testkit.FakeHerdr, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if h.ListCalls() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("ListAgents calls = %d, want >= %d", h.ListCalls(), want)
}

func TestRegistryRunCoalescesStructuralEvents(t *testing.T) {
	h := testkit.NewFakeHerdr(nil)
	clock := testkit.NewFakeClock(t0)
	r := app.NewRegistry(h, clock, nil)
	out, stop := runRegistry(t, r)
	defer stop()

	h.SetAgents([]domain.Agent{agent("p1", "t1", "a", domain.StatusWorking)})
	for range 3 {
		h.Push(domain.HerdrEvent{Kind: domain.PaneAgentDetected, PaneID: "p1"})
	}
	// Give Run time to consume the three events, then let the coalesce timer fire.
	waitTimers(t, clock, 2) // interval tick + coalesce
	if h.ListCalls() != 0 {
		t.Fatalf("snapshot before coalesce timer: %d", h.ListCalls())
	}
	clock.Advance(time.Second)
	if ev := recv(t, out); ev.Kind != app.AgentAppeared {
		t.Fatalf("event = %+v", ev)
	}
	if h.ListCalls() != 1 {
		t.Fatalf("ListAgents calls = %d, want 1", h.ListCalls())
	}
}

func TestRegistryRunStreamResetAndRequest(t *testing.T) {
	h := testkit.NewFakeHerdr(nil)
	clock := testkit.NewFakeClock(t0)
	r := app.NewRegistry(h, clock, nil)
	out, stop := runRegistry(t, r)
	defer stop()

	h.SetAgents([]domain.Agent{agent("p1", "t1", "a", domain.StatusWorking)})
	h.Push(domain.HerdrEvent{Kind: domain.StreamReset})
	if ev := recv(t, out); ev.Kind != app.AgentAppeared {
		t.Fatalf("after reset = %+v", ev)
	}
	waitListCalls(t, h, 1)

	h.SetAgents(nil)
	r.RequestSnapshot()
	if ev := recv(t, out); ev.Kind != app.AgentGone || ev.Agent.Status != domain.StatusExited {
		t.Fatalf("after request = %+v", ev)
	}
	waitListCalls(t, h, 2)
}

func TestRegistryRunIntervalAndStatusFastPath(t *testing.T) {
	h := testkit.NewFakeHerdr(nil)
	clock := testkit.NewFakeClock(t0)
	r := app.NewRegistry(h, clock, nil)
	out, stop := runRegistry(t, r)
	defer stop()

	h.SetAgents([]domain.Agent{agent("p1", "t1", "a", domain.StatusWorking)})
	waitTimers(t, clock, 1)
	clock.Advance(15 * time.Second)
	if ev := recv(t, out); ev.Kind != app.AgentAppeared {
		t.Fatalf("interval snapshot = %+v", ev)
	}
	blocked := agent("p1", "", "", domain.StatusBlocked)
	h.Push(domain.HerdrEvent{Kind: domain.PaneAgentStatusChanged, PaneID: "p1", Agent: &blocked})
	if ev := recv(t, out); ev.Kind != app.AgentChanged || ev.Agent.Status != domain.StatusBlocked {
		t.Fatalf("status fast path = %+v", ev)
	}
	if h.ListCalls() != 1 {
		t.Fatalf("status event triggered a snapshot: %d calls", h.ListCalls())
	}
}

// An event that shows a known pane under a new session, closed, exited or
// released retires its key at once: Agent stops vouching for it (sharing
// authorizes through Agent) while Live keeps it, so the next snapshot still
// diffs the replacement against it and the topic can move. Status events
// for the pane no longer touch the retired key.
func TestRegistryQuarantinesSupersededKeyUntilSnapshot(t *testing.T) {
	ctx := context.Background()
	swapped := sessionAgent("p1", "t1", "second")
	for _, tc := range []struct {
		name string
		ev   domain.HerdrEvent
	}{
		{"session swap", domain.HerdrEvent{Kind: domain.PaneUpdated, PaneID: "p1", Agent: &swapped}},
		{"closed", domain.HerdrEvent{Kind: domain.PaneClosed, PaneID: "p1"}},
		{"exited", domain.HerdrEvent{Kind: domain.PaneExited, PaneID: "p1"}},
		{"released", domain.HerdrEvent{Kind: domain.PaneAgentDetected, PaneID: "p1", Released: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := testkit.NewFakeHerdr(nil)
			r := app.NewRegistry(h, testkit.NewFakeClock(t0), nil)
			first := sessionAgent("p1", "t1", "first")
			h.SetAgents([]domain.Agent{first})
			if _, err := r.Snapshot(ctx); err != nil {
				t.Fatal(err)
			}
			h.FailList(domain.ErrDisconnected)
			if evs, structural := r.Apply(tc.ev); !structural || len(evs) != 0 {
				t.Fatalf("retiring event = %v, structural=%v", kinds(evs), structural)
			}
			if _, err := r.Snapshot(ctx); err == nil {
				t.Fatal("snapshot unexpectedly succeeded")
			}
			if _, ok := r.Agent(first.Key); ok {
				t.Fatal("retired key still authoritative before a successful snapshot")
			}
			if live := r.Live(); len(live) != 1 || live[0].Key != first.Key {
				t.Fatalf("Live dropped the retired key before the snapshot: %v", live)
			}
			idle := domain.Agent{Kind: "claude", Status: domain.StatusIdle}
			if evs, structural := r.Apply(domain.HerdrEvent{Kind: domain.PaneAgentStatusChanged, PaneID: "p1", Agent: &idle}); !structural || len(evs) != 0 {
				t.Fatalf("status applied to a retired key: %v structural=%v", kinds(evs), structural)
			}
			if evs, _ := r.Apply(domain.HerdrEvent{Kind: domain.PaneUpdated, PaneID: "p1", Agent: &first}); len(evs) != 0 {
				t.Fatalf("pane.updated revived a retired key: %v", kinds(evs))
			}
			if _, ok := r.Agent(first.Key); ok {
				t.Fatal("retired key revived by an event")
			}

			h.FailList(nil)
			h.SetAgents([]domain.Agent{swapped})
			evs, err := r.Snapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			// The same events as without the retirement: the reconciler
			// still sees the old key leave.
			if want := []string{"gone:" + first.Key.String(), "appeared:" + swapped.Key.String()}; !equal(kinds(evs), want) {
				t.Fatalf("snapshot after retirement = %v, want %v", kinds(evs), want)
			}
			if _, ok := r.Agent(swapped.Key); !ok {
				t.Fatal("replacement not live after the snapshot")
			}
			if _, ok := r.Agent(first.Key); ok {
				t.Fatal("retired key live after the snapshot")
			}
		})
	}
}

// A snapshot that still lists the key is the source of truth and lifts the
// quarantine.
func TestRegistrySnapshotLiftsQuarantine(t *testing.T) {
	ctx := context.Background()
	h := testkit.NewFakeHerdr(nil)
	r := app.NewRegistry(h, testkit.NewFakeClock(t0), nil)
	first := sessionAgent("p1", "t1", "first")
	h.SetAgents([]domain.Agent{first})
	if _, err := r.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	r.Apply(domain.HerdrEvent{Kind: domain.PaneExited, PaneID: "p1"})
	evs, err := r.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 0 {
		t.Fatalf("unchanged snapshot emitted %v", kinds(evs))
	}
	if _, ok := r.Agent(first.Key); !ok {
		t.Fatal("snapshot did not lift the quarantine")
	}
}

// hangingHerdr is a gateway whose agent.list never answers while hang is
// set: it returns only when the caller's context ends.
type hangingHerdr struct {
	*testkit.FakeHerdr
	hang atomic.Bool
}

func (h *hangingHerdr) ListAgents(ctx context.Context) ([]domain.Agent, error) {
	if h.hang.Load() {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return h.FakeHerdr.ListAgents(ctx)
}

func TestRegistrySnapshotDeadline(t *testing.T) {
	h := &hangingHerdr{FakeHerdr: testkit.NewFakeHerdr(nil)}
	h.SetAgents([]domain.Agent{agent("p1", "t1", "a", domain.StatusWorking)})
	h.hang.Store(true)
	var logBuf bytes.Buffer
	r := app.NewRegistry(h, testkit.NewFakeClock(t0), slog.New(slog.NewTextHandler(&logBuf, nil)))
	r.SnapshotTimeout = 50 * time.Millisecond

	_, err := r.Snapshot(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Snapshot err = %v, want a wrapped DeadlineExceeded", err)
	}
	if hl := r.Health(); hl.LastErr == nil || !hl.LastOK.IsZero() {
		t.Errorf("health after a timeout = %+v", hl)
	}
	if log := logBuf.String(); !strings.Contains(log, "agent snapshot failed") || !strings.Contains(log, "timeout=true") {
		t.Errorf("timeout not logged: %s", log)
	}

	h.hang.Store(false)
	evs, err := r.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot after recovery: %v", err)
	}
	if got := kinds(evs); !equal(got, []string{"appeared:p1/t1"}) {
		t.Fatalf("events after recovery = %v", got)
	}
	if hl := r.Health(); hl.LastErr != nil || hl.LastOK.IsZero() {
		t.Errorf("health after recovery = %+v", hl)
	}
}

// waitTimers blocks until the fake clock holds at least n armed timers.
func waitTimers(t *testing.T, clock *testkit.FakeClock, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if clock.Pending() >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("pending timers = %d, want >= %d", clock.Pending(), n)
}
