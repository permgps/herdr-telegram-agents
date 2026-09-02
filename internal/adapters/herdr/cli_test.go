package herdr

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// fakeHerdrBin writes a shell script that records its argv and exits with
// the given code, printing message to stderr when non-empty.
func fakeHerdrBin(t *testing.T, exit int, message string) (bin, argvFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake needs a POSIX shell")
	}
	dir := t.TempDir()
	argvFile = filepath.Join(dir, "argv")
	bin = filepath.Join(dir, "herdr")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argvFile + "\n"
	if message != "" {
		script += "echo '" + message + "' >&2\n"
	}
	script += "exit " + strconv.Itoa(exit) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argvFile
}

func TestCLIOpenPane(t *testing.T) {
	bin, argvFile := fakeHerdrBin(t, 0, "")
	c := NewCLI(bin, nil)
	if err := c.OpenPane(context.Background(), "permgps.telegram-agents", "setup"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatal(err)
	}
	want := "plugin\npane\nopen\n--plugin\npermgps.telegram-agents\n--entrypoint\nsetup\n"
	if string(got) != want {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

func TestCLIOpenPaneFailure(t *testing.T) {
	bin, _ := fakeHerdrBin(t, 1, "unknown entrypoint")
	c := NewCLI(bin, nil)
	err := c.OpenPane(context.Background(), "x", "nope")
	if err == nil || !strings.Contains(err.Error(), "unknown entrypoint") || !strings.Contains(err.Error(), "pane open") {
		t.Fatalf("err = %v", err)
	}
	c = NewCLI(filepath.Join(t.TempDir(), "missing"), nil)
	if err := c.OpenPane(context.Background(), "x", "setup"); err == nil {
		t.Fatal("missing binary should fail")
	}
}
