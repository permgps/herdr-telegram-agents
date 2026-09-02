package testkit

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// FakeProcess is an in-memory domain.ProcessControl and domain.PidFile.
// Spawned processes are alive until stopped or killed; by default a spawned
// daemon also acquires the pid file, as the real one does on start.
type FakeProcess struct {
	mu          sync.Mutex
	alive       map[int]bool
	nextPID     int
	spawned     [][]string
	signals     []string
	unsupported bool
	ignoreStop  bool
	spawnFails  error
	held        int
	heldSince   time.Time
	now         func() time.Time

	// SpawnAcquires makes Spawn take the pid file for the new process.
	SpawnAcquires bool
}

var (
	_ domain.ProcessControl = (*FakeProcess)(nil)
	_ domain.PidFile        = (*FakeProcess)(nil)
)

// NewFakeProcess returns a fake with no live processes.
func NewFakeProcess(now func() time.Time) *FakeProcess {
	if now == nil {
		now = time.Now
	}
	return &FakeProcess{alive: map[int]bool{}, nextPID: 1000, now: now, SpawnAcquires: true}
}

// SetUnsupported makes Stop and Resync return ErrUnsupportedPlatform.
func (p *FakeProcess) SetUnsupported(v bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.unsupported = v
}

// IgnoreStop makes Stop deliver the signal without ending the process, so
// a supervisor has to escalate to Kill.
func (p *FakeProcess) IgnoreStop(v bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ignoreStop = v
}

// FailSpawn makes Spawn return err.
func (p *FakeProcess) FailSpawn(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.spawnFails = err
}

// SetAlive marks a pid as running or not.
func (p *FakeProcess) SetAlive(pid int, alive bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.alive[pid] = alive
}

// Spawned returns the argument lists of every spawn, in order.
func (p *FakeProcess) Spawned() [][]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]string(nil), p.spawned...)
}

// Signals returns every signal as "<kind>:<pid>", in order.
func (p *FakeProcess) Signals() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.signals...)
}

// Held returns the pid recorded in the fake pid file, 0 when none.
func (p *FakeProcess) Held() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.held
}

func (p *FakeProcess) Spawn(_ context.Context, args []string) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.spawnFails != nil {
		return 0, p.spawnFails
	}
	p.nextPID++
	pid := p.nextPID
	p.alive[pid] = true
	p.spawned = append(p.spawned, append([]string(nil), args...))
	if p.SpawnAcquires && p.held == 0 {
		p.held, p.heldSince = pid, p.now()
	}
	return pid, nil
}

func (p *FakeProcess) Alive(pid int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.alive[pid]
}

func (p *FakeProcess) Stop(pid int) error {
	return p.signal("stop", pid, !p.ignoreStopLocked())
}

func (p *FakeProcess) Resync(pid int) error {
	return p.signal("resync", pid, false)
}

func (p *FakeProcess) Kill(pid int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.signals = append(p.signals, fmt.Sprintf("kill:%d", pid))
	if !p.alive[pid] {
		return fmt.Errorf("kill %d: %w", pid, domain.ErrNotRunning)
	}
	p.alive[pid] = false
	if p.held == pid {
		p.held = 0
	}
	return nil
}

func (p *FakeProcess) ignoreStopLocked() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ignoreStop
}

func (p *FakeProcess) signal(kind string, pid int, ends bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.signals = append(p.signals, fmt.Sprintf("%s:%d", kind, pid))
	if p.unsupported {
		return fmt.Errorf("%s: %w", kind, domain.ErrUnsupportedPlatform)
	}
	if !p.alive[pid] {
		return fmt.Errorf("%s %d: %w", kind, pid, domain.ErrNotRunning)
	}
	if ends {
		p.alive[pid] = false
		if p.held == pid {
			p.held = 0
		}
	}
	return nil
}

func (p *FakeProcess) Acquire(pid int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.held != 0 {
		return fmt.Errorf("%w: pid %d", domain.ErrAlreadyRunning, p.held)
	}
	p.held, p.heldSince = pid, p.now()
	return nil
}

func (p *FakeProcess) Read() (domain.PidInfo, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.held == 0 {
		return domain.PidInfo{}, domain.ErrNotRunning
	}
	return domain.PidInfo{PID: p.held, Since: p.heldSince}, nil
}

func (p *FakeProcess) Release() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.held = 0
	return nil
}

// String renders the fake's state for test failure messages.
func (p *FakeProcess) String() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return fmt.Sprintf("held=%d signals=[%s] spawned=%d", p.held, strings.Join(p.signals, " "), len(p.spawned))
}
