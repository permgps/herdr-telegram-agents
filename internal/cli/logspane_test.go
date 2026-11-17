package cli

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
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

// TestRenderLogLineNeutralizesControls: slog JSON escapes ESC as \u001b and
// decoding turns it back into a real ESC. Strangers' Telegram names are
// logged at Info, so a name like "x\x1b]0;pwned\x07\x1b[2J" must not reach
// the terminal as escape sequences (title change, screen clear), in the
// message, in values, in keys or in a line that is not JSON.
func TestRenderLogLineNeutralizesControls(t *testing.T) {
	evil := "x\x1b]0;pwned\x07\x1b[2J\u009b31m\x7f\u202eevil\u2066"
	var buf strings.Builder
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	log.Info("stranger "+evil, slog.String("name", evil), slog.String("plain", "ok"), slog.Any("nested", map[string]string{"k": evil}), slog.String("key"+evil, "v"))
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	lines = append(lines, "raw "+evil)
	for _, line := range lines {
		got := renderLogLine(line)
		for _, r := range got {
			if unicode.IsControl(r) || r == '\u202e' || r == '\u2066' {
				t.Fatalf("rendered line holds %U: %q", r, got)
			}
		}
		if !strings.Contains(got, "pwned") {
			t.Fatalf("rendered line lost the text: %q", got)
		}
	}
	if got := renderLogLine(lines[0]); !strings.Contains(got, "plain=ok") {
		t.Fatalf("safe value changed: %q", got)
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

func TestTailerPreservesWritesDuringInitialDisplay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	if err := os.WriteFile(path, []byte("ready\npartial"), 0o644); err != nil {
		t.Fatal(err)
	}

	buffer := &syncBuffer{}
	out := &appendDuringInitialDisplay{
		buffer: buffer,
		path:   path,
		data:   []byte("-complete\nduring\n"),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- newTailer(path, 10*time.Millisecond).run(ctx, out, 100) }()

	waitOutput(t, buffer, "during")
	waitOutput(t, buffer, "partial-complete")
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if out.appendErr != nil {
		t.Fatalf("append during initial display: %v", out.appendErr)
	}

	lines := strings.Split(buffer.String(), "\n")
	counts := map[string]int{}
	for _, line := range lines {
		counts[line]++
	}
	if counts["during"] != 1 || counts["partial-complete"] != 1 || counts["partial"] != 0 {
		t.Fatalf("initial and appended log lines = %q", buffer.String())
	}
}

type appendDuringInitialDisplay struct {
	buffer    *syncBuffer
	path      string
	data      []byte
	appended  bool
	appendErr error
}

func (w *appendDuringInitialDisplay) Write(p []byte) (int, error) {
	if !w.appended {
		w.appended = true
		f, err := os.OpenFile(w.path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			w.appendErr = err
		} else {
			_, w.appendErr = f.Write(w.data)
			if err := f.Close(); w.appendErr == nil {
				w.appendErr = err
			}
		}
		if w.appendErr != nil {
			return 0, w.appendErr
		}
	}
	return w.buffer.Write(p)
}

func TestWatchQuitKeys(t *testing.T) {
	for _, in := range []string{"q", "\x1b", "abc\x1b[A", "", "\x03", "\x04", "abc"} {
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

// TestWatchQuitKeysIgnoresOtherKeys: other keys leave the pane open while
// stdin stays open.
func TestWatchQuitKeysIgnoresOtherKeys(t *testing.T) {
	r, w := io.Pipe()
	defer w.Close()
	fired := make(chan struct{})
	go watchQuitKeys(r, func() { close(fired) })
	if _, err := w.Write([]byte("abc\r\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fired:
		t.Fatal("cancelled on keys that are not quit keys")
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := w.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fired:
	case <-time.After(time.Second):
		t.Fatal("q after other keys did not cancel")
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
