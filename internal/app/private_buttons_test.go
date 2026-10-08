package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

// buttonFixture is a PrivateControl with a dashboard and one Read grant per
// recipient; the button table is unexported, so these tests live inside the
// package.
type buttonFixture struct {
	s   *Sharing
	p   *PrivateControl
	d   *PrivateDashboard
	tg  *testkit.FakeTelegram
	now time.Time
}

func newButtonFixture(t *testing.T, recipients ...int64) *buttonFixture {
	t.Helper()
	ctx := context.Background()
	f := &buttonFixture{tg: testkit.NewFakeTelegram(nil), now: time.Unix(1000, 0)}
	f.s = NewSharing(ctx, testkit.NewMemSharingStore(), nil)
	f.s.BotID = 42
	f.s.Now = func() time.Time { return f.now }
	a := domain.Agent{Key: domain.Key{PaneID: "p", TerminalID: "t", SessionDigest: "digest"}, Name: "agent", Status: domain.StatusIdle, Kind: "claude"}
	f.s.Agent = func(k domain.Key) (domain.Agent, bool) { return a, k == a.Key }
	r := &PrivateReconciler{Sharing: f.s, Telegram: f.tg, Agent: f.s.Agent, Now: f.s.Now}
	for _, id := range recipients {
		if _, err := f.s.Register(ctx, id, id, "name", "", f.now); err != nil {
			t.Fatal(err)
		}
		g, err := f.s.Grant(ctx, GrantRequest{RecipientID: id, Key: a.Key, Role: domain.ShareRead, Bot: domain.BotIdentity{ID: 42, HasTopicsEnabled: true}}, f.now)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Grant(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	f.p = &PrivateControl{Sharing: f.s, Telegram: f.tg, Transport: f.tg, Herdr: testkit.NewFakeHerdr(nil), Agent: f.s.Agent, Now: f.s.Now}
	f.d = NewPrivateDashboard(f.p, r, "bot", nil)
	f.p.Dashboard = f.d
	return f
}

// entries counts the live buttons recipient holds, by kind.
func (f *buttonFixture) entries(recipient int64) map[string]int {
	n := map[string]int{}
	for _, b := range f.p.callbacks {
		if b.origin.ActorID == recipient {
			n[b.kind]++
		}
	}
	return n
}

// dashboard returns recipient's dashboard message and the buttons it shows.
func (f *buttonFixture) dashboard(recipient int64) (domain.MessageAddress, domain.TopicAddress, []domain.Button) {
	st, _ := f.s.Snapshot()
	m := st.Dashboards[recipient]
	return m, st.ServiceTopics[recipient], f.tg.Destination(recipient).Buttons(m.MessageID)
}

func (f *buttonFixture) press(t *testing.T, recipient int64, ref string) string {
	t.Helper()
	m, address, _ := f.dashboard(recipient)
	if err := f.p.Handle(context.Background(), domain.PrivateMessage{Contact: domain.PrivateContact{ActorID: recipient}, Address: address, MessageID: m.MessageID, CallbackID: "cb", CallbackData: ref}); err != nil {
		t.Fatal(err)
	}
	calls := f.tg.Calls()
	for i := len(calls) - 1; i >= 0; i-- {
		if strings.HasPrefix(calls[i], "answer:cb:") {
			return strings.TrimPrefix(calls[i], "answer:cb:")
		}
	}
	t.Fatal("press not answered")
	return ""
}

// TestPrivateDashboardRefreshPrunesButtons: every /agents refresh replaces
// the dashboard keyboard, so the table keeps exactly one set of buttons per
// grant, the newest; another recipient's set is untouched.
func TestPrivateDashboardRefreshPrunesButtons(t *testing.T) {
	f := newButtonFixture(t, 10, 11)
	ctx := context.Background()
	if err := f.d.Refresh(ctx, 11, true); err != nil {
		t.Fatal(err)
	}
	_, _, other := f.dashboard(11)
	if err := f.d.Refresh(ctx, 10, true); err != nil {
		t.Fatal(err)
	}
	_, _, first := f.dashboard(10)
	for range 50 {
		if err := f.d.Refresh(ctx, 10, true); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.entries(10); len(n) != 3 || n["status"] != 1 || n["screen"] != 1 || n["pause"] != 1 {
		t.Fatalf("recipient 10 holds %v after 50 refreshes", n)
	}
	if n := f.entries(11); n["status"]+n["screen"]+n["pause"] != 3 {
		t.Fatalf("recipient 11 lost buttons to 10's refreshes: %v", n)
	}
	if got := f.press(t, 10, first[0].Data); got != "This button is stale or unavailable." {
		t.Fatalf("pre-refresh button answered %q", got)
	}
	_, _, newest := f.dashboard(10)
	if len(newest) != 3 {
		t.Fatalf("dashboard buttons %+v", newest)
	}
	if got := f.press(t, 10, newest[0].Data); got != "" {
		t.Fatalf("newest button answered %q", got)
	}
	sent := f.tg.Destination(10).Sent()
	if last := sent[len(sent)-1]; last.Text != "agent · idle" {
		t.Fatalf("status press posted %+v", last)
	}
	if got := f.press(t, 11, other[0].Data); got != "" {
		t.Fatalf("other recipient's button answered %q", got)
	}
}

// TestPrivateButtonsCappedPerActor: one recipient minting more dialog
// buttons than its share loses its own oldest ones, never another's.
func TestPrivateButtonsCappedPerActor(t *testing.T) {
	f := newButtonFixture(t, 10, 11)
	if err := f.d.Refresh(context.Background(), 11, true); err != nil {
		t.Fatal(err)
	}
	st, _ := f.s.Snapshot()
	var origin domain.ShareOrigin
	for id, g := range st.Grants {
		if g.RecipientID == 10 {
			origin, _ = f.s.Origin(id)
		}
	}
	var refs []string
	for range 600 {
		refs = append(refs, f.p.button(privateButton{origin: origin, kind: "dialog", keys: []string{"1"}, expires: f.now.Add(10 * time.Minute)}))
	}
	if n := f.entries(10)["dialog"]; n != privateButtonsPerActor {
		t.Fatalf("recipient 10 holds %d dialog buttons", n)
	}
	if _, ok := f.p.callbacks[refs[0]]; ok {
		t.Fatal("oldest button survived the cap")
	}
	if _, ok := f.p.callbacks[refs[len(refs)-1]]; !ok {
		t.Fatal("newest button evicted")
	}
	if n := f.entries(11); n["status"]+n["screen"]+n["pause"] != 3 {
		t.Fatalf("recipient 11's buttons evicted: %v", n)
	}
}
