package app

import (
	"log/slog"
	"sync"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// debouncer coalesces edits per agent key: Schedule (re)starts a trailing
// timer and, when it fires, the key is delivered on Due. The owner runs the
// actual edit on its own goroutine, so topic writes stay single-writer.
type debouncer struct {
	clock domain.Clock
	delay time.Duration
	log   *slog.Logger

	mu      sync.Mutex
	pending map[domain.Key]chan struct{} // cancel channel per armed timer
	due     chan domain.Key
}

func newDebouncer(clock domain.Clock, delay time.Duration, log *slog.Logger) *debouncer {
	return &debouncer{
		clock:   clock,
		delay:   delay,
		log:     log,
		pending: map[domain.Key]chan struct{}{},
		due:     make(chan domain.Key, 256),
	}
}

// Due delivers keys whose timer fired.
func (d *debouncer) Due() <-chan domain.Key { return d.due }

// Schedule arms (or re-arms) the timer for key with the default delay.
func (d *debouncer) Schedule(key domain.Key) { d.ScheduleAfter(key, d.delay) }

// ScheduleAfter arms (or re-arms) the timer for key with an explicit delay.
func (d *debouncer) ScheduleAfter(key domain.Key, delay time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if cancel, ok := d.pending[key]; ok {
		close(cancel)
	}
	cancel := make(chan struct{})
	d.pending[key] = cancel
	timer := d.clock.After(delay)
	d.log.Debug("edit scheduled", slog.String("key", key.String()), slog.Int64("delay_ms", delay.Milliseconds()))
	go func() {
		select {
		case <-timer:
		case <-cancel:
			return
		}
		d.mu.Lock()
		if d.pending[key] != cancel {
			d.mu.Unlock()
			return
		}
		delete(d.pending, key)
		d.mu.Unlock()
		d.due <- key
	}()
}

// Cancel drops a pending timer for key, if any.
func (d *debouncer) Cancel(key domain.Key) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	cancel, ok := d.pending[key]
	if !ok {
		return false
	}
	close(cancel)
	delete(d.pending, key)
	d.log.Debug("edit cancelled", slog.String("key", key.String()))
	return true
}

// Drain cancels every pending timer and returns the keys, in a stable
// order, so the owner can fire them immediately.
func (d *debouncer) Drain() []domain.Key {
	d.mu.Lock()
	defer d.mu.Unlock()
	keys := make([]domain.Key, 0, len(d.pending))
	for key, cancel := range d.pending {
		close(cancel)
		keys = append(keys, key)
	}
	d.pending = map[domain.Key]chan struct{}{}
	sortKeys(keys)
	return keys
}

// Pending returns the number of armed timers.
func (d *debouncer) Pending() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.pending)
}
