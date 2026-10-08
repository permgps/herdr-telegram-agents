package state_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/adapters/state"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func TestInboxSaveCreatesDirAndFile(t *testing.T) {
	dir := t.TempDir()
	in := state.NewInbox(dir, nil)
	ctx := context.Background()
	path, err := in.Save(ctx, "20260906-120000-42-photo.jpg", []byte("jpeg"))
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(path) || filepath.Dir(path) != filepath.Join(dir, state.InboxDirName) {
		t.Fatalf("path = %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "jpeg" {
		t.Fatalf("content = %q, %v", data, err)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
			t.Fatalf("file mode = %o", info.Mode().Perm())
		}
		if info, _ := os.Stat(in.Dir()); info.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode = %o", info.Mode().Perm())
		}
	}
	if entries, _ := os.ReadDir(in.Dir()); len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

func TestInboxSaveUniqueNames(t *testing.T) {
	in := state.NewInbox(t.TempDir(), nil)
	ctx := context.Background()
	first, _ := in.Save(ctx, "a.txt", []byte("1"))
	second, err := in.Save(ctx, "a.txt", []byte("2"))
	if err != nil {
		t.Fatal(err)
	}
	third, _ := in.Save(ctx, "a.txt", []byte("3"))
	if filepath.Base(first) != "a.txt" || filepath.Base(second) != "a-2.txt" || filepath.Base(third) != "a-3.txt" {
		t.Fatalf("names = %q %q %q", first, second, third)
	}
	if data, _ := os.ReadFile(first); string(data) != "1" {
		t.Fatalf("first file overwritten: %q", data)
	}
}

func TestInboxSaveRefusesUnsafeName(t *testing.T) {
	in := state.NewInbox(t.TempDir(), nil)
	for _, name := range []string{"", "..", "../x", "a/b", `a\b`, "/etc/passwd"} {
		if _, err := in.Save(context.Background(), name, []byte("x")); err == nil {
			t.Errorf("Save(%q) accepted", name)
		}
	}
}

func TestInboxSweepByAge(t *testing.T) {
	dir := t.TempDir()
	in := state.NewInbox(dir, nil)
	ctx := context.Background()
	old1, _ := in.Save(ctx, "old1.txt", []byte("x"))
	old2, _ := in.Save(ctx, "old2.txt", []byte("x"))
	fresh, _ := in.Save(ctx, "fresh.txt", []byte("x"))
	past := time.Now().Add(-10 * 24 * time.Hour)
	for _, p := range []string{old1, old2} {
		if err := os.Chtimes(p, past, past); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(in.Dir(), "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	n, err := in.Sweep(ctx, 7*24*time.Hour)
	if err != nil || n != 2 {
		t.Fatalf("Sweep = %d, %v", n, err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("fresh file deleted")
	}
	if _, err := os.Stat(old1); !os.IsNotExist(err) {
		t.Fatal("old file kept")
	}
	if _, err := os.Stat(filepath.Join(in.Dir(), "sub")); err != nil {
		t.Fatal("subdirectory removed")
	}
}

func TestInboxSweepEmptyDir(t *testing.T) {
	in := state.NewInbox(t.TempDir(), nil)
	n, err := in.Sweep(context.Background(), time.Hour)
	if err != nil || n != 0 {
		t.Fatalf("Sweep on a missing inbox = %d, %v", n, err)
	}
}

// TestInboxTotalQuota: attachments stay within a total size. When a new
// file would go over, the oldest files go first; a file bigger than the
// whole quota is refused.
func TestInboxTotalQuota(t *testing.T) {
	ctx := context.Background()
	in := state.NewInbox(t.TempDir(), nil)
	in.MaxTotal = func() int64 { return 25 }
	old, err := in.Save(ctx, "old.bin", make([]byte, 10))
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	mid, err := in.Save(ctx, "mid.bin", make([]byte, 10))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.Save(ctx, "new.bin", make([]byte, 10)); err != nil {
		t.Fatalf("save over quota: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("oldest file kept: %v", err)
	}
	if _, err := os.Stat(mid); err != nil {
		t.Fatalf("newer file removed: %v", err)
	}
	if _, err := in.Save(ctx, "huge.bin", make([]byte, 26)); !errors.Is(err, domain.ErrFileTooBig) {
		t.Fatalf("file over the whole quota = %v", err)
	}
}

// saveAged saves a file through save and backdates it by age.
func saveAged(t *testing.T, save func(context.Context, string, []byte) (string, error), name string, size int, age time.Duration) string {
	t.Helper()
	path, err := save(context.Background(), name, make([]byte, size))
	if err != nil {
		t.Fatalf("save %s: %v", name, err)
	}
	past := time.Now().Add(-age)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	return path
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// TestInboxSharedQuota: shared files hold at most a quarter of the total
// quota and make room only from each other; the owner's file, older than
// every shared one, survives.
func TestInboxSharedQuota(t *testing.T) {
	in := state.NewInbox(t.TempDir(), nil)
	in.MaxTotal = func() int64 { return 40 }
	owner := saveAged(t, in.Save, "owner.bin", 10, 3*time.Hour)
	s1 := saveAged(t, in.SaveShared, "10-a-one.bin", 4, 2*time.Hour)
	s2 := saveAged(t, in.SaveShared, "10-b-two.bin", 4, time.Hour)
	if !strings.HasPrefix(filepath.Base(s1), "shared-") {
		t.Fatalf("shared file name = %q", filepath.Base(s1))
	}
	s3, err := in.SaveShared(context.Background(), "10-c-three.bin", make([]byte, 4))
	if err != nil {
		t.Fatalf("shared save over the shared quota: %v", err)
	}
	if exists(s1) {
		t.Fatal("oldest shared file kept over the shared quota")
	}
	if !exists(owner) || !exists(s2) || !exists(s3) {
		t.Fatal("shared save deleted more than the oldest shared file")
	}
}

// TestInboxSharedSaveNeverEvictsOwner: when only the owner's files could
// make room, a shared save is refused and nothing is deleted.
func TestInboxSharedSaveNeverEvictsOwner(t *testing.T) {
	in := state.NewInbox(t.TempDir(), nil)
	in.MaxTotal = func() int64 { return 40 }
	owner := saveAged(t, in.Save, "owner.bin", 35, time.Hour)
	if _, err := in.SaveShared(context.Background(), "10-a-one.bin", make([]byte, 8)); !errors.Is(err, domain.ErrFileTooBig) {
		t.Fatalf("shared save needing owner room = %v", err)
	}
	if !exists(owner) {
		t.Fatal("owner file deleted for a shared save")
	}
}

// TestInboxOwnerSaveEvictsShared: the owner's Save still frees room from
// the oldest file, shared or not.
func TestInboxOwnerSaveEvictsShared(t *testing.T) {
	in := state.NewInbox(t.TempDir(), nil)
	in.MaxTotal = func() int64 { return 40 }
	shared := saveAged(t, in.SaveShared, "10-a-one.bin", 8, 2*time.Hour)
	owner := saveAged(t, in.Save, "owner.bin", 20, time.Hour)
	if _, err := in.Save(context.Background(), "new.bin", make([]byte, 15)); err != nil {
		t.Fatal(err)
	}
	if exists(shared) || !exists(owner) {
		t.Fatalf("owner save evicted the wrong file: shared=%v owner=%v", exists(shared), exists(owner))
	}
}

// TestInboxSharedFileOverSubQuota: a shared file larger than a quarter of
// the total quota is refused, as is an unsafe name.
func TestInboxSharedFileOverSubQuota(t *testing.T) {
	in := state.NewInbox(t.TempDir(), nil)
	in.MaxTotal = func() int64 { return 40 }
	if _, err := in.SaveShared(context.Background(), "10-a-big.bin", make([]byte, 11)); !errors.Is(err, domain.ErrFileTooBig) {
		t.Fatalf("shared file over the sub-quota = %v", err)
	}
	for _, name := range []string{"", "..", "../escape", "a/b"} {
		if _, err := in.SaveShared(context.Background(), name, []byte("x")); err == nil {
			t.Errorf("SaveShared(%q) accepted", name)
		}
	}
}

// TestInboxSharedQuotaValue: the share PrivateControl checks before a download
// is the one SaveShared enforces: a quarter of the total, read live from
// the option, 125 MiB with the default 500 MiB.
func TestInboxSharedQuotaValue(t *testing.T) {
	in := state.NewInbox(t.TempDir(), nil)
	if got := in.SharedQuota(); got != 125<<20 {
		t.Fatalf("default SharedQuota = %d, want %d", got, 125<<20)
	}
	total := int64(40)
	in.MaxTotal = func() int64 { return total }
	if got := in.SharedQuota(); got != 10 {
		t.Fatalf("SharedQuota = %d, want 10", got)
	}
	if _, err := in.SaveShared(context.Background(), "10-a-fits.bin", make([]byte, in.SharedQuota())); err != nil {
		t.Fatalf("file of exactly SharedQuota refused: %v", err)
	}
	total = 200
	if got := in.SharedQuota(); got != 50 {
		t.Fatalf("SharedQuota after the option changed = %d, want 50", got)
	}
}
