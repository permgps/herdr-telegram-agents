package compose

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/adapters/github"
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
	// TelegramStartRetry is how the daemon keeps trying Telegram at start.
	TelegramStartRetry = telegram.StartRetry
	// TelegramStartWait describes one failed Telegram startup attempt.
	TelegramStartWait = telegram.StartWait
)

// DefaultTelegramStartRetry is the daemon's startup retry policy.
func DefaultTelegramStartRetry() TelegramStartRetry { return telegram.DefaultStartRetry() }

// StartingLine is the status reply of a daemon still reaching Telegram.
func StartingLine(version string, pid int, since time.Time, attempt int, now time.Time) string {
	return app.StartingLine(version, pid, since, attempt, now)
}

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
	claudeUsage := claudeUsageFile(env)
	return &app.Doctor{
		Version:       version,
		Config:        state.NewConfigStore(env.ConfigDir, log),
		Options:       state.NewOptionsStore(env.ConfigDir, log),
		Mapping:       mappings,
		Broken:        mappings.BrokenFiles,
		Pid:           state.NewPidFile(env.StateDir, proc.Alive, log).CheckStart(proc.StartTime),
		Alive:         proc.Alive,
		ControlStatus: proc.Status,
		Inspector: func(cfg domain.Config) (domain.TelegramInspector, error) {
			return BuildInspector(cfg, log)
		},
		Herdr:              herdr.NewGateway(env.SocketPath, log, herdr.DefaultBackoff),
		SupportedProtocols: herdr.SupportedProtocolVersions(),
		ClaudeUsage:        claudeUsage,
		ClaudeTap:          claudeTapCommand(env, claudeUsage.Path()),
		CodexUsage:         transcript.NewCodexUsage(log),
		Clock:              realClock{},
		Log:                log,
	}
}

// claudeUsageFile is the status line tap's file under the state dir.
func claudeUsageFile(env PluginEnv) *state.ClaudeUsageFile {
	return state.NewClaudeUsageFile(filepath.Join(env.StateDir, state.ClaudeUsageFileName))
}

// quotaSources lists the quota providers in display order.
func quotaSources(claude *state.ClaudeUsageFile, log *slog.Logger) []app.QuotaSource {
	return []app.QuotaSource{
		{Provider: domain.UsageClaude, Source: claude},
		{Provider: domain.UsageCodex, Source: transcript.NewCodexUsage(log)},
	}
}

// claudeTapCommand is the status line command prefix doctor suggests: the
// running binary (the plugin's bin/herdr-tg) with the tap's file. Paths
// with spaces are single-quoted for the shell Claude Code runs it in.
func claudeTapCommand(env PluginEnv, file string) string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = filepath.Join(env.Root, "bin", "herdr-tg")
	}
	return shellWord(exe) + " usage-tap --out " + shellWord(file)
}

// shellWord quotes s for a POSIX shell when it holds anything but plain
// path characters; a backslash counts as special, so a Windows path keeps
// its separators in a POSIX shell.
func shellWord(s string) string {
	plain := s != ""
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+:", r)) {
			plain = false
			break
		}
	}
	if plain {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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

// TapClaudeUsage is the status line tap behind `herdr-tg usage-tap`: it
// copies in to out and stores Claude Code's rate-limit windows at path.
func TapClaudeUsage(in io.Reader, out io.Writer, path string) error {
	return state.TapClaudeUsage(in, out, path, time.Now)
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

// TightenPermissions makes the state and config directories 0700 and the
// files this plugin writes there 0600 when an older build or the user left
// them looser (Unix only; a no-op on Windows). It returns how many paths
// it changed. Only the plugin's own names are listed: anything else in
// those directories is left alone.
func TightenPermissions(env PluginEnv, log *slog.Logger) int {
	var dirs, files []string
	if env.ConfigDir != "" {
		dirs = append(dirs, env.ConfigDir)
		for _, name := range []string{state.ConfigFileName, state.OptionsFileName} {
			files = append(files, filepath.Join(env.ConfigDir, name))
		}
	}
	if env.StateDir != "" {
		dirs = append(dirs, env.StateDir, filepath.Join(env.StateDir, state.InboxDirName))
		names := []string{
			state.MappingFileName, state.SharingFileName, state.UpdateFileName, state.PidFileName, state.ClaudeUsageFileName,
			logging.LogFileName, system.ErrLogFileName, system.UpdateWorkerErrLogFileName,
		}
		for i := 1; i <= logging.RotateKeep; i++ {
			names = append(names, fmt.Sprintf("%s.%d", logging.LogFileName, i))
		}
		for _, name := range names {
			files = append(files, filepath.Join(env.StateDir, name))
		}
	}
	return state.TightenPermissions(dirs, files, log)
}

// NewPidFile returns the daemon pid file with process liveness checks.
func NewPidFile(env PluginEnv, log *slog.Logger) domain.PidFile {
	proc := system.NewProcess(env.StateDir, log)
	return state.NewPidFile(env.StateDir, proc.Alive, log).CheckStart(proc.StartTime)
}

// BuildSupervisor wires pid file and process control for the actions.
func BuildSupervisor(env PluginEnv, log *slog.Logger) *Supervisor {
	proc := system.NewProcess(env.StateDir, log)
	pid := state.NewPidFile(env.StateDir, proc.Alive, log).CheckStart(proc.StartTime)
	return app.NewSupervisor(pid, proc, realClock{}, log)
}

// replySources is the reader chain behind done posts and /screen: Claude
// Code transcripts by working directory, then the exact-session OpenCode
// export, Codex rollout, Antigravity transcript and pi session file, then
// the Muse session log of the pane's process (processes lists the pane's
// pids, started reports when a pid's process started). Each reader rejects
// the kinds it does not know with ErrNoReply, and the chain keeps a
// reader's ErrReplyPending over a later reader's "unsupported agent".
func replySources(session func(context.Context, string) (domain.SessionTuple, error),
	openCodeExport func(context.Context, string) ([]byte, error),
	processes func(context.Context, string) ([]int, error), started func(int) (time.Time, bool),
	log *slog.Logger) domain.MultiReplySource {
	return domain.MultiReplySource{
		transcript.NewReader(log),
		transcript.NewOpenCodeReader(session, openCodeExport, log),
		transcript.NewCodexReader(session, log),
		transcript.NewAgyReader(session, log),
		transcript.NewPiReader(session, log),
		transcript.NewMuseReader(processes, started, log),
	}
}

// cleanUpdateArtifacts removes staged update workers and backups left by
// earlier updates. The current job keeps its own until it succeeded, and
// nothing is touched while an update worker holds the lock (the daemon may
// be the replacement that worker is health-checking).
func cleanUpdateArtifacts(env PluginEnv, job domain.UpdateJob, jobErr error, log *slog.Logger) {
	proc := system.NewProcess(env.StateDir, log)
	if system.NewUpdateLock(env.StateDir, proc.Alive, log).Active() {
		return
	}
	if jobErr != nil && !os.IsNotExist(jobErr) {
		return // unknown job: keep everything
	}
	keep := ""
	if jobErr == nil && job.Phase != "succeeded" {
		keep = job.ID
	}
	if n := system.CleanUpdateArtifacts(env.StateDir, keep, log); n > 0 {
		log.Info("update artifacts cleaned", slog.Int("removed", n), slog.String("kept_job", keep))
	}
}

// BuildUpdateManager wires the read-only check and approval flow used by
// the options panel. Every press uses fresh Herdr and Git observations.
func BuildUpdateManager(env PluginEnv, log *slog.Logger) *app.UpdateManager {
	proc := system.NewProcess(env.StateDir, log)
	reader := &herdr.InstallationReader{Bin: env.BinPath, ExpectedRoot: env.Root, Status: proc.Status, Log: log}
	preflight := &app.UpdatePreflight{Installation: reader, Checkout: &system.CheckoutInspector{Log: log}, ExpectedRoot: env.Root, History: state.NewUpdateStore(env.StateDir, log), Log: log}
	return &app.UpdateManager{Releases: github.NewSource(github.NewHTTPClient(), log), Preflight: preflight,
		Herdr: herdr.NewGateway(env.SocketPath, log, herdr.DefaultBackoff), Log: log}
}

func BuildUpdateWorker(env PluginEnv, log *slog.Logger) *app.UpdateWorker {
	proc := system.NewProcess(env.StateDir, log)
	reader := &herdr.InstallationReader{Bin: env.BinPath, Status: proc.Status, Log: log}
	return &app.UpdateWorker{Store: state.NewUpdateStore(env.StateDir, log), Lock: system.NewUpdateLock(env.StateDir, proc.Alive, log),
		Installer: &system.UpdateInstaller{StateDir: env.StateDir, HerdrBin: env.BinPath, Log: log},
		Reader:    reader, Supervisor: BuildSupervisor(env, log), Log: log}
}

func LaunchUpdateWorker(ctx context.Context, env PluginEnv, jobID string, log *slog.Logger) (int, error) {
	return system.LaunchUpdateWorker(ctx, env.StateDir, jobID, log)
}

// DeliverUpdateResult edits the existing General panel. Repeated attempts
// are idempotent because they target one known message id.
func DeliverUpdateResult(ctx context.Context, env PluginEnv, cfg domain.Config, log *slog.Logger) error {
	client, err := telegram.NewInspector(cfg.BotToken, cfg.ChatID, log)
	if err != nil {
		return err
	}
	return app.DeliverUpdateResult(ctx, state.NewUpdateStore(env.StateDir, log), client, cfg.ChatID, log)
}

func RecoverUpdate(ctx context.Context, env PluginEnv, log *slog.Logger) (domain.UpdateJob, error) {
	proc := system.NewProcess(env.StateDir, log)
	lock := system.NewUpdateLock(env.StateDir, proc.Alive, log)
	return state.NewUpdateStore(env.StateDir, log).Recover(ctx, lock.Active())
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
// 401/409 and should cancel the daemon context with a cause. retry is the
// Telegram startup retry policy; a cancelled ctx ends its wait.
func BuildDaemon(ctx context.Context, env PluginEnv, cfg domain.Config, log *slog.Logger, fatal context.CancelFunc, retry TelegramStartRetry) (
	d *Daemon, run func(context.Context), closeAll func(), err error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	log.Debug("compose daemon", slog.String("socket", env.SocketPath), slog.String("state_dir", env.StateDir),
		slog.Int64("chat_id", cfg.ChatID), slog.Duration("retry_max", retry.Max))

	// A missing Herdr socket still fails the start at once, but the event
	// stream opens only after Telegram answered: while the daemon waits for
	// the network nothing would read it, and Herdr would be left writing
	// into a full subscriber socket.
	hg := NewHerdrGateway(env.SocketPath, log)
	if _, err := hg.Ping(ctx); err != nil {
		return nil, nil, nil, fmt.Errorf("herdr connect %s: %w", env.SocketPath, err)
	}

	tg, run, err := telegram.Connect(ctx, cfg, log, fatal, retry)
	if err != nil {
		return nil, nil, nil, err
	}

	if err := hg.Start(ctx); err != nil {
		return nil, nil, nil, fmt.Errorf("herdr connect %s: %w", env.SocketPath, err)
	}
	log.Debug("[FIX] herdr stream started after telegram connected", slog.String("socket", env.SocketPath))
	closeAll = func() { _ = hg.Close() }

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
	// [FIX] quiet mode defaults to off (changed 2026-09-06) so a fresh install mirrors
	// every change with sound; say which value is in force and whether it
	// came from options.json or from the default.
	log.Info("[FIX] quiet mode default", slog.Bool("quiet", options.QuietEnabled()),
		slog.Bool("from_options_file", !options.IsDefault(domain.OptionQuietEnabled)), slog.String("path", optionsStore.Path()))

	clock := realClock{}
	registry := app.NewRegistry(hg, clock, log)
	// Topic names come from agent labels: they leave through the same
	// redaction as posts.
	reconciler := app.NewReconciler(app.NewRedactingGateway(tg, cfg.BotToken, opts.RedactEnabled, log), hg, mappings, mapping, opts, clock, log)
	capture := app.NewCapture(hg, registry.Live, clock, log)
	inbox := state.NewInbox(env.StateDir, log)
	inbox.MaxTotal = opts.InboxMaxTotalBytes
	// One exporter serves the topic posts and the private mirror; its export
	// files live under the state dir, and a crash's leftovers go now.
	system.SweepOpenCodeExports(env.StateDir, log)
	openCodeExport := system.NewOpenCodeExporter(env.StateDir, log).Export
	// The Muse reader ties a pane's pids to their process start times.
	started := system.NewProcess(env.StateDir, log).StartTime
	bridge := app.NewBridge(cfg, hg, tg, registry, reconciler, capture, opts,
		app.Services{Replies: replySources(hg.AgentSession, openCodeExport, hg.PaneProcesses, started, log), Git: system.NewGitRunner(log), Inbox: inbox, Config: state.NewConfigStore(env.ConfigDir, log),
			Updates: BuildUpdateManager(env, log), UpdateJobs: state.NewUpdateStore(env.StateDir, log),
			LaunchUpdate:  func(ctx context.Context, id string) (int, error) { return LaunchUpdateWorker(ctx, env, id, log) },
			UpdateRunning: func() bool { return BuildSupervisor(env, log).Status().Running }}, clock, log)
	job, jobErr := state.NewUpdateStore(env.StateDir, log).Load(ctx)
	if jobErr == nil {
		bridge.RestoreUpdate(job)
	} else if !os.IsNotExist(jobErr) {
		log.Warn("update state unreadable", slog.String("err", jobErr.Error()))
	}
	cleanUpdateArtifacts(env, job, jobErr, log)
	presence := app.NewPresence(system.NewIdleSource(log), opts, clock, log)
	d = app.NewDaemon(cfg, hg, tg, registry, reconciler, bridge, capture, state.NewConfigStore(env.ConfigDir, log), opts, presence, clock, log)
	d.SetInbox(inbox)
	claudeUsage := claudeUsageFile(env)
	log.Info("quota sources", slog.String("claude_file", claudeUsage.Path()), slog.Bool("codex", true))
	d.SetQuota(app.NewQuota(quotaSources(claudeUsage, log), opts, clock, log))
	d.Sharing = app.NewSharing(ctx, state.NewSharingStore(env.StateDir, log), log)
	d.Sharing.BotID = tg.ConnectedBotID()
	d.Sharing.Agent = registry.Agent
	d.Sharing.Now = clock.Now
	privateTelegram := app.PrivateRedactor{DestinationTelegram: tg, Redactor: domain.NewRedactor(cfg.BotToken), Log: log, Unavailable: func(ctx context.Context, chat int64) {
		// A private chat id is the recipient's user id.
		_ = d.Sharing.Reachability(ctx, chat, true, clock.Now())
	}}
	bridge.PrivateBusy = tg.PrivateBusy
	bridge.Shares = &app.SharePanel{Sharing: d.Sharing, Capability: &app.PrivateCapability{Source: tg, Log: log}, Telegram: app.NewRedactingGateway(tg, cfg.BotToken, opts.RedactEnabled, log), Private: privateTelegram, Config: cfg, Agent: registry.Agent, KeyForThread: reconciler.KeyForThread, Now: clock.Now}
	privateReconciler := &app.PrivateReconciler{Automatic: opts.SyncEnabled, Sharing: d.Sharing, Telegram: privateTelegram, Agent: registry.Agent, Now: clock.Now, Log: log}
	bridge.Shares.OnGrant = privateReconciler.Grant
	bridge.PrivateReconciler = privateReconciler
	bridge.SetPrivateControl(&app.PrivateControl{Sharing: d.Sharing, Telegram: privateTelegram, Transport: tg, Herdr: hg, Git: system.NewGitRunner(log), Inbox: inbox, InboxEnabled: opts.InboxEnabled, InboxMaxBytes: opts.InboxMaxBytes, Agent: registry.Agent, Now: clock.Now, Log: log})

	privateOutput := &app.PrivateOutput{Control: bridge.PrivateControl, Capture: capture, ExactReplies: transcript.NewOpenCodeReader(hg.AgentSession, openCodeExport, log), Automatic: opts.SyncEnabled}
	bridge.PrivateControl.Output = privateOutput
	bridge.PrivateControl.Read = privateOutput.Read
	privateDashboard := app.NewPrivateDashboard(bridge.PrivateControl, privateReconciler, cfg.BotUsername, tg)
	bridge.PrivateControl.Dashboard = privateDashboard
	bridge.PrivateControl.Overview = privateDashboard.Handle
	bridge.Shares.OnGrant = func(ctx context.Context, g domain.ShareGrant) error {
		if err := privateReconciler.Grant(ctx, g); err != nil {
			return err
		}
		return privateDashboard.Refresh(ctx, g.RecipientID, true)
	}
	tg.SetPrivateTrust(d.Sharing.Grantee)
	tg.SetPrivateRegistration(func(ctx context.Context, c domain.PrivateContact) (bool, error) {
		if _, enabled := d.Sharing.Snapshot(); !enabled {
			return false, nil
		}
		first, err := d.Sharing.Register(ctx, c.ActorID, c.ChatID, c.Name, c.Username, c.At)
		if err == nil {
			d.Sharing.ObserveUpdate(c.UpdateID)
		}
		return first, err
	})
	return d, run, closeAll, nil
}
