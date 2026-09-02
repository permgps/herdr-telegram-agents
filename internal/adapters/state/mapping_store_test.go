package state_test

import (
	"context"
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
	a := domain.Agent{Key: domain.Key{PaneID: "p1", TerminalID: "t1"}, Name: "reviewer", Status: domain.StatusWorking}
	at := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	m.Link(a.Key, domain.Topic{ThreadID: 42}, a, at)
	m.MarkExited(a.Key, at.Add(time.Minute))
	if err := s.Save(ctx, m); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := got.TopicFor(a.Key)
	if !ok || got.ChatID != -1001 || got.Version != domain.MappingVersion ||
		e.ThreadID != 42 || e.Name != "🏁 reviewer" || e.Status != domain.StatusExited || e.Closed || !e.UpdatedAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("round trip: chat=%d version=%d entry=%+v ok=%v", got.ChatID, got.Version, e, ok)
	}
	raw, _ := os.ReadFile(s.Path())
	for _, key := range []string{`"p1/t1"`, `"thread_id": 42`, `"status": "exited"`, `"updated_at"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("file lacks %s:\n%s", key, raw)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

func TestMappingStoreMovesMalformedAside(t *testing.T) {
	dir := t.TempDir()
	s := state.NewMappingStore(dir, nil)
	if err := os.WriteFile(s.Path(), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := s.Load(context.Background())
	if err != nil || len(m.Topics) != 0 {
		t.Fatalf("Load malformed = %+v, %v", m, err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "mapping.json.broken-*"))
	if len(matches) != 1 {
		t.Fatalf("backup files = %v", matches)
	}
	if _, err := os.Stat(s.Path()); !os.IsNotExist(err) {
		t.Fatalf("malformed file still present: %v", err)
	}
}
