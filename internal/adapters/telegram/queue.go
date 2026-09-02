package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// queueBuffer bounds how many calls may wait in the queue before Do blocks.
const queueBuffer = 64

// errQueueStopped is returned to callers once Run has exited.
var errQueueStopped = errors.New("telegram queue stopped")

// QueueConfig tunes the serial call queue. Zero fields take the defaults
// from DefaultQueueConfig; Now and Sleep are injectable for tests.
type QueueConfig struct {
	MinGap      time.Duration // minimum spacing between any two API calls
	WindowLimit int           // at most this many calls per Window
	Window      time.Duration // sliding window for WindowLimit
	MaxTries    int           // attempts per call including the first
	MaxWait     time.Duration // longest 429 retry_after the queue honours
	Now         func() time.Time
	Sleep       func(context.Context, time.Duration) error
}

// DefaultQueueConfig follows Telegram's published group limits: one call per
// second and twenty per minute, with five attempts and a one-minute cap on
// flood-control waits.
func DefaultQueueConfig() QueueConfig {
	return QueueConfig{
		MinGap:      time.Second,
		WindowLimit: 20,
		Window:      time.Minute,
		MaxTries:    5,
		MaxWait:     time.Minute,
	}
}

type job struct {
	ctx      context.Context
	op       func(context.Context) error
	done     chan error
	enqueued time.Time
}

// Queue runs Telegram API calls one at a time on a single goroutine, paces
// them with a token bucket (MinGap plus a sliding WindowLimit per Window)
// and retries transient failures. Every attempt, successful or not, counts
// against the bucket because it reached the API either way.
type Queue struct {
	cfg     QueueConfig
	log     *slog.Logger
	jobs    chan job
	stopped chan struct{}
	stopErr error

	// stamps is a ring of the last WindowLimit attempt times, owned by Run.
	stamps []time.Time
	head   int // index of the oldest stamp when count == len(stamps)
	count  int
}

// NewQueue builds a queue; call Run on a goroutine before using Do.
func NewQueue(log *slog.Logger, cfg QueueConfig) *Queue {
	def := DefaultQueueConfig()
	if cfg.MinGap <= 0 {
		cfg.MinGap = def.MinGap
	}
	if cfg.WindowLimit <= 0 {
		cfg.WindowLimit = def.WindowLimit
	}
	if cfg.Window <= 0 {
		cfg.Window = def.Window
	}
	if cfg.MaxTries <= 0 {
		cfg.MaxTries = def.MaxTries
	}
	if cfg.MaxWait <= 0 {
		cfg.MaxWait = def.MaxWait
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleep
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Queue{
		cfg:     cfg,
		log:     log,
		jobs:    make(chan job, queueBuffer),
		stopped: make(chan struct{}),
		stamps:  make([]time.Time, cfg.WindowLimit),
	}
}

// sleep is the default Sleep: a timer that gives up when ctx ends.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Len reports how many calls are waiting to run.
func (q *Queue) Len() int { return len(q.jobs) }

// Do enqueues op and waits for its outcome. It returns ctx.Err() when the
// caller gives up while the call is queued or running, and errQueueStopped
// once Run has exited.
func (q *Queue) Do(ctx context.Context, op func(context.Context) error) error {
	done := make(chan error, 1)
	j := job{ctx: ctx, op: op, done: done, enqueued: q.cfg.Now()}
	select {
	case q.jobs <- j:
	case <-ctx.Done():
		return ctx.Err()
	case <-q.stopped:
		return fmt.Errorf("%w: %w", errQueueStopped, q.stopErr)
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-q.stopped:
		return fmt.Errorf("%w: %w", errQueueStopped, q.stopErr)
	}
}

// Run executes queued calls until runCtx ends. Calls still waiting when it
// exits receive runCtx.Err(); nothing is drained.
func (q *Queue) Run(runCtx context.Context) {
	defer func() {
		q.stopErr = runCtx.Err()
		close(q.stopped)
		for {
			select {
			case j := <-q.jobs:
				j.done <- runCtx.Err()
			default:
				return
			}
		}
	}()
	for {
		select {
		case <-runCtx.Done():
			return
		case j := <-q.jobs:
			if err := j.ctx.Err(); err != nil {
				j.done <- err
				continue
			}
			start := q.cfg.Now()
			err := q.execute(runCtx, j)
			end := q.cfg.Now()
			q.log.Debug("telegram call executed",
				slog.Int64("dur_ms", end.Sub(start).Milliseconds()),
				slog.Int64("queued_ms", start.Sub(j.enqueued).Milliseconds()),
				slog.Any("err", err))
			j.done <- err
			if runCtx.Err() != nil {
				return
			}
		}
	}
}

// execute runs one job with pacing and retries. Sleeps stop early when the
// caller's context or runCtx ends; the caller's error is reported for the
// job while a runCtx failure also stops the queue.
func (q *Queue) execute(runCtx context.Context, j job) error {
	ctx, cancel := context.WithCancel(runCtx)
	defer cancel()
	stop := context.AfterFunc(j.ctx, cancel)
	defer stop()

	var err error
	for try := 1; try <= q.cfg.MaxTries; try++ {
		if err = q.pace(ctx); err != nil {
			return q.sleepErr(runCtx, j, err)
		}
		q.stamp()
		err = j.op(j.ctx)
		if err == nil {
			return nil
		}
		if !isRetryable(err) || try == q.cfg.MaxTries {
			return err
		}
		wait, reason := q.backoff(err, try)
		if wait < 0 {
			return err
		}
		q.log.Warn("telegram call retry",
			slog.Int("try", try),
			slog.Int64("wait_ms", wait.Milliseconds()),
			slog.String("reason", reason),
			slog.Any("err", err))
		if serr := q.cfg.Sleep(ctx, wait); serr != nil {
			return q.sleepErr(runCtx, j, serr)
		}
	}
	return err
}

// backoff chooses the wait before the next attempt: Telegram's retry_after
// for a 429 (negative when it exceeds MaxWait, meaning give up) or try²
// seconds for everything else.
func (q *Queue) backoff(err error, try int) (time.Duration, string) {
	if ra, ok := retryAfter(err); ok {
		if ra > q.cfg.MaxWait {
			return -1, "retry_after above max_wait"
		}
		return ra, "429"
	}
	return time.Duration(try*try) * time.Second, "transient"
}

// sleepErr attributes an interrupted sleep to the caller unless the queue
// itself is shutting down.
func (q *Queue) sleepErr(runCtx context.Context, j job, err error) error {
	if runCtx.Err() != nil {
		return runCtx.Err()
	}
	if j.ctx.Err() != nil {
		return j.ctx.Err()
	}
	return err
}

// pace sleeps until the token bucket allows another attempt.
func (q *Queue) pace(ctx context.Context) error {
	now := q.cfg.Now()
	var wait time.Duration
	if q.count > 0 {
		last := q.stamps[(q.head+q.count-1)%len(q.stamps)]
		wait = q.cfg.MinGap - now.Sub(last)
	}
	if q.count == len(q.stamps) {
		if w := q.cfg.Window - now.Sub(q.stamps[q.head]); w > wait {
			wait = w
		}
	}
	if wait <= 0 {
		return ctx.Err()
	}
	return q.cfg.Sleep(ctx, wait)
}

// stamp records an attempt time in the ring.
func (q *Queue) stamp() {
	if q.count == len(q.stamps) {
		q.stamps[q.head] = q.cfg.Now()
		q.head = (q.head + 1) % len(q.stamps)
		return
	}
	q.stamps[(q.head+q.count)%len(q.stamps)] = q.cfg.Now()
	q.count++
}
