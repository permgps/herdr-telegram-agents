package cli

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/compose"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

type fakeOpener struct {
	calls []string
	err   error
}

func (f *fakeOpener) OpenPane(_ context.Context, pluginID, entrypoint string) error {
	f.calls = append(f.calls, pluginID+"/"+entrypoint)
	return f.err
}

func useFakes(t *testing.T, sup *fakeSupervisor, op *fakeOpener) {
	t.Helper()
	wire.buildSupervisor = func(compose.PluginEnv, *slog.Logger) supervisor { return sup }
	wire.paneOpener = func(compose.PluginEnv, *slog.Logger) domain.PaneOpener { return op }
}

func TestActionDispatch(t *testing.T) {
	tests := []struct {
		id       string
		sup      fakeSupervisor
		opener   fakeOpener
		wantCode int
		wantOut  string   // substring of stdout (or stderr on failure)
		wantSup  []string // supervisor calls
		wantPane []string
	}{
		{id: "setup", wantCode: exitOK, wantOut: "opened the setup pane", wantPane: []string{"permgps.telegram-agents/setup"}},
		{id: "logs", wantCode: exitOK, wantOut: "opened the logs pane", wantPane: []string{"permgps.telegram-agents/logs"}},
		{id: "start", sup: fakeSupervisor{startPID: 77}, wantCode: exitOK, wantOut: "daemon started (pid 77)", wantSup: []string{"start"}},
		{id: "start", sup: fakeSupervisor{startPID: 77, already: true}, wantCode: exitOK, wantOut: "already running (pid 77)", wantSup: []string{"start"}},
		{id: "stop", wantCode: exitOK, wantOut: "daemon stopped", wantSup: []string{"stop"}},
		{id: "stop", sup: fakeSupervisor{err: domain.ErrNotRunning}, wantCode: exitOK, wantOut: "not running", wantSup: []string{"stop"}},
		{id: "restart", sup: fakeSupervisor{startPID: 78}, wantCode: exitOK, wantOut: "restarted (pid 78)", wantSup: []string{"restart"}},
		{id: "status", sup: fakeSupervisor{status: compose.DaemonStatus{Running: true, PID: 5, Since: time.Now()},
			describe: "version=v0.1.0 pid=5 uptime=1m0s agents=2 dropped=0 herdr=ok"},
			wantCode: exitOK, wantOut: "agents=2 dropped=0 herdr=ok", wantSup: []string{"describe"}},
		{id: "status", wantCode: exitOK, wantOut: "daemon not running", wantSup: []string{"describe"}},
		{id: "resync", wantCode: exitOK, wantOut: "resync requested", wantSup: []string{"resync"}},
		{id: "resync", sup: fakeSupervisor{err: domain.ErrNotRunning}, wantCode: exitOK, wantOut: "start it first", wantSup: []string{"resync"}},
		{id: "stop", sup: fakeSupervisor{err: domain.ErrUnsupportedPlatform}, wantCode: exitError, wantOut: "not available on this platform", wantSup: []string{"stop"}},
		{id: "resync", sup: fakeSupervisor{err: domain.ErrControlUnavailable}, wantCode: exitError, wantOut: "not listening on its control channel", wantSup: []string{"resync"}},
		{id: "restart", sup: fakeSupervisor{err: errors.New("spawn daemon: boom")}, wantCode: exitError, wantOut: "restart failed: spawn daemon: boom", wantSup: []string{"restart"}},
		{id: "setup", opener: fakeOpener{err: errors.New("herdr plugin pane open: exit 1")}, wantCode: exitError, wantOut: "setup failed", wantPane: []string{"permgps.telegram-agents/setup"}},
	}
	for _, tt := range tests {
		t.Run(tt.id+"/"+tt.wantOut, func(t *testing.T) {
			_, rec := testEnv(t)
			sup, op := tt.sup, tt.opener
			useFakes(t, &sup, &op)
			code, stdout, stderr := runCLI(t, "action", tt.id)
			if code != tt.wantCode {
				t.Fatalf("exit = %d, want %d (stdout %q, stderr %q)", code, tt.wantCode, stdout, stderr)
			}
			out := stdout
			if tt.wantCode != exitOK {
				out = stderr
			}
			if !strings.Contains(out, tt.wantOut) {
				t.Fatalf("output = %q, want %q", out, tt.wantOut)
			}
			if got := strings.Join(sup.calls, ","); got != strings.Join(tt.wantSup, ",") {
				t.Fatalf("supervisor calls = %v, want %v", sup.calls, tt.wantSup)
			}
			if got := strings.Join(op.calls, ","); got != strings.Join(tt.wantPane, ",") {
				t.Fatalf("opener calls = %v, want %v", op.calls, tt.wantPane)
			}
			if got := rec.Bodies(); len(got) != 1 || !strings.Contains(got[0], tt.wantOut) {
				t.Fatalf("notifications = %q, want one containing %q", got, tt.wantOut)
			}
		})
	}
}

func TestActionUsage(t *testing.T) {
	testEnv(t)
	for _, args := range [][]string{{"action"}, {"action", "bogus"}, {"action", "start", "extra"}} {
		code, _, stderr := runCLI(t, args...)
		if code != exitUsage || !strings.Contains(stderr, "usage: herdr-tg action setup|start|stop|restart|status|resync|logs") {
			t.Fatalf("Run(%v) = %d, stderr %q", args, code, stderr)
		}
	}
}
