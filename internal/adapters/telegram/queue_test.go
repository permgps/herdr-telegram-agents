package telegram

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot"
)

// fakeClock is a manual clock whose Sleep records every wait and advances
// time instead of blocking.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	c.mu.Unlock()
	return ctx.Err()
}

func (c *fakeClock) recorded() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

// startQueue runs a queue with the fake clock until the test ends.
func startQueue(t *testing.T, cfg QueueConfig) (*Queue, *fakeClock, context.CancelFunc) {
	t.Helper()
	clock := newFakeClock()
	cfg.Now = clock.Now
	cfg.Sleep = clock.Sleep
	q := NewQueue(testLogger(t), cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		q.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return q, clock, cancel
}

func ok(context.Context) error { return nil }

func equalSleeps(got, want []time.Duration) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestQueueMinGap(t *testing.T) {
	q, clock, _ := startQueue(t, QueueConfig{})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := q.Do(ctx, ok); err != nil {
			t.Fatalf("op %d: %v", i, err)
		}
	}
	if got := clock.recorded(); !equalSleeps(got, []time.Duration{time.Second, time.Second}) {
		t.Errorf("sleeps = %v, want [1s 1s]", got)
	}
}

func TestQueueWindowLimit(t *testing.T) {
	q, clock, _ := startQueue(t, QueueConfig{})
	ctx := context.Background()
	for i := 0; i < 21; i++ {
		if err := q.Do(ctx, ok); err != nil {
			t.Fatalf("op %d: %v", i, err)
		}
	}
	got := clock.recorded()
	if len(got) != 20 {
		t.Fatalf("got %d sleeps, want 20: %v", len(got), got)
	}
	for i := 0; i < 19; i++ {
		if got[i] != time.Second {
			t.Errorf("sleep %d = %s, want 1s", i, got[i])
		}
	}
	// Twenty calls were made at t0..t0+19s; the 21st must wait until the
	// first one leaves the 60 s window.
	if got[19] != 41*time.Second {
		t.Errorf("21st op waited %s, want 41s", got[19])
	}
}

func TestQueueRetryAfterOnce(t *testing.T) {
	q, clock, _ := startQueue(t, QueueConfig{})
	calls := 0
	err := q.Do(context.Background(), func(context.Context) error {
		calls++
		if calls == 1 {
			return &bot.TooManyRequestsError{Message: "too many requests", RetryAfter: 2}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
	if got := clock.recorded(); !equalSleeps(got, []time.Duration{2 * time.Second}) {
		t.Errorf("sleeps = %v, want [2s]", got)
	}
}

func TestQueueRetryAfterAboveMaxWait(t *testing.T) {
	q, clock, _ := startQueue(t, QueueConfig{MaxWait: 10 * time.Second})
	calls := 0
	tooMany := &bot.TooManyRequestsError{Message: "too many requests", RetryAfter: 11}
	err := q.Do(context.Background(), func(context.Context) error {
		calls++
		return tooMany
	})
	if !errors.Is(err, tooMany) {
		t.Fatalf("err = %v, want the 429", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
	if got := clock.recorded(); len(got) != 0 {
		t.Errorf("sleeps = %v, want none", got)
	}
}

func TestQueueDoesNotRetryBadRequest(t *testing.T) {
	q, clock, _ := startQueue(t, QueueConfig{})
	calls := 0
	bad := fmt.Errorf("%w, Bad Request: TOPIC_NAME_INVALID", bot.ErrorBadRequest)
	err := q.Do(context.Background(), func(context.Context) error {
		calls++
		return bad
	})
	if !errors.Is(err, bot.ErrorBadRequest) {
		t.Fatalf("err = %v", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
	if got := clock.recorded(); len(got) != 0 {
		t.Errorf("sleeps = %v, want none", got)
	}
}

func TestQueueBacksOffOnServerErrors(t *testing.T) {
	q, clock, _ := startQueue(t, QueueConfig{})
	calls := 0
	server := errors.New("error response from telegram for method sendMessage, 502 Bad Gateway")
	err := q.Do(context.Background(), func(context.Context) error {
		calls++
		return server
	})
	if !errors.Is(err, server) {
		t.Fatalf("err = %v, want the 5xx", err)
	}
	if calls != 5 {
		t.Errorf("calls = %d, want 5", calls)
	}
	want := []time.Duration{time.Second, 4 * time.Second, 9 * time.Second, 16 * time.Second}
	if got := clock.recorded(); !equalSleeps(got, want) {
		t.Errorf("sleeps = %v, want %v", got, want)
	}
}

func TestQueueCallerCancelWhileQueued(t *testing.T) {
	q, _, _ := startQueue(t, QueueConfig{})
	release := make(chan struct{})
	running := make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- q.Do(context.Background(), func(context.Context) error {
			close(running)
			<-release
			return nil
		})
	}()
	<-running

	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{}, 1)
	second := make(chan error, 1)
	go func() {
		second <- q.Do(ctx, func(context.Context) error {
			ran <- struct{}{}
			return nil
		})
	}()
	for q.Len() == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-second; !errors.Is(err, context.Canceled) {
		t.Fatalf("second Do = %v, want context.Canceled", err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first Do = %v", err)
	}
	if err := q.Do(context.Background(), ok); err != nil {
		t.Fatalf("third Do = %v", err)
	}
	select {
	case <-ran:
		t.Error("cancelled op was executed")
	default:
	}
}

func TestQueueShutdownFailsPending(t *testing.T) {
	q, _, cancel := startQueue(t, QueueConfig{})
	release := make(chan struct{})
	running := make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- q.Do(context.Background(), func(context.Context) error {
			close(running)
			<-release
			return nil
		})
	}()
	<-running
	pending := make(chan error, 1)
	go func() {
		pending <- q.Do(context.Background(), ok)
	}()
	for q.Len() == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	close(release)
	if err := <-first; err != nil {
		t.Errorf("running op = %v, want nil", err)
	}
	if err := <-pending; !errors.Is(err, context.Canceled) {
		t.Errorf("pending op = %v, want context.Canceled", err)
	}
	if err := q.Do(context.Background(), ok); !errors.Is(err, errQueueStopped) {
		t.Errorf("Do after shutdown = %v, want errQueueStopped", err)
	}
}

func TestQueueDefaults(t *testing.T) {
	q := NewQueue(nil, QueueConfig{})
	def := DefaultQueueConfig()
	if q.cfg.MinGap != def.MinGap || q.cfg.WindowLimit != def.WindowLimit || q.cfg.Window != def.Window ||
		q.cfg.MaxTries != def.MaxTries || q.cfg.MaxWait != def.MaxWait {
		t.Errorf("defaults not applied: %+v", q.cfg)
	}
	if q.cfg.Now == nil || q.cfg.Sleep == nil || q.log == nil {
		t.Error("nil Now, Sleep or logger")
	}
	if q.Len() != 0 {
		t.Errorf("Len = %d", q.Len())
	}
}

func TestSleepHonoursContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("sleep = %v", err)
	}
	if err := sleep(context.Background(), 0); err != nil {
		t.Errorf("zero sleep = %v", err)
	}
	if err := sleep(context.Background(), time.Millisecond); err != nil {
		t.Errorf("short sleep = %v", err)
	}
}

func TestQueueShutdownCancelsRunningCall(t *testing.T) {
	q, _, cancel := startQueue(t, QueueConfig{})
	running := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	result := make(chan error, 1)
	go func() {
		result <- q.Do(context.Background(), func(ctx context.Context) error {
			close(running)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
				return errors.New("call outlived the queue")
			}
		})
	}()
	<-running
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Do = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Do did not return after the queue stopped while a call was running")
	}
}

// TestQueueManyConcurrentCallers is the "many agents" case: every topic
// competes for one queue, so calls must still run one at a time and stay
// inside the window.
func TestQueueManyConcurrentCallers(t *testing.T) {
	q, clock, _ := startQueue(t, QueueConfig{})
	const callers = 50
	var mu sync.Mutex
	running, maxRunning, done := 0, 0, 0
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			err := q.Do(context.Background(), func(context.Context) error {
				mu.Lock()
				running++
				if running > maxRunning {
					maxRunning = running
				}
				mu.Unlock()
				mu.Lock()
				running--
				done++
				mu.Unlock()
				return nil
			})
			if err != nil {
				t.Errorf("Do: %v", err)
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if maxRunning != 1 {
		t.Fatalf("max concurrent calls = %d, want 1", maxRunning)
	}
	if done != callers {
		t.Fatalf("completed = %d, want %d", done, callers)
	}
	// One wait before every call but the first, and never shorter than the
	// minimum gap.
	sleeps := clock.recorded()
	if len(sleeps) != callers-1 {
		t.Fatalf("sleeps = %d, want %d", len(sleeps), callers-1)
	}
	var elapsed time.Duration
	for i, d := range sleeps {
		if d < time.Second {
			t.Fatalf("sleep %d = %s, shorter than MinGap", i, d)
		}
		elapsed += d
	}
	// The sliding window forces call i to wait for call i-20 to age out, so
	// 50 calls cannot finish faster than two full windows.
	if want := 2 * time.Minute; elapsed < want {
		t.Fatalf("50 calls took %s of simulated time, want at least %s", elapsed, want)
	}
}
