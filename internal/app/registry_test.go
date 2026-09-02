package app_test

import (
	"context"
	"errors"
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
	h.SetAgents([]domain.Agent{agent("p1", "t1", "a", domain.StatusWorking)})
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
	if a, _ := r.Agent(domain.Key{PaneID: "p1", TerminalID: "t1"}); a.Name != "a" {
		t.Fatalf("name after nameless pane.updated = %q", a.Name)
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
