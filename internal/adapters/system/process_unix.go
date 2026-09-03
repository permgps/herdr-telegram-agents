//go:build !windows

package system

import (
	"errors"
	"fmt"
	"log/slog"
	"syscall"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// Alive reports whether pid exists. EPERM means the process exists but
// belongs to another user, which still counts as alive.
func (p *Process) Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Stop asks the daemon through its control channel and falls back to
// SIGTERM, which is all a daemon from an older build understands.
func (p *Process) Stop(pid int) error {
	if _, err := p.control(ControlStop); err == nil {
		return nil
	} else if !errors.Is(err, domain.ErrControlUnavailable) {
		return err
	}
	p.log.Debug("control unavailable, sending SIGTERM", slog.Int("pid", pid))
	return p.signal(pid, syscall.SIGTERM, "stop")
}

// Resync asks the daemon through its control channel and falls back to
// SIGHUP; see Stop.
func (p *Process) Resync(pid int) error {
	if _, err := p.control(ControlResync); err == nil {
		return nil
	} else if !errors.Is(err, domain.ErrControlUnavailable) {
		return err
	}
	p.log.Debug("control unavailable, sending SIGHUP", slog.Int("pid", pid))
	return p.signal(pid, syscall.SIGHUP, "resync")
}

// Kill sends SIGKILL.
func (p *Process) Kill(pid int) error { return p.signal(pid, syscall.SIGKILL, "kill") }

func (p *Process) signal(pid int, sig syscall.Signal, name string) error {
	if pid <= 0 {
		return fmt.Errorf("%s: %w", name, domain.ErrNotRunning)
	}
	err := syscall.Kill(pid, sig)
	p.log.Debug("signal sent", slog.String("signal", name), slog.Int("pid", pid), slog.Any("err", err))
	if errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("%s %d: %w", name, pid, domain.ErrNotRunning)
	}
	if err != nil {
		return fmt.Errorf("%s %d: %w", name, pid, err)
	}
	return nil
}
