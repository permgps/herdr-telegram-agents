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
	tests := []struct {
		name, root, want string
	}{
		{"with plugin root", "/plugins/tg", "plugin\npane\nopen\n--plugin\npermgps.telegram-agents\n--entrypoint\nsetup\n--env\nPATH=/plugins/tg" + string(os.PathListSeparator) + "/usr/bin\n"},
		{"without root", "", "plugin\npane\nopen\n--plugin\npermgps.telegram-agents\n--entrypoint\nsetup\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bin, argvFile := fakeHerdrBin(t, 0, "")
			c := NewCLI(bin, tt.root, "/usr/bin", nil)
			if err := c.OpenPane(context.Background(), "permgps.telegram-agents", "setup"); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(argvFile)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Fatalf("argv = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPanePath(t *testing.T) {
	sep := string(os.PathListSeparator)
	if got := panePath("/r", ""); got != "/r" {
		t.Fatalf("panePath empty = %q", got)
	}
	if got := panePath("/r", "/a"+sep+"/b"); got != "/r"+sep+"/a"+sep+"/b" {
		t.Fatalf("panePath = %q", got)
	}
}

func TestCLIOpenPaneFailure(t *testing.T) {
	bin, _ := fakeHerdrBin(t, 1, "unknown entrypoint")
	c := NewCLI(bin, "", "", nil)
	err := c.OpenPane(context.Background(), "x", "nope")
	if err == nil || !strings.Contains(err.Error(), "unknown entrypoint") || !strings.Contains(err.Error(), "pane open") {
		t.Fatalf("err = %v", err)
	}
	c = NewCLI(filepath.Join(t.TempDir(), "missing"), "", "", nil)
	if err := c.OpenPane(context.Background(), "x", "setup"); err == nil {
		t.Fatal("missing binary should fail")
	}
}
