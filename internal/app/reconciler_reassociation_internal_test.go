package app

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

func reassociationTestKey(pane, terminal, value string) domain.Key {
	digest := (domain.SessionTuple{Source: "test", Agent: "codex", Kind: "id", Value: value}).Digest()
	return domain.Key{PaneID: pane, TerminalID: terminal, SessionDigest: digest}
}

func TestReassociationPublishesNewThreadRouteBeforeInbound(t *testing.T) {
	ctx := context.Background()
	tg := testkit.NewFakeTelegram(nil)
	herdr := testkit.NewFakeHerdr(nil)
	store := testkit.NewMemMappingStore()
	at := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	clock := testkit.NewFakeClock(at)
	mapping := domain.NewMapping(-1)
	oldKey := domain.Key{
		PaneID: "p1", TerminalID: "term-old",
		SessionDigest: (domain.SessionTuple{Source: "test", Agent: "codex", Kind: "id", Value: "session-1"}).Digest(),
	}
	oldAgent := domain.Agent{Key: oldKey, Name: "reviewer", Kind: "codex", Cwd: "/work/repo", Status: domain.StatusWorking}
	mapping.Link(oldKey, domain.Topic{ThreadID: 101, Name: "reviewer"}, oldAgent, at)
	rec := NewReconciler(tg, herdr, store, mapping, nil, clock, nil)

	newAgent := oldAgent
	newAgent.Key.TerminalID = "term-new"
	var routedKey domain.Key
	lookup := func(key domain.Key) (domain.Agent, bool) {
		routedKey = key
		return newAgent, key == newAgent.Key
	}
	live := func() []domain.Agent { return []domain.Agent{newAgent} }
	out := newOutbound(herdr, tg, -1, nil, rec.topics(), lookup, live, nil, nil, nil, clock, nil)
	in := newInbound(herdr, tg, rec.topics(), lookup, live, out, nil, Services{}, domain.Config{}, clock, nil)

	if err := rec.Reconcile(ctx, []domain.Agent{newAgent}); err != nil {
		t.Fatal(err)
	}
	if err := in.HandleTopic(ctx, domain.TopicMessage{ThreadID: 101, MessageID: 7, FromID: 1, Text: "/status"}); err != nil {
		t.Fatal(err)
	}
	if routedKey != newAgent.Key {
		t.Fatalf("inbound route used key %s, want reassociated key %s", routedKey.String(), newAgent.Key.String())
	}
	if got := tg.Calls(); len(got) != 1 || !strings.HasPrefix(got[0], "send:101:⚡ working · reviewer · pane p1:reply=") {
		t.Fatalf("topic operations and status reply = %v", got)
	}
	saved := store.Saved()
	if _, ok := saved.TopicFor(newAgent.Key); !ok {
		t.Fatal("new thread route was published without a durable mapping")
	}
}

func TestSameSessionRestartKeepsBridgeAndDashboardState(t *testing.T) {
	ctx := context.Background()
	tg := testkit.NewFakeTelegram(nil)
	herdr := testkit.NewFakeHerdr(nil)
	store := testkit.NewMemMappingStore()
	at := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	clock := testkit.NewFakeClock(at)
	oldAgent := domain.Agent{
		Key: reassociationTestKey("p1", "term-old", "session-2"), Name: "reviewer", Kind: "codex",
		Cwd: "/work/repo", Status: domain.StatusBlocked,
	}
	topic, err := tg.CreateTopic(ctx, "reviewer", domain.StatusBlocked)
	if err != nil {
		t.Fatal(err)
	}
	mapping := domain.NewMapping(-1001234567890)
	mapping.Link(oldAgent.Key, topic, oldAgent, at)
	rec := NewReconciler(tg, herdr, store, mapping, nil, clock, nil)
	registry := NewRegistry(herdr, clock, nil)
	herdr.SetAgents([]domain.Agent{oldAgent})
	initial, err := registry.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.Reconcile(ctx, registry.Live()); err != nil {
		t.Fatal(err)
	}
	capture := NewCapture(herdr, registry.Live, clock, nil)
	opts := NewOptions(domain.DefaultOptions(), nil, nil, nil)
	dash := NewDashboard(tg, rec, opts, rec.topics(), registry.Live, nil, -1001234567890, clock, nil)
	out := newOutbound(herdr, tg, -1001234567890, nil, rec.topics(), registry.Agent, registry.Live, capture, opts, nil, clock, nil)
	in := newInbound(herdr, tg, rec.topics(), registry.Agent, registry.Live, out, opts, Services{}, domain.Config{}, clock, nil)
	bridge := &Bridge{out: out, in: in, log: slog.New(slog.DiscardHandler)}
	newAgent := oldAgent
	newAgent.Key.TerminalID = "term-new"
	for _, ev := range initial {
		capture.Observe(ev)
		dash.Observe(ev)
		bridge.handle(ctx, ev)
	}
	dash.mu.Lock()
	dash.since[oldAgent.Key] = at.Add(-2 * time.Hour)
	dash.mu.Unlock()
	capture.mu.Lock()
	history := capture.history(oldAgent.Key)
	history.Append([]string{"previous output"})
	capture.history(newAgent.Key).Append([]string{"previous output", "current output"})
	capture.mu.Unlock()

	keyboardID, err := tg.Send(ctx, domain.Outgoing{ThreadID: topic.ThreadID, Text: "Question", Buttons: []domain.Button{{Text: "1️⃣ Yes", Data: "1"}}})
	if err != nil {
		t.Fatal(err)
	}
	closeID, err := tg.Send(ctx, domain.Outgoing{ThreadID: topic.ThreadID, Text: "Close this agent?", Buttons: []domain.Button{{Text: "No", Data: closeNo}}})
	if err != nil {
		t.Fatal(err)
	}
	out.keyboards[oldAgent.Key] = keyboard{messageID: keyboardID, choices: []domain.Choice{{Number: 1, Label: "Yes"}}}
	out.lastPosted[oldAgent.Key] = hashText("Question")
	in.closing[oldAgent.Key] = closeID
	in.pending[oldAgent.Key] = followUp{threadID: topic.ThreadID, messageID: 9, word: "/help"}
	in.deb.ScheduleAfter(oldAgent.Key, time.Minute)
	tg.Reset()

	herdr.SetAgents([]domain.Agent{newAgent})
	events, err := registry.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantEvents := []string{"appeared:" + newAgent.Key.String(), "gone:" + oldAgent.Key.String()}
	gotEvents := make([]string, len(events))
	for i, ev := range events {
		gotEvents[i] = string(ev.Kind) + ":" + ev.Agent.Key.String()
	}
	if len(gotEvents) != len(wantEvents) || gotEvents[0] != wantEvents[0] || gotEvents[1] != wantEvents[1] {
		t.Fatalf("restart events = %v, want %v", gotEvents, wantEvents)
	}
	for _, ev := range events {
		if err := rec.Handle(ctx, ev); err != nil {
			t.Fatal(err)
		}
		if from, ok := rec.TakeReassociatedFrom(ev.Agent.Key); ok {
			ev.ReassociatedFrom = &from
		}
		capture.Observe(ev)
		dash.Observe(ev)
		bridge.handle(ctx, ev)
	}
	if got := tg.Calls(); len(got) != 0 {
		t.Fatalf("current topic generated telegram calls during restart: %v", got)
	}

	if key, ok := rec.topics().KeyForThread(topic.ThreadID); !ok || key != newAgent.Key {
		t.Fatalf("thread route = %s, ok=%v; want %s", key.String(), ok, newAgent.Key.String())
	}
	if got, ok := rec.Mapping().TopicFor(newAgent.Key); !ok || got.ThreadID != topic.ThreadID {
		t.Fatalf("reassociated mapping = %+v, ok=%v", got, ok)
	}
	if out.lastPosted[newAgent.Key] != hashText("Question") || out.keyboards[newAgent.Key].messageID != keyboardID {
		t.Fatal("screen hash or keyboard state did not follow the retained topic")
	}
	if _, ok := out.keyboards[oldAgent.Key]; ok {
		t.Fatal("old key retained duplicate keyboard state")
	}
	if in.closing[newAgent.Key] != closeID || in.pending[newAgent.Key].messageID != 9 || in.deb.Pending() != 1 {
		t.Fatal("pending inbound callback or command state did not follow the retained topic")
	}
	if _, ok := in.pending[oldAgent.Key]; ok {
		t.Fatal("old key retained duplicate pending command state")
	}
	capture.mu.Lock()
	if capture.hist[newAgent.Key] != history || capture.hist[oldAgent.Key] != nil {
		capture.mu.Unlock()
		t.Fatal("screen history did not follow the retained topic")
	}
	if got := history.Lines(); len(got) != 2 || got[0] != "previous output" || got[1] != "current output" {
		capture.mu.Unlock()
		t.Fatalf("merged screen history = %v", got)
	}
	capture.mu.Unlock()
	dash.mu.Lock()
	if !dash.since[newAgent.Key].Equal(at.Add(-2*time.Hour)) || dash.since[oldAgent.Key] != (time.Time{}) {
		dash.mu.Unlock()
		t.Fatal("dashboard status duration did not follow the retained topic")
	}
	dash.mu.Unlock()
	body := dash.view().render()
	if !strings.Contains(body, "/101") {
		t.Fatalf("dashboard link did not retain thread 101: %s", body)
	}

	if err := in.PressClose(ctx, domain.ButtonPressed{CallbackID: "close-callback", ThreadID: topic.ThreadID, MessageID: closeID, FromID: 1, Data: closeNo}); err != nil {
		t.Fatal(err)
	}
	if got := tg.Text(closeID); got != closeKept {
		t.Fatalf("close callback routed to retained topic text %q, want %q", got, closeKept)
	}
	herdr.SetScreen("p1", "Question")
	if err := out.Press(ctx, domain.ButtonPressed{CallbackID: "dialog-callback", ThreadID: topic.ThreadID, MessageID: keyboardID, FromID: 1, Data: "1"}); err != nil {
		t.Fatal(err)
	}
	if got := herdr.Keys(); len(got) != 1 || got[0].Target != "p1" || len(got[0].Keys) != 1 || got[0].Keys[0] != "1" {
		t.Fatalf("retained keyboard callback keys = %+v", got)
	}
	if err := out.fire(ctx, newAgent.Key, false, false); err != nil {
		t.Fatal(err)
	}
	for _, call := range tg.Calls() {
		if strings.HasPrefix(call, "send:101:") {
			t.Fatalf("restart duplicated the existing screen post: %v", tg.Calls())
		}
	}
	tg.Reset()
	updated := newAgent
	updated.Status = domain.StatusIdle
	if err := rec.Handle(ctx, AgentEvent{Kind: AgentChanged, Agent: updated}); err != nil {
		t.Fatal(err)
	}
	if err := rec.Fire(ctx, updated.Key); err != nil {
		t.Fatal(err)
	}
	if got := tg.Calls(); len(got) != 1 || got[0] != "edit:101:status=idle" {
		t.Fatalf("status update after restart = %v", got)
	}
	in.deb.Cancel(newAgent.Key)
	dash.deb.Cancel(dashboardKey)
}
