package system

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// openCodeExportTimeout bounds the export attempts together.
	openCodeExportTimeout = 10 * time.Second
	// openCodeExportMaxOutput caps the JSON a run may produce. A session
	// export is a few MB in normal use, and truncated JSON cannot be parsed,
	// so a capped run is a failure rather than a partial result.
	openCodeExportMaxOutput = 16 << 20
	// openCodeExportPoll is how often a running export's file size is
	// checked, so a runaway child is killed near the cap instead of filling
	// the disk until the timeout.
	openCodeExportPoll = 50 * time.Millisecond
	// openCodeWaitDelay bounds how long a run waits for its pipes once the
	// timeout killed opencode.
	openCodeWaitDelay = time.Second
	// openCodeTempDir is the private directory under the state dir that
	// holds export files while they are read; openCodeTempPattern names
	// them, so the start-up sweep removes only its own leftovers.
	openCodeTempDir     = "tmp"
	openCodeTempPattern = "opencode-export-*.json"
)

// OpenCodeExporter runs OpenCode's session export command from the binary on
// PATH and returns its JSON. The session id Herdr reported for the pane is
// passed as one argument, never through a shell.
//
// opencode cuts its stdout at 64 KiB multiples when stdout is a pipe and
// still exits 0, so the child writes into a regular file instead: a private
// temp file (0600) under the state dir, read back under the cap and removed
// on every path. The file is watched while the child runs, and the child is
// killed once the file is over the cap.
type OpenCodeExporter struct {
	bin      string
	tempDir  string
	timeout  time.Duration
	maxBytes int
	poll     time.Duration
	log      *slog.Logger
	run      func(*exec.Cmd) error
}

// NewOpenCodeExporter returns an exporter for "opencode" on PATH whose
// export files live in <stateDir>/tmp. An empty stateDir uses the system
// temp directory.
func NewOpenCodeExporter(stateDir string, log *slog.Logger) *OpenCodeExporter {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &OpenCodeExporter{bin: "opencode", tempDir: openCodeTempPath(stateDir), timeout: openCodeExportTimeout,
		maxBytes: openCodeExportMaxOutput, poll: openCodeExportPoll, log: log}
}

// openCodeTempPath is where export files go for stateDir.
func openCodeTempPath(stateDir string) string {
	if stateDir == "" {
		return os.TempDir()
	}
	return filepath.Join(stateDir, openCodeTempDir)
}

// exportRun is the outcome of one opencode run: the output read back from
// the export file, whether it was over the cap (then out is nil), whether
// the size watcher killed the child for it, and a short category when the
// export file itself failed.
type exportRun struct {
	out      []byte
	capped   bool
	killed   bool
	fileStep string
}

// Export runs the export for sessionID. A missing binary, a timeout, a
// non-zero exit, output over the cap and a failing export file are all
// errors.
func (e *OpenCodeExporter) Export(ctx context.Context, sessionID string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("opencode export: %w", err)
	}
	if sessionID == "" || strings.HasPrefix(sessionID, "-") {
		return nil, fmt.Errorf("opencode export: invalid session id")
	}
	bin, err := exec.LookPath(e.bin)
	if err != nil {
		return nil, fmt.Errorf("opencode export: binary not found")
	}
	parentCtx := ctx
	childCtx, cancel := context.WithTimeout(parentCtx, e.timeout)
	defer cancel()
	start := time.Now()
	result, runErr := e.runExport(childCtx, bin, "session", "export", sessionID)
	// OpenCode 1.x used "opencode export". Confirm the major version
	// before trying it: a failed 2.x export can mean an unavailable session,
	// and "opencode export" on 2.x can open its interactive UI.
	var firstExit *exec.ExitError
	if errors.As(runErr, &firstExit) && childCtx.Err() == nil && !result.capped && result.fileStep == "" {
		version, versionErr := e.runExport(childCtx, bin, "--version")
		if versionErr == nil && version.fileStep == "" && !version.capped && openCodeV1Version(string(version.out)) {
			result, runErr = e.runExport(childCtx, bin, "export", sessionID)
		}
	}
	category := "ok"
	if parentCtx.Err() != nil {
		category = "parent_context"
	} else if childCtx.Err() != nil {
		category = "export_timeout"
	} else if result.fileStep != "" {
		category = result.fileStep
	} else if result.capped {
		category = "output_cap"
	} else if runErr != nil {
		category = "run_failed"
	}
	var exitErr *exec.ExitError
	exitCode := -1
	if errors.As(runErr, &exitErr) {
		exitCode = exitErr.ExitCode()
		if category == "run_failed" {
			category = "exit_nonzero"
		}
	}
	e.log.Debug("opencode export", slog.Int64("dur_ms", time.Since(start).Milliseconds()),
		slog.Int("bytes", len(result.out)), slog.Bool("capped", result.capped), slog.Bool("killed_at_cap", result.killed),
		slog.String("category", category), slog.Int("exit_code", exitCode))
	switch {
	case parentCtx.Err() != nil:
		return nil, fmt.Errorf("opencode export: %w", parentCtx.Err())
	case childCtx.Err() != nil:
		return nil, errors.New("opencode export: timed out")
	case result.fileStep != "":
		return nil, fmt.Errorf("opencode export: %s failed", strings.ReplaceAll(result.fileStep, "_", " "))
	case runErr == nil && !result.capped:
		return result.out, nil
	case result.capped:
		return nil, fmt.Errorf("opencode export: output over %d bytes", e.maxBytes)
	}
	if exitErr != nil {
		return nil, fmt.Errorf("opencode export: exit code %d", exitCode)
	}
	return nil, fmt.Errorf("opencode export: start or wait failed")
}

// runExport runs opencode with args, its stdout in a fresh export file.
// The file is closed and removed before returning whatever happened; its
// size is checked before it is read, and a Stat, Seek or read failure is
// reported as fileStep with a nil output. runErr is the child's own result.
// Without the run seam the child is watched while it writes and killed
// once the file is over the cap; the checks after the run stay as a
// backstop.
func (e *OpenCodeExporter) runExport(ctx context.Context, bin string, args ...string) (exportRun, error) {
	if err := os.MkdirAll(e.tempDir, 0o700); err != nil {
		e.log.Debug("opencode export file", slog.String("step", "temp_dir"), slog.String("err", errorKind(err)))
		return exportRun{fileStep: "temp_file"}, nil
	}
	file, err := os.CreateTemp(e.tempDir, openCodeTempPattern)
	if err != nil {
		e.log.Debug("opencode export file", slog.String("step", "create"), slog.String("err", errorKind(err)))
		return exportRun{fileStep: "temp_file"}, nil
	}
	name := file.Name()
	defer func() {
		// Close before remove: Windows cannot remove an open file.
		_ = file.Close()
		if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			e.log.Debug("opencode export file", slog.String("step", "temp_remove_failed"),
				slog.String("name", filepath.Base(name)), slog.String("err", errorKind(err)))
		}
	}()
	cmd := command(ctx, bin, args...)
	cmd.WaitDelay = openCodeWaitDelay
	cmd.Stdout, cmd.Stderr = file, io.Discard
	killed, runErr := e.runChild(cmd, file)
	if killed {
		return exportRun{capped: true, killed: true}, runErr
	}
	info, err := file.Stat()
	if err != nil {
		e.log.Debug("opencode export file", slog.String("step", "stat"), slog.String("err", errorKind(err)))
		return exportRun{fileStep: "temp_read"}, runErr
	}
	if info.Size() > int64(e.maxBytes) {
		return exportRun{capped: true}, runErr
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		e.log.Debug("opencode export file", slog.String("step", "seek"), slog.String("err", errorKind(err)))
		return exportRun{fileStep: "temp_read"}, runErr
	}
	out, err := io.ReadAll(io.LimitReader(file, int64(e.maxBytes)+1))
	if err != nil {
		e.log.Debug("opencode export file", slog.String("step", "read"), slog.String("err", errorKind(err)))
		return exportRun{fileStep: "temp_read"}, runErr
	}
	// The child may have written more between Stat and the read.
	if len(out) > e.maxBytes {
		return exportRun{capped: true}, runErr
	}
	return exportRun{out: out}, runErr
}

// runChild runs cmd through the run seam when a test set one. Otherwise it
// starts cmd and waits for it while a watcher polls the size of file, the
// child's stdout, and kills the child as soon as it is over the cap. The
// watcher is owned by this call and exits when Wait returns. killed
// reports whether the watcher killed the child; err is the child's result.
func (e *OpenCodeExporter) runChild(cmd *exec.Cmd, file *os.File) (killed bool, err error) {
	if e.run != nil {
		return false, e.run(cmd)
	}
	if err := cmd.Start(); err != nil {
		return false, err
	}
	poll := e.poll
	if poll <= 0 {
		poll = openCodeExportPoll
	}
	done := make(chan struct{})
	result := make(chan bool, 1)
	go func() {
		ticker := time.NewTicker(poll)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				result <- false
				return
			case <-ticker.C:
				info, err := file.Stat()
				if err != nil || info.Size() <= int64(e.maxBytes) {
					continue
				}
				_ = cmd.Process.Kill()
				result <- true
				return
			}
		}
	}()
	err = cmd.Wait()
	close(done)
	return <-result, err
}

// errorKind names an OS error without its path, which holds the temp file
// name under the user's state dir.
func errorKind(err error) string {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Op + ": " + pathErr.Err.Error()
	}
	return err.Error()
}

// SweepOpenCodeExports removes export files a crashed daemon left in
// <stateDir>/tmp. Only the daemon exports and it is single-instance, so
// nothing is in flight when it starts. A missing directory is not an
// error. It returns how many files were removed.
func SweepOpenCodeExports(stateDir string, log *slog.Logger) int {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if stateDir == "" {
		return 0
	}
	matches, err := filepath.Glob(filepath.Join(openCodeTempPath(stateDir), openCodeTempPattern))
	if err != nil {
		log.Debug("opencode export sweep failed", slog.String("err", err.Error()))
		return 0
	}
	removed := 0
	for _, path := range matches {
		if err := os.Remove(path); err != nil {
			log.Debug("opencode export leftover not removed", slog.String("name", filepath.Base(path)), slog.String("err", errorKind(err)))
			continue
		}
		removed++
	}
	if removed > 0 {
		log.Info("opencode export leftovers removed", slog.Int("count", removed))
	}
	log.Debug("opencode export sweep", slog.Int("found", len(matches)), slog.Int("removed", removed))
	return removed
}

func openCodeV1Version(version string) bool {
	for _, field := range strings.Fields(version) {
		if strings.HasPrefix(strings.TrimPrefix(field, "v"), "1.") {
			return true
		}
	}
	return false
}
