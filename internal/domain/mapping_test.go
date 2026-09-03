package domain_test

import (
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

var t0 = time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

func agent(pane, term, name string, st domain.Status) domain.Agent {
	return domain.Agent{Key: domain.Key{PaneID: pane, TerminalID: term}, Name: name, Status: st}
}

func linked(t *testing.T, m *domain.Mapping, a domain.Agent, thread int) {
	t.Helper()
	m.Link(a.Key, domain.Topic{ThreadID: thread}, a, t0)
}

func TestParseKey(t *testing.T) {
	tests := []struct {
		in   string
		want domain.Key
		ok   bool
	}{
		{"p1/t2", domain.Key{PaneID: "p1", TerminalID: "t2"}, true},
		{"p1", domain.Key{}, false},
		{"/t2", domain.Key{}, false},
		{"p1/", domain.Key{}, false},
	}
	for _, tt := range tests {
		got, ok := domain.ParseKey(tt.in)
		if ok != tt.ok || got != tt.want {
			t.Fatalf("ParseKey(%q) = %v,%v want %v,%v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestMappingLinkStoresDesiredOrConfirmedName(t *testing.T) {
	m := domain.NewMapping(-1)
	a := agent("p1", "t1", "reviewer", domain.StatusWorking)
	e := m.Link(a.Key, domain.Topic{ThreadID: 5}, a, t0)
	if e.Name != "reviewer" || e.Status != domain.StatusWorking || e.ThreadID != 5 {
		t.Fatalf("entry = %+v", *e)
	}
	e = m.Link(a.Key, domain.Topic{ThreadID: 6, Name: "⚙️ rev"}, a, t0)
	if e.Name != "⚙️ rev" {
		t.Fatalf("confirmed name not kept: %q", e.Name)
	}
}

func TestMappingDiff(t *testing.T) {
	tests := []struct {
		name       string
		after      domain.Agent
		wantChange bool
		wantName   bool
		wantStatus bool
	}{
		{"no change", agent("p1", "t1", "reviewer", domain.StatusWorking), false, false, false},
		{"label change", agent("p1", "t1", "fixer", domain.StatusWorking), true, true, true},
		{"status change", agent("p1", "t1", "reviewer", domain.StatusIdle), true, false, true},
		{"both", agent("p1", "t1", "fixer", domain.StatusIdle), true, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := domain.NewMapping(-1)
			linked(t, m, agent("p1", "t1", "reviewer", domain.StatusWorking), 1)
			p, changed := m.Diff(tt.after.Key, tt.after)
			if changed != tt.wantChange || (p.Name != nil) != tt.wantName || (p.Status != nil) != tt.wantStatus {
				t.Fatalf("Diff = %+v,%v", p, changed)
			}
		})
	}
	t.Run("unknown key", func(t *testing.T) {
		m := domain.NewMapping(-1)
		if _, changed := m.Diff(domain.Key{PaneID: "x", TerminalID: "y"}, agent("x", "y", "a", domain.StatusIdle)); changed {
			t.Fatal("unknown key reported a change")
		}
	})
}

func TestMappingApplyAndExit(t *testing.T) {
	m := domain.NewMapping(-1)
	a := agent("p1", "t1", "reviewer", domain.StatusWorking)
	linked(t, m, a, 1)
	name := "💤 reviewer"
	st := domain.StatusIdle
	m.Apply(a.Key, domain.TopicPatch{Name: &name, Status: &st}, t0.Add(time.Second))
	e, _ := m.TopicFor(a.Key)
	if e.Name != name || e.Status != st || !e.UpdatedAt.Equal(t0.Add(time.Second)) {
		t.Fatalf("after Apply: %+v", *e)
	}

	m.MarkExited(a.Key, t0.Add(2*time.Second))
	if e.Status != domain.StatusExited || e.Name != name {
		t.Fatalf("after MarkExited: %+v", *e)
	}
	if _, changed := m.Diff(a.Key, agent("p1", "t1", "reviewer", domain.StatusWorking)); changed {
		t.Fatal("exited entry must not diff")
	}
	if got := m.Unclosed(); len(got) != 1 || got[0] != a.Key {
		t.Fatalf("Unclosed = %v", got)
	}
	m.MarkClosed(a.Key, t0.Add(3*time.Second))
	if !e.Closed || len(m.Unclosed()) != 0 {
		t.Fatalf("after MarkClosed: closed=%v unclosed=%v", e.Closed, m.Unclosed())
	}
	m.Forget(a.Key)
	if _, ok := m.TopicFor(a.Key); ok {
		t.Fatal("Forget left the entry")
	}
}

func TestMappingOrphans(t *testing.T) {
	m := domain.NewMapping(-1)
	live := agent("p1", "t1", "a", domain.StatusWorking)
	gone := agent("p2", "t2", "b", domain.StatusWorking)
	old := agent("p3", "t3", "c", domain.StatusWorking)
	linked(t, m, live, 1)
	linked(t, m, gone, 2)
	linked(t, m, old, 3)
	m.MarkExited(old.Key, t0)

	got := m.Orphans(map[domain.Key]struct{}{live.Key: {}})
	if len(got) != 1 || got[0] != gone.Key {
		t.Fatalf("Orphans = %v, want [%v]", got, gone.Key)
	}
}

func TestMappingPrune(t *testing.T) {
	m := domain.NewMapping(-1)
	live := agent("p0", "t0", "live", domain.StatusWorking)
	linked(t, m, live, 0)
	for i := 1; i <= 4; i++ {
		a := agent("p"+string(rune('0'+i)), "t", "x", domain.StatusWorking)
		linked(t, m, a, i)
		m.MarkExited(a.Key, t0.Add(time.Duration(i)*time.Hour))
	}
	// Age alone removes nothing any more: the sweep owns that.
	if got := m.Prune(10); got != 0 {
		t.Fatalf("Prune under the cap removed %d", got)
	}
	if len(m.Topics) != 5 {
		t.Fatalf("entries = %d, want 5", len(m.Topics))
	}
	// Count: cap at 3 removes the two oldest exited.
	if got := m.Prune(3); got != 2 {
		t.Fatalf("Prune by count removed %d, want 2", got)
	}
	if _, ok := m.TopicFor(live.Key); !ok {
		t.Fatal("live entry pruned")
	}
	if _, ok := m.TopicFor(domain.Key{PaneID: "p4", TerminalID: "t"}); !ok {
		t.Fatal("newest exited entry pruned instead of the oldest")
	}
	// Cap 0 disables the prune.
	if got := m.Prune(0); got != 0 {
		t.Fatalf("Prune with cap 0 removed %d", got)
	}
}

func TestMappingStale(t *testing.T) {
	m := domain.NewMapping(-1)
	now := t0.Add(40 * 24 * time.Hour)
	live := agent("p0", "t0", "live", domain.StatusWorking)
	linked(t, m, live, 0)
	m.MarkClosed(live.Key, t0) // a closed live topic is never stale

	oldClosed := agent("p1", "t", "old", domain.StatusWorking)
	linked(t, m, oldClosed, 1)
	m.MarkExited(oldClosed.Key, t0)
	m.MarkClosed(oldClosed.Key, t0.Add(time.Hour))

	older := agent("p2", "t", "older", domain.StatusWorking)
	linked(t, m, older, 2)
	m.MarkExited(older.Key, t0)
	m.MarkClosed(older.Key, t0)
	m.Mute(older.Key, t0) // muted and exited still counts

	young := agent("p3", "t", "young", domain.StatusWorking)
	linked(t, m, young, 3)
	m.MarkExited(young.Key, now.Add(-time.Hour))
	m.MarkClosed(young.Key, now.Add(-time.Hour))

	open := agent("p4", "t", "open", domain.StatusWorking)
	linked(t, m, open, 4)
	m.MarkExited(open.Key, t0)
	m.MarkReopened(open.Key, t0) // exited but reopened by hand

	got := m.Stale(now, 30*24*time.Hour)
	if len(got) != 2 || got[0] != older.Key || got[1] != oldClosed.Key {
		t.Fatalf("Stale = %v, want [%v %v]", got, older.Key, oldClosed.Key)
	}
	if got := m.Stale(now, 0); len(got) != 3 {
		t.Fatalf("Stale with zero age = %v, want three closed exited entries", got)
	}
	l, e, mu := m.Counts()
	if l != 1 || e != 4 || mu != 1 {
		t.Fatalf("Counts = %d %d %d", l, e, mu)
	}
}

func TestMappingDedupeThreads(t *testing.T) {
	m := domain.NewMapping(-1)
	older := agent("p1", "t1", "a", domain.StatusWorking)
	newer := agent("p1", "t2", "a", domain.StatusWorking)
	other := agent("p2", "t1", "b", domain.StatusWorking)
	linked(t, m, older, 9)
	linked(t, m, newer, 9)
	linked(t, m, other, 10)
	e, _ := m.TopicFor(newer.Key)
	e.UpdatedAt = t0.Add(time.Minute)

	if got := m.DedupeThreads(); got != 1 {
		t.Fatalf("DedupeThreads removed %d, want 1", got)
	}
	if _, ok := m.TopicFor(older.Key); ok {
		t.Fatal("older duplicate kept")
	}
	if _, ok := m.TopicFor(newer.Key); !ok {
		t.Fatal("newer duplicate removed")
	}
	if keys := m.Keys(); len(keys) != 2 || keys[0] != newer.Key || keys[1] != other.Key {
		t.Fatalf("Keys = %v", keys)
	}
}

func TestMappingKeyForThread(t *testing.T) {
	m := domain.NewMapping(-1)
	a := agent("p1", "t1", "a", domain.StatusWorking)
	b := agent("p2", "t2", "b", domain.StatusWorking)
	linked(t, m, a, 7)
	linked(t, m, b, 8)
	if k, ok := m.KeyForThread(8); !ok || k != b.Key {
		t.Fatalf("KeyForThread(8) = %v,%v want %v", k, ok, b.Key)
	}
	if _, ok := m.KeyForThread(9); ok {
		t.Fatal("unknown thread found a key")
	}

	t.Run("newest entry wins on duplicates", func(t *testing.T) {
		m := domain.NewMapping(-1)
		old := agent("p1", "t1", "old", domain.StatusWorking)
		fresh := agent("p1", "t2", "fresh", domain.StatusWorking)
		m.Link(old.Key, domain.Topic{ThreadID: 5}, old, t0)
		m.Link(fresh.Key, domain.Topic{ThreadID: 5}, fresh, t0.Add(time.Minute))
		if k, ok := m.KeyForThread(5); !ok || k != fresh.Key {
			t.Fatalf("KeyForThread(5) = %v,%v want %v", k, ok, fresh.Key)
		}
	})
	t.Run("live entry wins a tie", func(t *testing.T) {
		m := domain.NewMapping(-1)
		gone := agent("p1", "t1", "gone", domain.StatusWorking)
		live := agent("p1", "t2", "live", domain.StatusWorking)
		m.Link(gone.Key, domain.Topic{ThreadID: 5}, gone, t0)
		m.Link(live.Key, domain.Topic{ThreadID: 5}, live, t0)
		m.MarkExited(gone.Key, t0)
		if k, ok := m.KeyForThread(5); !ok || k != live.Key {
			t.Fatalf("KeyForThread(5) = %v,%v want %v", k, ok, live.Key)
		}
	})
}

func TestMappingMuteUnmute(t *testing.T) {
	m := domain.NewMapping(-1)
	a := agent("p1", "t1", "a", domain.StatusWorking)
	linked(t, m, a, 1)
	m.Mute(a.Key, t0.Add(time.Minute))
	e, _ := m.TopicFor(a.Key)
	if !e.Muted || !e.UpdatedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("after Mute: %+v", *e)
	}
	m.Unmute(a.Key, t0.Add(2*time.Minute))
	if e.Muted || !e.UpdatedAt.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("after Unmute: %+v", *e)
	}
	m.Mute(domain.Key{PaneID: "x", TerminalID: "y"}, t0) // unknown key is a no-op
	if len(m.Topics) != 1 {
		t.Fatalf("unknown key created an entry: %d", len(m.Topics))
	}
}

func TestMappingMutedEntriesAreSkipped(t *testing.T) {
	m := domain.NewMapping(-1)
	mutedLive := agent("p1", "t1", "a", domain.StatusWorking)
	mutedExited := agent("p2", "t2", "b", domain.StatusWorking)
	plain := agent("p3", "t3", "c", domain.StatusWorking)
	linked(t, m, mutedLive, 1)
	linked(t, m, mutedExited, 2)
	linked(t, m, plain, 3)
	m.Mute(mutedLive.Key, t0)
	m.Mute(mutedExited.Key, t0)
	m.MarkExited(mutedExited.Key, t0)
	m.MarkExited(plain.Key, t0)

	if got := m.Orphans(map[domain.Key]struct{}{}); len(got) != 0 {
		t.Fatalf("Orphans listed a muted entry: %v", got)
	}
	if got := m.Unclosed(); len(got) != 1 || got[0] != plain.Key {
		t.Fatalf("Unclosed = %v, want [%v]", got, plain.Key)
	}
}

func TestMappingMarkReopened(t *testing.T) {
	m := domain.NewMapping(-1)
	a := agent("p1", "t1", "a", domain.StatusWorking)
	linked(t, m, a, 1)
	m.MarkClosed(a.Key, t0)
	m.MarkReopened(a.Key, t0.Add(time.Minute))
	e, _ := m.TopicFor(a.Key)
	if e.Closed || !e.UpdatedAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("after MarkReopened: %+v", *e)
	}
}
