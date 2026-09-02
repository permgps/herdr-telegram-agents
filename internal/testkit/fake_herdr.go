package testkit

import (
	"context"
	"log/slog"
	"sync"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// Notification is one desktop notification a fake recorded.
type Notification struct {
	Title string
	Body  string
}

// FakeHerdr is an in-memory domain.HerdrGateway. Tests script the agent
// list, push socket events and inspect what the application asked for.
type FakeHerdr struct {
	mu       sync.Mutex
	agents   []domain.Agent
	listErr  error
	listN    int
	watches  [][]string
	notifies []Notification
	prompts  []string
	events   chan domain.Event
	log      *slog.Logger
}

var _ domain.HerdrGateway = (*FakeHerdr)(nil)

// NewFakeHerdr returns an empty fake with a buffered event channel.
func NewFakeHerdr(log *slog.Logger) *FakeHerdr {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &FakeHerdr{events: make(chan domain.Event, 64), log: log}
}

// SetAgents replaces the snapshot ListAgents returns.
func (f *FakeHerdr) SetAgents(agents []domain.Agent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agents = append([]domain.Agent(nil), agents...)
	f.log.Debug("fake herdr agents set", slog.Int("count", len(agents)))
}

// FailList makes ListAgents return err until called with nil.
func (f *FakeHerdr) FailList(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listErr = err
}

// Push delivers a socket event to the application.
func (f *FakeHerdr) Push(ev domain.HerdrEvent) {
	f.log.Debug("fake herdr push", slog.String("kind", string(ev.Kind)), slog.String("pane", ev.PaneID))
	f.events <- ev
}

// ListCalls returns how many times ListAgents was called.
func (f *FakeHerdr) ListCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listN
}

// WatchCalls returns every pane set passed to WatchPanes, in order.
func (f *FakeHerdr) WatchCalls() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.watches...)
}

// Notifications returns every Notify call, in order.
func (f *FakeHerdr) Notifications() []Notification {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Notification(nil), f.notifies...)
}

// Prompts returns every Prompt call as "<target>: <text>".
func (f *FakeHerdr) Prompts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.prompts...)
}

func (f *FakeHerdr) ListAgents(context.Context) ([]domain.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listN++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]domain.Agent(nil), f.agents...), nil
}

func (f *FakeHerdr) ReadScreen(context.Context, string, domain.ScreenSource, int) (domain.Screen, error) {
	return domain.Screen{Text: "screen"}, nil
}

func (f *FakeHerdr) Prompt(_ context.Context, target, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prompts = append(f.prompts, target+": "+text)
	return nil
}

func (f *FakeHerdr) SendKeys(context.Context, string, []string) error { return nil }

func (f *FakeHerdr) Rename(context.Context, string, *string) error { return nil }

func (f *FakeHerdr) Notify(_ context.Context, title, body string, _ domain.NotifySound) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notifies = append(f.notifies, Notification{Title: title, Body: body})
	f.log.Debug("fake herdr notify", slog.String("title", title))
	return nil
}

func (f *FakeHerdr) Events() <-chan domain.Event { return f.events }

func (f *FakeHerdr) WatchPanes(_ context.Context, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.watches = append(f.watches, append([]string(nil), ids...))
	return nil
}
