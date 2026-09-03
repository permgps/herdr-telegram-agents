//go:build windows

package system

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"

	"github.com/Microsoft/go-winio"
)

// ControlPath is the daemon's control pipe. Windows named pipes live in a
// flat namespace, so the state directory is hashed into the name and two
// plugin installations never collide.
func ControlPath(stateDir string) string {
	sum := sha256.Sum256([]byte(stateDir))
	return `\\.\pipe\herdr-tg-` + hex.EncodeToString(sum[:8])
}

// ListenControl opens the control pipe. Windows releases the name when the
// owning process exits, so there is no stale-socket case to clean up.
func ListenControl(stateDir string, log *slog.Logger) (net.Listener, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	path := ControlPath(stateDir)
	ln, err := winio.ListenPipe(path, nil)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	return ln, nil
}

// dialControl connects to the daemon's control pipe.
func dialControl(ctx context.Context, path string) (net.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, controlTimeout)
	defer cancel()
	return winio.DialPipeContext(dialCtx, path)
}
