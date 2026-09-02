// Package app holds the use cases of the plugin: the agent registry, the
// topic reconciler with its debounce, the setup wizard, the daemon
// supervisor and the daemon event loop. It depends on internal/domain only.
package app

import "time"

// Timing constants shared by the use cases. They are not user-facing
// settings; tests override the corresponding struct fields instead.
const (
	// reconcileInterval is how often the registry takes an agent.list
	// snapshot when nothing else triggers one.
	reconcileInterval = 15 * time.Second
	// snapshotCoalesce delays a snapshot after a structural socket event so
	// a burst of events costs one agent.list call.
	snapshotCoalesce = 500 * time.Millisecond
	// editDebounce is the trailing-edge delay before a topic edit.
	editDebounce = 3 * time.Second
	// mappingPruneAge is the age after which exited entries are dropped.
	mappingPruneAge = 7 * 24 * time.Hour
	// mappingMaxEntries caps the mapping file; oldest exited entries go first.
	mappingMaxEntries = 500
)
