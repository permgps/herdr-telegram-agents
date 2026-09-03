package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/compose"
)

// errTelegramFatal is the cancellation cause set when the Telegram poller
// reports 401 or 409; the daemon exits 1 with it.
var errTelegramFatal = errors.New("telegram polling stopped: invalid token or another instance is polling")

const (
	// daemonNotifyTimeout bounds the notifications sent while exiting.
	daemonNotifyTimeout = 3 * time.Second
	// telegramStopTimeout bounds the wait for the Telegram poller and queue
	// after the daemon loop returned; it exceeds the loop's own flush
	// budget so a hung poller cannot block exit.
	telegramStopTimeout = 10 * time.Second
)

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
	// The control channel's stop command cancels the same context a
	// SIGTERM would, so both paths run the same graceful shutdown.
	ctlCtx, requestStop := context.WithCancel(sigCtx)
	defer requestStop()
	runCtx, cancel := context.WithCancelCause(ctlCtx)
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
	d.Version = rc.version

	stopResync := watchResync(d.Resync, log)
	defer stopResync()

	// The control channel serves stop, resync and status on every
	// platform; on Unix the signal handlers above stay as the fallback for
	// an action from an older build.
	stopControl, err := wire.startControl(ctlCtx, env, compose.ControlHandlers{
		Stop:   requestStop,
		Resync: d.Resync,
		Status: func() string { return compose.StatsLine(statsWithPID(d.Stats(), os.Getpid()), time.Now()) },
	}, log)
	if err != nil {
		log.Warn("control channel unavailable, signals only", slog.String("err", err.Error()))
	} else {
		defer stopControl()
	}

	runErr := runWithTelegram(ctx, runCtx, d.Run, runTelegram, telegramStopTimeout, log)
	cancel(nil)

	code, reason := exitOK, "stopped"
	if runErr != nil {
		code, reason = exitError, runErr.Error()
		fmt.Fprintf(rc.stderr, "herdr-tg daemon: %v\n", runErr)
	}
	log.Info("daemon exit", slog.Int("code", code), slog.String("reason", reason))
	return code
}

// statsWithPID fills in the pid, which the app layer does not read itself.
func statsWithPID(s compose.Stats, pid int) compose.Stats {
	s.PID = pid
	return s
}

// runWithTelegram runs the daemon loop with the Telegram poller and queue
// alongside it. The Telegram side lives on a context derived from parent,
// not from the signal context, so a SIGTERM stops the loop first and the
// loop's shutdown (final topic edits, the stopping notice) still goes out
// through a live queue; only then is the poller stopped and waited for,
// bounded by stopTimeout. A fatal poller error cancels runCtx with its
// cause, so the loop returns first there too.
func runWithTelegram(parent, runCtx context.Context, run func(context.Context) error, telegram func(context.Context),
	stopTimeout time.Duration, log *slog.Logger) error {
	tgCtx, stopTelegram := context.WithCancel(parent)
	defer stopTelegram()
	done := make(chan struct{})
	go func() {
		defer close(done)
		telegram(tgCtx)
	}()
	err := run(runCtx)
	stopTelegram()
	start := time.Now()
	select {
	case <-done:
		log.Debug("telegram runner stopped", slog.Int64("dur_ms", time.Since(start).Milliseconds()))
	case <-time.After(stopTimeout):
		log.Warn("telegram runner did not stop in time, exiting anyway", slog.Int64("dur_ms", time.Since(start).Milliseconds()))
	}
	return err
}
