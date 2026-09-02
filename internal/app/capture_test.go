package app

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

// captureFixture drives a Capture with scripted live agents and screens.
type captureFixture struct {
	herdr   *testkit.FakeHerdr
	clock   *testkit.FakeClock
	agents  map[domain.Key]domain.Agent
	capture *Capture
	ctx     context.Context
}

func newCaptureFixture(t *testing.T) *captureFixture {
	t.Helper()
	f := &captureFixture{
		herdr:  testkit.NewFakeHerdr(nil),
		clock:  testkit.NewFakeClock(tb0),
		agents: map[domain.Key]domain.Agent{},
		ctx:    context.Background(),
	}
	live := func() []domain.Agent {
		var out []domain.Agent
		for _, a := range f.agents {
			out = append(out, a)
		}
		return out
	}
	f.capture = NewCapture(f.herdr, live, f.clock, nil)
	return f
}

func (f *captureFixture) agent(pane string, st domain.Status) domain.Agent {
	a := domain.Agent{Key: domain.Key{PaneID: pane, TerminalID: "t"}, Kind: "claude", Status: st}
	f.agents[a.Key] = a
	return a
}

func (f *captureFixture) status(a domain.Agent, st domain.Status) domain.Agent {
	a.Status = st
	f.agents[a.Key] = a
	return a
}

// text builds a screen of numbered lines from..to, newline separated.
func text(from, to int) string {
	var b strings.Builder
	for i := from; i <= to; i++ {
		if i > from {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "L%d", i)
	}
	return b.String()
}

func TestCaptureTickReadsWorkingAgentsOnly(t *testing.T) {
	f := newCaptureFixture(t)
	working := f.agent("p1", domain.StatusWorking)
	f.agent("p2", domain.StatusIdle)
	f.herdr.SetScreen("p1", text(1, 20))
	f.herdr.SetScreen("p2", text(1, 20))

	f.capture.tick(f.ctx)
	reads := f.herdr.Reads()
	if len(reads) != 1 || reads[0] != (testkit.ReadCall{Target: "p1", Source: domain.ScreenRecent, Lines: captureLines}) {
		t.Fatalf("Reads = %+v", reads)
	}
	// The same screen again is hashed and skipped by the history.
	f.capture.tick(f.ctx)
	if h := f.capture.hist[working.Key]; h.Len() != 12 {
		t.Fatalf("committed after unchanged tick = %d, want 12", h.Len())
	}
	// A scrolled screen adds the new lines.
	f.herdr.SetScreen("p1", text(4, 24))
	f.capture.tick(f.ctx)
	lines, marked, err := f.capture.Since(f.ctx, working.Key)
	if err != nil || marked {
		t.Fatalf("Since = %v, marked %v", err, marked)
	}
	if want := screenLines(text(1, 24)); !reflect.DeepEqual(lines, want) {
		t.Fatalf("Since lines = %v\nwant %v", lines, want)
	}
}

func TestCaptureRunFiresOnClock(t *testing.T) {
	f := newCaptureFixture(t)
	f.agent("p1", domain.StatusWorking)
	f.herdr.SetScreen("p1", text(1, 20))
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.capture.Run(ctx)
	}()
	waitUntil(t, "capture timer", func() bool { return f.clock.Pending() == 1 })
	f.clock.Advance(f.capture.Interval)
	waitUntil(t, "capture read", func() bool { return len(f.herdr.Reads()) == 1 })
	waitUntil(t, "capture timer re-armed", func() bool { return f.clock.Pending() == 1 })
	cancel()
	<-done
}

func TestCaptureGraceAfterLeavingWorking(t *testing.T) {
	f := newCaptureFixture(t)
	a := f.agent("p1", domain.StatusWorking)
	f.herdr.SetScreen("p1", text(1, 20))
	f.capture.Observe(AgentEvent{Kind: AgentAppeared, Agent: a})
	f.capture.tick(f.ctx)

	idle := f.status(a, domain.StatusIdle)
	f.capture.Observe(AgentEvent{Kind: AgentChanged, Agent: idle})
	f.herdr.SetScreen("p1", text(4, 24))
	f.clock.Advance(f.capture.Grace / 2)
	f.capture.tick(f.ctx)
	if n := len(f.herdr.Reads()); n != 2 {
		t.Fatalf("reads within grace = %d, want 2", n)
	}
	f.clock.Advance(f.capture.Grace)
	f.capture.tick(f.ctx)
	if n := len(f.herdr.Reads()); n != 2 {
		t.Fatalf("reads after grace = %d, want still 2", n)
	}
	if h := f.capture.hist[a.Key]; h.Len() != 16 {
		t.Fatalf("committed = %d, want 16 (final screen merged during grace)", h.Len())
	}
}

func TestCaptureObserveMarksTransitionsIntoWorking(t *testing.T) {
	f := newCaptureFixture(t)
	a := f.agent("p1", domain.StatusIdle)
	f.herdr.SetScreen("p1", text(1, 20))
	f.capture.Observe(AgentEvent{Kind: AgentAppeared, Agent: a})
	if _, marked, _ := f.capture.Since(f.ctx, a.Key); marked {
		t.Fatal("idle appearance must not mark")
	}

	// idle -> working marks after the current screen was committed.
	f.capture.Observe(AgentEvent{Kind: AgentChanged, Agent: f.status(a, domain.StatusWorking)})
	f.herdr.SetScreen("p1", text(4, 24))
	f.capture.tick(f.ctx)
	lines, marked, _ := f.capture.Since(f.ctx, a.Key)
	if !marked {
		t.Fatal("working transition must mark")
	}
	if want := screenLines(text(13, 24)); !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines after mark = %v\nwant %v", lines, want)
	}

	// working -> working (a label change) keeps the mark where it was.
	f.capture.Observe(AgentEvent{Kind: AgentChanged, Agent: f.status(a, domain.StatusWorking)})
	f.herdr.SetScreen("p1", text(8, 28))
	f.capture.tick(f.ctx)
	lines, _, _ = f.capture.Since(f.ctx, a.Key)
	if lines[0] != "L13" {
		t.Fatalf("mark moved on working->working: first line %q", lines[0])
	}

	// blocked -> working (an answered question) marks again once the
	// agent was away long enough for a human to have answered.
	f.capture.Observe(AgentEvent{Kind: AgentChanged, Agent: f.status(a, domain.StatusBlocked)})
	f.clock.Advance(f.capture.MinAway)
	f.capture.Observe(AgentEvent{Kind: AgentChanged, Agent: f.status(a, domain.StatusWorking)})
	f.herdr.SetScreen("p1", text(12, 32))
	f.capture.tick(f.ctx)
	lines, _, _ = f.capture.Since(f.ctx, a.Key)
	if want := screenLines(text(21, 32)); !reflect.DeepEqual(lines, want) {
		t.Fatalf("lines after second mark = %v\nwant %v", lines, want)
	}
}

func TestCaptureGoneDropsHistory(t *testing.T) {
	f := newCaptureFixture(t)
	a := f.agent("p1", domain.StatusWorking)
	f.herdr.SetScreen("p1", text(1, 20))
	f.capture.Observe(AgentEvent{Kind: AgentAppeared, Agent: a})
	f.capture.tick(f.ctx)
	f.capture.Observe(AgentEvent{Kind: AgentGone, Agent: a})
	if len(f.capture.hist) != 0 || len(f.capture.status) != 0 || len(f.capture.last) != 0 {
		t.Fatalf("state after gone: hist %d status %d last %d", len(f.capture.hist), len(f.capture.status), len(f.capture.last))
	}
	f.herdr.SetScreen("p1", text(50, 69))
	lines, marked, err := f.capture.Since(f.ctx, a.Key)
	if err != nil || marked {
		t.Fatalf("Since after gone = %v, marked %v", err, marked)
	}
	if lines[0] != "L50" || len(lines) != 20 {
		t.Fatalf("Since after gone = %v", lines)
	}
}

func TestCaptureReadErrorIsSkipped(t *testing.T) {
	f := newCaptureFixture(t)
	a := f.agent("p1", domain.StatusWorking)
	f.herdr.SetScreen("p1", text(1, 20))
	f.herdr.FailNext("read", domain.ErrDisconnected)
	f.capture.tick(f.ctx)
	if _, ok := f.capture.hist[a.Key]; ok {
		t.Fatal("a failed read must not create a history")
	}
	f.capture.tick(f.ctx)
	if h := f.capture.hist[a.Key]; h == nil || h.Len() != 12 {
		t.Fatalf("history after recovery = %v", h)
	}
	f.herdr.FailNext("read", domain.ErrAgentGone)
	if _, _, err := f.capture.Since(f.ctx, a.Key); err == nil {
		t.Fatal("Since must return the read error")
	}
}

func TestCaptureTickStopsOnCancelledContext(t *testing.T) {
	f := newCaptureFixture(t)
	f.capture.ReadTimeout = time.Millisecond
	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	f.agent("p1", domain.StatusWorking)
	f.capture.tick(ctx)
	if n := len(f.herdr.Reads()); n != 0 {
		t.Fatalf("a cancelled context must stop the tick before reading, got %d reads", n)
	}
}

func TestCaptureShortFlapDoesNotMark(t *testing.T) {
	f := newCaptureFixture(t)
	a := f.agent("p1", domain.StatusWorking)
	f.herdr.SetScreen("p1", text(1, 20))
	f.capture.tick(f.ctx)
	// The first sighting of a working agent marks after what was captured.
	f.capture.Observe(AgentEvent{Kind: AgentAppeared, Agent: a})
	f.herdr.SetScreen("p1", text(4, 24))
	f.capture.tick(f.ctx)

	// A one-second dip into idle or blocked is Herdr's detection, not a
	// human: the mark stays where it was.
	for _, dip := range []domain.Status{domain.StatusIdle, domain.StatusBlocked} {
		f.capture.Observe(AgentEvent{Kind: AgentChanged, Agent: f.status(a, dip)})
		f.clock.Advance(time.Second)
		f.capture.Observe(AgentEvent{Kind: AgentChanged, Agent: f.status(a, domain.StatusWorking)})
	}
	lines, marked, _ := f.capture.Since(f.ctx, a.Key)
	if !marked || lines[0] != "L13" {
		t.Fatalf("mark moved on a flap: marked %v, first line %q", marked, lines[0])
	}
	if _, ok := f.capture.left[a.Key]; ok {
		t.Fatal("left must be cleared on the return to working")
	}

	// A pause of MinAway or more is a human message.
	f.capture.Observe(AgentEvent{Kind: AgentChanged, Agent: f.status(a, domain.StatusBlocked)})
	f.clock.Advance(f.capture.MinAway)
	f.capture.Observe(AgentEvent{Kind: AgentChanged, Agent: f.status(a, domain.StatusWorking)})
	f.herdr.SetScreen("p1", text(8, 28))
	f.capture.tick(f.ctx)
	lines, _, _ = f.capture.Since(f.ctx, a.Key)
	if lines[0] != "L17" {
		t.Fatalf("mark after a real pause: first line %q, want L17", lines[0])
	}
}
