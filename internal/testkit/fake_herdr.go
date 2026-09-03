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

// ReadCall is one ReadScreen call a fake recorded.
type ReadCall struct {
	Target string
	Source domain.ScreenSource
	Lines  int
}

// KeysCall is one SendKeys call a fake recorded.
type KeysCall struct {
	Target string
	Keys   []string
}

// RenameCall is one Rename call a fake recorded; Name nil means clear.
type RenameCall struct {
	Target string
	Name   *string
}

// TabRenameCall is one RenameTab call a fake recorded.
type TabRenameCall struct {
	TabID string
	Label string
}

// FakeHerdr is an in-memory domain.HerdrGateway. Tests script the agent
// list and the screens, push socket events and inspect what the application
// asked for. FailNext fails the next call of a method (read, prompt, keys,
// focus, rename) once.
type FakeHerdr struct {
	mu         sync.Mutex
	agents     []domain.Agent
	listErr    error
	listN      int
	watches    [][]string
	notifies   []Notification
	prompts    []string
	screens    map[string]string
	revisions  map[string]int64
	reads      []ReadCall
	keys       []KeysCall
	focused    []string
	renames    []RenameCall
	tabRenames []TabRenameCall
	failNext   map[string]error
	info       domain.HerdrInfo
	events     chan domain.Event
	log        *slog.Logger
}

var (
	_ domain.HerdrGateway = (*FakeHerdr)(nil)
	_ domain.HerdrProber  = (*FakeHerdr)(nil)
)

// NewFakeHerdr returns an empty fake with a buffered event channel.
func NewFakeHerdr(log *slog.Logger) *FakeHerdr {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &FakeHerdr{
		screens:   map[string]string{},
		revisions: map[string]int64{},
		failNext:  map[string]error{},
		events:    make(chan domain.Event, 64),
		log:       log,
	}
}

// SetScreen scripts what ReadScreen returns for the target. Targets without
// a script return "screen".
func (f *FakeHerdr) SetScreen(target, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.screens[target] = text
}

// SetScreenAt scripts the screen text and the revision ReadScreen reports
// for the target; SetScreen keeps revision 0.
func (f *FakeHerdr) SetScreenAt(target, text string, revision int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.screens[target] = text
	f.revisions[target] = revision
}

// FailNext makes the next call of method (read, prompt, keys, focus,
// rename) return err. Only one failure is queued per method.
func (f *FakeHerdr) FailNext(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext[method] = err
}

// Reads returns every ReadScreen call, in order.
func (f *FakeHerdr) Reads() []ReadCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ReadCall(nil), f.reads...)
}

// Keys returns every SendKeys call, in order.
func (f *FakeHerdr) Keys() []KeysCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]KeysCall(nil), f.keys...)
}

// Focused returns the target of every Focus call, in order.
func (f *FakeHerdr) Focused() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.focused...)
}

// TabRenames returns every RenameTab call, in order.
func (f *FakeHerdr) TabRenames() []TabRenameCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]TabRenameCall(nil), f.tabRenames...)
}

// Renames returns every Rename call, in order.
func (f *FakeHerdr) Renames() []RenameCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]RenameCall(nil), f.renames...)
}

// fail pops the queued failure for method, if any. Callers hold mu.
func (f *FakeHerdr) fail(method string) error {
	err, ok := f.failNext[method]
	if !ok {
		return nil
	}
	delete(f.failNext, method)
	f.log.Debug("fake herdr failing call", slog.String("method", method), slog.Any("err", err))
	return err
}

// SetPing scripts the Ping answer (default: version "fake", protocol 17).
func (f *FakeHerdr) SetPing(info domain.HerdrInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.info = info
}

// Ping answers the scripted HerdrInfo, or the error armed with
// FailNext("ping", err).
func (f *FakeHerdr) Ping(context.Context) (domain.HerdrInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("ping"); err != nil {
		return domain.HerdrInfo{}, err
	}
	if f.info == (domain.HerdrInfo{}) {
		return domain.HerdrInfo{Version: "fake", Protocol: 17}, nil
	}
	return f.info, nil
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

func (f *FakeHerdr) ReadScreen(_ context.Context, target string, source domain.ScreenSource, lines int) (domain.Screen, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, ReadCall{Target: target, Source: source, Lines: lines})
	f.log.Debug("fake herdr read", slog.String("target", target), slog.String("source", string(source)), slog.Int("lines", lines))
	if err := f.fail("read"); err != nil {
		return domain.Screen{}, err
	}
	text, ok := f.screens[target]
	if !ok {
		text = "screen"
	}
	return domain.Screen{Text: text, Revision: f.revisions[target]}, nil
}

func (f *FakeHerdr) Prompt(_ context.Context, target, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prompts = append(f.prompts, target+": "+text)
	f.log.Debug("fake herdr prompt", slog.String("target", target), slog.Int("len", len(text)))
	return f.fail("prompt")
}

func (f *FakeHerdr) SendKeys(_ context.Context, target string, keys []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, KeysCall{Target: target, Keys: append([]string(nil), keys...)})
	f.log.Debug("fake herdr send_keys", slog.String("target", target), slog.Any("keys", keys))
	return f.fail("keys")
}

func (f *FakeHerdr) Focus(_ context.Context, target string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.focused = append(f.focused, target)
	f.log.Debug("fake herdr focus", slog.String("target", target))
	return f.fail("focus")
}

func (f *FakeHerdr) Rename(_ context.Context, target string, name *string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renames = append(f.renames, RenameCall{Target: target, Name: name})
	f.log.Debug("fake herdr rename", slog.String("target", target), slog.Any("name", name))
	return f.fail("rename")
}

func (f *FakeHerdr) RenameTab(_ context.Context, tabID, label string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tabRenames = append(f.tabRenames, TabRenameCall{TabID: tabID, Label: label})
	f.log.Debug("fake herdr rename tab", slog.String("tab_id", tabID), slog.String("label", label))
	return f.fail("rename_tab")
}

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
