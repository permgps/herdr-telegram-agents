package app_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/app"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

// scriptUI answers prompts from queues and records what was printed.
type scriptUI struct {
	mu       sync.Mutex
	secrets  []string
	confirms []bool
	choices  []int
	printed  []string
	prompts  []string
}

func (u *scriptUI) Printed() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return strings.Join(u.printed, "\n")
}

func (u *scriptUI) Prompts() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.prompts...)
}

func (u *scriptUI) Print(text string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.printed = append(u.printed, text)
}
func (u *scriptUI) Ask(prompt string) (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.prompts = append(u.prompts, prompt)
	return "", nil
}
func (u *scriptUI) AskSecret(prompt string) (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.prompts = append(u.prompts, prompt)
	if len(u.secrets) == 0 {
		return "", errors.New("no scripted secret")
	}
	s := u.secrets[0]
	u.secrets = u.secrets[1:]
	return s, nil
}
func (u *scriptUI) Confirm(prompt string) (bool, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.prompts = append(u.prompts, prompt)
	if len(u.confirms) == 0 {
		return false, errors.New("no scripted confirm")
	}
	c := u.confirms[0]
	u.confirms = u.confirms[1:]
	return c, nil
}
func (u *scriptUI) Choose(prompt string, options []string) (int, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.prompts = append(u.prompts, prompt+" | "+strings.Join(options, " ; "))
	if len(u.choices) == 0 {
		return 0, errors.New("no scripted choice")
	}
	c := u.choices[0]
	u.choices = u.choices[1:]
	return c, nil
}

// fakeProbe scripts Identity per token and streams candidates.
type fakeProbe struct {
	token      string
	rejected   map[string]bool
	candidates chan domain.GroupCandidate
	closed     bool
}

func (p *fakeProbe) Identity(context.Context) (domain.BotIdentity, error) {
	if p.rejected[p.token] {
		return domain.BotIdentity{}, domain.ErrBotUnauthorized
	}
	return domain.BotIdentity{ID: 42, Username: "agents_bot"}, nil
}

func (p *fakeProbe) Candidates(context.Context) (<-chan domain.GroupCandidate, error) {
	return p.candidates, nil
}

func (p *fakeProbe) Close() error {
	p.closed = true
	return nil
}

type setupFixture struct {
	store  *testkit.MemConfigStore
	clock  *testkit.FakeClock
	ui     *scriptUI
	probes []*fakeProbe
	feed   chan domain.GroupCandidate
	setup  *app.Setup
}

func newSetup(t *testing.T, rejected ...string) *setupFixture {
	t.Helper()
	f := &setupFixture{
		store: testkit.NewMemConfigStore(),
		clock: testkit.NewFakeClock(t0),
		ui:    &scriptUI{},
		feed:  make(chan domain.GroupCandidate, 8),
	}
	bad := map[string]bool{}
	for _, r := range rejected {
		bad[r] = true
	}
	factory := func(token string) (domain.SetupProbe, error) {
		p := &fakeProbe{token: token, rejected: bad, candidates: f.feed}
		f.probes = append(f.probes, p)
		return p, nil
	}
	f.setup = app.NewSetup(f.store, factory, f.ui, f.clock, nil)
	return f
}

// run executes the wizard on a goroutine so the test can feed candidates
// and advance the clock.
func (f *setupFixture) run(ctx context.Context) <-chan setupResult {
	out := make(chan setupResult, 1)
	go func() {
		cfg, saved, err := f.setup.Run(ctx)
		out <- setupResult{cfg, saved, err}
	}()
	return out
}

type setupResult struct {
	cfg   domain.Config
	saved bool
	err   error
}

func (f *setupFixture) waitTimers(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f.clock.Pending() >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("pending timers = %d, want >= %d", f.clock.Pending(), n)
}

func result(t *testing.T, ch <-chan setupResult) setupResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(3 * time.Second):
		t.Fatal("setup did not finish")
		return setupResult{}
	}
}

func TestSetupHappyPath(t *testing.T) {
	f := newSetup(t)
	f.ui.secrets = []string{" 1:token "}
	f.ui.confirms = []bool{true}
	done := f.run(context.Background())

	f.waitTimers(t, 1) // hint timer armed: wizard is waiting
	f.feed <- domain.GroupCandidate{ChatID: -1001, Title: "Agents", FromID: 7, FromUsername: "alex"}
	f.waitTimers(t, 2) // sibling timer armed
	f.clock.Advance(3 * time.Second)

	r := result(t, done)
	if r.err != nil || !r.saved {
		t.Fatalf("Run = %+v", r)
	}
	if r.cfg.BotToken != "1:token" || r.cfg.BotUsername != "agents_bot" || r.cfg.ChatID != -1001 ||
		r.cfg.ChatTitle != "Agents" || len(r.cfg.OperatorIDs) != 1 || r.cfg.OperatorIDs[0] != 7 || !r.cfg.ConfiguredAt.Equal(f.clock.Now()) {
		t.Fatalf("config = %+v", r.cfg)
	}
	if f.store.SaveCount() != 1 {
		t.Fatalf("saves = %d", f.store.SaveCount())
	}
	if !f.probes[0].closed {
		t.Fatal("probe not closed")
	}
	if !strings.Contains(strings.Join(f.ui.Prompts(), "\n"), `Group "Agents", operator @alex (id 7). Save?`) {
		t.Fatalf("prompts = %v", f.ui.Prompts())
	}
}

func TestSetupRetriesRejectedToken(t *testing.T) {
	f := newSetup(t, "bad")
	f.ui.secrets = []string{"bad", "", "good"}
	f.ui.confirms = []bool{true}
	done := f.run(context.Background())

	f.waitTimers(t, 1)
	f.feed <- domain.GroupCandidate{ChatID: -1, Title: "G", FromID: 1}
	f.waitTimers(t, 2)
	f.clock.Advance(3 * time.Second)

	r := result(t, done)
	if r.err != nil || r.cfg.BotToken != "good" {
		t.Fatalf("Run = %+v", r)
	}
	if len(f.probes) != 2 || !f.probes[0].closed {
		t.Fatalf("probes = %d, first closed=%v", len(f.probes), f.probes[0].closed)
	}
	if !strings.Contains(f.ui.Printed(), "rejected this token") {
		t.Fatalf("printed = %v", f.ui.Printed())
	}
}

func TestSetupGivesUpAfterThreeRejections(t *testing.T) {
	f := newSetup(t, "bad")
	f.ui.secrets = []string{"bad", "bad", "bad"}
	r := result(t, f.run(context.Background()))
	if !errors.Is(r.err, app.ErrSetupCancelled) {
		t.Fatalf("err = %v", r.err)
	}
}

func TestSetupChoosesAmongSiblings(t *testing.T) {
	f := newSetup(t)
	f.ui.secrets = []string{"tok"}
	f.ui.choices = []int{1}
	done := f.run(context.Background())

	f.waitTimers(t, 1)
	f.feed <- domain.GroupCandidate{ChatID: -1, Title: "First", FromID: 1}
	f.feed <- domain.GroupCandidate{ChatID: -2, Title: "Second", FromID: 2, FromUsername: "bob"}
	f.waitTimers(t, 2)
	f.clock.Advance(3 * time.Second)

	r := result(t, done)
	if r.err != nil || r.cfg.ChatID != -2 || r.cfg.OperatorIDs[0] != 2 {
		t.Fatalf("Run = %+v", r)
	}
	prompts := f.ui.Prompts()
	last := prompts[len(prompts)-1]
	if !strings.Contains(last, `"First"`) || !strings.Contains(last, `"Second" (chat -2), operator @bob (id 2)`) {
		t.Fatalf("choose prompt = %q", last)
	}
}

func TestSetupKeepsExistingConfig(t *testing.T) {
	f := newSetup(t)
	existing := domain.Config{Version: 1, BotToken: "old", ChatID: -5, ChatTitle: "Old", OperatorIDs: []int64{3}, LogLevel: "debug"}
	f.store.Set(existing)
	f.ui.confirms = []bool{false}
	r := result(t, f.run(context.Background()))
	if r.err != nil || r.saved || r.cfg.BotToken != "old" {
		t.Fatalf("Run = %+v", r)
	}
	if len(f.probes) != 0 || f.store.SaveCount() != 0 {
		t.Fatal("kept config must not touch telegram or the store")
	}

	// Reconfiguring keeps the log level but replaces everything else.
	f = newSetup(t)
	f.store.Set(existing)
	f.ui.confirms = []bool{true, true}
	f.ui.secrets = []string{"new"}
	done := f.run(context.Background())
	f.waitTimers(t, 1)
	f.feed <- domain.GroupCandidate{ChatID: -9, Title: "New", FromID: 4}
	f.waitTimers(t, 2)
	f.clock.Advance(3 * time.Second)
	r = result(t, done)
	if r.err != nil || !r.saved || r.cfg.BotToken != "new" || r.cfg.ChatID != -9 || r.cfg.LogLevel != "debug" {
		t.Fatalf("reconfigure = %+v", r)
	}
}

func TestSetupHintAndCancel(t *testing.T) {
	f := newSetup(t)
	f.ui.secrets = []string{"tok"}
	ctx, cancel := context.WithCancel(context.Background())
	done := f.run(ctx)

	f.waitTimers(t, 1)
	f.clock.Advance(10 * time.Minute)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(f.ui.Printed(), "Nothing seen yet") {
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(f.ui.Printed(), "remove its admin rights") {
		t.Fatalf("hint not printed: %v", f.ui.Printed())
	}
	cancel()
	r := result(t, done)
	if !errors.Is(r.err, context.Canceled) || f.store.SaveCount() != 0 {
		t.Fatalf("cancel = %+v, saves=%d", r, f.store.SaveCount())
	}
}

func TestSetupDeclinedSave(t *testing.T) {
	f := newSetup(t)
	f.ui.secrets = []string{"tok"}
	f.ui.confirms = []bool{false}
	done := f.run(context.Background())
	f.waitTimers(t, 1)
	f.feed <- domain.GroupCandidate{ChatID: -1, Title: "G", FromID: 1}
	f.waitTimers(t, 2)
	f.clock.Advance(3 * time.Second)
	r := result(t, done)
	if !errors.Is(r.err, app.ErrSetupCancelled) || f.store.SaveCount() != 0 {
		t.Fatalf("declined = %+v", r)
	}
}
