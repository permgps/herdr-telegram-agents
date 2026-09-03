package testkit

import (
	"context"
	"sync"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// FakeIdle is a domain.IdleSource whose answer the test sets: a duration,
// or an error that sticks until the next Set.
type FakeIdle struct {
	mu    sync.Mutex
	idle  time.Duration
	err   error
	calls int
}

var _ domain.IdleSource = (*FakeIdle)(nil)

// NewFakeIdle starts with the given idle time and no error.
func NewFakeIdle(d time.Duration) *FakeIdle { return &FakeIdle{idle: d} }

// Set makes every following Idle answer d and clears any error.
func (f *FakeIdle) Set(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.idle, f.err = d, nil
}

// Fail makes every following Idle return err until Set is called.
func (f *FakeIdle) Fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// Unsupported makes the source behave like a platform without one.
func (f *FakeIdle) Unsupported() { f.Fail(domain.ErrIdleUnsupported) }

// Calls returns how many times Idle was asked.
func (f *FakeIdle) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// Idle answers the configured value or error.
func (f *FakeIdle) Idle(context.Context) (time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return 0, f.err
	}
	return f.idle, nil
}
