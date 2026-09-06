package testkit

import (
	"context"
	"sync"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// GitCall is one Run call recorded by FakeGit.
type GitCall struct {
	Dir  string
	Args []string
}

// FakeGit is a scripted domain.GitRunner: every Run answers the last
// SetResult or SetError and is recorded.
type FakeGit struct {
	mu     sync.Mutex
	calls  []GitCall
	result domain.GitResult
	err    error
}

var _ domain.GitRunner = (*FakeGit)(nil)

// NewFakeGit returns a runner that answers an empty result.
func NewFakeGit() *FakeGit { return &FakeGit{} }

// SetResult scripts a successful result and clears any error.
func (f *FakeGit) SetResult(r domain.GitResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.result, f.err = r, nil
}

// SetError scripts a failure for every following Run.
func (f *FakeGit) SetError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// Calls returns every Run call, in order.
func (f *FakeGit) Calls() []GitCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]GitCall(nil), f.calls...)
}

// Run implements domain.GitRunner.
func (f *FakeGit) Run(_ context.Context, dir string, args []string) (domain.GitResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, GitCall{Dir: dir, Args: append([]string(nil), args...)})
	if f.err != nil {
		return domain.GitResult{}, f.err
	}
	return f.result, nil
}
