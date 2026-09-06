package system

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// gitRepo builds a repository with one commit and a dirty tracked file;
// tests skip when git is not on PATH.
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_NOSYSTEM=1", "HOME="+dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-q", "-m", "first")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestGitRunnerStatusDiffLog(t *testing.T) {
	dir := gitRepo(t)
	r := NewGitRunner(nil)
	ctx := context.Background()
	status, err := r.Run(ctx, dir, []string{"status", "--short", "--branch"})
	if err != nil || !strings.HasPrefix(status.Output, "## main") || !strings.Contains(status.Output, " M a.txt") || status.Truncated {
		t.Fatalf("status = %+v, %v", status, err)
	}
	diff, err := r.Run(ctx, dir, []string{"diff", "HEAD"})
	if err != nil || !strings.Contains(diff.Output, "+two") || strings.Contains(diff.Output, "\x1b[") {
		t.Fatalf("diff = %+v, %v", diff, err)
	}
	log, err := r.Run(ctx, dir, []string{"log", "--oneline", "--decorate", "-n", "10"})
	if err != nil || !strings.Contains(log.Output, "first") || strings.Count(log.Output, "\n") != 0 {
		t.Fatalf("log = %+v, %v", log, err)
	}
	staged, err := r.Run(ctx, dir, []string{"diff", "--cached"})
	if err != nil || staged.Output != "" {
		t.Fatalf("staged diff = %+v, %v", staged, err)
	}
}

func TestGitRunnerNotRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	r := NewGitRunner(nil)
	_, err := r.Run(context.Background(), dir, []string{"status", "--short", "--branch"})
	if !errors.Is(err, domain.ErrNotRepository) || !strings.Contains(err.Error(), dir) {
		t.Fatalf("err = %v", err)
	}
}

func TestGitRunnerMissingBinary(t *testing.T) {
	r := NewGitRunner(nil)
	r.bin = "git-definitely-missing-binary"
	if _, err := r.Run(context.Background(), t.TempDir(), []string{"status"}); !errors.Is(err, domain.ErrGitMissing) {
		t.Fatalf("err = %v", err)
	}
}

func TestGitRunnerBadArgsCarryStderr(t *testing.T) {
	dir := gitRepo(t)
	r := NewGitRunner(nil)
	_, err := r.Run(context.Background(), dir, []string{"log", "--no-such-flag"})
	if err == nil || errors.Is(err, domain.ErrNotRepository) || !strings.Contains(err.Error(), "no-such-flag") {
		t.Fatalf("err = %v", err)
	}
}

func TestGitRunnerTruncates(t *testing.T) {
	dir := gitRepo(t)
	big := strings.Repeat("a line that makes the diff long enough to pass the cap\n", 400)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewGitRunner(nil)
	r.maxBytes = 64
	res, err := r.Run(context.Background(), dir, []string{"diff", "HEAD"})
	if err != nil || !res.Truncated || len(res.Output) > 64 || res.Output == "" {
		t.Fatalf("truncated run = %+v, %v", res, err)
	}
}

func TestGitRunnerTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in is Unix only")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "slow-git")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := NewGitRunner(nil)
	r.bin = script
	r.timeout = 100 * time.Millisecond
	start := time.Now()
	_, err := r.Run(context.Background(), dir, []string{"status"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("timeout did not stop the process: %v", time.Since(start))
	}
}
