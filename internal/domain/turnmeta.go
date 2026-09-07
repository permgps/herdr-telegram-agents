package domain

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Line renders the turn summary shown under a done post: the known parts
// joined by " · " in the order duration, model, files, output tokens, for
// example "⏱ 4 min · fable-5-1 · ✏️ 3 files · ↑ 12k tokens". A part whose
// value is unknown is left out; nothing known is the empty string.
func (m TurnMeta) Line() string {
	var parts []string
	if d, ok := m.Duration(); ok {
		parts = append(parts, "⏱ "+formatTurnDuration(d))
	}
	if model := ShortModel(m.Model); model != "" {
		parts = append(parts, model)
	}
	switch n := len(m.Files); {
	case n == 1:
		parts = append(parts, "✏️ 1 file")
	case n > 1:
		parts = append(parts, fmt.Sprintf("✏️ %d files", n))
	}
	if m.OutputTokens > 0 {
		parts = append(parts, "↑ "+formatTokens(m.OutputTokens)+" tokens")
	}
	return strings.Join(parts, " · ")
}

// Duration is Ended minus Started; false when either is unknown or Ended
// is not after Started.
func (m TurnMeta) Duration() (time.Duration, bool) {
	if m.Started.IsZero() || m.Ended.IsZero() || !m.Ended.After(m.Started) {
		return 0, false
	}
	return m.Ended.Sub(m.Started), true
}

// formatTurnDuration renders "42 s" under a minute, "4 min" under an hour
// (rounded to the nearest minute, never below 1 min) and "1 h 12 min"
// from an hour on ("2 h" when the minutes round to zero).
func formatTurnDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d s", int(d.Round(time.Second)/time.Second))
	}
	minutes := int(d.Round(time.Minute) / time.Minute)
	if minutes < 1 {
		minutes = 1
	}
	if minutes < 60 {
		return fmt.Sprintf("%d min", minutes)
	}
	if rest := minutes % 60; rest > 0 {
		return fmt.Sprintf("%d h %d min", minutes/60, rest)
	}
	return fmt.Sprintf("%d h", minutes/60)
}

// formatTokens renders a token count: "954" under a thousand, "4.6k"
// under ten thousand (one decimal) and "46k" above.
func formatTokens(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 9950:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%dk", int(math.Round(float64(n)/1000)))
	}
}

// ShortModel is the model name as the summary line shows it: a "claude-"
// prefix and a trailing "-YYYYMMDD" date segment are dropped, so
// "claude-haiku-4-5-20251001" becomes "haiku-4-5" and "claude-fable-5-1"
// "fable-5-1"; any other name is returned unchanged.
func ShortModel(model string) string {
	model = strings.TrimSpace(model)
	model = strings.TrimPrefix(model, "claude-")
	if i := strings.LastIndex(model, "-"); i > 0 && isDateSegment(model[i+1:]) {
		model = model[:i]
	}
	return model
}

// isDateSegment reports an eight-digit YYYYMMDD string.
func isDateSegment(s string) bool {
	if len(s) != 8 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
