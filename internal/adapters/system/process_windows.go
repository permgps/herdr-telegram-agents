//go:build windows

package system

import (
	"fmt"
	"log/slog"
	"syscall"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

const (
	processQueryLimitedInformation = 0x1000
	processTerminate               = 0x0001
	stillActive                    = 259
)

// Alive opens the process and checks that it has not exited.
func (p *Process) Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

// Stop has no signal to send on Windows; the Windows milestone adds a
// control channel.
func (p *Process) Stop(pid int) error {
	p.log.Debug("stop unsupported", slog.Int("pid", pid))
	return fmt.Errorf("stop: %w", domain.ErrUnsupportedPlatform)
}

// Resync has no signal to send on Windows; see Stop.
func (p *Process) Resync(pid int) error {
	p.log.Debug("resync unsupported", slog.Int("pid", pid))
	return fmt.Errorf("resync: %w", domain.ErrUnsupportedPlatform)
}

// Kill terminates the process immediately.
func (p *Process) Kill(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("kill: %w", domain.ErrNotRunning)
	}
	h, err := syscall.OpenProcess(processTerminate, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("kill %d: %w", pid, domain.ErrNotRunning)
	}
	defer syscall.CloseHandle(h)
	if err := syscall.TerminateProcess(h, 1); err != nil {
		return fmt.Errorf("kill %d: %w", pid, err)
	}
	p.log.Debug("process terminated", slog.Int("pid", pid))
	return nil
}
