package state_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/adapters/state"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func TestOptionsStoreMissingFileIsDefaults(t *testing.T) {
	s := state.NewOptionsStore(t.TempDir(), nil)
	got, err := s.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.SyncEnabled() || got.StatusIcons() != domain.DefaultStatusIcons() {
		t.Fatalf("defaults not returned: %+v", got.Values())
	}
}

func TestOptionsStoreRoundTripKeepsUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	s := state.NewOptionsStore(dir, nil)
	ctx := context.Background()
	seed := `{"version": 1, "values": {"sync.enabled": false, "icons.working": "🔥", "future.thing": {"a": 1}}}`
	if err := os.WriteFile(s.Path(), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.SyncEnabled() || got.StatusIcons().Working != "🔥" || got.StatusIcons().Idle != "✅" {
		t.Fatalf("loaded = %v", got.Values())
	}
	next, _ := got.With(domain.OptionSyncEnabled, "true")
	if err := s.Save(ctx, next); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(s.Path())
	var f struct {
		Version int                        `json:"version"`
		Values  map[string]json.RawMessage `json:"values"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("saved file does not parse: %v\n%s", err, raw)
	}
	if f.Version != 1 || string(f.Values["sync.enabled"]) != "true" || string(f.Values["icons.working"]) != `"🔥"` {
		t.Errorf("saved values = %s", raw)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, f.Values["future.thing"]); err != nil || compact.String() != `{"a":1}` {
		t.Errorf("unknown key lost: %s (%v)", f.Values["future.thing"], err)
	}
	if len(f.Values) != len(domain.OptionSpecs)+1 {
		t.Errorf("saved %d keys, want %d", len(f.Values), len(domain.OptionSpecs)+1)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(s.Path())
		if info.Mode().Perm() != 0o600 {
			t.Errorf("mode = %04o, want 0600", info.Mode().Perm())
		}
	}
	again, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !again.SyncEnabled() || again.StatusIcons().Working != "🔥" {
		t.Errorf("second load = %v", again.Values())
	}
}

func TestOptionsStoreSkipsWrongTypesAndKeepsGoing(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s := state.NewOptionsStore(t.TempDir(), log)
	seed := `{"version": 1, "values": {"sync.enabled": "nope", "icons.idle": 7, "icons.done": "🧠"}}`
	if err := os.WriteFile(s.Path(), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.SyncEnabled() || got.StatusIcons().Idle != "✅" || got.StatusIcons().Done != "🧠" {
		t.Errorf("loaded = %v", got.Values())
	}
	if n := strings.Count(buf.String(), "option ignored"); n != 2 {
		t.Errorf("want 2 warnings, log:\n%s", buf.String())
	}
}

func TestOptionsStoreBoolAsString(t *testing.T) {
	s := state.NewOptionsStore(t.TempDir(), nil)
	seed := `{"version": 1, "values": {"sync.enabled": "false"}}`
	if err := os.WriteFile(s.Path(), []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.SyncEnabled() {
		t.Error("string false not honoured")
	}
}

func TestOptionsStoreCorruptFileIsAnError(t *testing.T) {
	s := state.NewOptionsStore(t.TempDir(), nil)
	if err := os.WriteFile(s.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(context.Background())
	if err == nil {
		t.Fatal("corrupt file loaded without error")
	}
	if !got.SyncEnabled() {
		t.Error("corrupt load should still hand back defaults")
	}
}
