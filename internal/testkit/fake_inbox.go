package testkit

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// SavedFile is one Save call recorded by FakeInbox.
type SavedFile struct {
	Name string
	Path string
	Data []byte
}

// FakeInbox is an in-memory domain.InboxStore: Save records the file and
// answers <dir>/<name> without touching the disk; Sweep answers a scripted
// count and records the age it was asked for.
type FakeInbox struct {
	mu       sync.Mutex
	dir      string
	saved    []SavedFile
	sweeps   []time.Duration
	sweepN   int
	failNext map[string]error
}

var _ domain.InboxStore = (*FakeInbox)(nil)

// NewFakeInbox returns a fake rooted at dir (any absolute-looking path).
func NewFakeInbox(dir string) *FakeInbox {
	return &FakeInbox{dir: dir, failNext: map[string]error{}}
}

// FailNext makes the next call of method (save, sweep) return err.
func (f *FakeInbox) FailNext(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext[method] = err
}

// SetSweepCount scripts what Sweep reports as deleted.
func (f *FakeInbox) SetSweepCount(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sweepN = n
}

// Saved returns every Save call, in order.
func (f *FakeInbox) Saved() []SavedFile {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]SavedFile(nil), f.saved...)
}

// Sweeps returns the ages Sweep was asked for, in order.
func (f *FakeInbox) Sweeps() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.sweeps...)
}

func (f *FakeInbox) take(method string) error {
	if err, ok := f.failNext[method]; ok {
		delete(f.failNext, method)
		return err
	}
	return nil
}

// Save implements domain.InboxStore.
func (f *FakeInbox) Save(_ context.Context, name string, data []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.take("save"); err != nil {
		return "", err
	}
	path := filepath.Join(f.dir, name)
	f.saved = append(f.saved, SavedFile{Name: name, Path: path, Data: append([]byte(nil), data...)})
	return path, nil
}

// Sweep implements domain.InboxStore.
func (f *FakeInbox) Sweep(_ context.Context, olderThan time.Duration) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sweeps = append(f.sweeps, olderThan)
	if err := f.take("sweep"); err != nil {
		return 0, err
	}
	return f.sweepN, nil
}
