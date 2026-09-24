package state_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/adapters/state"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func TestMappingStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := state.NewMappingStore(dir, nil)
	ctx := context.Background()

	empty, err := s.Load(ctx)
	if err != nil || len(empty.Topics) != 0 || empty.ChatID != 0 {
		t.Fatalf("Load on missing = %+v, %v", empty, err)
	}

	m := domain.NewMapping(-1001)
	a := domain.Agent{
		Key: domain.Key{PaneID: "p1", TerminalID: "t1"}, Name: "reviewer", Kind: "codex",
		Cwd: "/work/project", Status: domain.StatusWorking,
	}
	at := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	m.Link(a.Key, domain.Topic{ThreadID: 42}, a, at)
	m.MarkExited(a.Key, at.Add(time.Minute))
	m.MarkClosed(a.Key, at.Add(time.Minute))
	m.Mute(a.Key, at.Add(time.Minute))
	if err := s.Save(ctx, m); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := got.TopicFor(a.Key)
	if !ok || got.ChatID != -1001 || got.Version != domain.MappingVersion ||
		e.ThreadID != 42 || e.Name != "reviewer" || e.Status != domain.StatusExited || !e.Closed || !e.Muted ||
		e.Cwd != a.Cwd || e.AgentKind != a.Kind || !e.UpdatedAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("round trip: chat=%d version=%d entry=%+v ok=%v", got.ChatID, got.Version, e, ok)
	}
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"version": 2`, `"p1/t1"`, `"thread_id": 42`, `"status": "exited"`, `"closed": true`, `"muted": true`, `"cwd": "/work/project"`, `"agent_kind": "codex"`, `"updated_at"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("file lacks %s:\n%s", key, raw)
		}
	}
	if err := s.Save(ctx, got); err != nil {
		t.Fatal(err)
	}
	stable, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stable, raw) {
		t.Fatalf("load/save changed valid mapping bytes:\nbefore:\n%s\nafter:\n%s", raw, stable)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

func TestMappingStorePendingIntentsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := state.NewMappingStore(dir, nil)
	mapping := domain.NewMapping(-1001)
	key := domain.Key{PaneID: "pane", TerminalID: "terminal"}.String()
	mapping.PendingCreates[key] = true
	mapping.PendingDashboard = true
	if err := store.Save(context.Background(), mapping); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.PendingCreates[key] || !loaded.PendingDashboard {
		t.Fatalf("create intents lost after restart: %+v", loaded)
	}
}

func TestMappingStoreMigratesV1WithoutLosingTopicState(t *testing.T) {
	dir := t.TempDir()
	var logs bytes.Buffer
	s := state.NewMappingStore(dir, slog.New(slog.NewTextHandler(&logs, nil)))
	ctx := context.Background()
	const old = `{"version":1,"chat_id":-1001,"dashboard_message_id":777,"topics":{"p1/t1":{"thread_id":42,"name":"reviewer","status":"exited","closed":true,"muted":true,"updated_at":"2026-09-02T12:01:00Z"}}}` + "\n"
	if err := os.WriteFile(s.Path(), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := m.TopicFor(domain.Key{PaneID: "p1", TerminalID: "t1"})
	if !ok || m.Version != 1 || m.ChatID != -1001 || m.Dashboard != 777 ||
		e.ThreadID != 42 || e.Name != "reviewer" || e.Status != domain.StatusExited ||
		!e.Closed || !e.Muted || e.Cwd != "" || e.AgentKind != "" || !e.LegacyNoCwd ||
		!e.UpdatedAt.Equal(time.Date(2026, 9, 2, 12, 1, 0, 0, time.UTC)) {
		t.Fatalf("version 1 load lost state: version=%d chat=%d dashboard=%d entry=%+v ok=%v", m.Version, m.ChatID, m.Dashboard, e, ok)
	}
	if err := s.Save(ctx, m); err != nil {
		t.Fatal(err)
	}
	if m.Version != domain.MappingVersion {
		t.Fatalf("in-memory version after migration = %d, want %d", m.Version, domain.MappingVersion)
	}
	if got := strings.Count(logs.String(), "mapping entry migrated"); got != 1 {
		t.Fatalf("migration log count = %d, want 1: %s", got, logs.String())
	}
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"version": 2`) || !strings.Contains(string(raw), `"legacy_no_cwd": true`) ||
		strings.Contains(string(raw), `"cwd"`) || strings.Contains(string(raw), `"agent_kind"`) {
		t.Fatalf("version 1 entry was not safely migrated:\n%s", raw)
	}
	got, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	e, ok = got.TopicFor(domain.Key{PaneID: "p1", TerminalID: "t1"})
	if !ok || got.Version != domain.MappingVersion || got.Dashboard != 777 || e.ThreadID != 42 || e.Status != domain.StatusExited || !e.Closed || !e.Muted || e.Cwd != "" || e.AgentKind != "" || !e.LegacyNoCwd {
		t.Fatalf("migrated version 2 load lost legacy state: version=%d dashboard=%d entry=%+v ok=%v", got.Version, got.Dashboard, e, ok)
	}
	live := domain.Agent{Key: domain.Key{PaneID: "p1", TerminalID: "t2"}, Name: "reviewer", Kind: "codex", Cwd: "/work/repo", Status: domain.StatusWorking}
	plan := got.PlanReassociation([]domain.Agent{live})
	if len(plan.Assignments) != 1 || plan.Assignments[0].Reason != domain.ReassociationFallback {
		t.Fatalf("migrated v1 entry lost guarded fallback: %+v", plan)
	}
	if err := got.ApplyReassociation(plan); err != nil {
		t.Fatal(err)
	}
	if !got.UpdateMetadata(live.Key, live) {
		t.Fatal("adopted entry did not learn cwd")
	}
	adopted, _ := got.TopicFor(live.Key)
	if adopted.LegacyNoCwd || adopted.Cwd != live.Cwd {
		t.Fatalf("legacy allowance remained after cwd was learned: %+v", adopted)
	}
	if err := s.Save(ctx, got); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), `"legacy_no_cwd"`) {
		t.Fatalf("legacy allowance persisted after adoption:\n%s", stored)
	}
}

func TestMappingStoreDashboardID(t *testing.T) {
	dir := t.TempDir()
	s := state.NewMappingStore(dir, nil)
	ctx := context.Background()

	// A file written before the dashboard existed loads with no id and is
	// saved back without the key.
	old := `{"version": 1, "chat_id": -1001, "topics": {}}` + "\n"
	if err := os.WriteFile(s.Path(), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := s.Load(ctx)
	if err != nil || m.Dashboard != 0 || m.ChatID != -1001 {
		t.Fatalf("Load old file = %+v, %v", m, err)
	}
	if err := s.Save(ctx, m); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(s.Path())
	if strings.Contains(string(raw), "dashboard_message_id") {
		t.Fatalf("zero id must be omitted:\n%s", raw)
	}

	m.Dashboard = 4242
	if err := s.Save(ctx, m); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(s.Path())
	if !strings.Contains(string(raw), `"dashboard_message_id": 4242`) {
		t.Fatalf("file lacks the dashboard id:\n%s", raw)
	}
	got, err := s.Load(ctx)
	if err != nil || got.Dashboard != 4242 {
		t.Fatalf("Load = dashboard %d, %v", got.Dashboard, err)
	}
}

func TestMappingStoreMovesMalformedAside(t *testing.T) {
	dir := t.TempDir()
	var logs bytes.Buffer
	s := state.NewMappingStore(dir, slog.New(slog.NewTextHandler(&logs, nil)))
	if err := os.WriteFile(s.Path(), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := s.Load(context.Background())
	if err != nil || len(m.Topics) != 0 {
		t.Fatalf("Load malformed = %+v, %v", m, err)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "mapping file malformed, moved aside and starting empty") {
		t.Fatalf("malformed mapping warning missing: %s", logs.String())
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "mapping.json.broken-*"))
	if len(matches) != 1 {
		t.Fatalf("backup files = %v", matches)
	}
	if _, err := os.Stat(s.Path()); !os.IsNotExist(err) {
		t.Fatalf("malformed file still present: %v", err)
	}
	broken, err := s.BrokenFiles()
	if err != nil || len(broken) != 1 || !strings.HasPrefix(broken[0], "mapping.json.broken-") {
		t.Fatalf("BrokenFiles = %v, %v", broken, err)
	}
}

func TestMappingStoreBrokenFilesEmpty(t *testing.T) {
	s := state.NewMappingStore(t.TempDir(), nil)
	broken, err := s.BrokenFiles()
	if err != nil || len(broken) != 0 {
		t.Fatalf("BrokenFiles = %v, %v", broken, err)
	}
}
