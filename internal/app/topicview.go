package app

import (
	"sync"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// topicView is a goroutine-safe snapshot of the mapping for readers outside
// the reconciler goroutine (the bridge). The reconciler republishes it after
// every mapping change, so a reader sees at worst the state before the
// current Telegram call. Entries are copies; nothing here aliases the
// mapping.
type topicView struct {
	mu       sync.RWMutex
	entries  map[domain.Key]domain.TopicEntry
	byThread map[int]domain.Key
}

func newTopicView() *topicView {
	return &topicView{entries: map[domain.Key]domain.TopicEntry{}, byThread: map[int]domain.Key{}}
}

// publish replaces the snapshot with the mapping's current content.
func (v *topicView) publish(m *domain.Mapping) {
	entries := make(map[domain.Key]domain.TopicEntry, len(m.Topics))
	byThread := make(map[int]domain.Key, len(m.Topics))
	for _, key := range m.Keys() {
		e, ok := m.TopicFor(key)
		if !ok {
			continue
		}
		entries[key] = *e
		if _, seen := byThread[e.ThreadID]; !seen {
			if k, ok := m.KeyForThread(e.ThreadID); ok {
				byThread[e.ThreadID] = k
			}
		}
	}
	v.mu.Lock()
	v.entries, v.byThread = entries, byThread
	v.mu.Unlock()
}

// Entry returns a copy of the entry for key.
func (v *topicView) Entry(key domain.Key) (domain.TopicEntry, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	e, ok := v.entries[key]
	return e, ok
}

// KeyForThread returns the agent behind a topic, newest entry first.
func (v *topicView) KeyForThread(threadID int) (domain.Key, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	k, ok := v.byThread[threadID]
	return k, ok
}
