package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// errTelegramFatal is the cancellation cause set when the Telegram poller
// reports 401 or 409; the daemon exits 1 with it.
var errTelegramFatal = errors.New("telegram polling stopped: invalid token or another instance is polling")

// daemonNotifyTimeout bounds the notifications sent while exiting.
const daemonNotifyTimeout = 3 * time.Second

// runDaemon is the long-lived process spawned by startup and the start
// action. It owns the pid file for its lifetime, logs to daemon.log and
// exits 0 on a normal stop, 1 on a fatal error.
func runDaemon(rc *runContext, _ []string) int {
	env, err := wire.env()
	if err != nil {
		fmt.Fprintf(rc.stderr, "herdr-tg daemon: %v\n", err)
		return exitError
	}
	ctx := context.Background()
	cfg, err := wire.loadConfig(ctx, env, rc.log)
	if err != nil {
		fmt.Fprintf(rc.stderr, "herdr-tg daemon: %v\n", err)
		if isNotConfigured(err) {
			notify(ctx, env, "Not configured: run the setup action to connect a Telegram group", rc.log)
		}
		return exitError
	}

	log, closer, err := wire.fileLogger(env, cfg.LogLevel)
	if err != nil {
		fmt.Fprintf(rc.stderr, "herdr-tg daemon: %v\n", err)
		return exitError
	}
	defer func() { _ = closer.Close() }()

	pid := wire.pidFile(env, log)
	if err := pid.Acquire(os.Getpid()); err != nil {
		if isAlreadyRunning(err) {
			log.Info("daemon already running, exiting", slog.String("err", err.Error()))
			fmt.Fprintf(rc.stderr, "herdr-tg daemon: %v\n", err)
			return exitOK
		}
		log.Error("pid file", slog.String("err", err.Error()))
		fmt.Fprintf(rc.stderr, "herdr-tg daemon: %v\n", err)
		return exitError
	}
	defer func() { _ = pid.Release() }()
	log.Info("daemon starting", slog.String("version", rc.version), slog.Int("pid", os.Getpid()),
		slog.String("state_dir", env.StateDir))

	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	runCtx, cancel := context.WithCancelCause(sigCtx)
	defer cancel(nil)
	fatal := func() { cancel(errTelegramFatal) }

	d, runTelegram, closeAll, err := wire.buildDaemon(runCtx, env, cfg, log, fatal)
	if err != nil {
		log.Error("daemon build failed", slog.String("err", err.Error()))
		fmt.Fprintf(rc.stderr, "herdr-tg daemon: %v\n", err)
		nctx, ncancel := context.WithTimeout(ctx, daemonNotifyTimeout)
		notify(nctx, env, "Telegram Agents could not start: "+err.Error(), log)
		ncancel()
		return exitError
	}
	defer closeAll()

	stopResync := watchResync(d.Resync, log)
	defer stopResync()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runTelegram(runCtx)
	}()
	runErr := d.Run(runCtx)
	cancel(nil)
	wg.Wait()

	code, reason := exitOK, "stopped"
	if runErr != nil {
		code, reason = exitError, runErr.Error()
		fmt.Fprintf(rc.stderr, "herdr-tg daemon: %v\n", runErr)
	}
	log.Info("daemon exit", slog.Int("code", code), slog.String("reason", reason))
	return code
}
