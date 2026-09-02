package logging_test

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/adapters/logging"
)

func readOr(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRotatingWriterRotatesAndKeeps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.log")
	w, err := logging.NewRotatingWriter(path, 10, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	lines := []string{"aaaa\n", "bbbb\n", "cccc\n", "dddd\n", "eeee\n"}
	for _, l := range lines {
		if _, err := w.Write([]byte(l)); err != nil {
			t.Fatal(err)
		}
	}
	// Limit 10: "aaaa\nbbbb\n" fills the file; "cccc\n" rotates; "dddd\n"
	// joins it; "eeee\n" rotates again. So: live=eeee, .1=cccc+dddd, .2=aaaa+bbbb.
	if got := readOr(t, path); got != "eeee\n" {
		t.Fatalf("live = %q", got)
	}
	if got := readOr(t, path+".1"); got != "cccc\ndddd\n" {
		t.Fatalf(".1 = %q", got)
	}
	if got := readOr(t, path+".2"); got != "aaaa\nbbbb\n" {
		t.Fatalf(".2 = %q", got)
	}
	for _, l := range []string{"ffff\n", "gggg\n", "hhhh\n"} {
		if _, err := w.Write([]byte(l)); err != nil {
			t.Fatal(err)
		}
	}
	if got := readOr(t, path+".2"); got != "cccc\ndddd\n" {
		t.Fatalf("after third rotation .2 = %q", got)
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf(".3 should not exist: %v", err)
	}
}

func TestRotatingWriterPrimesCounterFromExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.log")
	if err := os.WriteFile(path, []byte("12345678\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := logging.NewRotatingWriter(path, 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("xyz\n")); err != nil {
		t.Fatal(err)
	}
	if got := readOr(t, path); got != "xyz\n" {
		t.Fatalf("live = %q, want rotation before the write", got)
	}
	if got := readOr(t, path+".1"); got != "12345678\n" {
		t.Fatalf(".1 = %q", got)
	}
}

func TestRotatingWriterOversizedRecordStillWritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.log")
	w, err := logging.NewRotatingWriter(path, 4, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Write([]byte("0123456789\n")); err != nil {
		t.Fatal(err)
	}
	if got := readOr(t, path); got != "0123456789\n" {
		t.Fatalf("live = %q", got)
	}
}

func TestNewFileLoggerWritesJSON(t *testing.T) {
	dir := t.TempDir()
	log, closer, err := logging.NewFileLogger(dir, slog.LevelDebug)
	if err != nil {
		t.Fatal(err)
	}
	log.Debug("hello", slog.String("k", "v"))
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	got := readOr(t, filepath.Join(dir, logging.LogFileName))
	if !strings.Contains(got, `"msg":"log opened"`) || !strings.Contains(got, `"k":"v"`) {
		t.Fatalf("log content:\n%s", got)
	}
}

func TestParseAndResolveLevel(t *testing.T) {
	tests := []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug}, {" DEBUG ", slog.LevelDebug}, {"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn}, {"error", slog.LevelError}, {"", slog.LevelInfo}, {"nope", slog.LevelInfo},
	}
	for _, tt := range tests {
		if got := logging.ParseLevel(tt.in); got != tt.want {
			t.Fatalf("ParseLevel(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
	if got := logging.ResolveLevel("warn", "debug"); got != slog.LevelWarn {
		t.Fatalf("env should win: %v", got)
	}
	if got := logging.ResolveLevel("", "debug"); got != slog.LevelDebug {
		t.Fatalf("config fallback: %v", got)
	}
	if got := logging.ResolveLevel("", ""); got != slog.LevelInfo {
		t.Fatalf("default: %v", got)
	}
}
