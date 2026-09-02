package cli

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string // prefix
		wantStderr string // substring
	}{
		{name: "no args prints usage", args: nil, wantCode: 2, wantStderr: "usage: herdr-tg"},
		{name: "unknown subcommand", args: []string{"bogus"}, wantCode: 2, wantStderr: `unknown subcommand "bogus"`},
		{name: "version", args: []string{"version"}, wantCode: 0, wantStdout: "herdr-tg 1.2.3 "},
		{name: "daemon not implemented", args: []string{"daemon"}, wantCode: 2, wantStderr: "herdr-tg daemon: not implemented yet"},
		{name: "action not implemented", args: []string{"action", "setup"}, wantCode: 2, wantStderr: "herdr-tg action: not implemented yet"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			got := Run(tt.args, "1.2.3", &stdout, &stderr)
			if got != tt.wantCode {
				t.Fatalf("Run(%v) = %d, want %d (stderr: %s)", tt.args, got, tt.wantCode, stderr.String())
			}
			if tt.wantStdout != "" && !strings.HasPrefix(stdout.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want prefix %q", stdout.String(), tt.wantStdout)
			}
			if tt.wantStderr != "" && !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want substring %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestUsageHidesDev(t *testing.T) {
	var stderr bytes.Buffer
	Run(nil, "dev", &bytes.Buffer{}, &stderr)
	if strings.Contains(stderr.String(), "dev ") {
		t.Errorf("usage must not document the dev subcommand:\n%s", stderr.String())
	}
}

func TestParseLevel(t *testing.T) {
	tests := map[string]slog.Level{
		"":        slog.LevelInfo,
		"debug":   slog.LevelDebug,
		"DEBUG":   slog.LevelDebug,
		" info ":  slog.LevelInfo,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
		"bogus":   slog.LevelInfo,
	}
	for in, want := range tests {
		if got := parseLevel(in); got != want {
			t.Errorf("parseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestDebugLogGoesToStderrWhenEnabled(t *testing.T) {
	t.Setenv("LOG_LEVEL", "debug")
	var stdout, stderr bytes.Buffer
	if got := Run([]string{"version"}, "x", &stdout, &stderr); got != 0 {
		t.Fatalf("exit = %d", got)
	}
	if !strings.Contains(stderr.String(), "cli dispatch") {
		t.Errorf("expected debug dispatch line in stderr, got %q", stderr.String())
	}
}
