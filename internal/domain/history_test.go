package domain_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// screen builds a snapshot of lines "L<from>".."L<to>" inclusive.
func screen(from, to int) []string {
	out := make([]string, 0, to-from+1)
	for i := from; i <= to; i++ {
		out = append(out, fmt.Sprintf("L%d", i))
	}
	return out
}

func TestHistoryFirstSnapshotKeepsTail(t *testing.T) {
	h := domain.NewHistory()
	added, shift, gap := h.Append(screen(1, 20))
	if added != 12 || shift != 0 || gap {
		t.Fatalf("Append = (%d, %d, %v), want (12, 0, false)", added, shift, gap)
	}
	if h.Len() != 12 {
		t.Fatalf("Len = %d, want 12", h.Len())
	}
	if got := h.Lines(); !reflect.DeepEqual(got, screen(1, 20)) {
		t.Fatalf("Lines = %v", got)
	}
	if h.Marked() {
		t.Fatal("fresh history must not be marked")
	}
}

func TestHistoryUnchangedScreenAddsNothing(t *testing.T) {
	h := domain.NewHistory()
	h.Append(screen(1, 20))
	added, shift, gap := h.Append(screen(1, 20))
	if added != 0 || shift != 0 || gap {
		t.Fatalf("Append = (%d, %d, %v), want (0, 0, false)", added, shift, gap)
	}
	if h.Len() != 12 {
		t.Fatalf("Len = %d, want 12", h.Len())
	}
}

func TestHistoryScrollAppendsNewLines(t *testing.T) {
	h := domain.NewHistory()
	h.Append(screen(1, 20))
	added, shift, gap := h.Append(screen(4, 23))
	if added != 3 || shift != 3 || gap {
		t.Fatalf("Append = (%d, %d, %v), want (3, 3, false)", added, shift, gap)
	}
	if got := h.Lines(); !reflect.DeepEqual(got, screen(1, 23)) {
		t.Fatalf("Lines = %v", got)
	}
	// A second scroll continues from where the first left off.
	h.Append(screen(10, 29))
	if got := h.Lines(); !reflect.DeepEqual(got, screen(1, 29)) {
		t.Fatalf("Lines after second scroll = %v", got)
	}
}

func TestHistoryInPlaceRewriteIsRefreshed(t *testing.T) {
	h := domain.NewHistory()
	h.Append(screen(1, 20))
	next := screen(1, 20)
	next[9] = "L10 done"
	added, shift, gap := h.Append(next)
	if added != 0 || shift != 0 || gap {
		t.Fatalf("Append = (%d, %d, %v), want (0, 0, false)", added, shift, gap)
	}
	if got := h.Lines()[9]; got != "L10 done" {
		t.Fatalf("line 10 = %q, want refreshed", got)
	}
}

func TestHistoryUnalignedScreenGetsGap(t *testing.T) {
	h := domain.NewHistory()
	h.Append(screen(1, 20))
	added, _, gap := h.Append(screen(100, 119))
	if added != 12 || !gap {
		t.Fatalf("Append = (%d, _, %v), want (12, true)", added, gap)
	}
	want := append(append(screen(1, 12), domain.HistoryGapMarker), screen(100, 119)...)
	if got := h.Lines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Lines = %v\nwant %v", got, want)
	}
}

func TestHistoryBlankAnchorNeverAligns(t *testing.T) {
	h := domain.NewHistory()
	blank := make([]string, 20)
	h.Append(blank)
	text := screen(1, 20)
	text[0], text[1], text[2] = "", "", ""
	_, _, gap := h.Append(text)
	if !gap {
		t.Fatal("blank lines must not serve as an anchor")
	}
}

func TestHistoryMarkLimitsLines(t *testing.T) {
	h := domain.NewHistory()
	h.Append(screen(1, 20))
	h.Mark()
	if !h.Marked() {
		t.Fatal("Marked = false after Mark")
	}
	h.Append(screen(4, 23))
	if got := h.Lines(); !reflect.DeepEqual(got, screen(13, 23)) {
		t.Fatalf("Lines after mark = %v, want L13..L23", got)
	}
	// A rewrite in place below the mark does not move it.
	next := screen(4, 23)
	next[0] = "L4 rewritten"
	h.Append(next)
	if got := h.Lines(); !reflect.DeepEqual(got, screen(13, 23)) {
		t.Fatalf("Lines after rewrite = %v", got)
	}
	// A second mark restarts the range.
	h.Mark()
	h.Append(screen(10, 29))
	if got := h.Lines(); !reflect.DeepEqual(got, screen(16, 29)) {
		t.Fatalf("Lines after second mark = %v, want L16..L29", got)
	}
}

func TestHistoryLineCapDropsFromFront(t *testing.T) {
	h := domain.NewHistory()
	big := screen(1, domain.HistoryMaxLines+domain.HistoryLiveLines+10)
	h.Append(big)
	if h.Len() != domain.HistoryMaxLines {
		t.Fatalf("Len = %d, want %d", h.Len(), domain.HistoryMaxLines)
	}
	got := h.Lines()
	if got[0] != domain.HistoryGapMarker || got[1] != "L11" {
		t.Fatalf("Lines start = %v, want gap then L11", got[:2])
	}
	// A mark after the truncation clears the gap; a later scroll that drops
	// lines before the mark keeps it clear.
	h.Mark()
	shifted := screen(6, domain.HistoryMaxLines+domain.HistoryLiveLines+15)
	added, shift, gap := h.Append(shifted)
	if added != 5 || shift != 5 || gap {
		t.Fatalf("Append = (%d, %d, %v), want (5, 5, false)", added, shift, gap)
	}
	want := screen(domain.HistoryMaxLines+11, domain.HistoryMaxLines+domain.HistoryLiveLines+15)
	if got := h.Lines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Lines after mark and scroll = %v (len %d)\nwant %v", got, len(got), want)
	}
}

func TestHistoryMarkDroppedByCapIsTruncated(t *testing.T) {
	h := domain.NewHistory()
	h.Append(screen(1, 20))
	h.Mark()
	h.Append(screen(1, domain.HistoryMaxLines+domain.HistoryLiveLines+30))
	got := h.Lines()
	if got[0] != domain.HistoryGapMarker {
		t.Fatalf("Lines start = %q, want gap marker", got[0])
	}
	if !h.Marked() {
		t.Fatal("mark must survive as truncated")
	}
}

func TestHistoryByteCapDropsFromFront(t *testing.T) {
	h := domain.NewHistory()
	wide := strings.Repeat("x", 1000)
	lines := make([]string, 300+domain.HistoryLiveLines)
	for i := range lines {
		lines[i] = fmt.Sprintf("%d %s", i, wide)
	}
	h.Append(lines)
	if h.Len() >= 300 {
		t.Fatalf("Len = %d, want fewer than 300 after the byte cap", h.Len())
	}
	if got := h.Lines(); got[0] != domain.HistoryGapMarker {
		t.Fatalf("Lines start = %q, want gap marker", got[0][:10])
	}
}

func TestHistoryShortScreenHasNoStablePart(t *testing.T) {
	h := domain.NewHistory()
	added, _, gap := h.Append(screen(1, 5))
	if added != 0 || gap {
		t.Fatalf("Append = (%d, _, %v), want (0, false)", added, gap)
	}
	if got := h.Lines(); !reflect.DeepEqual(got, screen(1, 5)) {
		t.Fatalf("Lines = %v", got)
	}
	// The first full screen afterwards is committed without a gap.
	_, _, gap = h.Append(screen(1, 20))
	if gap {
		t.Fatal("first stable screen must not report a gap")
	}
	if got := h.Lines(); !reflect.DeepEqual(got, screen(1, 20)) {
		t.Fatalf("Lines = %v", got)
	}
}
