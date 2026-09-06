package system

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

const (
	// gitTimeout bounds one git run.
	gitTimeout = 10 * time.Second
	// gitMaxOutput is the most stdout a run may produce; the rest is cut
	// and the result marked truncated.
	gitMaxOutput = 5 << 20
	// gitWaitDelay bounds how long a run waits for its pipes once the
	// timeout killed git: a child that inherited stdout (a hook, a helper)
	// would otherwise keep Wait blocked until it exits on its own.
	gitWaitDelay = time.Second
)

// errOutputCapped stops the stdout copy once gitMaxOutput is reached.
var errOutputCapped = errors.New("git output capped")

// GitRunner implements domain.GitRunner over the git binary on PATH: a
// fixed argv from the domain runs in the agent's directory with colour
// and pager off, a timeout and an output cap.
type GitRunner struct {
	bin      string
	timeout  time.Duration
	maxBytes int
	log      *slog.Logger
}

var _ domain.GitRunner = (*GitRunner)(nil)

// NewGitRunner returns a runner for "git" on PATH.
func NewGitRunner(log *slog.Logger) *GitRunner {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &GitRunner{bin: "git", timeout: gitTimeout, maxBytes: gitMaxOutput, log: log}
}

// Run implements domain.GitRunner.
func (r *GitRunner) Run(ctx context.Context, dir string, args []string) (domain.GitResult, error) {
	bin, err := exec.LookPath(r.bin)
	if err != nil {
		r.log.Warn("git binary not found", slog.String("bin", r.bin), slog.String("err", err.Error()))
		return domain.GitResult{}, domain.ErrGitMissing
	}
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	argv := append([]string{"-c", "color.ui=never"}, args...)
	cmd := exec.CommandContext(ctx, bin, argv...)
	cmd.Dir = dir
	cmd.WaitDelay = gitWaitDelay
	cmd.Env = append(os.Environ(), "GIT_PAGER=cat", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	stdout := &limitedWriter{max: r.maxBytes}
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = stdout, &stderr
	start := time.Now()
	runErr := cmd.Run()
	exit := -1
	if cmd.ProcessState != nil {
		exit = cmd.ProcessState.ExitCode()
	}
	r.log.Debug("git run", slog.String("dir", dir), slog.String("args", strings.Join(args, " ")),
		slog.Int64("dur_ms", time.Since(start).Milliseconds()), slog.Int("bytes", stdout.buf.Len()),
		slog.Bool("truncated", stdout.capped), slog.Int("exit", exit), slog.Any("err", runErr))
	res := domain.GitResult{Output: strings.TrimRight(stdout.buf.String(), " \t\r\n"), Truncated: stdout.capped}
	switch {
	case runErr == nil:
		return res, nil
	case stdout.capped && (errors.Is(runErr, errOutputCapped) || exit == 0 || isBrokenPipe(runErr, stderr.String())):
		// The process was cut by the cap, not by its own failure.
		return res, nil
	case ctx.Err() != nil:
		r.log.Warn("git timed out", slog.String("dir", dir), slog.String("args", strings.Join(args, " ")))
		return domain.GitResult{}, context.DeadlineExceeded
	}
	msg := strings.TrimSpace(stderr.String())
	r.log.Warn("git failed", slog.String("dir", dir), slog.String("args", strings.Join(args, " ")), slog.Int("exit", exit), slog.String("stderr", firstLine(msg)))
	if strings.Contains(msg, "not a git repository") {
		return domain.GitResult{}, fmt.Errorf("%w: %s", domain.ErrNotRepository, dir)
	}
	if msg == "" {
		return domain.GitResult{}, fmt.Errorf("git %s: %w", args[0], runErr)
	}
	return domain.GitResult{}, fmt.Errorf("git %s: %w: %s", args[0], runErr, firstLine(msg))
}

// isBrokenPipe reports whether git died because its stdout was closed by
// the cap (signalled by SIGPIPE or an EPIPE line on stderr).
func isBrokenPipe(err error, stderr string) bool {
	return strings.Contains(err.Error(), "broken pipe") || strings.Contains(err.Error(), "signal: broken pipe") ||
		strings.Contains(stderr, "Broken pipe")
}

// firstLine returns the first line of s.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}

// limitedWriter collects up to max bytes and refuses the rest, which stops
// exec's copy and lets the process die on a closed pipe.
type limitedWriter struct {
	buf    bytes.Buffer
	max    int
	capped bool
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	room := w.max - w.buf.Len()
	if room <= 0 {
		w.capped = true
		return 0, errOutputCapped
	}
	if len(p) > room {
		w.buf.Write(p[:room])
		w.capped = true
		return room, errOutputCapped
	}
	return w.buf.Write(p)
}
