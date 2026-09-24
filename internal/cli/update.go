package cli

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/compose"
)

// runUpdateWorker runs only from a detached copy under STATE_DIR. It never
// polls Telegram; the old daemon exits before the replacement starts.
func runUpdateWorker(rc *runContext, args []string) int {
	if len(args) != 1 || args[0] == "" {
		fmt.Fprintln(rc.stderr, "usage: herdr-tg update-worker <job-id>")
		return exitUsage
	}
	env, err := wire.env()
	if err != nil {
		fmt.Fprintf(rc.stderr, "update worker: %v\n", err)
		return exitError
	}
	log, closer, err := wire.fileLogger(env, "")
	if err != nil {
		fmt.Fprintf(rc.stderr, "update worker log: %v\n", err)
		return exitError
	}
	defer closer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	job, err := compose.BuildUpdateWorker(env, log).Run(ctx, args[0])
	if cfg, cfgErr := wire.loadConfig(context.Background(), env, log); cfgErr == nil {
		resultCtx, cancelResult := context.WithTimeout(context.Background(), 10*time.Second)
		if notifyErr := compose.DeliverUpdateResult(resultCtx, env, cfg, log); notifyErr != nil {
			log.Warn("update result pending delivery", slog.String("job", args[0]), slog.Any("err", notifyErr))
		}
		cancelResult()
	}
	if err != nil {
		log.Error("update worker exited", slog.String("job", args[0]), slog.String("phase", job.Phase), slog.Any("err", err))
		return exitError
	}
	log.Info("update worker finished", slog.String("job", job.ID), slog.String("phase", job.Phase))
	return exitOK
}
