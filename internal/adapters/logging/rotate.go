// Package logging builds the daemon's slog logger: JSON lines into a
// size-rotated file under the plugin state directory.
package logging

import (
	"fmt"
	"os"
	"sync"
)

// RotatingWriter appends to a file and rotates it once it grows past size:
// path -> path.1 -> path.2 ... up to keep backups, the oldest deleted. The
// current size is tracked with a counter primed from the file on open, so
// no stat call happens per write.
type RotatingWriter struct {
	path string
	size int64
	keep int

	mu   sync.Mutex
	file *os.File
	n    int64
}

// NewRotatingWriter opens (or creates, mode 0600) the file at path.
func NewRotatingWriter(path string, size int64, keep int) (*RotatingWriter, error) {
	w := &RotatingWriter{path: path, size: size, keep: keep}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *RotatingWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open log %s: %w", w.path, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("stat log %s: %w", w.path, err)
	}
	w.file, w.n = f, info.Size()
	return nil
}

// Write appends p, rotating first when the file would exceed the limit. A
// single record larger than the limit is still written whole.
func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return 0, fmt.Errorf("write log %s: closed", w.path)
	}
	if w.n > 0 && w.n+int64(len(p)) > w.size {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(p)
	w.n += int64(n)
	return n, err
}

// rotate shifts the backups up by one, renames the live file to .1 and
// reopens an empty live file.
func (w *RotatingWriter) rotate() error {
	if err := w.file.Close(); err != nil {
		return fmt.Errorf("close log %s: %w", w.path, err)
	}
	w.file = nil
	for i := w.keep; i >= 1; i-- {
		from := w.backupName(i)
		if i == w.keep {
			if err := os.Remove(from); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove %s: %w", from, err)
			}
			continue
		}
		to := w.backupName(i + 1)
		if err := os.Rename(from, to); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("rename %s: %w", from, err)
		}
	}
	if w.keep >= 1 {
		if err := os.Rename(w.path, w.backupName(1)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("rename %s: %w", w.path, err)
		}
	} else if err := os.Remove(w.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", w.path, err)
	}
	return w.open()
}

func (w *RotatingWriter) backupName(i int) string {
	return fmt.Sprintf("%s.%d", w.path, i)
}

// Close closes the live file. Further writes fail.
func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}
