package app_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/app"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

type privateFixture struct {
	s      *app.Sharing
	p      *app.PrivateControl
	store  *testkit.MemSharingStore
	tg     *testkit.FakeTelegram
	h      *testkit.FakeHerdr
	g      domain.ShareGrant
	origin domain.ShareOrigin
	now    time.Time
	// agent is what f.s.Agent reports; tests move its status and
	// StateChangeSeq through setAgent.
	agent *domain.Agent
}

// setAgent moves the shared agent to status with a new StateChangeSeq and
// returns it.
func (f *privateFixture) setAgent(status domain.Status, seq int64) domain.Agent {
	f.agent.Status, f.agent.StateChangeSeq = status, seq
	return *f.agent
}

func newPrivateFixture(t *testing.T, role domain.ShareRole) *privateFixture {
	t.Helper()
	ctx := context.Background()
	f := &privateFixture{store: testkit.NewMemSharingStore(), tg: testkit.NewFakeTelegram(nil), h: testkit.NewFakeHerdr(nil), now: time.Unix(1000, 0)}
	f.s = app.NewSharing(ctx, f.store, nil)
	f.s.BotID = 42
	f.s.Now = func() time.Time { return f.now }
	a := domain.Agent{Key: domain.Key{PaneID: "p", TerminalID: "t", SessionDigest: "digest"}, Name: "agent", Status: domain.StatusIdle, Kind: "claude"}
	f.agent = &a
	f.s.Agent = func(k domain.Key) (domain.Agent, bool) { return *f.agent, k == f.agent.Key }
	_, err := f.s.Register(ctx, 10, 10, "name", "", f.now)
	if err != nil {
		t.Fatal(err)
	}
	f.g, err = f.s.Grant(ctx, app.GrantRequest{RecipientID: 10, Key: a.Key, Role: role, Bot: domain.BotIdentity{ID: 42, HasTopicsEnabled: true}}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	r := &app.PrivateReconciler{Sharing: f.s, Telegram: f.tg, Agent: f.s.Agent, Now: f.s.Now}
	if err := r.Grant(ctx, f.g); err != nil {
		t.Fatal(err)
	}
	st, _ := f.s.Snapshot()
	f.g = st.Grants[f.g.ID]
	f.origin, _ = f.s.Origin(f.g.ID)
	f.p = &app.PrivateControl{Sharing: f.s, Telegram: f.tg, Transport: f.tg, Herdr: f.h, Agent: f.s.Agent, Now: f.s.Now}
	return f
}

func TestPrivateReadCannotControl(t *testing.T) {
	f := newPrivateFixture(t, domain.ShareRead)
	for _, text := range []string{"prompt", "/keys enter", "/usage", "/new", "/options", "/close", "/unknown"} {
		if err := f.p.Handle(context.Background(), domain.PrivateMessage{Contact: domain.PrivateContact{ActorID: 10}, Address: f.origin.Address, MessageID: 900, Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.h.Prompts())+len(f.h.Keys())+len(f.h.Closed()) != 0 {
		t.Fatal("reader invoked Herdr")
	}
}

func TestPrivateRevocationCancelsDispatchAndSurvivesSaveFailure(t *testing.T) {
	f := newPrivateFixture(t, domain.ShareControl)
	ctx := context.Background()
	callCtx, done, d := f.s.Begin(ctx, f.origin, domain.SharePrompt)
	if !d.Allowed {
		t.Fatal(d)
	}
	defer done()
	f.store.Fail(errors.New("disk full"))
	if err := f.s.ChangeState(ctx, f.g.ID, f.g.Revision, domain.GrantRevoked, f.now); err == nil {
		t.Fatal("durable revoke falsely succeeded")
	}
	if callCtx.Err() == nil {
		t.Fatal("running dispatch not cancelled")
	}
	if _, _, d := f.s.Begin(ctx, f.origin, domain.SharePrompt); d.Allowed {
		t.Fatal("failed durable revoke kept local access")
	}
	f.store.Fail(nil)
	if err := f.s.Flush(ctx, f.now, true); err != nil {
		t.Fatal(err)
	}
	st, _ := f.store.Load(ctx)
	if st.Grants[f.g.ID].State != domain.GrantRevoked {
		t.Fatal("revocation retry not durable")
	}
}

func TestPrivateUnknownSlashNeverBecomesPrompt(t *testing.T) {
	f := newPrivateFixture(t, domain.ShareControl)
	if err := f.p.Handle(context.Background(), domain.PrivateMessage{Contact: domain.PrivateContact{ActorID: 10}, Address: f.origin.Address, MessageID: 900, Text: "/invented"}); err != nil {
		t.Fatal(err)
	}
	if len(f.h.Prompts()) != 0 {
		t.Fatal("unknown slash forwarded")
	}
}

func TestPrivateAsyncCompletionAfterRevocation(t *testing.T) {
	for _, operation := range []string{"attachment", "git"} {
		t.Run(operation, func(t *testing.T) {
			f := newPrivateFixture(t, domain.ShareControl)
			ctx := context.Background()
			f.p.Inbox = testkit.NewFakeInbox("/inbox")
			f.tg.SetFile("file", []byte("private attachment"))
			git := testkit.NewFakeGit()
			git.SetResult(domain.GitResult{Output: "private repository contents"})
			f.p.Git = git
			var work func(context.Context) func(context.Context) error
			f.p.Async = func(fn func(context.Context) func(context.Context) error) bool { work = fn; return true }
			event := domain.PrivateMessage{Contact: domain.PrivateContact{ActorID: 10}, Address: f.origin.Address, MessageID: 99, Text: "/git status"}
			if operation == "attachment" {
				event.Attachment = &domain.TopicAttachment{FileID: "file", Name: "same.txt", Kind: "document"}
			}
			if err := f.p.Handle(ctx, event); err != nil {
				t.Fatal(err)
			}
			if work == nil {
				t.Fatal("async operation not scheduled")
			}
			result := work(ctx)
			if result == nil {
				t.Fatal("completion not produced")
			}
			before := len(f.tg.Destination(10).Sent())
			if err := f.s.ChangeState(ctx, f.g.ID, f.g.Revision, domain.GrantRevoked, f.now); err != nil {
				t.Fatal(err)
			}
			_ = result(ctx)
			if len(f.h.Prompts()) != 0 || len(f.tg.Destination(10).Sent()) != before {
				t.Fatal("revoked async completion dispatched")
			}
		})
	}
}

func TestExpiredGrantPersistsDenial(t *testing.T) {
	f := newPrivateFixture(t, domain.ShareControl)
	ctx := context.Background()
	g, err := f.s.Grant(ctx, app.GrantRequest{RecipientID: 10, Key: f.g.Key, Role: domain.ShareControl, ExpiresAt: f.now.Add(time.Minute), Bot: domain.BotIdentity{ID: 42, HasTopicsEnabled: true}}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Minute)
	o, _ := f.s.Origin(g.ID)
	if _, _, decision := f.s.Begin(ctx, o, domain.SharePrompt); decision.Allowed {
		t.Fatal("expiry depends on sweep")
	}
	if err := f.s.Expire(ctx, f.now); err != nil {
		t.Fatal(err)
	}
	persisted, err := f.store.Load(ctx)
	if err != nil || persisted.Grants[g.ID].State != domain.GrantExpired {
		t.Fatal("expiry not durable")
	}
}

func TestStaleGrantConfirmationCannotUndoRevoke(t *testing.T) {
	f := newPrivateFixture(t, domain.ShareControl)
	ctx := context.Background()
	revision := f.g.Revision
	if err := f.s.ChangeState(ctx, f.g.ID, revision, domain.GrantRevoked, f.now); err != nil {
		t.Fatal(err)
	}
	_, err := f.s.Grant(ctx, app.GrantRequest{ExpectedRevision: &revision, RecipientID: 10, Key: f.g.Key, Role: domain.ShareControl, Bot: domain.BotIdentity{ID: 42, HasTopicsEnabled: true}}, f.now)
	if err == nil {
		t.Fatal("old confirmation revived revoked grant")
	}
}

func TestAlbumRevokedBeforeSettlement(t *testing.T) {
	f := newPrivateFixture(t, domain.ShareControl)
	ctx := context.Background()
	inbox := testkit.NewFakeInbox("/inbox")
	f.p.Inbox = inbox
	f.p.Async = func(fn func(context.Context) func(context.Context) error) bool {
		if result := fn(ctx); result != nil {
			_ = result(ctx)
		}
		return true
	}
	f.tg.SetFile("a", []byte("private"))
	for range 2 {
		if err := f.p.Handle(ctx, domain.PrivateMessage{Contact: domain.PrivateContact{ActorID: 10}, Address: f.origin.Address, Attachment: &domain.TopicAttachment{FileID: "a", Name: "same.txt", Kind: "document", GroupID: "album"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.ChangeState(ctx, f.g.ID, f.g.Revision, domain.GrantRevoked, f.now); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(2 * time.Second)
	if err := f.p.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(inbox.Saved()) != 0 || len(f.h.Prompts()) != 0 {
		t.Fatal("revoked album reached inbox or agent")
	}
}

// inboxControl wires an inbox and a synchronous transfer runner into a
// Control fixture and sets the owner's Inbox options explicitly.
func inboxControl(f *privateFixture, enabled bool, max int64) *testkit.FakeInbox {
	ctx := context.Background()
	inbox := testkit.NewFakeInbox("/inbox")
	f.p.Inbox = inbox
	f.p.InboxEnabled = func() bool { return enabled }
	f.p.InboxMaxBytes = func() int64 { return max }
	f.p.Async = func(fn func(context.Context) func(context.Context) error) bool {
		if result := fn(ctx); result != nil {
			_ = result(ctx)
		}
		return true
	}
	return inbox
}

// lastPrivate returns the text of the last message sent to recipient 10.
func (f *privateFixture) lastPrivate(t *testing.T) string {
	t.Helper()
	sent := f.tg.Destination(10).Sent()
	if len(sent) == 0 {
		t.Fatal("nothing sent to the recipient")
	}
	return sent[len(sent)-1].Text
}

// downloads returns the recorded download calls.
func (f *privateFixture) downloads() []string {
	var calls []string
	for _, c := range f.tg.Calls() {
		if strings.HasPrefix(c, "download:") {
			calls = append(calls, c)
		}
	}
	return calls
}

func (f *privateFixture) attach(t *testing.T, at domain.TopicAttachment) {
	t.Helper()
	if err := f.p.Handle(context.Background(), domain.PrivateMessage{Contact: domain.PrivateContact{ActorID: 10}, Address: f.origin.Address, MessageID: 99, Attachment: &at}); err != nil {
		t.Fatal(err)
	}
}

// TestPrivateAttachmentFollowsInboxOptions: a recipient's file obeys the
// owner's Inbox switch and per-file limit before any download, and the
// download itself is capped at the option.
func TestPrivateAttachmentFollowsInboxOptions(t *testing.T) {
	t.Run("inbox_off", func(t *testing.T) {
		f := newPrivateFixture(t, domain.ShareControl)
		inbox := inboxControl(f, false, 20<<20)
		f.tg.SetFile("file", []byte("x"))
		f.attach(t, domain.TopicAttachment{FileID: "file", Name: "a.txt", Kind: "document", Size: 1})
		if d := f.downloads(); len(d) != 0 || len(inbox.Saved()) != 0 {
			t.Fatalf("downloaded with the inbox off: %v", d)
		}
		if got := f.lastPrivate(t); got != "⚠️ inbox is off (/options → Inbox)" {
			t.Fatalf("refusal = %q", got)
		}
	})
	t.Run("too_big", func(t *testing.T) {
		f := newPrivateFixture(t, domain.ShareControl)
		inboxControl(f, true, 1<<20)
		f.tg.SetFile("file", make([]byte, 10))
		f.attach(t, domain.TopicAttachment{FileID: "file", Name: "a.bin", Kind: "document", Size: 2 << 20})
		if d := f.downloads(); len(d) != 0 {
			t.Fatalf("oversized file downloaded: %v", d)
		}
		if got := f.lastPrivate(t); got != "⚠️ file too big: 2.0 MB > 1.0 MB" {
			t.Fatalf("refusal = %q", got)
		}
	})
	t.Run("under_limit", func(t *testing.T) {
		f := newPrivateFixture(t, domain.ShareControl)
		inbox := inboxControl(f, true, 1<<20)
		f.tg.SetFile("file", []byte("private attachment"))
		f.attach(t, domain.TopicAttachment{FileID: "file", Name: "a.txt", Kind: "document", Size: 18})
		if d := f.downloads(); len(d) != 1 || d[0] != "download:file:1048576" {
			t.Fatalf("downloads = %v", d)
		}
		saved := inbox.Saved()
		if len(saved) != 1 || !strings.HasPrefix(saved[0].Name, "shared-10-") {
			t.Fatalf("saved = %+v", saved)
		}
		if p := f.h.Prompts(); len(p) != 1 {
			t.Fatalf("prompts = %v", p)
		}
	})
	t.Run("album_too_big", func(t *testing.T) {
		f := newPrivateFixture(t, domain.ShareControl)
		inbox := inboxControl(f, true, 1<<20)
		f.tg.SetFile("part", make([]byte, 800<<10))
		for range 3 {
			f.attach(t, domain.TopicAttachment{FileID: "part", Name: "a.bin", Kind: "document", GroupID: "album"})
		}
		f.now = f.now.Add(2 * time.Second)
		if err := f.p.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := f.lastPrivate(t); got != "Album exceeds 2.0 MB." {
			t.Fatalf("refusal = %q", got)
		}
		if len(f.h.Prompts()) != 0 || len(inbox.Saved()) != 2 {
			t.Fatalf("album over twice the limit: prompts=%v saved=%d", f.h.Prompts(), len(inbox.Saved()))
		}
	})
	// The recipients' share of the inbox can be smaller than Largest file
	// (Inbox size under four times it): such a file is refused before the
	// download, not fetched and then refused as "inbox full".
	t.Run("over_shared_quota", func(t *testing.T) {
		f := newPrivateFixture(t, domain.ShareControl)
		inbox := inboxControl(f, true, 5<<20)
		f.p.InboxSharedMax = func() int64 { return 1 << 20 }
		f.tg.SetFile("file", make([]byte, 10))
		f.attach(t, domain.TopicAttachment{FileID: "file", Name: "a.bin", Kind: "document", Size: 2 << 20})
		if d := f.downloads(); len(d) != 0 || len(inbox.Saved()) != 0 {
			t.Fatalf("file over the shared quota downloaded: %v", d)
		}
		if got := f.lastPrivate(t); got != "⚠️ file too big: 2.0 MB > 1.0 MB" {
			t.Fatalf("refusal = %q", got)
		}
	})
	// An album over the shared quota would evict its own first files to
	// save the last ones, and the prompt would name deleted paths.
	t.Run("album_over_shared_quota", func(t *testing.T) {
		f := newPrivateFixture(t, domain.ShareControl)
		inbox := inboxControl(f, true, 1<<20)
		f.p.InboxSharedMax = func() int64 { return 1 << 20 }
		f.tg.SetFile("part", make([]byte, 800<<10))
		for range 2 {
			f.attach(t, domain.TopicAttachment{FileID: "part", Name: "a.bin", Kind: "document", GroupID: "album"})
		}
		f.now = f.now.Add(2 * time.Second)
		if err := f.p.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := f.lastPrivate(t); got != "Album exceeds 1.0 MB." {
			t.Fatalf("refusal = %q", got)
		}
		if len(f.h.Prompts()) != 0 || len(inbox.Saved()) != 1 {
			t.Fatalf("album over the shared quota: prompts=%v saved=%d", f.h.Prompts(), len(inbox.Saved()))
		}
	})
	t.Run("shared_inbox_full", func(t *testing.T) {
		f := newPrivateFixture(t, domain.ShareControl)
		inbox := inboxControl(f, true, 1<<20)
		inbox.FailNext("save", fmt.Errorf("inbox: %w", domain.ErrFileTooBig))
		f.tg.SetFile("file", []byte("x"))
		f.attach(t, domain.TopicAttachment{FileID: "file", Name: "a.txt", Kind: "document", Size: 1})
		if got := f.lastPrivate(t); got != "Attachment not saved: the shared inbox is full." {
			t.Fatalf("reply = %q", got)
		}
	})
}

// A session change in the shared pane is a re-grant boundary: from the
// pane.updated that names the new session until the next snapshot (which
// may keep failing), the grant's key must not authorize input or reads,
// because every Herdr effect targets only the pane id.
func TestPrivateGrantDeniedAfterSessionSwapBeforeSnapshot(t *testing.T) {
	f := newPrivateFixture(t, domain.ShareControl)
	ctx := context.Background()
	reg := app.NewRegistry(f.h, testkit.NewFakeClock(f.now), nil)
	shared := domain.Agent{Key: f.g.Key, Name: "agent", Status: domain.StatusIdle, Kind: "claude"}
	f.h.SetAgents([]domain.Agent{shared})
	if _, err := reg.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	f.s.Agent = reg.Agent
	f.p.Agent = reg.Agent
	f.h.SetScreen("p", "new session secrets")
	f.h.FailList(domain.ErrDisconnected)
	swapped := shared
	swapped.Key.SessionDigest = "other-session"
	reg.Apply(domain.HerdrEvent{Kind: domain.PaneUpdated, PaneID: "p", Agent: &swapped})
	if _, err := reg.Snapshot(ctx); err == nil {
		t.Fatal("snapshot unexpectedly succeeded")
	}
	idle := domain.Agent{Kind: "claude", Status: domain.StatusIdle}
	reg.Apply(domain.HerdrEvent{Kind: domain.PaneAgentStatusChanged, PaneID: "p", Agent: &idle})
	for _, text := range []string{"hello new session", "/screen", "/keys enter"} {
		if err := f.p.Handle(ctx, domain.PrivateMessage{Contact: domain.PrivateContact{ActorID: 10}, Address: f.origin.Address, MessageID: 900, Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.h.Prompts())+len(f.h.Keys())+len(f.h.Reads()) != 0 {
		t.Fatalf("stale grant reached the new session: prompts=%v keys=%v reads=%v", f.h.Prompts(), f.h.Keys(), f.h.Reads())
	}
	if _, _, d := f.s.Begin(ctx, f.origin, domain.ShareScreen); d.Allowed {
		t.Fatal("stale grant still authorizes screen reads")
	}
}

// TestPrivateForwardRefusedWhileBusy: a recipient's forwarded command is
// refused while the agent works or waits at a dialog, as on the owner's
// path: typed into a dialog its Enter would confirm the highlighted option.
// /models exists on OpenCode only and is never typed into Claude Code.
func TestPrivateForwardRefusedWhileBusy(t *testing.T) {
	for _, tc := range []struct {
		status domain.Status
		text   string
	}{
		{domain.StatusBlocked, "/model"},
		{domain.StatusBlocked, "/compact"},
		{domain.StatusWorking, "/clear now"},
		{domain.StatusWorking, "/usage"},
		{domain.StatusIdle, "/models"},
	} {
		t.Run(string(tc.status)+tc.text, func(t *testing.T) {
			f := newPrivateFixture(t, domain.ShareControl)
			f.setAgent(tc.status, 1)
			if err := f.p.Handle(context.Background(), domain.PrivateMessage{Contact: domain.PrivateContact{ActorID: 10}, Address: f.origin.Address, MessageID: 900, Text: tc.text}); err != nil {
				t.Fatal(err)
			}
			if p := f.h.Prompts(); len(p) != 0 {
				t.Fatalf("%s typed while %s: %v", tc.text, tc.status, p)
			}
		})
	}
	f := newPrivateFixture(t, domain.ShareControl)
	if err := f.p.Handle(context.Background(), domain.PrivateMessage{Contact: domain.PrivateContact{ActorID: 10}, Address: f.origin.Address, MessageID: 901, Text: "/model"}); err != nil {
		t.Fatal(err)
	}
	if p := f.h.Prompts(); len(p) != 1 {
		t.Fatalf("/model on an idle Claude agent not typed: %v", p)
	}
}

// A Control recipient's answer to a blocked agent is typed into the dialog
// when Herdr refuses the prompt (agent_blocked, issue #37).
func TestPrivatePromptToBlockedAgentIsTyped(t *testing.T) {
	f := newPrivateFixture(t, domain.ShareControl)
	f.setAgent(domain.StatusBlocked, 1)
	f.h.FailNext("prompt", domain.ErrAgentBlocked)
	if err := f.p.Handle(context.Background(), domain.PrivateMessage{Contact: domain.PrivateContact{ActorID: 10}, Address: f.origin.Address, MessageID: 900, Text: "use the\nstaging db"}); err != nil {
		t.Fatal(err)
	}
	if texts := f.h.Texts(); len(texts) != 1 || texts[0] != (testkit.TextCall{Target: "p", Text: "use the staging db"}) {
		t.Fatalf("Texts = %+v", texts)
	}
	if keys := f.h.Keys(); len(keys) != 1 || keys[0].Target != "p" || len(keys[0].Keys) != 1 || keys[0].Keys[0] != domain.KeyEnter {
		t.Fatalf("Keys = %+v", keys)
	}
	for _, c := range f.tg.Calls() {
		if strings.Contains(c, "not retried") {
			t.Fatalf("typed delivery reported as a failure: %q", f.tg.Calls())
		}
	}
}

func TestPrivateForwardIntoBlockedAgentIsNotTyped(t *testing.T) {
	f := newPrivateFixture(t, domain.ShareControl)
	f.h.FailNext("prompt", domain.ErrAgentBlocked)
	_ = f.p.Handle(context.Background(), domain.PrivateMessage{Contact: domain.PrivateContact{ActorID: 10}, Address: f.origin.Address, MessageID: 900, Text: "/clear"})
	if texts := f.h.Texts(); len(texts) != 0 {
		t.Fatalf("forward typed into a dialog: %+v", texts)
	}
	if p := f.h.Prompts(); len(p) != 1 {
		t.Fatalf("Prompts = %v", p)
	}
}
