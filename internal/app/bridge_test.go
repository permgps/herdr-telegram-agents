package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

type runningBridge struct {
	*bridgeFixture
	bridge *Bridge
	cancel context.CancelFunc
	done   chan struct{}
}

// newRunningBridge builds a Bridge over the fixture's fakes with a real
// registry and reconciler, and runs it until the test ends.
func newRunningBridge(t *testing.T) *runningBridge {
	t.Helper()
	f := newBridgeFixture(t)
	cfg := domain.Config{ChatID: -1001234567890, BotUsername: "agents_bot"}
	registry := NewRegistry(f.herdr, f.clock, nil)
	reconciler := NewReconciler(f.tg, f.herdr, testkit.NewMemMappingStore(), f.mapping, f.clock, nil)
	b := NewBridge(cfg, f.herdr, f.tg, registry, reconciler, f.clock, nil)
	// The fixture's agent map stands in for the registry so tests control
	// statuses directly.
	b.out.agents = f.out.agents
	b.in.agents = f.in.agents
	b.in.live = f.in.live
	b.out.topics, b.in.topics = f.view, f.view
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return &runningBridge{bridgeFixture: f, bridge: b, cancel: cancel, done: done}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestBridgeRunsJobsInOrder(t *testing.T) {
	r := newRunningBridge(t)
	r.add(t, "p1", "t1", "reviewer", domain.StatusIdle)
	r.bridge.Submit(topicMsg(101, 1, "first"))
	r.bridge.Submit(topicMsg(101, 2, "second"))
	r.bridge.Submit(domain.GeneralCommand{MessageID: 3, FromID: 1, Text: "/help"})
	// The help reply is the only Telegram call; it comes last, so both
	// prompts are already delivered when it shows up.
	waitUntil(t, "three jobs", func() bool { return len(r.tg.Calls()) == 1 })
	if p := r.herdr.Prompts(); len(p) != 2 || p[0] != "p1: first" || p[1] != "p1: second" {
		t.Fatalf("Prompts = %v", p)
	}
	assertCallsEqual(t, r.tg, "send:0:Commands\n/screen [N]: post the agent screen, the whole visible screen or its last N lines\n/keys k1 k2 ...: send raw keys to the agent (esc, enter, y, 1 ...)\n/focus: bring the agent's pane to the front in Herdr\n/status: this agent's status; in General, every agent with a link to its topic\n/help: this list\n\nWhile an agent is blocked, y, n, yes, no, 1-9, enter, ok and esc are sent as keys.\nAny other text is typed into the agent as a prompt.:reply=3")
}

func TestBridgeSettleTimerFiresThroughRun(t *testing.T) {
	r := newRunningBridge(t)
	a := r.add(t, "p1", "t1", "reviewer", domain.StatusWorking)
	r.herdr.SetScreen("p1", "question?")
	r.bridge.Submit(AgentEvent{Kind: AgentChanged, Agent: r.setStatus(a, domain.StatusBlocked)})
	waitUntil(t, "settle timer armed", func() bool { return r.clock.Pending() == 1 })
	if n := len(r.tg.Sent()); n != 0 {
		t.Fatalf("posted before settle: %d", n)
	}
	r.clock.Advance(screenSettle)
	waitUntil(t, "screen post", func() bool { return len(r.tg.Sent()) == 1 })
	if sent := r.tg.Sent(); sent[0].Text != "question?" || !sent[0].Notify {
		t.Fatalf("Sent = %+v", sent)
	}
}

func TestBridgeOverflowDrops(t *testing.T) {
	f := newBridgeFixture(t)
	b := &Bridge{out: f.out, in: f.in, jobs: make(chan any, 2), fatal: make(chan error, 1), log: f.out.log, CallTimeout: time.Second}
	for i := 0; i < 5; i++ {
		b.Submit(topicMsg(101, i, "x"))
	}
	if b.Dropped() != 3 {
		t.Fatalf("Dropped = %d, want 3", b.Dropped())
	}
	b.Submit("not a job")
	if b.Dropped() != 3 || len(b.jobs) != 2 {
		t.Fatalf("unknown job type changed the queue: dropped=%d queued=%d", b.Dropped(), len(b.jobs))
	}
}

func TestBridgeReportsFatalOnce(t *testing.T) {
	r := newRunningBridge(t)
	r.add(t, "p1", "t1", "reviewer", domain.StatusIdle)
	r.tg.FailNext("send", domain.ErrForbidden)
	r.bridge.Submit(topicMsg(101, 1, "/help"))
	select {
	case err := <-r.bridge.Fatal():
		if !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("Fatal = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fatal error not reported")
	}
	// A non-fatal failure afterwards is absorbed and the loop keeps serving.
	r.tg.FailNext("send", domain.ErrTopicGone)
	r.bridge.Submit(topicMsg(101, 2, "/help"))
	r.bridge.Submit(topicMsg(101, 3, "/help"))
	waitUntil(t, "later jobs served", func() bool { return len(r.tg.Calls()) == 3 })
	select {
	case err := <-r.bridge.Fatal():
		t.Fatalf("second fatal reported: %v", err)
	default:
	}
}
