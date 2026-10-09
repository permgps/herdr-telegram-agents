package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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
	privateFrames map[domain.Key][]privateFrame
	herdr         domain.HerdrGateway
	live          func() []domain.Agent
	clock         domain.Clock
	log           *slog.Logger

	mu     sync.Mutex
	hist   map[domain.Key]*domain.History
	last   map[domain.Key]string              // SHA-256 of the last screen merged per key
	source map[domain.Key]domain.ScreenSource // last successful read source per key
	status map[domain.Key]domain.Status
	left   map[domain.Key]time.Time // when the agent last left working
	// busy holds keys whose recent read Herdr ever refused with
	// agent_not_idle, which marks a full-screen (alternate-screen) agent.
	// Herdr refuses such reads while the agent works and serves them while
	// it is idle by scrolling the agent's transcript with the mouse wheel,
	// so the user sees the pane run from top to bottom. A busy key reads
	// visible for the rest of its life; only AgentGone clears it.
	busy map[domain.Key]bool

	// Interval is the tick between reads; Grace keeps reading after an
	// agent left working so its final screen is committed; MinAway is the
	// shortest pause outside working that counts as a human message;
	// ReadTimeout bounds one agent.read. Tests shorten them.
	Interval    time.Duration
	Grace       time.Duration
	MinAway     time.Duration
	ReadTimeout time.Duration
}

// NewCapture wires the capture over the Herdr port and the registry's live
// agent list.
func NewCapture(herdr domain.HerdrGateway, live func() []domain.Agent, clock domain.Clock, log *slog.Logger) *Capture {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Capture{
		herdr:         herdr,
		live:          live,
		clock:         clock,
		log:           log,
		hist:          map[domain.Key]*domain.History{},
		privateFrames: map[domain.Key][]privateFrame{},
		last:          map[domain.Key]string{},
		source:        map[domain.Key]domain.ScreenSource{},
		status:        map[domain.Key]domain.Status{},
		left:          map[domain.Key]time.Time{},
		busy:          map[domain.Key]bool{},
		Interval:      captureInterval,
		Grace:         captureGrace,
		MinAway:       captureMarkMinAway,
		ReadTimeout:   captureReadTimeout,
	}
}

// Observe folds one registry event into the marks: a transition into
// working after at least MinAway outside it marks the history, a
// transition out of working starts the grace period, and a gone agent is
// forgotten. It never blocks.
func (c *Capture) Observe(ev AgentEvent) {
	key := ev.Agent.Key
	c.mu.Lock()
	defer c.mu.Unlock()
	if ev.Kind == AgentGone {
		delete(c.hist, key)
		delete(c.privateFrames, key)
		delete(c.last, key)
		delete(c.source, key)
		delete(c.status, key)
		delete(c.left, key)
		delete(c.busy, key)
		c.log.Debug("history dropped", slog.String("key", key.String()))
		return
	}
	if from := ev.ReassociatedFrom; from != nil {
		c.moveHistory(*from, key)
		moveAgentState(c.last, *from, key)
		moveAgentState(c.source, *from, key)
		moveAgentState(c.status, *from, key)
		moveAgentState(c.left, *from, key)
		moveAgentState(c.busy, *from, key)
		c.log.Debug("history reassociated", slog.String("old_key", from.String()), slog.String("new_key", key.String()))
	}
	prev, known := c.status[key]
	cur := ev.Agent.Status
	c.status[key] = cur
	switch {
	case cur == domain.StatusWorking && (!known || prev != domain.StatusWorking):
		// Herdr's detection flaps between working and idle or blocked
		// for a second or two while an agent runs a tool; only a pause
		// long enough for a human to have answered counts as a message.
		away := c.MinAway
		if at, ok := c.left[key]; ok {
			away = c.clock.Now().Sub(at)
			delete(c.left, key)
		}
		if known && away < c.MinAway {
			c.log.Debug("history mark skipped", slog.String("key", key.String()),
				slog.String("from", string(prev)), slog.Int64("away_ms", away.Milliseconds()))
			return
		}
		h := c.history(key)
		h.Mark()
		c.log.Debug("history marked", slog.String("key", key.String()),
			slog.String("from", string(prev)), slog.String("to", string(cur)),
			slog.Int64("away_ms", away.Milliseconds()), slog.Int("committed", h.Len()))
	case known && prev == domain.StatusWorking && cur != domain.StatusWorking:
		c.left[key] = c.clock.Now()
		c.log.Debug("capture grace started", slog.String("key", key.String()), slog.String("to", string(cur)))
	}
}

func (c *Capture) moveHistory(from, to domain.Key) {
	previous, hadPrevious := c.hist[from]
	current, hadCurrent := c.hist[to]
	if hadPrevious && hadCurrent {
		previous.Append(current.Lines())
		c.hist[to] = previous
		delete(c.hist, from)
		return
	}
	moveAgentState(c.hist, from, to)
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
		c.capture(ctx, a.Key, a.Status == domain.StatusWorking)
	}
}

func (c *Capture) inGrace(key domain.Key, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	at, ok := c.left[key]
	return ok && now.Sub(at) <= c.Grace
}

// capture reads one screen and merges it. Read failures are logged and left
// to the next tick.
func (c *Capture) capture(ctx context.Context, key domain.Key, working bool) {
	screen, source, err := c.read(ctx, key, working)
	if err != nil {
		c.log.Warn("capture read failed", slog.String("key", key.String()), slog.String("err", err.Error()))
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.merge(key, screen, source)
}

// read prefers the recent source, which carries scrollback, and falls back
// to visible when Herdr refuses it as busy. A key already refused goes
// straight to visible, working or not.
func (c *Capture) read(ctx context.Context, key domain.Key, working bool) (domain.Screen, domain.ScreenSource, error) {
	rctx, cancel := context.WithTimeout(ctx, c.ReadTimeout)
	defer cancel()
	c.mu.Lock()
	skipRecent := c.busy[key]
	c.mu.Unlock()
	if skipRecent {
		if !working {
			c.log.Debug("[FIX] recent read skipped for a full-screen agent", slog.String("key", key.String()))
		}
		visible, err := c.herdr.ReadScreen(rctx, key.PaneID, domain.ScreenVisible, captureLines)
		if err != nil {
			return domain.Screen{}, "", err
		}
		return visible, domain.ScreenVisible, nil
	}
	screen, err := c.herdr.ReadScreen(rctx, key.PaneID, domain.ScreenRecent, captureLines)
	if err == nil {
		return screen, domain.ScreenRecent, nil
	}
	if !errors.Is(err, domain.ErrAgentBusy) || rctx.Err() != nil {
		return domain.Screen{}, "", err
	}
	c.mu.Lock()
	c.busy[key] = true
	c.mu.Unlock()
	c.log.Info("[FIX] recent screen busy, reading visible for the agent's lifetime",
		slog.String("key", key.String()), slog.Bool("working", working), slog.String("err", err.Error()))

	visible, visibleErr := c.herdr.ReadScreen(rctx, key.PaneID, domain.ScreenVisible, captureLines)
	if visibleErr != nil {
		return domain.Screen{}, "", fmt.Errorf("recent screen read failed: %w; visible fallback failed: %w", err, visibleErr)
	}
	c.log.Debug("capture used visible screen fallback",
		slog.String("key", key.String()),
		slog.String("err", err.Error()))
	return visible, domain.ScreenVisible, nil
}

// merge appends the screen to the key's history unless it equals the last
// merged one. Herdr 0.7.5 reports revision 0 for agent.read, so the text
// hash, not the revision, decides. The caller holds the lock.
func (c *Capture) merge(key domain.Key, screen domain.Screen, source domain.ScreenSource) *domain.History {
	h := c.history(key)
	previousSource := c.source[key]
	c.source[key] = source
	hash := hashText(screen.Text)
	if c.last[key] == hash {
		c.log.Debug("screen unchanged", slog.String("key", key.String()),
			slog.String("source", string(source)), slog.Int("committed", h.Len()))
		return h
	}
	var added, shift int
	var gap bool
	if previousSource == domain.ScreenVisible && source == domain.ScreenRecent {
		added, shift, gap = h.AppendRecentAfterVisible(screenLines(screen.Text))
	} else {
		added, shift, gap = h.Append(screenLines(screen.Text))
	}
	c.last[key] = hash
	c.recordPrivateFrame(key, screen.Text)
	if gap {
		c.log.Warn("screen history gap", slog.String("key", key.String()),
			slog.String("source", string(source)), slog.Int64("revision", screen.Revision),
			slog.Int("added", added), slog.Int("committed", h.Len()))
		return h
	}
	c.log.Debug("screen captured", slog.String("key", key.String()), slog.String("source", string(source)),
		slog.Int64("revision", screen.Revision),
		slog.Int("added", added), slog.Int("shift", shift), slog.Int("committed", h.Len()), slog.Bool("truncated", screen.Truncated))
	return h
}

// Since reads a fresh screen, merges it and returns the history lines after
// the last mark (all of them when there is none) and whether a mark exists.
func (c *Capture) Since(ctx context.Context, key domain.Key) (lines []string, marked bool, err error) {
	c.mu.Lock()
	working := c.status[key] == domain.StatusWorking
	c.mu.Unlock()
	screen, source, err := c.read(ctx, key, working)
	if err != nil {
		return nil, false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.merge(key, screen, source)
	lines = h.Lines()
	c.log.Debug("history read", slog.String("key", key.String()), slog.String("source", string(source)),
		slog.Int("lines", len(lines)), slog.Bool("marked", h.Marked()))
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

// Private frames share the existing capture loop. No old owner history buffer
// is consulted by private exports, including after regrant or restart.
type privateFrame struct {
	at   time.Time
	text string
}

func (c *Capture) recordPrivateFrame(key domain.Key, text string) {
	frames := append(c.privateFrames[key], privateFrame{at: c.clock.Now(), text: text})
	bytes, lines := 0, 0
	start := len(frames)
	for start > 0 {
		f := frames[start-1]
		if bytes+len(f.text) > domain.HistoryMaxBytes || lines+strings.Count(f.text, "\n")+1 > domain.HistoryMaxLines {
			break
		}
		bytes += len(f.text)
		lines += strings.Count(f.text, "\n") + 1
		start--
	}
	c.privateFrames[key] = append([]privateFrame(nil), frames[start:]...)
}
func (c *Capture) PrivateSince(key domain.Key, activation time.Time) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var parts []string
	for _, f := range c.privateFrames[key] {
		if f.at.After(activation) {
			parts = append(parts, f.text)
		}
	}
	return strings.Join(parts, "\n…\n")
}
