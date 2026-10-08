package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

// pickerRecipient gives recipient 10 a Control grant on a and wires a
// PrivateControl to the bridge fixture's inbound through SetPrivateControl,
// so both sides share the owner's picker hold. Prompts, keys and logs land
// in the fixture's fakes.
type pickerRecipient struct {
	p      *PrivateControl
	origin domain.ShareOrigin
	tg     *testkit.FakeTelegram
}

func newPickerRecipient(t *testing.T, f *bridgeFixture, a domain.Agent) *pickerRecipient {
	t.Helper()
	ctx := f.ctx
	s := NewSharing(ctx, testkit.NewMemSharingStore(), nil)
	s.BotID = 42
	s.Now = f.clock.Now
	s.Agent = func(k domain.Key) (domain.Agent, bool) {
		a, ok := f.agents[k]
		return a, ok
	}
	if _, err := s.Register(ctx, 10, 10, "name", "", f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	g, err := s.Grant(ctx, GrantRequest{RecipientID: 10, Key: a.Key, Role: domain.ShareControl, Bot: domain.BotIdentity{ID: 42, HasTopicsEnabled: true}}, f.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	private := testkit.NewFakeTelegram(nil)
	r := &PrivateReconciler{Sharing: s, Telegram: private, Agent: s.Agent, Now: s.Now}
	if err := r.Grant(ctx, g); err != nil {
		t.Fatal(err)
	}
	origin, _ := s.Origin(g.ID)
	log := slog.New(slog.NewJSONHandler(f.logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	p := &PrivateControl{Sharing: s, Telegram: private, Transport: private, Herdr: f.herdr, Inbox: f.inbox, Agent: s.Agent, Now: f.clock.Now, Log: log}
	b := &Bridge{in: f.in, out: f.out}
	b.SetPrivateControl(p)
	p.Async = func(fn func(context.Context) func(context.Context) error) bool {
		if result := fn(ctx); result != nil {
			_ = result(ctx)
		}
		return true
	}
	return &pickerRecipient{p: p, origin: origin, tg: private}
}

func (r *pickerRecipient) send(t *testing.T, e domain.PrivateMessage) {
	t.Helper()
	e.Contact = domain.PrivateContact{ActorID: 10}
	e.Address = r.origin.Address
	e.MessageID = 900
	if err := r.p.Handle(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}

// refusals counts the picker refusals the recipient received.
func (r *pickerRecipient) refusals() int {
	n := 0
	for _, o := range r.tg.Destination(10).Sent() {
		if o.Text == fmt.Sprintf(pickerRefusedFmt, "/model") {
			n++
		}
	}
	return n
}

// TestPrivatePickerHoldsRecipientPromptOnce: a Control recipient's plain
// message after the owner's /model left a picker open is held back once,
// with the owner's wording; the resend is typed.
func TestPrivatePickerHoldsRecipientPromptOnce(t *testing.T) {
	f := newBridgeFixture(t)
	keptPicker(t, f)
	r := newPickerRecipient(t, f, f.agents[domain.Key{PaneID: "p1", TerminalID: "t1"}])
	before := len(f.herdr.Prompts())
	r.send(t, domain.PrivateMessage{Text: "1"})
	if p := f.herdr.Prompts(); len(p) != before {
		t.Fatalf("held recipient message typed: %v", p)
	}
	if n := r.refusals(); n != 1 {
		t.Fatalf("refusals = %d", n)
	}
	if log := f.logBuf.String(); !strings.Contains(log, `"msg":"private prompt held for picker"`) || !strings.Contains(log, `"actor":10`) {
		t.Fatalf("hold not logged: %s", log)
	}
	r.send(t, domain.PrivateMessage{Text: "1"})
	if p := f.herdr.Prompts(); len(p) != before+1 || p[len(p)-1] != "p1: 1" {
		t.Fatalf("resend not typed: %v", p)
	}
	if n := r.refusals(); n != 1 {
		t.Fatalf("refusals after resend = %d", n)
	}
}

// TestPrivatePickerKeysReleaseOwnerHold: a recipient driving the picker
// with /keys releases the hold, so the owner's next message is typed.
func TestPrivatePickerKeysReleaseOwnerHold(t *testing.T) {
	f := newBridgeFixture(t)
	keptPicker(t, f)
	r := newPickerRecipient(t, f, f.agents[domain.Key{PaneID: "p1", TerminalID: "t1"}])
	r.send(t, domain.PrivateMessage{Text: "/keys esc"})
	if k := f.herdr.Keys(); len(k) == 0 {
		t.Fatal("recipient keys not sent")
	}
	if log := f.logBuf.String(); !strings.Contains(log, `"msg":"picker hold cleared"`) || !strings.Contains(log, `"reason":"keys"`) {
		t.Fatalf("release not logged: %s", log)
	}
	before := len(f.herdr.Prompts())
	f.tg.Reset()
	handle(t, f, "hello")
	if p := f.herdr.Prompts(); len(p) != before+1 || p[len(p)-1] != "p1: hello" {
		t.Fatalf("owner message after recipient /keys: %v", p)
	}
	for _, c := range f.tg.Calls() {
		if strings.Contains(c, "picker may still be open") {
			t.Fatalf("owner refused after the recipient released the hold: %v", f.tg.Calls())
		}
	}
}

// TestPrivatePickerHoldShared: the hold is one, so the owner's refusal uses
// it up for the recipient too.
func TestPrivatePickerHoldShared(t *testing.T) {
	f := newBridgeFixture(t)
	keptPicker(t, f)
	r := newPickerRecipient(t, f, f.agents[domain.Key{PaneID: "p1", TerminalID: "t1"}])
	before := len(f.herdr.Prompts())
	handle(t, f, "hello")
	if p := f.herdr.Prompts(); len(p) != before {
		t.Fatalf("owner message typed into the picker: %v", p)
	}
	r.send(t, domain.PrivateMessage{Text: "1"})
	if p := f.herdr.Prompts(); len(p) != before+1 || p[len(p)-1] != "p1: 1" {
		t.Fatalf("recipient message after the owner's refusal: %v", p)
	}
	if n := r.refusals(); n != 0 {
		t.Fatalf("recipient refused after the owner used the hold: %d", n)
	}
}

// TestPrivatePickerHoldsRecipientAttachment: a recipient's file sent while
// a kept picker may be open is saved but not typed, as on the owner's path.
func TestPrivatePickerHoldsRecipientAttachment(t *testing.T) {
	f := newBridgeFixture(t)
	keptPicker(t, f)
	r := newPickerRecipient(t, f, f.agents[domain.Key{PaneID: "p1", TerminalID: "t1"}])
	r.tg.SetFile("file", []byte("x"))
	before := len(f.herdr.Prompts())
	r.send(t, domain.PrivateMessage{Attachment: &domain.TopicAttachment{FileID: "file", Name: "a.txt", Kind: "document", Size: 1}})
	if p := f.herdr.Prompts(); len(p) != before {
		t.Fatalf("attachment typed into the picker: %v", p)
	}
	if saved := f.inbox.Saved(); len(saved) != 1 || !strings.HasPrefix(saved[0].Name, "shared-") {
		t.Fatalf("held attachment not kept: %+v", saved)
	}
	if n := r.refusals(); n != 1 {
		t.Fatalf("refusals = %d", n)
	}
	if _, held := f.in.pickers[domain.Key{PaneID: "p1", TerminalID: "t1"}]; held {
		t.Fatal("hold kept after the attachment used it")
	}
}
