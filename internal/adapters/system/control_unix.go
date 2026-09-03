//go:build !windows

package system

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
)

// ControlPath is the daemon's control socket inside the state directory.
func ControlPath(stateDir string) string {
	return filepath.Join(stateDir, ControlFileName)
}

// ListenControl opens the control socket, replacing a socket file left
// behind by a daemon that died without cleaning up. A socket that still
// answers belongs to a live daemon and is never removed.
func ListenControl(stateDir string, log *slog.Logger) (net.Listener, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir state dir: %w", err)
	}
	path := ControlPath(stateDir)
	ln, err := net.Listen("unix", path)
	if err == nil {
		_ = os.Chmod(path, 0o600)
		return ln, nil
	}
	if _, statErr := os.Stat(path); statErr != nil {
		return nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	if conn, dialErr := net.DialTimeout("unix", path, controlTimeout); dialErr == nil {
		_ = conn.Close()
		return nil, fmt.Errorf("another daemon listens on %s", path)
	}
	log.Warn("removing a stale control socket", slog.String("path", path))
	if err := os.Remove(path); err != nil {
		return nil, fmt.Errorf("remove stale %s: %w", path, err)
	}
	ln, err = net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	_ = os.Chmod(path, 0o600)
	return ln, nil
}

// dialControl connects to the daemon's control socket.
func dialControl(ctx context.Context, path string) (net.Conn, error) {
	d := net.Dialer{Timeout: controlTimeout}
	dialCtx, cancel := context.WithTimeout(ctx, controlTimeout)
	defer cancel()
	return d.DialContext(dialCtx, "unix", path)
}
