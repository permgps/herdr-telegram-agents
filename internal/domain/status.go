package domain

import (
	"strings"
	"unicode/utf8"
)

// Status is the lifecycle state of an agent as shown in Telegram.
//
// Five values mirror the strings reported by the Herdr socket API. The sixth,
// StatusExited, is plugin-internal: it is set when the pane hosting the agent
// closes and is never reported by Herdr itself.
type Status string

const (
	StatusWorking Status = "working"
	StatusIdle    Status = "idle"
	StatusBlocked Status = "blocked"
	StatusDone    Status = "done"
	StatusUnknown Status = "unknown"
	StatusExited  Status = "exited"
)

// displayNameMaxRunes is the Telegram limit for a forum topic name.
const displayNameMaxRunes = 128

// legacyPrefixes are the status emoji older releases wrote in front of the
// topic name. StripPrefix removes them so a mapping.json written before the
// icon-only scheme still yields the bare label.
var legacyPrefixes = [...]string{"⚙️", "💤", "❓", "✅", "❔", "🏁"}

// ParseStatus maps a Herdr wire string to a Status. Anything that is not one
// of the five wire values, including "exited", yields StatusUnknown: Herdr
// never reports an exit as a status, so trusting that string would let a
// malformed payload close a topic.
func ParseStatus(s string) Status {
	switch Status(s) {
	case StatusWorking, StatusIdle, StatusBlocked, StatusDone, StatusUnknown:
		return Status(s)
	default:
		return StatusUnknown
	}
}

// Live reports whether the agent behind this status is still running.
// Only StatusExited is not live.
func (s Status) Live() bool {
	return s != StatusExited
}

// Emoji is the glyph that stands for the status in bot text, the same one
// the topic icon pack prefers. Unknown values render as StatusUnknown.
func (s Status) Emoji() string {
	switch s {
	case StatusWorking:
		return "⚡"
	case StatusIdle:
		return "✅"
	case StatusBlocked:
		return "❓"
	case StatusDone:
		return "🏆"
	case StatusExited:
		return "🏁"
	default:
		return "👀"
	}
}

// ReadyForInput reports whether the agent is waiting at a prompt and can take
// a new instruction without interrupting anything: idle or done.
func (s Status) ReadyForInput() bool {
	return s == StatusIdle || s == StatusDone
}

// DisplayName clamps a label to the Telegram limit of 128 runes for a forum
// topic name. The status is not part of the name: it is shown by the topic
// icon only (decided 2026-09-02 after a live run, where the emoji prefix
// doubled the icon and 💤 rendered as noisy "zzz" glyphs). The clamp counts
// runes, not bytes, so labels with multi-byte characters keep as many
// characters as fit.
func DisplayName(label string) string {
	if utf8.RuneCountInString(label) <= displayNameMaxRunes {
		return label
	}
	runes := []rune(label)
	return string(runes[:displayNameMaxRunes])
}

// StripPrefix removes a legacy status prefix and the whitespace that follows
// it. Names without one are returned unchanged.
func StripPrefix(name string) string {
	for _, p := range legacyPrefixes {
		if strings.HasPrefix(name, p) {
			return strings.TrimLeft(name[len(p):], " \t")
		}
	}
	return name
}
