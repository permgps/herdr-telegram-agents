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

type presenceFixture struct {
	idle  *testkit.FakeIdle
	clock *testkit.FakeClock
	opts  *app.Options
	p     *app.Presence
}

func newPresence(t *testing.T, idle *testkit.FakeIdle) *presenceFixture {
	t.Helper()
	store := testkit.NewMemOptionsStore()
	opts := app.NewOptions(store.Stored(), store, nil, nil)
	clock := testkit.NewFakeClock(t0)
	return &presenceFixture{idle: idle, clock: clock, opts: opts, p: app.NewPresence(idle, opts, clock, nil)}
}

// change returns the pending change, or ok false when none is queued.
func (f *presenceFixture) change() (quiet, ok bool) {
	select {
	case v := <-f.p.Changes():
		return v, true
	default:
		return false, false
	}
}

func TestPresenceAtDeskAndAway(t *testing.T) {
	f := newPresence(t, testkit.NewFakeIdle(5*time.Second))
	ctx := context.Background()
	if f.p.Quiet() {
		t.Fatal("quiet before the first sample")
	}
	f.p.Poll(ctx)
	if !f.p.Quiet() {
		t.Fatal("not quiet while at the desk")
	}
	if v, ok := f.change(); !ok || !v {
		t.Fatalf("change after first poll = %v, %v", v, ok)
	}
	st := f.p.State()
	if !st.Enabled || !st.Supported || !st.AtDesk || st.ManualAway || !st.Quiet || st.Word() != "on" {
		t.Errorf("state = %+v", st)
	}
	f.p.Poll(ctx) // same verdict, no change queued
	if _, ok := f.change(); ok {
		t.Error("change queued without a flip")
	}

	f.idle.Set(3*time.Minute + time.Second)
	f.p.Poll(ctx)
	if f.p.Quiet() {
		t.Fatal("still quiet past the threshold")
	}
	if v, ok := f.change(); !ok || v {
		t.Fatalf("change after leaving = %v, %v", v, ok)
	}
	if got := f.p.State().Word(); got != "away" {
		t.Errorf("Word = %q", got)
	}
	// Exactly the threshold counts as away.
	f.idle.Set(3 * time.Minute)
	f.p.Poll(ctx)
	if f.p.Quiet() {
		t.Error("idle == threshold should be away")
	}
}

func TestPresenceFlapKeepsLatestChange(t *testing.T) {
	f := newPresence(t, testkit.NewFakeIdle(time.Second))
	ctx := context.Background()
	f.p.Poll(ctx) // quiet on, change queued
	f.idle.Set(time.Hour)
	f.p.Poll(ctx) // quiet off, replaces the queued value
	v, ok := f.change()
	if !ok || v {
		t.Fatalf("change = %v, %v; want latest (false)", v, ok)
	}
	if _, ok := f.change(); ok {
		t.Error("a second change was queued")
	}
}

func TestPresenceManualAway(t *testing.T) {
	f := newPresence(t, testkit.NewFakeIdle(time.Second))
	ctx := context.Background()
	f.p.Poll(ctx)
	f.change()

	st := f.p.Away(0, 7)
	if !st.ManualAway || !st.Until.IsZero() || st.Quiet || st.Word() != "away-manual" {
		t.Fatalf("state after /away = %+v", st)
	}
	if v, ok := f.change(); !ok || v {
		t.Fatalf("change after /away = %v, %v", v, ok)
	}
	f.p.Poll(ctx) // still at the desk, still overridden
	if f.p.Quiet() {
		t.Error("quiet despite /away")
	}
	st = f.p.Here(7)
	if st.ManualAway || !st.Quiet {
		t.Fatalf("state after /here = %+v", st)
	}
	if v, ok := f.change(); !ok || !v {
		t.Fatalf("change after /here = %v, %v", v, ok)
	}

	st = f.p.Away(2*time.Hour, 7)
	if !st.Until.Equal(t0.Add(2 * time.Hour)) {
		t.Fatalf("until = %v", st.Until)
	}
	f.change()
	f.clock.Advance(time.Hour)
	f.p.Poll(ctx)
	if f.p.Quiet() {
		t.Error("timed away expired early")
	}
	f.clock.Advance(time.Hour)
	f.p.Poll(ctx)
	if !f.p.Quiet() || f.p.State().ManualAway {
		t.Errorf("timed away did not expire: %+v", f.p.State())
	}
	if v, ok := f.change(); !ok || !v {
		t.Fatalf("change after expiry = %v, %v", v, ok)
	}
}

func TestPresenceOptionChanges(t *testing.T) {
	f := newPresence(t, testkit.NewFakeIdle(time.Second))
	ctx := context.Background()
	f.p.Poll(ctx)
	f.change()

	if err := f.opts.Set(ctx, domain.OptionQuietEnabled, "false", 7); err != nil {
		t.Fatal(err)
	}
	f.p.Recompute()
	if f.p.Quiet() {
		t.Fatal("quiet with the option off")
	}
	if v, ok := f.change(); !ok || v {
		t.Fatalf("change after disabling = %v, %v", v, ok)
	}
	if got := f.p.State().Word(); got != "off" {
		t.Errorf("Word = %q", got)
	}
	if err := f.opts.Set(ctx, domain.OptionQuietEnabled, "true", 7); err != nil {
		t.Fatal(err)
	}
	f.p.Recompute()
	if !f.p.Quiet() {
		t.Fatal("not quiet after re-enabling")
	}

	// A shorter threshold takes effect on the next sample.
	f.idle.Set(90 * time.Second)
	if err := f.opts.Set(ctx, domain.OptionQuietIdleMinutes, "1", 7); err != nil {
		t.Fatal(err)
	}
	f.change()
	f.p.Poll(ctx)
	if f.p.Quiet() {
		t.Error("90 s idle should be away with a 1 min threshold")
	}
}

func TestPresenceUnsupportedAndFailing(t *testing.T) {
	idle := testkit.NewFakeIdle(0)
	idle.Unsupported()
	f := newPresence(t, idle)
	ctx := context.Background()
	f.p.Poll(ctx)
	f.p.Poll(ctx)
	if f.p.Quiet() || f.p.State().Supported || idle.Calls() != 1 {
		t.Fatalf("unsupported source: quiet=%v state=%+v calls=%d", f.p.Quiet(), f.p.State(), idle.Calls())
	}
	if got := f.p.State().Word(); got != "off" {
		t.Errorf("Word = %q", got)
	}
	st := f.p.Away(0, 7)
	if st.Quiet || st.Word() != "off" {
		t.Errorf("/away on unsupported platform = %+v", st)
	}
	if _, ok := f.change(); ok {
		t.Error("unsupported source queued a change")
	}

	nilSource := app.NewPresence(nil, f.opts, f.clock, nil)
	nilSource.Poll(ctx)
	if nilSource.Quiet() || nilSource.State().Supported {
		t.Error("nil source should be unsupported")
	}

	// A transient failure keeps the previous verdict.
	g := newPresence(t, testkit.NewFakeIdle(time.Second))
	g.p.Poll(ctx)
	g.change()
	g.idle.Fail(errors.New("ioreg: exit 1"))
	g.p.Poll(ctx)
	if !g.p.Quiet() {
		t.Error("failure dropped the at-desk verdict")
	}
	g.idle.Set(time.Hour)
	g.p.Poll(ctx)
	if g.p.Quiet() {
		t.Error("recovered sample ignored")
	}
}
