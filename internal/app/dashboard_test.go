package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

// dashFixture drives a Dashboard against the fakes with a hand-fed agent
// set and a reconciler that owns the mapping.
type dashFixture struct {
	tg     *testkit.FakeTelegram
	clock  *testkit.FakeClock
	store  *testkit.MemMappingStore
	rec    *Reconciler
	opts   *Options
	agents map[domain.Key]domain.Agent
	logBuf *bytes.Buffer
	dash   *Dashboard
	ctx    context.Context
}

func newDashFixture(t *testing.T) *dashFixture {
	t.Helper()
	f := &dashFixture{
		tg:     testkit.NewFakeTelegram(nil),
		clock:  testkit.NewFakeClock(tb0),
		store:  testkit.NewMemMappingStore(),
		agents: map[domain.Key]domain.Agent{},
		logBuf: &bytes.Buffer{},
		ctx:    context.Background(),
	}
	log := slog.New(slog.NewJSONHandler(f.logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	f.opts = NewOptions(domain.DefaultOptions(), testkit.NewMemOptionsStore(), nil, nil)
	f.rec = NewReconciler(f.tg, testkit.NewFakeHerdr(nil), f.store, domain.NewMapping(-1001234567890), f.opts, f.clock, nil)
	live := func() []domain.Agent {
		var out []domain.Agent
		for _, a := range f.agents {
			out = append(out, a)
		}
		return out
	}
	f.dash = NewDashboard(f.tg, f.rec, f.opts, f.rec.topics(), live, nil, -1001234567890, f.clock, log)
	return f
}

// add creates the agent's topic through the reconciler and records the
// event on the dashboard.
func (f *dashFixture) add(t *testing.T, pane, name string, st domain.Status) domain.Agent {
	t.Helper()
	a := domain.Agent{Key: domain.Key{PaneID: pane, TerminalID: "t"}, WorkspaceLabel: "ws", Name: name, Status: st}
	f.agents[a.Key] = a
	ev := AgentEvent{Kind: AgentAppeared, Agent: a}
	if err := f.rec.Handle(f.ctx, ev); err != nil {
		t.Fatal(err)
	}
	f.dash.Observe(ev)
	return a
}

func (f *dashFixture) change(a domain.Agent, st domain.Status) domain.Agent {
	a.Status = st
	f.agents[a.Key] = a
	f.dash.Observe(AgentEvent{Kind: AgentChanged, Agent: a})
	return a
}

// fire advances past the settle and runs Fire for every due timer, which
// must be exactly want.
func (f *dashFixture) fire(t *testing.T, want int) {
	t.Helper()
	f.clock.Advance(dashboardSettle)
	for i := 0; i < want; i++ {
		select {
		case <-f.dash.Due():
			if err := f.dash.Fire(f.ctx); err != nil {
				t.Fatalf("Fire = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("dashboard timer %d did not fire", i+1)
		}
	}
	select {
	case <-f.dash.Due():
		t.Fatal("unexpected extra dashboard timer")
	case <-time.After(30 * time.Millisecond):
	}
}

// board returns the calls that concern the dashboard (topic calls of the
// reconciler are filtered out).
func (f *dashFixture) board() []string {
	var out []string
	for _, c := range f.tg.Calls() {
		if strings.HasPrefix(c, "send:0:") || strings.HasPrefix(c, "pin:") || strings.HasPrefix(c, "unpin:") ||
			strings.HasPrefix(c, "deletemsg:") || strings.HasPrefix(c, "edittext:") {
			out = append(out, c)
		}
	}
	return out
}

func TestDashboardCreatesPinsAndEdits(t *testing.T) {
	f := newDashFixture(t)
	a := f.add(t, "p1", "alpha", domain.StatusWorking)
	f.fire(t, 1)
	calls := f.board()
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "send:0:1 agent\n⚡ <a href=\"https://t.me/c/1234567890/101\">ws · alpha</a>\n\n<i>updated 12:00</i>") || strings.Contains(calls[0], ":notify") || calls[1] != "pin:1000" {
		t.Fatalf("first fire calls = %q", calls)
	}
	if f.rec.DashboardID() != 1000 || f.store.Saved().Dashboard != 1000 || !f.tg.Pinned(1000) {
		t.Fatalf("id=%d saved=%+v pinned=%v", f.rec.DashboardID(), f.store.Saved(), f.tg.Pinned(1000))
	}
	if !strings.Contains(f.logBuf.String(), `"msg":"dashboard created"`) {
		t.Errorf("log lacks 'dashboard created':\n%s", f.logBuf.String())
	}
	// The same agents again: nothing to edit.
	f.tg.Reset()
	f.dash.Schedule("test")
	f.fire(t, 1)
	if got := f.board(); len(got) != 0 {
		t.Fatalf("unchanged board edited: %q", got)
	}
	if !strings.Contains(f.logBuf.String(), `"msg":"dashboard unchanged"`) {
		t.Errorf("log lacks 'dashboard unchanged'")
	}
	// A status change edits after the settle, with the duration absent
	// under a minute.
	f.tg.Reset()
	f.clock.Advance(10 * time.Second)
	f.change(a, domain.StatusBlocked)
	f.fire(t, 1)
	calls = f.board()
	if len(calls) != 1 || calls[0] != "edittext:1000:1 agent\n❓ <a href=\"https://t.me/c/1234567890/101\">ws · alpha</a>\n\n<i>updated 12:00</i>:buttons=0" {
		t.Fatalf("edit after change = %q", calls)
	}
	// Two events inside the settle coalesce into one edit.
	f.tg.Reset()
	f.change(a, domain.StatusWorking)
	f.clock.Advance(time.Second)
	f.change(a, domain.StatusIdle)
	f.fire(t, 1)
	if calls = f.board(); len(calls) != 1 || !strings.Contains(calls[0], "✅ <a") {
		t.Fatalf("coalesced edit = %q", calls)
	}
}

func TestDashboardTickMovesDurations(t *testing.T) {
	f := newDashFixture(t)
	a := f.add(t, "p1", "alpha", domain.StatusWorking)
	f.change(a, domain.StatusBlocked)
	f.fire(t, 1)
	f.tg.Reset()
	// 40 s later the label is still under a minute: no edit.
	f.clock.Advance(40 * time.Second)
	if err := f.dash.Tick(f.ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.board(); len(got) != 0 {
		t.Fatalf("tick under a minute edited: %q", got)
	}
	// Past the minute the duration appears and the footer moves.
	f.clock.Advance(30 * time.Second)
	if err := f.dash.Tick(f.ctx); err != nil {
		t.Fatal(err)
	}
	got := f.board()
	if len(got) != 1 || !strings.HasSuffix(got[0], "ws · alpha</a> · 1 min\n\n<i>updated 12:01</i>:buttons=0") {
		t.Fatalf("tick past a minute = %q", got)
	}
	// /status sees the same start time.
	if since := f.dash.Since(); !since[a.Key].Equal(tb0) {
		t.Errorf("Since = %v", since)
	}
}

func TestDashboardRecreatesGoneMessage(t *testing.T) {
	f := newDashFixture(t)
	a := f.add(t, "p1", "alpha", domain.StatusWorking)
	f.fire(t, 1)
	f.tg.Reset()
	if err := f.tg.DeleteMessage(f.ctx, 1000); err != nil {
		t.Fatal(err)
	}
	f.tg.Reset()
	f.change(a, domain.StatusDone)
	f.fire(t, 1)
	calls := f.board()
	if len(calls) != 3 || !strings.HasPrefix(calls[0], "edittext:1000:") || !strings.HasPrefix(calls[1], "send:0:") || calls[2] != "pin:1001" {
		t.Fatalf("recreate calls = %q", calls)
	}
	if f.rec.DashboardID() != 1001 || f.store.Saved().Dashboard != 1001 {
		t.Fatalf("new id not stored: %d", f.rec.DashboardID())
	}
	if !strings.Contains(f.logBuf.String(), `"msg":"dashboard recreated: message gone"`) {
		t.Error("log lacks the recreate line")
	}
}

func TestDashboardWorksUnpinnedWithoutTheRight(t *testing.T) {
	f := newDashFixture(t)
	a := f.add(t, "p1", "alpha", domain.StatusWorking)
	f.tg.FailNext("pin", domain.ErrForbidden)
	f.fire(t, 1)
	if f.rec.DashboardID() != 1000 || f.tg.Pinned(1000) {
		t.Fatalf("id=%d pinned=%v", f.rec.DashboardID(), f.tg.Pinned(1000))
	}
	if n := strings.Count(f.logBuf.String(), `dashboard not pinned: grant the bot the \"Pin messages\" right`); n != 1 {
		t.Fatalf("pin warning logged %d times:\n%s", n, f.logBuf.String())
	}
	// The message keeps being edited; a second creation warns no more.
	f.tg.Reset()
	f.change(a, domain.StatusBlocked)
	f.fire(t, 1)
	if got := f.board(); len(got) != 1 || !strings.HasPrefix(got[0], "edittext:1000:") {
		t.Fatalf("edit unpinned = %q", got)
	}
	if err := f.tg.DeleteMessage(f.ctx, 1000); err != nil {
		t.Fatal(err)
	}
	f.tg.FailNext("pin", domain.ErrForbidden)
	f.change(a, domain.StatusDone)
	f.fire(t, 1)
	if n := strings.Count(f.logBuf.String(), "dashboard not pinned"); n != 1 {
		t.Fatalf("pin warning repeated: %d", n)
	}
}

func TestDashboardOptionOffRemovesAndOnRecreates(t *testing.T) {
	f := newDashFixture(t)
	f.add(t, "p1", "alpha", domain.StatusWorking)
	f.fire(t, 1)
	f.tg.Reset()
	if err := f.opts.Set(f.ctx, domain.OptionSyncDashboard, "false", 1); err != nil {
		t.Fatal(err)
	}
	if err := f.dash.Enable(f.ctx, false); err != nil {
		t.Fatal(err)
	}
	if got := f.board(); len(got) != 2 || got[0] != "unpin:1000" || got[1] != "deletemsg:1000" {
		t.Fatalf("off calls = %q", got)
	}
	if f.rec.DashboardID() != 0 || f.store.Saved().Dashboard != 0 {
		t.Fatalf("id not cleared: %d", f.rec.DashboardID())
	}
	if !strings.Contains(f.logBuf.String(), `"msg":"dashboard removed"`) {
		t.Error("log lacks 'dashboard removed'")
	}
	// Off with no message left: Schedule arms nothing.
	f.tg.Reset()
	f.dash.Schedule("test")
	f.fire(t, 0)
	if got := f.board(); len(got) != 0 {
		t.Fatalf("off board touched: %q", got)
	}
	// Back on: a new message is created and pinned.
	if err := f.opts.Set(f.ctx, domain.OptionSyncDashboard, "true", 1); err != nil {
		t.Fatal(err)
	}
	if err := f.dash.Enable(f.ctx, true); err != nil {
		t.Fatal(err)
	}
	if got := f.board(); len(got) != 2 || !strings.HasPrefix(got[0], "send:0:") || got[1] != "pin:1001" {
		t.Fatalf("on calls = %q", got)
	}
}

func TestDashboardStopWritesTheStoppedFooter(t *testing.T) {
	f := newDashFixture(t)
	f.add(t, "p1", "alpha", domain.StatusWorking)
	f.fire(t, 1)
	f.tg.Reset()
	f.clock.Advance(7 * time.Minute)
	f.dash.Stop(f.ctx)
	got := f.board()
	if len(got) != 1 || !strings.HasSuffix(got[0], "\n\n<i>⏹ stopped 12:07</i>:buttons=0") {
		t.Fatalf("stop edit = %q", got)
	}
	if !strings.Contains(f.logBuf.String(), `"msg":"dashboard stopped"`) {
		t.Error("log lacks 'dashboard stopped'")
	}
	// Without a message Stop sends nothing.
	f.tg.Reset()
	f.rec.SetDashboardID(f.ctx, 0)
	f.dash.Stop(f.ctx)
	if got := f.board(); len(got) != 0 {
		t.Fatalf("stop without message = %q", got)
	}
}

func TestDashboardRepinAtStart(t *testing.T) {
	f := newDashFixture(t)
	f.add(t, "p1", "alpha", domain.StatusWorking)
	f.fire(t, 1)
	f.tg.Reset()
	f.dash.Repin(f.ctx)
	if got := f.board(); len(got) != 1 || got[0] != "pin:1000" {
		t.Fatalf("repin calls = %q", got)
	}
	// A message Telegram lost is forgotten so the next fire recreates it.
	f.tg.Reset()
	if err := f.tg.DeleteMessage(f.ctx, 1000); err != nil {
		t.Fatal(err)
	}
	f.tg.Reset()
	f.dash.Repin(f.ctx)
	if f.rec.DashboardID() != 0 {
		t.Fatalf("gone message kept: %d", f.rec.DashboardID())
	}
	f.tg.Reset()
	f.dash.Schedule("start")
	f.fire(t, 1)
	if got := f.board(); len(got) != 2 || !strings.HasPrefix(got[0], "send:0:") || got[1] != "pin:1001" {
		t.Fatalf("recreate after repin = %q", got)
	}
}

func TestDashboardObserveTracksSince(t *testing.T) {
	f := newDashFixture(t)
	a := f.add(t, "p1", "alpha", domain.StatusWorking)
	if _, ok := f.dash.Since()[a.Key]; ok {
		t.Fatal("appearance recorded a start time")
	}
	f.clock.Advance(time.Minute)
	// A label change keeps the status and the start time unchanged.
	f.change(a, domain.StatusWorking)
	if _, ok := f.dash.Since()[a.Key]; ok {
		t.Fatal("same status recorded a start time")
	}
	f.change(a, domain.StatusBlocked)
	if since := f.dash.Since(); !since[a.Key].Equal(tb0.Add(time.Minute)) {
		t.Fatalf("Since after change = %v", since)
	}
	f.clock.Advance(time.Minute)
	f.change(a, domain.StatusBlocked)
	if since := f.dash.Since(); !since[a.Key].Equal(tb0.Add(time.Minute)) {
		t.Fatalf("repeated status moved the start: %v", since)
	}
	f.dash.Observe(AgentEvent{Kind: AgentGone, Agent: a})
	if _, ok := f.dash.Since()[a.Key]; ok {
		t.Fatal("gone agent kept")
	}
}

func TestDashboardFatalErrorIsReturned(t *testing.T) {
	f := newDashFixture(t)
	f.add(t, "p1", "alpha", domain.StatusWorking)
	f.tg.FailNext("send", domain.ErrBotUnauthorized)
	f.clock.Advance(dashboardSettle)
	<-f.dash.Due()
	if err := f.dash.Fire(f.ctx); !errors.Is(err, domain.ErrBotUnauthorized) {
		t.Fatalf("Fire = %v, want ErrBotUnauthorized", err)
	}
	// A plain failure is retried on the next refresh.
	f.tg.FailNext("send", errors.New("boom"))
	if err := f.dash.Tick(f.ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.logBuf.String(), `"msg":"dashboard create failed"`) {
		t.Error("log lacks the create failure")
	}
	if err := f.dash.Tick(f.ctx); err != nil || f.rec.DashboardID() == 0 {
		t.Fatalf("retry: err=%v id=%d", err, f.rec.DashboardID())
	}
}
