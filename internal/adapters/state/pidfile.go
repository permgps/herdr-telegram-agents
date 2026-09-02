package state

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// PidFileName is the daemon's single-instance lock under the state dir.
const PidFileName = "daemon.pid"

// PidFile implements domain.PidFile over STATE_DIR/daemon.pid. The file is
// created with O_EXCL; when it already exists the recorded pid is checked
// with alive and a stale file is removed and the acquire retried once.
type PidFile struct {
	path  string
	alive func(int) bool
	log   *slog.Logger

	mu    sync.Mutex
	owned int
}

var _ domain.PidFile = (*PidFile)(nil)

// NewPidFile returns a pid file inside dir. alive decides whether a pid found
// in an existing file still runs; nil treats every existing file as live.
func NewPidFile(dir string, alive func(int) bool, log *slog.Logger) *PidFile {
	if alive == nil {
		alive = func(int) bool { return true }
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &PidFile{path: filepath.Join(dir, PidFileName), alive: alive, log: log}
}

// Path returns the pid file location.
func (p *PidFile) Path() string { return p.path }

// Acquire records pid. It returns ErrAlreadyRunning (wrapping the live pid)
// when another daemon owns the file.
func (p *PidFile) Acquire(pid int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(p.path), 0o755); err != nil {
		return fmt.Errorf("mkdir for pid file: %w", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		err := p.create(pid)
		if err == nil {
			p.owned = pid
			p.log.Debug("pid file acquired", slog.String("path", p.path), slog.Int("pid", pid))
			return nil
		}
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create pid file: %w", err)
		}
		info, readErr := p.read()
		if readErr != nil {
			// Unreadable or empty: treat as stale and replace it.
			p.log.Warn("pid file unreadable, removing", slog.String("path", p.path), slog.String("err", readErr.Error()))
		} else if p.alive(info.PID) {
			return fmt.Errorf("%w: pid %d", domain.ErrAlreadyRunning, info.PID)
		} else {
			p.log.Warn("stale pid file removed", slog.String("path", p.path), slog.Int("pid", info.PID))
		}
		if err := os.Remove(p.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale pid file: %w", err)
		}
	}
	return fmt.Errorf("%w: pid file keeps reappearing", domain.ErrAlreadyRunning)
}

func (p *PidFile) create(pid int) error {
	f, err := os.OpenFile(p.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(f, "%d\n", pid); err != nil {
		_ = f.Close()
		_ = os.Remove(p.path)
		return err
	}
	return f.Close()
}

// Read returns the recorded pid and the file's modification time, or
// ErrNotRunning when there is no file.
func (p *PidFile) Read() (domain.PidInfo, error) {
	info, err := p.read()
	if errors.Is(err, os.ErrNotExist) {
		return domain.PidInfo{}, domain.ErrNotRunning
	}
	return info, err
}

func (p *PidFile) read() (domain.PidInfo, error) {
	data, err := os.ReadFile(p.path)
	if err != nil {
		return domain.PidInfo{}, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return domain.PidInfo{}, fmt.Errorf("pid file %s: bad content %q", p.path, strings.TrimSpace(string(data)))
	}
	st, err := os.Stat(p.path)
	if err != nil {
		return domain.PidInfo{}, err
	}
	return domain.PidInfo{PID: pid, Since: st.ModTime()}, nil
}

// Release removes the file only when it still holds the pid this process
// acquired, so a newer daemon's file is never deleted by an old one.
func (p *PidFile) Release() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.owned == 0 {
		return nil
	}
	info, err := p.read()
	if errors.Is(err, os.ErrNotExist) {
		p.owned = 0
		return nil
	}
	if err == nil && info.PID != p.owned {
		p.log.Warn("pid file owned by another process, not removed",
			slog.String("path", p.path), slog.Int("pid", info.PID), slog.Int("own", p.owned))
		p.owned = 0
		return nil
	}
	if err := os.Remove(p.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove pid file: %w", err)
	}
	p.log.Debug("pid file released", slog.String("path", p.path), slog.Int("pid", p.owned))
	p.owned = 0
	return nil
}
