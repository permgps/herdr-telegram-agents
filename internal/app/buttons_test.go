package app

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

// dialogScreen is the AskUserQuestion dialog measured on 2026-09-03.
const dialogScreen = `Какой цвет выбрать?

❯ 1. Красный
     Тёплый, яркий, привлекает внимание
  2. Зелёный
     Спокойный, ассоциируется с природой
  3. Синий
     Холодный, строгий, деловой
  4. Type something.
────────────────────────────────────────
  5. Chat about this

Enter to select · ↑/↓ to navigate · Esc to cancel`

const secondDialog = `Какой размер?

❯ 1. Маленький
  2. Большой
  3. Type something.

Enter to select · ↑/↓ to navigate · Esc to cancel`

// blockedWithDialog posts the dialog for a fresh blocked agent and returns
// the agent; the post is message 1000 in topic 101.
func blockedWithDialog(t *testing.T, f *bridgeFixture, screen string) domain.Agent {
	t.Helper()
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusWorking)
	f.herdr.SetScreen("p1", screen)
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusBlocked)})
	f.fire(t, 1)
	return a
}

func press(thread, msgID int, data string) domain.ButtonPressed {
	return domain.ButtonPressed{CallbackID: "cb1", ThreadID: thread, MessageID: msgID, FromID: 1, Data: data}
}

func TestOutboundBlockedAttachesButtons(t *testing.T) {
	f := newBridgeFixture(t)
	blockedWithDialog(t, f, dialogScreen)
	sent := f.tg.Sent()
	if len(sent) != 1 || !sent[0].Notify || !sent[0].Code {
		t.Fatalf("Sent = %+v", sent)
	}
	want := []domain.Button{{Text: "1️⃣ Красный", Data: "1"}, {Text: "2️⃣ Зелёный", Data: "2"}, {Text: "3️⃣ Синий", Data: "3"}}
	if !reflect.DeepEqual(sent[0].Buttons, want) {
		t.Fatalf("Buttons = %+v, want %+v", sent[0].Buttons, want)
	}
	if calls := f.tg.Calls(); len(calls) != 1 || !strings.HasSuffix(calls[0], ":notify:buttons=3") {
		t.Fatalf("Calls = %q", calls)
	}
	if kb := f.tg.Buttons(1000); len(kb) != 3 {
		t.Fatalf("keyboard on message 1000 = %+v", kb)
	}
}

func TestOutboundBlockedWithoutChoicesHasNoButtons(t *testing.T) {
	f := newBridgeFixture(t)
	blockedWithDialog(t, f, "Steps:\n  1. build\n  2. test\n\nContinue? (y/n)")
	sent := f.tg.Sent()
	if len(sent) != 1 || sent[0].Buttons != nil {
		t.Fatalf("Sent = %+v", sent)
	}
}

func TestOutboundDoneHasNoButtons(t *testing.T) {
	f := newBridgeFixture(t)
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusWorking)
	f.herdr.SetScreen("p1", "Summary:\n  1. one\n  2. two\n")
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusDone)})
	f.fire(t, 1)
	sent := f.tg.Sent()
	if len(sent) != 1 || sent[0].Buttons != nil || sent[0].Notify {
		t.Fatalf("Sent = %+v", sent)
	}
}

func TestOutboundPressSendsDigitAndMarksButton(t *testing.T) {
	f := newBridgeFixture(t)
	blockedWithDialog(t, f, dialogScreen)
	f.tg.Reset()
	if err := f.out.Press(f.ctx, press(101, 1000, "2")); err != nil {
		t.Fatal(err)
	}
	if keys := f.herdr.Keys(); len(keys) != 1 || !reflect.DeepEqual(keys[0], testkit.KeysCall{Target: "p1", Keys: []string{"2"}}) {
		t.Fatalf("Keys = %+v", keys)
	}
	assertCallsEqual(t, f.tg, "buttons:1000:✅ 2 · Зелёный", "answer:cb1:sent: 2")
	if kb := f.tg.Buttons(1000); len(kb) != 1 || kb[0].Data != "done" {
		t.Fatalf("keyboard after press = %+v", kb)
	}
	// The screen timer is armed again: a follow-up question (agent still
	// blocked, screen changed) is posted with its own buttons while the
	// answered keyboard keeps its ✅.
	f.herdr.SetScreen("p1", secondDialog)
	f.tg.Reset()
	f.fire(t, 1)
	sent := f.tg.Sent()
	if len(sent) != 1 || len(sent[0].Buttons) != 2 || sent[0].Buttons[1].Text != "2️⃣ Большой" {
		t.Fatalf("second question Sent = %+v", sent)
	}
	if kb := f.tg.Buttons(1000); len(kb) != 1 || kb[0].Data != "done" {
		t.Fatalf("first keyboard changed by the second post: %+v", kb)
	}
	// After the last answer the screen does not change: the re-armed
	// timer finds a duplicate and posts nothing.
	if err := f.out.Press(f.ctx, press(101, 1001, "2")); err != nil {
		t.Fatal(err)
	}
	f.tg.Reset()
	f.fire(t, 1)
	if n := len(f.tg.Sent()); n != 0 {
		t.Fatalf("unchanged screen reposted: %d", n)
	}
}

func TestOutboundPressRetiredMessageIsStale(t *testing.T) {
	f := newBridgeFixture(t)
	blockedWithDialog(t, f, dialogScreen)
	if err := f.out.Press(f.ctx, press(101, 1000, "2")); err != nil {
		t.Fatal(err)
	}
	f.tg.Reset()
	// The keyboard is gone from the table after the press, so a second
	// digit press on the same message can no longer act.
	if err := f.out.Press(f.ctx, press(101, 1000, "3")); err != nil {
		t.Fatal(err)
	}
	assertCallsEqual(t, f.tg, "buttons:1000:", "answer:cb1:not the latest question")
	if n := len(f.herdr.Keys()); n != 1 {
		t.Fatalf("Keys = %d, want the first press only", n)
	}
}

func TestOutboundPressDoneAnswersAlreadyAnswered(t *testing.T) {
	f := newBridgeFixture(t)
	blockedWithDialog(t, f, dialogScreen)
	f.tg.Reset()
	if err := f.out.Press(f.ctx, press(101, 1000, "done")); err != nil {
		t.Fatal(err)
	}
	assertCallsEqual(t, f.tg, "answer:cb1:already answered")
	if n := len(f.herdr.Keys()); n != 0 {
		t.Fatalf("keys sent for a done button: %d", n)
	}
}

func TestOutboundPressWhenNotBlocked(t *testing.T) {
	f := newBridgeFixture(t)
	a := blockedWithDialog(t, f, dialogScreen)
	f.setStatus(a, domain.StatusIdle)
	f.tg.Reset()
	if err := f.out.Press(f.ctx, press(101, 1000, "2")); err != nil {
		t.Fatal(err)
	}
	assertCallsEqual(t, f.tg, "buttons:1000:", "answer:cb1:agent is not waiting anymore")
	if n := len(f.herdr.Keys()); n != 0 {
		t.Fatalf("keys sent to an idle agent: %d", n)
	}
	if f.tg.Buttons(1000) != nil {
		t.Fatalf("keyboard not removed")
	}
}

func TestOutboundPressStaleMessage(t *testing.T) {
	f := newBridgeFixture(t)
	blockedWithDialog(t, f, dialogScreen)
	f.tg.Reset()
	if err := f.out.Press(f.ctx, press(101, 999, "2")); err != nil {
		t.Fatal(err)
	}
	assertCallsEqual(t, f.tg, "buttons:999:", "answer:cb1:not the latest question")
	if n := len(f.herdr.Keys()); n != 0 {
		t.Fatalf("keys sent for a stale message: %d", n)
	}
	if kb := f.tg.Buttons(1000); len(kb) != 3 {
		t.Fatalf("latest keyboard touched: %+v", kb)
	}
}

func TestOutboundPressUnknownThreadAndData(t *testing.T) {
	f := newBridgeFixture(t)
	blockedWithDialog(t, f, dialogScreen)
	f.tg.Reset()
	if err := f.out.Press(f.ctx, press(555, 1000, "2")); err != nil {
		t.Fatal(err)
	}
	if err := f.out.Press(f.ctx, press(101, 1000, "x")); err != nil {
		t.Fatal(err)
	}
	assertCallsEqual(t, f.tg, "buttons:1000:", "answer:cb1:topic is not mapped", "buttons:1000:", "answer:cb1:unknown button")
	if n := len(f.herdr.Keys()); n != 0 {
		t.Fatalf("keys sent: %d", n)
	}
}

func TestOutboundPressAgentGone(t *testing.T) {
	f := newBridgeFixture(t)
	a := blockedWithDialog(t, f, dialogScreen)
	delete(f.agents, a.Key)
	f.tg.Reset()
	if err := f.out.Press(f.ctx, press(101, 1000, "2")); err != nil {
		t.Fatal(err)
	}
	assertCallsEqual(t, f.tg, "buttons:1000:", "answer:cb1:agent has exited")
	if n := len(f.herdr.Keys()); n != 0 {
		t.Fatalf("keys sent to a gone agent: %d", n)
	}
}

func TestOutboundPressSendKeysFailure(t *testing.T) {
	f := newBridgeFixture(t)
	blockedWithDialog(t, f, dialogScreen)
	f.tg.Reset()
	f.herdr.FailNext("keys", domain.ErrAgentGone)
	if err := f.out.Press(f.ctx, press(101, 1000, "2")); err != nil {
		t.Fatal(err)
	}
	assertCallsEqual(t, f.tg, "buttons:1000:", "answer:cb1:⚠️ agent is gone")
	if _, ok := f.out.keyboards[domain.Key{PaneID: "p1", TerminalID: "t1"}]; ok {
		t.Fatalf("keyboard kept after a failed send")
	}
}

func TestOutboundNewScreenRetiresOldKeyboard(t *testing.T) {
	f := newBridgeFixture(t)
	a := blockedWithDialog(t, f, dialogScreen)
	f.tg.Reset()
	f.herdr.SetScreen("p1", secondDialog)
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusWorking)})
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusBlocked)})
	f.fire(t, 1)
	calls := f.tg.Calls()
	if len(calls) != 2 || calls[0] != "buttons:1000:" || !strings.HasPrefix(calls[1], "send:101:") || !strings.HasSuffix(calls[1], ":buttons=2") {
		t.Fatalf("Calls = %q", calls)
	}
	if f.tg.Buttons(1000) != nil || len(f.tg.Buttons(1001)) != 2 {
		t.Fatalf("keyboards: old=%+v new=%+v", f.tg.Buttons(1000), f.tg.Buttons(1001))
	}
}

func TestOutboundForgetRetiresKeyboard(t *testing.T) {
	f := newBridgeFixture(t)
	a := blockedWithDialog(t, f, dialogScreen)
	f.tg.Reset()
	if err := f.out.Forget(f.ctx, a.Key); err != nil {
		t.Fatal(err)
	}
	assertCallsEqual(t, f.tg, "buttons:1000:")
	if _, ok := f.out.lastPosted[a.Key]; ok {
		t.Fatalf("lastPosted kept after Forget")
	}
	// A second Forget has nothing to do.
	f.tg.Reset()
	if err := f.out.Forget(f.ctx, a.Key); err != nil {
		t.Fatal(err)
	}
	assertCallsEqual(t, f.tg)
}

func TestOutboundEditFailureIsAbsorbed(t *testing.T) {
	f := newBridgeFixture(t)
	blockedWithDialog(t, f, dialogScreen)
	f.tg.Reset()
	f.tg.FailNext("buttons", errors.New("boom"))
	if err := f.out.Press(f.ctx, press(101, 1000, "2")); err != nil {
		t.Fatalf("edit failure must be absorbed, got %v", err)
	}
	assertCallsEqual(t, f.tg, "buttons:1000:✅ 2 · Зелёный", "answer:cb1:sent: 2")
	if n := len(f.herdr.Keys()); n != 1 {
		t.Fatalf("Keys = %d", n)
	}
}

func TestOutboundEditFatalIsReturned(t *testing.T) {
	f := newBridgeFixture(t)
	blockedWithDialog(t, f, dialogScreen)
	f.tg.FailNext("buttons", domain.ErrBotUnauthorized)
	if err := f.out.Press(f.ctx, press(101, 1000, "2")); !errors.Is(err, domain.ErrBotUnauthorized) {
		t.Fatalf("err = %v, want the fatal error", err)
	}
}

func TestChoiceButtonsLabels(t *testing.T) {
	long := strings.Repeat("ж", 70)
	got := choiceButtons([]domain.Choice{{Number: 4, Label: "Yes"}, {Number: 5, Label: long}})
	if got[0].Text != "4️⃣ Yes" || got[0].Data != "4" {
		t.Fatalf("button 0 = %+v", got[0])
	}
	if want := strings.Repeat("ж", choiceLabelRunes-1) + "…"; got[1].Text != "5️⃣ "+want {
		t.Fatalf("long label = %q", got[1].Text)
	}
	if choiceButtons(nil) != nil {
		t.Fatalf("nil choices must give nil buttons")
	}
}
