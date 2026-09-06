package app

import (
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func TestFormatDuration(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{-5 * time.Second, ""},
		{0, ""},
		{59 * time.Second, ""},
		{time.Minute, "1 min"},
		{59*time.Minute + 59*time.Second, "59 min"},
		{time.Hour, "1 h 0 min"},
		{3*time.Hour + 7*time.Minute, "3 h 7 min"},
		{23*time.Hour + 59*time.Minute, "23 h 59 min"},
		{24 * time.Hour, "1 d 0 h"},
		{49*time.Hour + 30*time.Minute, "2 d 1 h"},
	} {
		if got := formatDuration(tc.d); got != tc.want {
			t.Errorf("formatDuration(%s) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestStatusViewRender(t *testing.T) {
	view := newTopicView()
	m := domain.NewMapping(-1001234567890)
	a := domain.Agent{Key: domain.Key{PaneID: "p1", TerminalID: "t1"}, WorkspaceLabel: "ws", Name: "beta", Status: domain.StatusBlocked}
	b := domain.Agent{Key: domain.Key{PaneID: "p2", TerminalID: "t2"}, WorkspaceLabel: "ws", Name: "alpha <x>", Status: domain.StatusWorking}
	gone := domain.Agent{Key: domain.Key{PaneID: "p3", TerminalID: "t3"}, WorkspaceLabel: "ws", Name: "old", Status: domain.StatusExited}
	m.Link(a.Key, domain.Topic{ThreadID: 11}, a, tb0)
	m.Link(b.Key, domain.Topic{ThreadID: 12}, b, tb0)
	view.publish(m)
	now := tb0.Add(time.Hour)
	v := statusView{
		agents:   []domain.Agent{a, b, gone},
		topics:   view,
		icons:    domain.DefaultStatusIcons(),
		syncOff:  true,
		presence: "🔕 quiet: you are at the desk (/away to override)\n",
		chatID:   -1001234567890,
		since:    map[domain.Key]time.Time{a.Key: now.Add(-5 * time.Minute), b.Key: now.Add(-30 * time.Second)},
		now:      now,
		footer:   "<i>updated 13:00</i>",
	}
	want := "🔇 Herdr → Telegram sync is off (/options)\n" +
		"🔕 quiet: you are at the desk (/away to override)\n" +
		"2 agents\n" +
		"⚡ <a href=\"https://t.me/c/1234567890/12\">ws · alpha &lt;x&gt;</a>\n" +
		"❓ <a href=\"https://t.me/c/1234567890/11\">ws · beta</a> · 5 min\n\n" +
		"<i>updated 13:00</i>"
	if got := v.render(); got != want {
		t.Errorf("render =\n%s\nwant\n%s", got, want)
	}
	// The body ignores the footer, so an equal body means no edit.
	v.footer = "<i>updated 13:01</i>"
	if got := v.body(); got+"\n\n<i>updated 13:01</i>" != v.render() {
		t.Errorf("body/render mismatch: %q", got)
	}
	if got := (statusView{presence: "🏃 away (manual) until /here\n"}).render(); got != "🏃 away (manual) until /here\nno agents" {
		t.Errorf("empty view = %q", got)
	}
	// A cap keeps the first lines and sums up the rest.
	capped := statusView{agents: []domain.Agent{a, b}, icons: domain.DefaultStatusIcons(), maxAgents: 1}
	if got := capped.render(); got != "2 agents\n⚡ ws · alpha &lt;x&gt;\n… +1 more" {
		t.Errorf("capped = %q", got)
	}
	// Without a topic view the label is plain text.
	if got := (statusView{agents: []domain.Agent{b}, icons: domain.DefaultStatusIcons()}).render(); got != "1 agent\n⚡ ws · alpha &lt;x&gt;" {
		t.Errorf("no topics = %q", got)
	}
}

func TestStatusIncludesDurationsFromSince(t *testing.T) {
	f := newBridgeFixture(t)
	a := f.add(t, "p1", "t1", "alpha", domain.StatusWorking)
	f.in.SetSince(func() map[domain.Key]time.Time {
		return map[domain.Key]time.Time{a.Key: f.clock.Now().Add(-90 * time.Minute)}
	})
	if got := f.in.statusSummary(); got != "1 agent\n⚡ <a href=\"https://t.me/c/1234567890/101\">ws · alpha</a> · 1 h 30 min" {
		t.Errorf("status with since = %q", got)
	}
	f.in.SetSince(nil)
	if got := f.in.statusSummary(); got != "1 agent\n⚡ <a href=\"https://t.me/c/1234567890/101\">ws · alpha</a>" {
		t.Errorf("status without since = %q", got)
	}
}
