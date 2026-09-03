//go:build !windows

package system

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"github.com/permgps/herdr-telegram-agents/internal/testkit"
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

// TestStopPrefersControlChannel checks the order the actions rely on: a
// listening daemon is asked politely and never signalled, and a daemon
// without a channel still gets SIGTERM.
func TestStopPrefersControlChannel(t *testing.T) {
	dir := testkit.ShortTempDir(t)
	ln, err := ListenControl(dir, nil)
	if err != nil {
		t.Fatalf("ListenControl: %v", err)
	}
	var mu sync.Mutex
	stops, resyncs := 0, 0
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() {
		defer close(served)
		ServeControl(ctx, ln, ControlHandlers{
			Stop: func() {
				mu.Lock()
				stops++
				mu.Unlock()
			},
			Resync: func() {
				mu.Lock()
				resyncs++
				mu.Unlock()
			},
			Status: func() string { return "version=test pid=1 uptime=0s agents=0 dropped=0 herdr=ok" },
		}, nil)
	}()
	t.Cleanup(func() {
		cancel()
		<-served
	})

	p := NewProcess(dir, nil)
	p.exe = "/bin/sh"
	pid, err := p.Spawn(context.Background(), []string{"-c", "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Kill(pid) })
	waitUntil(t, func() bool { return p.Alive(pid) }, "child alive")

	if err := p.Stop(pid); err != nil {
		t.Fatalf("Stop = %v", err)
	}
	if err := p.Resync(pid); err != nil {
		t.Fatalf("Resync = %v", err)
	}
	line, err := p.Status(context.Background())
	if err != nil || line != "version=test pid=1 uptime=0s agents=0 dropped=0 herdr=ok" {
		t.Fatalf("Status = %q, err = %v", line, err)
	}
	mu.Lock()
	gotStops, gotResyncs := stops, resyncs
	mu.Unlock()
	if gotStops != 1 || gotResyncs != 1 {
		t.Fatalf("handlers called stop=%d resync=%d", gotStops, gotResyncs)
	}
	// The control channel answered, so no signal reached the child.
	time.Sleep(50 * time.Millisecond)
	if !p.Alive(pid) {
		t.Fatal("child was signalled although the control channel answered")
	}
}

// TestStatusWithoutControlChannel reports the sentinel the supervisor
// checks for.
func TestStatusWithoutControlChannel(t *testing.T) {
	p := NewProcess(testkit.ShortTempDir(t), nil)
	if _, err := p.Status(context.Background()); !errors.Is(err, domain.ErrControlUnavailable) {
		t.Fatalf("Status = %v, want ErrControlUnavailable", err)
	}
}
