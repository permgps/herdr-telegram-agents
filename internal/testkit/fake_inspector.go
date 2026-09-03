package testkit

import (
	"context"
	"sync"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// FakeInspector is an in-memory domain.TelegramInspector with scripted
// answers. Every call is recorded ("identity", "group", "send-test:<text>")
// and a call whose Block flag is set waits for the context to end, which
// lets tests exercise timeouts.
type FakeInspector struct {
	mu       sync.Mutex
	identity domain.BotIdentity
	group    domain.GroupInfo
	idErr    error
	groupErr error
	sendErr  error
	sendID   int
	block    map[string]bool
	calls    []string
}

var _ domain.TelegramInspector = (*FakeInspector)(nil)

// NewFakeInspector returns a fake whose bot is @fakebot, an owner of a forum
// group titled "Agents", answering message id 500 to SendTest.
func NewFakeInspector() *FakeInspector {
	return &FakeInspector{
		identity: domain.BotIdentity{ID: 42, Username: "fakebot"},
		group: domain.GroupInfo{Title: "Agents", Rights: domain.Rights{
			IsForum: true, IsAdmin: true, CanManageTopics: true, CanDeleteMessages: true}},
		sendID: 500,
		block:  map[string]bool{},
	}
}

// SetIdentity scripts Identity.
func (f *FakeInspector) SetIdentity(id domain.BotIdentity, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.identity, f.idErr = id, err
}

// SetGroup scripts Group.
func (f *FakeInspector) SetGroup(g domain.GroupInfo, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.group, f.groupErr = g, err
}

// SetSend scripts SendTest.
func (f *FakeInspector) SetSend(id int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendID, f.sendErr = id, err
}

// Block makes the named method ("identity", "group", "send-test") wait
// for its context instead of answering.
func (f *FakeInspector) Block(method string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.block[method] = true
}

// Calls lists the recorded calls in order.
func (f *FakeInspector) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *FakeInspector) enter(ctx context.Context, method, call string) error {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	blocked := f.block[method]
	f.mu.Unlock()
	if blocked {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (f *FakeInspector) Identity(ctx context.Context) (domain.BotIdentity, error) {
	if err := f.enter(ctx, "identity", "identity"); err != nil {
		return domain.BotIdentity{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.identity, f.idErr
}

func (f *FakeInspector) Group(ctx context.Context) (domain.GroupInfo, error) {
	if err := f.enter(ctx, "group", "group"); err != nil {
		return domain.GroupInfo{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.group, f.groupErr
}

func (f *FakeInspector) SendTest(ctx context.Context, text string) (int, error) {
	if err := f.enter(ctx, "send-test", "send-test:"+text); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return 0, f.sendErr
	}
	return f.sendID, nil
}
