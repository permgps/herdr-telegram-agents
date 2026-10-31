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
	// Ping checks the socket without starting the event stream.
	domain.HerdrProber
	// Start connects to the socket and launches the event stream.
	Start(ctx context.Context) error
	// Close stops the stream and drops the connections.
	Close() error
	// AgentSession returns the transient session tuple Herdr reports for a
	// pane, for a reply source that needs the value for one lookup.
	AgentSession(ctx context.Context, paneID string) (domain.SessionTuple, error)
	// AgentSessions returns the transient session tuple of every pane that
	// has one, for the Claude reader to tell apart panes in one directory.
	AgentSessions(ctx context.Context) (map[string]domain.SessionTuple, error)
	// PaneProcesses returns the pids in the foreground of a pane, for a
	// reply source that ties a pane to an agent session by process.
	PaneProcesses(ctx context.Context, paneID string) ([]int, error)
}

// NewHerdrGateway builds the Herdr adapter for the socket at path with the
// daemon's default reconnect schedule.
func NewHerdrGateway(path string, log *slog.Logger) HerdrGateway {
	return herdr.NewGateway(path, log, herdr.DefaultBackoff)
}
