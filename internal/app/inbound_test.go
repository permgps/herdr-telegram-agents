package app

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

func topicMsg(thread, id int, text string) domain.TopicMessage {
	return domain.TopicMessage{ThreadID: thread, MessageID: id, FromID: 1, Text: text}
}

func assertCallsEqual(t *testing.T, tg *testkit.FakeTelegram, want ...string) {
	t.Helper()
	got := tg.Calls()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Calls = %q, want %q", got, want)
	}
}

func TestInboundPromptAndShortReply(t *testing.T) {
	f := newBridgeFixture(t)
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusIdle)

	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 5, "fix the tests")); err != nil {
		t.Fatal(err)
	}
	if p := f.herdr.Prompts(); len(p) != 1 || p[0] != "p1: fix the tests" {
		t.Fatalf("Prompts = %v", p)
	}
	assertCallsEqual(t, f.tg)

	// "y" while idle is a prompt, while blocked a key press.
	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 6, "y")); err != nil {
		t.Fatal(err)
	}
	f.setStatus(a, domain.StatusBlocked)
	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 7, "y")); err != nil {
		t.Fatal(err)
	}
	if p := f.herdr.Prompts(); len(p) != 2 || p[1] != "p1: y" {
		t.Fatalf("Prompts = %v", p)
	}
	if k := f.herdr.Keys(); len(k) != 1 || k[0].Target != "p1" || !reflect.DeepEqual(k[0].Keys, []string{"y"}) {
		t.Fatalf("Keys = %+v", k)
	}
	// Delivery is silent: no reaction, no reply.
	assertCallsEqual(t, f.tg)
}

func TestInboundKeysFocusScreen(t *testing.T) {
	f := newBridgeFixture(t)
	f.add(t, "p1", "t1", "reviewer", domain.StatusWorking)
	f.herdr.SetScreen("p1", "the screen")

	for _, text := range []string{"/keys esc enter", "/focus", "/screen 10", "/screen@agents_bot"} {
		if err := f.in.HandleTopic(f.ctx, topicMsg(101, 1, text)); err != nil {
			t.Fatalf("%s: %v", text, err)
		}
	}
	if k := f.herdr.Keys(); len(k) != 1 || !reflect.DeepEqual(k[0].Keys, []string{"esc", "enter"}) {
		t.Fatalf("Keys = %+v", k)
	}
	if fo := f.herdr.Focused(); len(fo) != 1 || fo[0] != "p1" {
		t.Fatalf("Focused = %v", fo)
	}
	reads := f.herdr.Reads()
	if len(reads) != 2 || reads[0].Lines != 10 || reads[0].Source != domain.ScreenVisible || reads[1].Lines != 0 {
		t.Fatalf("Reads = %+v", reads)
	}
	assertCallsEqual(t, f.tg, "send:101:the screen", "send:101:the screen")
}

func TestInboundStatusHelpUnknown(t *testing.T) {
	f := newBridgeFixture(t)
	f.add(t, "p1", "t1", "reviewer", domain.StatusBlocked)
	for _, text := range []string{"/status", "/help", "/restart"} {
		if err := f.in.HandleTopic(f.ctx, topicMsg(101, 3, text)); err != nil {
			t.Fatalf("%s: %v", text, err)
		}
	}
	sent := f.tg.Sent()
	if len(sent) != 3 {
		t.Fatalf("Sent = %+v", sent)
	}
	if sent[0].Text != "❓ blocked · ws · reviewer · pane p1" || sent[0].ReplyTo != 3 || sent[0].Notify {
		t.Errorf("status reply = %+v", sent[0])
	}
	if !strings.HasPrefix(sent[1].Text, "Commands\n/screen") || !strings.Contains(sent[1].Text, "/clear, /compact [instructions], /usage, /model [name]") {
		t.Errorf("help reply = %q", sent[1].Text)
	}
	if sent[2].Text != "unknown command, see /help" {
		t.Errorf("unknown reply = %q", sent[2].Text)
	}
	if n := len(f.herdr.Prompts()) + len(f.herdr.Keys()); n != 0 {
		t.Errorf("commands reached herdr: %d calls", n)
	}
}

func TestInboundExitedAndUnknownThread(t *testing.T) {
	f := newBridgeFixture(t)
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusWorking)
	delete(f.agents, a.Key)
	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 9, "hello")); err != nil {
		t.Fatal(err)
	}
	if err := f.in.HandleTopic(f.ctx, topicMsg(999, 10, "hello")); err != nil {
		t.Fatal(err)
	}
	assertCallsEqual(t, f.tg, "send:101:agent has exited:reply=9")
	if n := len(f.herdr.Prompts()); n != 0 {
		t.Fatalf("prompt sent to an exited agent")
	}
}

func TestInboundHerdrFailureReply(t *testing.T) {
	f := newBridgeFixture(t)
	f.add(t, "p1", "t1", "reviewer", domain.StatusIdle)
	f.herdr.FailNext("prompt", domain.ErrAgentGone)
	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 11, "go")); err != nil {
		t.Fatal(err)
	}
	f.herdr.FailNext("keys", errors.New("socket: write failed\nsecond line"))
	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 12, "/keys esc")); err != nil {
		t.Fatal(err)
	}
	f.herdr.FailNext("read", domain.ErrDisconnected)
	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 13, "/screen")); err != nil {
		t.Fatal(err)
	}
	assertCallsEqual(t, f.tg,
		"send:101:⚠️ agent is gone:reply=11",
		"send:101:⚠️ socket: write failed:reply=12",
		"send:101:⚠️ herdr is unreachable:reply=13")
}

func TestInboundTelegramErrorPolicy(t *testing.T) {
	f := newBridgeFixture(t)
	f.add(t, "p1", "t1", "reviewer", domain.StatusIdle)
	f.tg.FailNext("send", domain.ErrTopicGone)
	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 2, "/help")); err != nil {
		t.Fatalf("send failure must be absorbed: %v", err)
	}
	f.tg.FailNext("send", domain.ErrForbidden)
	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 3, "/help")); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("fatal send error must propagate, got %v", err)
	}
	f.herdr.FailNext("prompt", domain.ErrAgentGone)
	f.tg.FailNext("send", domain.ErrBotUnauthorized)
	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 4, "go")); !errors.Is(err, domain.ErrBotUnauthorized) {
		t.Fatalf("fatal error on the failure reply must propagate, got %v", err)
	}
}

func TestInboundGeneralStatus(t *testing.T) {
	f := newBridgeFixture(t)
	f.add(t, "p2", "t2", "zeta", domain.StatusWorking)
	f.add(t, "p1", "t1", "alpha <x>", domain.StatusBlocked)
	gone := f.add(t, "p3", "t3", "gone", domain.StatusIdle)
	delete(f.agents, gone.Key)
	f.agents[domain.Key{PaneID: "p4", TerminalID: "t4"}] = domain.Agent{Key: domain.Key{PaneID: "p4", TerminalID: "t4"}, Name: "notopic", Status: domain.StatusIdle}

	if err := f.in.HandleGeneral(f.ctx, domain.GeneralCommand{MessageID: 50, FromID: 1, Text: "/status@agents_bot"}); err != nil {
		t.Fatal(err)
	}
	sent := f.tg.Sent()
	if len(sent) != 1 || sent[0].ThreadID != 0 || !sent[0].HTML || sent[0].ReplyTo != 50 || sent[0].Notify {
		t.Fatalf("Sent = %+v", sent)
	}
	want := "3 agents\n" +
		"✅ notopic\n" +
		`❓ <a href="https://t.me/c/1234567890/102">ws · alpha &lt;x&gt;</a>` + "\n" +
		`⚡ <a href="https://t.me/c/1234567890/101">ws · zeta</a>`
	if sent[0].Text != want {
		t.Fatalf("status =\n%s\nwant\n%s", sent[0].Text, want)
	}
}

func TestInboundGeneralHelpUnknownAndEmpty(t *testing.T) {
	f := newBridgeFixture(t)
	for _, text := range []string{"/help", "/whatever", "/status"} {
		if err := f.in.HandleGeneral(f.ctx, domain.GeneralCommand{MessageID: 1, FromID: 1, Text: text}); err != nil {
			t.Fatalf("%s: %v", text, err)
		}
	}
	sent := f.tg.Sent()
	if len(sent) != 3 || !strings.HasPrefix(sent[0].Text, "Commands") || sent[1].Text != "unknown command, see /help" || sent[2].Text != "no agents" {
		t.Fatalf("Sent = %+v", sent)
	}
	for _, s := range sent {
		if s.ThreadID != 0 || s.ReplyTo != 1 {
			t.Fatalf("general reply not addressed to General: %+v", s)
		}
	}
}

func TestTopicLinkAndPlural(t *testing.T) {
	if got := topicLink(-1001234567890, 42); got != "https://t.me/c/1234567890/42" {
		t.Errorf("topicLink = %s", got)
	}
	if got := topicLink(-1, 7); got != "https://t.me/c/1/7" {
		t.Errorf("topicLink small id = %s", got)
	}
	if plural(1, "agent") != "1 agent" || plural(0, "agent") != "0 agents" || plural(2, "agent") != "2 agents" {
		t.Errorf("plural is wrong")
	}
}

func TestInboundScreenAll(t *testing.T) {
	f := newBridgeFixture(t)
	f.add(t, "p1", "t1", "reviewer", domain.StatusWorking)
	f.herdr.SetScreen("p1", "the history")
	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 1, "/screen all")); err != nil {
		t.Fatal(err)
	}
	reads := f.herdr.Reads()
	if len(reads) != 1 || reads[0].Source != domain.ScreenRecent || reads[0].Lines != captureLines {
		t.Fatalf("Reads = %+v", reads)
	}
	assertCallsEqual(t, f.tg, "send:101:(history starts at daemon start)\nthe history")
	f.tg.Reset()
	f.herdr.FailNext("read", domain.ErrDisconnected)
	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 2, "/screen all")); err != nil {
		t.Fatal(err)
	}
	assertCallsEqual(t, f.tg, "send:101:⚠️ herdr is unreachable:reply=2")
	if !strings.Contains(helpText, `"all"`) {
		t.Fatal("help must mention /screen all")
	}
}

// fireCommand advances the clock past the command settle delay and runs
// every due follow-up.
func (f *bridgeFixture) fireCommand(t *testing.T, want int) {
	t.Helper()
	f.clock.Advance(commandSettle)
	for i := 0; i < want; i++ {
		select {
		case key := <-f.in.Due():
			if err := f.in.Fire(f.ctx, key); err != nil {
				t.Fatalf("Fire = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("due command %d did not fire", i+1)
		}
	}
	select {
	case key := <-f.in.Due():
		t.Fatalf("unexpected extra due key %v", key)
	case <-time.After(30 * time.Millisecond):
	}
}

const overlayScreen = "❯ /usage\ntranscript line\n▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔\n   Settings  Status   Usage\n\n   Current session\n   █████ 10% used\n\n"

func TestInboundForwardClearPostsTail(t *testing.T) {
	f := newBridgeFixture(t)
	f.add(t, "p1", "t1", "reviewer", domain.StatusIdle)
	f.herdr.SetScreen("p1", "\n❯\n────\n  main │ 0%: 0k\n")

	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 21, "/clear")); err != nil {
		t.Fatal(err)
	}
	if p := f.herdr.Prompts(); len(p) != 1 || p[0] != "p1: /clear" {
		t.Fatalf("Prompts = %v", p)
	}
	assertCallsEqual(t, f.tg)
	if f.in.Pending() != 1 || f.clock.Pending() != 1 {
		t.Fatalf("pending = %d timers = %d", f.in.Pending(), f.clock.Pending())
	}
	f.fireCommand(t, 1)
	assertCallsEqual(t, f.tg, "send:101:❯\n────\n  main │ 0%: 0k:reply=21")
	sent := f.tg.Sent()
	if !sent[0].Code || sent[0].Notify {
		t.Fatalf("post = %+v", sent[0])
	}
	reads := f.herdr.Reads()
	if len(reads) != 1 || reads[0].Source != domain.ScreenVisible || reads[0].Lines != commandTailLines {
		t.Fatalf("Reads = %+v", reads)
	}
	if k := f.herdr.Keys(); len(k) != 0 {
		t.Fatalf("esc sent after /clear: %+v", k)
	}
	if f.in.Pending() != 0 {
		t.Fatalf("pending after fire = %d", f.in.Pending())
	}
}

func TestInboundForwardUsageCutsOverlayAndDismisses(t *testing.T) {
	f := newBridgeFixture(t)
	f.add(t, "p1", "t1", "reviewer", domain.StatusDone)
	f.herdr.SetScreen("p1", overlayScreen)

	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 22, "/usage")); err != nil {
		t.Fatal(err)
	}
	f.fireCommand(t, 1)
	assertCallsEqual(t, f.tg, "send:101:   Settings  Status   Usage\n\n   Current session\n   █████ 10% used:reply=22")
	if reads := f.herdr.Reads(); len(reads) != 1 || reads[0].Lines != 0 || reads[0].Source != domain.ScreenVisible {
		t.Fatalf("Reads = %+v", reads)
	}
	if k := f.herdr.Keys(); len(k) != 1 || k[0].Target != "p1" || !reflect.DeepEqual(k[0].Keys, []string{"esc"}) {
		t.Fatalf("Keys = %+v", k)
	}
}

func TestInboundForwardScreenWithoutRuleIsPostedWhole(t *testing.T) {
	f := newBridgeFixture(t)
	f.add(t, "p1", "t1", "reviewer", domain.StatusIdle)
	f.herdr.SetScreen("p1", "\nno overlay here\n")

	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 23, "/usage")); err != nil {
		t.Fatal(err)
	}
	f.fireCommand(t, 1)
	assertCallsEqual(t, f.tg, "send:101:no overlay here:reply=23")
}

func TestInboundForwardModelBareVsName(t *testing.T) {
	f := newBridgeFixture(t)
	f.add(t, "p1", "t1", "reviewer", domain.StatusIdle)
	f.herdr.SetScreen("p1", overlayScreen)

	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 24, "/model")); err != nil {
		t.Fatal(err)
	}
	f.fireCommand(t, 1)
	if k := f.herdr.Keys(); len(k) != 1 {
		t.Fatalf("bare /model: Keys = %+v", k)
	}
	if reads := f.herdr.Reads(); len(reads) != 1 || reads[0].Lines != 0 {
		t.Fatalf("bare /model: Reads = %+v", reads)
	}

	f.herdr.SetScreen("p1", "❯ /model sonnet\n  ⎿  Set model to sonnet\n")
	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 25, "/model sonnet")); err != nil {
		t.Fatal(err)
	}
	f.fireCommand(t, 1)
	if p := f.herdr.Prompts(); len(p) != 2 || p[1] != "p1: /model sonnet" {
		t.Fatalf("Prompts = %v", p)
	}
	if k := f.herdr.Keys(); len(k) != 1 {
		t.Fatalf("/model sonnet sent esc: %+v", k)
	}
	if reads := f.herdr.Reads(); len(reads) != 2 || reads[1].Lines != commandTailLines {
		t.Fatalf("/model sonnet: Reads = %+v", reads)
	}
	sent := f.tg.Sent()
	if len(sent) != 2 || sent[1].Text != "❯ /model sonnet\n  ⎿  Set model to sonnet" || sent[1].ReplyTo != 25 {
		t.Fatalf("Sent = %+v", sent)
	}
}

func TestInboundForwardCompactIsSilent(t *testing.T) {
	f := newBridgeFixture(t)
	f.add(t, "p1", "t1", "reviewer", domain.StatusIdle)

	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 26, "/compact keep the API notes")); err != nil {
		t.Fatal(err)
	}
	if p := f.herdr.Prompts(); len(p) != 1 || p[0] != "p1: /compact keep the API notes" {
		t.Fatalf("Prompts = %v", p)
	}
	if f.in.Pending() != 0 || f.clock.Pending() != 0 {
		t.Fatalf("compact armed a follow-up: pending=%d timers=%d", f.in.Pending(), f.clock.Pending())
	}
	f.fireCommand(t, 0)
	assertCallsEqual(t, f.tg)
	if n := len(f.herdr.Reads()) + len(f.herdr.Keys()); n != 0 {
		t.Fatalf("compact read the screen or sent keys: %d calls", n)
	}
}

func TestInboundForwardRefused(t *testing.T) {
	f := newBridgeFixture(t)
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusWorking)

	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 27, "/usage")); err != nil {
		t.Fatal(err)
	}
	f.setStatus(a, domain.StatusBlocked)
	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 28, "/clear")); err != nil {
		t.Fatal(err)
	}
	assertCallsEqual(t, f.tg,
		"send:101:⚠️ agent is working, try again when it is idle:reply=27",
		"send:101:⚠️ agent is waiting for an answer: reply to it or send /keys esc first:reply=28")
	if n := len(f.herdr.Prompts()) + len(f.herdr.Keys()); n != 0 {
		t.Fatalf("refused command reached herdr: %d calls", n)
	}
	if f.in.Pending() != 0 {
		t.Fatalf("refused command armed a follow-up")
	}

	// unknown passes like idle.
	f.setStatus(a, domain.StatusUnknown)
	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 29, "/clear")); err != nil {
		t.Fatal(err)
	}
	if p := f.herdr.Prompts(); len(p) != 1 || p[0] != "p1: /clear" {
		t.Fatalf("Prompts = %v", p)
	}
}

func TestInboundForwardPromptFailure(t *testing.T) {
	f := newBridgeFixture(t)
	f.add(t, "p1", "t1", "reviewer", domain.StatusIdle)
	f.herdr.FailNext("prompt", domain.ErrDisconnected)

	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 30, "/usage")); err != nil {
		t.Fatal(err)
	}
	assertCallsEqual(t, f.tg, "send:101:⚠️ herdr is unreachable:reply=30")
	if f.in.Pending() != 0 || f.clock.Pending() != 0 {
		t.Fatalf("failed prompt armed a follow-up")
	}
}

func TestInboundForwardReadFailureStillDismisses(t *testing.T) {
	f := newBridgeFixture(t)
	f.add(t, "p1", "t1", "reviewer", domain.StatusIdle)

	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 31, "/usage")); err != nil {
		t.Fatal(err)
	}
	f.herdr.FailNext("read", errors.New("read: timeout\nmore"))
	f.fireCommand(t, 1)
	assertCallsEqual(t, f.tg, "send:101:⚠️ screen read failed: read: timeout:reply=31")
	if k := f.herdr.Keys(); len(k) != 1 || !reflect.DeepEqual(k[0].Keys, []string{"esc"}) {
		t.Fatalf("Keys = %+v", k)
	}
}

func TestInboundForwardDismissFailureIsReported(t *testing.T) {
	f := newBridgeFixture(t)
	f.add(t, "p1", "t1", "reviewer", domain.StatusIdle)
	f.herdr.SetScreen("p1", overlayScreen)

	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 32, "/model")); err != nil {
		t.Fatal(err)
	}
	f.herdr.FailNext("keys", domain.ErrAgentGone)
	f.fireCommand(t, 1)
	calls := f.tg.Calls()
	if len(calls) != 2 || calls[1] != "send:101:⚠️ could not close the overlay: agent is gone:reply=32" {
		t.Fatalf("Calls = %q", calls)
	}
}

func TestInboundForwardReplacesPending(t *testing.T) {
	f := newBridgeFixture(t)
	f.add(t, "p1", "t1", "reviewer", domain.StatusIdle)
	f.herdr.SetScreen("p1", overlayScreen)

	for _, id := range []int{33, 34} {
		if err := f.in.HandleTopic(f.ctx, topicMsg(101, id, "/usage")); err != nil {
			t.Fatal(err)
		}
	}
	if f.in.Pending() != 1 {
		t.Fatalf("pending = %d, want 1", f.in.Pending())
	}
	f.fireCommand(t, 1)
	sent := f.tg.Sent()
	if len(sent) != 1 || sent[0].ReplyTo != 34 {
		t.Fatalf("Sent = %+v", sent)
	}
	if k := f.herdr.Keys(); len(k) != 1 {
		t.Fatalf("Keys = %+v", k)
	}
}

func TestInboundForwardAgentGoneBeforeFire(t *testing.T) {
	f := newBridgeFixture(t)
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusIdle)

	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 35, "/usage")); err != nil {
		t.Fatal(err)
	}
	delete(f.agents, a.Key)
	f.fireCommand(t, 1)
	assertCallsEqual(t, f.tg)
	if n := len(f.herdr.Reads()) + len(f.herdr.Keys()); n != 0 {
		t.Fatalf("follow-up touched a gone agent: %d calls", n)
	}
}

func TestInboundForwardTopicGoneStillDismisses(t *testing.T) {
	f := newBridgeFixture(t)
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusIdle)

	if err := f.in.HandleTopic(f.ctx, topicMsg(101, 36, "/usage")); err != nil {
		t.Fatal(err)
	}
	f.mapping.MarkExited(a.Key, tb0)
	f.view.publish(f.mapping)
	f.fireCommand(t, 1)
	assertCallsEqual(t, f.tg)
	if k := f.herdr.Keys(); len(k) != 1 || !reflect.DeepEqual(k[0].Keys, []string{"esc"}) {
		t.Fatalf("Keys = %+v", k)
	}
}
