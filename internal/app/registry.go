package app

import (
	"context"
	"errors"
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
	// ReassociatedFrom is set by the daemon when the reconciler retained
	// this agent's existing Telegram thread under a replacement key.
	ReassociatedFrom *domain.Key
}

// Registry merges Herdr socket events with agent.list snapshots into a
// de-duplicated stream of AgentEvents. Snapshots are the source of truth;
// status and pane-update events are applied immediately so the topic
// reacts before the next snapshot, and structural events (a pane appears,
// closes or exits, an agent is detected or released) schedule a snapshot.
//
// An event that shows a known key is no longer the pane's agent (a new
// session in the same pane, a close, an exit, a release) retires that key
// at once: Agent stops vouching for it while Live keeps it, so private
// sharing, which authorizes through Agent and acts on the pane id, cannot
// reach the next session before a snapshot reconciles.
type Registry struct {
	herdr domain.HerdrGateway
	clock domain.Clock
	log   *slog.Logger

	// Interval, Coalesce and SnapshotTimeout default to the package
	// constants; tests shorten them or drive the fake clock.
	Interval        time.Duration
	Coalesce        time.Duration
	SnapshotTimeout time.Duration

	mu        sync.Mutex
	agents    map[domain.Key]domain.Agent
	byPane    map[string]domain.Key
	retired   map[domain.Key]bool // keys an event superseded; cleared by snapshots
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
		herdr:           herdr,
		clock:           clock,
		log:             log,
		Interval:        reconcileInterval,
		Coalesce:        snapshotCoalesce,
		SnapshotTimeout: snapshotTimeout,
		agents:          map[domain.Key]domain.Agent{},
		byPane:          map[string]domain.Key{},
		retired:         map[domain.Key]bool{},
		request:         make(chan struct{}, 1),
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
// agent has exited, and from the moment an event retires its key until a
// snapshot lists it again. It is safe from any goroutine.
func (r *Registry) Agent(key domain.Key) (domain.Agent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retired[key] {
		return domain.Agent{}, false
	}
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
// the watched pane set. It returns the resulting events in a stable order.
// A replacement that still identifies the same agent appears before its
// previous key goes away, so the reconciler can move the topic first.
// Both gateway calls run under SnapshotTimeout, so a Herdr that never
// answers costs one failed snapshot and the loop goes on to the next tick.
func (r *Registry) Snapshot(ctx context.Context) ([]AgentEvent, error) {
	ctx, cancel := context.WithTimeout(ctx, r.SnapshotTimeout)
	defer cancel()
	agents, err := r.herdr.ListAgents(ctx)
	now := r.clock.Now()
	if err != nil {
		r.mu.Lock()
		r.lastErr, r.lastErrAt = err, now
		r.mu.Unlock()
		attrs := []any{slog.String("err", err.Error())}
		if errors.Is(err, context.DeadlineExceeded) {
			attrs = append(attrs, slog.Bool("timeout", true))
		}
		r.log.Warn("agent snapshot failed", attrs...)
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
	clear(r.retired)
	r.byPane = make(map[string]domain.Key, len(next))
	panes := make([]string, 0, len(next))
	for key := range next {
		r.byPane[key.PaneID] = key
		panes = append(panes, key.PaneID)
	}
	sort.Strings(panes)
	events := orderSnapshotEvents(gone, appeared, changed)
	r.log.Debug("agent snapshot applied",
		slog.Int("agents", len(next)), slog.Int("appeared", len(appeared)),
		slog.Int("changed", len(changed)), slog.Int("gone", len(gone)))
	return events, panes
}

// orderSnapshotEvents puts an appeared replacement ahead of its gone key
// only when the two agents share a known session or satisfy the same
// conservative metadata fallback used by topic reassociation. Other
// replacements keep the normal exit-before-create order.
func orderSnapshotEvents(gone, appeared, changed []AgentEvent) []AgentEvent {
	gone = sortEvents(gone)
	appeared = sortEvents(appeared)
	changed = sortEvents(changed)

	pairedAppeared := make(map[int]bool)
	oldOwners := make(map[int]int)
	newMatches := make(map[int]int)
	for newIndex, next := range appeared {
		for oldIndex, prev := range gone {
			if sameAgentRestart(prev.Agent, next.Agent) {
				newMatches[newIndex]++
				oldOwners[oldIndex]++
			}
		}
	}
	for newIndex := range appeared {
		if newMatches[newIndex] != 1 {
			continue
		}
		for oldIndex, prev := range gone {
			if oldOwners[oldIndex] == 1 && sameAgentRestart(prev.Agent, appeared[newIndex].Agent) {
				pairedAppeared[newIndex] = true
				break
			}
		}
	}

	events := make([]AgentEvent, 0, len(gone)+len(appeared)+len(changed))
	for i, ev := range appeared {
		if pairedAppeared[i] {
			events = append(events, ev)
		}
	}
	events = append(events, gone...)
	for i, ev := range appeared {
		if !pairedAppeared[i] {
			events = append(events, ev)
		}
	}
	return append(events, changed...)
}

// sameAgentRestart is the event-ordering counterpart of the mapping's
// session-first matcher. It is deliberately conservative when either
// session digest is absent, so a known identity conflict is never reordered
// as a reusable agent.
func sameAgentRestart(previous, next domain.Agent) bool {
	if previous.Key == next.Key || previous.Key.PaneID == "" || previous.Key.PaneID != next.Key.PaneID {
		return false
	}
	if previous.Key.SessionDigest != "" && next.Key.SessionDigest != "" {
		return previous.Key.SameSession(next.Key)
	}
	if previous.Key.ConflictsWith(next.Key) || next.Cwd == "" || (previous.Cwd != "" && previous.Cwd != next.Cwd) {
		return false
	}
	if previous.Kind != "" && previous.Kind != next.Kind {
		return false
	}
	previousName, _ := domain.Desired(previous)
	nextName, _ := domain.Desired(next)
	return previousName == nextName
}

func moveAgentState[T any](state map[domain.Key]T, from, to domain.Key) {
	if value, ok := state[from]; ok {
		if _, exists := state[to]; !exists {
			state[to] = value
		}
		delete(state, from)
	}
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
	case domain.PaneClosed, domain.PaneExited:
		r.retirePane(ev.PaneID, string(ev.Kind))
		return nil, true
	case domain.PaneAgentDetected:
		if ev.Released {
			r.retirePane(ev.PaneID, "released")
		}
		return nil, true
	case domain.StreamReset, domain.TabRenamed, domain.WorkspaceRenamed:
		return nil, true
	default:
		return nil, false
	}
}

// retirePane retires the key a pane currently holds, if any. The caller
// holds r.mu.
func (r *Registry) retirePane(paneID, reason string) {
	key, ok := r.byPane[paneID]
	if !ok || r.retired[key] {
		return
	}
	r.retired[key] = true
	r.log.Info("[FIX] agent key retired until the next snapshot", slog.String("key", key.String()), slog.String("reason", reason))
}

func (r *Registry) applyStatus(ev domain.HerdrEvent) ([]AgentEvent, bool) {
	key, ok := r.byPane[ev.PaneID]
	// A retired key no longer names the pane's agent; the status belongs
	// to whatever replaced it, which only a snapshot can introduce.
	if !ok || ev.Agent == nil || r.retired[key] {
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
		// snapshot introduce it so the old key leaves in the same pass, and
		// retire the pane's old key now, so nothing authorizes the next
		// session through it in the meantime.
		r.retirePane(a.Key.PaneID, "replaced")
		return nil, true
	}
	if r.retired[a.Key] {
		// Only a snapshot brings a retired key back.
		return nil, true
	}
	// Pane events carry no workspace or tab labels and no agent name
	// (pane.updated has no name field at all, verified against Herdr
	// 0.7.5); keep what the last snapshot resolved so the label does not
	// flap between event and pass. Names change only through snapshots.
	if a.WorkspaceLabel == "" {
		a.WorkspaceLabel = old.WorkspaceLabel
	}
	if a.TabLabel == "" {
		a.TabLabel = old.TabLabel
	}
	a.Name = old.Name
	if a.Cwd == "" {
		a.Cwd = old.Cwd
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
	return a.Label() != b.Label() || a.Status != b.Status || a.Kind != b.Kind || a.Cwd != b.Cwd
}

func exited(a domain.Agent) domain.Agent {
	a.Status = domain.StatusExited
	return a
}

func sortEvents(evs []AgentEvent) []AgentEvent {
	sort.Slice(evs, func(i, j int) bool { return evs[i].Agent.Key.String() < evs[j].Agent.Key.String() })
	return evs
}
