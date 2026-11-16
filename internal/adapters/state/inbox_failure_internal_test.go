package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInboxStageFailurePreservesOldFile(t *testing.T) {
	in := NewInbox(t.TempDir(), nil)
	in.MaxTotal = func() int64 { return 2 }
	old, err := in.Save(context.Background(), "old.bin", []byte("ok"))
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("injected staging failure")
	in.stageForSave = func(string, []byte, os.FileMode) (string, error) {
		return "", wantErr
	}
	if _, err := in.Save(context.Background(), "new.bin", []byte("x")); !errors.Is(err, wantErr) {
		t.Fatalf("Save error = %v, want staging error", err)
	}
	if data, err := os.ReadFile(old); err != nil || string(data) != "ok" {
		t.Fatalf("old file after staging failure = %q, %v", data, err)
	}
}

func TestInboxFinalizationFailureRestoresOldFile(t *testing.T) {
	in := NewInbox(t.TempDir(), nil)
	in.MaxTotal = func() int64 { return 2 }
	old, err := in.Save(context.Background(), "old.bin", []byte("ok"))
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("injected finalization failure")
	var calls int
	in.renameForSave = func(from, to string) error {
		calls++
		if calls == 2 {
			return wantErr
		}
		return os.Rename(from, to)
	}
	if _, err := in.Save(context.Background(), "new.bin", []byte("x")); !errors.Is(err, wantErr) {
		t.Fatalf("Save error = %v, want finalization error", err)
	}
	assertInboxFile(t, old, "ok")
	if _, err := os.Stat(filepath.Join(in.Dir(), "new.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new file after failed finalization: %v", err)
	}
}

func TestInboxPartialQuotaCleanupRestoresAllOldFiles(t *testing.T) {
	in := NewInbox(t.TempDir(), nil)
	in.MaxTotal = func() int64 { return 4 }
	old, err := in.Save(context.Background(), "old.bin", []byte("aa"))
	if err != nil {
		t.Fatal(err)
	}
	newer, err := in.Save(context.Background(), "newer.bin", []byte("bb"))
	if err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-2 * time.Hour)
	newerTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newer, newerTime, newerTime); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("injected partial quota cleanup failure")
	var calls int
	in.renameForSave = func(from, to string) error {
		calls++
		if calls == 2 {
			return wantErr
		}
		return os.Rename(from, to)
	}
	if _, err := in.Save(context.Background(), "replacement.bin", []byte("new")); !errors.Is(err, wantErr) {
		t.Fatalf("Save error = %v, want partial cleanup error", err)
	}
	assertInboxFile(t, old, "aa")
	assertInboxFile(t, newer, "bb")
	entries, err := os.ReadDir(in.Dir())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if len(entry.Name()) >= len(".inbox-quarantine-") && entry.Name()[:len(".inbox-quarantine-")] == ".inbox-quarantine-" {
			t.Fatalf("quarantined old file not restored: %s", entry.Name())
		}
	}
}

func TestInboxRestoresQuarantinedFileAfterRestart(t *testing.T) {
	stateDir := t.TempDir()
	in := NewInbox(stateDir, nil)
	old, err := in.Save(context.Background(), "old.bin", []byte("attachment"))
	if err != nil {
		t.Fatal(err)
	}
	quarantine := filepath.Join(in.Dir(), ".inbox-quarantine-old.bin")
	if err := os.Rename(old, quarantine); err != nil {
		t.Fatal(err)
	}

	restarted := NewInbox(stateDir, nil)
	assertInboxFile(t, filepath.Join(restarted.Dir(), "old.bin"), "attachment")
	if _, err := os.Stat(quarantine); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("quarantine path after restart: %v", err)
	}
}

func assertInboxFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want {
		t.Fatalf("file %s = %q, %v; want %q", path, data, err, want)
	}
}
