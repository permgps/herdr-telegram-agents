package domain

import (
	"context"
	"time"
)

// PidInfo describes the daemon recorded in the pid file.
type PidInfo struct {
	PID   int
	Since time.Time
}

// PidFile is the single-instance lock of the daemon. Acquire fails with
// ErrAlreadyRunning when another process owns the file; the supervisor
// decides whether that owner is alive.
type PidFile interface {
	Acquire(pid int) error
	Read() (PidInfo, error)
	Release() error
}

// ProcessControl starts and controls the daemon process. Stop, Resync and
// Status reach a running daemon through its control channel and return
// ErrControlUnavailable when it is not listening.
type ProcessControl interface {
	// Spawn starts the plugin binary detached with the given arguments and
	// returns its pid without waiting for it.
	Spawn(ctx context.Context, args []string) (int, error)
	// Alive reports whether a process with the pid exists.
	Alive(pid int) bool
	// Stop asks the daemon to shut down gracefully.
	Stop(pid int) error
	// Resync asks the daemon to run a full reconcile.
	Resync(pid int) error
	// Status asks the daemon for its one-line status.
	Status(ctx context.Context) (string, error)
	// Kill terminates the daemon immediately.
	Kill(pid int) error
}
