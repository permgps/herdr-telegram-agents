package app

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// AgentEventKind names the normalized events the registry produces.
type AgentEventKind string

const (
	AgentAppeared AgentEventKind = "appeared"
	AgentChanged  AgentEventKind = "changed"
	AgentGone     AgentEventKind = "gone"
)

// AgentEvent is one change in the set of live agents. For AgentGone the
// agent carries its last known label and StatusExited.
type AgentEvent struct {
	Kind  AgentEventKind
	Agent domain.Agent
}

// Registry merges Herdr socket events with agent.list snapshots into a
// de-duplicated stream of AgentEvents. Snapshots are the source of truth;
// status and pane-update events are applied immediately so the topic
// reacts before the next snapshot, and structural events (a pane appears,
// closes or exits, an agent is detected or released) schedule a snapshot.
type Registry struct {
	herdr domain.HerdrGateway
	clock domain.Clock
	log   *slog.Logger

	// Interval and Coalesce default to the package constants; tests shorten
	// them or drive the fake clock.
	Interval time.Duration
	Coalesce time.Duration

	mu        sync.Mutex
	agents    map[domain.Key]domain.Agent
	byPane    map[string]domain.Key
	lastOK    time.Time
	lastErr   error
	lastErrAt time.Time

	request chan struct{}
}

// NewRegistry returns an empty registry.
func NewRegistry(herdr domain.HerdrGateway, clock domain.Clock, log *slog.Logger) *Registry {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Registry{
		herdr:    herdr,
		clock:    clock,
		log:      log,
		Interval: reconcileInterval,
		Coalesce: snapshotCoalesce,
		agents:   map[domain.Key]domain.Agent{},
		byPane:   map[string]domain.Key{},
		request:  make(chan struct{}, 1),
	}
}

// Live returns the current agents in key order.
func (r *Registry) Live() []domain.Agent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.Agent, 0, len(r.agents))
	for _, a := range r.agents {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key.String() < out[j].Key.String() })
	return out
}

// Agent returns the current view of one live agent; ok is false once the
// agent has exited. It is safe from any goroutine.
func (r *Registry) Agent(key domain.Key) (domain.Agent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.agents[key]
	return a, ok
}

// Health describes the registry's contact with the Herdr socket.
type Health struct {
	LastOK    time.Time // last successful snapshot
	LastErr   error     // last snapshot failure, nil after a success
	LastErrAt time.Time // when LastErr happened
}

// Health reports when the last snapshot succeeded and the last failure.
func (r *Registry) Health() Health {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Health{LastOK: r.lastOK, LastErr: r.lastErr, LastErrAt: r.lastErrAt}
}

// RequestSnapshot asks the running loop for a snapshot as soon as possible.
// It never blocks; a pending request is enough.
func (r *Registry) RequestSnapshot() {
	select {
	case r.request <- struct{}{}:
	default:
	}
}

// Snapshot lists the agents, diffs them against the registry and updates
// the watched pane set. It returns the resulting events in a stable order:
// gone, then appeared, then changed.
func (r *Registry) Snapshot(ctx context.Context) ([]AgentEvent, error) {
	agents, err := r.herdr.ListAgents(ctx)
	now := r.clock.Now()
	if err != nil {
		r.mu.Lock()
		r.lastErr, r.lastErrAt = err, now
		r.mu.Unlock()
		r.log.Warn("agent snapshot failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("agent snapshot: %w", err)
	}
	events, panes := r.applySnapshot(agents, now)
	if err := r.herdr.WatchPanes(ctx, panes); err != nil {
		r.log.Warn("watch panes failed", slog.String("err", err.Error()))
	}
	return events, nil
}

func (r *Registry) applySnapshot(agents []domain.Agent, now time.Time) ([]AgentEvent, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastOK, r.lastErr = now, nil

	next := make(map[domain.Key]domain.Agent, len(agents))
	for _, a := range agents {
		if a.Kind == "" {
			continue
		}
		next[a.Key] = a
	}
	var gone, appeared, changed []AgentEvent
	for key, old := range r.agents {
		if _, ok := next[key]; !ok {
			gone = append(gone, AgentEvent{Kind: AgentGone, Agent: exited(old)})
		}
	}
	for key, a := range next {
		old, ok := r.agents[key]
		switch {
		case !ok:
			appeared = append(appeared, AgentEvent{Kind: AgentAppeared, Agent: a})
		case differs(old, a):
			changed = append(changed, AgentEvent{Kind: AgentChanged, Agent: a})
		}
	}
	r.agents = next
	r.byPane = make(map[string]domain.Key, len(next))
	panes := make([]string, 0, len(next))
	for key := range next {
		r.byPane[key.PaneID] = key
		panes = append(panes, key.PaneID)
	}
	sort.Strings(panes)
	events := append(append(sortEvents(gone), sortEvents(appeared)...), sortEvents(changed)...)
	r.log.Debug("agent snapshot applied",
		slog.Int("agents", len(next)), slog.Int("appeared", len(appeared)),
		slog.Int("changed", len(changed)), slog.Int("gone", len(gone)))
	return events, panes
}

// Apply folds one socket event into the registry. It returns the events to
// emit right away and whether a snapshot should be scheduled.
func (r *Registry) Apply(ev domain.HerdrEvent) (events []AgentEvent, structural bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log.Debug("herdr event", slog.String("kind", string(ev.Kind)), slog.String("pane", ev.PaneID))
	switch ev.Kind {
	case domain.PaneAgentStatusChanged:
		return r.applyStatus(ev)
	case domain.PaneUpdated:
		return r.applyUpdate(ev)
	case domain.StreamReset, domain.PaneAgentDetected, domain.PaneClosed, domain.PaneExited, domain.TabRenamed, domain.WorkspaceRenamed:
		return nil, true
	default:
		return nil, false
	}
}

func (r *Registry) applyStatus(ev domain.HerdrEvent) ([]AgentEvent, bool) {
	key, ok := r.byPane[ev.PaneID]
	if !ok || ev.Agent == nil {
		return nil, true
	}
	old := r.agents[key]
	a := old
	a.Status = ev.Agent.Status
	if ev.Agent.Title != "" {
		a.Title = ev.Agent.Title
	}
	r.agents[key] = a
	if !differs(old, a) {
		return nil, false
	}
	r.log.Debug("agent status applied", slog.String("key", key.String()), slog.String("status", string(a.Status)))
	return []AgentEvent{{Kind: AgentChanged, Agent: a}}, false
}

func (r *Registry) applyUpdate(ev domain.HerdrEvent) ([]AgentEvent, bool) {
	if ev.Agent == nil || ev.Agent.Kind == "" {
		// Not an agent pane; a pane that lost its agent is reported by
		// PaneAgentDetected with Released, which is structural.
		return nil, false
	}
	a := *ev.Agent
	old, ok := r.agents[a.Key]
	if !ok {
		// New key (new agent, or a replacement in a known pane): let the
		// snapshot introduce it so the old key is retired in the same pass.
		return nil, true
	}
	// Pane events carry no workspace or tab labels; keep the ones the last
	// snapshot resolved so the label does not flap between event and pass.
	if a.WorkspaceLabel == "" {
		a.WorkspaceLabel = old.WorkspaceLabel
	}
	if a.TabLabel == "" {
		a.TabLabel = old.TabLabel
	}
	r.agents[a.Key] = a
	if !differs(old, a) {
		return nil, false
	}
	r.log.Debug("agent update applied", slog.String("key", a.Key.String()), slog.String("label", a.Label()), slog.String("status", string(a.Status)))
	return []AgentEvent{{Kind: AgentChanged, Agent: a}}, false
}

// Run drives the registry until ctx is done: it applies socket events,
// takes periodic snapshots, coalesces structural events into one snapshot
// and serves RequestSnapshot. Events are delivered on out in order.
func (r *Registry) Run(ctx context.Context, out chan<- AgentEvent) error {
	tick := r.clock.After(r.Interval)
	var coalesce <-chan time.Time
	events := r.herdr.Events()
	emit := func(evs []AgentEvent) bool {
		for _, ev := range evs {
			select {
			case out <- ev:
			case <-ctx.Done():
				return false
			}
		}
		return true
	}
	snapshot := func(reason string) bool {
		r.log.Debug("agent snapshot", slog.String("reason", reason))
		evs, _ := r.Snapshot(ctx)
		coalesce = nil
		tick = r.clock.After(r.Interval)
		return emit(evs)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case raw, ok := <-events:
			if !ok {
				r.log.Warn("herdr events channel closed")
				events = nil
				continue
			}
			ev, isHerdr := raw.(domain.HerdrEvent)
			if !isHerdr {
				continue
			}
			evs, structural := r.Apply(ev)
			if !emit(evs) {
				return ctx.Err()
			}
			if ev.Kind == domain.StreamReset {
				if !snapshot("stream reset") {
					return ctx.Err()
				}
				continue
			}
			if structural && coalesce == nil {
				coalesce = r.clock.After(r.Coalesce)
			}
		case <-coalesce:
			if !snapshot("structural event") {
				return ctx.Err()
			}
		case <-tick:
			if !snapshot("interval") {
				return ctx.Err()
			}
		case <-r.request:
			if !snapshot("request") {
				return ctx.Err()
			}
		}
	}
}

func differs(a, b domain.Agent) bool {
	return a.Label() != b.Label() || a.Status != b.Status
}

func exited(a domain.Agent) domain.Agent {
	a.Status = domain.StatusExited
	return a
}

func sortEvents(evs []AgentEvent) []AgentEvent {
	sort.Slice(evs, func(i, j int) bool { return evs[i].Agent.Key.String() < evs[j].Agent.Key.String() })
	return evs
}
