package app

import (
	"errors"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

// Screens of Claude Code in a pane Herdr does not drive, after the typed
// prompt "fix the tests" and its enter.
const (
	typedStuckScreen = "● done\n────\n❯ fix the tests\n────\n  ⏵⏵ auto mode"
	typedTakenScreen = "❯ fix the tests\n● on it\n────\n❯\n────\n  ⏵⏵ auto mode"
)

// typedFixture has an idle Claude Code agent Herdr does not drive in thread
// 101, showing screen.
func typedFixture(t *testing.T, screen string) *bridgeFixture {
	t.Helper()
	f := newBridgeFixture(t)
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusIdle)
	a.Kind = domain.ClaudeKind
	f.agents[a.Key] = a
	f.herdr.SetTyped("p1")
	f.herdr.SetScreen("p1", screen)
	return f
}

// visibleReads counts the screen reads of the typed-prompt check.
func visibleReads(h *testkit.FakeHerdr) int {
	n := 0
	for _, r := range h.Reads() {
		if r.Source == domain.ScreenVisible {
			n++
		}
	}
	return n
}

func TestTypedPromptLeftInBoxGetsReply(t *testing.T) {
	f := typedFixture(t, typedStuckScreen)
	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 7, "fix the tests")); err != nil {
		t.Fatal(err)
	}
	// The bridge goroutine does not wait for the screen.
	if n := visibleReads(f.herdr); n != 0 {
		t.Fatalf("screen read %d times before the check was due", n)
	}
	f.tg.Reset()
	f.fireInboundAfter(t, typedVerifyDelay, 1)
	assertCallsEqual(t, f.tg, "send:101:⚠️ "+failureReason(domain.ErrPromptNotSubmitted)+":reply=7")
}

func TestTypedPromptTakenIsSilent(t *testing.T) {
	f := typedFixture(t, typedTakenScreen)
	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 7, "fix the tests")); err != nil {
		t.Fatal(err)
	}
	f.tg.Reset()
	f.fireInboundAfter(t, typedVerifyDelay, 1)
	if n := visibleReads(f.herdr); n != 1 {
		t.Fatalf("screen read %d times, want 1", n)
	}
	assertCallsEqual(t, f.tg)
}

// A read that fails cannot tell, and a wrong alarm would make the operator
// press enter twice.
func TestTypedPromptUnreadableIsSilent(t *testing.T) {
	f := typedFixture(t, typedStuckScreen)
	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 7, "fix the tests")); err != nil {
		t.Fatal(err)
	}
	f.tg.Reset()
	f.herdr.FailNext("read", errors.New("agent target p1 not found"))
	f.fireInboundAfter(t, typedVerifyDelay, 1)
	assertCallsEqual(t, f.tg)
}

// A prompt Herdr took through agent.prompt is never read back.
func TestDrivenPromptIsNotChecked(t *testing.T) {
	f := newBridgeFixture(t)
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusIdle)
	a.Kind = domain.ClaudeKind
	f.agents[a.Key] = a
	f.herdr.SetScreen("p1", typedStuckScreen)
	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 7, "fix the tests")); err != nil {
		t.Fatal(err)
	}
	if _, armed := f.in.typedChecks["p1"]; armed {
		t.Fatal("typed-prompt check armed for a driven agent")
	}
}
