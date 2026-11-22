package app

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

const (
	updateCheckData              = "o:u:c"
	updateInstallPrefix          = "o:u:i:"
	updateJournalRecoveryTimeout = 10 * time.Second
)

func (p *panel) pressUpdate(ctx context.Context, ev domain.ButtonPressed) error {
	allowed := false
	for _, id := range p.operators {
		if id == ev.FromID {
			allowed = true
			break
		}
	}
	if !allowed || ev.ThreadID != 0 || ev.MessageID != p.lastID || p.updates == nil || p.async == nil {
		p.log.Warn("update callback rejected", slog.String("reason", "unauthorized_or_stale"), slog.Int("panel", ev.MessageID))
		return p.answer(ctx, ev.CallbackID, "expired or not allowed")
	}
	if ev.Data == updateCheckData {
		p.answer0(ctx, ev.CallbackID, "checking")
		if err := p.show(ctx, ev.MessageID, view{"<b>Checking for updates…</b>", nil}); err != nil {
			return err
		}
		panelID, operatorID := p.lastID, ev.FromID
		p.async(func(runCtx context.Context) any {
			check, err := p.updates.Check(runCtx, p.chatID, panelID, operatorID)
			return updateCheckResult{panelID: panelID, check: check, err: err}
		})
		return nil
	}
	if !strings.HasPrefix(ev.Data, updateInstallPrefix) {
		return p.answer(ctx, ev.CallbackID, panelToastUnknown)
	}
	intentID := strings.TrimPrefix(ev.Data, updateInstallPrefix)
	if intentID == "" {
		return p.answer(ctx, ev.CallbackID, panelToastUnknown)
	}
	p.answer0(ctx, ev.CallbackID, "rechecking")
	if err := p.show(ctx, ev.MessageID, view{"<b>Rechecking update…</b>", nil}); err != nil {
		return err
	}
	if p.lastID != ev.MessageID {
		return p.show(ctx, p.lastID, view{"<b>Update panel moved.</b> Check again before installing.", []domain.Button{{Text: "Check again", Data: updateCheckData}}})
	}
	panelID, operatorID := ev.MessageID, ev.FromID
	p.async(func(runCtx context.Context) any {
		job, err := p.startUpdate(runCtx, intentID, panelID, operatorID)
		return updateStartResult{panelID: panelID, job: job, err: err}
	})
	return nil
}

func (p *panel) startUpdate(ctx context.Context, intentID string, panelID int, operatorID int64) (domain.UpdateJob, error) {
	p.updates.startMu.Lock()
	defer p.updates.startMu.Unlock()
	check, err := p.updates.Consume(ctx, intentID, p.chatID, panelID, operatorID)
	if err != nil {
		return domain.UpdateJob{}, err
	}
	if p.jobs == nil || p.launch == nil || p.running == nil {
		return domain.UpdateJob{}, fmt.Errorf("update worker unavailable")
	}
	previous, err := p.jobs.Load(ctx)
	if err == nil && !previous.Terminal() {
		if failed, ok := p.updates.failedLaunch(previous.ID); ok {
			recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), updateJournalRecoveryTimeout)
			recoveryErr := p.jobs.Save(recoveryCtx, failed)
			cancel()
			if recoveryErr != nil {
				return failed, fmt.Errorf("recover failed worker launch before retry: %w", recoveryErr)
			}
			p.updates.clearFailedLaunch(previous.ID)
			previous = failed
		}
		if !previous.Terminal() {
			return domain.UpdateJob{}, fmt.Errorf("another update job needs recovery: %s", previous.Phase)
		}
	}
	if err != nil && !os.IsNotExist(err) {
		return domain.UpdateJob{}, err
	}
	oldCommit := check.Installation.ResolvedCommit
	if check.Installation.SourceKind == "local" {
		oldCommit = check.Checkout.Commit
	}
	job := domain.UpdateJob{
		ID: intentID, Phase: "queued", OldVersion: check.Installation.ManifestVersion,
		OldBinaryVersion: check.Installation.BinaryVersion, TargetVersion: strings.TrimPrefix(check.Release.Tag, "v"),
		TargetTag: check.Release.Tag, TargetCommit: check.Commit, TargetSigner: check.Signer,
		TargetChecksum: check.Checksum, TargetAssetURL: check.Release.AssetURL, TargetChecksumsURL: check.Release.ChecksumsURL,
		SourceKind: check.Installation.SourceKind, SourceRoot: check.Installation.Root, OldCommit: oldCommit,
		PriorRunning: p.running(), StartedAt: p.updates.now().UTC(), NotificationStatus: "pending",
		ChatID: p.chatID, PanelMessageID: panelID,
	}
	if check.Installation.SourceKind == "github" && ownsManagedPin(check.Installation, previous) {
		job.PreviousOwnedRef = check.Installation.RequestedRef
		job.PreviousOwnedCommit = check.Installation.ResolvedCommit
	}
	if err := p.jobs.Save(ctx, job); err != nil {
		return job, err
	}
	if _, err := p.launch(ctx, job.ID); err != nil {
		job.Phase, job.ErrorCode = "failed", "worker_launch"
		job.ErrorMessage = "The update worker could not start. Check daemon.log."
		if saveErr := p.jobs.Save(ctx, job); saveErr != nil {
			recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), updateJournalRecoveryTimeout)
			recoveryErr := p.jobs.Save(recoveryCtx, job)
			cancel()
			persistErr := fmt.Errorf("persist failed worker launch state: %w", saveErr)
			if recoveryErr != nil {
				p.updates.rememberFailedLaunch(job)
				persistErr = errors.Join(persistErr, fmt.Errorf("retry failed worker launch journal write: %w", recoveryErr))
			}
			return job, errors.Join(err, persistErr)
		}
		return job, err
	}
	p.log.Info("update worker requested", slog.String("job", job.ID), slog.String("tag", job.TargetTag), slog.Bool("prior_running", job.PriorRunning))
	return job, nil
}

func (p *panel) checkFinished(ctx context.Context, result updateCheckResult) error {
	if result.panelID != p.lastID {
		return nil
	}
	if result.err != nil {
		p.log.Warn("update check failed", slog.String("err", result.err.Error()))
		return p.show(ctx, result.panelID, view{"<b>Update check failed.</b> Try again later or check daemon.log.", []domain.Button{{Text: "Check for updates", Data: updateCheckData}, {Text: panelBack, Data: dataHome()}}})
	}
	check := result.check
	text := "<b>Plugin updates</b>\nInstalled: " + html.EscapeString(check.Installed)
	if check.Running != "" {
		text += "\nRunning: " + html.EscapeString(check.Running)
	}
	buttons := []domain.Button{{Text: "Check again", Data: updateCheckData}, {Text: panelBack, Data: dataHome()}}
	if !check.Available {
		text += "\nNo newer release found."
	} else {
		text += "\nNew release: " + html.EscapeString(check.Release.Tag)
		if check.Signer != "" {
			text += " · signed"
		}
		if check.Blocker != "" {
			text += "\n" + html.EscapeString(check.Blocker)
		}
		if check.IntentID != "" {
			buttons = append([]domain.Button{{Text: "Update", Data: updateInstallPrefix + check.IntentID}}, buttons...)
		}
	}
	if !check.Available && check.Blocker != "" {
		text += "\n" + html.EscapeString(check.Blocker)
	}
	return p.show(ctx, result.panelID, view{text, buttons})
}

func (p *panel) startFinished(ctx context.Context, result updateStartResult) error {
	if result.panelID != p.lastID {
		return nil
	}
	if result.err != nil && result.job.ErrorCode == "worker_launch" {
		p.log.Warn("update start failed", slog.String("err", result.err.Error()))
		text := "<b>Update could not start.</b> " + html.EscapeString(result.err.Error())
		text += "\nInspect update.json and daemon.log before retrying."
		return p.show(ctx, result.panelID, view{text, []domain.Button{{Text: "Check again", Data: updateCheckData}}})
	}
	if p.jobs != nil {
		if active, err := p.jobs.Load(ctx); err == nil && active.PanelMessageID == result.panelID {
			if active.Terminal() {
				return nil
			}
			if active.Phase != "" && active.Phase != "interrupted" {
				return p.show(ctx, result.panelID, view{"<b>Updating to " + html.EscapeString(active.TargetTag) + "…</b>\nThe result will appear here when the worker finishes.", nil})
			}
		}
	}
	if result.err != nil {
		p.log.Warn("update start failed", slog.String("err", result.err.Error()))
		return p.show(ctx, result.panelID, view{"<b>Update could not start.</b> " + html.EscapeString(result.err.Error()), []domain.Button{{Text: "Check again", Data: updateCheckData}}})
	}
	return p.show(ctx, result.panelID, view{"<b>Updating to " + html.EscapeString(result.job.TargetTag) + "…</b>\nThe result will appear here when the worker finishes.", nil})
}

func (p *panel) restoreUpdate(ctx context.Context, job domain.UpdateJob) error {
	p.lastID = job.PanelMessageID
	text := "<b>Updating to " + html.EscapeString(job.TargetTag) + "…</b>\nPhase: " + html.EscapeString(job.Phase)
	if job.Phase == "interrupted" {
		text = "<b>Update interrupted.</b> Inspect update.json and daemon.log before retrying."
	}
	if err := p.tg.EditText(ctx, job.PanelMessageID, text, true, nil); err != nil {
		p.log.Warn("update progress could not be restored", slog.Int("panel", job.PanelMessageID), slog.String("err", err.Error()))
		return nil
	}
	p.log.Info("update progress restored", slog.String("job", job.ID), slog.String("phase", job.Phase))
	return nil
}
