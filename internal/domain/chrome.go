package domain

import (
	"strings"
	"unicode/utf8"
)

// The input frame Claude Code draws under its transcript, as read from a
// live pane on 2026-09-07 (`herdr agent read --source visible`), bottom up:
//
//	  ⏵⏵ auto mode on (shift+tab to cycle) · ← 2 agents      the mode hint
//	  …/herdr_tg │ main ✓ │ 14%: 121k[▓░░░] │ $4.37 │ …      the status line
//	──────────────────────────────────────────────           the box: a rule,
//	❯                                                        the prompt row,
//	──────────────────────────────────────────────           a rule
//
// Five lines in the default layout. In the default permission mode the
// hint reads "? for shortcuts"; while an agent works a spinner and a
// "⎿  Tip:" line sit above the box and are transcript, not frame. With a
// question open the dialog replaces the box: only the status line and
// the hint may follow the dialog's footer, and only those are cut.
const (
	// chromeRuleMinRunes is the shortest run of ─ that counts as one of the
	// two rules of the input box.
	chromeRuleMinRunes = 8
	// chromePrompt is the whole trimmed prompt row of an empty input box.
	// A row with typed text after the glyph is the operator's unsent input
	// and is left alone.
	chromePrompt = "❯"
	// chromeShortcutsHint is the hint of the default permission mode.
	chromeShortcutsHint = "? for shortcuts"
	// chromeCycleHint is the tail every mode hint carries.
	chromeCycleHint = "shift+tab to cycle"
	// chromeStatusSeparators is how many " │ " (or " | ") a line needs to
	// count as the status line.
	chromeStatusSeparators = 2
)

// chromeHintPrefixes start the mode hints Claude Code draws under the
// status line: ⏵⏵ for auto and plan modes, ▶▶ where the glyph is not
// available, ⏸ while paused.
var chromeHintPrefixes = []string{"⏵⏵ ", "▶▶ ", "⏸ "}

// CutChrome removes Claude Code's input frame from the bottom of a screen
// and returns the remainder with the number of frame lines removed, 0 when
// nothing matched (the text is then returned unchanged). It walks up from
// the last non-blank line and accepts, in this order, any number of hint
// lines, at most one status line and the three-line input box; the
// longest suffix of that shape is cut when at least one part matched. A
// status line is cut only under a hint or right under the box, so a
// transcript line with two bars in it survives. The walk never looks
// above the first line that is none of the frame, so a dialog drawn where
// the box would be, and everything above it, is left as it is. Text
// without the frame, such as a Codex screen, passes through unchanged.
func CutChrome(text string) (string, int) {
	if text == "" {
		return text, 0
	}
	lines := strings.Split(text, "\n")
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	cut := end
	hints := 0
	for cut > 0 && isChromeHint(lines[cut-1]) {
		cut--
		hints++
	}
	status := cut > 0 && isChromeStatus(lines[cut-1])
	boxTop := cut
	if status {
		boxTop--
	}
	box := isChromeBox(lines, boxTop)
	switch {
	case box:
		cut = boxTop - 3
	case status && hints > 0:
		cut = boxTop
	}
	removed := end - cut
	if removed == 0 {
		return text, 0
	}
	return strings.Join(lines[:cut], "\n"), removed
}

// isChromeBox reports whether the three lines ending right before end are
// a rule, the bare prompt row and a rule.
func isChromeBox(lines []string, end int) bool {
	if end < 3 {
		return false
	}
	return isChromeRule(lines[end-3]) && isChromePromptRow(lines[end-2]) && isChromeRule(lines[end-1])
}

// isChromeHint reports whether the line is a mode hint of the frame.
func isChromeHint(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == chromeShortcutsHint || strings.Contains(trimmed, chromeCycleHint) {
		return true
	}
	for _, p := range chromeHintPrefixes {
		if strings.HasPrefix(trimmed, p) {
			return true
		}
	}
	return false
}

// isChromeStatus reports whether the line looks like the status line:
// at least two " │ " or " | " separators between its cells.
func isChromeStatus(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.Count(trimmed, " │ ")+strings.Count(trimmed, " | ") >= chromeStatusSeparators
}

// isChromePromptRow reports whether the line is the empty prompt row.
func isChromePromptRow(line string) bool {
	return strings.TrimSpace(line) == chromePrompt
}

// isChromeRule reports whether the line is a rule of the input box: only
// ─ characters, at least chromeRuleMinRunes of them.
func isChromeRule(line string) bool {
	trimmed := strings.TrimSpace(line)
	if utf8.RuneCountInString(trimmed) < chromeRuleMinRunes {
		return false
	}
	for _, r := range trimmed {
		if r != '─' {
			return false
		}
	}
	return true
}
