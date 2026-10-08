package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// UpdateWorker owns one transaction while holding the cross-process lock.
// Every transition is persisted before the next irreversible operation.
type UpdateWorker struct {
	Store         domain.UpdateJobStore
	Lock          domain.UpdateLock
	Installer     domain.UpdateInstaller
	Reader        domain.InstallationReader
	Supervisor    *Supervisor
	Log           *slog.Logger
	HealthTimeout time.Duration
}

func (w *UpdateWorker) healthTimeout() time.Duration {
	if w.HealthTimeout > 0 {
		return w.HealthTimeout
	}
	return 30 * time.Second
}

func (w *UpdateWorker) Run(ctx context.Context, id string) (domain.UpdateJob, error) {
	if w.Store == nil || w.Lock == nil || w.Installer == nil || w.Reader == nil || w.Supervisor == nil {
		return domain.UpdateJob{}, fmt.Errorf("update worker not configured")
	}
	if err := w.Lock.Acquire(); err != nil {
		return domain.UpdateJob{}, err
	}
	defer w.Lock.Release()
	job, err := w.Store.Load(ctx)
	if err != nil {
		return job, err
	}
	if job.ID != id || job.Phase != "queued" {
		return job, fmt.Errorf("update job is not queued for this worker")
	}
	// A managed install is bound to the signed commit after the fact; a job
	// without one cannot be checked, so nothing is touched.
	if job.SourceKind == "github" && !commitPattern.MatchString(job.TargetCommit) {
		return w.fail(ctx, job, "commit_unknown", fmt.Errorf("update job has no signed target commit"))
	}
	if w.Supervisor.Status().Running != job.PriorRunning {
		return w.fail(ctx, job, "daemon_state_changed", fmt.Errorf("daemon running state changed after approval"))
	}
	job, err = w.Installer.Backup(ctx, job)
	if err != nil {
		return w.fail(ctx, job, "backup_failed", err)
	}
	if err := w.phase(ctx, &job, "backed_up"); err != nil {
		return job, err
	}
	if job.PriorRunning {
		if err := w.Supervisor.Stop(ctx); err != nil {
			return w.fail(ctx, job, "stop_failed", err)
		}
	}
	if err := w.phase(ctx, &job, "daemon_stopped"); err != nil {
		return job, err
	}
	if err := w.Installer.Install(ctx, job); err != nil {
		return w.rollback(ctx, job, "install_failed", err)
	}
	if err := w.phase(ctx, &job, "installed"); err != nil {
		return w.rollback(ctx, job, "journal_failed", err)
	}
	w.logReceipt(ctx, job)
	// Reading the installation runs the new binary (`herdr-tg version`), so
	// its checksum is checked at the approved root first; the check against
	// the registered root below stays.
	if err := w.Installer.Verify(ctx, job, job.SourceRoot); err != nil {
		if w.Log != nil {
			w.Log.Warn("[FIX] new binary refused before first run", slog.String("job", job.ID), slog.String("root", job.SourceRoot))
		}
		return w.rollback(ctx, job, "checksum_mismatch", err)
	}
	installed, err := w.Reader.ReadInstallation(ctx)
	if err != nil {
		return w.rollback(ctx, job, "inspect_failed", err)
	}
	if job.SourceKind == "github" && installed.ResolvedCommit != job.TargetCommit {
		if w.Log != nil {
			w.Log.Warn("[FIX] managed install resolved another commit", slog.String("job", job.ID),
				slog.String("want", shortCommit(job.TargetCommit)), slog.String("got", shortCommit(installed.ResolvedCommit)))
		}
		return w.rollback(ctx, job, "commit_mismatch", fmt.Errorf("installed commit is not the signed commit"))
	}
	if installed.ManifestVersion != job.TargetVersion || installed.BinaryVersion != job.TargetVersion {
		return w.rollback(ctx, job, "version_mismatch", fmt.Errorf("installed manifest or binary version does not match target"))
	}
	if err := w.Installer.Verify(ctx, job, installed.Root); err != nil {
		return w.rollback(ctx, job, "checksum_mismatch", err)
	}
	if job.PriorRunning {
		pid, already, err := w.Supervisor.StartAt(ctx, installed.Root)
		if err != nil || already {
			return w.rollback(ctx, job, "start_failed", fmt.Errorf("start replacement: pid=%d already=%t: %v", pid, already, err))
		}
		if err := w.Supervisor.WaitHealthy(ctx, pid, job.TargetVersion, w.healthTimeout()); err != nil {
			return w.rollback(ctx, job, "health_failed", err)
		}
	}
	job.NewCommit = installed.ResolvedCommit
	if err := w.phase(ctx, &job, "succeeded"); err != nil {
		return job, err
	}
	return job, nil
}

// logReceipt records what the install script saw. It never decides the
// update: Verify below re-hashes the binary either way. "approved none" on a
// managed install means Herdr did not pass HERDR_TG_EXPECTED_SHA256 to the
// [[build]] step.
func (w *UpdateWorker) logReceipt(ctx context.Context, job domain.UpdateJob) {
	if w.Log == nil {
		return
	}
	receipt, err := w.Installer.Receipt(ctx, job.SourceRoot)
	if errors.Is(err, os.ErrNotExist) {
		w.Log.Debug("install receipt missing", slog.String("job", job.ID))
		return
	}
	if err != nil {
		w.Log.Warn("install receipt unreadable", slog.String("job", job.ID), slog.Any("err", err))
		return
	}
	approved := receipt.Approved != "" && receipt.Approved != "none"
	w.Log.Info("install receipt", slog.String("job", job.ID), slog.String("signature", receipt.Signature),
		slog.Bool("approved_present", approved), slog.Bool("sha_match", strings.EqualFold(receipt.SHA256, job.TargetChecksum)))
	if !approved && job.TargetChecksum != "" {
		w.Log.Warn("[FIX] approved checksum did not reach the install script", slog.String("job", job.ID), slog.String("source_kind", job.SourceKind))
	}
}

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func (w *UpdateWorker) phase(ctx context.Context, job *domain.UpdateJob, phase string) error {
	job.Phase = phase
	if err := w.Store.Save(ctx, *job); err != nil {
		return err
	}
	if w.Log != nil {
		w.Log.Info("update worker phase", slog.String("job", job.ID), slog.String("phase", phase))
	}
	return nil
}

func (w *UpdateWorker) fail(ctx context.Context, job domain.UpdateJob, code string, reason error) (domain.UpdateJob, error) {
	job.Phase, job.ErrorCode = "failed", code
	job.ErrorMessage = "Update stopped before installation. Check daemon.log and update.json."
	recoveryCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = w.Store.Save(recoveryCtx, job)
	if w.Log != nil {
		w.Log.Error("update worker failed", slog.String("job", job.ID), slog.String("code", code), slog.Any("err", reason))
	}
	return job, reason
}

func (w *UpdateWorker) rollback(ctx context.Context, job domain.UpdateJob, code string, reason error) (domain.UpdateJob, error) {
	recoveryCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	job.ErrorCode = code
	job.ErrorMessage = "Update failed; rollback attempted. Check daemon.log and update.json."
	_ = w.phase(recoveryCtx, &job, "rolling_back")
	if w.Log != nil {
		w.Log.Warn("update rollback started", slog.String("job", job.ID), slog.String("code", code), slog.Any("err", reason))
	}
	if st := w.Supervisor.Status(); st.Running {
		if err := w.Supervisor.Stop(recoveryCtx); err != nil {
			return w.stuck(recoveryCtx, job, err)
		}
	}
	if err := w.Installer.Rollback(recoveryCtx, job); err != nil {
		return w.stuck(recoveryCtx, job, err)
	}
	installed, err := w.Reader.ReadInstallation(recoveryCtx)
	if err != nil {
		return w.stuck(recoveryCtx, job, err)
	}
	if installed.ManifestVersion != job.OldVersion {
		return w.stuck(recoveryCtx, job, fmt.Errorf("rollback manifest version mismatch"))
	}
	old, err := domain.ParseVersion(installed.BinaryVersion)
	if err != nil {
		return w.stuck(recoveryCtx, job, err)
	}
	want, _ := domain.ParseVersion(job.OldVersion)
	if old.Compare(want) != 0 {
		return w.stuck(recoveryCtx, job, fmt.Errorf("rollback binary version mismatch"))
	}
	if job.PriorRunning {
		pid, already, err := w.Supervisor.StartAt(recoveryCtx, installed.Root)
		if err != nil || already {
			return w.stuck(recoveryCtx, job, fmt.Errorf("restart old daemon: %v", err))
		}
		oldVersion := job.OldBinaryVersion
		if oldVersion == "" {
			oldVersion = job.OldVersion
		}
		if err := w.Supervisor.WaitHealthy(recoveryCtx, pid, oldVersion, w.healthTimeout()); err != nil {
			return w.stuck(recoveryCtx, job, err)
		}
	}
	if err := w.phase(recoveryCtx, &job, "rolled_back"); err != nil {
		return job, err
	}
	return job, reason
}

func (w *UpdateWorker) stuck(ctx context.Context, job domain.UpdateJob, err error) (domain.UpdateJob, error) {
	job.Phase = "stuck"
	job.ErrorMessage = "Rollback could not restore the old plugin. Inspect update.json, daemon.log, and the backup directory; reinstall the previous release manually."
	if saveErr := w.Store.Save(ctx, job); saveErr != nil {
		return job, fmt.Errorf("rollback failed: %v; journal failed: %w", err, saveErr)
	}
	if w.Log != nil {
		w.Log.Error("update rollback stuck", slog.String("job", job.ID), slog.Any("err", err))
	}
	return job, err
}
