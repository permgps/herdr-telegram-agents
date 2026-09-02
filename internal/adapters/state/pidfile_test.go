package state_test

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/adapters/state"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func TestPidFileAcquireReadRelease(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	p := state.NewPidFile(dir, func(int) bool { return true }, nil)
	if _, err := p.Read(); !errors.Is(err, domain.ErrNotRunning) {
		t.Fatalf("Read before acquire = %v", err)
	}
	if err := p.Acquire(123); err != nil {
		t.Fatal(err)
	}
	info, err := p.Read()
	if err != nil || info.PID != 123 || info.Since.IsZero() {
		t.Fatalf("Read = %+v, %v", info, err)
	}
	if err := p.Acquire(124); !errors.Is(err, domain.ErrAlreadyRunning) || !strings.Contains(err.Error(), "123") {
		t.Fatalf("second Acquire = %v", err)
	}
	if err := p.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.Path()); !os.IsNotExist(err) {
		t.Fatalf("file still present after Release: %v", err)
	}
	if err := p.Release(); err != nil {
		t.Fatalf("second Release = %v", err)
	}
}

func TestPidFileReplacesStale(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	p := state.NewPidFile(dir, func(pid int) bool { return pid == 999 }, log)
	if err := os.WriteFile(p.Path(), []byte("555\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.Acquire(1); err != nil {
		t.Fatalf("Acquire over stale = %v", err)
	}
	if info, _ := p.Read(); info.PID != 1 {
		t.Fatalf("pid after replace = %d", info.PID)
	}
	if !strings.Contains(buf.String(), "stale pid file removed") {
		t.Fatalf("no stale warning:\n%s", buf.String())
	}
	_ = p.Release()

	if err := os.WriteFile(p.Path(), []byte("999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.Acquire(2); !errors.Is(err, domain.ErrAlreadyRunning) {
		t.Fatalf("Acquire over live = %v", err)
	}
}

func TestPidFileReleaseKeepsForeignFile(t *testing.T) {
	dir := t.TempDir()
	p := state.NewPidFile(dir, nil, nil)
	if err := p.Acquire(10); err != nil {
		t.Fatal(err)
	}
	// Another daemon took over the file meanwhile.
	if err := os.WriteFile(p.Path(), []byte("11\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.Release(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p.Path())
	if err != nil || strings.TrimSpace(string(data)) != "11" {
		t.Fatalf("foreign pid file removed: %q, %v", data, err)
	}
}

func TestPidFileUnreadableContentIsStale(t *testing.T) {
	dir := t.TempDir()
	p := state.NewPidFile(dir, func(int) bool { return true }, nil)
	if err := os.WriteFile(p.Path(), []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.Acquire(3); err != nil {
		t.Fatalf("Acquire over garbage = %v", err)
	}
}
