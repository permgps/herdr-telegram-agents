package testkit_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

func TestFakeTelegramRecordsAndFails(t *testing.T) {
	tg := testkit.NewFakeTelegram(nil)
	ctx := context.Background()
	topic, err := tg.CreateTopic(ctx, "⚙️ a", domain.StatusWorking)
	if err != nil || topic.ThreadID != 101 {
		t.Fatalf("CreateTopic = %+v, %v", topic, err)
	}
	name := "💤 a"
	if err := tg.EditTopic(ctx, topic.ThreadID, domain.TopicPatch{Name: &name}); err != nil {
		t.Fatal(err)
	}
	if err := tg.EditTopic(ctx, topic.ThreadID, domain.TopicPatch{}); err != nil {
		t.Fatal(err)
	}
	tg.FailNext("close", domain.ErrTopicGone)
	if err := tg.CloseTopic(ctx, topic.ThreadID); !errors.Is(err, domain.ErrTopicGone) {
		t.Fatalf("CloseTopic = %v, want ErrTopicGone", err)
	}
	if err := tg.CloseTopic(ctx, topic.ThreadID); err != nil {
		t.Fatalf("second CloseTopic = %v", err)
	}
	want := []string{"create:⚙️ a:working", "edit:101:name=💤 a", "close:101", "close:101"}
	got := tg.Calls()
	if len(got) != len(want) {
		t.Fatalf("Calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Calls[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if got, _ := tg.Topic(101); got.Name != "💤 a" || !got.Closed {
		t.Fatalf("topic state = %+v", got)
	}
}

func TestFakeClockFiresInOrder(t *testing.T) {
	c := testkit.NewFakeClock(time.Unix(0, 0))
	late := c.After(3 * time.Second)
	early := c.After(time.Second)
	if n := c.Advance(500 * time.Millisecond); n != 0 {
		t.Fatalf("fired %d timers early", n)
	}
	if n := c.Advance(time.Second); n != 1 {
		t.Fatalf("fired %d, want 1", n)
	}
	select {
	case <-early:
	default:
		t.Fatal("early timer did not fire")
	}
	select {
	case <-late:
		t.Fatal("late timer fired too soon")
	default:
	}
	if n := c.Advance(2 * time.Second); n != 1 || c.Pending() != 0 {
		t.Fatalf("fired %d, pending %d", n, c.Pending())
	}
	if got := c.Now(); !got.Equal(time.Unix(3, 500_000_000)) {
		t.Fatalf("Now = %v", got)
	}
}

func TestFakeHerdrSnapshotsAndWatches(t *testing.T) {
	h := testkit.NewFakeHerdr(nil)
	h.SetAgents([]domain.Agent{{Key: domain.Key{PaneID: "p", TerminalID: "t"}}})
	agents, err := h.ListAgents(context.Background())
	if err != nil || len(agents) != 1 || h.ListCalls() != 1 {
		t.Fatalf("ListAgents = %v, %v (calls %d)", agents, err, h.ListCalls())
	}
	h.FailList(domain.ErrDisconnected)
	if _, err := h.ListAgents(context.Background()); !errors.Is(err, domain.ErrDisconnected) {
		t.Fatalf("ListAgents after FailList = %v", err)
	}
	_ = h.WatchPanes(context.Background(), []string{"p"})
	if w := h.WatchCalls(); len(w) != 1 || w[0][0] != "p" {
		t.Fatalf("WatchCalls = %v", w)
	}
	h.Push(domain.HerdrEvent{Kind: domain.PaneClosed, PaneID: "p"})
	ev := (<-h.Events()).(domain.HerdrEvent)
	if ev.Kind != domain.PaneClosed {
		t.Fatalf("event = %+v", ev)
	}
}

func TestMemMappingStoreCopies(t *testing.T) {
	s := testkit.NewMemMappingStore()
	m := domain.NewMapping(-1)
	a := domain.Agent{Key: domain.Key{PaneID: "p", TerminalID: "t"}, Name: "a"}
	m.Link(a.Key, domain.Topic{ThreadID: 1}, a, time.Unix(0, 0))
	if err := s.Save(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	m.Forget(a.Key)
	loaded, _ := s.Load(context.Background())
	if _, ok := loaded.TopicFor(a.Key); !ok || s.SaveCount() != 1 {
		t.Fatalf("saved copy shared state with the original (saves=%d)", s.SaveCount())
	}
}

func TestFakeProcessLifecycle(t *testing.T) {
	p := testkit.NewFakeProcess(nil)
	if _, err := p.Read(); !errors.Is(err, domain.ErrNotRunning) {
		t.Fatalf("Read on empty = %v", err)
	}
	pid, err := p.Spawn(context.Background(), []string{"daemon"})
	if err != nil || !p.Alive(pid) || p.Held() != pid {
		t.Fatalf("Spawn: pid=%d err=%v %s", pid, err, p)
	}
	if err := p.Acquire(42); !errors.Is(err, domain.ErrAlreadyRunning) {
		t.Fatalf("Acquire while held = %v", err)
	}
	if err := p.Stop(pid); err != nil || p.Alive(pid) || p.Held() != 0 {
		t.Fatalf("Stop: err=%v %s", err, p)
	}
	p.IgnoreStop(true)
	pid, _ = p.Spawn(context.Background(), nil)
	if err := p.Stop(pid); err != nil || !p.Alive(pid) {
		t.Fatalf("Stop with IgnoreStop: err=%v alive=%v", err, p.Alive(pid))
	}
	if err := p.Kill(pid); err != nil || p.Alive(pid) {
		t.Fatalf("Kill: err=%v alive=%v", err, p.Alive(pid))
	}
	p.SetUnsupported(true)
	if err := p.Resync(pid); !errors.Is(err, domain.ErrUnsupportedPlatform) {
		t.Fatalf("Resync unsupported = %v", err)
	}
}

func TestFakeTelegramSendAndReact(t *testing.T) {
	tg := testkit.NewFakeTelegram(nil)
	ctx := context.Background()
	topic, _ := tg.CreateTopic(ctx, "a", domain.StatusWorking)
	if err := tg.Send(ctx, domain.Outgoing{ThreadID: 0, Text: "hello general"}); err != nil {
		t.Fatalf("Send to General = %v", err)
	}
	if err := tg.Send(ctx, domain.Outgoing{ThreadID: topic.ThreadID, Text: "screen", Code: true, ReplyTo: 9, Notify: true}); err != nil {
		t.Fatal(err)
	}
	if err := tg.Send(ctx, domain.Outgoing{ThreadID: 999, Text: "nowhere"}); !errors.Is(err, domain.ErrTopicGone) {
		t.Fatalf("Send to unknown topic = %v, want ErrTopicGone", err)
	}
	tg.FailNext("react", domain.ErrForbidden)
	if err := tg.React(ctx, topic.ThreadID, 9, "👍"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("React = %v, want ErrForbidden", err)
	}
	if err := tg.React(ctx, topic.ThreadID, 9, "👍"); err != nil {
		t.Fatalf("second React = %v", err)
	}
	want := []string{
		"create:a:working",
		"send:0:hello general",
		"send:101:screen:reply=9:notify",
		"send:999:nowhere",
		"react:101:9:👍",
		"react:101:9:👍",
	}
	got := tg.Calls()
	if len(got) != len(want) {
		t.Fatalf("Calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Calls[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	sent := tg.Sent()
	if len(sent) != 2 || sent[1].ReplyTo != 9 || !sent[1].Notify || !sent[1].Code {
		t.Fatalf("Sent = %+v", sent)
	}
}

func TestFakeHerdrRecordsCallsAndFails(t *testing.T) {
	h := testkit.NewFakeHerdr(nil)
	ctx := context.Background()
	h.SetScreen("p1", "line1\nline2")
	if sc, err := h.ReadScreen(ctx, "p1", domain.ScreenDetection, 25); err != nil || sc.Text != "line1\nline2" {
		t.Fatalf("ReadScreen = %+v, %v", sc, err)
	}
	if sc, _ := h.ReadScreen(ctx, "p2", domain.ScreenVisible, 0); sc.Text != "screen" {
		t.Fatalf("unscripted screen = %q", sc.Text)
	}
	h.FailNext("read", domain.ErrAgentGone)
	if _, err := h.ReadScreen(ctx, "p1", domain.ScreenVisible, 0); !errors.Is(err, domain.ErrAgentGone) {
		t.Fatalf("ReadScreen after FailNext = %v", err)
	}
	if _, err := h.ReadScreen(ctx, "p1", domain.ScreenVisible, 0); err != nil {
		t.Fatalf("failure fired twice: %v", err)
	}
	reads := h.Reads()
	if len(reads) != 4 || reads[0] != (testkit.ReadCall{Target: "p1", Source: domain.ScreenDetection, Lines: 25}) {
		t.Fatalf("Reads = %+v", reads)
	}

	h.FailNext("prompt", domain.ErrDisconnected)
	if err := h.Prompt(ctx, "p1", "hi"); !errors.Is(err, domain.ErrDisconnected) {
		t.Fatalf("Prompt = %v", err)
	}
	if err := h.SendKeys(ctx, "p1", []string{"esc"}); err != nil {
		t.Fatal(err)
	}
	if err := h.Focus(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	name := "fixer"
	if err := h.Rename(ctx, "p1", &name); err != nil {
		t.Fatal(err)
	}
	h.FailNext("rename", domain.ErrAgentGone)
	if err := h.Rename(ctx, "p1", nil); !errors.Is(err, domain.ErrAgentGone) {
		t.Fatalf("Rename = %v", err)
	}
	if p := h.Prompts(); len(p) != 1 || p[0] != "p1: hi" {
		t.Fatalf("Prompts = %v", p)
	}
	if k := h.Keys(); len(k) != 1 || k[0].Target != "p1" || len(k[0].Keys) != 1 || k[0].Keys[0] != "esc" {
		t.Fatalf("Keys = %+v", k)
	}
	if f := h.Focused(); len(f) != 1 || f[0] != "p1" {
		t.Fatalf("Focused = %v", f)
	}
	r := h.Renames()
	if len(r) != 2 || *r[0].Name != "fixer" || r[1].Name != nil {
		t.Fatalf("Renames = %+v", r)
	}
}

func TestFakeTelegramSendDocument(t *testing.T) {
	tg := testkit.NewFakeTelegram(nil)
	ctx := context.Background()
	topic, _ := tg.CreateTopic(ctx, "a", domain.StatusWorking)
	doc := domain.Document{ThreadID: topic.ThreadID, Name: "screen.txt", Data: []byte("abc"), Caption: "3 lines", ReplyTo: 4}
	if err := tg.SendDocument(ctx, doc); err != nil {
		t.Fatalf("SendDocument = %v", err)
	}
	if err := tg.SendDocument(ctx, domain.Document{ThreadID: 999, Name: "x.txt"}); !errors.Is(err, domain.ErrTopicGone) {
		t.Fatalf("SendDocument to unknown topic = %v, want ErrTopicGone", err)
	}
	tg.FailNext("document", domain.ErrForbidden)
	if err := tg.SendDocument(ctx, domain.Document{Name: "general.txt"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("SendDocument after FailNext = %v", err)
	}
	if got := tg.Calls(); len(got) != 4 || got[1] != "document:101:screen.txt:3:reply=4" {
		t.Fatalf("Calls = %v", got)
	}
	if docs := tg.Documents(); len(docs) != 1 || docs[0].Caption != "3 lines" || string(docs[0].Data) != "abc" {
		t.Fatalf("Documents = %+v", docs)
	}
	tg.Reset()
	if len(tg.Documents()) != 0 {
		t.Fatal("Reset must drop documents")
	}
}

func TestFakeHerdrScreenRevision(t *testing.T) {
	h := testkit.NewFakeHerdr(nil)
	ctx := context.Background()
	h.SetScreen("p1", "one")
	if sc, _ := h.ReadScreen(ctx, "p1", domain.ScreenRecent, 400); sc.Revision != 0 {
		t.Fatalf("SetScreen revision = %d, want 0", sc.Revision)
	}
	h.SetScreenAt("p1", "two", 7)
	if sc, _ := h.ReadScreen(ctx, "p1", domain.ScreenRecent, 400); sc.Text != "two" || sc.Revision != 7 {
		t.Fatalf("SetScreenAt read = %+v", sc)
	}
}
