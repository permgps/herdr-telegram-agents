package app

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

const testKey = "sk-abcdefghijklmnopqrstuvwx"

func TestRedactingGatewayMasksEverythingThatLeaves(t *testing.T) {
	fake := testkit.NewFakeTelegram(nil)
	on := true
	tg := newRedactingGateway(fake, domain.NewRedactor(testBotToken), func() bool { return on }, nil)
	ctx := context.Background()

	buttons := []domain.Button{{Text: "1️⃣ use " + testKey, Data: "1"}, {Text: "2️⃣ no", Data: "2"}}
	id, err := tg.Send(ctx, domain.Outgoing{ThreadID: 0, Text: "token " + testBotToken + " and " + testKey, Buttons: buttons})
	if err != nil {
		t.Fatal(err)
	}
	sent := fake.Sent()
	if len(sent) != 1 || sent[0].Text != "token [redacted] and sk-…uvwx" {
		t.Fatalf("Sent = %+v", sent)
	}
	if got := fake.Buttons(id); got[0].Text != "1️⃣ use sk-…uvwx" || got[1].Text != "2️⃣ no" {
		t.Fatalf("Buttons = %+v", got)
	}
	if buttons[0].Text != "1️⃣ use "+testKey {
		t.Fatal("caller's keyboard was rewritten in place")
	}

	if err := tg.SendDocument(ctx, domain.Document{ThreadID: 0, Name: "s.txt", Data: []byte("line " + testKey + "\n"), Caption: "cap " + testBotToken}); err != nil {
		t.Fatal(err)
	}
	docs := fake.Documents()
	if len(docs) != 1 || string(docs[0].Data) != "line sk-…uvwx\n" || docs[0].Caption != "cap [redacted]" {
		t.Fatalf("Documents = %+v", docs)
	}

	if err := tg.EditText(ctx, id, "edited "+testKey, false, []domain.Button{{Text: "Bearer 0123456789abcdefghij", Data: "x"}}); err != nil {
		t.Fatal(err)
	}
	if fake.Text(id) != "edited sk-…uvwx" || fake.Buttons(id)[0].Text != "Bearer …ghij" {
		t.Fatalf("EditText left %q / %+v", fake.Text(id), fake.Buttons(id))
	}
	if err := tg.EditButtons(ctx, id, []domain.Button{{Text: "✅ " + testKey, Data: "1"}}); err != nil {
		t.Fatal(err)
	}
	if fake.Buttons(id)[0].Text != "✅ sk-…uvwx" {
		t.Fatalf("EditButtons left %+v", fake.Buttons(id))
	}

	on = false
	if _, err := tg.Send(ctx, domain.Outgoing{ThreadID: 0, Text: "raw " + testKey}); err != nil {
		t.Fatal(err)
	}
	if got := fake.Sent(); got[len(got)-1].Text != "raw "+testKey {
		t.Fatalf("option off still redacted: %q", got[len(got)-1].Text)
	}
}

func TestRedactingGatewayPassesRightsAndIcons(t *testing.T) {
	fake := testkit.NewFakeTelegram(nil)
	tg := newRedactingGateway(fake, nil, nil, nil)
	if _, err := tg.Rights(context.Background()); err != nil {
		t.Fatal(err)
	}
	tg.SetStatusIcons(domain.StatusIcons{Working: "🔥"})
	if fake.Icons().Working != "🔥" || len(tg.IconPack()) == 0 {
		t.Fatal("pass-through methods broken")
	}
}

func TestOutboundBlockedPostIsRedacted(t *testing.T) {
	f := newBridgeFixture(t)
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusWorking)
	f.herdr.SetScreen("p1", "\n  Use key "+testKey+"?  \n  1. Yes "+testKey+"  \n  2. No  \n\n")
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusBlocked)})
	f.fire(t, 1)
	sent := f.tg.Sent()
	if len(sent) != 1 || sent[0].Text != "  Use key sk-…uvwx?\n  1. Yes sk-…uvwx\n  2. No" || !sent[0].Notify {
		t.Fatalf("Sent = %+v", sent)
	}
	if got := f.tg.Buttons(1000); len(got) != 2 || got[0].Text != "1️⃣ Yes sk-…uvwx" {
		t.Fatalf("Buttons = %+v", got)
	}
	// The duplicate check hashes the raw screen: the same screen again is
	// still skipped.
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusWorking)})
	f.out.Observe(AgentEvent{Kind: AgentChanged, Agent: f.setStatus(a, domain.StatusBlocked)})
	f.fire(t, 1)
	if n := len(f.tg.Sent()); n != 1 {
		t.Fatalf("duplicate posted: %d sends", n)
	}
}

func TestOutboundScreenAllDocumentIsRedacted(t *testing.T) {
	f := newBridgeFixture(t)
	a := f.add(t, "w1:p1", "t1", "a", domain.StatusWorking)
	f.capture.Observe(AgentEvent{Kind: AgentChanged, Agent: a})
	lines := make([]string, 0, 200)
	for i := 0; i < 200; i++ {
		lines = append(lines, fmt.Sprintf("%03d %s %s", i, testKey, strings.Repeat("ж", 60)))
	}
	f.herdr.SetScreen("w1:p1", strings.Join(lines, "\n"))
	if err := f.out.ScreenAll(f.ctx, a.Key); err != nil {
		t.Fatal(err)
	}
	docs := f.tg.Documents()
	if len(docs) != 1 || strings.Contains(string(docs[0].Data), testKey) || strings.Count(string(docs[0].Data), "sk-…uvwx") != 200 {
		t.Fatalf("document not redacted: %d docs", len(docs))
	}
}

func TestOutboundRedactionOffPostsRaw(t *testing.T) {
	f := newBridgeFixture(t)
	if err := f.opts.Set(f.ctx, domain.OptionRedact, "false", 1); err != nil {
		t.Fatal(err)
	}
	a := f.add(t, "p1", "t1", "reviewer", domain.StatusWorking)
	f.herdr.SetScreen("p1", "key "+testKey)
	if err := f.out.Screen(f.ctx, a.Key, 0); err != nil {
		t.Fatal(err)
	}
	if sent := f.tg.Sent(); len(sent) != 1 || sent[0].Text != "key "+testKey {
		t.Fatalf("Sent = %+v", sent)
	}
}
