package system

import (
	"context"
	"errors"
	"runtime"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// TestIdleSourceOnHost samples the real source once. macOS and Windows have
// one; a build agent without an input session may fail the call, which is
// allowed as long as the failure is not "unsupported". Everything else must
// answer ErrIdleUnsupported.
func TestIdleSourceOnHost(t *testing.T) {
	src := NewIdleSource(nil)
	d, err := src.Idle(context.Background())
	switch runtime.GOOS {
	case "darwin", "windows":
		if errors.Is(err, domain.ErrIdleUnsupported) {
			t.Fatalf("%s reports unsupported", runtime.GOOS)
		}
		if err != nil {
			t.Logf("idle sample failed on this host (tolerated): %v", err)
			return
		}
		if d < 0 {
			t.Errorf("negative idle %v", d)
		}
	default:
		if !errors.Is(err, domain.ErrIdleUnsupported) {
			t.Fatalf("Idle = %v, %v; want ErrIdleUnsupported", d, err)
		}
	}
}
