package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
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
	logs    *recordHandler
}

type scriptedRead struct {
	source domain.ScreenSource
	screen domain.Screen
	err    error
	after  func()
}

// scriptedHerdr lets capture tests choose a response for each source while
// retaining FakeHerdr for the other gateway methods.
type scriptedHerdr struct {
	*testkit.FakeHerdr
	mu    sync.Mutex
	steps []scriptedRead
	reads []testkit.ReadCall
}

func (h *scriptedHerdr) ReadScreen(ctx context.Context, target string, source domain.ScreenSource, lines int) (domain.Screen, error) {
	h.mu.Lock()
	h.reads = append(h.reads, testkit.ReadCall{Target: target, Source: source, Lines: lines})
	if len(h.steps) == 0 {
		h.mu.Unlock()
		return h.FakeHerdr.ReadScreen(ctx, target, source, lines)
	}
	step := h.steps[0]
	h.steps = h.steps[1:]
	h.mu.Unlock()
	if step.source != source {
		return domain.Screen{}, fmt.Errorf("script expected %s read, got %s", step.source, source)
	}
	if step.after != nil {
		step.after()
	}
	return step.screen, step.err
}

func (h *scriptedHerdr) Reads() []testkit.ReadCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]testkit.ReadCall(nil), h.reads...)
}

func newCaptureFixture(t *testing.T) *captureFixture {
	t.Helper()
	f := &captureFixture{
		herdr:  testkit.NewFakeHerdr(nil),
		clock:  testkit.NewFakeClock(tb0),
		agents: map[domain.Key]domain.Agent{},
		ctx:    context.Background(),
		logs:   &recordHandler{},
	}
	live := func() []domain.Agent {
		var out []domain.Agent
		for _, a := range f.agents {
			out = append(out, a)
		}
		return out
	}
	f.capture = NewCapture(f.herdr, live, f.clock, slog.New(f.logs))
	return f
}

func (f *captureFixture) scriptReads(steps ...scriptedRead) *scriptedHerdr {
	h := &scriptedHerdr{FakeHerdr: f.herdr, steps: append([]scriptedRead(nil), steps...)}
	f.capture.herdr = h
	return h
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

// A recent screen merged after a visible one carries an older prefix the
// visible snapshot lacked; the history must stay continuous.
func TestCaptureVisibleThenRecentWithOlderPrefixKeepsHistoryContinuous(t *testing.T) {
	f := newCaptureFixture(t)
	a := f.agent("p1", domain.StatusWorking)
	f.capture.merge(a.Key, domain.Screen{Text: text(1, 20)}, domain.ScreenRecent)
	f.capture.merge(a.Key, domain.Screen{Text: text(4, 24)}, domain.ScreenVisible)
	f.capture.merge(a.Key, domain.Screen{Text: text(1, 28)}, domain.ScreenRecent)
	if got, want := f.capture.hist[a.Key].Lines(), screenLines(text(1, 28)); !reflect.DeepEqual(got, want) {
		t.Fatalf("history after visible → recent = %v\nwant %v", got, want)
	}
}

// Herdr answers every recent read of a working alternate-screen agent with
// agent_not_idle, so once it did the capture reads visible directly, also
// after the agent leaves working; otherwise Herdr logs one error per second
// while it works and scrolls its transcript once it is idle.
func TestCaptureReadsVisibleWhileRecentIsBusy(t *testing.T) {
	f := newCaptureFixture(t)
	a := f.agent("p1", domain.StatusWorking)
	f.capture.Observe(AgentEvent{Kind: AgentAppeared, Agent: a})
	h := f.scriptReads(
		scriptedRead{source: domain.ScreenRecent, err: domain.ErrAgentBusy},
		scriptedRead{source: domain.ScreenVisible, screen: domain.Screen{Text: text(1, 20)}},
		scriptedRead{source: domain.ScreenVisible, screen: domain.Screen{Text: text(2, 21)}},
		scriptedRead{source: domain.ScreenVisible, screen: domain.Screen{Text: text(3, 22)}},
		scriptedRead{source: domain.ScreenVisible, screen: domain.Screen{Text: text(5, 24)}},
	)
	for range 3 {
		f.capture.tick(f.ctx)
	}
	idle := f.status(a, domain.StatusIdle)
	f.capture.Observe(AgentEvent{Kind: AgentChanged, Agent: idle})
	f.capture.tick(f.ctx)

	wantCalls := []testkit.ReadCall{
		{Target: "p1", Source: domain.ScreenRecent, Lines: captureLines},
		{Target: "p1", Source: domain.ScreenVisible, Lines: captureLines},
		{Target: "p1", Source: domain.ScreenVisible, Lines: captureLines},
		{Target: "p1", Source: domain.ScreenVisible, Lines: captureLines},
		{Target: "p1", Source: domain.ScreenVisible, Lines: captureLines},
	}
	if got := h.Reads(); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("Reads = %+v\nwant %+v", got, wantCalls)
	}
	if got, want := f.capture.hist[a.Key].Lines(), screenLines(text(1, 24)); !reflect.DeepEqual(got, want) {
		t.Fatalf("history = %v\nwant %v", got, want)
	}
}

// Herdr serves a recent read of an idle alternate-screen agent by scrolling
// its transcript with the mouse wheel, which the user sees as the pane
// running from top to bottom. Once a key was refused as busy it is such an
// agent, so neither the grace reads nor Since may ask for recent again.
func TestCaptureNeverReadsRecentAfterBusyRefusal(t *testing.T) {
	f := newCaptureFixture(t)
	a := f.agent("p1", domain.StatusWorking)
	f.capture.Observe(AgentEvent{Kind: AgentAppeared, Agent: a})
	h := f.scriptReads(
		scriptedRead{source: domain.ScreenRecent, err: domain.ErrAgentBusy},
		scriptedRead{source: domain.ScreenVisible, screen: domain.Screen{Text: text(1, 20)}},
		scriptedRead{source: domain.ScreenVisible, screen: domain.Screen{Text: text(2, 21)}},
		scriptedRead{source: domain.ScreenVisible, screen: domain.Screen{Text: text(2, 21)}},
		scriptedRead{source: domain.ScreenVisible, screen: domain.Screen{Text: text(2, 21)}},
	)
	f.capture.tick(f.ctx)
	f.capture.Observe(AgentEvent{Kind: AgentChanged, Agent: f.status(a, domain.StatusIdle)})
	f.capture.tick(f.ctx)
	f.clock.Advance(f.capture.Interval)
	f.capture.tick(f.ctx)
	if _, _, err := f.capture.Since(f.ctx, a.Key); err != nil {
		t.Fatalf("Since = %v", err)
	}

	wantCalls := []testkit.ReadCall{
		{Target: "p1", Source: domain.ScreenRecent, Lines: captureLines},
		{Target: "p1", Source: domain.ScreenVisible, Lines: captureLines},
		{Target: "p1", Source: domain.ScreenVisible, Lines: captureLines},
		{Target: "p1", Source: domain.ScreenVisible, Lines: captureLines},
		{Target: "p1", Source: domain.ScreenVisible, Lines: captureLines},
	}
	if got := h.Reads(); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("Reads = %+v\nwant %+v", got, wantCalls)
	}
	if got, want := f.capture.hist[a.Key].Lines(), screenLines(text(1, 21)); !reflect.DeepEqual(got, want) {
		t.Fatalf("history = %v\nwant %v", got, want)
	}
}

// A refusal met outside working, for example by /screen all on a blocked
// agent, identifies a full-screen agent just as well.
func TestCaptureBusyRefusalOutsideWorkingSticks(t *testing.T) {
	f := newCaptureFixture(t)
	a := f.agent("p1", domain.StatusBlocked)
	f.capture.Observe(AgentEvent{Kind: AgentAppeared, Agent: a})
	h := f.scriptReads(
		scriptedRead{source: domain.ScreenRecent, err: domain.ErrAgentBusy},
		scriptedRead{source: domain.ScreenVisible, screen: domain.Screen{Text: text(1, 20)}},
		scriptedRead{source: domain.ScreenVisible, screen: domain.Screen{Text: text(1, 20)}},
	)
	for range 2 {
		if _, _, err := f.capture.Since(f.ctx, a.Key); err != nil {
			t.Fatalf("Since = %v", err)
		}
	}
	wantCalls := []testkit.ReadCall{
		{Target: "p1", Source: domain.ScreenRecent, Lines: captureLines},
		{Target: "p1", Source: domain.ScreenVisible, Lines: captureLines},
		{Target: "p1", Source: domain.ScreenVisible, Lines: captureLines},
	}
	if got := h.Reads(); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("Reads = %+v\nwant %+v", got, wantCalls)
	}
}

// An agent Herdr never refuses (Codex with alternate_screen = "never") has
// real scrollback, so the grace reads and Since keep asking for recent.
func TestCaptureKeepsRecentForAgentNeverRefused(t *testing.T) {
	f := newCaptureFixture(t)
	a := domain.Agent{Key: domain.Key{PaneID: "p1", TerminalID: "t"}, Kind: "codex", Status: domain.StatusWorking}
	f.agents[a.Key] = a
	f.herdr.SetScreen("p1", text(1, 20))
	f.capture.Observe(AgentEvent{Kind: AgentAppeared, Agent: a})
	f.capture.tick(f.ctx)
	f.capture.Observe(AgentEvent{Kind: AgentChanged, Agent: f.status(a, domain.StatusIdle)})
	f.capture.tick(f.ctx)
	if _, _, err := f.capture.Since(f.ctx, a.Key); err != nil {
		t.Fatalf("Since = %v", err)
	}
	recent := testkit.ReadCall{Target: "p1", Source: domain.ScreenRecent, Lines: captureLines}
	if got, want := f.herdr.Reads(), []testkit.ReadCall{recent, recent, recent}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Reads = %+v\nwant %+v", got, want)
	}
}

func TestCaptureSinceSkipsBusyRecentWhileWorking(t *testing.T) {
	f := newCaptureFixture(t)
	a := f.agent("p1", domain.StatusWorking)
	f.capture.Observe(AgentEvent{Kind: AgentAppeared, Agent: a})
	h := f.scriptReads(
		scriptedRead{source: domain.ScreenRecent, err: domain.ErrAgentBusy},
		scriptedRead{source: domain.ScreenVisible, screen: domain.Screen{Text: text(1, 20)}},
		scriptedRead{source: domain.ScreenVisible, screen: domain.Screen{Text: text(1, 20)}},
	)
	f.capture.tick(f.ctx)
	if _, _, err := f.capture.Since(f.ctx, a.Key); err != nil {
		t.Fatalf("Since = %v", err)
	}
	wantCalls := []testkit.ReadCall{
		{Target: "p1", Source: domain.ScreenRecent, Lines: captureLines},
		{Target: "p1", Source: domain.ScreenVisible, Lines: captureLines},
		{Target: "p1", Source: domain.ScreenVisible, Lines: captureLines},
	}
	if got := h.Reads(); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("Reads = %+v\nwant %+v", got, wantCalls)
	}
}

func TestCaptureBusyFollowsReassociationAndGone(t *testing.T) {
	f := newCaptureFixture(t)
	old := f.agent("p1", domain.StatusWorking)
	f.capture.Observe(AgentEvent{Kind: AgentAppeared, Agent: old})
	f.scriptReads(
		scriptedRead{source: domain.ScreenRecent, err: domain.ErrAgentBusy},
		scriptedRead{source: domain.ScreenVisible, screen: domain.Screen{Text: text(1, 20)}},
	)
	f.capture.tick(f.ctx)

	delete(f.agents, old.Key)
	moved := domain.Agent{Key: domain.Key{PaneID: "p1", TerminalID: "t2"}, Kind: "claude", Status: domain.StatusWorking}
	f.agents[moved.Key] = moved
	f.capture.Observe(AgentEvent{Kind: AgentChanged, Agent: moved, ReassociatedFrom: &old.Key})
	if !f.capture.busy[moved.Key] || f.capture.busy[old.Key] {
		t.Fatalf("busy after reassociation = %v, want only the new key", f.capture.busy)
	}

	f.capture.Observe(AgentEvent{Kind: AgentGone, Agent: moved})
	if len(f.capture.busy) != 0 {
		t.Fatalf("busy after gone = %v, want empty", f.capture.busy)
	}
}

func TestCaptureSinceUsesVisibleFallback(t *testing.T) {
	f := newCaptureFixture(t)
	a := f.agent("p1", domain.StatusWorking)
	h := f.scriptReads(
		scriptedRead{source: domain.ScreenRecent, err: domain.ErrAgentBusy},
		scriptedRead{source: domain.ScreenVisible, screen: domain.Screen{Text: text(1, 20)}},
	)

	lines, marked, err := f.capture.Since(f.ctx, a.Key)
	if err != nil || marked {
		t.Fatalf("Since = (%v, %v, %v), want visible lines, unmarked, nil", lines, marked, err)
	}
	if want := screenLines(text(1, 20)); !reflect.DeepEqual(lines, want) {
		t.Fatalf("Since lines = %v\nwant %v", lines, want)
	}
	wantCalls := []testkit.ReadCall{
		{Target: "p1", Source: domain.ScreenRecent, Lines: captureLines},
		{Target: "p1", Source: domain.ScreenVisible, Lines: captureLines},
	}
	if got := h.Reads(); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("Reads = %+v\nwant %+v", got, wantCalls)
	}
	if got := f.logs.count(slog.LevelDebug, "capture used visible screen fallback"); got != 1 {
		t.Fatalf("visible fallback DEBUG records = %d, want 1", got)
	}
}

func TestCaptureUpdatesSourceWhenRecentScreenIsUnchanged(t *testing.T) {
	f := newCaptureFixture(t)
	a := f.agent("p1", domain.StatusWorking)
	f.capture.merge(a.Key, domain.Screen{Text: text(1, 20)}, domain.ScreenVisible)
	f.capture.merge(a.Key, domain.Screen{Text: text(1, 20)}, domain.ScreenRecent)

	if got := f.capture.source[a.Key]; got != domain.ScreenRecent {
		t.Fatalf("last source = %q, want %q after unchanged recent read", got, domain.ScreenRecent)
	}
	if got, want := f.capture.hist[a.Key].Lines(), screenLines(text(1, 20)); !reflect.DeepEqual(got, want) {
		t.Fatalf("history after unchanged recent read = %v\nwant %v", got, want)
	}
}

func TestCaptureVisibleFallbackFailureWarnsOnceAndReturnsBothErrors(t *testing.T) {
	f := newCaptureFixture(t)
	a := f.agent("p1", domain.StatusWorking)
	recentErr := fmt.Errorf("%w: alternate-screen capture unavailable", domain.ErrAgentBusy)
	visibleErr := fmt.Errorf("%w: pane closed during read", domain.ErrDisconnected)
	h := f.scriptReads(
		scriptedRead{source: domain.ScreenRecent, err: recentErr},
		scriptedRead{source: domain.ScreenVisible, err: visibleErr},
	)
	f.capture.tick(f.ctx)
	wantCalls := []testkit.ReadCall{
		{Target: "p1", Source: domain.ScreenRecent, Lines: captureLines},
		{Target: "p1", Source: domain.ScreenVisible, Lines: captureLines},
	}
	if got := h.Reads(); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("Reads = %+v\nwant %+v", got, wantCalls)
	}
	if got := f.logs.count(slog.LevelWarn, "capture read failed"); got != 1 {
		t.Fatalf("capture read WARN records = %d, want 1", got)
	}
	if _, ok := f.capture.hist[a.Key]; ok {
		t.Fatal("failed reads must not create or mutate history")
	}

	// A fresh capture: the refusal above made p1 read visible for good.
	f = newCaptureFixture(t)
	a = f.agent("p1", domain.StatusWorking)
	h = f.scriptReads(
		scriptedRead{source: domain.ScreenRecent, err: recentErr},
		scriptedRead{source: domain.ScreenVisible, err: visibleErr},
	)
	_, _, err := f.capture.Since(f.ctx, a.Key)
	if !errors.Is(err, domain.ErrAgentBusy) || !errors.Is(err, domain.ErrDisconnected) {
		t.Fatalf("Since error = %v, want both original and fallback causes", err)
	}
	for _, detail := range []string{"recent screen read failed", "alternate-screen capture unavailable", "visible fallback failed", "pane closed during read"} {
		if !strings.Contains(err.Error(), detail) {
			t.Errorf("Since error %q does not contain %q", err, detail)
		}
	}
	if got := h.Reads(); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("Since Reads = %+v\nwant %+v", got, wantCalls)
	}
}

func TestCaptureDoesNotRetryUnrelatedOrCancelledReads(t *testing.T) {
	t.Run("unrelated error", func(t *testing.T) {
		f := newCaptureFixture(t)
		a := f.agent("p1", domain.StatusWorking)
		h := f.scriptReads(scriptedRead{source: domain.ScreenRecent, err: domain.ErrDisconnected})
		f.capture.tick(f.ctx)
		want := []testkit.ReadCall{{Target: "p1", Source: domain.ScreenRecent, Lines: captureLines}}
		if got := h.Reads(); !reflect.DeepEqual(got, want) {
			t.Fatalf("Reads = %+v\nwant %+v", got, want)
		}
		if _, ok := f.capture.hist[a.Key]; ok {
			t.Fatal("unrelated read error must not create history")
		}
	})

	t.Run("cancelled after busy response", func(t *testing.T) {
		f := newCaptureFixture(t)
		a := f.agent("p1", domain.StatusWorking)
		ctx, cancel := context.WithCancel(f.ctx)
		h := f.scriptReads(scriptedRead{source: domain.ScreenRecent, err: domain.ErrAgentBusy, after: cancel})
		f.capture.tick(ctx)
		want := []testkit.ReadCall{{Target: "p1", Source: domain.ScreenRecent, Lines: captureLines}}
		if got := h.Reads(); !reflect.DeepEqual(got, want) {
			t.Fatalf("Reads = %+v\nwant %+v", got, want)
		}
		if _, ok := f.capture.hist[a.Key]; ok {
			t.Fatal("cancelled read must not create history")
		}
		if got := f.logs.count(slog.LevelWarn, "capture read failed"); got != 1 {
			t.Fatalf("capture read WARN records = %d, want 1", got)
		}
	})
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
	if len(f.capture.hist) != 0 || len(f.capture.status) != 0 || len(f.capture.last) != 0 || len(f.capture.source) != 0 {
		t.Fatalf("state after gone: hist %d status %d last %d source %d", len(f.capture.hist), len(f.capture.status), len(f.capture.last), len(f.capture.source))
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

func TestPrivateHistoryUsesActivationBoundary(t *testing.T) {
	clock := testkit.NewFakeClock(time.Unix(100, 0))
	c := NewCapture(testkit.NewFakeHerdr(nil), func() []domain.Agent { return nil }, clock, nil)
	key := domain.Key{PaneID: "p", TerminalID: "t", SessionDigest: "verified"}
	c.recordPrivateFrame(key, "old owner history")
	activation := clock.Now()
	clock.Advance(time.Second)
	c.recordPrivateFrame(key, "permitted new output")
	if got := c.PrivateSince(key, activation); strings.Contains(got, "old owner") || !strings.Contains(got, "permitted") {
		t.Fatal("history crossed activation")
	}
	regrant := clock.Now()
	if got := c.PrivateSince(key, regrant); got != "" {
		t.Fatal("regrant revived previous grant history")
	}
	other := key
	other.SessionDigest = "replacement"
	if got := c.PrivateSince(other, time.Time{}); got != "" {
		t.Fatal("replacement session inherited private history")
	}
}
