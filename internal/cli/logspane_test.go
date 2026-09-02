package cli

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRenderLogLine(t *testing.T) {
	tests := []struct{ in, want string }{
		{`{"time":"2026-09-02T10:20:30.123Z","level":"INFO","msg":"daemon starting","pid":42,"state_dir":"/tmp/s t"}`,
			time.Date(2026, 9, 2, 10, 20, 30, 0, time.UTC).Local().Format("15:04:05") + ` INFO daemon starting pid=42 state_dir="/tmp/s t"`},
		{`{"level":"WARN","msg":"x","ratio":0.5,"ok":true,"list":[1,2],"n":null}`, `WARN x list=[1,2] n=null ok=true ratio=0.5`},
		{`plain text line`, `plain text line`},
		{`{not json`, `{not json`},
		{``, ``},
	}
	for _, tt := range tests {
		if got := renderLogLine(tt.in); got != tt.want {
			t.Errorf("renderLogLine(%q)\n got %q\nwant %q", tt.in, got, tt.want)
		}
	}
}

func TestTailerFollowsAndSurvivesRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	var lines []string
	for i := 1; i <= 120; i++ {
		lines = append(lines, `{"level":"INFO","msg":"line `+strconv.Itoa(i)+`"}`)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out syncBuffer
	done := make(chan error, 1)
	tl := newTailer(path, 10*time.Millisecond)
	go func() { done <- tl.run(ctx, &out, 100) }()

	waitOutput(t, &out, "INFO line 120")
	if strings.Contains(out.String(), "INFO line 20\n") || !strings.Contains(out.String(), "INFO line 21\n") {
		t.Fatalf("tail did not keep exactly the last 100 lines:\n%s", out.String())
	}

	// Append: partial writes are joined before rendering.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"level":"INFO","msg":"appen`)
	time.Sleep(30 * time.Millisecond)
	_, _ = f.WriteString(`ded","k":"v"}` + "\n")
	_ = f.Close()
	waitOutput(t, &out, "INFO appended k=v")

	// Rotate: move the file aside and start a fresh one.
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"level":"INFO","msg":"after rotation"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitOutput(t, &out, "INFO after rotation")

	// Truncate in place: reopen from the start.
	if err := os.WriteFile(path, []byte("raw\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitOutput(t, &out, "raw\n")

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tailer did not stop")
	}
}

func TestTailerWaitsForMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out syncBuffer
	done := make(chan error, 1)
	go func() { done <- newTailer(path, 10*time.Millisecond).run(ctx, &out, 100) }()
	waitOutput(t, &out, "(no log file yet)")
	if err := os.WriteFile(path, []byte(`{"level":"INFO","msg":"born"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitOutput(t, &out, "INFO born")
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWatchQuitKeys(t *testing.T) {
	for _, in := range []string{"q", "\x1b", "abc\x1b[A", ""} {
		_, cancel := context.WithCancel(context.Background())
		fired := make(chan struct{})
		go watchQuitKeys(strings.NewReader(in), func() { cancel(); close(fired) })
		select {
		case <-fired:
		case <-time.After(time.Second):
			t.Fatalf("input %q did not cancel", in)
		}
	}
}

func TestLogsPaneOutsideHerdr(t *testing.T) {
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", "")
	t.Setenv("HERDR_PLUGIN_STATE_DIR", "")
	code, _, stderr := runCLI(t, "logs-pane")
	if code != exitError || !strings.Contains(stderr, "HERDR_PLUGIN_CONFIG_DIR") {
		t.Fatalf("exit = %d, stderr = %q", code, stderr)
	}
}
