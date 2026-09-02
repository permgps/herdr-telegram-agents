package domain

import (
	"sort"
	"strings"
	"time"
)

// MappingVersion is the schema version written to mapping.json.
const MappingVersion = 1

// TopicEntry is what the plugin last wrote to Telegram for one agent.
// Telegram offers no way to read a topic back, so Name and Status are the
// values of the last successful create or edit, not the live state.
type TopicEntry struct {
	ThreadID  int
	Name      string
	Status    Status
	Closed    bool
	UpdatedAt time.Time
}

// Label returns the agent label stored in the topic name, without prefix.
func (e *TopicEntry) Label() string {
	return StripPrefix(e.Name)
}

// Mapping is the aggregate linking agent keys to forum topics. It is mutated
// in memory by the reconciler and persisted after every successful Telegram
// call. Keys are stored as strings (Key.String) so the JSON file stays flat.
type Mapping struct {
	Version int
	ChatID  int64
	Topics  map[string]*TopicEntry
}

// NewMapping returns an empty mapping for the given chat.
func NewMapping(chatID int64) *Mapping {
	return &Mapping{Version: MappingVersion, ChatID: chatID, Topics: map[string]*TopicEntry{}}
}

// ParseKey is the inverse of Key.String. The first slash separates pane and
// terminal ids; neither id produced by Herdr contains one.
func ParseKey(s string) (Key, bool) {
	pane, term, ok := strings.Cut(s, "/")
	if !ok || pane == "" || term == "" {
		return Key{}, false
	}
	return Key{PaneID: pane, TerminalID: term}, true
}

// Desired returns the topic name and status an agent should have right now.
func Desired(a Agent) (string, Status) {
	return DisplayName(a.Label(), a.Status), a.Status
}

// TopicFor returns the entry for the key, if any.
func (m *Mapping) TopicFor(k Key) (*TopicEntry, bool) {
	e, ok := m.Topics[k.String()]
	return e, ok
}

// Link records a freshly created topic for the agent. The stored name is the
// one Telegram confirmed when it is non-empty, otherwise the desired name.
func (m *Mapping) Link(k Key, t Topic, a Agent, now time.Time) *TopicEntry {
	name, status := Desired(a)
	if t.Name != "" {
		name = t.Name
	}
	e := &TopicEntry{ThreadID: t.ThreadID, Name: name, Status: status, Closed: t.Closed, UpdatedAt: now}
	m.Topics[k.String()] = e
	return e
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
		p.Name = &name
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

// ExitedName returns the topic name to write when the agent behind the key
// exits: the stored label with the exited prefix.
func (m *Mapping) ExitedName(k Key) (string, bool) {
	e, ok := m.Topics[k.String()]
	if !ok {
		return "", false
	}
	return DisplayName(e.Label(), StatusExited), true
}

// MarkExited records that the exited name was written to Telegram.
func (m *Mapping) MarkExited(k Key, now time.Time) {
	e, ok := m.Topics[k.String()]
	if !ok {
		return
	}
	e.Name = DisplayName(e.Label(), StatusExited)
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

// Forget drops the entry, typically because Telegram reported the topic
// gone; the next reconcile pass recreates it if the agent is still live.
func (m *Mapping) Forget(k Key) {
	delete(m.Topics, k.String())
}

// Orphans lists live entries whose agent is not in the live set. They are
// agents that exited while the daemon was not watching.
func (m *Mapping) Orphans(live map[Key]struct{}) []Key {
	var out []Key
	for _, k := range m.Keys() {
		e := m.Topics[k.String()]
		if !e.Status.Live() {
			continue
		}
		if _, ok := live[k]; !ok {
			out = append(out, k)
		}
	}
	return out
}

// Unclosed lists exited entries whose topic is still open, so a failed
// CloseTopic can be retried on the next pass.
func (m *Mapping) Unclosed() []Key {
	var out []Key
	for _, k := range m.Keys() {
		e := m.Topics[k.String()]
		if !e.Status.Live() && !e.Closed {
			out = append(out, k)
		}
	}
	return out
}

// Prune removes exited entries older than maxAge and, when the mapping still
// holds more than maxEntries, the oldest exited entries until it fits. Live
// entries are never removed. It returns the number of removed entries.
func (m *Mapping) Prune(now time.Time, maxAge time.Duration, maxEntries int) int {
	removed := 0
	for key, e := range m.Topics {
		if !e.Status.Live() && now.Sub(e.UpdatedAt) > maxAge {
			delete(m.Topics, key)
			removed++
		}
	}
	if maxEntries <= 0 || len(m.Topics) <= maxEntries {
		return removed
	}
	type aged struct {
		key string
		at  time.Time
	}
	var exited []aged
	for key, e := range m.Topics {
		if !e.Status.Live() {
			exited = append(exited, aged{key, e.UpdatedAt})
		}
	}
	sort.Slice(exited, func(i, j int) bool {
		if exited[i].at.Equal(exited[j].at) {
			return exited[i].key < exited[j].key
		}
		return exited[i].at.Before(exited[j].at)
	})
	for _, a := range exited {
		if len(m.Topics) <= maxEntries {
			break
		}
		delete(m.Topics, a.key)
		removed++
	}
	return removed
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
