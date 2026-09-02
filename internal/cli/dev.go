package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/compose"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// socketPathOverride, when non-empty, replaces the environment lookup of
// the Herdr socket path. Tests point it at a fake server.
var socketPathOverride string

// devConnectTimeout bounds dialling and the initial agent.list.
const devConnectTimeout = 5 * time.Second

// runDev is the undocumented diagnostics entrypoint: `dev agents` lists the
// live agents, `dev watch` prints stream events until interrupted. Both
// only run inside Herdr (HERDR_ENV=1) because they need its socket.
func runDev(rc *runContext, args []string) int {
	if os.Getenv("HERDR_ENV") != "1" {
		fmt.Fprintln(rc.stderr, "herdr-tg dev: only available inside Herdr (HERDR_ENV=1 is not set)")
		return exitUsage
	}
	if len(args) == 0 {
		fmt.Fprintln(rc.stderr, "usage: herdr-tg dev agents|watch")
		return exitUsage
	}
	path, err := herdrSocketPath()
	if err != nil {
		fmt.Fprintf(rc.stderr, "herdr-tg dev: %v\n", err)
		return exitError
	}
	switch args[0] {
	case "agents":
		return devAgents(rc, path)
	case "watch":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return devWatch(ctx, rc, path)
	default:
		fmt.Fprintf(rc.stderr, "herdr-tg dev: unknown subcommand %q (want agents|watch)\n", args[0])
		return exitUsage
	}
}

// herdrSocketPath resolves the socket the way Herdr documents it:
// HERDR_SOCKET_PATH, else ~/.config/herdr/herdr.sock.
func herdrSocketPath() (string, error) {
	if socketPathOverride != "" {
		return socketPathOverride, nil
	}
	if p := os.Getenv("HERDR_SOCKET_PATH"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("HERDR_SOCKET_PATH unset and home dir unknown: %w", err)
	}
	return filepath.Join(home, ".config", "herdr", "herdr.sock"), nil
}

func devAgents(rc *runContext, path string) int {
	ctx, cancel := context.WithTimeout(context.Background(), devConnectTimeout)
	defer cancel()
	g, agents, code := devConnect(ctx, rc, path)
	if code != exitOK {
		return code
	}
	defer func() { _ = g.Close() }()
	for _, a := range agents {
		fmt.Fprintln(rc.stdout, formatAgent(a))
	}
	rc.log.Info("dev agents listed", slog.Int("count", len(agents)))
	return exitOK
}

func devWatch(ctx context.Context, rc *runContext, path string) int {
	connectCtx, cancel := context.WithTimeout(ctx, devConnectTimeout)
	g, agents, code := devConnect(connectCtx, rc, path)
	cancel()
	if code != exitOK {
		return code
	}
	defer func() { _ = g.Close() }()

	panes := paneIDs(agents)
	if err := g.WatchPanes(ctx, panes); err != nil {
		fmt.Fprintf(rc.stderr, "herdr-tg dev watch: %v\n", err)
		return exitError
	}
	fmt.Fprintf(rc.stdout, "watching %d agent pane(s); Ctrl-C to stop\n", len(panes))

	for {
		select {
		case <-ctx.Done():
			rc.log.Info("dev watch stopped")
			return exitOK
		case ev, ok := <-g.Events():
			if !ok {
				return exitOK
			}
			line := formatEvent(ev)
			fmt.Fprintln(rc.stdout, line)
			rc.log.Debug("dev watch event", slog.String("event", line))
			if he, isHerdr := ev.(domain.HerdrEvent); isHerdr && needsRewatch(he.Kind) {
				if err := devRewatch(ctx, g, rc); err != nil && ctx.Err() == nil {
					fmt.Fprintf(rc.stderr, "herdr-tg dev watch: refresh panes: %v\n", err)
				}
			}
		}
	}
}

// devConnect starts the gateway and lists agents; on failure it reports to
// stderr and returns the exit code.
func devConnect(ctx context.Context, rc *runContext, path string) (compose.HerdrGateway, []domain.Agent, int) {
	g := compose.NewHerdrGateway(path, rc.log)
	if err := g.Start(ctx); err != nil {
		fmt.Fprintf(rc.stderr, "herdr-tg dev: connect %s: %v\n", path, err)
		return nil, nil, exitError
	}
	rc.log.Info("dev connected", slog.String("socket", path))
	agents, err := g.ListAgents(ctx)
	if err != nil {
		_ = g.Close()
		fmt.Fprintf(rc.stderr, "herdr-tg dev: agent.list: %v\n", err)
		return nil, nil, exitError
	}
	return g, agents, exitOK
}

func devRewatch(ctx context.Context, g compose.HerdrGateway, rc *runContext) error {
	listCtx, cancel := context.WithTimeout(ctx, devConnectTimeout)
	defer cancel()
	agents, err := g.ListAgents(listCtx)
	if err != nil {
		return err
	}
	panes := paneIDs(agents)
	rc.log.Debug("dev watch refreshing panes", slog.Int("count", len(panes)))
	return g.WatchPanes(ctx, panes)
}

// needsRewatch reports whether the pane set may have changed after kind.
func needsRewatch(kind domain.HerdrEventKind) bool {
	switch kind {
	case domain.PaneAgentDetected, domain.PaneClosed, domain.PaneExited, domain.StreamReset:
		return true
	}
	return false
}

func paneIDs(agents []domain.Agent) []string {
	ids := make([]string, 0, len(agents))
	for _, a := range agents {
		ids = append(ids, a.PaneID)
	}
	return ids
}

// formatAgent renders one agent.list row:
// <pane_id> <status> <label> (<kind>, term <terminal_id>)
func formatAgent(a domain.Agent) string {
	return fmt.Sprintf("%s %s %s (%s, term %s)", a.PaneID, a.Status, a.Label(), a.Kind, a.TerminalID)
}

// formatEvent renders a domain event as one line of key=value fields,
// omitting fields the event kind does not carry.
func formatEvent(ev domain.Event) string {
	he, ok := ev.(domain.HerdrEvent)
	if !ok {
		return fmt.Sprintf("%T %+v", ev, ev)
	}
	parts := []string{string(he.Kind)}
	add := func(k, v string) {
		if v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	add("pane", he.PaneID)
	add("ws", he.WorkspaceID)
	add("tab", he.TabID)
	if he.Agent != nil {
		add("agent", he.Agent.Kind)
		add("status", string(he.Agent.Status))
		if he.Agent.Name != "" {
			add("name", he.Agent.Name)
		} else {
			add("title", he.Agent.Title)
		}
	}
	add("label", he.Label)
	if he.Released {
		add("released", "true")
	}
	if he.FinalStatus != nil {
		add("final", string(*he.FinalStatus))
	}
	return strings.Join(parts, " ")
}
