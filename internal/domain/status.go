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

// prefixes lists every status with its emoji prefix in a stable order.
// The order matters for StripPrefix: longer prefixes are not a concern here
// because every prefix is a single grapheme, but keeping the table explicit
// avoids a map iteration in the hot path.
var prefixes = [...]struct {
	status Status
	prefix string
}{
	{StatusWorking, "⚙️"},
	{StatusIdle, "💤"},
	{StatusBlocked, "❓"},
	{StatusDone, "✅"},
	{StatusUnknown, "❔"},
	{StatusExited, "🏁"},
}

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

// Prefix returns the emoji placed before the agent label in the topic name.
// An unrecognised Status gets the unknown prefix so a topic is never left
// without a marker.
func (s Status) Prefix() string {
	for _, p := range prefixes {
		if p.status == s {
			return p.prefix
		}
	}
	return StatusUnknown.Prefix()
}

// Live reports whether the agent behind this status is still running.
// Only StatusExited is not live.
func (s Status) Live() bool {
	return s != StatusExited
}

// ReadyForInput reports whether the agent is waiting at a prompt and can take
// a new instruction without interrupting anything: idle or done.
func (s Status) ReadyForInput() bool {
	return s == StatusIdle || s == StatusDone
}

// DisplayName builds the topic name "<prefix> <label>" clamped to the
// Telegram limit of 128 runes. The clamp counts runes, not bytes, so labels
// with multi-byte characters keep as many characters as fit.
func DisplayName(label string, st Status) string {
	name := st.Prefix() + " " + label
	if utf8.RuneCountInString(name) <= displayNameMaxRunes {
		return name
	}
	runes := []rune(name)
	return string(runes[:displayNameMaxRunes])
}

// StripPrefix removes a known status prefix and the whitespace that follows
// it. Names without a known prefix are returned unchanged. It is the inverse
// of DisplayName for names that were not clamped.
func StripPrefix(name string) string {
	for _, p := range prefixes {
		if strings.HasPrefix(name, p.prefix) {
			return strings.TrimLeft(name[len(p.prefix):], " \t")
		}
	}
	return name
}
