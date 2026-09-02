package testkit

import (
	"sort"
	"sync"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// FakeClock is a domain.Clock whose time moves only through Advance. Timers
// created with After fire, in due order, when Advance crosses their deadline.
type FakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []fakeTimer
}

type fakeTimer struct {
	seq  int
	when time.Time
	ch   chan time.Time
}

var _ domain.Clock = (*FakeClock)(nil)

// NewFakeClock starts at the given instant.
func NewFakeClock(start time.Time) *FakeClock {
	return &FakeClock{now: start}
}

func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *FakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.timers = append(c.timers, fakeTimer{seq: len(c.timers), when: c.now.Add(d), ch: ch})
	return ch
}

// Advance moves the clock forward and fires every timer that became due,
// earliest first. It returns how many timers fired.
func (c *FakeClock) Advance(d time.Duration) int {
	c.mu.Lock()
	c.now = c.now.Add(d)
	sort.SliceStable(c.timers, func(i, j int) bool { return c.timers[i].when.Before(c.timers[j].when) })
	var due, rest []fakeTimer
	for _, t := range c.timers {
		if !t.when.After(c.now) {
			due = append(due, t)
		} else {
			rest = append(rest, t)
		}
	}
	c.timers = rest
	now := c.now
	c.mu.Unlock()
	for _, t := range due {
		t.ch <- now
	}
	return len(due)
}

// Pending returns the number of timers that have not fired yet.
func (c *FakeClock) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}
