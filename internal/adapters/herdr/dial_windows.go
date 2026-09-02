//go:build windows

package herdr

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
)

// dialFunc opens one raw connection to the Herdr server at path.
type dialFunc func(ctx context.Context, path string) (net.Conn, error)

// dial connects to the Herdr named pipe.
func dial(ctx context.Context, path string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, path)
}
