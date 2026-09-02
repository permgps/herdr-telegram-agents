package cli

import (
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/compose"
)

func useFakeSupervisor(t *testing.T, f *fakeSupervisor) {
	t.Helper()
	wire.buildSupervisor = func(compose.PluginEnv, *slog.Logger) supervisor { return f }
}

func TestStartupNotConfigured(t *testing.T) {
	_, rec := testEnv(t)
	sup := &fakeSupervisor{}
	useFakeSupervisor(t, sup)
	code, stdout, stderr := runCLI(t, "startup")
	if code != exitOK {
		t.Fatalf("exit = %d (stderr %q)", code, stderr)
	}
	if !strings.Contains(stdout, "not configured") {
		t.Fatalf("stdout = %q", stdout)
	}
	if got := rec.Bodies(); len(got) != 1 || got[0] != "Run the setup action to connect a Telegram group" {
		t.Fatalf("notifications = %q", got)
	}
	if len(sup.calls) != 0 {
		t.Fatalf("supervisor called: %v", sup.calls)
	}
}

func TestStartupStartsDaemon(t *testing.T) {
	env, rec := testEnv(t)
	saveConfig(t, env)
	sup := &fakeSupervisor{startPID: 4242}
	useFakeSupervisor(t, sup)
	code, stdout, stderr := runCLI(t, "startup")
	if code != exitOK {
		t.Fatalf("exit = %d (stderr %q)", code, stderr)
	}
	if stdout != "daemon started (pid 4242)\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if got := sup.calls; len(got) != 1 || got[0] != "start" {
		t.Fatalf("supervisor calls = %v", got)
	}
	if got := rec.Bodies(); len(got) != 0 {
		t.Fatalf("unexpected notifications %q", got)
	}

	sup.already = true
	code, stdout, _ = runCLI(t, "startup")
	if code != exitOK || stdout != "daemon already running (pid 4242)\n" {
		t.Fatalf("exit = %d, stdout = %q", code, stdout)
	}
}

func TestStartupSpawnFailure(t *testing.T) {
	env, rec := testEnv(t)
	saveConfig(t, env)
	useFakeSupervisor(t, &fakeSupervisor{err: errors.New("spawn daemon: boom")})
	code, _, stderr := runCLI(t, "startup")
	if code != exitError || !strings.Contains(stderr, "spawn daemon: boom") {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	if got := rec.Bodies(); len(got) != 1 || !strings.Contains(got[0], "failed to start") {
		t.Fatalf("notifications = %q", got)
	}
}
