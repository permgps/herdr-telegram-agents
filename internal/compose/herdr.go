// Package compose wires adapters into the ports the application and the
// CLI depend on. It is the only package that knows every concrete adapter,
// so the CLI can stay free of adapter imports.
package compose

import (
	"context"
	"log/slog"

	"github.com/permgps/herdr-telegram-agents/internal/adapters/herdr"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// HerdrGateway is the Herdr port together with its lifecycle.
type HerdrGateway interface {
	domain.HerdrGateway
	// Start connects to the socket and launches the event stream.
	Start(ctx context.Context) error
	// Close stops the stream and drops the connections.
	Close() error
}

// NewHerdrGateway builds the Herdr adapter for the socket at path with the
// daemon's default reconnect schedule.
func NewHerdrGateway(path string, log *slog.Logger) HerdrGateway {
	return herdr.NewGateway(path, log, herdr.DefaultBackoff)
}
