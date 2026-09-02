package state_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/adapters/state"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func sampleConfig() domain.Config {
	return domain.Config{
		Version:      domain.ConfigVersion,
		BotToken:     "1:secret",
		BotUsername:  "bot",
		ChatID:       -1001,
		ChatTitle:    "Agents",
		OperatorIDs:  []int64{7, 8},
		LogLevel:     "debug",
		ConfiguredAt: time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC),
	}
}

func TestConfigStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := state.NewConfigStore(dir, nil)
	ctx := context.Background()

	if _, err := s.Load(ctx); !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("Load on missing = %v, want ErrNotConfigured", err)
	}
	want := sampleConfig()
	if err := s.Save(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.BotToken != want.BotToken || got.ChatID != want.ChatID || len(got.OperatorIDs) != 2 ||
		got.ChatTitle != want.ChatTitle || got.LogLevel != want.LogLevel || !got.ConfiguredAt.Equal(want.ConfiguredAt) {
		t.Fatalf("round trip: got %+v", got)
	}
	raw, _ := os.ReadFile(s.Path())
	for _, key := range []string{`"bot_token"`, `"chat_id"`, `"operator_ids"`, `"configured_at"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("file lacks %s:\n%s", key, raw)
		}
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(s.Path())
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %04o, want 0600", info.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

func TestConfigStoreRejectsInvalid(t *testing.T) {
	s := state.NewConfigStore(t.TempDir(), nil)
	bad := sampleConfig()
	bad.OperatorIDs = nil
	if err := s.Save(context.Background(), bad); !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("Save invalid = %v", err)
	}
	if err := os.WriteFile(s.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(context.Background()); !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("Load malformed = %v", err)
	}
	if err := os.WriteFile(s.Path(), []byte(`{"version":9,"bot_token":"t","chat_id":-1,"operator_ids":[1]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(context.Background()); !errors.Is(err, domain.ErrNotConfigured) || !strings.Contains(err.Error(), "version 9") {
		t.Fatalf("Load wrong version = %v", err)
	}
}

func TestConfigStoreWarnsOnLoosePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are meaningless on windows")
	}
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s := state.NewConfigStore(t.TempDir(), log)
	if err := s.Save(context.Background(), sampleConfig()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.Path(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "readable by others") {
		t.Fatalf("no permission warning in log:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "secret") {
		t.Fatalf("token leaked into log:\n%s", buf.String())
	}
}

func TestConfigStoreCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "cfg")
	s := state.NewConfigStore(dir, nil)
	if err := s.Save(context.Background(), sampleConfig()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Path()); err != nil {
		t.Fatal(err)
	}
}
