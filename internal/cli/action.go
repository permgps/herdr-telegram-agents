package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// actionTimeout bounds one action: restart may wait 10 s for a stop and
// 5 s for the new daemon to claim the pid file.
const actionTimeout = 20 * time.Second

// actionIDs lists the [[actions]] entrypoints, in manifest order.
var actionIDs = []string{"setup", "start", "stop", "restart", "status", "resync", "logs"}

// runAction handles `action <id>`. Every outcome goes to stdout and, best
// effort, to a Herdr notification because actions run without a visible
// terminal.
func runAction(rc *runContext, args []string) int {
	if len(args) != 1 || !isAction(args[0]) {
		fmt.Fprintf(rc.stderr, "usage: herdr-tg action %s\n", strings.Join(actionIDs, "|"))
		return exitUsage
	}
	id := args[0]
	env, err := wire.env()
	if err != nil {
		fmt.Fprintf(rc.stderr, "herdr-tg action %s: %v\n", id, err)
		return exitError
	}
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	defer cancel()

	msg, err := doAction(ctx, rc, env.PluginID, id, wire.buildSupervisor(env, rc.log), wire.paneOpener(env, rc.log))
	if err != nil {
		if errors.Is(err, domain.ErrUnsupportedPlatform) {
			msg = fmt.Sprintf("%s is not available on this platform", id)
		} else if errors.Is(err, domain.ErrControlUnavailable) {
			msg = fmt.Sprintf("%s failed: the daemon is not listening on its control channel; restart it", id)
		} else {
			msg = fmt.Sprintf("%s failed: %v", id, err)
		}
		rc.log.Warn("action failed", slog.String("id", id), slog.String("err", err.Error()))
		fmt.Fprintln(rc.stderr, "herdr-tg action: "+msg)
		notify(ctx, env, msg, rc.log)
		return exitError
	}
	rc.log.Info("action done", slog.String("id", id), slog.String("result", msg))
	fmt.Fprintln(rc.stdout, msg)
	notify(ctx, env, msg, rc.log)
	return exitOK
}

// doAction performs one action and returns the human-readable outcome.
func doAction(ctx context.Context, rc *runContext, pluginID, id string, sup supervisor, panes domain.PaneOpener) (string, error) {
	switch id {
	case "setup", "logs":
		if err := panes.OpenPane(ctx, pluginID, id); err != nil {
			return "", err
		}
		return fmt.Sprintf("opened the %s pane", id), nil
	case "start":
		pid, already, err := sup.Start(ctx)
		if err != nil {
			return "", err
		}
		if already {
			return fmt.Sprintf("daemon already running (pid %d)", pid), nil
		}
		return fmt.Sprintf("daemon started (pid %d)", pid), nil
	case "stop":
		if err := sup.Stop(ctx); err != nil {
			if errors.Is(err, domain.ErrNotRunning) {
				return "daemon is not running", nil
			}
			return "", err
		}
		return "daemon stopped", nil
	case "restart":
		pid, err := sup.Restart(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("daemon restarted (pid %d)", pid), nil
	case "status":
		return "daemon " + sup.Describe(ctx), nil
	case "resync":
		if err := sup.Resync(); err != nil {
			if errors.Is(err, domain.ErrNotRunning) {
				return "daemon is not running; start it first", nil
			}
			return "", err
		}
		return "resync requested", nil
	}
	return "", fmt.Errorf("unknown action %q", id)
}

func isAction(id string) bool {
	for _, a := range actionIDs {
		if a == id {
			return true
		}
	}
	return false
}
