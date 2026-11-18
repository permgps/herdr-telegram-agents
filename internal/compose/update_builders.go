package compose

import (
	"context"
	"log/slog"
	"os"

	"github.com/permgps/herdr-telegram-agents/internal/adapters/github"
	"github.com/permgps/herdr-telegram-agents/internal/adapters/herdr"
	"github.com/permgps/herdr-telegram-agents/internal/adapters/state"
	"github.com/permgps/herdr-telegram-agents/internal/adapters/system"
	"github.com/permgps/herdr-telegram-agents/internal/adapters/telegram"
	"github.com/permgps/herdr-telegram-agents/internal/app"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// cleanUpdateArtifacts removes staged workers and backups left by previous
// updates while preserving the current job and any worker-held lock.
func cleanUpdateArtifacts(env PluginEnv, job domain.UpdateJob, jobErr error, log *slog.Logger) {
	proc := system.NewProcess(env.StateDir, log)
	if system.NewUpdateLock(env.StateDir, proc.Alive, log).Active() || (jobErr != nil && !os.IsNotExist(jobErr)) {
		return
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

// DeliverUpdateResult edits the existing General panel idempotently.
func DeliverUpdateResult(ctx context.Context, env PluginEnv, cfg domain.Config, log *slog.Logger) error {
	client, err := telegram.NewInspector(cfg.BotToken, cfg.ChatID, log)
	if err != nil {
		return err
	}
	return app.DeliverUpdateResult(ctx, state.NewUpdateStore(env.StateDir, log), client, cfg.ChatID, log)
}
