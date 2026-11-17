package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/app"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

func TestPrivateMirrorIntentAndLifecycle(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000, 0)
	store := testkit.NewMemSharingStore()
	sharing := app.NewSharing(ctx, store, nil)
	sharing.BotID = 42
	sharing.Now = func() time.Time { return now }
	a := domain.Agent{Key: domain.Key{PaneID: "p", TerminalID: "t", SessionDigest: "digest"}, Name: "Agent", Status: domain.StatusIdle}
	sharing.Agent = func(k domain.Key) (domain.Agent, bool) { return a, k == a.Key }
	if _, err := sharing.Register(ctx, 10, 10, "name", "", now); err != nil {
		t.Fatal(err)
	}
	g, err := sharing.Grant(ctx, app.GrantRequest{RecipientID: 10, Key: a.Key, Role: domain.ShareRead, Bot: domain.BotIdentity{ID: 42, HasTopicsEnabled: true}}, now)
	if err != nil {
		t.Fatal(err)
	}
	tg := testkit.NewFakeTelegram(nil)
	r := &app.PrivateReconciler{Sharing: sharing, Telegram: tg, Agent: sharing.Agent, Now: sharing.Now}
	if err := r.Grant(ctx, g); err != nil {
		t.Fatal(err)
	}
	st, _ := sharing.Snapshot()
	g = st.Grants[g.ID]
	thread := st.Mirrors[g.ID].Address.ThreadID
	if err := r.Grant(ctx, g); err != nil {
		t.Fatal(err)
	}
	creates := 0
	for _, call := range tg.Destination(10).Calls() {
		if len(call) > 7 && call[:7] == "create:" {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("created %d mirrors", creates)
	}
	posts := tg.Destination(10).Sent()
	if len(posts) == 0 || posts[0].Code {
		t.Fatal("missing neutral initial card")
	}
	if err := r.Observe(ctx, app.AgentEvent{Kind: app.AgentGone, Agent: a}); err != nil {
		t.Fatal(err)
	}
	st, _ = sharing.Snapshot()
	if st.Grants[g.ID].State != domain.GrantSuspended {
		t.Fatal("exit still active")
	}
	a.TerminalID = "resumed"
	if err := r.Observe(ctx, app.AgentEvent{Kind: app.AgentAppeared, Agent: a}); err != nil {
		t.Fatal(err)
	}
	st, _ = sharing.Snapshot()
	if st.Grants[g.ID].State != domain.GrantActive || st.Mirrors[g.ID].Address.ThreadID != thread {
		t.Fatal("same session not reused")
	}
	a.SessionDigest = "replacement"
	if err := r.Observe(ctx, app.AgentEvent{Kind: app.AgentAppeared, Agent: a}); err != nil {
		t.Fatal(err)
	}
	st, _ = sharing.Snapshot()
	if st.Grants[g.ID].State != domain.GrantSuspended {
		t.Fatal("replacement kept access")
	}
}

func TestPrivateMirrorAmbiguousCreateNotRetried(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000, 0)
	s := app.NewSharing(ctx, testkit.NewMemSharingStore(), nil)
	s.BotID = 42
	s.Now = func() time.Time { return now }
	a := domain.Agent{Key: domain.Key{PaneID: "p", TerminalID: "t", SessionDigest: "digest"}}
	s.Agent = func(domain.Key) (domain.Agent, bool) { return a, true }
	_, _ = s.Register(ctx, 10, 10, "name", "", now)
	g, err := s.Grant(ctx, app.GrantRequest{RecipientID: 10, Key: a.Key, Role: domain.ShareRead, Bot: domain.BotIdentity{ID: 42, HasTopicsEnabled: true}}, now)
	if err != nil {
		t.Fatal(err)
	}
	tg := testkit.NewFakeTelegram(nil)
	tg.Destination(10).FailNext("create", errors.New("unknown network outcome"))
	r := &app.PrivateReconciler{Sharing: s, Telegram: tg, Agent: s.Agent, Now: s.Now}
	if err := r.Grant(ctx, g); err == nil {
		t.Fatal("ambiguous create accepted")
	}
	st, _ := s.Snapshot()
	if st.Grants[g.ID].State != domain.GrantNeedsRepair {
		t.Fatal("repair state missing")
	}
	if err := r.Grant(ctx, g); err == nil {
		t.Fatal("stale create retried")
	}
}

// TestLifecycleSaveFailureKeepsRevokedGrants: when the exit of a shared
// agent cannot be saved, the rollback suspends the grants the event affects.
// It used to suspend every grant on the pane, so a revoked grantee became
// "suspended persistence" and counted as a grantee again.
func TestLifecycleSaveFailureKeepsRevokedGrants(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000, 0)
	store := testkit.NewMemSharingStore()
	s := app.NewSharing(ctx, store, nil)
	s.BotID = 42
	s.Now = func() time.Time { return now }
	a := domain.Agent{Key: domain.Key{PaneID: "p", TerminalID: "t", SessionDigest: "digest"}, Name: "Agent", Status: domain.StatusIdle}
	s.Agent = func(k domain.Key) (domain.Agent, bool) { return a, k == a.Key }
	bot := domain.BotIdentity{ID: 42, HasTopicsEnabled: true}
	for _, id := range []int64{10, 11} {
		if _, err := s.Register(ctx, id, id, "name", "", now); err != nil {
			t.Fatal(err)
		}
	}
	revoked, err := s.Grant(ctx, app.GrantRequest{RecipientID: 10, Key: a.Key, Role: domain.ShareControl, Bot: bot}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeState(ctx, revoked.ID, revoked.Revision, domain.GrantRevoked, now); err != nil {
		t.Fatal(err)
	}
	active, err := s.Grant(ctx, app.GrantRequest{RecipientID: 11, Key: a.Key, Role: domain.ShareRead, Bot: bot}, now)
	if err != nil {
		t.Fatal(err)
	}
	r := &app.PrivateReconciler{Sharing: s, Telegram: testkit.NewFakeTelegram(nil), Agent: s.Agent, Now: s.Now}
	if err := r.Grant(ctx, active); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Snapshot()
	if before.Grants[active.ID].State != domain.GrantActive || s.Grantee(10) {
		t.Fatal("setup: want one active grant and one revoked grantee")
	}
	store.Fail(errors.New("disk full"))
	if err := s.Lifecycle(ctx, app.AgentEvent{Kind: app.AgentGone, Agent: a}, now); err == nil {
		t.Fatal("failed save reported success")
	}
	st, _ := s.Snapshot()
	if g := st.Grants[revoked.ID]; g != before.Grants[revoked.ID] {
		t.Fatalf("revoked grant changed: %s %s", g.State, g.SuspendReason)
	}
	if s.Grantee(10) {
		t.Fatal("revoked recipient counts as a grantee again")
	}
	if g := st.Grants[active.ID]; g.State != domain.GrantSuspended || g.SuspendReason != "persistence" {
		t.Fatalf("affected grant %s %s, want suspended persistence", g.State, g.SuspendReason)
	}
}

func TestPrivateReconcilerRetriesMetadataEditOnLaterFlush(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000, 0)
	s := app.NewSharing(ctx, testkit.NewMemSharingStore(), nil)
	s.BotID = 42
	s.Now = func() time.Time { return now }
	a := domain.Agent{Key: domain.Key{PaneID: "p", TerminalID: "t", SessionDigest: "digest"}, Name: "Before", Status: domain.StatusIdle}
	s.Agent = func(k domain.Key) (domain.Agent, bool) { return a, k == a.Key }
	_, _ = s.Register(ctx, 10, 10, "name", "", now)
	g, err := s.Grant(ctx, app.GrantRequest{RecipientID: 10, Key: a.Key, Role: domain.ShareRead, Bot: domain.BotIdentity{ID: 42, HasTopicsEnabled: true}}, now)
	if err != nil {
		t.Fatal(err)
	}
	tg := testkit.NewFakeTelegram(nil)
	r := &app.PrivateReconciler{Sharing: s, Telegram: tg, Agent: s.Agent, Now: s.Now}
	if err := r.Grant(ctx, g); err != nil {
		t.Fatal(err)
	}
	a.Name = "After"
	tg.Destination(10).FailNext("edit", errors.New("temporary network failure"))
	if err := r.Observe(ctx, app.AgentEvent{Kind: app.AgentAppeared, Agent: a}); err != nil {
		t.Fatal(err)
	}
	if got := privateEditCalls(tg.Destination(10)); got != 1 {
		t.Fatalf("edit calls after failure = %d, want one", got)
	}
	if err := r.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got := privateEditCalls(tg.Destination(10)); got != 1 {
		t.Fatalf("immediate retry edit calls = %d, want backoff to defer it", got)
	}
	now = now.Add(time.Second)
	if err := r.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	topics := tg.Destination(10).Topics()
	if len(topics) != 1 || topics[0].Name != "After" || topics[0].IconEmojiID != string(domain.StatusIdle) {
		t.Fatalf("topic after retry = %+v, want latest metadata", topics)
	}
	if got := privateEditCalls(tg.Destination(10)); got != 2 {
		t.Fatalf("total edit calls = %d, want exactly one retry", got)
	}
}

func TestPrivateReconcilerRetriesAgentGoneEditOnLaterFlush(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000, 0)
	s := app.NewSharing(ctx, testkit.NewMemSharingStore(), nil)
	s.BotID = 42
	s.Now = func() time.Time { return now }
	a := domain.Agent{Key: domain.Key{PaneID: "p", TerminalID: "t", SessionDigest: "digest"}, Name: "Agent", Status: domain.StatusIdle}
	s.Agent = func(k domain.Key) (domain.Agent, bool) { return a, k == a.Key }
	_, _ = s.Register(ctx, 10, 10, "name", "", now)
	g, err := s.Grant(ctx, app.GrantRequest{RecipientID: 10, Key: a.Key, Role: domain.ShareRead, Bot: domain.BotIdentity{ID: 42, HasTopicsEnabled: true}}, now)
	if err != nil {
		t.Fatal(err)
	}
	tg := testkit.NewFakeTelegram(nil)
	r := &app.PrivateReconciler{Sharing: s, Telegram: tg, Agent: s.Agent, Now: s.Now}
	if err := r.Grant(ctx, g); err != nil {
		t.Fatal(err)
	}
	tg.Destination(10).FailNext("edit", errors.New("temporary network failure"))
	if err := r.Observe(ctx, app.AgentEvent{Kind: app.AgentGone, Agent: a}); err != nil {
		t.Fatal(err)
	}
	afterGone, _ := s.Snapshot()
	exitedRevision := afterGone.Grants[g.ID].Revision
	if got := privateEditCalls(tg.Destination(10)); got != 1 {
		t.Fatalf("edit calls after failure = %d, want one", got)
	}
	now = now.Add(time.Second)
	if err := r.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Snapshot()
	if got := st.Grants[g.ID].Revision; got != exitedRevision {
		t.Fatalf("grant revision after retry = %d, want lifecycle to remain at %d", got, exitedRevision)
	}
	topic, ok := tg.Destination(10).Topic(st.Mirrors[g.ID].Address.ThreadID)
	if !ok || topic.IconEmojiID != string(domain.StatusExited) {
		t.Fatalf("topic after retry = %+v, exists=%v, want exited", topic, ok)
	}
	if got := privateEditCalls(tg.Destination(10)); got != 2 {
		t.Fatalf("total edit calls = %d, want exactly one retry", got)
	}
}

func TestPrivateReconcilerDoesNotRetryUnavailableRecipient(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000, 0)
	s := app.NewSharing(ctx, testkit.NewMemSharingStore(), nil)
	s.BotID = 42
	s.Now = func() time.Time { return now }
	a := domain.Agent{Key: domain.Key{PaneID: "p", TerminalID: "t", SessionDigest: "digest"}, Name: "Agent", Status: domain.StatusIdle}
	s.Agent = func(k domain.Key) (domain.Agent, bool) { return a, k == a.Key }
	_, _ = s.Register(ctx, 10, 10, "name", "", now)
	g, err := s.Grant(ctx, app.GrantRequest{RecipientID: 10, Key: a.Key, Role: domain.ShareRead, Bot: domain.BotIdentity{ID: 42, HasTopicsEnabled: true}}, now)
	if err != nil {
		t.Fatal(err)
	}
	tg := testkit.NewFakeTelegram(nil)
	r := &app.PrivateReconciler{Sharing: s, Telegram: tg, Agent: s.Agent, Now: s.Now}
	if err := r.Grant(ctx, g); err != nil {
		t.Fatal(err)
	}
	tg.Destination(10).FailNext("edit", domain.ErrRecipientUnavailable)
	a.Name = "After"
	if err := r.Observe(ctx, app.AgentEvent{Kind: app.AgentAppeared, Agent: a}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := r.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got := privateEditCalls(tg.Destination(10)); got != 1 {
		t.Fatalf("edit calls = %d, want unavailable-recipient failure to be terminal", got)
	}
}

func TestPrivateReconcilerRetriesDoNotStarveLaterGrants(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000, 0)
	s := app.NewSharing(ctx, testkit.NewMemSharingStore(), nil)
	s.BotID = 42
	s.Now = func() time.Time { return now }
	a := domain.Agent{Key: domain.Key{PaneID: "p", TerminalID: "t", SessionDigest: "digest"}, Name: "Before", Status: domain.StatusIdle}
	s.Agent = func(k domain.Key) (domain.Agent, bool) { return a, k == a.Key }
	tg := testkit.NewFakeTelegram(nil)
	for recipient := int64(10); recipient < 15; recipient++ {
		if _, err := s.Register(ctx, recipient, recipient, "name", "", now); err != nil {
			t.Fatal(err)
		}
		g, err := s.Grant(ctx, app.GrantRequest{RecipientID: recipient, Key: a.Key, Role: domain.ShareRead, Bot: domain.BotIdentity{ID: 42, HasTopicsEnabled: true}}, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := (&app.PrivateReconciler{Sharing: s, Telegram: tg, Agent: s.Agent, Now: s.Now}).Grant(ctx, g); err != nil {
			t.Fatal(err)
		}
		if recipient < 14 {
			tg.Destination(recipient).FailNext("edit", errors.New("temporary network failure"))
		}
	}
	r := &app.PrivateReconciler{Sharing: s, Telegram: tg, Agent: s.Agent, Now: s.Now}
	a.Name = "After"
	if err := r.Observe(ctx, app.AgentEvent{Kind: app.AgentAppeared, Agent: a}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if err := r.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got := privateEditCalls(tg.Destination(14)); got != 1 {
		t.Fatalf("fifth grant edit calls after retry pass = %d, want new work to progress", got)
	}
}

func privateEditCalls(tg *testkit.FakeTelegram) int {
	n := 0
	for _, call := range tg.Calls() {
		if len(call) >= 5 && call[:5] == "edit:" {
			n++
		}
	}
	return n
}
