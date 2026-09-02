package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDaemonNotConfigured(t *testing.T) {
	_, rec := testEnv(t)
	code, _, stderr := runCLI(t, "daemon")
	if code != exitError {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, exitError, stderr)
	}
	if !strings.Contains(stderr, "not configured") {
		t.Fatalf("stderr = %q", stderr)
	}
	if got := rec.Bodies(); len(got) != 1 || !strings.Contains(got[0], "setup action") {
		t.Fatalf("notifications = %q", got)
	}
}

func TestDaemonAlreadyRunningExitsZero(t *testing.T) {
	env, rec := testEnv(t)
	saveConfig(t, env)
	pid := wire.pidFile(env, nil)
	if err := pid.Acquire(os.Getpid()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pid.Release() })

	code, _, stderr := runCLI(t, "daemon")
	if code != exitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, stderr)
	}
	if !strings.Contains(stderr, "already running") {
		t.Fatalf("stderr = %q", stderr)
	}
	if got := rec.Bodies(); len(got) != 0 {
		t.Fatalf("unexpected notifications %q", got)
	}
	if _, err := os.Stat(filepath.Join(env.StateDir, "daemon.log")); err != nil {
		t.Fatalf("daemon.log missing: %v", err)
	}
}

func TestDaemonHerdrUnreachable(t *testing.T) {
	env, rec := testEnv(t)
	saveConfig(t, env)
	code, _, stderr := runCLI(t, "daemon")
	if code != exitError {
		t.Fatalf("exit = %d, want 1 (stderr %q)", code, stderr)
	}
	if !strings.Contains(stderr, "herdr connect") {
		t.Fatalf("stderr = %q", stderr)
	}
	if got := rec.Bodies(); len(got) != 1 || !strings.Contains(got[0], "could not start") {
		t.Fatalf("notifications = %q", got)
	}
	// The pid file must not linger after a failed start.
	if _, err := os.Stat(filepath.Join(env.StateDir, "daemon.pid")); !os.IsNotExist(err) {
		t.Fatalf("pid file still present (err %v)", err)
	}
	data, err := os.ReadFile(filepath.Join(env.StateDir, "daemon.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"daemon starting"`) || !strings.Contains(string(data), `"daemon build failed"`) {
		t.Fatalf("daemon.log = %s", data)
	}
	if strings.Contains(string(data), "123:abc") {
		t.Fatal("daemon.log leaks the token")
	}
}

func TestDaemonOutsideHerdr(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", "")
	t.Setenv("HERDR_PLUGIN_STATE_DIR", "")
	code, _, stderr := runCLI(t, "daemon")
	if code != exitError || !strings.Contains(stderr, "HERDR_PLUGIN_CONFIG_DIR") {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
}
