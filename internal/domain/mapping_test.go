package domain_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

var t0 = time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

func sessionKey(pane, terminal, session string) domain.Key {
	digest := (domain.SessionTuple{Source: "test", Agent: "codex", Kind: "id", Value: session}).Digest()
	return domain.Key{PaneID: pane, TerminalID: terminal, SessionDigest: digest}
}

func agent(pane, term, name string, st domain.Status) domain.Agent {
	return domain.Agent{Key: domain.Key{PaneID: pane, TerminalID: term}, Name: name, Status: st}
}

func linked(t *testing.T, m *domain.Mapping, a domain.Agent, thread int) {
	t.Helper()
	m.Link(a.Key, domain.Topic{ThreadID: thread}, a, t0)
}

func TestParseKey(t *testing.T) {
	digest := (domain.SessionTuple{Source: "herdr", Agent: "agent-7", Kind: "id", Value: "session-7"}).Digest()
	sessioned := domain.Key{PaneID: "pane:/α", TerminalID: "term:/β", SessionDigest: digest}
	tests := []struct {
		in   string
		want domain.Key
		ok   bool
	}{
		{"p1/t2", domain.Key{PaneID: "p1", TerminalID: "t2"}, true},
		{sessioned.String(), sessioned, true},
		{"p1", domain.Key{}, false},
		{"/t2", domain.Key{}, false},
		{"p1/", domain.Key{}, false},
		{"v2:", domain.Key{}, false},
		{"v2::dA:" + digest, domain.Key{}, false},
		{"v2:cA::" + digest, domain.Key{}, false},
		{"v2:*:dA:" + digest, domain.Key{}, false},
		{"v2:cA:dA:short", domain.Key{}, false},
		{"v2:cA:dA:A" + strings.Repeat("0", 63), domain.Key{}, false},
		{"v2:cA:dA:" + digest + ":extra", domain.Key{}, false},
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

func TestMappingMetadataRefreshesOnlyWhenKnownValuesChange(t *testing.T) {
	m := domain.NewMapping(-1001)
	a := domain.Agent{
		Key: domain.Key{PaneID: "p1", TerminalID: "t1"}, Name: "reviewer", Kind: "codex",
		Cwd: "/work/one", Status: domain.StatusWorking,
	}
	e := m.Link(a.Key, domain.Topic{ThreadID: 5}, a, t0)
	if e.Cwd != a.Cwd || e.AgentKind != a.Kind {
		t.Fatalf("linked metadata = cwd %q kind %q", e.Cwd, e.AgentKind)
	}

	if m.UpdateMetadata(a.Key, a) {
		t.Fatal("unchanged metadata reported a change")
	}
	changed := a
	changed.Cwd = "/work/two"
	changed.Kind = "claude"
	if !m.UpdateMetadata(a.Key, changed) {
		t.Fatal("changed metadata was not reported")
	}
	if e.Cwd != changed.Cwd || e.AgentKind != changed.Kind || !e.UpdatedAt.Equal(t0) {
		t.Fatalf("metadata refresh changed unexpected fields: %+v", e)
	}
	if m.UpdateMetadata(a.Key, domain.Agent{Key: a.Key}) || e.Cwd != changed.Cwd || e.AgentKind != changed.Kind {
		t.Fatalf("unknown metadata erased known values: %+v", e)
	}
	if m.UpdateMetadata(domain.Key{PaneID: "missing", TerminalID: "t"}, changed) {
		t.Fatal("missing entry reported a metadata change")
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

func TestMappingPruneRetainsTopicReferencesUntilDeletion(t *testing.T) {
	m := domain.NewMapping(-1)
	live := agent("p0", "t0", "live", domain.StatusWorking)
	linked(t, m, live, 0)
	for i := 1; i <= 501; i++ {
		a := agent(fmt.Sprintf("p%d", i), "t", "x", domain.StatusWorking)
		linked(t, m, a, i)
		m.MarkExited(a.Key, t0.Add(time.Duration(i)*time.Hour))
	}
	// A count cap cannot be enforced by forgetting topics Telegram still has.
	if got := m.Prune(500); got != 0 {
		t.Fatalf("Prune removed %d Telegram topic references", got)
	}
	if len(m.Topics) != 502 {
		t.Fatalf("entries = %d, want 502", len(m.Topics))
	}
	if _, ok := m.TopicFor(live.Key); !ok {
		t.Fatal("live entry pruned")
	}
	for i := 1; i <= 501; i++ {
		if _, ok := m.TopicFor(domain.Key{PaneID: fmt.Sprintf("p%d", i), TerminalID: "t"}); !ok {
			t.Fatalf("exited topic %d lost before Telegram deletion", i)
		}
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

func TestPlanReassociationPrefersExactAndSameSession(t *testing.T) {
	m := domain.NewMapping(-1001)
	exactKey := sessionKey("pane-exact", "term-same", "session-exact")
	exactAgent := domain.Agent{Key: exactKey, Name: "exact", Kind: "codex", Cwd: "/work/exact", Status: domain.StatusWorking}
	linked(t, m, exactAgent, 10)

	oldKey := sessionKey("pane-resume", "term-old", "session-resume")
	oldAgent := domain.Agent{Key: oldKey, Name: "reviewer", Kind: "claude", Cwd: "/work/review", Status: domain.StatusWorking}
	entry := m.Link(oldKey, domain.Topic{ThreadID: 11}, oldAgent, t0)
	entry.Status = domain.StatusExited
	entry.Closed = true
	entry.Muted = true
	entry.UpdatedAt = t0.Add(time.Hour)
	newKey := oldKey
	newKey.TerminalID = "term-new"
	newAgent := oldAgent
	newAgent.Key = newKey

	plan := m.PlanReassociation([]domain.Agent{newAgent, exactAgent})
	if len(plan.Assignments) != 2 || len(plan.Ambiguous) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
	gotReasons := map[domain.Key]domain.ReassociationReason{}
	for _, assignment := range plan.Assignments {
		gotReasons[assignment.To] = assignment.Reason
		if assignment.To == exactKey && assignment.From != exactKey {
			t.Fatalf("exact assignment = %+v", assignment)
		}
		if assignment.To == newKey && assignment.From != oldKey {
			t.Fatalf("session assignment = %+v", assignment)
		}
	}
	if gotReasons[exactKey] != domain.ReassociationExact || gotReasons[newKey] != domain.ReassociationSession {
		t.Fatalf("assignment reasons = %v", gotReasons)
	}
	if _, ok := m.TopicFor(oldKey); !ok {
		t.Fatal("planning mutated the mapping")
	}
	if err := m.ApplyReassociation(plan); err != nil {
		t.Fatalf("ApplyReassociation: %v", err)
	}
	if len(m.Topics) != 2 {
		t.Fatalf("mapping entries = %d, want 2", len(m.Topics))
	}
	if _, ok := m.TopicFor(oldKey); ok {
		t.Fatal("old session key remained after reassociation")
	}
	got, ok := m.TopicFor(newKey)
	if !ok || got != entry || got.ThreadID != 11 || got.Status != domain.StatusExited || !got.Closed || !got.Muted ||
		got.Cwd != oldAgent.Cwd || got.AgentKind != oldAgent.Kind || !got.UpdatedAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("reassociated entry did not retain state: %+v ok=%v", got, ok)
	}
}

func TestPlanReassociationFallbackRequirements(t *testing.T) {
	tests := []struct {
		name      string
		entryName string
		entryCwd  string
		entryKind string
		liveCwd   string
		liveKind  string
		wantMatch bool
	}{
		{name: "v1 without cwd uses nonempty live cwd and canonical name", entryName: "⚙️ reviewer", liveCwd: "/work/repo", liveKind: "codex", wantMatch: true},
		{name: "known cwd and kind match", entryName: "reviewer", entryCwd: "/work/repo", entryKind: "codex", liveCwd: "/work/repo", liveKind: "codex", wantMatch: true},
		{name: "live cwd is required", entryName: "reviewer", liveKind: "codex"},
		{name: "known cwd must match", entryName: "reviewer", entryCwd: "/work/old", liveCwd: "/work/new", liveKind: "codex"},
		{name: "known kind must match", entryName: "reviewer", entryKind: "claude", liveCwd: "/work/repo", liveKind: "codex"},
		{name: "canonical topic name must match", entryName: "different", liveCwd: "/work/repo", liveKind: "codex"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := domain.NewMapping(-1001)
			oldKey := domain.Key{PaneID: "pane-1", TerminalID: "term-old"}
			newKey := domain.Key{PaneID: "pane-1", TerminalID: "term-new"}
			m.Topics[oldKey.String()] = &domain.TopicEntry{
				ThreadID: 42, Name: tt.entryName, Status: domain.StatusWorking,
				Cwd: tt.entryCwd, AgentKind: tt.entryKind,
				LegacyNoCwd: tt.name == "v1 without cwd uses nonempty live cwd and canonical name",
				UpdatedAt:   t0,
			}
			live := domain.Agent{Key: newKey, Name: "reviewer", Kind: tt.liveKind, Cwd: tt.liveCwd, Status: domain.StatusIdle}
			plan := m.PlanReassociation([]domain.Agent{live})
			gotMatch := len(plan.Assignments) == 1 && plan.Assignments[0].From == oldKey &&
				plan.Assignments[0].To == newKey && plan.Assignments[0].Reason == domain.ReassociationFallback
			if gotMatch != tt.wantMatch {
				t.Fatalf("plan = %+v, want match %v", plan, tt.wantMatch)
			}
			if _, ok := m.TopicFor(oldKey); !ok {
				t.Fatal("planning mutated the mapping")
			}
		})
	}
}

func TestPlanReassociationDoesNotFallbackFromNewEntryWithoutCwd(t *testing.T) {
	m := domain.NewMapping(-1001)
	previous := domain.Agent{
		Key:  domain.Key{PaneID: "pane-1", TerminalID: "term-old"},
		Name: "reviewer", Kind: "codex", Status: domain.StatusWorking,
	}
	m.Link(previous.Key, domain.Topic{ThreadID: 42}, previous, t0)
	current := previous
	current.Key.TerminalID = "term-new"
	current.Cwd = "/work/repo"

	plan := m.PlanReassociation([]domain.Agent{current})
	if len(plan.Assignments) != 0 {
		t.Fatalf("new entry with unknown cwd was adopted: %+v", plan.Assignments)
	}
}

func TestPlanReassociationRejectsConflictsAndAmbiguity(t *testing.T) {
	t.Run("known conflicting sessions never fall back", func(t *testing.T) {
		m := domain.NewMapping(-1001)
		oldKey := sessionKey("pane-1", "term-old", "old-session")
		newKey := sessionKey("pane-1", "term-new", "new-session")
		m.Topics[oldKey.String()] = &domain.TopicEntry{
			ThreadID: 42, Name: "reviewer", Status: domain.StatusWorking, Cwd: "/work/repo", AgentKind: "codex",
		}
		live := domain.Agent{Key: newKey, Name: "reviewer", Kind: "codex", Cwd: "/work/repo", Status: domain.StatusIdle}
		plan := m.PlanReassociation([]domain.Agent{live})
		if len(plan.Assignments) != 0 || len(plan.Ambiguous) != 0 {
			t.Fatalf("conflicting sessions matched: %+v", plan)
		}
	})

	t.Run("duplicate same-session mappings are ambiguous", func(t *testing.T) {
		m := domain.NewMapping(-1001)
		oldA := sessionKey("pane-1", "term-old-a", "same-session")
		oldB := sessionKey("pane-1", "term-old-b", "same-session")
		m.Topics[oldA.String()] = &domain.TopicEntry{ThreadID: 1, Name: "reviewer", Status: domain.StatusWorking}
		m.Topics[oldB.String()] = &domain.TopicEntry{ThreadID: 2, Name: "reviewer", Status: domain.StatusExited}
		liveKey := sessionKey("pane-1", "term-new", "same-session")
		plan := m.PlanReassociation([]domain.Agent{{Key: liveKey, Name: "reviewer", Status: domain.StatusIdle}})
		if len(plan.Assignments) != 0 || len(plan.Ambiguous) != 1 ||
			plan.Ambiguous[0].CandidateCount != 2 || plan.Ambiguous[0].Reason != domain.ReassociationAmbiguousSession {
			t.Fatalf("duplicate session plan = %+v", plan)
		}
	})

	t.Run("sole live legacy candidate beats closed generations", func(t *testing.T) {
		m := domain.NewMapping(-1001)
		closed := domain.Key{PaneID: "pane-1", TerminalID: "term-closed"}
		current := domain.Key{PaneID: "pane-1", TerminalID: "term-current"}
		m.Topics[closed.String()] = &domain.TopicEntry{ThreadID: 1, Name: "reviewer", Status: domain.StatusExited, Closed: true, Cwd: "/repo", AgentKind: "codex"}
		m.Topics[current.String()] = &domain.TopicEntry{ThreadID: 2, Name: "reviewer", Status: domain.StatusWorking, Cwd: "/repo", AgentKind: "codex"}
		liveKey := domain.Key{PaneID: "pane-1", TerminalID: "term-live"}
		live := domain.Agent{Key: liveKey, Name: "reviewer", Kind: "codex", Cwd: "/repo", Status: domain.StatusIdle}
		plan := m.PlanReassociation([]domain.Agent{live})
		if len(plan.Assignments) != 1 || plan.Assignments[0].From != current || len(plan.Ambiguous) != 0 {
			t.Fatalf("plan = %+v, want the sole live candidate", plan)
		}
	})

	t.Run("multiple closed legacy candidates are ambiguous", func(t *testing.T) {
		m := domain.NewMapping(-1001)
		for _, term := range []string{"term-old-1", "term-old-2"} {
			key := domain.Key{PaneID: "pane-1", TerminalID: term}
			m.Topics[key.String()] = &domain.TopicEntry{ThreadID: len(m.Topics) + 1, Name: "reviewer", Status: domain.StatusExited, Closed: true, Cwd: "/repo", AgentKind: "codex"}
		}
		liveKey := domain.Key{PaneID: "pane-1", TerminalID: "term-live"}
		live := domain.Agent{Key: liveKey, Name: "reviewer", Kind: "codex", Cwd: "/repo", Status: domain.StatusIdle}
		plan := m.PlanReassociation([]domain.Agent{live})
		if len(plan.Assignments) != 0 || len(plan.Ambiguous) != 1 || plan.Ambiguous[0].LiveKey != liveKey ||
			plan.Ambiguous[0].CandidateCount != 2 || plan.Ambiguous[0].Reason != domain.ReassociationAmbiguousFallback {
			t.Fatalf("ambiguous plan = %+v", plan)
		}
		if len(m.Topics) != 2 {
			t.Fatalf("planning changed mapping size to %d", len(m.Topics))
		}
	})

	t.Run("one legacy entry cannot be claimed by two live agents", func(t *testing.T) {
		m := domain.NewMapping(-1001)
		oldKey := domain.Key{PaneID: "pane-1", TerminalID: "term-old"}
		m.Topics[oldKey.String()] = &domain.TopicEntry{ThreadID: 42, Name: "reviewer", Status: domain.StatusWorking, Cwd: "/repo", AgentKind: "codex"}
		liveA := domain.Agent{Key: domain.Key{PaneID: "pane-1", TerminalID: "term-a"}, Name: "reviewer", Kind: "codex", Cwd: "/repo"}
		liveB := domain.Agent{Key: domain.Key{PaneID: "pane-1", TerminalID: "term-b"}, Name: "reviewer", Kind: "codex", Cwd: "/repo"}
		plan := m.PlanReassociation([]domain.Agent{liveA, liveB})
		if len(plan.Assignments) != 0 || len(plan.Ambiguous) != 2 {
			t.Fatalf("many-to-one plan = %+v", plan)
		}
		for _, ambiguity := range plan.Ambiguous {
			if ambiguity.CandidateCount != 2 || ambiguity.Reason != domain.ReassociationAmbiguousManyToOne {
				t.Fatalf("ambiguity = %+v", ambiguity)
			}
		}
	})
}

func TestApplyReassociationIsAtomicOnFailure(t *testing.T) {
	m := domain.NewMapping(-1001)
	from := domain.Key{PaneID: "pane-1", TerminalID: "old"}
	occupied := domain.Key{PaneID: "pane-1", TerminalID: "occupied"}
	fromEntry := &domain.TopicEntry{ThreadID: 1, Name: "old", Status: domain.StatusWorking, UpdatedAt: t0}
	occupiedEntry := &domain.TopicEntry{ThreadID: 2, Name: "occupied", Status: domain.StatusIdle, UpdatedAt: t0}
	m.Topics[from.String()] = fromEntry
	m.Topics[occupied.String()] = occupiedEntry
	plan := domain.ReassociationPlan{Assignments: []domain.ReassociationAssignment{{From: from, To: occupied, Reason: domain.ReassociationSession}}}
	if err := m.ApplyReassociation(plan); err == nil {
		t.Fatal("ApplyReassociation succeeded with an occupied destination")
	}
	if len(m.Topics) != 2 || m.Topics[from.String()] != fromEntry || m.Topics[occupied.String()] != occupiedEntry {
		t.Fatalf("failed apply mutated mapping: %+v", m.Topics)
	}
}

func TestPlanReassociationHandles56EntriesAnd24LiveAgents(t *testing.T) {
	m := domain.NewMapping(-1001)
	live := make([]domain.Agent, 0, 24)
	for i := 0; i < 24; i++ {
		pane := fmt.Sprintf("pane-%02d", i)
		oldKey := sessionKey(pane, fmt.Sprintf("term-old-%02d", i), fmt.Sprintf("session-%02d", i))
		newKey := oldKey
		newKey.TerminalID = fmt.Sprintf("term-new-%02d", i)
		a := domain.Agent{Key: oldKey, Name: fmt.Sprintf("agent-%02d", i), Kind: "codex", Cwd: fmt.Sprintf("/repo/%02d", i), Status: domain.StatusWorking}
		m.Link(oldKey, domain.Topic{ThreadID: i + 1}, a, t0)
		a.Key = newKey
		live = append(live, a)
	}
	for i := 0; i < 32; i++ {
		key := sessionKey("pane-stale", fmt.Sprintf("term-%02d", i), fmt.Sprintf("stale-%02d", i))
		m.Topics[key.String()] = &domain.TopicEntry{ThreadID: 100 + i, Name: fmt.Sprintf("stale-%02d", i), Status: domain.StatusExited, Closed: true, UpdatedAt: t0}
	}
	if len(m.Topics) != 56 {
		t.Fatalf("seed mapping entries = %d, want 56", len(m.Topics))
	}
	plan := m.PlanReassociation(live)
	if len(plan.Assignments) != 24 || len(plan.Ambiguous) != 0 {
		t.Fatalf("large mapping plan assignments=%d ambiguities=%+v", len(plan.Assignments), plan.Ambiguous)
	}
	if err := m.ApplyReassociation(plan); err != nil {
		t.Fatalf("ApplyReassociation: %v", err)
	}
	if len(m.Topics) != 56 {
		t.Fatalf("reassociation changed mapping count to %d", len(m.Topics))
	}
	for _, a := range live {
		if _, ok := m.TopicFor(a.Key); !ok {
			t.Fatalf("live key %s has no topic after reassociation", a.Key.String())
		}
	}
}
