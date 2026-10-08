//go:build unix

package transcript

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// openWithin runs open and fails the test when it has not returned within
// two seconds: a blocking open of a FIFO waits for a writer forever.
func openWithin(t *testing.T, open func() (*os.File, error)) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		f, err := open()
		if f != nil {
			f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("open blocked on a FIFO")
		return nil
	}
}

// TestOpenRegularRefusesAFIFO: a FIFO is refused at once, both when the
// Lstat sees it and when it is swapped in after a regular file's Lstat.
func TestOpenRegularRefusesAFIFO(t *testing.T) {
	root, dir := openTestRoot(t)
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo.jsonl"), 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	err := openWithin(t, func() (*os.File, error) {
		f, _, err := openRegular(root, "fifo.jsonl")
		return f, err
	})
	if !errors.Is(err, errNotRegular) {
		t.Fatalf("FIFO = %v, want errNotRegular", err)
	}
	seen, err := root.Lstat("file.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	err = openWithin(t, func() (*os.File, error) {
		f, _, err := openRegularSeen(root, "fifo.jsonl", seen)
		return f, err
	})
	if !errors.Is(err, errNotRegular) {
		t.Fatalf("swapped-in FIFO = %v, want errNotRegular", err)
	}
}
