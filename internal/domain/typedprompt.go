package domain

import (
	"strings"
	"unicode"
)

// typedPromptPrefix is how much of the typed line must still sit in the
// input box for the prompt to count as not submitted. Long enough that a
// suggestion the agent shows in an empty box does not match by accident,
// short enough to survive the box wrapping or cutting the line.
const typedPromptPrefix = 40

// promptMarker starts the input box line of Claude Code's terminal UI,
// where the typed text stays when its enter was not taken. Agents with
// another input box are never reported as stuck: the check cannot see
// their text, so it stays silent rather than wrong.
const promptMarker = "❯"

// PromptStuck reports whether the typed line still sits in the agent's
// input box after enter was sent: the text after the last prompt marker,
// up to the box's bottom edge, starts with the line. A submitted prompt is
// echoed above the box and the box is empty or shows a suggestion, so the
// text after the marker no longer matches. A screen without the marker is
// never stuck.
func PromptStuck(screen, line string) bool {
	want := foldSpace(line)
	if want == "" {
		return false
	}
	if r := []rune(want); len(r) > typedPromptPrefix {
		want = string(r[:typedPromptPrefix])
	}
	lines := strings.Split(screen, "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimLeftFunc(l, unicode.IsSpace), promptMarker) {
			start = i
		}
	}
	if start < 0 {
		return false
	}
	var box []string
	first := strings.TrimPrefix(strings.TrimLeftFunc(lines[start], unicode.IsSpace), promptMarker)
	box = append(box, first)
	for _, l := range lines[start+1:] {
		if strings.TrimSpace(l) == "" || boxEdge(l) {
			break
		}
		box = append(box, l)
	}
	return strings.HasPrefix(foldSpace(strings.Join(box, " ")), want)
}

// boxEdge reports a line drawn only with horizontal rules, the top and
// bottom of the input box.
func boxEdge(l string) bool {
	l = strings.TrimSpace(l)
	if l == "" {
		return false
	}
	for _, r := range l {
		if r != '─' && r != '━' && r != '-' && r != '▔' {
			return false
		}
	}
	return true
}

// foldSpace trims the text and collapses every run of white space to one
// space, so wrapped lines compare equal to the line that was typed.
func foldSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
