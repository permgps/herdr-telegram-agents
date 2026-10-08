package system

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeOpenCode writes a shell stand-in for the opencode binary.
func fakeOpenCode(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in is Unix only")
	}
	script := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestOpenCodeExporterRunsExport(t *testing.T) {
	e := NewOpenCodeExporter(t.TempDir(), nil)
	e.bin = fakeOpenCode(t, `if [ "$1" = --version ]; then echo 'opencode 1.25.0'; exit 0; fi
[ "$1" = export ] && [ "$2" = ses_abc ] && [ $# -eq 2 ] || exit 3
echo '{"messages":[]}'
`)
	out, err := e.Export(context.Background(), "ses_abc")
	if err != nil || strings.TrimSpace(string(out)) != `{"messages":[]}` {
		t.Fatalf("Export = %q, %v", out, err)
	}
}

func TestOpenCodeExporterRunsV2SessionExport(t *testing.T) {
	e := NewOpenCodeExporter(t.TempDir(), nil)
	e.bin = fakeOpenCode(t, `[ "$1" = session ] && [ "$2" = export ] && [ "$3" = ses_abc ] && [ $# -eq 3 ] || exit 3
echo '{"messages":[]}'
`)
	out, err := e.Export(context.Background(), "ses_abc")
	if err != nil || strings.TrimSpace(string(out)) != `{"messages":[]}` {
		t.Fatalf("Export = %q, %v", out, err)
	}
}

func TestOpenCodeExporterV2FailureDoesNotStartLegacyCommand(t *testing.T) {
	e := NewOpenCodeExporter(t.TempDir(), nil)
	e.bin = fakeOpenCode(t, `if [ "$1" = session ] && [ "$2" = export ]; then exit 4; fi
if [ "$1" = --version ]; then echo 'opencode v2.0.18'; exit 0; fi
echo 'unexpected legacy command'
exit 0
`)
	out, err := e.Export(context.Background(), "ses_abc")
	if out != nil || err == nil || !strings.Contains(err.Error(), "exit code 4") {
		t.Fatalf("v2 failure = %q, %v", out, err)
	}
}

func TestOpenCodeExporterDiscardsStderr(t *testing.T) {
	for _, exit := range []string{"0", "1"} {
		t.Run(exit, func(t *testing.T) {
			var logs bytes.Buffer
			e := NewOpenCodeExporter(t.TempDir(), slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
			e.bin = fakeOpenCode(t, "printf '{\"messages\":[]}'\nprintf 'ses_private secret_private' >&2\nhead -c 1048577 /dev/zero >&2\nexit "+exit+"\n")
			out, err := e.Export(context.Background(), "ses_abc")
			if exit == "0" && (err != nil || string(out) != `{"messages":[]}`) {
				t.Fatalf("success = %q, %v", out, err)
			}
			if exit == "1" && (err == nil || !strings.Contains(err.Error(), "exit code 1") || out != nil) {
				t.Fatalf("failure = %q, %v", out, err)
			}
			for _, secret := range []string{"ses_private", "secret_private"} {
				if strings.Contains(logs.String(), secret) || err != nil && strings.Contains(err.Error(), secret) {
					t.Fatalf("secret leaked: %s", secret)
				}
			}
		})
	}
}

func TestOpenCodeExporterCapIsAnError(t *testing.T) {
	e := NewOpenCodeExporter(t.TempDir(), nil)
	e.bin = fakeOpenCode(t, "i=0; while [ $i -lt 100 ]; do echo 'padding padding padding'; i=$((i+1)); done\n")
	e.maxBytes = 64
	if _, err := e.Export(context.Background(), "ses_abc"); err == nil || !strings.Contains(err.Error(), "output over") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenCodeExporterExactCap(t *testing.T) {
	e := NewOpenCodeExporter(t.TempDir(), nil)
	e.bin = fakeOpenCode(t, "printf 12345678\n")
	e.maxBytes = 8
	if out, err := e.Export(context.Background(), "ses_abc"); err != nil || string(out) != "12345678" {
		t.Fatalf("at cap = %q, %v", out, err)
	}
	e.bin = fakeOpenCode(t, "printf 123456789\n")
	if out, err := e.Export(context.Background(), "ses_abc"); err == nil || out != nil {
		t.Fatalf("over cap = %q, %v", out, err)
	}
}

// TestOpenCodeExporterKillsAtCap: a child that never stops writing is
// killed once its file is over the cap, long before the timeout, and the
// file is removed.
func TestOpenCodeExporterKillsAtCap(t *testing.T) {
	var logs bytes.Buffer
	state := t.TempDir()
	e := NewOpenCodeExporter(state, slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	e.bin = fakeOpenCode(t, "while :; do printf 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'; done\n")
	e.maxBytes = 64 << 10
	e.poll = 10 * time.Millisecond
	start := time.Now()
	out, err := e.Export(context.Background(), "ses_abc")
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("Export took %v, want the watcher to kill the child", took)
	}
	if out != nil || err == nil || !strings.Contains(err.Error(), "output over") {
		t.Fatalf("Export = %q, %v; want the cap error", out, err)
	}
	if !strings.Contains(logs.String(), `"killed_at_cap":true`) || !strings.Contains(logs.String(), `"category":"output_cap"`) {
		t.Fatalf("logs = %s", logs.String())
	}
	if left, _ := filepath.Glob(filepath.Join(state, "tmp", "*")); len(left) != 0 {
		t.Fatalf("files left behind: %v", left)
	}
}

func TestOpenCodeExporterContext(t *testing.T) {
	e := NewOpenCodeExporter(t.TempDir(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Export(ctx, "ses_abc"); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled err = %v", err)
	}
	e.bin = fakeOpenCode(t, "exit 0\n")
	e.run = func(*exec.Cmd) error { cancel(); return context.Canceled }
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	if _, err := e.Export(ctx, "ses_abc"); !errors.Is(err, context.Canceled) {
		t.Fatalf("during-run err = %v", err)
	}
	deadlineCtx, deadlineCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer deadlineCancel()
	if _, err := e.Export(deadlineCtx, "ses_abc"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline err = %v", err)
	}
}

func TestOpenCodeExporterChildTimeoutKeepsParentAlive(t *testing.T) {
	var logs bytes.Buffer
	e := NewOpenCodeExporter(t.TempDir(), slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	e.bin = fakeOpenCode(t, "exit 0\n")
	e.timeout = -time.Second
	e.run = func(cmd *exec.Cmd) error { return cmd.Run() }
	parent := context.Background()
	out, err := e.Export(parent, "ses_private")
	if out != nil || err == nil || errors.Is(err, context.DeadlineExceeded) || parent.Err() != nil {
		t.Fatalf("child timeout = %q, %v; parent = %v", out, err, parent.Err())
	}
	if !strings.Contains(err.Error(), "timed out") || strings.Contains(err.Error(), "ses_private") ||
		!strings.Contains(logs.String(), `"category":"export_timeout"`) || strings.Contains(logs.String(), "ses_private") {
		t.Fatalf("unsafe timeout result: %v; logs = %s", err, logs.String())
	}
}

func TestOpenCodeExporterMissingBinary(t *testing.T) {
	e := NewOpenCodeExporter(t.TempDir(), nil)
	e.bin = "opencode-definitely-missing-binary"
	if _, err := e.Export(context.Background(), "ses_abc"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenCodeExporterRejectsBadSessionID(t *testing.T) {
	e := NewOpenCodeExporter(t.TempDir(), nil)
	e.bin = "opencode-definitely-missing-binary" // never reached
	for _, id := range []string{"", "--help", "-x"} {
		if _, err := e.Export(context.Background(), id); err == nil || !strings.Contains(err.Error(), "invalid session id") {
			t.Fatalf("Export(%q) err = %v", id, err)
		}
	}
}

// TestOpenCodeExporterUsesAPrivateFile: opencode cuts piped stdout at
// 64 KiB multiples and still exits 0, so the child writes into a 0600
// file under <state>/tmp that is gone once Export returns.
func TestOpenCodeExporterUsesAPrivateFile(t *testing.T) {
	state := t.TempDir()
	e := NewOpenCodeExporter(state, nil)
	e.bin = fakeOpenCode(t, `printf '{"messages":[]}'`)
	var name string
	e.run = func(cmd *exec.Cmd) error {
		f, ok := cmd.Stdout.(*os.File)
		if !ok {
			t.Fatalf("stdout is %T, want *os.File: opencode truncates piped output", cmd.Stdout)
		}
		name = f.Name()
		if filepath.Dir(name) != filepath.Join(state, "tmp") {
			t.Fatalf("export file %s is outside %s", name, filepath.Join(state, "tmp"))
		}
		if info, err := f.Stat(); err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("export file mode = %v, %v; want 0600", info.Mode().Perm(), err)
		}
		return cmd.Run()
	}
	out, err := e.Export(context.Background(), "ses_abc")
	if err != nil || string(out) != `{"messages":[]}` {
		t.Fatalf("Export = %q, %v", out, err)
	}
	if _, statErr := os.Stat(name); !os.IsNotExist(statErr) {
		t.Fatalf("export file left behind: %v", statErr)
	}
}

// TestOpenCodeExporterReadsPast64KiB: an export larger than a pipe buffer
// comes back whole.
func TestOpenCodeExporterReadsPast64KiB(t *testing.T) {
	e := NewOpenCodeExporter(t.TempDir(), nil)
	e.bin = fakeOpenCode(t, `printf '{"text":"'; head -c 200000 /dev/zero | tr '\0' a; printf '"}'`)
	out, err := e.Export(context.Background(), "ses_abc")
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if want := 200000 + len(`{"text":""}`); len(out) != want {
		t.Fatalf("len(out) = %d, want %d", len(out), want)
	}
}

// TestOpenCodeExporterRemovesFileOnFailure: the export file is removed on
// a non-zero exit, over the cap and on a timeout too.
func TestOpenCodeExporterRemovesFileOnFailure(t *testing.T) {
	for name, tc := range map[string]struct {
		body    string
		max     int
		timeout time.Duration
	}{
		"exit":    {body: "printf partial\nexit 2\n"},
		"cap":     {body: "printf 123456789\n", max: 8},
		"timeout": {body: "exit 0\n", timeout: -time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			state := t.TempDir()
			e := NewOpenCodeExporter(state, nil)
			e.bin = fakeOpenCode(t, tc.body)
			if tc.max > 0 {
				e.maxBytes = tc.max
			}
			if tc.timeout != 0 {
				e.timeout = tc.timeout
			}
			if out, err := e.Export(context.Background(), "ses_abc"); err == nil || out != nil {
				t.Fatalf("Export = %q, %v; want an error", out, err)
			}
			left, _ := filepath.Glob(filepath.Join(state, "tmp", "*"))
			if len(left) != 0 {
				t.Fatalf("files left behind: %v", left)
			}
		})
	}
}

// TestOpenCodeExporterTempDirFailure: an export file that cannot be
// created is an error, without running opencode.
func TestOpenCodeExporterTempDirFailure(t *testing.T) {
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(state, "tmp"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	e := NewOpenCodeExporter(state, nil)
	e.bin = fakeOpenCode(t, "exit 0\n")
	ran := false
	e.run = func(cmd *exec.Cmd) error { ran = true; return cmd.Run() }
	if out, err := e.Export(context.Background(), "ses_abc"); err == nil || out != nil || ran || !strings.Contains(err.Error(), "temp file") {
		t.Fatalf("Export = %q, %v, ran = %v", out, err, ran)
	}
}

func TestSweepOpenCodeExports(t *testing.T) {
	state := t.TempDir()
	if got := SweepOpenCodeExports(state, nil); got != 0 {
		t.Fatalf("missing dir sweep = %d", got)
	}
	dir := filepath.Join(state, "tmp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"opencode-export-1.json", "opencode-export-2.json", "keep.json", "opencode-export-3.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var logs bytes.Buffer
	if got := SweepOpenCodeExports(state, slog.New(slog.NewJSONHandler(&logs, nil))); got != 2 {
		t.Fatalf("sweep removed %d, want 2", got)
	}
	left, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(left) != 2 || !strings.Contains(logs.String(), `"count":2`) {
		t.Fatalf("left = %v, logs = %s", left, logs.String())
	}
}
