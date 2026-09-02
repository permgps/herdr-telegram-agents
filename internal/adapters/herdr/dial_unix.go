//go:build !windows

package herdr

import (
	"context"
	"net"
)

// dialFunc opens one raw connection to the Herdr server at path.
type dialFunc func(ctx context.Context, path string) (net.Conn, error)

// dial connects to the Herdr Unix domain socket.
func dial(ctx context.Context, path string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", path)
}
