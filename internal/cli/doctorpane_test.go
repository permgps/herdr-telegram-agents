package cli

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/compose"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

type fakeDoctor struct {
	checks []domain.Check
	runs   int
}

func (f *fakeDoctor) Run(context.Context) []domain.Check {
	f.runs++
	return f.checks
}

func TestDoctorPanePrintsReportAndClosesOnEOF(t *testing.T) {
	testEnv(t)
	doc := &fakeDoctor{checks: []domain.Check{
		{Name: "config", Level: domain.CheckOK, Detail: "config.json v1"},
		{Name: "group", Level: domain.CheckWarn, Detail: "no delete right"},
		{Name: "herdr", Level: domain.CheckFail, Detail: "socket gone"},
	}}
	wire.buildDoctor = func(_ compose.PluginEnv, version string, _ *slog.Logger) doctor {
		if version != "test" {
			t.Errorf("version = %q", version)
		}
		return doc
	}
	saved := stdin
	stdin = strings.NewReader("") // EOF closes the pane at once
	t.Cleanup(func() { stdin = saved })

	code, stdout, stderr := runCLI(t, "doctor-pane")
	if code != exitOK {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
	want := []string{
		"running checks…",
		"Telegram Agents doctor test",
		"✓ config: config.json v1",
		"! group: no delete right",
		"✗ herdr: socket gone",
		"1 ok, 1 warning, 1 failure",
		"(Esc or q to close)",
	}
	for _, w := range want {
		if !strings.Contains(stdout, w) {
			t.Errorf("stdout lacks %q:\n%s", w, stdout)
		}
	}
	if doc.runs != 1 {
		t.Fatalf("doctor ran %d times", doc.runs)
	}
}

func TestDoctorPaneOutsideHerdr(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", "")
	t.Setenv("HERDR_PLUGIN_STATE_DIR", "")
	code, _, stderr := runCLI(t, "doctor-pane")
	if code != exitError || !strings.Contains(stderr, "HERDR_PLUGIN_CONFIG_DIR") {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
}
