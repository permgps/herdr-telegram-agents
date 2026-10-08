package transcript

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// openTestRoot makes a directory with a regular file "file.jsonl" and
// returns it opened as a root, with its path.
func openTestRoot(t *testing.T) (*os.Root, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.jsonl"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root, dir
}

func TestOpenRegularOpensAFile(t *testing.T) {
	root, _ := openTestRoot(t)
	f, info, err := openRegular(root, "file.jsonl")
	if err != nil {
		t.Fatalf("openRegular: %v", err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil || string(data) != "hello\n" || info.Size() != 6 || !info.Mode().IsRegular() {
		t.Fatalf("read %q, %v; info size %d mode %v", data, err, info.Size(), info.Mode())
	}
}

func TestOpenRegularMissingFile(t *testing.T) {
	root, _ := openTestRoot(t)
	if f, _, err := openRegular(root, "absent.jsonl"); f != nil || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file = %v, %v", f, err)
	}
}

func TestOpenRegularRefusesALink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	root, dir := openTestRoot(t)
	if err := os.Symlink(filepath.Join(dir, "file.jsonl"), filepath.Join(dir, "link.jsonl")); err != nil {
		t.Fatal(err)
	}
	f, _, err := openRegular(root, "link.jsonl")
	if f != nil || !errors.Is(err, errNotRegular) {
		t.Fatalf("link = %v, %v", f, err)
	}
	if strings.Contains(err.Error(), dir) || strings.Contains(err.Error(), "link.jsonl") {
		t.Fatalf("error leaks the path: %v", err)
	}
}

// TestOpenRegularSeenRefusesAnotherFile: a file that is not the one the
// earlier Lstat saw is refused.
func TestOpenRegularSeenRefusesAnotherFile(t *testing.T) {
	root, dir := openTestRoot(t)
	if err := os.WriteFile(filepath.Join(dir, "other.jsonl"), []byte("other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seen, err := root.Lstat("file.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if f, _, err := openRegularSeen(root, "other.jsonl", seen); f != nil || !errors.Is(err, errFileChanged) {
		t.Fatalf("other file = %v, %v", f, err)
	}
}

func TestOpenFailureWording(t *testing.T) {
	if err := openFailure("the pi session", fs.ErrNotExist); err != fs.ErrNotExist {
		t.Fatalf("missing = %v, want fs.ErrNotExist", err)
	}
	err := openFailure("the pi session", errNotRegular)
	if !errors.Is(err, domain.ErrNoReply) || err.Error() != domain.ErrNoReply.Error()+": the pi session is not a regular file" {
		t.Fatalf("not regular = %v", err)
	}
}
