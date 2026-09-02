package testkit_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

func TestFakeTelegramRecordsAndFails(t *testing.T) {
	tg := testkit.NewFakeTelegram(nil)
	ctx := context.Background()
	topic, err := tg.CreateTopic(ctx, "⚙️ a", domain.StatusWorking)
	if err != nil || topic.ThreadID != 101 {
		t.Fatalf("CreateTopic = %+v, %v", topic, err)
	}
	name := "💤 a"
	if err := tg.EditTopic(ctx, topic.ThreadID, domain.TopicPatch{Name: &name}); err != nil {
		t.Fatal(err)
	}
	if err := tg.EditTopic(ctx, topic.ThreadID, domain.TopicPatch{}); err != nil {
		t.Fatal(err)
	}
	tg.FailNext("close", domain.ErrTopicGone)
	if err := tg.CloseTopic(ctx, topic.ThreadID); !errors.Is(err, domain.ErrTopicGone) {
		t.Fatalf("CloseTopic = %v, want ErrTopicGone", err)
	}
	if err := tg.CloseTopic(ctx, topic.ThreadID); err != nil {
		t.Fatalf("second CloseTopic = %v", err)
	}
	want := []string{"create:⚙️ a:working", "edit:101:name=💤 a", "close:101", "close:101"}
	got := tg.Calls()
	if len(got) != len(want) {
		t.Fatalf("Calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Calls[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if got, _ := tg.Topic(101); got.Name != "💤 a" || !got.Closed {
		t.Fatalf("topic state = %+v", got)
	}
}

func TestFakeClockFiresInOrder(t *testing.T) {
	c := testkit.NewFakeClock(time.Unix(0, 0))
	late := c.After(3 * time.Second)
	early := c.After(time.Second)
	if n := c.Advance(500 * time.Millisecond); n != 0 {
		t.Fatalf("fired %d timers early", n)
	}
	if n := c.Advance(time.Second); n != 1 {
		t.Fatalf("fired %d, want 1", n)
	}
	select {
	case <-early:
	default:
		t.Fatal("early timer did not fire")
	}
	select {
	case <-late:
		t.Fatal("late timer fired too soon")
	default:
	}
	if n := c.Advance(2 * time.Second); n != 1 || c.Pending() != 0 {
		t.Fatalf("fired %d, pending %d", n, c.Pending())
	}
	if got := c.Now(); !got.Equal(time.Unix(3, 500_000_000)) {
		t.Fatalf("Now = %v", got)
	}
}

func TestFakeHerdrSnapshotsAndWatches(t *testing.T) {
	h := testkit.NewFakeHerdr(nil)
	h.SetAgents([]domain.Agent{{Key: domain.Key{PaneID: "p", TerminalID: "t"}}})
	agents, err := h.ListAgents(context.Background())
	if err != nil || len(agents) != 1 || h.ListCalls() != 1 {
		t.Fatalf("ListAgents = %v, %v (calls %d)", agents, err, h.ListCalls())
	}
	h.FailList(domain.ErrDisconnected)
	if _, err := h.ListAgents(context.Background()); !errors.Is(err, domain.ErrDisconnected) {
		t.Fatalf("ListAgents after FailList = %v", err)
	}
	_ = h.WatchPanes(context.Background(), []string{"p"})
	if w := h.WatchCalls(); len(w) != 1 || w[0][0] != "p" {
		t.Fatalf("WatchCalls = %v", w)
	}
	h.Push(domain.HerdrEvent{Kind: domain.PaneClosed, PaneID: "p"})
	ev := (<-h.Events()).(domain.HerdrEvent)
	if ev.Kind != domain.PaneClosed {
		t.Fatalf("event = %+v", ev)
	}
}

func TestMemMappingStoreCopies(t *testing.T) {
	s := testkit.NewMemMappingStore()
	m := domain.NewMapping(-1)
	a := domain.Agent{Key: domain.Key{PaneID: "p", TerminalID: "t"}, Name: "a"}
	m.Link(a.Key, domain.Topic{ThreadID: 1}, a, time.Unix(0, 0))
	if err := s.Save(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	m.Forget(a.Key)
	loaded, _ := s.Load(context.Background())
	if _, ok := loaded.TopicFor(a.Key); !ok || s.SaveCount() != 1 {
		t.Fatalf("saved copy shared state with the original (saves=%d)", s.SaveCount())
	}
}

func TestFakeProcessLifecycle(t *testing.T) {
	p := testkit.NewFakeProcess(nil)
	if _, err := p.Read(); !errors.Is(err, domain.ErrNotRunning) {
		t.Fatalf("Read on empty = %v", err)
	}
	pid, err := p.Spawn(context.Background(), []string{"daemon"})
	if err != nil || !p.Alive(pid) || p.Held() != pid {
		t.Fatalf("Spawn: pid=%d err=%v %s", pid, err, p)
	}
	if err := p.Acquire(42); !errors.Is(err, domain.ErrAlreadyRunning) {
		t.Fatalf("Acquire while held = %v", err)
	}
	if err := p.Stop(pid); err != nil || p.Alive(pid) || p.Held() != 0 {
		t.Fatalf("Stop: err=%v %s", err, p)
	}
	p.IgnoreStop(true)
	pid, _ = p.Spawn(context.Background(), nil)
	if err := p.Stop(pid); err != nil || !p.Alive(pid) {
		t.Fatalf("Stop with IgnoreStop: err=%v alive=%v", err, p.Alive(pid))
	}
	if err := p.Kill(pid); err != nil || p.Alive(pid) {
		t.Fatalf("Kill: err=%v alive=%v", err, p.Alive(pid))
	}
	p.SetUnsupported(true)
	if err := p.Resync(pid); !errors.Is(err, domain.ErrUnsupportedPlatform) {
		t.Fatalf("Resync unsupported = %v", err)
	}
}
