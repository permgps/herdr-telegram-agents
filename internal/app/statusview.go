package app

import (
	"fmt"
	"html"
	"sort"
	"strings"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// statusView renders the agent overview shared by /status and the General
// dashboard, so the two never disagree: the sync-off line, the presence
// header, "N agents", then one line per live agent with its status icon,
// its label linked to the topic and, when known, how long it has been in
// that status. The dashboard adds a footer; /status has none.
type statusView struct {
	agents  []domain.Agent
	topics  *topicView
	icons   domain.StatusIcons
	syncOff bool
	// presence is the header line about quiet mode with its trailing
	// newline, or empty (see presenceHeaderText).
	presence string
	chatID   int64
	// since holds when each agent entered its current status; agents
	// missing from it get no duration.
	since  map[domain.Key]time.Time
	now    time.Time
	footer string
}

// render returns the HTML text.
func (v statusView) render() string {
	body := v.body()
	if v.footer == "" {
		return body
	}
	return body + "\n\n" + v.footer
}

// body renders everything but the footer; the dashboard hashes it to skip
// edits that would only move the footer's clock.
func (v statusView) body() string {
	live := make([]domain.Agent, 0, len(v.agents))
	for _, a := range v.agents {
		if a.Status.Live() {
			live = append(live, a)
		}
	}
	header := ""
	if v.syncOff {
		header = "🔇 Herdr → Telegram sync is off (/options)\n"
	}
	header += v.presence
	if len(live) == 0 {
		return header + "no agents"
	}
	sort.Slice(live, func(a, b int) bool {
		if live[a].Label() != live[b].Label() {
			return live[a].Label() < live[b].Label()
		}
		return live[a].Key.String() < live[b].Key.String()
	})
	lines := make([]string, 0, len(live)+1)
	lines = append(lines, header+plural(len(live), "agent"))
	for _, a := range live {
		label := html.EscapeString(a.Label())
		if v.topics != nil {
			if entry, ok := v.topics.Entry(a.Key); ok && entry.Status.Live() {
				label = fmt.Sprintf(`<a href="%s">%s</a>`, topicLink(v.chatID, entry.ThreadID), label)
			}
		}
		line := v.icons.For(a.Status) + " " + label
		if at, ok := v.since[a.Key]; ok {
			if d := formatDuration(v.now.Sub(at)); d != "" {
				line += " · " + d
			}
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// formatDuration words how long an agent has been in its status: nothing
// under a minute, "N min" under an hour, "N h M min" under a day, "N d M h"
// beyond.
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	minutes := int(d / time.Minute)
	switch {
	case minutes < 1:
		return ""
	case minutes < 60:
		return fmt.Sprintf("%d min", minutes)
	case minutes < 24*60:
		return fmt.Sprintf("%d h %d min", minutes/60, minutes%60)
	default:
		hours := minutes / 60
		return fmt.Sprintf("%d d %d h", hours/24, hours%24)
	}
}

// presenceHeaderText is the /status and dashboard line about quiet mode,
// with a trailing newline; empty when there is nothing to say (quiet off,
// no tracker, or away by the automatic verdict).
func presenceHeaderText(p *Presence, quietEnabled bool, clock domain.Clock) string {
	if p == nil || !quietEnabled {
		return ""
	}
	st := p.State()
	switch {
	case st.ManualAway && st.Until.IsZero():
		return presenceHeaderOpen
	case st.ManualAway:
		return fmt.Sprintf(presenceHeaderManual, wallClock(clock, st.Until))
	case st.Quiet:
		return presenceHeaderQuiet
	}
	return ""
}

// wallClock renders an instant as wall-clock time in the clock's location
// (local time on a real clock).
func wallClock(clock domain.Clock, at time.Time) string {
	return at.In(clock.Now().Location()).Format(presenceTimeLayout)
}
