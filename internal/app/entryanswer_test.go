package app

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

const entryHint = "⚠️ agent is waiting at a dialog: answer it with the buttons, ✏️ for your own text, or /keys; the message was not sent"

// liveClaudeScreen reads a Claude Code screen captured live on 2026-10-09
// (see internal/domain/testdata/claude).
func liveClaudeScreen(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "domain", "testdata", "claude", name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// claudeBlocked posts a blocked Claude Code agent's screen with its
// buttons (message 1000 in thread 101) and clears the Telegram record.
func claudeBlocked(t *testing.T, f *bridgeFixture, screen string) domain.Agent {
	t.Helper()
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusWorking)
	a.Kind = domain.ClaudeKind
	f.agents[a.Key] = a
	f.herdr.SetScreen("p1", screen)
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusBlocked)})
	f.fire(t, 1)
	f.tg.Reset()
	return f.agents[a.Key]
}

func TestEntryAnswerTypesThroughVerifiedEntry(t *testing.T) {
	f := newBridgeFixture(t)
	f.reactionsOn(t)
	claudeBlocked(t, f, liveClaudeScreen(t, "question"))
	f.herdr.SetScreenAfterKeys("p1", "4", liveClaudeScreen(t, "question-entry-open"))
	f.herdr.FailNext("prompt", domain.ErrAgentBlocked)

	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 7, "teal 2\nplease")); err != nil {
		t.Fatal(err)
	}
	// The entry's digit first, nothing typed until the screen proves the
	// entry has the focus.
	if keys := f.herdr.Keys(); !reflect.DeepEqual(keys, []testkit.KeysCall{{Target: "p1", Keys: []string{"4"}}}) {
		t.Fatalf("Keys = %+v", keys)
	}
	if texts := f.herdr.Texts(); len(texts) != 0 {
		t.Fatalf("typed before the entry was verified: %+v", texts)
	}
	f.fireInboundAfter(t, entryPollDelay, 1)
	if texts := f.herdr.Texts(); !reflect.DeepEqual(texts, []testkit.TextCall{{Target: "p1", Text: "teal 2 please"}}) {
		t.Fatalf("Texts = %+v", texts)
	}
	if keys := f.herdr.Keys(); len(keys) != 2 || !reflect.DeepEqual(keys[1].Keys, []string{domain.KeyEnter}) {
		t.Fatalf("Keys = %+v", keys)
	}
	assertCallsEqual(t, f.tg, "buttons:1000:✅ ✏️ · teal 2 please", "react:101:7:👀")
}

func TestEntryAnswerTypesIntoFocusedEntryWithoutDigit(t *testing.T) {
	f := newBridgeFixture(t)
	claudeBlocked(t, f, liveClaudeScreen(t, "question"))
	// The entry took the focus at the desk: a digit would be typed as text.
	f.herdr.SetScreen("p1", liveClaudeScreen(t, "question-entry-open"))
	f.herdr.FailNext("prompt", domain.ErrAgentBlocked)

	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 7, "teal")); err != nil {
		t.Fatal(err)
	}
	if keys := f.herdr.Keys(); !reflect.DeepEqual(keys, []testkit.KeysCall{{Target: "p1", Keys: []string{domain.KeyEnter}}}) {
		t.Fatalf("Keys = %+v", keys)
	}
	if texts := f.herdr.Texts(); !reflect.DeepEqual(texts, []testkit.TextCall{{Target: "p1", Text: "teal"}}) {
		t.Fatalf("Texts = %+v", texts)
	}
}

// A permission prompt has no free-text entry: nothing reaches the pane
// (on the live screen the typed text was ignored and the enter approved
// the command).
func TestEntryAnswerRefusesPermissionDialog(t *testing.T) {
	f := newBridgeFixture(t)
	claudeBlocked(t, f, liveClaudeScreen(t, "permission"))
	f.herdr.FailNext("prompt", domain.ErrAgentBlocked)

	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 7, "no, fix the test first")); err != nil {
		t.Fatal(err)
	}
	if keys, texts := f.herdr.Keys(), f.herdr.Texts(); len(keys) != 0 || len(texts) != 0 {
		t.Fatalf("Keys = %+v, Texts = %+v", keys, texts)
	}
	assertCallsEqual(t, f.tg, "send:101:"+entryHint+":reply=7")
}

func TestEntryAnswerRefusesChangedDialog(t *testing.T) {
	f := newBridgeFixture(t)
	claudeBlocked(t, f, liveClaudeScreen(t, "question"))
	// A permission prompt replaced the question before its post moved on.
	f.herdr.SetScreen("p1", liveClaudeScreen(t, "permission"))
	f.herdr.FailNext("prompt", domain.ErrAgentBlocked)

	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 7, "teal")); err != nil {
		t.Fatal(err)
	}
	if keys, texts := f.herdr.Keys(), f.herdr.Texts(); len(keys) != 0 || len(texts) != 0 {
		t.Fatalf("Keys = %+v, Texts = %+v", keys, texts)
	}
	assertCallsEqual(t, f.tg, "send:101:"+entryHint+":reply=7")
}

// The entry's digit went out but a permission prompt appeared instead of
// the focused entry: the polls give up and nothing is typed.
func TestEntryAnswerGivesUpWhenEntryNeverOpens(t *testing.T) {
	f := newBridgeFixture(t)
	claudeBlocked(t, f, liveClaudeScreen(t, "question"))
	f.herdr.SetScreenAfterKeys("p1", "4", liveClaudeScreen(t, "permission"))
	f.herdr.FailNext("prompt", domain.ErrAgentBlocked)

	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 7, "teal")); err != nil {
		t.Fatal(err)
	}
	for range entryPolls {
		f.fireInboundAfter(t, entryPollDelay, 1)
	}
	if texts := f.herdr.Texts(); len(texts) != 0 {
		t.Fatalf("typed into another dialog: %+v", texts)
	}
	if keys := f.herdr.Keys(); !reflect.DeepEqual(keys, []testkit.KeysCall{{Target: "p1", Keys: []string{"4"}}}) {
		t.Fatalf("Keys = %+v", keys)
	}
	assertCallsEqual(t, f.tg, "send:101:"+entryHint+":reply=7")
	if f.clock.Pending() != 0 {
		t.Fatalf("poll still armed: %d timers", f.clock.Pending())
	}
}

func TestEntryAnswerRefusesOtherAgentKinds(t *testing.T) {
	f := newBridgeFixture(t)
	a := claudeBlocked(t, f, liveClaudeScreen(t, "question"))
	a.Kind = "codex"
	f.agents[a.Key] = a
	f.herdr.FailNext("prompt", domain.ErrAgentBlocked)

	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 7, "teal")); err != nil {
		t.Fatal(err)
	}
	if keys, texts := f.herdr.Keys(), f.herdr.Texts(); len(keys) != 0 || len(texts) != 0 {
		t.Fatalf("Keys = %+v, Texts = %+v", keys, texts)
	}
	assertCallsEqual(t, f.tg, "send:101:"+entryHint+":reply=7")
}

func TestEntryAnswerAttachmentSkipsDelayedSubmit(t *testing.T) {
	f := newBridgeFixture(t)
	claudeBlocked(t, f, liveClaudeScreen(t, "question"))
	f.herdr.SetScreenAfterKeys("p1", "4", liveClaudeScreen(t, "question-entry-open"))
	f.tg.SetFile("file1", []byte("jpegbytes"))
	f.herdr.FailNext("prompt", domain.ErrAgentBlocked)

	if err := f.in.HandleAttachment(f.ctx, attachment(101, 42, domain.AttachmentPhoto, "file1", "", "look", 9)); err != nil {
		t.Fatal(err)
	}
	f.fireInboundAfter(t, entryPollDelay, 1)
	if texts := f.herdr.Texts(); !reflect.DeepEqual(texts, []testkit.TextCall{{Target: "p1", Text: "look /state/inbox/20260902-120001-42-photo.jpg"}}) {
		t.Fatalf("Texts = %+v", texts)
	}
	// The entry submitted the answer with its own enter; no delayed one.
	if f.clock.Pending() != 0 {
		t.Fatalf("delayed submit armed after typing into the entry: %d timers", f.clock.Pending())
	}
	if keys := f.herdr.Keys(); len(keys) != 2 || keys[0].Keys[0] != "4" || keys[1].Keys[0] != domain.KeyEnter {
		t.Fatalf("Keys = %+v", keys)
	}
}
