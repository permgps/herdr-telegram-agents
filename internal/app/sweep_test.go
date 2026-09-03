package app_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/app"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

const day = 24 * time.Hour

var fullRights = domain.Rights{IsForum: true, IsAdmin: true, CanManageTopics: true, CanDeleteMessages: true}

// exited links a topic for an agent that exited and closed at the given
// time; the fake Telegram knows the topic so a delete succeeds.
func (f *recFixture) exited(t *testing.T, pane string, closedAt time.Time) domain.Key {
	t.Helper()
	a := agent(pane, "t", pane, domain.StatusWorking)
	topic, err := f.tg.CreateTopic(f.ctx, a.Label(), a.Status)
	if err != nil {
		t.Fatal(err)
	}
	m := f.rec.Mapping()
	m.Link(a.Key, topic, a, closedAt)
	m.MarkExited(a.Key, closedAt)
	m.MarkClosed(a.Key, closedAt)
	return a.Key
}

func TestSweepDeletesOldClosedTopicsOnly(t *testing.T) {
	f := newRec(t)
	f.clock.Advance(40 * day)
	old := f.exited(t, "old", t0)
	young := f.exited(t, "young", t0.Add(39*day))
	live := agent("live", "t", "live", domain.StatusWorking)
	f.handle(t, app.AgentAppeared, live)
	f.tg.Reset()

	n, err := f.rec.Sweep(f.ctx, 30*day, fullRights)
	if err != nil || n != 1 {
		t.Fatalf("Sweep = %d, %v", n, err)
	}
	assertCalls(t, f.tg, "delete:101")
	m := f.rec.Mapping()
	if _, ok := m.TopicFor(old); ok {
		t.Fatal("deleted topic still mapped")
	}
	if _, ok := m.TopicFor(young); !ok {
		t.Fatal("young entry dropped")
	}
	if _, ok := m.TopicFor(live.Key); !ok {
		t.Fatal("live entry dropped")
	}
	if f.store.SaveCount() != 2 { // create of the live agent, then the forget
		t.Fatalf("saves = %d", f.store.SaveCount())
	}
	// The next pass has nothing to do.
	f.tg.Reset()
	if n, _ := f.rec.Sweep(f.ctx, 30*day, fullRights); n != 0 || len(f.tg.Calls()) != 0 {
		t.Fatalf("second sweep = %d, calls %v", n, f.tg.Calls())
	}
}

func TestSweepForgetsGoneTopicAndKeepsFailedOnes(t *testing.T) {
	f := newRec(t)
	f.clock.Advance(40 * day)
	gone := f.exited(t, "gone", t0)
	failing := f.exited(t, "fail", t0.Add(time.Hour))
	f.tg.Reset()
	f.tg.FailNext("delete", errors.New("telegram api 400: something"))
	// The first delete (oldest: gone) fails with the generic error and the
	// entry stays; the second (failing) succeeds.
	n, err := f.rec.Sweep(f.ctx, 30*day, fullRights)
	if err != nil || n != 1 {
		t.Fatalf("Sweep = %d, %v", n, err)
	}
	assertCalls(t, f.tg, "delete:101", "delete:102")
	m := f.rec.Mapping()
	if _, ok := m.TopicFor(gone); !ok {
		t.Fatal("entry with a failed delete was dropped")
	}
	if _, ok := m.TopicFor(failing); ok {
		t.Fatal("deleted entry kept")
	}
	// A topic already gone in Telegram is forgotten too.
	f.tg.Reset()
	f.tg.FailNext("delete", domain.ErrTopicGone)
	if n, err := f.rec.Sweep(f.ctx, 30*day, fullRights); err != nil || n != 1 {
		t.Fatalf("Sweep after gone = %d, %v", n, err)
	}
	if _, ok := m.TopicFor(gone); ok {
		t.Fatal("gone entry kept")
	}
}

func TestSweepStopsOnForbidden(t *testing.T) {
	f := newRec(t)
	f.clock.Advance(40 * day)
	f.exited(t, "a", t0)
	f.exited(t, "b", t0.Add(time.Hour))
	f.tg.Reset()
	f.tg.FailNext("delete", domain.ErrForbidden)
	n, err := f.rec.Sweep(f.ctx, 30*day, fullRights)
	if err != nil || n != 0 {
		t.Fatalf("Sweep = %d, %v", n, err)
	}
	assertCalls(t, f.tg, "delete:101")
	if len(f.rec.Mapping().Topics) != 2 {
		t.Fatal("entries dropped after a forbidden delete")
	}
}

func TestSweepBatchAndSkips(t *testing.T) {
	f := newRec(t)
	f.clock.Advance(40 * day)
	for i := 0; i < 55; i++ {
		f.exited(t, fmt.Sprintf("p%02d", i), t0.Add(time.Duration(i)*time.Minute))
	}
	f.tg.Reset()

	// Off, paused and no delete right: nothing happens.
	if n, _ := f.rec.Sweep(f.ctx, 0, fullRights); n != 0 || len(f.tg.Calls()) != 0 {
		t.Fatalf("off sweep = %d, calls %v", n, f.tg.Calls())
	}
	noDelete := fullRights
	noDelete.CanDeleteMessages = false
	if n, _ := f.rec.Sweep(f.ctx, 30*day, noDelete); n != 0 || len(f.tg.Calls()) != 0 {
		t.Fatalf("no-rights sweep = %d, calls %v", n, f.tg.Calls())
	}
	f.rec.SetReadOnly(true)
	if n, _ := f.rec.Sweep(f.ctx, 30*day, fullRights); n != 0 || len(f.tg.Calls()) != 0 {
		t.Fatalf("read-only sweep = %d, calls %v", n, f.tg.Calls())
	}
	f.rec.SetReadOnly(false)

	// A batch of 50 goes first, the rest on the next pass.
	n, err := f.rec.Sweep(f.ctx, 30*day, fullRights)
	if err != nil || n != 50 || len(f.tg.Calls()) != 50 {
		t.Fatalf("first pass = %d, %v, calls %d", n, err, len(f.tg.Calls()))
	}
	f.tg.Reset()
	if n, _ := f.rec.Sweep(f.ctx, 30*day, fullRights); n != 5 {
		t.Fatalf("second pass = %d", n)
	}
	if len(f.rec.Mapping().Topics) != 0 {
		t.Fatal("entries left after both passes")
	}
}

func TestSweepPausedBySyncOff(t *testing.T) {
	f := newRec(t)
	opts, _ := domain.DefaultOptions().With(domain.OptionSyncEnabled, "false")
	f.rec = app.NewReconciler(f.tg, f.herdr, f.store, domain.NewMapping(-1), app.NewOptions(opts, testkit.NewMemOptionsStore(), nil, nil), f.clock, nil)
	f.clock.Advance(40 * day)
	f.exited(t, "a", t0)
	f.tg.Reset()
	if n, _ := f.rec.Sweep(context.Background(), 30*day, fullRights); n != 0 || len(f.tg.Calls()) != 0 {
		t.Fatalf("paused sweep = %d, calls %v", n, f.tg.Calls())
	}
}
