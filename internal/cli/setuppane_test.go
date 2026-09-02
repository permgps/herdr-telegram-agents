package cli

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/compose"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func TestConsoleUI(t *testing.T) {
	in := strings.NewReader("  123:abc \nY\nn\nx\n2\n")
	var out bytes.Buffer
	ui := newConsoleUI(in, &out)
	if got, err := ui.AskSecret("Token:"); err != nil || got != "123:abc" {
		t.Fatalf("AskSecret = %q, %v", got, err)
	}
	if !strings.Contains(out.String(), "visible while you type") {
		t.Fatalf("no visibility warning: %q", out.String())
	}
	if ok, err := ui.Confirm("Save? [y/N]"); err != nil || !ok {
		t.Fatalf("Confirm(Y) = %v, %v", ok, err)
	}
	if ok, err := ui.Confirm("Save? [y/N]"); err != nil || ok {
		t.Fatalf("Confirm(n) = %v, %v", ok, err)
	}
	if idx, err := ui.Choose("Group", []string{"a", "b"}); err != nil || idx != 1 {
		t.Fatalf("Choose = %d, %v", idx, err)
	}
	if !strings.Contains(out.String(), "1) a") || !strings.Contains(out.String(), "between 1 and 2") {
		t.Fatalf("Choose output = %q", out.String())
	}
	if _, err := ui.Ask("more?"); err == nil {
		t.Fatal("Ask at EOF should fail")
	}
}

type fakeSetup struct {
	cfg   domain.Config
	saved bool
	err   error
	ui    domain.SetupUI
}

func (f *fakeSetup) Run(context.Context) (domain.Config, bool, error) {
	if f.ui != nil {
		f.ui.Print("wizard ran")
	}
	return f.cfg, f.saved, f.err
}

func useFakeSetup(t *testing.T, fs *fakeSetup, sup *fakeSupervisor, input string) {
	t.Helper()
	wire.buildSetup = func(_ compose.PluginEnv, ui domain.SetupUI, _ *slog.Logger) setupRunner {
		fs.ui = ui
		return fs
	}
	wire.buildSupervisor = func(compose.PluginEnv, *slog.Logger) supervisor { return sup }
	saved := stdin
	stdin = strings.NewReader(input)
	t.Cleanup(func() { stdin = saved })
}

func TestSetupPaneStartsDaemon(t *testing.T) {
	testEnv(t)
	sup := &fakeSupervisor{startPID: 91}
	useFakeSetup(t, &fakeSetup{cfg: domain.Config{ChatTitle: "Agents"}, saved: true}, sup, "\n")
	code, stdout, stderr := runCLI(t, "setup-pane")
	if code != exitOK {
		t.Fatalf("exit = %d (stderr %q)", code, stderr)
	}
	for _, want := range []string{"wizard ran", `Daemon started (pid 91) for group "Agents".`, "press Enter to close"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	if got := strings.Join(sup.calls, ","); got != "status,start" {
		t.Fatalf("supervisor calls = %v", sup.calls)
	}
}

func TestSetupPaneRestartsRunningDaemon(t *testing.T) {
	testEnv(t)
	sup := &fakeSupervisor{startPID: 92, status: compose.DaemonStatus{Running: true, PID: 5}}
	useFakeSetup(t, &fakeSetup{cfg: domain.Config{ChatTitle: "Agents"}, saved: true}, sup, "\n")
	code, stdout, _ := runCLI(t, "setup-pane")
	if code != exitOK || !strings.Contains(stdout, "Daemon restarted (pid 92)") {
		t.Fatalf("exit = %d, stdout = %q", code, stdout)
	}
	if got := strings.Join(sup.calls, ","); got != "status,restart" {
		t.Fatalf("supervisor calls = %v", sup.calls)
	}
}

func TestSetupPaneCancelledAndFailed(t *testing.T) {
	testEnv(t)
	sup := &fakeSupervisor{}
	useFakeSetup(t, &fakeSetup{err: ErrSetupCancelled}, sup, "\n")
	code, stdout, _ := runCLI(t, "setup-pane")
	if code != exitOK || !strings.Contains(stdout, "Setup cancelled") || len(sup.calls) != 0 {
		t.Fatalf("cancelled: exit = %d, stdout = %q, calls = %v", code, stdout, sup.calls)
	}

	useFakeSetup(t, &fakeSetup{err: errors.New("token rejected 3 times")}, sup, "")
	code, stdout, _ = runCLI(t, "setup-pane")
	if code != exitError || !strings.Contains(stdout, "Setup failed: token rejected 3 times") {
		t.Fatalf("failed: exit = %d, stdout = %q", code, stdout)
	}
}
