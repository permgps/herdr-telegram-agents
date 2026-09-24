package system

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestUpdateLockContentionAndStaleOwner(t *testing.T) {
	dir := t.TempDir()
	first := NewUpdateLock(dir, func(int) bool { return true }, nil)
	if err := first.Acquire(); err != nil {
		t.Fatal(err)
	}
	second := NewUpdateLock(dir, func(int) bool { return true }, nil)
	if err := second.Acquire(); !errors.Is(err, ErrUpdateLocked) {
		t.Fatalf("contention=%v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "update.lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "update.lock", "owner.json"), []byte(`{"pid":999999,"nonce":"stale"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := NewUpdateLock(dir, func(int) bool { return false }, nil)
	if err := stale.Acquire(); err != nil {
		t.Fatalf("stale recovery=%v", err)
	}
	if err := stale.Release(); err != nil {
		t.Fatal(err)
	}
}
