package herdr

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

var fastBackoff = Backoff{Min: 5 * time.Millisecond, Max: 20 * time.Millisecond}

// runResult caches the return value of Stream.Run so several waiters
// (the test and its cleanup) can observe it.
type runResult struct {
	done chan struct{}
	err  error
}

func (r *runResult) wait(t *testing.T) error {
	t.Helper()
	select {
	case <-r.done:
		return r.err
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
		return nil
	}
}

func startStream(t *testing.T, s *testkit.NDJSONServer, panes []string) (chan domain.Event, context.CancelFunc, *runResult) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	st := NewStream(dial, s.Path(), testLogger(t), fastBackoff)
	st.SetPanes(panes)
	out := make(chan domain.Event, 16)
	res := &runResult{done: make(chan struct{})}
	go func() {
		res.err = st.Run(ctx, out)
		close(res.done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-res.done:
		case <-time.After(2 * time.Second):
			t.Error("Run did not stop after cancel")
		}
	})
	if got := s.WaitRequests("events.subscribe", 1, 2*time.Second); len(got) < 1 {
		t.Fatalf("no subscribe request")
	}
	return out, cancel, res
}

func next(t *testing.T, out <-chan domain.Event) domain.HerdrEvent {
	t.Helper()
	select {
	case ev := <-out:
		he, ok := ev.(domain.HerdrEvent)
		if !ok {
			t.Fatalf("event %T is not HerdrEvent", ev)
		}
		return he
	case <-time.After(2 * time.Second):
		t.Fatal("no event")
		return domain.HerdrEvent{}
	}
}

func subscribedTypes(t *testing.T, r testkit.Request) []subscription {
	t.Helper()
	var p subscribeParams
	if err := json.Unmarshal(r.Params, &p); err != nil {
		t.Fatalf("subscribe params: %v", err)
	}
	return p.Subscriptions
}

func TestStreamDeliversEventsInOrder(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	out, _, _ := startStream(t, s, []string{"w1:p1"})

	subs := subscribedTypes(t, s.Requests()[0])
	if len(subs) != len(globalKinds)+1 {
		t.Fatalf("subscriptions = %+v", subs)
	}
	if last := subs[len(subs)-1]; last.Type != "pane.agent_status_changed" || last.PaneID != "w1:p1" {
		t.Fatalf("pane subscription = %+v", last)
	}

	kind := "claude"
	s.Push("pane_agent_detected", map[string]any{"pane_id": "w1:p1", "workspace_id": "w1", "agent": kind, "released": false, "final_status": nil})
	s.Push("pane_agent_status_changed", map[string]any{"pane_id": "w1:p1", "workspace_id": "w1", "agent": "claude", "agent_status": "blocked", "title": "✳ ask"})
	s.Push("some.future_kind", map[string]any{"x": 1})
	s.Push("pane_closed", map[string]any{"pane_id": "w1:p1", "workspace_id": "w1"})

	ev := next(t, out)
	if ev.Kind != domain.PaneAgentDetected || ev.PaneID != "w1:p1" || ev.Agent == nil || ev.Agent.Kind != "claude" || ev.Released || ev.FinalStatus != nil {
		t.Fatalf("event 1 = %+v", ev)
	}
	ev = next(t, out)
	if ev.Kind != domain.PaneAgentStatusChanged || ev.Agent == nil || ev.Agent.Status != domain.StatusBlocked || ev.Agent.Title != "ask" {
		t.Fatalf("event 2 = %+v agent=%+v", ev, ev.Agent)
	}
	ev = next(t, out)
	if ev.Kind != domain.PaneClosed || ev.PaneID != "w1:p1" {
		t.Fatalf("event 3 = %+v", ev)
	}
}

func TestStreamKeepsEventsPushedRightAfterSubscribe(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	// Herdr may push current-state events immediately after the reply.
	// Pushing from the test as soon as the request lands is close enough.
	go func() {
		s.WaitRequests("events.subscribe", 1, time.Second)
		for s.SubscriptionCount() == 0 {
			time.Sleep(time.Millisecond)
		}
		s.Push("pane_closed", map[string]any{"pane_id": "w1:p9", "workspace_id": "w1"})
	}()
	out, _, _ := startStream(t, s, nil)
	if ev := next(t, out); ev.Kind != domain.PaneClosed || ev.PaneID != "w1:p9" {
		t.Fatalf("event = %+v", ev)
	}
}

func TestStreamSetPanesResubscribes(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := NewStream(dial, s.Path(), testLogger(t), fastBackoff)
	out := make(chan domain.Event, 16)
	done := make(chan error, 1)
	go func() { done <- st.Run(ctx, out) }()
	s.WaitRequests("events.subscribe", 1, 2*time.Second)
	if !s.WaitConns(1, time.Second) {
		t.Fatalf("conns = %d", s.ConnCount())
	}

	st.SetPanes([]string{"w1:p2", "w1:p1", "w1:p2"})
	reqs := s.WaitRequests("events.subscribe", 2, 2*time.Second)
	if len(reqs) != 2 {
		t.Fatalf("subscribe requests = %d", len(reqs))
	}
	subs := subscribedTypes(t, reqs[1])
	var panes []string
	for _, sub := range subs {
		if sub.Type == "pane.agent_status_changed" {
			panes = append(panes, sub.PaneID)
		}
	}
	if len(panes) != 2 || panes[0] != "w1:p1" || panes[1] != "w1:p2" {
		t.Fatalf("panes = %v", panes)
	}
	if !s.WaitConns(1, time.Second) {
		t.Fatalf("old connection not closed: conns = %d", s.ConnCount())
	}
	if s.SubscriptionCount() != 1 {
		t.Fatalf("subscriptions = %d", s.SubscriptionCount())
	}

	// Same set again is a no-op.
	st.SetPanes([]string{"w1:p1", "w1:p2"})
	time.Sleep(20 * time.Millisecond)
	if got := len(s.WaitRequests("events.subscribe", 3, 50*time.Millisecond)); got != 2 {
		t.Fatalf("unexpected resubscribe: %d requests", got)
	}

	// The replacement connection is live.
	s.Push("tab_renamed", map[string]any{"tab_id": "w1:t1", "label": "new"})
	if ev := next(t, out); ev.Kind != domain.TabRenamed || ev.TabID != "w1:t1" || ev.Label != "new" {
		t.Fatalf("event = %+v", ev)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestStreamResetOnServerRestart(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	out, _, _ := startStream(t, s, nil)
	s.CloseAll()
	if ev := next(t, out); ev.Kind != domain.StreamReset {
		t.Fatalf("event = %+v, want StreamReset", ev)
	}
	if got := s.WaitRequests("events.subscribe", 2, 2*time.Second); len(got) != 2 {
		t.Fatalf("resubscribe requests = %d", len(got))
	}
	for s.SubscriptionCount() == 0 {
		time.Sleep(time.Millisecond)
	}
	s.Push("pane_exited", map[string]any{"pane_id": "w1:p1", "workspace_id": "w1"})
	if ev := next(t, out); ev.Kind != domain.PaneExited {
		t.Fatalf("event after reset = %+v", ev)
	}
}

func TestStreamRetriesWhenServerDown(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	path := s.Path()
	s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := NewStream(dial, path, testLogger(t), fastBackoff)
	done := make(chan error, 1)
	go func() { done <- st.Run(ctx, make(chan domain.Event)) }()
	time.Sleep(50 * time.Millisecond) // a few failed attempts
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop while retrying")
	}
}

func TestStreamCancelStopsPromptly(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	_, cancel, res := startStream(t, s, []string{"w1:p1"})
	start := time.Now()
	cancel()
	if err := res.wait(t); err != context.Canceled {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("stop took %v", d)
	}
	if !s.WaitConns(0, time.Second) {
		t.Fatalf("connection left open")
	}
}

func TestTranslateEvent(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	tests := []struct {
		name string
		line string
		want domain.HerdrEvent
		ok   bool
	}{
		{
			name: "released agent with final status",
			line: `{"event":"pane_agent_detected","data":{"pane_id":"p","workspace_id":"w","agent":null,"released":true,"final_status":"done"}}`,
			want: domain.HerdrEvent{Kind: domain.PaneAgentDetected, PaneID: "p", WorkspaceID: "w", Released: true, FinalStatus: ptr(domain.StatusDone)},
			ok:   true,
		},
		{
			name: "dotted kind accepted",
			line: `{"event":"pane.closed","data":{"pane_id":"p","workspace_id":"w"}}`,
			want: domain.HerdrEvent{Kind: domain.PaneClosed, PaneID: "p", WorkspaceID: "w"},
			ok:   true,
		},
		{
			name: "pane updated carries full agent",
			line: `{"event":"pane_updated","data":{"pane":` + agentListSampleAgent + `}}`,
			want: domain.HerdrEvent{Kind: domain.PaneUpdated, PaneID: "w3:p2", WorkspaceID: "w3", TabID: "w3:t2"},
			ok:   true,
		},
		{name: "unknown kind", line: `{"event":"layout_updated","data":{}}`, ok: false},
		{
			name: "workspace renamed",
			line: `{"event":"workspace_renamed","data":{"workspace_id":"w3","label":"Jobs"}}`,
			want: domain.HerdrEvent{Kind: domain.WorkspaceRenamed, WorkspaceID: "w3", Label: "Jobs"},
			ok:   true,
		},
		{name: "bad json", line: `{nope`, ok: false},
		{name: "reply line without event", line: `{"id":"r1","result":{}}`, ok: false},
		{name: "bad data", line: `{"event":"pane_closed","data":"str"}`, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := translateEvent([]byte(tt.line), log)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v (%+v)", ok, tt.ok, got)
			}
			if !ok {
				return
			}
			if got.Kind == domain.PaneUpdated {
				if got.Agent == nil || got.Agent.TerminalID != "term_65a77760e3b0a1" || got.Agent.Status != domain.StatusIdle {
					t.Fatalf("agent = %+v", got.Agent)
				}
				got.Agent = nil
			}
			if (got.FinalStatus == nil) != (tt.want.FinalStatus == nil) || (got.FinalStatus != nil && *got.FinalStatus != *tt.want.FinalStatus) {
				t.Fatalf("final status = %v", got.FinalStatus)
			}
			got.FinalStatus, tt.want.FinalStatus = nil, nil
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestBackoffNext(t *testing.T) {
	b := Backoff{Min: 250 * time.Millisecond, Max: 10 * time.Second}
	for i, want := range []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second, 10 * time.Second} {
		if got := b.Next(i); got != want {
			t.Errorf("Next(%d) = %v, want %v", i, got, want)
		}
	}
	j := Backoff{Min: time.Second, Max: time.Second, Jitter: 0.5}
	for i := 0; i < 20; i++ {
		if got := j.Next(i); got < time.Second || got > 1500*time.Millisecond {
			t.Fatalf("jittered Next = %v", got)
		}
	}
	if got := (Backoff{}).Next(0); got != DefaultBackoff.Min {
		t.Fatalf("zero backoff Next = %v", got)
	}
}

func ptr[T any](v T) *T { return &v }

// agentListSampleAgent is the single agent object from agentListSample.
const agentListSampleAgent = `{
    "agent": "claude",
    "agent_status": "idle",
    "focused": false,
    "pane_id": "w3:p2",
    "revision": 172,
    "state_change_seq": 9,
    "tab_id": "w3:t2",
    "terminal_id": "term_65a77760e3b0a1",
    "terminal_title": "✳ Объяснение первого пункта",
    "terminal_title_stripped": "Объяснение первого пункта",
    "workspace_id": "w3"
  }`

// TestStreamSetPanesKeepsInFlightEvents pins the guarantee documented on
// SetPanes: swapping the subscription must not lose events the old
// connection had already delivered. The consumer is deliberately slow so
// events pile up inside the reader while the swap happens.
func TestStreamSetPanesKeepsInFlightEvents(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	st := NewStream(dial, s.Path(), testLogger(t), fastBackoff)
	out := make(chan domain.Event) // unbuffered: the loop parks in emit
	done := make(chan error, 1)
	go func() { done <- st.Run(ctx, out) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Run did not stop")
		}
	})
	s.WaitRequests("events.subscribe", 1, 2*time.Second)

	const rounds, perRound = 12, 3
	want, got := 0, 0
	for r := 0; r < rounds; r++ {
		if !s.WaitConns(1, time.Second) {
			t.Fatalf("round %d: conns = %d", r, s.ConnCount())
		}
		for i := 0; i < perRound; i++ {
			if n := s.Push("tab_renamed", map[string]any{"tab_id": fmt.Sprintf("t%d-%d", r, i), "label": "x"}); n != 1 {
				t.Fatalf("round %d: push reached %d connections", r, n)
			}
		}
		want += perRound
		st.SetPanes([]string{fmt.Sprintf("w1:p%d", r)})
		deadline := time.After(time.Second)
	drain:
		for got < want {
			select {
			case ev := <-out:
				if he, ok := ev.(domain.HerdrEvent); ok && he.Kind == domain.TabRenamed {
					got++
				} else {
					t.Fatalf("round %d: unexpected event %+v", r, ev)
				}
			case <-deadline:
				break drain
			}
		}
		if got != want {
			t.Fatalf("round %d: delivered %d of %d events across the resubscribe", r, got, want)
		}
	}
}

// TestStreamFlappingEscalates checks that a subscription which drops before
// stableAfter counts as a failed attempt, so the delay climbs the schedule,
// and that a stable one resets it.
func TestStreamFlappingEscalates(t *testing.T) {
	s := testkit.NewNDJSONServer(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := NewStream(dial, s.Path(), testLogger(t), Backoff{Min: 10 * time.Millisecond, Max: 80 * time.Millisecond})
	delays := make(chan time.Duration, 16)
	st.sleep = func(ctx context.Context, d time.Duration) bool {
		delays <- d
		return ctx.Err() == nil
	}
	var mu sync.Mutex
	now := time.Unix(1000, 0)
	st.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
	}
	out := make(chan domain.Event, 64)
	done := make(chan error, 1)
	go func() { done <- st.Run(ctx, out) }()

	drop := func(i int) time.Duration {
		t.Helper()
		if got := s.WaitRequests("events.subscribe", i+1, 2*time.Second); len(got) < i+1 {
			t.Fatalf("subscribe %d did not arrive", i+1)
		}
		if !s.WaitConns(1, 2*time.Second) {
			t.Fatalf("subscription %d not open", i+1)
		}
		s.CloseAll()
		select {
		case d := <-delays:
			return d
		case <-time.After(2 * time.Second):
			t.Fatalf("no delay after drop %d", i+1)
			return 0
		}
	}

	want := []time.Duration{10, 20, 40, 80, 80}
	for i, w := range want {
		if got := drop(i); got != w*time.Millisecond {
			t.Fatalf("drop %d: delay = %v, want %v", i+1, got, w*time.Millisecond)
		}
	}
	// A connection that lives longer than stableAfter resets the counter.
	if got := s.WaitRequests("events.subscribe", len(want)+1, 2*time.Second); len(got) < len(want)+1 {
		t.Fatal("no resubscribe after the last drop")
	}
	if !s.WaitConns(1, 2*time.Second) {
		t.Fatal("stable subscription not open")
	}
	advance(stableAfter + time.Second)
	s.CloseAll()
	select {
	case d := <-delays:
		if d != 10*time.Millisecond {
			t.Fatalf("delay after a stable run = %v, want 10ms", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no delay after the stable drop")
	}
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}
