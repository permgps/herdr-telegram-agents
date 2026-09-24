package system

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// ErrLogFileName captures the detached daemon's stdout and stderr (panics,
// library noise). It is truncated on every spawn.
const ErrLogFileName = "daemon.err.log"

// Process implements domain.ProcessControl for the plugin binary: detached
// spawn into the state directory's error log, liveness checks and signals.
type Process struct {
	stateDir string
	exe      string // override for tests; empty means os.Executable()
	log      *slog.Logger
}

var _ domain.ProcessControl = (*Process)(nil)

// control sends one command to the daemon listening for this state
// directory.
func (p *Process) control(cmd string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), controlTimeout)
	defer cancel()
	reply, err := SendControl(ctx, p.stateDir, cmd)
	p.log.Debug("control command sent", slog.String("cmd", cmd), slog.String("reply", reply), slog.Any("err", err))
	return reply, err
}

// Status asks the running daemon for its status line.
func (p *Process) Status(ctx context.Context) (string, error) {
	return SendControl(ctx, p.stateDir, ControlStatus)
}

// NewProcess returns process control writing the error log under stateDir.
func NewProcess(stateDir string, log *slog.Logger) *Process {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Process{stateDir: stateDir, log: log}
}

// Spawn starts the plugin binary with args, detached from this process:
// own session or process group, stdin from the null device, stdout and
// stderr appended to daemon.err.log. It returns the child's pid without
// waiting for it.
func (p *Process) Spawn(ctx context.Context, args []string) (int, error) {
	exe := p.exe
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return 0, fmt.Errorf("locate executable: %w", err)
		}
	}
	return p.spawnExecutable(ctx, exe, args, ErrLogFileName, "")
}

// SpawnAt starts the daemon from the explicit installed root. The updater
// uses it because its own executable is a copy under the state directory.
func (p *Process) SpawnAt(ctx context.Context, root string, args []string) (int, error) {
	name := "herdr-tg"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return p.spawnExecutable(ctx, filepath.Join(root, "bin", name), args, ErrLogFileName, root)
}

func (p *Process) spawnExecutable(ctx context.Context, exe string, args []string, logName, rootEnv string) (int, error) {
	if err := os.MkdirAll(p.stateDir, 0o755); err != nil {
		return 0, fmt.Errorf("mkdir state dir: %w", err)
	}
	errPath := filepath.Join(p.stateDir, logName)
	errLog, err := os.OpenFile(errPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", errPath, err)
	}
	defer errLog.Close()

	cmd := exec.CommandContext(ctx, exe, args...)
	if rootEnv != "" {
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "HERDR_PLUGIN_ROOT=") {
				cmd.Env = append(cmd.Env, value)
			}
		}
		cmd.Env = append(cmd.Env, "HERDR_PLUGIN_ROOT="+rootEnv)
	}
	cmd.Stdin = nil
	cmd.Stdout = errLog
	cmd.Stderr = errLog
	cmd.SysProcAttr = detachAttr()
	// CommandContext would kill the child when ctx ends; the daemon must
	// outlive the action that started it.
	cmd.Cancel = func() error { return nil }
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start %s: %w", exe, err)
	}
	pid := cmd.Process.Pid
	// Reap the child if it exits while this process is still around, so a
	// dead daemon never lingers as a zombie that looks alive to kill(pid, 0).
	// In the normal case the spawning action exits first and init reaps.
	go func() {
		err := cmd.Wait()
		p.log.Debug("spawned process exited", slog.Int("pid", pid), slog.Any("err", err))
	}()
	p.log.Debug("process spawned", slog.Int("pid", pid), slog.Int("args_len", len(args)), slog.String("err_log", errPath))
	return pid, nil
}
