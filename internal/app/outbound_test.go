package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

var tb0 = time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

// bridgeFixture holds the fakes behind an outbound/inbound pair with a
// hand-built topic view and an in-memory agent set.
type bridgeFixture struct {
	herdr   *testkit.FakeHerdr
	tg      *testkit.FakeTelegram
	clock   *testkit.FakeClock
	mapping *domain.Mapping
	view    *topicView
	agents  map[domain.Key]domain.Agent
	capture *Capture
	options *testkit.MemOptionsStore
	opts    *Options
	out     *outbound
	in      *inbound
	ctx     context.Context
}

// testBotToken is the exact secret the fixture's redactor knows.
const testBotToken = "1234567890:" + "AAHf3kJd9sLq2mN8pR4tV6wX0yZ1bC3dE5f" // built from parts so secret scanners ignore it

func newBridgeFixture(t *testing.T) *bridgeFixture {
	t.Helper()
	f := &bridgeFixture{
		herdr:   testkit.NewFakeHerdr(nil),
		tg:      testkit.NewFakeTelegram(nil),
		clock:   testkit.NewFakeClock(tb0),
		mapping: domain.NewMapping(-1001234567890),
		view:    newTopicView(),
		agents:  map[domain.Key]domain.Agent{},
		ctx:     context.Background(),
	}
	lookup := func(k domain.Key) (domain.Agent, bool) {
		a, ok := f.agents[k]
		return a, ok
	}
	live := func() []domain.Agent {
		var out []domain.Agent
		for _, a := range f.agents {
			out = append(out, a)
		}
		return out
	}
	f.capture = NewCapture(f.herdr, live, f.clock, nil)
	f.options = testkit.NewMemOptionsStore()
	f.opts = NewOptions(domain.DefaultOptions(), f.options, func(name string) []string { return f.tg.IconPack() }, nil)
	// Same wrapping as NewBridge: the fake records what really leaves.
	tg := newRedactingGateway(f.tg, domain.NewRedactor(testBotToken), f.opts.RedactEnabled, nil)
	f.out = newOutbound(f.herdr, tg, f.view, lookup, live, f.capture, f.opts, f.clock, nil)
	f.in = newInbound(f.herdr, tg, f.view, lookup, live, f.out, f.opts, -1001234567890, "agents_bot", f.clock, nil)
	return f
}

// add registers a live agent with a topic created on the fake Telegram.
func (f *bridgeFixture) add(t *testing.T, pane, term, name string, st domain.Status) domain.Agent {
	t.Helper()
	a := domain.Agent{Key: domain.Key{PaneID: pane, TerminalID: term}, WorkspaceLabel: "ws", Name: name, Status: st}
	topic, err := f.tg.CreateTopic(f.ctx, a.Label(), st)
	if err != nil {
		t.Fatal(err)
	}
	f.mapping.Link(a.Key, topic, a, tb0)
	f.view.publish(f.mapping)
	f.agents[a.Key] = a
	f.tg.Reset()
	return a
}

func (f *bridgeFixture) setStatus(a domain.Agent, st domain.Status) domain.Agent {
	a.Status = st
	f.agents[a.Key] = a
	return a
}

// fire advances the clock past the settle delay and runs every due key.
func (f *bridgeFixture) fire(t *testing.T, want int) {
	t.Helper()
	f.clock.Advance(screenSettle)
	for i := 0; i < want; i++ {
		select {
		case key := <-f.out.Due():
			if err := f.out.Fire(f.ctx, key); err != nil {
				t.Fatalf("Fire = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("due screen %d did not fire", i+1)
		}
	}
	select {
	case key := <-f.out.Due():
		t.Fatalf("unexpected extra due key %v", key)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestOutboundBlockedPostsAfterSettle(t *testing.T) {
	f := newBridgeFixture(t)
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusWorking)
	f.herdr.SetScreen("p1", "\n  Allow edit?  \n  1. Yes  \n  2. No  \n\n")
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusBlocked)})
	if len(f.tg.Sent()) != 0 {
		t.Fatalf("posted before settle: %+v", f.tg.Sent())
	}
	f.fire(t, 1)
	sent := f.tg.Sent()
	if len(sent) != 1 || sent[0].ThreadID != 101 || !sent[0].Code || !sent[0].Notify || sent[0].Text != "  Allow edit?\n  1. Yes\n  2. No" {
		t.Fatalf("Sent = %+v", sent)
	}
	reads := f.herdr.Reads()
	if len(reads) != 1 || reads[0] != (testkit.ReadCall{Target: "p1", Source: domain.ScreenDetection, Lines: blockedLines}) {
		t.Fatalf("Reads = %+v", reads)
	}
}

func TestOutboundSkipsWhenNoLongerBlocked(t *testing.T) {
	f := newBridgeFixture(t)
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusWorking)
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusBlocked)})
	f.setStatus(a, domain.StatusWorking)
	f.fire(t, 1)
	if n := len(f.tg.Sent()); n != 0 {
		t.Fatalf("posted for a working agent: %d", n)
	}
	if n := len(f.herdr.Reads()); n != 0 {
		t.Fatalf("screen read for a working agent: %d", n)
	}
}

func TestOutboundWorkingEventCancelsPending(t *testing.T) {
	f := newBridgeFixture(t)
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusWorking)
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusBlocked)})
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusWorking)})
	if f.out.deb.Pending() != 0 {
		t.Fatalf("timer still pending after working event")
	}
}

func TestOutboundDuplicateScreenSkipped(t *testing.T) {
	f := newBridgeFixture(t)
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusWorking)
	f.herdr.SetScreen("p1", "same question")
	blocked := f.setStatus(a, domain.StatusBlocked)
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: blocked})
	f.fire(t, 1)
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusWorking)})
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusBlocked)})
	f.fire(t, 1)
	if n := len(f.tg.Sent()); n != 1 {
		t.Fatalf("duplicate posted: %d sends", n)
	}
	if n := len(f.herdr.Reads()); n != 2 {
		t.Fatalf("second screen not read: %d reads", n)
	}
	f.herdr.SetScreen("p1", "new question")
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: blocked})
	f.fire(t, 1)
	if sent := f.tg.Sent(); len(sent) != 2 || sent[1].Text != "new question" {
		t.Fatalf("changed screen not posted: %+v", sent)
	}
}

func TestOutboundDoneIsShortAndSilent(t *testing.T) {
	f := newBridgeFixture(t)
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusWorking)
	f.herdr.SetScreen("p1", "recap: all tests pass")
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusDone)})
	f.fire(t, 1)
	sent := f.tg.Sent()
	if len(sent) != 1 || sent[0].Notify || !sent[0].Code {
		t.Fatalf("Sent = %+v", sent)
	}
	if reads := f.herdr.Reads(); len(reads) != 1 || reads[0].Lines != doneLines {
		t.Fatalf("Reads = %+v", reads)
	}
}

func TestOutboundAppeared(t *testing.T) {
	f := newBridgeFixture(t)
	blocked := f.add(t, "p1", "t1", "a", domain.StatusBlocked)
	done := f.add(t, "p2", "t2", "b", domain.StatusDone)
	f.out.Observe(AgentEvent{Kind: AgentAppeared, Agent: blocked})
	f.out.Observe(AgentEvent{Kind: AgentAppeared, Agent: done})
	f.fire(t, 1)
	sent := f.tg.Sent()
	if len(sent) != 1 || sent[0].ThreadID != 101 {
		t.Fatalf("Sent = %+v (appeared+blocked posts, appeared+done does not)", sent)
	}
}

func TestOutboundMutedAndExitedSkipped(t *testing.T) {
	f := newBridgeFixture(t)
	a := f.add(t, "p1", "t1", "a", domain.StatusWorking)
	f.mapping.Mute(a.Key, tb0)
	f.view.publish(f.mapping)
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusBlocked)})
	f.fire(t, 1)
	if n := len(f.tg.Sent()); n != 0 {
		t.Fatalf("muted topic got a post: %d", n)
	}

	b := f.add(t, "p2", "t2", "b", domain.StatusWorking)
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(b, domain.StatusBlocked)})
	delete(f.agents, b.Key)
	f.fire(t, 1)
	if n := len(f.tg.Sent()); n != 0 {
		t.Fatalf("exited agent got a post: %d", n)
	}

	c := f.add(t, "p3", "t3", "c", domain.StatusWorking)
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(c, domain.StatusBlocked)})
	f.out.Observe(AgentEvent{Kind: AgentGone, Agent: c})
	if f.out.deb.Pending() != 0 {
		t.Fatal("gone did not cancel the timer")
	}
}

func TestOutboundReadErrorDoesNotPoisonHash(t *testing.T) {
	f := newBridgeFixture(t)
	a := f.add(t, "p1", "t1", "a", domain.StatusWorking)
	f.herdr.SetScreen("p1", "question")
	blocked := f.setStatus(a, domain.StatusBlocked)
	f.herdr.FailNext("read", domain.ErrDisconnected)
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: blocked})
	f.fire(t, 1)
	if n := len(f.tg.Sent()); n != 0 {
		t.Fatalf("posted despite read failure: %d", n)
	}
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: blocked})
	f.fire(t, 1)
	if sent := f.tg.Sent(); len(sent) != 1 || sent[0].Text != "question" {
		t.Fatalf("retry after read failure: %+v", sent)
	}
}

func TestOutboundSendErrorPolicy(t *testing.T) {
	f := newBridgeFixture(t)
	a := f.add(t, "p1", "t1", "a", domain.StatusWorking)
	f.herdr.SetScreen("p1", "question")
	blocked := f.setStatus(a, domain.StatusBlocked)

	f.tg.FailNext("send", domain.ErrTopicClosed)
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: blocked})
	f.clock.Advance(screenSettle)
	key := <-f.out.Due()
	if err := f.out.Fire(f.ctx, key); err != nil {
		t.Fatalf("closed topic must not be fatal: %v", err)
	}
	// The hash was not stored, so the next attempt posts again.
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: blocked})
	f.fire(t, 1)
	if n := len(f.tg.Sent()); n != 1 {
		t.Fatalf("sends after closed topic = %d", n)
	}

	f.herdr.SetScreen("p1", "other question")
	f.tg.FailNext("send", domain.ErrForbidden)
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: blocked})
	f.clock.Advance(screenSettle)
	key = <-f.out.Due()
	if err := f.out.Fire(f.ctx, key); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("forbidden must propagate, got %v", err)
	}
}

func TestOutboundScreenOnRequest(t *testing.T) {
	f := newBridgeFixture(t)
	a := f.add(t, "p1", "t1", "a", domain.StatusWorking)
	f.mapping.Mute(a.Key, tb0)
	f.view.publish(f.mapping)
	f.herdr.SetScreen("p1", "full screen\n")
	if err := f.out.Screen(f.ctx, a.Key, 0); err != nil {
		t.Fatal(err)
	}
	if err := f.out.Screen(f.ctx, a.Key, 10); err != nil {
		t.Fatal(err)
	}
	reads := f.herdr.Reads()
	if len(reads) != 2 || reads[0].Source != domain.ScreenVisible || reads[0].Lines != 0 || reads[1].Lines != 10 {
		t.Fatalf("Reads = %+v", reads)
	}
	sent := f.tg.Sent()
	if len(sent) != 2 || sent[0].Text != "full screen" || sent[0].Notify || !sent[0].Code {
		t.Fatalf("Sent = %+v", sent)
	}
	f.herdr.SetScreen("p1", "   \n\n")
	if err := f.out.Screen(f.ctx, a.Key, 0); err != nil {
		t.Fatal(err)
	}
	if sent := f.tg.Sent(); sent[2].Text != "(screen is empty)" {
		t.Fatalf("empty screen text = %q", sent[2].Text)
	}
	f.herdr.FailNext("read", domain.ErrAgentGone)
	if err := f.out.Screen(f.ctx, a.Key, 0); !errors.Is(err, domain.ErrAgentGone) {
		t.Fatalf("read error not returned: %v", err)
	}
}

func TestTrimScreen(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"\n\n", ""},
		{"a  \nb\t\n", "a\nb"},
		{"\n\n  x  \n\n y \n\n", "  x\n\n y"},
		{"one\r\ntwo\r\n", "one\ntwo"},
	}
	for _, tt := range tests {
		if got := trimScreen(tt.in); got != tt.want {
			t.Errorf("trimScreen(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if !strings.HasPrefix(hashText("a"), "ca978112") {
		t.Errorf("hashText is not sha256 hex: %s", hashText("a"))
	}
}

func TestOutboundScreenAllInline(t *testing.T) {
	f := newBridgeFixture(t)
	a := f.add(t, "w1:p1", "t1", "a", domain.StatusWorking)
	f.mapping.Mute(a.Key, tb0)
	f.view.publish(f.mapping)
	f.capture.Observe(AgentEvent{Kind: AgentChanged, Agent: a})
	f.herdr.SetScreen("w1:p1", "line1\nline2\n")
	if err := f.out.ScreenAll(f.ctx, a.Key); err != nil {
		t.Fatal(err)
	}
	reads := f.herdr.Reads()
	if len(reads) != 1 || reads[0].Source != domain.ScreenRecent || reads[0].Lines != captureLines {
		t.Fatalf("Reads = %+v", reads)
	}
	sent := f.tg.Sent()
	if len(sent) != 1 || sent[0].Text != "line1\nline2" || !sent[0].Code || sent[0].Notify || sent[0].ThreadID != 101 {
		t.Fatalf("Sent = %+v", sent)
	}
	if docs := f.tg.Documents(); len(docs) != 0 {
		t.Fatalf("Documents = %+v, want none", docs)
	}
}

func TestOutboundScreenAllDocument(t *testing.T) {
	f := newBridgeFixture(t)
	a := f.add(t, "w1:p1", "t1", "a", domain.StatusWorking)
	f.capture.Observe(AgentEvent{Kind: AgentChanged, Agent: a})
	lines := make([]string, 0, 200)
	for i := 0; i < 200; i++ {
		lines = append(lines, fmt.Sprintf("%03d %s", i, strings.Repeat("ж", 70)))
	}
	f.herdr.SetScreen("w1:p1", strings.Join(lines, "\n"))
	if err := f.out.ScreenAll(f.ctx, a.Key); err != nil {
		t.Fatal(err)
	}
	if sent := f.tg.Sent(); len(sent) != 0 {
		t.Fatalf("Sent = %+v, want a document instead", sent)
	}
	docs := f.tg.Documents()
	if len(docs) != 1 {
		t.Fatalf("Documents = %d, want 1", len(docs))
	}
	d := docs[0]
	if d.ThreadID != 101 || d.Name != "screen-w1-p1-120000.txt" || d.Caption != "200 lines since your last message" {
		t.Fatalf("Document = thread %d name %q caption %q", d.ThreadID, d.Name, d.Caption)
	}
	if got := string(d.Data); got != strings.Join(lines, "\n")+"\n" {
		t.Fatalf("Document body differs: %d bytes", len(got))
	}
}

func TestOutboundScreenAllWithoutMark(t *testing.T) {
	f := newBridgeFixture(t)
	a := f.add(t, "w1:p1", "t1", "a", domain.StatusIdle)
	f.herdr.SetScreen("w1:p1", "only screen")
	if err := f.out.ScreenAll(f.ctx, a.Key); err != nil {
		t.Fatal(err)
	}
	sent := f.tg.Sent()
	if len(sent) != 1 || sent[0].Text != "(history starts at daemon start)\nonly screen" {
		t.Fatalf("Sent = %+v", sent)
	}
	// The same without a mark but long enough for a file says so in the caption.
	f.tg.Reset()
	f.herdr.SetScreen("w1:p1", strings.Repeat("x", 13000))
	if err := f.out.ScreenAll(f.ctx, a.Key); err != nil {
		t.Fatal(err)
	}
	docs := f.tg.Documents()
	if len(docs) != 1 || !strings.HasSuffix(docs[0].Caption, "since daemon start") {
		t.Fatalf("Documents = %+v", docs)
	}
}

func TestOutboundScreenAllEmptyAndErrors(t *testing.T) {
	f := newBridgeFixture(t)
	a := f.add(t, "w1:p1", "t1", "a", domain.StatusWorking)
	f.herdr.SetScreen("w1:p1", "  \n\n")
	if err := f.out.ScreenAll(f.ctx, a.Key); err != nil {
		t.Fatal(err)
	}
	if sent := f.tg.Sent(); len(sent) != 1 || sent[0].Text != "(no output since your last message)" || sent[0].Code {
		t.Fatalf("Sent = %+v", sent)
	}
	f.herdr.FailNext("read", domain.ErrAgentGone)
	if err := f.out.ScreenAll(f.ctx, a.Key); !errors.Is(err, domain.ErrAgentGone) {
		t.Fatalf("read error not returned: %v", err)
	}
	f.herdr.SetScreen("w1:p1", "text")
	f.tg.FailNext("send", domain.ErrForbidden)
	if err := f.out.ScreenAll(f.ctx, a.Key); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("send error not returned: %v", err)
	}
	if err := f.out.ScreenAll(f.ctx, domain.Key{PaneID: "nope", TerminalID: "t"}); err == nil {
		t.Fatal("unknown key must fail")
	}
}

func TestOutboundSyncOffSkipsPostsUntilOn(t *testing.T) {
	f := newBridgeFixture(t)
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusWorking)
	f.herdr.SetScreen("p1", "\n  Allow edit?  \n  1. Yes  \n  2. No  \n\n")
	if err := f.opts.Set(f.ctx, domain.OptionSyncEnabled, "false", 1); err != nil {
		t.Fatal(err)
	}
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusBlocked)})
	f.fire(t, 1)
	if len(f.tg.Sent()) != 0 || len(f.herdr.Reads()) != 0 {
		t.Fatalf("posted while sync off: %+v", f.tg.Sent())
	}

	if err := f.opts.Set(f.ctx, domain.OptionSyncEnabled, "true", 1); err != nil {
		t.Fatal(err)
	}
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusBlocked)})
	f.fire(t, 1)
	if sent := f.tg.Sent(); len(sent) != 1 || sent[0].Text != "  Allow edit?\n  1. Yes\n  2. No" {
		t.Fatalf("Sent after sync on = %+v", sent)
	}
}

// quietOutbound wires quiet mode into the fixture's outbound with a
// switchable predicate.
func quietOutbound(f *bridgeFixture) *bool {
	quiet := false
	f.out.SetPresence(func() bool { return quiet }, f.opts)
	return &quiet
}

func TestOutboundQuietPostsModes(t *testing.T) {
	for _, tc := range []struct {
		mode        domain.PostsMode
		sent        int
		notify      bool
		afterSilent bool // a Fire after quiet ends posts nothing new (duplicate)
	}{
		{domain.PostsSilent, 1, false, true},
		{domain.PostsNormal, 1, true, true},
		{domain.PostsHeld, 0, false, false},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			f := newBridgeFixture(t)
			quiet := quietOutbound(f)
			if err := f.opts.Set(f.ctx, domain.OptionQuietPosts, string(tc.mode), 1); err != nil {
				t.Fatal(err)
			}
			a := f.add(t, "p1", "t1", "reviewer", domain.StatusWorking)
			f.herdr.SetScreen("p1", "\n  Allow edit?  \n  1. Yes  \n  2. No  \n\n")
			*quiet = true
			a = f.setStatus(a, domain.StatusBlocked)
			f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: a})
			f.fire(t, 1)
			sent := f.tg.Sent()
			if len(sent) != tc.sent {
				t.Fatalf("sent while quiet = %+v", sent)
			}
			if tc.sent == 1 && sent[0].Notify != tc.notify {
				t.Fatalf("notify = %v, want %v", sent[0].Notify, tc.notify)
			}
			if got := f.out.announced[a.Key]; got != (tc.sent == 1 && tc.notify) {
				t.Errorf("announced = %v", got)
			}
			// Quiet ends and the agent is still blocked: a plain Fire posts
			// only what was held (a silent or normal post is a duplicate).
			*quiet = false
			f.tg.Reset()
			f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: a})
			f.fire(t, 1)
			if got := len(f.tg.Sent()); got != map[bool]int{true: 0, false: 1}[tc.afterSilent] {
				t.Fatalf("sent after quiet = %+v", f.tg.Sent())
			}
			if tc.mode == domain.PostsHeld && !f.tg.Sent()[0].Notify {
				t.Error("held post released without sound")
			}
		})
	}
}

func TestOutboundQuietDoneScreens(t *testing.T) {
	f := newBridgeFixture(t)
	quiet := quietOutbound(f)
	*quiet = true
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusWorking)
	f.herdr.SetScreen("p1", "\n  ✓ tests pass  \n\n")
	a = f.setStatus(a, domain.StatusDone)
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: a})
	f.fire(t, 1)
	if sent := f.tg.Sent(); len(sent) != 1 || sent[0].Notify {
		t.Fatalf("done while quiet (silent mode) = %+v", sent)
	}
	if err := f.opts.Set(f.ctx, domain.OptionQuietPosts, string(domain.PostsHeld), 1); err != nil {
		t.Fatal(err)
	}
	f.tg.Reset()
	f.herdr.SetScreen("p1", "\n  ✓ lint pass  \n\n")
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: a})
	f.fire(t, 1)
	if len(f.tg.Sent()) != 0 {
		t.Fatalf("done while quiet (held) = %+v", f.tg.Sent())
	}
}

func TestOutboundCatchUpOneSoundPerQuestion(t *testing.T) {
	f := newBridgeFixture(t)
	quiet := quietOutbound(f)
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusWorking)
	f.herdr.SetScreen("p1", "\n  Allow edit?  \n  1. Yes  \n  2. No  \n\n")
	*quiet = true
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusBlocked)})
	f.fire(t, 1) // silent post
	f.tg.Reset()

	*quiet = false
	if err := f.out.CatchUp(f.ctx); err != nil {
		t.Fatal(err)
	}
	sent := f.tg.Sent()
	if len(sent) != 1 || !sent[0].Notify || len(sent[0].Buttons) != 2 {
		t.Fatalf("catch-up post = %+v", sent)
	}
	if !f.out.announced[a.Key] {
		t.Error("catch-up did not mark the question announced")
	}
	// Back at the desk and away again: the same question stays silent.
	f.tg.Reset()
	if err := f.out.CatchUp(f.ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.tg.Sent()) != 0 {
		t.Fatalf("second catch-up posted %+v", f.tg.Sent())
	}
	// The agent answers, works, asks again: a new question, announced anew.
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusWorking)})
	if f.out.announced[a.Key] {
		t.Fatal("leaving blocked did not clear the announced flag")
	}
	*quiet = true
	f.herdr.SetScreen("p1", "\n  Run tests?  \n  1. Yes  \n  2. No  \n\n")
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusBlocked)})
	f.fire(t, 1)
	*quiet = false
	f.tg.Reset()
	if err := f.out.CatchUp(f.ctx); err != nil {
		t.Fatal(err)
	}
	if sent := f.tg.Sent(); len(sent) != 1 || !sent[0].Notify || !strings.Contains(sent[0].Text, "Run tests?") {
		t.Fatalf("catch-up for the new question = %+v", sent)
	}
}

func TestOutboundCatchUpRules(t *testing.T) {
	f := newBridgeFixture(t)
	quiet := quietOutbound(f)
	// a: silently posted while quiet; b: new agent, never posted (no topic
	// yet at the time); c: muted; d: done, not blocked; e: errors on send.
	a := f.add(t, "p1", "t1", "a", domain.StatusWorking)
	f.herdr.SetScreen("p1", "\n  Q a?  \n\n")
	*quiet = true
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusBlocked)})
	f.fire(t, 1)
	b := f.add(t, "p2", "t2", "b", domain.StatusBlocked)
	f.herdr.SetScreen("p2", "\n  Q b?  \n\n")
	c := f.add(t, "p3", "t3", "c", domain.StatusBlocked)
	f.herdr.SetScreen("p3", "\n  Q c?  \n\n")
	f.mapping.Mute(c.Key, tb0)
	f.view.publish(f.mapping)
	d := f.add(t, "p4", "t4", "d", domain.StatusDone)
	f.herdr.SetScreen("p4", "\n  done d  \n\n")
	e := f.add(t, "p5", "t5", "e", domain.StatusBlocked)
	f.herdr.SetScreen("p5", "\n  Q e?  \n\n")
	_, _ = b, d
	f.tg.Reset()

	// Re-announce off: a (already has a post) is skipped, b (no post) still goes out.
	if err := f.opts.Set(f.ctx, domain.OptionQuietReannounce, "false", 1); err != nil {
		t.Fatal(err)
	}
	*quiet = false
	f.tg.FailNext("send", errors.New("boom"))
	if err := f.out.CatchUp(f.ctx); err != nil {
		t.Fatal(err)
	}
	sent := f.tg.Sent()
	texts := make([]string, 0, len(sent))
	for _, s := range sent {
		texts = append(texts, strings.TrimSpace(s.Text))
		if !s.Notify {
			t.Errorf("catch-up post without sound: %+v", s)
		}
	}
	// The failing send hit whichever of b and e came first; the other one posted.
	if len(sent) != 1 || (texts[0] != "Q b?" && texts[0] != "Q e?") {
		t.Fatalf("catch-up with reannounce off = %v", texts)
	}
	if f.out.announced[a.Key] || f.out.announced[c.Key] {
		t.Error("skipped agents were marked announced")
	}
	_ = e

	// Re-announce on: a is posted again with sound; the previously failed one too.
	if err := f.opts.Set(f.ctx, domain.OptionQuietReannounce, "true", 1); err != nil {
		t.Fatal(err)
	}
	f.tg.Reset()
	if err := f.out.CatchUp(f.ctx); err != nil {
		t.Fatal(err)
	}
	sent = f.tg.Sent()
	if len(sent) != 2 {
		t.Fatalf("catch-up with reannounce on = %+v", sent)
	}
	for _, s := range sent {
		if !s.Notify || strings.Contains(s.Text, "Q c?") || strings.Contains(s.Text, "done d") {
			t.Errorf("unexpected catch-up post %+v", s)
		}
	}
	// Fatal Telegram errors stop the catch-up and surface.
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusWorking)})
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusBlocked)})
	f.tg.FailNext("send", domain.ErrBotUnauthorized)
	if err := f.out.CatchUp(f.ctx); !errors.Is(err, domain.ErrBotUnauthorized) {
		t.Fatalf("fatal error not returned: %v", err)
	}
}
