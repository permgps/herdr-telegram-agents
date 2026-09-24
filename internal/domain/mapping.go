package domain

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// MappingVersion is the schema version written to mapping.json.
const MappingVersion = 2

// TopicEntry is what the plugin last wrote to Telegram for one agent.
// Telegram offers no way to read a topic back, so Name and Status are the
// values of the last successful create or edit, not the live state.
//
// Muted is set when an operator closed the topic by hand: the mirror then
// sends no edits and posts no screens until the topic is reopened.
type TopicEntry struct {
	ThreadID  int
	Name      string
	Status    Status
	Closed    bool
	Muted     bool
	Cwd       string
	AgentKind string
	// LegacyNoCwd preserves the v1 fallback allowance until cwd is learned.
	LegacyNoCwd bool
	UpdatedAt   time.Time
}

// ReassociationReason describes why a live agent was assigned to an existing
// mapping entry.
type ReassociationReason string

const (
	ReassociationExact              ReassociationReason = "exact_identity"
	ReassociationSession            ReassociationReason = "same_session"
	ReassociationFallback           ReassociationReason = "fallback"
	ReassociationAmbiguousSession   ReassociationReason = "ambiguous_session"
	ReassociationAmbiguousFallback  ReassociationReason = "ambiguous_fallback"
	ReassociationAmbiguousManyToOne ReassociationReason = "many_to_one"
)

// ReassociationAssignment selects one existing entry for one live agent.
// From and To may be equal for an exact identity match.
type ReassociationAssignment struct {
	From   Key
	To     Key
	Reason ReassociationReason
}

// ReassociationAmbiguity records a live agent for which the matcher refused
// to choose among candidate mapping entries.
type ReassociationAmbiguity struct {
	LiveKey        Key
	CandidateCount int
	Reason         ReassociationReason
}

// ReassociationPlan is the deterministic result of matching a live snapshot
// against stored mapping entries.
type ReassociationPlan struct {
	Assignments []ReassociationAssignment
	Ambiguous   []ReassociationAmbiguity
}

type mappingCandidate struct {
	storageKey string
	key        Key
	entry      *TopicEntry
}

type reassociationCandidateSet struct {
	agent      Agent
	candidates []mappingCandidate
}

// Label returns the agent label stored in the topic name, without prefix.
func (e *TopicEntry) Label() string {
	return StripPrefix(e.Name)
}

// Mapping is the aggregate linking agent keys to forum topics. It is mutated
// in memory by the reconciler and persisted after every successful Telegram
// call. Keys are stored as strings (Key.String) so the JSON file stays flat.
//
// Dashboard is the id of the pinned status message in General, 0 when none
// has been created. The field is optional in the file so an older binary
// loads and saves the mapping without it (leaving one stale pinned message
// behind after a downgrade).
// PendingCreates and PendingDashboard record durable intent before Telegram
// creates a topic or dashboard message, which cannot be replayed safely after
// an ambiguous failure.
type Mapping struct {
	Version          int
	ChatID           int64
	Topics           map[string]*TopicEntry
	Dashboard        int
	PendingCreates   map[string]bool
	PendingDashboard bool
}

// NewMapping returns an empty mapping for the given chat.
func NewMapping(chatID int64) *Mapping {
	return &Mapping{Version: MappingVersion, ChatID: chatID, Topics: map[string]*TopicEntry{}, PendingCreates: map[string]bool{}}
}

// ParseKey is the inverse of Key.String. It accepts the legacy
// "<pane>/<terminal>" form and the versioned session-aware form.
func ParseKey(s string) (Key, bool) {
	if strings.HasPrefix(s, "v2:") {
		parts := strings.Split(strings.TrimPrefix(s, "v2:"), ":")
		if len(parts) != 3 {
			return Key{}, false
		}
		pane, err := base64.RawURLEncoding.DecodeString(parts[0])
		if err != nil || len(pane) == 0 || base64.RawURLEncoding.EncodeToString(pane) != parts[0] {
			return Key{}, false
		}
		term, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil || len(term) == 0 || base64.RawURLEncoding.EncodeToString(term) != parts[1] {
			return Key{}, false
		}
		digest, err := hex.DecodeString(parts[2])
		if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != parts[2] {
			return Key{}, false
		}
		return Key{PaneID: string(pane), TerminalID: string(term), SessionDigest: parts[2]}, true
	}

	pane, term, ok := strings.Cut(s, "/")
	if !ok || pane == "" || term == "" {
		return Key{}, false
	}
	return Key{PaneID: pane, TerminalID: term}, true
}

// PlanReassociation matches stored topics to the live agents in a snapshot.
// It returns exact identity matches, safe session/fallback matches and any
// ambiguities without changing the mapping.
func (m *Mapping) PlanReassociation(live []Agent) ReassociationPlan {
	var plan ReassociationPlan
	entries := make([]mappingCandidate, 0, len(m.Topics))
	for _, storageKey := range m.sortedKeys() {
		key, ok := ParseKey(storageKey)
		if !ok || m.Topics[storageKey] == nil {
			continue
		}
		entries = append(entries, mappingCandidate{storageKey: storageKey, key: key, entry: m.Topics[storageKey]})
	}
	agents := append([]Agent(nil), live...)
	sort.SliceStable(agents, func(i, j int) bool { return agents[i].Key.String() < agents[j].Key.String() })
	uniqueAgents := agents[:0]
	seenLive := make(map[Key]bool, len(agents))
	for _, a := range agents {
		if seenLive[a.Key] {
			continue
		}
		seenLive[a.Key] = true
		uniqueAgents = append(uniqueAgents, a)
	}
	agents = uniqueAgents

	assignedLive := make(map[Key]bool, len(agents))
	usedEntries := make(map[string]bool, len(agents))
	blockedLive := make(map[Key]bool)
	ambiguous := make(map[Key]ReassociationAmbiguity)
	assign := func(a Agent, c mappingCandidate, reason ReassociationReason) {
		plan.Assignments = append(plan.Assignments, ReassociationAssignment{From: c.key, To: a.Key, Reason: reason})
		assignedLive[a.Key] = true
		usedEntries[c.storageKey] = true
	}
	noteAmbiguous := func(a Agent, count int, reason ReassociationReason) {
		if _, exists := ambiguous[a.Key]; exists {
			return
		}
		ambiguous[a.Key] = ReassociationAmbiguity{LiveKey: a.Key, CandidateCount: count, Reason: reason}
		blockedLive[a.Key] = true
	}

	// Exact stored keys win only when the parsed key still identifies the same
	// known agent. In particular, a session digest conflict cannot be hidden by
	// equal pane and terminal IDs.
	for _, a := range agents {
		if a.Key.PaneID == "" || a.Key.TerminalID == "" {
			continue
		}
		storageKey := a.Key.String()
		entry, ok := m.Topics[storageKey]
		key, valid := ParseKey(storageKey)
		if ok && entry != nil && valid && key.SameIdentity(a.Key) {
			assign(a, mappingCandidate{storageKey: storageKey, key: key, entry: entry}, ReassociationExact)
		}
	}

	// Match equal, known session identities before attempting the weaker
	// metadata fallback. If duplicate entries or live agents claim the same
	// identity, leave that identity untouched instead of guessing.
	var sessionSets []reassociationCandidateSet
	for _, a := range agents {
		if assignedLive[a.Key] || a.Key.PaneID == "" || a.Key.TerminalID == "" {
			continue
		}
		var candidates []mappingCandidate
		for _, c := range entries {
			if usedEntries[c.storageKey] || !c.key.SameSession(a.Key) {
				continue
			}
			candidates = append(candidates, c)
		}
		if len(candidates) > 0 {
			sessionSets = append(sessionSets, reassociationCandidateSet{agent: a, candidates: candidates})
		}
	}
	sessionOwners := candidateOwners(sessionSets)
	for _, set := range sessionSets {
		if len(set.candidates) != 1 {
			noteAmbiguous(set.agent, len(set.candidates), ReassociationAmbiguousSession)
			continue
		}
		c := set.candidates[0]
		if owners := len(sessionOwners[c.storageKey]); owners > 1 {
			noteAmbiguous(set.agent, owners, ReassociationAmbiguousManyToOne)
			continue
		}
		assign(set.agent, c, ReassociationSession)
	}

	// A fallback is considered only when at least one side has no session
	// digest. Candidate ownership is counted before applying the live-status
	// preference so two live agents can never claim different generations of
	// the same pane/name/cwd identity.
	var fallbackSets []reassociationCandidateSet
	for _, a := range agents {
		if assignedLive[a.Key] || blockedLive[a.Key] || a.Key.PaneID == "" || a.Key.TerminalID == "" {
			continue
		}
		var candidates []mappingCandidate
		for _, c := range entries {
			if usedEntries[c.storageKey] || !eligibleFallback(c.key, c.entry, a) {
				continue
			}
			candidates = append(candidates, c)
		}
		if len(candidates) > 0 {
			fallbackSets = append(fallbackSets, reassociationCandidateSet{agent: a, candidates: candidates})
		}
	}
	fallbackOwners := candidateOwners(fallbackSets)
	for _, set := range fallbackSets {
		var liveCandidates []mappingCandidate
		for _, c := range set.candidates {
			if c.entry.Status.Live() {
				liveCandidates = append(liveCandidates, c)
			}
		}
		var selected mappingCandidate
		switch {
		case len(liveCandidates) == 1:
			selected = liveCandidates[0]
		case len(set.candidates) == 1:
			selected = set.candidates[0]
		default:
			noteAmbiguous(set.agent, len(set.candidates), ReassociationAmbiguousFallback)
			continue
		}
		if owners := len(fallbackOwners[selected.storageKey]); owners > 1 {
			noteAmbiguous(set.agent, owners, ReassociationAmbiguousManyToOne)
			continue
		}
		assign(set.agent, selected, ReassociationFallback)
	}

	for _, a := range ambiguous {
		plan.Ambiguous = append(plan.Ambiguous, a)
	}
	sort.Slice(plan.Assignments, func(i, j int) bool {
		if plan.Assignments[i].To.String() == plan.Assignments[j].To.String() {
			return plan.Assignments[i].From.String() < plan.Assignments[j].From.String()
		}
		return plan.Assignments[i].To.String() < plan.Assignments[j].To.String()
	})
	sort.Slice(plan.Ambiguous, func(i, j int) bool {
		return plan.Ambiguous[i].LiveKey.String() < plan.Ambiguous[j].LiveKey.String()
	})
	return plan
}

// ApplyReassociation moves selected entries atomically in memory. A stale or
// invalid plan returns an error without changing the mapping.
func (m *Mapping) ApplyReassociation(plan ReassociationPlan) error {
	if len(plan.Assignments) == 0 {
		return nil
	}
	moveSources := make(map[string]bool)
	destinations := make(map[string]bool)
	assignments := make(map[string]ReassociationAssignment, len(plan.Assignments))
	for _, assignment := range plan.Assignments {
		from, to := assignment.From.String(), assignment.To.String()
		if _, ok := ParseKey(from); !ok {
			return fmt.Errorf("invalid reassociation source")
		}
		if _, ok := ParseKey(to); !ok {
			return fmt.Errorf("invalid reassociation destination")
		}
		if _, exists := assignments[from]; exists {
			return fmt.Errorf("reassociation source assigned more than once")
		}
		if destinations[to] {
			return fmt.Errorf("reassociation destination assigned more than once")
		}
		if m.Topics[from] == nil {
			return fmt.Errorf("reassociation source is missing")
		}
		assignments[from] = assignment
		destinations[to] = true
		if from != to {
			moveSources[from] = true
		}
	}
	for from, assignment := range assignments {
		to := assignment.To.String()
		if from == to || m.Topics[to] == nil || moveSources[to] {
			continue
		}
		return fmt.Errorf("reassociation destination is occupied")
	}

	next := make(map[string]*TopicEntry, len(m.Topics))
	for key, entry := range m.Topics {
		if !moveSources[key] {
			next[key] = entry
		}
	}
	for from, assignment := range assignments {
		to := assignment.To.String()
		if from == to {
			continue
		}
		if next[to] != nil {
			return fmt.Errorf("reassociation destination is occupied")
		}
		next[to] = m.Topics[from]
	}
	m.Topics = next
	return nil
}

func candidateOwners(sets []reassociationCandidateSet) map[string]map[Key]struct{} {
	owners := make(map[string]map[Key]struct{})
	for _, set := range sets {
		for _, candidate := range set.candidates {
			if owners[candidate.storageKey] == nil {
				owners[candidate.storageKey] = make(map[Key]struct{})
			}
			owners[candidate.storageKey][set.agent.Key] = struct{}{}
		}
	}
	return owners
}

func eligibleFallback(entryKey Key, entry *TopicEntry, live Agent) bool {
	if entryKey.PaneID != live.Key.PaneID || entryKey.ConflictsWith(live.Key) ||
		(entryKey.SessionDigest != "" && live.Key.SessionDigest != "") {
		return false
	}
	if live.Cwd == "" || (entry.Cwd == "" && !entry.LegacyNoCwd) ||
		(entry.Cwd != "" && entry.Cwd != live.Cwd) {
		return false
	}
	if entry.AgentKind != "" && entry.AgentKind != live.Kind {
		return false
	}
	name, _ := Desired(live)
	return StripPrefix(entry.Name) == name
}

// Desired returns the topic name and status an agent should have right now.
// The name carries no status marker; the status drives the topic icon.
func Desired(a Agent) (string, Status) {
	return DisplayName(a.Label()), a.Status
}

// TopicFor returns the entry for the key, if any.
func (m *Mapping) TopicFor(k Key) (*TopicEntry, bool) {
	e, ok := m.Topics[k.String()]
	return e, ok
}

// KeyForThread finds the agent behind a topic. When several entries share a
// thread id (a stale mapping), the newest wins, as in DedupeThreads.
func (m *Mapping) KeyForThread(threadID int) (Key, bool) {
	var best string
	for _, key := range m.sortedKeys() {
		e := m.Topics[key]
		if e.ThreadID != threadID {
			continue
		}
		if best == "" || m.newer(key, best) {
			best = key
		}
	}
	if best == "" {
		return Key{}, false
	}
	return ParseKey(best)
}

// Mute records that an operator closed the topic; the mirror leaves it
// alone until Unmute.
func (m *Mapping) Mute(k Key, now time.Time) {
	m.setMuted(k, true, now)
}

// Unmute records that the topic was reopened by an operator.
func (m *Mapping) Unmute(k Key, now time.Time) {
	m.setMuted(k, false, now)
}

func (m *Mapping) setMuted(k Key, muted bool, now time.Time) {
	e, ok := m.Topics[k.String()]
	if !ok {
		return
	}
	e.Muted = muted
	e.UpdatedAt = now
}

// Link records a freshly created topic for the agent. The stored name is the
// one Telegram confirmed when it is non-empty, otherwise the desired name.
func (m *Mapping) Link(k Key, t Topic, a Agent, now time.Time) *TopicEntry {
	name, status := Desired(a)
	if t.Name != "" {
		name = t.Name
	}
	e := &TopicEntry{
		ThreadID: t.ThreadID, Name: name, Status: status, Closed: t.Closed,
		Cwd: a.Cwd, AgentKind: a.Kind, UpdatedAt: now,
	}
	m.Topics[k.String()] = e
	return e
}

// UpdateMetadata refreshes known cwd and agent kind after a successful
// association. Unknown live values do not erase metadata learned earlier.
// It returns false when the entry is missing or nothing changed.
func (m *Mapping) UpdateMetadata(k Key, a Agent) bool {
	e, ok := m.Topics[k.String()]
	if !ok {
		return false
	}
	changed := false
	if a.Cwd != "" && e.Cwd != a.Cwd {
		e.Cwd = a.Cwd
		changed = true
	}
	if a.Cwd != "" && e.LegacyNoCwd {
		e.LegacyNoCwd = false
		changed = true
	}
	if a.Kind != "" && e.AgentKind != a.Kind {
		e.AgentKind = a.Kind
		changed = true
	}
	return changed
}

// Diff compares the agent's desired name and status with what was last
// written. It reports nothing for unknown keys and for exited entries: an
// exited topic is final and must not be revived by a late status event.
func (m *Mapping) Diff(k Key, a Agent) (TopicPatch, bool) {
	e, ok := m.Topics[k.String()]
	if !ok || !e.Status.Live() {
		return TopicPatch{}, false
	}
	name, status := Desired(a)
	var p TopicPatch
	if name != e.Name {
		// A rename re-sends the icon in the same call: it costs nothing
		// extra and heals topics written by older releases, whose stored
		// status may match while the icon on Telegram does not.
		p.Name, p.Status = &name, &status
	}
	if status != e.Status {
		p.Status = &status
	}
	return p, !p.Empty()
}

// Apply records a patch that Telegram accepted.
func (m *Mapping) Apply(k Key, p TopicPatch, now time.Time) {
	e, ok := m.Topics[k.String()]
	if !ok {
		return
	}
	if p.Name != nil {
		e.Name = *p.Name
	}
	if p.Status != nil {
		e.Status = *p.Status
	}
	e.UpdatedAt = now
}

// MarkExited records that the exited icon was written to Telegram. The
// name stays as it was.
func (m *Mapping) MarkExited(k Key, now time.Time) {
	e, ok := m.Topics[k.String()]
	if !ok {
		return
	}
	e.Status = StatusExited
	e.UpdatedAt = now
}

// MarkClosed records that the topic was closed in Telegram.
func (m *Mapping) MarkClosed(k Key, now time.Time) {
	e, ok := m.Topics[k.String()]
	if !ok {
		return
	}
	e.Closed = true
	e.UpdatedAt = now
}

// MarkReopened records that the topic is open again in Telegram.
func (m *Mapping) MarkReopened(k Key, now time.Time) {
	e, ok := m.Topics[k.String()]
	if !ok {
		return
	}
	e.Closed = false
	e.UpdatedAt = now
}

// Forget drops the entry, typically because Telegram reported the topic
// gone; the next reconcile pass recreates it if the agent is still live.
func (m *Mapping) Forget(k Key) {
	delete(m.Topics, k.String())
}

// Orphans lists live entries whose agent is not in the live set. They are
// agents that exited while the daemon was not watching. Muted entries are
// left alone: the operator asked for silence.
func (m *Mapping) Orphans(live map[Key]struct{}) []Key {
	var out []Key
	for _, k := range m.Keys() {
		e := m.Topics[k.String()]
		if !e.Status.Live() || e.Muted {
			continue
		}
		if _, ok := live[k]; !ok {
			out = append(out, k)
		}
	}
	return out
}

// Unclosed lists exited entries whose topic is still open, so a failed
// CloseTopic can be retried on the next pass. Muted entries are skipped.
func (m *Mapping) Unclosed() []Key {
	var out []Key
	for _, k := range m.Keys() {
		e := m.Topics[k.String()]
		if !e.Status.Live() && !e.Closed && !e.Muted {
			out = append(out, k)
		}
	}
	return out
}

// Prune is retained for callers of older releases but never drops a topic
// reference. A mapping entry can be forgotten only after Telegram confirms
// its topic was deleted or is already gone; a size cap cannot establish that.
func (m *Mapping) Prune(maxEntries int) int {
	return 0
}

// Stale lists the candidates of the stale-topic sweep: exited entries whose
// topic is closed and has not changed for longer than maxAge, oldest
// first (ties by key). Muted entries count: an operator closed that topic
// and its agent is gone. Live agents and open topics are never listed.
func (m *Mapping) Stale(now time.Time, maxAge time.Duration) []Key {
	var out []Key
	for _, k := range m.Keys() {
		e := m.Topics[k.String()]
		if e.Status.Live() || !e.Closed || now.Sub(e.UpdatedAt) <= maxAge {
			continue
		}
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := m.Topics[out[i].String()], m.Topics[out[j].String()]
		if a.UpdatedAt.Equal(b.UpdatedAt) {
			return out[i].String() < out[j].String()
		}
		return a.UpdatedAt.Before(b.UpdatedAt)
	})
	return out
}

// Counts returns how many entries are live, exited and muted.
func (m *Mapping) Counts() (live, exited, muted int) {
	for _, e := range m.Topics {
		if e.Status.Live() {
			live++
		} else {
			exited++
		}
		if e.Muted {
			muted++
		}
	}
	return live, exited, muted
}

// DedupeThreads keeps one entry per thread id: the newest by UpdatedAt, with
// live entries winning ties. It returns the number of removed entries.
func (m *Mapping) DedupeThreads() int {
	best := map[int]string{}
	for _, key := range m.sortedKeys() {
		e := m.Topics[key]
		cur, ok := best[e.ThreadID]
		if !ok || m.newer(key, cur) {
			best[e.ThreadID] = key
		}
	}
	removed := 0
	for key, e := range m.Topics {
		if best[e.ThreadID] != key {
			delete(m.Topics, key)
			removed++
		}
	}
	return removed
}

func (m *Mapping) newer(a, b string) bool {
	ea, eb := m.Topics[a], m.Topics[b]
	if !ea.UpdatedAt.Equal(eb.UpdatedAt) {
		return ea.UpdatedAt.After(eb.UpdatedAt)
	}
	return ea.Status.Live() && !eb.Status.Live()
}

// Keys returns every key in a stable order for deterministic passes.
func (m *Mapping) Keys() []Key {
	var out []Key
	for _, key := range m.sortedKeys() {
		if k, ok := ParseKey(key); ok {
			out = append(out, k)
		}
	}
	return out
}

func (m *Mapping) sortedKeys() []string {
	keys := make([]string, 0, len(m.Topics))
	for key := range m.Topics {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
