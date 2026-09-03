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
	// mappingMaxEntries caps the mapping file; oldest exited entries go
	// first. Age alone never drops an entry: the stale-topic sweep deletes
	// the topic and forgets the entry instead.
	mappingMaxEntries = 500
	// screenSettle is how long the bridge waits after an agent turns blocked
	// or done before reading its screen, so the dialog has fully rendered.
	screenSettle = 1500 * time.Millisecond
	// blockedLines is the tail of the detection screen posted on blocked.
	blockedLines = 25
	// doneLines is the shorter tail posted on done.
	doneLines = 12
	// replyMaxParts caps a done post taken from the transcript: a reply
	// longer than this many messages is cut with a trailer.
	replyMaxParts = 5
	// bridgeBuffer bounds jobs waiting for the bridge goroutine.
	bridgeBuffer = 256
	// bridgeCallTimeout bounds one bridge job (Herdr read plus Telegram send).
	bridgeCallTimeout = 15 * time.Second
	// dropReportInterval is how often the daemon warns about bridge jobs
	// lost to overflow; each drop itself is logged at debug.
	dropReportInterval = 1 * time.Minute
	// captureInterval is how often the capture reads the screens of
	// working agents.
	captureInterval = 1 * time.Second
	// captureGrace keeps reading an agent after it left working so the
	// final screen of the exchange is committed.
	captureGrace = 3 * time.Second
	// captureMarkMinAway is the shortest pause outside working before a
	// return to working counts as a human message; Herdr's detection flaps
	// between working and idle or blocked for a second or two while an
	// agent runs a tool (measured 2026-09-03: 344 transitions in one
	// session on a Claude Code pane).
	captureMarkMinAway = 5 * time.Second
	// captureReadTimeout bounds one agent.read made by the capture.
	captureReadTimeout = 5 * time.Second
	// captureLines is how many lines of recent output one capture read
	// asks for; Claude Code panes return only the visible screen anyway.
	captureLines = 400
	// screenAllInlineRunes is the longest /screen all text still posted as
	// messages (about three chunks); longer output goes out as a file.
	screenAllInlineRunes = 12000
	// commandSettle is how long the bridge waits after typing a forwarded
	// Claude Code command (/clear, /usage, /model) before reading the
	// screen, so the overlay or the command output has rendered.
	commandSettle = 2 * time.Second
	// commandTailLines is the tail posted after a forwarded command that
	// prints a short confirmation (/clear, /model <name>).
	commandTailLines = 12
	// sweepInterval is how often the daemon looks for stale topics to
	// delete, on top of the pass at start and the one an option change
	// requests.
	sweepInterval = 24 * time.Hour
	// sweepBatch caps the deletions of one sweep pass so a long backlog
	// does not monopolise the Telegram queue; the rest wait for the next
	// pass.
	sweepBatch = 50
	// presenceInterval is how often the daemon samples the machine's input
	// idle time to decide whether the operator is at the desk.
	presenceInterval = 10 * time.Second
	// choiceLabelRunes is the longest option label shown on an inline
	// button; longer labels are cut with an ellipsis so a phone still shows
	// the number and the start of the text.
	choiceLabelRunes = 60
)
