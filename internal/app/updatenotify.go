package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// DeliverUpdateResult edits one previously-created General message. A retry
// after a crash edits that same id, never sends a second completion message.
func DeliverUpdateResult(ctx context.Context, store domain.UpdateJobStore, notifier domain.UpdateNotifier, chatID int64, log *slog.Logger) error {
	job, err := store.Load(ctx)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !(job.Terminal() || job.Phase == "interrupted") || job.NotificationStatus == "sent" {
		return nil
	}
	if job.ChatID != chatID || job.PanelMessageID <= 0 {
		return fmt.Errorf("update result has a different chat or no panel")
	}
	message := "Plugin update to " + job.TargetTag + ": "
	switch job.Phase {
	case "succeeded":
		message += "installed successfully."
		if job.PriorRunning {
			message += " The new daemon is running and Telegram is ready."
		} else {
			message += " The daemon remains stopped; start it when needed."
		}
	case "rolled_back":
		message += "failed; the previous version was restored. Check daemon.log."
	case "stuck":
		message += "failed and rollback needs manual recovery. Check update.json and daemon.log."
	case "interrupted":
		message += "was interrupted. Inspect update.json and daemon.log before trying again."
	default:
		message += "could not start. Check daemon.log."
	}
	if err := notifier.EditUpdate(ctx, job.PanelMessageID, message); err != nil {
		return err
	}
	job.NotificationStatus = "sent"
	if err := store.Save(ctx, job); err != nil {
		return err
	}
	if log != nil {
		log.Info("update result delivered", slog.String("job", job.ID), slog.String("phase", job.Phase), slog.Int("panel", job.PanelMessageID))
	}
	return nil
}
