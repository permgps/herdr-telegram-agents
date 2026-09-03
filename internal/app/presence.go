package app

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// Presence decides whether quiet mode is in force: the operator is at the
// desk (input seen within quiet.idle_minutes), quiet.enabled is on, the
// platform has an idle source, and no /away override is active. The daemon
// loop samples it every presenceInterval; the bridge goroutine reads it
// (posts, /status) and changes it (/away, /here), so every field sits
// behind the mutex. A change of the effective flag is handed to the daemon
// loop through Changes, never acted on inline.
type Presence struct {
	mu    sync.Mutex
	idle  domain.IdleSource
	opts  *Options
	clock domain.Clock
	log   *slog.Logger

	// atDesk is the latest automatic verdict, sampled says whether one
	// exists yet, unsupported that the source answered ErrIdleUnsupported
	// (final for the daemon's life), failing that the last sample failed
	// (so the warning is logged once).
	atDesk      bool
	sampled     bool
	unsupported bool
	failing     bool
	// manualAway and manualUntil are the /away override; a zero
	// manualUntil means "until /here".
	manualAway  bool
	manualUntil time.Time
	// quiet is the last effective flag handed out through Changes.
	quiet bool

	changes chan bool
}

// NewPresence builds the tracker. A nil idle source behaves like a platform
// without one: never quiet, /away and /here still answer.
func NewPresence(idle domain.IdleSource, opts *Options, clock domain.Clock, log *slog.Logger) *Presence {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if opts == nil {
		opts = NewOptions(domain.DefaultOptions(), nil, nil, log)
	}
	p := &Presence{idle: idle, opts: opts, clock: clock, log: log, changes: make(chan bool, 1)}
	if idle == nil {
		p.unsupported = true
	}
	return p
}

// Changes delivers the effective quiet flag every time it flips: true when
// quiet begins, false when it ends and the daemon should catch up. Only the
// latest value is kept.
func (p *Presence) Changes() <-chan bool { return p.changes }

// Quiet reports whether quiet mode is in force right now.
func (p *Presence) Quiet() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.quiet
}

// State snapshots the tracker.
func (p *Presence) State() domain.PresenceState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state()
}

func (p *Presence) state() domain.PresenceState {
	return domain.PresenceState{
		Enabled:    p.opts.QuietEnabled(),
		Supported:  !p.unsupported,
		AtDesk:     p.sampled && p.atDesk,
		ManualAway: p.manualAway,
		Until:      p.manualUntil,
		Quiet:      p.quiet,
	}
}

// Poll expires a timed /away, samples the idle source and recomputes the
// effective flag. It runs on the daemon goroutine.
func (p *Presence) Poll(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.clock.Now()
	if p.manualAway && !p.manualUntil.IsZero() && !now.Before(p.manualUntil) {
		p.manualAway, p.manualUntil = false, time.Time{}
		p.log.Info("away expired, presence automatic again")
	}
	p.sample(ctx)
	p.recompute()
}

// sample asks the idle source once and updates the automatic verdict.
func (p *Presence) sample(ctx context.Context) {
	if p.unsupported {
		return
	}
	d, err := p.idle.Idle(ctx)
	switch {
	case errors.Is(err, domain.ErrIdleUnsupported):
		p.unsupported = true
		p.atDesk, p.sampled = false, true
		p.log.Warn("presence: no input idle source on this platform, quiet mode stays off; /away and /here still answer")
		return
	case err != nil:
		if !p.failing {
			p.failing = true
			p.log.Warn("presence sample failed, keeping the previous verdict", slog.String("err", err.Error()), slog.Bool("at_desk", p.atDesk))
		}
		return
	}
	if p.failing {
		p.failing = false
		p.log.Info("presence source recovered")
	}
	threshold := p.opts.QuietIdle()
	atDesk := d < threshold
	if !p.sampled || atDesk != p.atDesk {
		p.log.Debug("presence sample", slog.Int64("idle_ms", d.Milliseconds()), slog.Int64("threshold_ms", threshold.Milliseconds()), slog.Bool("at_desk", atDesk))
	}
	p.atDesk, p.sampled = atDesk, true
}

// Away forces "away": until /here when d is zero, otherwise for d. by is
// the operator id, for the log.
func (p *Presence) Away(d time.Duration, by int64) domain.PresenceState {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.manualAway = true
	p.manualUntil = time.Time{}
	if d > 0 {
		p.manualUntil = p.clock.Now().Add(d)
	}
	p.log.Info("away set", slog.Int64("by", by), slog.Time("until", p.manualUntil), slog.Bool("timed", d > 0))
	p.recompute()
	return p.state()
}

// Here clears the /away override; the automatic verdict rules again.
func (p *Presence) Here(by int64) domain.PresenceState {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.manualAway, p.manualUntil = false, time.Time{}
	p.log.Info("presence automatic", slog.Int64("by", by), slog.Bool("at_desk", p.atDesk))
	p.recompute()
	return p.state()
}

// Recompute re-evaluates the effective flag after an option change.
func (p *Presence) Recompute() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.recompute()
}

// recompute derives the effective flag and, when it flipped, logs it and
// hands the new value to the daemon loop, replacing an undelivered one so
// the loop always sees the latest state.
func (p *Presence) recompute() {
	quiet := p.opts.QuietEnabled() && !p.unsupported && p.sampled && p.atDesk && !p.manualAway
	if quiet == p.quiet {
		return
	}
	p.quiet = quiet
	if quiet {
		p.log.Info("quiet on: operator at the desk")
	} else {
		p.log.Info("quiet off: operator away, catching up", slog.Bool("manual", p.manualAway), slog.Bool("at_desk", p.atDesk))
	}
	select {
	case <-p.changes:
	default:
	}
	p.changes <- quiet
}
