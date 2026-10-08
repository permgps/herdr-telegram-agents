package app_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/app"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

func TestPrivateFanoutAndReadKeyboardIsolation(t *testing.T) {
	f := newPrivateFixture(t, domain.ShareControl)
	ctx := context.Background()
	_, _ = f.s.Register(ctx, 11, 11, "reader", "", f.now)
	g, err := f.s.Grant(ctx, app.GrantRequest{RecipientID: 11, Key: f.g.Key, Role: domain.ShareRead, Bot: domain.BotIdentity{ID: 42, HasTopicsEnabled: true}}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	r := &app.PrivateReconciler{Sharing: f.s, Telegram: f.tg, Agent: f.s.Agent, Now: f.s.Now}
	if err := r.Grant(ctx, g); err != nil {
		t.Fatal(err)
	}
	f.h.SetScreen("p", "1. Allow\n2. Deny")
	p := &app.PrivateOutput{Control: f.p, Automatic: func() bool { return true }}
	f.p.Output = p
	a, _ := f.s.Agent(f.g.Key)
	a.Status = domain.StatusBlocked
	p.Observe(app.AgentEvent{Kind: app.AgentChanged, Agent: a})
	f.now = f.now.Add(3 * time.Second)
	if err := p.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	control, read := f.tg.Destination(10).Sent(), f.tg.Destination(11).Sent()
	if len(control) != 2 || len(read) != 2 {
		t.Fatalf("fanout posts %d/%d", len(control), len(read))
	}
	if len(control[1].Buttons) != 2 || len(read[1].Buttons) != 0 {
		t.Fatal("reader received action keyboard")
	}
	if len(f.h.Reads()) != 1 {
		t.Fatal("capture multiplied by mirror count")
	}
	if !control[1].Notify || !read[1].Notify {
		t.Fatal("blocked alert silent by default")
	}
	if strings.Contains(read[1].Text, "transcript") {
		t.Fatal("unverified source")
	}
	p.Observe(app.AgentEvent{Kind: app.AgentChanged, Agent: a})
	f.now = f.now.Add(3 * time.Second)
	_ = p.Tick(ctx)
	if len(f.tg.Destination(10).Sent()) != 2 {
		t.Fatal("dedup failed")
	}
}

func TestTwoControllersCannotSubmitSameDialog(t *testing.T) {
	f := newPrivateFixture(t, domain.ShareControl)
	ctx := context.Background()
	_, _ = f.s.Register(ctx, 11, 11, "controller", "", f.now)
	g, err := f.s.Grant(ctx, app.GrantRequest{RecipientID: 11, Key: f.g.Key, Role: domain.ShareControl, Bot: domain.BotIdentity{ID: 42, HasTopicsEnabled: true}}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	r := &app.PrivateReconciler{Sharing: f.s, Telegram: f.tg, Agent: f.s.Agent, Now: f.s.Now}
	if err := r.Grant(ctx, g); err != nil {
		t.Fatal(err)
	}
	f.h.SetScreen("p", "1. Allow\n2. Deny")
	output := &app.PrivateOutput{Control: f.p}
	f.p.Output = output
	a := f.setAgent(domain.StatusBlocked, 1)
	output.Observe(app.AgentEvent{Kind: app.AgentChanged, Agent: a})
	f.now = f.now.Add(3 * time.Second)
	if err := output.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	first := f.tg.Destination(10).Sent()[1].Buttons[0]
	second := f.tg.Destination(11).Sent()[1].Buttons[0]
	other, _ := f.s.Origin(g.ID)
	for _, e := range []domain.PrivateMessage{
		{Contact: domain.PrivateContact{ActorID: 10}, Address: f.origin.Address, MessageID: 1001, CallbackID: "first", CallbackData: first.Data},
		{Contact: domain.PrivateContact{ActorID: 11}, Address: other.Address, MessageID: 1001, CallbackID: "second", CallbackData: second.Data},
	} {
		if err := f.p.Handle(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.h.Keys()) != 1 {
		t.Fatal("two controllers submitted the same dialog")
	}
}

// TestPrivateOldDialogButtonDoesNotAnswerANewDialog: a dialog answered
// elsewhere (the owner at the desk) and followed by a new one must not take
// the old keyboard's press: "1 Allow" for the first question would approve
// the second. Covered both before the new dialog is mirrored (the agent's
// state moved on) and after (a newer post replaced the keyboard).
func TestPrivateOldDialogButtonDoesNotAnswerANewDialog(t *testing.T) {
	for _, mirrored := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_new_post", true: "after_new_post"}[mirrored], func(t *testing.T) {
			f := newPrivateFixture(t, domain.ShareControl)
			ctx := context.Background()
			output := &app.PrivateOutput{Control: f.p}
			f.p.Output = output
			f.h.SetScreen("p", "Read file foo?\n1. Allow\n2. Deny")
			output.Observe(app.AgentEvent{Kind: app.AgentChanged, Agent: f.setAgent(domain.StatusBlocked, 1)})
			f.now = f.now.Add(3 * time.Second)
			if err := output.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			sent := f.tg.Destination(10).Sent()
			old := sent[len(sent)-1]
			if len(old.Buttons) == 0 {
				t.Fatalf("first dialog posted without buttons: %+v", old)
			}
			// The owner answers at the desk; the agent works, then asks again.
			f.setAgent(domain.StatusWorking, 2)
			f.h.SetScreen("p", "Run rm -rf build?\n1. Yes\n2. No")
			blocked := f.setAgent(domain.StatusBlocked, 3)
			if mirrored {
				output.Observe(app.AgentEvent{Kind: app.AgentChanged, Agent: blocked})
				f.now = f.now.Add(3 * time.Second)
				if err := output.Tick(ctx); err != nil {
					t.Fatal(err)
				}
			}
			press := domain.PrivateMessage{Contact: domain.PrivateContact{ActorID: 10}, Address: f.origin.Address,
				MessageID: 1000 + len(sent) - 1, CallbackID: "old", CallbackData: old.Buttons[0].Data}
			if err := f.p.Handle(ctx, press); err != nil {
				t.Fatal(err)
			}
			if keys := f.h.Keys(); len(keys) != 0 {
				t.Fatalf("old Allow button answered the new dialog: %+v", keys)
			}
		})
	}
}

// TestPrivateOldTextEntryButtonDoesNotAnswerANewDialog: the ✏️ button of an
// answered question does not press its number in a newer dialog, and the
// recipient is told the question is gone.
func TestPrivateOldTextEntryButtonDoesNotAnswerANewDialog(t *testing.T) {
	f := newPrivateFixture(t, domain.ShareControl)
	ctx := context.Background()
	output := &app.PrivateOutput{Control: f.p}
	f.p.Output = output
	f.h.SetScreen("p", "Which colour?\n\n❯ 1. Red\n  2. Green\n  3. Type something.\n\nEnter to select · ↑/↓ to navigate · Esc to cancel")
	output.Observe(app.AgentEvent{Kind: app.AgentChanged, Agent: f.setAgent(domain.StatusBlocked, 1)})
	f.now = f.now.Add(3 * time.Second)
	if err := output.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	sent := f.tg.Destination(10).Sent()
	post := sent[len(sent)-1]
	var text domain.Button
	for _, b := range post.Buttons {
		if strings.Contains(b.Text, "Type something") {
			text = b
		}
	}
	if text.Data == "" {
		t.Fatalf("no text-entry button in %+v", post.Buttons)
	}
	f.setAgent(domain.StatusWorking, 2)
	f.h.SetScreen("p", "Run rm -rf build?\n1. Yes\n2. No\n3. No, and tell Claude what to do differently")
	f.setAgent(domain.StatusBlocked, 3)
	press := domain.PrivateMessage{Contact: domain.PrivateContact{ActorID: 10}, Address: f.origin.Address,
		MessageID: 1000 + len(sent) - 1, CallbackID: "old", CallbackData: text.Data}
	if err := f.p.Handle(ctx, press); err != nil {
		t.Fatal(err)
	}
	if keys := f.h.Keys(); len(keys) != 0 {
		t.Fatalf("old text-entry button pressed into the new dialog: %+v", keys)
	}
	after := f.tg.Destination(10).Sent()
	if last := after[len(after)-1]; last.Text != "That question is no longer open." {
		t.Fatalf("stale press not answered: %+v", last)
	}
}

// openCodeMirror turns the fixture's agent into an OpenCode agent whose
// exact replies come from the returned fake, wired as the mirror's output
// and /screen reader.
func openCodeMirror(f *privateFixture) (*app.PrivateOutput, *testkit.FakeReplies) {
	f.agent.Kind = "opencode"
	replies := testkit.NewFakeReplies()
	replies.SetNow(func() time.Time { return f.now })
	output := &app.PrivateOutput{Control: f.p, ExactReplies: replies, Automatic: func() bool { return true }}
	f.p.Output = output
	f.p.Read = output.Read
	f.h.SetScreen("p", "screen capture")
	return output, replies
}

// posts returns the texts sent to the recipient after the first n.
func (f *privateFixture) posts(n int) []string {
	var texts []string
	for _, o := range f.tg.Destination(10).Sent()[n:] {
		texts = append(texts, o.Text)
	}
	return texts
}

// TestPrivateScreenReplyFromBeforeGrant: an exact reply written before the
// grant was active belongs to the owner's private history; /screen and the
// automatic post show the screen instead, and a later reply as itself.
func TestPrivateScreenReplyFromBeforeGrant(t *testing.T) {
	for _, tc := range []struct {
		name    string
		written time.Duration
		want    string
	}{
		{"before_grant", -time.Minute, "screen capture"},
		{"after_grant", time.Second, "the reply"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newPrivateFixture(t, domain.ShareRead)
			output, replies := openCodeMirror(f)
			replies.Set(f.g.Key, "the reply")
			replies.SetMeta(f.g.Key, domain.TurnMeta{}, f.g.ActivatedAt.Add(tc.written))
			f.now = f.now.Add(time.Minute)
			before := len(f.tg.Destination(10).Sent())
			if err := f.p.Handle(ctx, domain.PrivateMessage{Contact: domain.PrivateContact{ActorID: 10}, Address: f.origin.Address, MessageID: 900, Text: "/screen"}); err != nil {
				t.Fatal(err)
			}
			if got := f.posts(before); len(got) != 1 || got[0] != tc.want {
				t.Fatalf("/screen posted %q, want %q", got, tc.want)
			}
			before = len(f.tg.Destination(10).Sent())
			output.Observe(app.AgentEvent{Kind: app.AgentChanged, Agent: f.setAgent(domain.StatusDone, 1)})
			f.now = f.now.Add(3 * time.Second)
			if err := output.Tick(ctx); err != nil {
				t.Fatal(err)
			}
			if got := f.posts(before); len(got) != 1 || got[0] != tc.want {
				t.Fatalf("automatic post %q, want %q", got, tc.want)
			}
		})
	}
}

// privatePending scripts the OpenCode reply source to report a running turn.
func privatePending(f *privateFixture, replies *testkit.FakeReplies) {
	replies.Fail(f.g.Key, fmt.Errorf("%w: turn running", domain.ErrReplyPending))
}

// TestPrivateMirrorWaitsForPendingReply: a done event while OpenCode still
// runs the turn is retried; the reply is posted once it lands, and no
// screen goes out before it.
func TestPrivateMirrorWaitsForPendingReply(t *testing.T) {
	ctx := context.Background()
	f := newPrivateFixture(t, domain.ShareRead)
	output, replies := openCodeMirror(f)
	privatePending(f, replies)
	before := len(f.tg.Destination(10).Sent())
	output.Observe(app.AgentEvent{Kind: app.AgentChanged, Agent: f.setAgent(domain.StatusDone, 1)})
	f.now = f.now.Add(3 * time.Second)
	if err := output.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.posts(before); len(got) != 0 {
		t.Fatalf("posted while the reply was pending: %q", got)
	}
	replies.Set(f.g.Key, "the reply")
	for range 3 {
		f.now = f.now.Add(app.ReplyPendingDelayForTest)
		if err := output.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.posts(before); len(got) != 1 || got[0] != "the reply" {
		t.Fatalf("posts after the reply landed: %q", got)
	}
}

// TestPrivateMirrorPendingReplyFallsBackToScreen: a reply that stays pending
// through every retry ends in exactly one screen post.
func TestPrivateMirrorPendingReplyFallsBackToScreen(t *testing.T) {
	ctx := context.Background()
	f := newPrivateFixture(t, domain.ShareRead)
	output, replies := openCodeMirror(f)
	privatePending(f, replies)
	before := len(f.tg.Destination(10).Sent())
	output.Observe(app.AgentEvent{Kind: app.AgentChanged, Agent: f.setAgent(domain.StatusDone, 1)})
	f.now = f.now.Add(3 * time.Second)
	for range app.ReplyPendingRetriesForTest + 3 {
		if err := output.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		f.now = f.now.Add(app.ReplyPendingDelayForTest)
	}
	if got := f.posts(before); len(got) != 1 || got[0] != "screen capture" {
		t.Fatalf("posts after the retries ran out: %q", got)
	}
	if n := len(replies.Calls()); n != app.ReplyPendingRetriesForTest+1 {
		t.Fatalf("reply source asked %d times", n)
	}
}

// TestPrivateMirrorPendingReplyDroppedByWork: the agent going back to work
// during the wait drops the retry, as on the owner's topics.
func TestPrivateMirrorPendingReplyDroppedByWork(t *testing.T) {
	ctx := context.Background()
	f := newPrivateFixture(t, domain.ShareRead)
	output, replies := openCodeMirror(f)
	privatePending(f, replies)
	before := len(f.tg.Destination(10).Sent())
	output.Observe(app.AgentEvent{Kind: app.AgentChanged, Agent: f.setAgent(domain.StatusDone, 1)})
	f.now = f.now.Add(3 * time.Second)
	if err := output.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	output.Observe(app.AgentEvent{Kind: app.AgentChanged, Agent: f.setAgent(domain.StatusWorking, 2)})
	for range app.ReplyPendingRetriesForTest + 2 {
		f.now = f.now.Add(app.ReplyPendingDelayForTest)
		if err := output.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.posts(before); len(got) != 0 {
		t.Fatalf("posted after the agent went back to work: %q", got)
	}
}

// TestPrivateScreenMirrorDoesNotWaitForReply: a mirror that shows the screen
// never waits for the reply source.
func TestPrivateScreenMirrorDoesNotWaitForReply(t *testing.T) {
	ctx := context.Background()
	f := newPrivateFixture(t, domain.ShareRead)
	output, replies := openCodeMirror(f)
	privatePending(f, replies)
	st, _ := f.s.Snapshot()
	prefs := st.Mirrors[f.g.ID].Preferences
	prefs.Display = "screen"
	if err := f.s.Preferences(ctx, f.origin, prefs, f.now); err != nil {
		t.Fatal(err)
	}
	before := len(f.tg.Destination(10).Sent())
	output.Observe(app.AgentEvent{Kind: app.AgentChanged, Agent: f.setAgent(domain.StatusDone, 1)})
	f.now = f.now.Add(3 * time.Second)
	if err := output.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.posts(before); len(got) != 1 || got[0] != "screen capture" {
		t.Fatalf("screen mirror posts: %q", got)
	}
}
