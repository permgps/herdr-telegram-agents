package compose

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/adapters/herdr"
	"github.com/permgps/herdr-telegram-agents/internal/adapters/logging"
	"github.com/permgps/herdr-telegram-agents/internal/adapters/state"
	"github.com/permgps/herdr-telegram-agents/internal/adapters/system"
	"github.com/permgps/herdr-telegram-agents/internal/adapters/telegram"
	"github.com/permgps/herdr-telegram-agents/internal/adapters/transcript"
	"github.com/permgps/herdr-telegram-agents/internal/app"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// The CLI may import only compose and domain, so the application types it
// needs are re-exported here as aliases. They are the same types; nothing
// is wrapped.
type (
	// PluginEnv is the environment Herdr passes to plugin commands.
	PluginEnv = system.PluginEnv
	// Daemon is the sync event loop.
	Daemon = app.Daemon
	// Setup is the interactive configuration wizard.
	Setup = app.Setup
	// Supervisor starts, stops and signals the daemon.
	Supervisor = app.Supervisor
	// DaemonStatus is what the status action reports.
	DaemonStatus = app.DaemonStatus
	// Stats is the running daemon's self-description.
	Stats = app.Stats
	// ControlHandlers are the daemon actions the control channel exposes.
	ControlHandlers = system.ControlHandlers
	// Doctor runs the diagnostic checks of the doctor pane.
	Doctor = app.Doctor
)

// ErrSetupCancelled is returned by Setup.Run when the user declines to save.
var ErrSetupCancelled = app.ErrSetupCancelled

// NotifyTitle is the title of every Herdr notification the plugin shows.
const NotifyTitle = "Telegram Agents"

// Summary renders a DaemonStatus for humans.
func Summary(st DaemonStatus, now time.Time) string { return app.Summary(st, now) }

// StatsLine renders a running daemon's Stats as one line.
func StatsLine(s Stats, now time.Time) string { return app.StatsLine(s, now) }

// RenderChecks renders a doctor report for the pane.
func RenderChecks(version string, checks []domain.Check) string {
	return app.RenderChecks(version, checks)
}

// SendTest posts the send-test message through the inspector and returns
// the sentence the action reports.
func SendTest(ctx context.Context, insp domain.TelegramInspector, version string, log *slog.Logger) (string, error) {
	return app.SendTest(ctx, insp, version, time.Now(), log)
}

// BuildInspector builds the light Telegram client of the doctor and
// send-test actions from a loaded config.
func BuildInspector(cfg domain.Config, log *slog.Logger) (domain.TelegramInspector, error) {
	return telegram.NewInspector(cfg.BotToken, cfg.ChatID, log)
}

// BuildDoctor wires the doctor checks against the real stores, the pid
// file, the control channel, the Telegram inspector and the Herdr socket.
func BuildDoctor(env PluginEnv, version string, log *slog.Logger) *Doctor {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	proc := system.NewProcess(env.StateDir, log)
	mappings := state.NewMappingStore(env.StateDir, log)
	return &app.Doctor{
		Version:       version,
		Config:        state.NewConfigStore(env.ConfigDir, log),
		Options:       state.NewOptionsStore(env.ConfigDir, log),
		Mapping:       mappings,
		Broken:        mappings.BrokenFiles,
		Pid:           state.NewPidFile(env.StateDir, proc.Alive, log),
		Alive:         proc.Alive,
		ControlStatus: proc.Status,
		Inspector: func(cfg domain.Config) (domain.TelegramInspector, error) {
			return BuildInspector(cfg, log)
		},
		Herdr:            herdr.NewGateway(env.SocketPath, log, herdr.DefaultBackoff),
		ExpectedProtocol: herdr.ProtocolVersion,
		Clock:            realClock{},
		Log:              log,
	}
}

// StartControl opens the daemon's control channel and serves it until ctx
// is done. Listening and serving are one call so no net.Listener crosses
// into internal/cli, which may not import net. The returned stop function
// closes the channel and waits for the loop.
func StartControl(ctx context.Context, env PluginEnv, h ControlHandlers, log *slog.Logger) (func(), error) {
	ln, err := system.ListenControl(env.StateDir, log)
	if err != nil {
		return nil, fmt.Errorf("control channel: %w", err)
	}
	serveCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		system.ServeControl(serveCtx, ln, h, log)
	}()
	return func() {
		cancel()
		<-done
	}, nil
}

// realClock is the production domain.Clock.
type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Clock returns the wall clock.
func Clock() domain.Clock { return realClock{} }

// Env reads the HERDR_* environment. It fails outside Herdr because the
// config and state directories are unknown there.
func Env() (PluginEnv, error) {
	return system.ReadEnv()
}

// SocketPath resolves the Herdr socket for commands that need nothing else.
func SocketPath() (string, error) {
	return system.DefaultSocketPath()
}

// NewFileLogger opens the rotating JSON daemon log under the state
// directory. The level comes from LOG_LEVEL when set, else configLevel.
func NewFileLogger(env PluginEnv, configLevel string) (*slog.Logger, io.Closer, error) {
	level := logging.ResolveLevel(env.LogLevel, configLevel)
	log, closer, err := logging.NewFileLogger(env.StateDir, level)
	if err != nil {
		return nil, nil, fmt.Errorf("open daemon log: %w", err)
	}
	log.Debug("compose env", slog.String("plugin", env.PluginID), slog.String("config_dir", env.ConfigDir),
		slog.String("state_dir", env.StateDir), slog.String("socket", env.SocketPath), slog.String("bin", env.BinPath))
	return log, closer, nil
}

// TeeLogger writes to every given logger; nil loggers are skipped.
func TeeLogger(loggers ...*slog.Logger) *slog.Logger { return logging.Tee(loggers...) }

// ConfigStore returns the config.json store.
func ConfigStore(env PluginEnv, log *slog.Logger) domain.ConfigStore {
	return state.NewConfigStore(env.ConfigDir, log)
}

// LoadConfig reads and validates config.json. A missing or invalid file
// yields domain.ErrNotConfigured.
func LoadConfig(ctx context.Context, env PluginEnv, log *slog.Logger) (domain.Config, error) {
	cfg, err := state.NewConfigStore(env.ConfigDir, log).Load(ctx)
	if err != nil {
		return domain.Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return domain.Config{}, err
	}
	return cfg, nil
}

// Notify shows a Herdr notification through a one-shot socket call; no
// event stream is started. Errors are returned so callers can log them.
func Notify(ctx context.Context, env PluginEnv, body string, log *slog.Logger) error {
	g := herdr.NewGateway(env.SocketPath, log, herdr.DefaultBackoff)
	return g.Notify(ctx, NotifyTitle, body, domain.NotifySoundDefault)
}

// PaneOpener returns the herdr CLI runner used to open manifest panes.
// OpenShared opens a file for reading without blocking a rename or delete
// by another process; the logs pane uses it so that following the log does
// not stop the daemon from rotating it on Windows.
func OpenShared(path string) (*os.File, error) { return system.OpenShared(path) }

// OpenURL opens a link in the user's browser (best effort).
func OpenURL(ctx context.Context, url string) error { return system.OpenURL(ctx, url) }

func PaneOpener(env PluginEnv, log *slog.Logger) domain.PaneOpener {
	return herdr.NewCLI(env.BinPath, env.Root, env.Path, log)
}

// NewPidFile returns the daemon pid file with process liveness checks.
func NewPidFile(env PluginEnv, log *slog.Logger) domain.PidFile {
	proc := system.NewProcess(env.StateDir, log)
	return state.NewPidFile(env.StateDir, proc.Alive, log)
}

// BuildSupervisor wires pid file and process control for the actions.
func BuildSupervisor(env PluginEnv, log *slog.Logger) *Supervisor {
	proc := system.NewProcess(env.StateDir, log)
	pid := state.NewPidFile(env.StateDir, proc.Alive, log)
	return app.NewSupervisor(pid, proc, realClock{}, log)
}

// BuildSetup wires the wizard with the real Telegram probe.
func BuildSetup(env PluginEnv, ui domain.SetupUI, log *slog.Logger) *Setup {
	probe := func(token string) (domain.SetupProbe, error) {
		return telegram.NewProbe(token, log)
	}
	return app.NewSetup(state.NewConfigStore(env.ConfigDir, log), probe, ui, realClock{}, log)
}

// BuildDaemon connects to Herdr and Telegram and wires the sync loop. The
// returned run function polls Telegram until its context ends; closeAll
// releases the Herdr connection. fatal is invoked by the Telegram poller on
// 401/409 and should cancel the daemon context with a cause.
func BuildDaemon(ctx context.Context, env PluginEnv, cfg domain.Config, log *slog.Logger, fatal context.CancelFunc) (
	d *Daemon, run func(context.Context), closeAll func(), err error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	log.Debug("compose daemon", slog.String("socket", env.SocketPath), slog.String("state_dir", env.StateDir),
		slog.Int64("chat_id", cfg.ChatID))

	hg := NewHerdrGateway(env.SocketPath, log)
	if err := hg.Start(ctx); err != nil {
		return nil, nil, nil, fmt.Errorf("herdr connect %s: %w", env.SocketPath, err)
	}
	closeAll = func() { _ = hg.Close() }

	tg, run, err := telegram.Connect(ctx, cfg, log, fatal)
	if err != nil {
		closeAll()
		return nil, nil, nil, err
	}

	mappings := state.NewMappingStore(env.StateDir, log)
	mapping, err := mappings.Load(ctx)
	if err != nil {
		closeAll()
		return nil, nil, nil, fmt.Errorf("load mapping: %w", err)
	}
	if mapping.ChatID != 0 && mapping.ChatID != cfg.ChatID {
		log.Warn("mapping belongs to another chat, starting empty",
			slog.Int64("mapping_chat_id", mapping.ChatID), slog.Int64("chat_id", cfg.ChatID))
		mapping = domain.NewMapping(cfg.ChatID)
	}
	if mapping.ChatID == 0 {
		mapping.ChatID = cfg.ChatID
	}

	optionsStore := state.NewOptionsStore(env.ConfigDir, log)
	options, err := optionsStore.Load(ctx)
	if err != nil {
		log.Error("options unreadable, using defaults", slog.String("path", optionsStore.Path()), slog.String("err", err.Error()))
		options = domain.DefaultOptions()
	}
	choices := func(name string) []string {
		if name == domain.ChoiceSourceIcons {
			return tg.IconPack()
		}
		return nil
	}
	if clean, dropped := domain.SanitizeOptions(options, choices); len(dropped) > 0 {
		log.Warn("options ignored, defaults used for them", slog.String("path", optionsStore.Path()), slog.Any("keys", dropped))
		options = clean
	}
	opts := app.NewOptions(options, optionsStore, choices, log)
	log.Info("options loaded", slog.Bool("sync", options.SyncEnabled()), slog.Bool("quiet", options.QuietEnabled()),
		slog.Int64("quiet_idle_min", int64(options.QuietIdle()/time.Minute)), slog.String("quiet_posts", string(options.QuietPosts())), slog.String("icons",
			options.StatusIcons().Working+options.StatusIcons().Idle+options.StatusIcons().Blocked+
				options.StatusIcons().Done+options.StatusIcons().Unknown+options.StatusIcons().Exited))

	clock := realClock{}
	registry := app.NewRegistry(hg, clock, log)
	reconciler := app.NewReconciler(tg, hg, mappings, mapping, opts, clock, log)
	capture := app.NewCapture(hg, registry.Live, clock, log)
	bridge := app.NewBridge(cfg, hg, tg, registry, reconciler, capture, opts, transcript.NewReader(log), clock, log)
	presence := app.NewPresence(system.NewIdleSource(log), opts, clock, log)
	d = app.NewDaemon(cfg, hg, tg, registry, reconciler, bridge, capture, state.NewConfigStore(env.ConfigDir, log), opts, presence, clock, log)
	return d, run, closeAll, nil
}
