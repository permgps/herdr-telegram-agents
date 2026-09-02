package telegram

import (
	"html"
	"strings"
)

const (
	// textMax leaves headroom below Telegram's 4096-character message limit
	// for the <pre></pre> wrapper and a short marker.
	textMax = 4096 - 64
	// defaultTopicName is used when a label is empty after trimming.
	defaultTopicName = "agent"
)

// chunk splits text into pieces of at most max runes. It prefers to cut at
// the last newline in the second half of a piece so lines stay whole; a
// newline that becomes a cut point is dropped rather than carried over.
func chunk(text string, max int) []string {
	if max <= 0 || text == "" {
		if text == "" {
			return nil
		}
		return []string{text}
	}
	var out []string
	rest := []rune(text)
	for len(rest) > max {
		cut := max
		if nl := lastIndexRune(rest[:max], '\n'); nl > max/2 {
			cut = nl
		}
		out = append(out, string(rest[:cut]))
		rest = rest[cut:]
		if len(rest) > 0 && rest[0] == '\n' {
			rest = rest[1:]
		}
	}
	if len(rest) > 0 {
		out = append(out, string(rest))
	}
	return out
}

func lastIndexRune(rs []rune, r rune) int {
	for i := len(rs) - 1; i >= 0; i-- {
		if rs[i] == r {
			return i
		}
	}
	return -1
}

// renderPlain escapes a chunk for parse_mode HTML.
func renderPlain(part string) string {
	return html.EscapeString(part)
}

// renderCode wraps an escaped chunk in <pre> for terminal output.
func renderCode(part string) string {
	return "<pre>" + html.EscapeString(part) + "</pre>"
}

// truncateName trims a topic name to max runes and substitutes
// defaultTopicName for an empty one.
func truncateName(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		s = defaultTopicName
	}
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}
	return s
}
