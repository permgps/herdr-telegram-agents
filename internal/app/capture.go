package app

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// Capture accumulates the output of working agents. Herdr keeps no
// scrollback for agents that redraw the whole screen (Claude Code), so the
// only way to show what an agent printed between two human messages is to
// read its screen about once a second while it works and merge the
// snapshots into a domain.History per agent. Marks come from registry
// events: every transition into working is a human message, whether typed
// in Herdr or sent from Telegram. Capture runs on its own goroutine owned
// by Daemon.Run; Since is called from the bridge goroutine.
type Capture struct {
	herdr domain.HerdrGateway
	live  func() []domain.Agent
	clock domain.Clock
	log   *slog.Logger

	mu     sync.Mutex
	hist   map[domain.Key]*domain.History
	last   map[domain.Key]string // SHA-256 of the last screen merged per key
	status map[domain.Key]domain.Status
	left   map[domain.Key]time.Time // when the agent last left working

	// Interval is the tick between reads; Grace keeps reading after an
	// agent left working so its final screen is committed; ReadTimeout
	// bounds one agent.read. Tests shorten them.
	Interval    time.Duration
	Grace       time.Duration
	ReadTimeout time.Duration
}

// NewCapture wires the capture over the Herdr port and the registry's live
// agent list.
func NewCapture(herdr domain.HerdrGateway, live func() []domain.Agent, clock domain.Clock, log *slog.Logger) *Capture {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Capture{
		herdr:       herdr,
		live:        live,
		clock:       clock,
		log:         log,
		hist:        map[domain.Key]*domain.History{},
		last:        map[domain.Key]string{},
		status:      map[domain.Key]domain.Status{},
		left:        map[domain.Key]time.Time{},
		Interval:    captureInterval,
		Grace:       captureGrace,
		ReadTimeout: captureReadTimeout,
	}
}

// Observe folds one registry event into the marks: a transition into
// working marks the history, a transition out of it starts the grace
// period, and a gone agent is forgotten. It never blocks.
func (c *Capture) Observe(ev AgentEvent) {
	key := ev.Agent.Key
	c.mu.Lock()
	defer c.mu.Unlock()
	if ev.Kind == AgentGone {
		delete(c.hist, key)
		delete(c.last, key)
		delete(c.status, key)
		delete(c.left, key)
		c.log.Debug("history dropped", slog.String("key", key.String()))
		return
	}
	prev, known := c.status[key]
	cur := ev.Agent.Status
	c.status[key] = cur
	switch {
	case cur == domain.StatusWorking && (!known || prev != domain.StatusWorking):
		h := c.history(key)
		h.Mark()
		c.log.Debug("history marked", slog.String("key", key.String()),
			slog.String("from", string(prev)), slog.String("to", string(cur)), slog.Int("committed", h.Len()))
	case known && prev == domain.StatusWorking && cur != domain.StatusWorking:
		c.left[key] = c.clock.Now()
		c.log.Debug("capture grace started", slog.String("key", key.String()), slog.String("to", string(cur)))
	}
}

// Run reads the screens of working agents on every tick until ctx is done.
func (c *Capture) Run(ctx context.Context) {
	c.log.Info("capture started", slog.Int64("interval_ms", c.Interval.Milliseconds()), slog.Int("lines", captureLines))
	defer c.log.Info("capture stopped")
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.clock.After(c.Interval):
			c.tick(ctx)
		}
	}
}

// tick captures every agent that is working or left working within Grace.
func (c *Capture) tick(ctx context.Context) {
	now := c.clock.Now()
	for _, a := range c.live() {
		if ctx.Err() != nil {
			return
		}
		if a.Status != domain.StatusWorking && !c.inGrace(a.Key, now) {
			continue
		}
		c.capture(ctx, a.Key)
	}
}

func (c *Capture) inGrace(key domain.Key, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	at, ok := c.left[key]
	if !ok {
		return false
	}
	if now.Sub(at) > c.Grace {
		delete(c.left, key)
		return false
	}
	return true
}

// capture reads one screen and merges it. Read failures are logged and left
// to the next tick.
func (c *Capture) capture(ctx context.Context, key domain.Key) {
	screen, err := c.read(ctx, key)
	if err != nil {
		c.log.Warn("capture read failed", slog.String("key", key.String()), slog.String("err", err.Error()))
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.merge(key, screen)
}

func (c *Capture) read(ctx context.Context, key domain.Key) (domain.Screen, error) {
	rctx, cancel := context.WithTimeout(ctx, c.ReadTimeout)
	defer cancel()
	return c.herdr.ReadScreen(rctx, key.PaneID, domain.ScreenRecent, captureLines)
}

// merge appends the screen to the key's history unless it equals the last
// merged one. Herdr 0.7.5 reports revision 0 for agent.read, so the text
// hash, not the revision, decides. The caller holds the lock.
func (c *Capture) merge(key domain.Key, screen domain.Screen) *domain.History {
	h := c.history(key)
	hash := hashText(screen.Text)
	if c.last[key] == hash {
		c.log.Debug("screen unchanged", slog.String("key", key.String()), slog.Int("committed", h.Len()))
		return h
	}
	added, shift, gap := h.Append(screenLines(screen.Text))
	c.last[key] = hash
	if gap {
		c.log.Warn("screen history gap", slog.String("key", key.String()),
			slog.Int64("revision", screen.Revision), slog.Int("added", added), slog.Int("committed", h.Len()))
		return h
	}
	c.log.Debug("screen captured", slog.String("key", key.String()), slog.Int64("revision", screen.Revision),
		slog.Int("added", added), slog.Int("shift", shift), slog.Int("committed", h.Len()), slog.Bool("truncated", screen.Truncated))
	return h
}

// Since reads a fresh screen, merges it and returns the history lines after
// the last mark (all of them when there is none) and whether a mark exists.
func (c *Capture) Since(ctx context.Context, key domain.Key) (lines []string, marked bool, err error) {
	screen, err := c.read(ctx, key)
	if err != nil {
		return nil, false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.merge(key, screen)
	lines = h.Lines()
	c.log.Debug("history read", slog.String("key", key.String()), slog.Int("lines", len(lines)), slog.Bool("marked", h.Marked()))
	return lines, h.Marked(), nil
}

// history returns the key's history, creating it. The caller holds the lock.
func (c *Capture) history(key domain.Key) *domain.History {
	h, ok := c.hist[key]
	if !ok {
		h = domain.NewHistory()
		c.hist[key] = h
	}
	return h
}
