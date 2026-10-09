package app

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// notices counts the unreadable-replies notice sends in the fake chat.
func notices(f *bridgeFixture) int {
	n := 0
	for _, s := range f.tg.Sent() {
		if s.Text == unreadableNotice {
			n++
		}
	}
	return n
}

// TestOutboundUnreadableNotice: after unreadableNoticeAfter consecutive
// done posts whose reply could not be read, one notice lands in the topic;
// a readable reply clears the streak and an unsupported kind never counts.
func TestOutboundUnreadableNotice(t *testing.T) {
	f := newBridgeFixture(t)
	f.herdr.SetScreen("p1", "raw screen text")
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusIdle)
	fail := fmt.Errorf("%w: export failed", domain.ErrNoReply)
	f.replies.Fail(a.Key, fail)
	turns := 0
	turn := func() {
		turns++
		f.herdr.SetScreen("p1", fmt.Sprintf("raw screen %d", turns))
		f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusWorking)})
		f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusDone)})
		f.fire(t, 1)
	}
	for i := 0; i < unreadableNoticeAfter-1; i++ {
		turn()
	}
	if got := notices(f); got != 0 {
		t.Fatalf("notice before the streak: %d", got)
	}
	turn()
	if got := notices(f); got != 1 {
		t.Fatalf("notices after the streak = %d, want 1", got)
	}
	// A readable reply clears the streak: the next failures start over.
	f.replies.Set(a.Key, "the answer")
	f.replies.SetMeta(a.Key, domain.TurnMeta{}, f.clock.Now().Add(time.Second))
	turn()
	f.replies.Fail(a.Key, fail)
	for i := 0; i < unreadableNoticeAfter-1; i++ {
		turn()
	}
	if got := notices(f); got != 1 {
		t.Fatalf("notices after a readable reply = %d, want 1", got)
	}
	// The streak continues: one more delivered failure reaches the
	// threshold again (a new streak after the readable reply) and never
	// repeats while the streak runs on.
	turn()
	if got := notices(f); got != 2 {
		t.Fatalf("second streak notice = %d, want 2", got)
	}
	for i := 0; i < unreadableNoticeAfter+2; i++ {
		turn()
	}
	if got := notices(f); got != 2 {
		t.Fatalf("notices in one long streak = %d, want 2", got)
	}

	// An unsupported kind never counts towards the notice.
	g := newBridgeFixture(t)
	g.herdr.SetScreen("p1", "raw screen text")
	b := g.add(t, "p1", "t1", "shell", domain.StatusIdle)
	g.replies.Fail(b.Key, fmt.Errorf("%w: unsupported agent %q", domain.ErrUnsupportedAgent, "shell"))
	for i := 0; i < unreadableNoticeAfter+1; i++ {
		g.out.Observe(AgentEvent{Kind: AgentChanged, Agent: g.setStatus(b, domain.StatusWorking)})
		g.out.Observe(AgentEvent{Kind: AgentChanged, Agent: g.setStatus(b, domain.StatusDone)})
		g.fire(t, 1)
	}
	if got := notices(g); got != 0 {
		t.Fatal("unsupported kind got the unreadable notice")
	}
}

// TestOutboundUnreadableNoticeIgnoresUndeliveredPosts: only fallback posts
// that actually go out count; duplicate screens are skipped and never
// advance the streak.
func TestOutboundUnreadableNoticeIgnoresUndeliveredPosts(t *testing.T) {
	f := newBridgeFixture(t)
	f.herdr.SetScreen("p1", "raw screen text")
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusIdle)
	f.replies.Fail(a.Key, fmt.Errorf("%w: export failed", domain.ErrNoReply))
	for i := 0; i < unreadableNoticeAfter+2; i++ {
		f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusWorking)})
		f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusDone)})
		f.fire(t, 1)
	}
	if got := notices(f); got != 0 {
		t.Fatalf("notice from undelivered duplicate posts: %d", got)
	}
}

// TestOutboundStaleNeverNotices: a reply older than the turn's start is an
// expected fallback (two panes in one directory), never a broken reader.
func TestOutboundStaleNeverNotices(t *testing.T) {
	f := newBridgeFixture(t)
	f.herdr.SetScreen("p1", "raw screen text")
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusIdle)
	f.replies.Set(a.Key, "an older answer")
	f.replies.SetMeta(a.Key, domain.TurnMeta{}, f.clock.Now().Add(-time.Minute))
	for i := 0; i < unreadableNoticeAfter+1; i++ {
		f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusWorking)})
		f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusDone)})
		f.fire(t, 1)
	}
	if got := notices(f); got != 0 {
		t.Fatal("stale transcripts got the unreadable notice")
	}
	if !errors.Is(domain.ErrStaleTranscript, domain.ErrStaleTranscript) {
		t.Fatal("sentinel sanity")
	}
}
