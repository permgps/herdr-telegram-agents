package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

const (
	// stopTimeout bounds the wait for a graceful daemon stop before Kill.
	stopTimeout = 10 * time.Second
	// startTimeout bounds the wait for a spawned daemon to become alive.
	startTimeout = 5 * time.Second
	// supervisorPoll is the liveness polling interval.
	supervisorPoll = 200 * time.Millisecond
)

// DaemonStatus is what the status action reports.
type DaemonStatus struct {
	Running bool
	PID     int
	Since   time.Time
}

// Supervisor starts, stops and signals the daemon through the pid file and
// process control ports. It never runs daemon logic itself.
type Supervisor struct {
	pid   domain.PidFile
	proc  domain.ProcessControl
	clock domain.Clock
	log   *slog.Logger

	StopTimeout  time.Duration
	StartTimeout time.Duration
	Poll         time.Duration
}

// NewSupervisor wires the supervisor.
func NewSupervisor(pid domain.PidFile, proc domain.ProcessControl, clock domain.Clock, log *slog.Logger) *Supervisor {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Supervisor{pid: pid, proc: proc, clock: clock, log: log,
		StopTimeout: stopTimeout, StartTimeout: startTimeout, Poll: supervisorPoll}
}

// Status reports whether a daemon is running. A pid file whose process is
// dead counts as not running.
func (s *Supervisor) Status() DaemonStatus {
	info, err := s.pid.Read()
	if err != nil {
		return DaemonStatus{}
	}
	if !s.proc.Alive(info.PID) {
		s.log.Debug("pid file points to a dead process", slog.Int("pid", info.PID))
		return DaemonStatus{PID: info.PID, Since: info.Since}
	}
	return DaemonStatus{Running: true, PID: info.PID, Since: info.Since}
}

// Start spawns the daemon unless one is already running. It waits until the
// child is alive and has claimed the pid file.
func (s *Supervisor) Start(ctx context.Context) (pid int, alreadyRunning bool, err error) {
	if st := s.Status(); st.Running {
		s.log.Info("daemon already running", slog.Int("pid", st.PID))
		return st.PID, true, nil
	}
	pid, err = s.proc.Spawn(ctx, []string{"daemon"})
	if err != nil {
		return 0, false, fmt.Errorf("spawn daemon: %w", err)
	}
	s.log.Info("daemon spawned", slog.Int("pid", pid))
	deadline := s.clock.Now().Add(s.StartTimeout)
	for {
		if !s.proc.Alive(pid) {
			return pid, false, fmt.Errorf("daemon %d exited right after start; check daemon.err.log and daemon.log", pid)
		}
		if info, err := s.pid.Read(); err == nil && info.PID == pid {
			return pid, false, nil
		}
		if !s.clock.Now().Before(deadline) {
			s.log.Warn("daemon did not claim the pid file in time", slog.Int("pid", pid))
			return pid, false, fmt.Errorf("daemon %d started but did not claim the pid file within %s", pid, s.StartTimeout)
		}
		if err := s.sleep(ctx); err != nil {
			return pid, false, err
		}
	}
}

// Stop asks the daemon to exit and escalates to Kill after StopTimeout.
func (s *Supervisor) Stop(ctx context.Context) error {
	st := s.Status()
	if !st.Running {
		return domain.ErrNotRunning
	}
	if err := s.proc.Stop(st.PID); err != nil {
		if errors.Is(err, domain.ErrNotRunning) {
			return domain.ErrNotRunning
		}
		return err
	}
	s.log.Info("daemon stop requested", slog.Int("pid", st.PID))
	deadline := s.clock.Now().Add(s.StopTimeout)
	for s.proc.Alive(st.PID) {
		if !s.clock.Now().Before(deadline) {
			s.log.Warn("daemon ignored stop, killing", slog.Int("pid", st.PID))
			if err := s.proc.Kill(st.PID); err != nil && !errors.Is(err, domain.ErrNotRunning) {
				return fmt.Errorf("kill daemon: %w", err)
			}
			break
		}
		if err := s.sleep(ctx); err != nil {
			return err
		}
	}
	_ = s.pid.Release()
	s.log.Info("daemon stopped", slog.Int("pid", st.PID))
	return nil
}

// Restart stops a running daemon (ignoring a stopped one) and starts a new one.
func (s *Supervisor) Restart(ctx context.Context) (int, error) {
	if err := s.Stop(ctx); err != nil && !errors.Is(err, domain.ErrNotRunning) {
		return 0, err
	}
	pid, _, err := s.Start(ctx)
	return pid, err
}

// Resync asks the running daemon for a full reconcile.
func (s *Supervisor) Resync() error {
	st := s.Status()
	if !st.Running {
		return domain.ErrNotRunning
	}
	if err := s.proc.Resync(st.PID); err != nil {
		return err
	}
	s.log.Info("daemon resync requested", slog.Int("pid", st.PID))
	return nil
}

func (s *Supervisor) sleep(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.clock.After(s.Poll):
		return nil
	}
}

// Summary renders a status line for the actions.
func Summary(st DaemonStatus, now time.Time) string {
	if !st.Running {
		if st.PID != 0 {
			return fmt.Sprintf("not running (stale pid file for %d)", st.PID)
		}
		return "not running"
	}
	up := now.Sub(st.Since).Round(time.Second)
	if up < 0 {
		up = 0
	}
	return fmt.Sprintf("running (pid %d, up %s)", st.PID, up)
}
