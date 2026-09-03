package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// Backoff describes the reconnect delay: Min doubled per attempt up to Max,
// with up to Jitter (a fraction of the delay) added on top.
type Backoff struct {
	Min    time.Duration
	Max    time.Duration
	Jitter float64
}

// DefaultBackoff is the reconnect schedule used by the daemon.
var DefaultBackoff = Backoff{Min: 250 * time.Millisecond, Max: 10 * time.Second, Jitter: 0.2}

// Next returns the delay for the given zero-based attempt.
func (b Backoff) Next(attempt int) time.Duration {
	if b.Min <= 0 {
		b.Min = DefaultBackoff.Min
	}
	if b.Max < b.Min {
		b.Max = b.Min
	}
	d := b.Min
	for i := 0; i < attempt && d < b.Max; i++ {
		d *= 2
	}
	if d > b.Max {
		d = b.Max
	}
	if b.Jitter > 0 {
		d += time.Duration(rand.Float64() * b.Jitter * float64(d))
	}
	return d
}

// globalKinds are subscribed once per connection regardless of panes.
var globalKinds = []string{
	"pane.agent_detected",
	"pane.closed",
	"pane.exited",
	"pane.updated",
	"tab.renamed",
	"workspace.renamed",
}

// subscribeTimeout bounds the wait for subscription_started after dialing.
const subscribeTimeout = 5 * time.Second

// drainGrace is how long the old connection keeps being read after a
// replacement subscription is live, so events Herdr already wrote to it
// are delivered before it is closed.
const drainGrace = 100 * time.Millisecond

// stableAfter is how long a subscription must stay up before the reconnect
// attempt counter resets. A connection that drops sooner counts as a failed
// attempt, so a socket that flaps every second climbs the backoff schedule
// instead of reconnecting every Min forever.
const stableAfter = 5 * time.Second

// Stream owns the write-once subscription connection to Herdr.
//
// It subscribes to the global event kinds plus one
// pane.agent_status_changed entry per watched pane, translates envelopes
// into domain.HerdrEvent values and reconnects with backoff when the
// connection drops, emitting a StreamReset so consumers can reconcile.
type Stream struct {
	dial    dialFunc
	path    string
	log     *slog.Logger
	backoff Backoff

	mu      sync.Mutex
	panes   []string
	changed chan struct{}

	drain time.Duration // read grace for the old connection on a swap

	// sleep and now are replaced by tests so backoff delays can be observed
	// without waiting for them.
	sleep func(ctx context.Context, d time.Duration) bool
	now   func() time.Time
}

// NewStream builds a stream; call Run to start it.
func NewStream(dial dialFunc, path string, log *slog.Logger, backoff Backoff) *Stream {
	return &Stream{
		dial:    dial,
		path:    path,
		log:     log,
		backoff: backoff,
		changed: make(chan struct{}, 1),
		drain:   drainGrace,
		sleep:   sleep,
		now:     time.Now,
	}
}

// SetPanes replaces the set of panes whose status changes are streamed.
// The running loop opens a replacement connection with the new set, drains
// what the old one already delivered and only then closes it, so no event
// is lost in between (one pushed while both are live may arrive twice).
func (s *Stream) SetPanes(ids []string) {
	next := slices.Clone(ids)
	slices.Sort(next)
	next = slices.Compact(next)
	s.mu.Lock()
	same := slices.Equal(next, s.panes)
	s.panes = next
	s.mu.Unlock()
	if same {
		return
	}
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

func (s *Stream) currentPanes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.panes)
}

// Run connects, streams events into out and reconnects until ctx is done.
// It returns ctx.Err(). The attempt counter grows on every failed subscribe
// and on every connection that lived shorter than stableAfter; only a
// stable connection resets it.
func (s *Stream) Run(ctx context.Context, out chan<- domain.Event) error {
	attempt := 0
	for {
		conn, err := s.subscribe(ctx, s.currentPanes())
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			delay := s.backoff.Next(attempt)
			attempt++
			s.log.Warn("herdr stream subscribe failed",
				slog.String("err", err.Error()),
				slog.Int("attempt", attempt),
				slog.Int64("retry_ms", delay.Milliseconds()))
			if !s.sleep(ctx, delay) {
				return ctx.Err()
			}
			continue
		}
		connectedAt := s.now()
		err = s.serve(ctx, conn, out)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		lived := s.now().Sub(connectedAt)
		if lived >= stableAfter {
			if attempt > 0 {
				s.log.Debug("herdr stream was stable, backoff reset",
					slog.Int64("lived_ms", lived.Milliseconds()),
					slog.Int("attempts", attempt))
			}
			attempt = 0
		}
		delay := s.backoff.Next(attempt)
		attempt++
		s.log.Warn("herdr stream reset",
			slog.String("err", err.Error()),
			slog.Int64("lived_ms", lived.Milliseconds()),
			slog.Int("attempt", attempt),
			slog.Int64("retry_ms", delay.Milliseconds()))
		if !emit(ctx, out, domain.HerdrEvent{Kind: domain.StreamReset}) {
			return ctx.Err()
		}
		if !s.sleep(ctx, delay) {
			return ctx.Err()
		}
	}
}

// serve reads one connection until it fails, swapping it for a fresh one
// whenever the watch set changes. It returns the read error.
func (s *Stream) serve(ctx context.Context, conn *streamConn, out chan<- domain.Event) error {
	lines, stop := reader(conn)
	defer func() {
		close(stop)
		_ = conn.Close()
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.changed:
			next, err := s.subscribe(ctx, s.currentPanes())
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				s.log.Warn("herdr stream resubscribe failed, keeping old connection",
					slog.String("err", err.Error()))
				// Try again after a short pause without dropping events.
				go func() {
					if sleep(ctx, s.backoff.Next(0)) {
						select {
						case s.changed <- struct{}{}:
						default:
						}
					}
				}()
				continue
			}
			drainErr := s.drainOld(ctx, conn, lines, stop, out)
			conn = next
			lines, stop = reader(conn)
			if drainErr != nil {
				return drainErr
			}
		case r := <-lines:
			if r.err != nil {
				return r.err
			}
			ev, ok := translateEvent(r.line, s.log)
			if !ok {
				continue
			}
			s.log.Debug("herdr event", slog.String("kind", string(ev.Kind)), slog.String("pane_id", ev.PaneID))
			if !emit(ctx, out, ev) {
				return ctx.Err()
			}
		}
	}
}

// drainOld hands over from old to a live replacement: it gives the old
// connection a short read grace, forwards every event it still yields and
// closes it. Only a consumer refusal (ctx done) is an error.
func (s *Stream) drainOld(ctx context.Context, old *streamConn, lines <-chan readResult, stop chan struct{}, out chan<- domain.Event) error {
	defer func() {
		close(stop)
		_ = old.Close()
	}()
	_ = old.SetReadDeadline(time.Now().Add(s.drain))
	drained := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case r := <-lines:
			if r.err != nil {
				s.log.Debug("herdr stream old connection drained", slog.Int("events", drained))
				return nil
			}
			ev, ok := translateEvent(r.line, s.log)
			if !ok {
				continue
			}
			drained++
			if !emit(ctx, out, ev) {
				return ctx.Err()
			}
		}
	}
}

type readResult struct {
	line []byte
	err  error
}

// streamConn pairs a connection with the buffered reader that consumed the
// subscription reply, so bytes read ahead of that reply are not lost.
type streamConn struct {
	net.Conn
	rd *bufio.Reader
}

// reader pumps lines from conn until it fails or stop is closed.
func reader(conn *streamConn) (<-chan readResult, chan struct{}) {
	lines := make(chan readResult)
	stop := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(conn.rd)
		sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
		for sc.Scan() {
			line := slices.Clone(sc.Bytes())
			select {
			case lines <- readResult{line: line}:
			case <-stop:
				return
			}
		}
		err := sc.Err()
		if err == nil {
			err = errors.New("connection closed")
		}
		select {
		case lines <- readResult{err: err}:
		case <-stop:
		}
	}()
	return lines, stop
}

// subscribe dials, writes the single events.subscribe request and waits
// for subscription_started.
func (s *Stream) subscribe(ctx context.Context, panes []string) (*streamConn, error) {
	conn, err := s.dial(ctx, s.path)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", s.path, err)
	}
	subs := make([]subscription, 0, len(globalKinds)+len(panes))
	for _, k := range globalKinds {
		subs = append(subs, subscription{Type: k})
	}
	for _, p := range panes {
		subs = append(subs, subscription{Type: "pane.agent_status_changed", PaneID: p})
	}
	line, err := json.Marshal(request{ID: "sub", Method: "events.subscribe", Params: subscribeParams{Subscriptions: subs}})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("encode subscribe: %w", err)
	}
	deadline := time.Now().Add(subscribeTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	if _, err := conn.Write(append(line, '\n')); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("write subscribe: %w", err)
	}
	rd := bufio.NewReaderSize(conn, 64<<10)
	raw, err := rd.ReadBytes('\n')
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("await subscription_started: %w", err)
	}
	var resp response
	if err := json.Unmarshal(raw, &resp); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("decode subscribe reply: %w", err)
	}
	if resp.Error != nil {
		_ = conn.Close()
		return nil, &APIError{Code: resp.Error.Code, Message: resp.Error.Message}
	}
	var res struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(resp.Result, &res); err != nil || res.Type != "subscription_started" {
		_ = conn.Close()
		return nil, fmt.Errorf("unexpected subscribe reply %s", strings.TrimSpace(string(raw)))
	}
	_ = conn.SetDeadline(time.Time{})
	s.log.Info("herdr stream subscribed", slog.Int("panes", len(panes)), slog.Int("kinds", len(globalKinds)))
	return &streamConn{Conn: conn, rd: rd}, nil
}

// translateEvent maps one envelope line to a domain event. Unknown kinds
// return ok=false after a debug log.
func translateEvent(line []byte, log *slog.Logger) (domain.HerdrEvent, bool) {
	var env eventEnvelope
	if err := json.Unmarshal(line, &env); err != nil || env.Event == "" {
		log.Debug("herdr stream ignoring line", slog.Int("bytes", len(line)))
		return domain.HerdrEvent{}, false
	}
	kind := strings.ReplaceAll(env.Event, ".", "_")
	switch kind {
	case "pane_agent_detected":
		var d paneAgentDetectedData
		if err := json.Unmarshal(env.Data, &d); err != nil {
			return bad(log, kind, err)
		}
		ev := domain.HerdrEvent{Kind: domain.PaneAgentDetected, PaneID: d.PaneID, WorkspaceID: d.WorkspaceID, Released: d.Released}
		if d.Agent != nil {
			ev.Agent = &domain.Agent{Key: domain.Key{PaneID: d.PaneID}, WorkspaceID: d.WorkspaceID, Kind: *d.Agent, Status: domain.StatusUnknown}
		}
		if d.FinalStatus != nil {
			st := domain.ParseStatus(*d.FinalStatus)
			ev.FinalStatus = &st
		}
		return ev, true
	case "pane_agent_status_changed":
		var d paneAgentStatusChangedData
		if err := json.Unmarshal(env.Data, &d); err != nil {
			return bad(log, kind, err)
		}
		return domain.HerdrEvent{
			Kind: domain.PaneAgentStatusChanged, PaneID: d.PaneID, WorkspaceID: d.WorkspaceID,
			Agent: &domain.Agent{
				Key: domain.Key{PaneID: d.PaneID}, WorkspaceID: d.WorkspaceID, Kind: d.Agent,
				Title: cleanTitle(d.Title), Status: domain.ParseStatus(d.AgentStatus),
			},
		}, true
	case "pane_closed":
		var d paneClosedData
		if err := json.Unmarshal(env.Data, &d); err != nil {
			return bad(log, kind, err)
		}
		return domain.HerdrEvent{Kind: domain.PaneClosed, PaneID: d.PaneID, WorkspaceID: d.WorkspaceID}, true
	case "pane_exited":
		var d paneExitedData
		if err := json.Unmarshal(env.Data, &d); err != nil {
			return bad(log, kind, err)
		}
		return domain.HerdrEvent{Kind: domain.PaneExited, PaneID: d.PaneID, WorkspaceID: d.WorkspaceID}, true
	case "pane_updated":
		var d paneUpdatedData
		if err := json.Unmarshal(env.Data, &d); err != nil {
			return bad(log, kind, err)
		}
		a := toDomainAgent(d.Pane)
		return domain.HerdrEvent{Kind: domain.PaneUpdated, PaneID: a.PaneID, WorkspaceID: a.WorkspaceID, TabID: a.TabID, Agent: &a}, true
	case "tab_renamed":
		var d tabRenamedData
		if err := json.Unmarshal(env.Data, &d); err != nil {
			return bad(log, kind, err)
		}
		return domain.HerdrEvent{Kind: domain.TabRenamed, TabID: d.TabID, Label: d.Label}, true
	case "workspace_renamed":
		var d workspaceRenamedData
		if err := json.Unmarshal(env.Data, &d); err != nil {
			return bad(log, kind, err)
		}
		return domain.HerdrEvent{Kind: domain.WorkspaceRenamed, WorkspaceID: d.WorkspaceID, Label: d.Label}, true
	default:
		log.Debug("herdr stream unknown event", slog.String("kind", env.Event))
		return domain.HerdrEvent{}, false
	}
}

func bad(log *slog.Logger, kind string, err error) (domain.HerdrEvent, bool) {
	log.Debug("herdr stream bad event data", slog.String("kind", kind), slog.String("err", err.Error()))
	return domain.HerdrEvent{}, false
}

func emit(ctx context.Context, out chan<- domain.Event, ev domain.HerdrEvent) bool {
	select {
	case out <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
