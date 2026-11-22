package app

import (
	"context"
	"log/slog"
	"strings"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// show edits the panel message; when the edit fails for a non-fatal reason
// (the message is gone) a fresh panel is sent instead.
func (p *panel) show(ctx context.Context, messageID int, v view) error {
	if err := p.edit(ctx, messageID, v.text, v.buttons); err != nil {
		return err
	}
	if p.lastID == 0 {
		p.lastID = messageID
	}
	return nil
}

func (p *panel) edit(ctx context.Context, messageID int, text string, buttons []domain.Button) error {
	err := p.tg.EditText(ctx, messageID, text, true, buttons)
	if err == nil {
		return nil
	}
	if isFatal(err) {
		return err
	}
	p.log.Warn("options panel edit failed, sending a new panel", slog.Int("message_id", messageID), slog.String("err", err.Error()))
	for _, button := range buttons {
		if strings.HasPrefix(button.Data, updateInstallPrefix) {
			text += "\nThis panel moved. Check again before updating."
			buttons = []domain.Button{{Text: "Check again", Data: updateCheckData}}
			break
		}
	}
	id, err := p.tg.Send(ctx, domain.Outgoing{ThreadID: 0, Text: text, HTML: true, Buttons: buttons})
	if err != nil {
		if isFatal(err) {
			return err
		}
		p.log.Warn("options panel re-send failed", slog.String("err", err.Error()))
		return nil
	}
	p.lastID = id
	p.log.Info("options panel re-sent", slog.Int("message_id", id))
	return nil
}
