package testkit

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// FakeReplies is an in-memory domain.ReplySource: replies and failures are
// scripted per agent key and every lookup is recorded.
type FakeReplies struct {
	mu      sync.Mutex
	replies map[domain.Key]string
	errs    map[domain.Key]error
	calls   []domain.Key
}

// NewFakeReplies returns an empty source; unscripted keys answer
// domain.ErrNoReply.
func NewFakeReplies() *FakeReplies {
	return &FakeReplies{replies: map[domain.Key]string{}, errs: map[domain.Key]error{}}
}

// Set scripts the reply text for key and clears any scripted failure.
func (f *FakeReplies) Set(key domain.Key, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies[key] = text
	delete(f.errs, key)
}

// Fail scripts an error for key; LastReply returns it as is.
func (f *FakeReplies) Fail(key domain.Key, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[key] = err
	delete(f.replies, key)
}

// Calls returns the keys looked up so far, in order.
func (f *FakeReplies) Calls() []domain.Key {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.Key(nil), f.calls...)
}

// LastReply implements domain.ReplySource.
func (f *FakeReplies) LastReply(_ context.Context, agent domain.Agent) (domain.Reply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, agent.Key)
	if err, ok := f.errs[agent.Key]; ok {
		return domain.Reply{}, err
	}
	if text, ok := f.replies[agent.Key]; ok {
		return domain.Reply{Text: text, Source: "fake:" + agent.Key.String(), Age: time.Second}, nil
	}
	return domain.Reply{}, fmt.Errorf("%w: not scripted for %s", domain.ErrNoReply, agent.Key)
}
