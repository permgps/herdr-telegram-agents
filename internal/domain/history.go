package domain

import "strings"

// History constants. They are not user-facing settings.
const (
	// HistoryLiveLines is the bottom part of a screen that is still being
	// redrawn (input box, status line, spinner); it is kept aside as the
	// tail and only committed once it scrolls up.
	HistoryLiveLines = 8
	// HistoryAnchorWindow is how many lines at the start of an overlap are
	// compared to decide whether two screens are aligned.
	HistoryAnchorWindow = 6
	// HistoryAnchorLines is how many non-blank lines inside the window must
	// be equal for the screens to count as aligned.
	HistoryAnchorLines = 3
	// HistoryMaxLines caps the committed lines per agent.
	HistoryMaxLines = 2000
	// HistoryMaxBytes caps the committed bytes per agent.
	HistoryMaxBytes = 256 << 10
	// HistoryGapMarker is the line inserted where screens could not be
	// aligned or where old lines were dropped.
	HistoryGapMarker = "…"
)

// History is the output of one agent accumulated from screen snapshots.
// The daemon appends a snapshot about once a second while the agent
// works; the aggregate aligns each snapshot with the previous one by an
// anchor of a few lines and commits what scrolled up. A mark records where
// the last human message started so Lines can return only what came after
// it. Lines passed in must already be normalized (no CR, no trailing
// spaces). History is not safe for concurrent use; the owner locks.
type History struct {
	committed []string
	bytes     int
	// prev is the stable part of the last snapshot, nil before the first.
	prev []string
	// tail is the live part of the last snapshot.
	tail []string
	// mark is an index into committed, -1 when no human message was seen.
	mark int
	// truncated is set when lines after the mark (or, without a mark, any
	// lines) were dropped by the caps; Lines then starts with a gap.
	truncated bool
}

// NewHistory returns an empty history without a mark.
func NewHistory() *History {
	return &History{mark: -1}
}

// Append merges one screen snapshot. It reports how many committed lines
// were added, the number of lines the screen scrolled since the previous
// snapshot (shift), and whether the snapshot could not be aligned, in which
// case a gap marker precedes it. The bottom HistoryLiveLines lines are kept
// as the tail and not committed yet.
func (h *History) Append(lines []string) (added, shift int, gap bool) {
	stable, tail := splitLive(lines)
	h.tail = append([]string(nil), tail...)
	if len(stable) == 0 {
		return 0, 0, false
	}
	defer func() {
		h.prev = append([]string(nil), stable...)
		h.trim()
	}()
	if len(h.prev) == 0 {
		h.commit(stable)
		return len(stable), 0, false
	}
	for shift = 0; shift < len(h.prev); shift++ {
		overlap := h.prev[shift:]
		if len(overlap) > len(stable) {
			continue
		}
		if !aligned(overlap, stable) {
			continue
		}
		// The caps may already have dropped the head of the overlap from
		// committed; skip that part of the snapshot so indexes (and the
		// mark) keep their meaning.
		keep, skip := len(h.committed)-len(overlap), 0
		if keep < 0 {
			keep, skip = 0, -keep
		}
		h.cut(keep)
		h.commit(stable[skip:])
		return len(stable) - len(overlap), shift, false
	}
	h.commit([]string{HistoryGapMarker})
	h.commit(stable)
	return len(stable), len(h.prev), true
}

// Mark records that a human message starts here: everything committed so
// far belongs to the previous exchange. A previous truncation no longer
// matters after a mark.
func (h *History) Mark() {
	h.mark = len(h.committed)
	h.truncated = false
}

// Marked reports whether a mark exists.
func (h *History) Marked() bool { return h.mark >= 0 }

// Len returns the number of committed lines.
func (h *History) Len() int { return len(h.committed) }

// Lines returns the committed lines after the mark (all of them without a
// mark) followed by the current tail. A gap marker comes first when lines
// in that range were dropped by the caps.
func (h *History) Lines() []string {
	start := 0
	if h.mark > 0 {
		start = h.mark
	}
	if start > len(h.committed) {
		start = len(h.committed)
	}
	out := make([]string, 0, len(h.committed)-start+len(h.tail)+1)
	if h.truncated {
		out = append(out, HistoryGapMarker)
	}
	out = append(out, h.committed[start:]...)
	return append(out, h.tail...)
}

func (h *History) commit(lines []string) {
	for _, l := range lines {
		h.bytes += len(l) + 1
	}
	h.committed = append(h.committed, lines...)
}

// cut drops committed lines from index n on. The caller commits the fresh
// version of the same region right after, so the mark keeps its index.
func (h *History) cut(n int) {
	for _, l := range h.committed[n:] {
		h.bytes -= len(l) + 1
	}
	h.committed = h.committed[:n]
}

// trim drops lines from the front until both caps hold.
func (h *History) trim() {
	drop := 0
	bytes := h.bytes
	for drop < len(h.committed) && (len(h.committed)-drop > HistoryMaxLines || bytes > HistoryMaxBytes) {
		bytes -= len(h.committed[drop]) + 1
		drop++
	}
	if drop == 0 {
		return
	}
	h.committed = append([]string(nil), h.committed[drop:]...)
	h.bytes = bytes
	if h.mark >= 0 {
		h.mark -= drop
		if h.mark < 0 {
			h.mark = 0
			h.truncated = true
		}
	} else {
		h.truncated = true
	}
}

// splitLive separates the live bottom of a screen from the stable rest.
func splitLive(lines []string) (stable, tail []string) {
	if len(lines) <= HistoryLiveLines {
		return nil, lines
	}
	cut := len(lines) - HistoryLiveLines
	return lines[:cut], lines[cut:]
}

// aligned reports whether the start of overlap (a suffix of the previous
// stable screen) matches the start of stable: within the first
// HistoryAnchorWindow lines at least HistoryAnchorLines non-blank lines
// (or all of them when fewer exist) must be equal. Blank lines never count,
// so an empty screen is never an anchor; a single line redrawn in place
// does not break the alignment.
func aligned(overlap, stable []string) bool {
	window := len(overlap)
	if window > HistoryAnchorWindow {
		window = HistoryAnchorWindow
	}
	text, hits := 0, 0
	for i := 0; i < window; i++ {
		if strings.TrimSpace(overlap[i]) == "" {
			continue
		}
		text++
		if overlap[i] == stable[i] {
			hits++
		}
	}
	if text == 0 {
		return false
	}
	need := HistoryAnchorLines
	if text < need {
		need = text
	}
	return hits >= need
}
