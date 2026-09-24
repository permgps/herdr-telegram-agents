package system

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
)

// LaunchUpdateWorker copies the currently running executable out of the
// checkout before detaching. The old root can then be replaced safely.
func LaunchUpdateWorker(ctx context.Context, stateDir, jobID string, log *slog.Logger) (int, error) {
	if jobID == "" {
		return 0, fmt.Errorf("update job id missing")
	}
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	name := "update-worker-" + jobID
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(stateDir, name)
	if err := copyUpdateFile(exe, path); err != nil {
		return 0, fmt.Errorf("stage update worker: %w", err)
	}
	proc := NewProcess(stateDir, log)
	pid, err := proc.spawnExecutable(ctx, path, []string{"update-worker", jobID}, "update-worker.err.log", "")
	if err != nil {
		return 0, err
	}
	if log != nil {
		log.Info("update worker launched", slog.String("job", jobID), slog.Int("pid", pid))
	}
	return pid, nil
}
