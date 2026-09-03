package cli

import (
	"context"
	"io"
	"log/slog"

	"github.com/permgps/herdr-telegram-agents/internal/compose"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// supervisor is the slice of compose.Supervisor the subcommands use. It is
// an interface so tests can substitute a scripted fake.
type supervisor interface {
	Status() compose.DaemonStatus
	Start(ctx context.Context) (pid int, alreadyRunning bool, err error)
	Stop(ctx context.Context) error
	Restart(ctx context.Context) (int, error)
	Resync() error
	Describe(ctx context.Context) string
}

// wiring holds the composition-root entry points the subcommands call.
// Tests replace individual fields and restore them with t.Cleanup; the
// defaults are the real compose functions.
type wiring struct {
	env             func() (compose.PluginEnv, error)
	loadConfig      func(ctx context.Context, env compose.PluginEnv, log *slog.Logger) (domain.Config, error)
	fileLogger      func(env compose.PluginEnv, configLevel string) (*slog.Logger, io.Closer, error)
	notify          func(ctx context.Context, env compose.PluginEnv, body string, log *slog.Logger) error
	pidFile         func(env compose.PluginEnv, log *slog.Logger) domain.PidFile
	buildDaemon     func(ctx context.Context, env compose.PluginEnv, cfg domain.Config, log *slog.Logger, fatal context.CancelFunc) (*compose.Daemon, func(context.Context), func(), error)
	buildSupervisor func(env compose.PluginEnv, log *slog.Logger) supervisor
	startControl    func(ctx context.Context, env compose.PluginEnv, h compose.ControlHandlers, log *slog.Logger) (func(), error)
	buildSetup      func(env compose.PluginEnv, ui domain.SetupUI, log *slog.Logger) setupRunner
	paneOpener      func(env compose.PluginEnv, log *slog.Logger) domain.PaneOpener
	openURL         func(ctx context.Context, url string) error
	teeLogger       func(loggers ...*slog.Logger) *slog.Logger
}

var wire = defaultWiring()

func defaultWiring() wiring {
	return wiring{
		env:         compose.Env,
		loadConfig:  compose.LoadConfig,
		fileLogger:  compose.NewFileLogger,
		notify:      compose.Notify,
		pidFile:     compose.NewPidFile,
		buildDaemon: compose.BuildDaemon,
		buildSupervisor: func(env compose.PluginEnv, log *slog.Logger) supervisor {
			return compose.BuildSupervisor(env, log)
		},
		startControl: compose.StartControl,
		buildSetup: func(env compose.PluginEnv, ui domain.SetupUI, log *slog.Logger) setupRunner {
			return compose.BuildSetup(env, ui, log)
		},
		paneOpener: compose.PaneOpener,
		openURL:    compose.OpenURL,
		teeLogger:  compose.TeeLogger,
	}
}

// ErrSetupCancelled is the wizard's "user declined" outcome.
var ErrSetupCancelled = compose.ErrSetupCancelled

// notify sends a Herdr notification and logs failures at warn; every
// subcommand treats notifications as best effort.
func notify(ctx context.Context, env compose.PluginEnv, body string, log *slog.Logger) {
	if err := wire.notify(ctx, env, body, log); err != nil {
		log.Warn("herdr notification failed", slog.String("body", body), slog.String("err", err.Error()))
	}
}
