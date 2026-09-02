package cli

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// startupTimeout bounds the whole [[startup]] hook so Herdr's launch is
// never held up by a slow daemon.
const startupTimeout = 5 * time.Second

// runStartup is the [[startup]] hook: ensure the daemon is running when
// the plugin is configured, otherwise remind the user to run setup.
func runStartup(rc *runContext, _ []string) int {
	env, err := wire.env()
	if err != nil {
		fmt.Fprintf(rc.stderr, "herdr-tg startup: %v\n", err)
		return exitError
	}
	ctx, cancel := context.WithTimeout(context.Background(), startupTimeout)
	defer cancel()

	if _, err := wire.loadConfig(ctx, env, rc.log); err != nil {
		if isNotConfigured(err) {
			rc.log.Info("startup: not configured", slog.String("err", err.Error()))
			fmt.Fprintln(rc.stdout, "not configured; run the setup action")
			notify(ctx, env, "Run the setup action to connect a Telegram group", rc.log)
			return exitOK
		}
		fmt.Fprintf(rc.stderr, "herdr-tg startup: %v\n", err)
		return exitError
	}

	pid, already, err := wire.buildSupervisor(env, rc.log).Start(ctx)
	if err != nil {
		fmt.Fprintf(rc.stderr, "herdr-tg startup: %v\n", err)
		notify(ctx, env, "Telegram Agents daemon failed to start: "+err.Error(), rc.log)
		return exitError
	}
	if already {
		fmt.Fprintf(rc.stdout, "daemon already running (pid %d)\n", pid)
	} else {
		fmt.Fprintf(rc.stdout, "daemon started (pid %d)\n", pid)
	}
	rc.log.Info("startup done", slog.Int("pid", pid), slog.Bool("already_running", already))
	return exitOK
}
