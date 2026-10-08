// Package logging builds the daemon's slog logger: JSON lines into a
// size-rotated file under the plugin state directory.
package logging

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// rotateRetry spaces the attempts after a failed rotation (or a failed
// reopen of the live file), so an obstacle such as a directory where a
// backup belongs costs one attempt and one stderr line per minute, not one
// per record.
const rotateRetry = time.Minute

// RotatingWriter appends to a file and rotates it once it grows past size:
// path -> path.1 -> path.2 ... up to keep backups, the oldest deleted. The
// current size is tracked with a counter primed from the file on open, so
// no stat call happens per write. A failed rotation keeps writing to the
// live file and is retried after rotateRetry.
type RotatingWriter struct {
	path string
	size int64
	keep int

	// now and errOut are the clock and the sink for rotation failures; the
	// writer cannot log to itself, and the daemon's spawn redirects its
	// stderr to a log of its own. Tests replace both.
	now    func() time.Time
	errOut io.Writer

	mu      sync.Mutex
	file    *os.File
	n       int64
	closed  bool
	retryAt time.Time
}

// NewRotatingWriter opens (or creates, mode 0600) the file at path.
func NewRotatingWriter(path string, size int64, keep int) (*RotatingWriter, error) {
	w := &RotatingWriter{path: path, size: size, keep: keep, now: time.Now, errOut: os.Stderr}
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
// single record larger than the limit is still written whole. A failed
// rotation is reported once to errOut, the record still goes to the live
// file, and no rotation is tried again before rotateRetry has passed. Only
// Close makes writes fail for good.
func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, fmt.Errorf("write log %s: closed", w.path)
	}
	now := w.now()
	if w.file == nil {
		// A failed rotation could not even reopen the live file.
		if now.Before(w.retryAt) {
			return 0, fmt.Errorf("write log %s: not open, retry pending", w.path)
		}
		if err := w.open(); err != nil {
			w.failed(now, err)
			return 0, err
		}
	}
	if w.n > 0 && w.n+int64(len(p)) > w.size && !now.Before(w.retryAt) {
		if err := w.rotate(); err != nil {
			w.failed(now, err)
			if w.file == nil {
				return 0, err
			}
		}
	}
	n, err := w.file.Write(p)
	w.n += int64(n)
	return n, err
}

// failed schedules the next attempt and reports err on one line.
func (w *RotatingWriter) failed(now time.Time, err error) {
	w.retryAt = now.Add(rotateRetry)
	_, _ = fmt.Fprintln(w.errOut, err.Error())
}

// rotate shifts the backups up by one, renames the live file to .1 and
// reopens an empty live file. When a step fails the live file is reopened
// in append mode, so writing goes on with the counter primed from its
// size; if even that fails, file stays nil until the next attempt.
func (w *RotatingWriter) rotate() error {
	err := w.file.Close()
	w.file = nil
	if err != nil {
		err = fmt.Errorf("close log %s: %w", w.path, err)
	} else {
		err = w.shift()
	}
	if err != nil {
		if oerr := w.open(); oerr != nil {
			return fmt.Errorf("%w; %w", err, oerr)
		}
		return err
	}
	return w.open()
}

// shift renames the backups and the live file one place up.
func (w *RotatingWriter) shift() error {
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
	return nil
}

func (w *RotatingWriter) backupName(i int) string {
	return fmt.Sprintf("%s.%d", w.path, i)
}

// Close closes the live file. Further writes fail.
func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}
