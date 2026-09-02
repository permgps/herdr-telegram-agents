//go:build !windows

package system

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func waitUntil(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestAliveOwnAndReapedPid(t *testing.T) {
	p := NewProcess(t.TempDir(), nil)
	if !p.Alive(os.Getpid()) {
		t.Fatal("own pid reported dead")
	}
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skip("no `true` binary:", err)
	}
	if p.Alive(cmd.Process.Pid) {
		t.Fatal("reaped child reported alive")
	}
	if p.Alive(0) || p.Alive(-1) {
		t.Fatal("non-positive pid reported alive")
	}
}

func TestSpawnStopAndKill(t *testing.T) {
	dir := t.TempDir()
	p := NewProcess(dir, nil)
	p.exe = "/bin/sh"

	pid, err := p.Spawn(context.Background(), []string{"-c", "echo spawned; sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Kill(pid) })
	waitUntil(t, func() bool { return p.Alive(pid) }, "child alive")
	waitUntil(t, func() bool {
		b, _ := os.ReadFile(filepath.Join(dir, ErrLogFileName))
		return string(b) == "spawned\n"
	}, "child output in err log")

	if err := p.Resync(pid); err != nil {
		// sh ignores nothing by default: SIGHUP ends it, which is fine here.
		t.Fatalf("Resync = %v", err)
	}
	waitUntil(t, func() bool { return !p.Alive(pid) }, "child gone after SIGHUP")

	pid, err = p.Spawn(context.Background(), []string{"-c", "trap '' HUP; sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Kill(pid) })
	waitUntil(t, func() bool { return p.Alive(pid) }, "second child alive")
	if err := p.Stop(pid); err != nil {
		t.Fatalf("Stop = %v", err)
	}
	waitUntil(t, func() bool { return !p.Alive(pid) }, "child gone after SIGTERM")
	if err := p.Stop(pid); !errors.Is(err, domain.ErrNotRunning) {
		t.Fatalf("Stop on dead pid = %v, want ErrNotRunning", err)
	}
}

func TestSpawnSurvivesContextCancel(t *testing.T) {
	p := NewProcess(t.TempDir(), nil)
	p.exe = "/bin/sh"
	ctx, cancel := context.WithCancel(context.Background())
	pid, err := p.Spawn(ctx, []string{"-c", "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Kill(pid) })
	cancel()
	time.Sleep(100 * time.Millisecond)
	if !p.Alive(pid) {
		t.Fatal("child died when the spawning context was cancelled")
	}
	if err := p.Kill(pid); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return !p.Alive(pid) }, "child gone after SIGKILL")
}
