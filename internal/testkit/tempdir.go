package testkit

import (
	"os"
	"runtime"
	"testing"
)

// ShortTempDir returns a temporary directory short enough to hold a unix
// socket path. macOS caps sun_path at 104 bytes and t.TempDir() derives its
// name from the test name, which easily exceeds that. The directory is
// removed when the test ends.
func ShortTempDir(tb testing.TB) string {
	tb.Helper()
	if runtime.GOOS == "windows" {
		return tb.TempDir()
	}
	dir, err := os.MkdirTemp("/tmp", "htg")
	if err != nil {
		tb.Fatalf("temp dir: %v", err)
	}
	tb.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
