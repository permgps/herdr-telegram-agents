package app

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordHandler keeps every record so tests can assert on levels and
// messages without touching the real logger.
type recordHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}
func (h *recordHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordHandler) WithGroup(string) slog.Handler      { return h }

func (h *recordHandler) count(level slog.Level, msg string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, r := range h.records {
		if r.Level == level && r.Message == msg {
			n++
		}
	}
	return n
}

func TestReportDropsOncePerInterval(t *testing.T) {
	h := &recordHandler{}
	b := &Bridge{log: slog.New(h)}
	d := &Daemon{bridge: b, log: slog.New(h)}
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

	d.reportDrops(now)
	if got := h.count(slog.LevelWarn, "bridge dropped jobs"); got != 0 {
		t.Fatalf("warned with no drops: %d", got)
	}
	b.dropped.Store(44)
	d.reportDrops(now)
	if got := h.count(slog.LevelWarn, "bridge dropped jobs"); got != 1 {
		t.Fatalf("first report: warnings = %d", got)
	}
	b.dropped.Store(50)
	d.reportDrops(now.Add(30 * time.Second))
	if got := h.count(slog.LevelWarn, "bridge dropped jobs"); got != 1 {
		t.Fatalf("inside the interval: warnings = %d", got)
	}
	d.reportDrops(now.Add(dropReportInterval))
	if got := h.count(slog.LevelWarn, "bridge dropped jobs"); got != 2 {
		t.Fatalf("after the interval: warnings = %d", got)
	}
	if d.lastDropped != 50 {
		t.Fatalf("lastDropped = %d", d.lastDropped)
	}
	// Nothing new: no third warning however much time passes.
	d.reportDrops(now.Add(10 * dropReportInterval))
	if got := h.count(slog.LevelWarn, "bridge dropped jobs"); got != 2 {
		t.Fatalf("no new drops: warnings = %d", got)
	}
}

func TestStatsLine(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		s    Stats
		want string
	}{
		{"fresh", Stats{Version: "v0.1.0", PID: 42, Since: now.Add(-90 * time.Second), Agents: 3, HerdrOK: true, DeleteAfterDays: 30},
			"version=v0.1.0 pid=42 uptime=1m30s agents=3 dropped=0 herdr=ok sync=on cleanup=30d quiet=off"},
		{"failing", Stats{PID: 7, Since: now.Add(-time.Hour), Agents: 0, Dropped: 12, HerdrFailingSince: now.Add(-25 * time.Second)},
			"version=dev pid=7 uptime=1h0m0s agents=0 dropped=12 herdr=failing since 25s sync=on cleanup=off quiet=off"},
		{"zero since", Stats{Version: "x", HerdrOK: true}, "version=x pid=0 uptime=0s agents=0 dropped=0 herdr=ok sync=on cleanup=off quiet=off"},
	}
	for _, c := range cases {
		if got := StatsLine(c.s, now); got != c.want {
			t.Errorf("%s: StatsLine = %q, want %q", c.name, got, c.want)
		}
		if strings.Contains(StatsLine(c.s, now), "\n") {
			t.Errorf("%s: multi-line status", c.name)
		}
	}
}
