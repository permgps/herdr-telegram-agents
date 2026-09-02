package telegram

import (
	"html"
	"strings"
	"unicode/utf16"
)

const (
	// textMax leaves headroom below Telegram's 4096-character message limit.
	// Telegram counts the text after entity parsing, so tags and escapes
	// cost nothing; the margin only absorbs rounding at chunk boundaries.
	textMax = 4096 - 64
	// defaultTopicName is used when a label is empty after trimming.
	defaultTopicName = "agent"
)

// chunk splits text into pieces of at most max UTF-16 code units, which is
// how Telegram measures message length (an emoji outside the BMP costs
// two). It prefers to cut at the last newline in the second half of a
// piece so lines stay whole; a newline that becomes a cut point is dropped
// rather than carried over. Runes are never split.
func chunk(text string, max int) []string {
	if text == "" {
		return nil
	}
	if max <= 0 {
		return []string{text}
	}
	var out []string
	rest := []rune(text)
	for {
		n := fitUTF16(rest, max)
		if n == len(rest) {
			return append(out, string(rest))
		}
		cut := n
		if nl := lastIndexRune(rest[:n], '\n'); nl > n/2 {
			cut = nl
		}
		out = append(out, string(rest[:cut]))
		rest = rest[cut:]
		if len(rest) > 0 && rest[0] == '\n' {
			rest = rest[1:]
		}
		if len(rest) == 0 {
			return out
		}
	}
}

// fitUTF16 returns how many leading runes of rs fit into max UTF-16 code
// units, always at least one so a caller can make progress.
func fitUTF16(rs []rune, max int) int {
	units := 0
	for i, r := range rs {
		u := utf16.RuneLen(r)
		if u < 0 {
			u = 1
		}
		if units+u > max && i > 0 {
			return i
		}
		units += u
	}
	return len(rs)
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

// truncateName trims a topic name to max UTF-16 code units (Telegram's
// unit for the 128-character topic limit) and substitutes defaultTopicName
// for an empty one.
func truncateName(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		s = defaultTopicName
	}
	r := []rune(s)
	if n := fitUTF16(r, max); n < len(r) {
		return string(r[:n])
	}
	return s
}
