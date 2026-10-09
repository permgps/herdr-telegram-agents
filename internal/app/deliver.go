package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// deliverText sends the free text of a ✏️ answer to the agent as a prompt.
// Herdr 0.9.3 refuses a prompt for an agent waiting at a question or
// approval dialog (ErrAgentBlocked) before any input reaches the pane, so
// the text is then typed into the dialog's text box as one line
// (domain.DialogText) and submitted with enter. Only the ✏️ flow may call
// it: the operator chose the text entry, so a text box is open. Any other
// text typed into a dialog would land in a select menu whose enter
// confirms the highlighted option, often the approval. typed reports that the
// fallback ran and pressed enter; it is also true when the text was typed
// but the enter failed. Any other prompt error is returned unchanged.
func deliverText(ctx context.Context, h domain.HerdrGateway, log *slog.Logger, paneID, text string) (typed bool, err error) {
	err = h.Prompt(ctx, paneID, text)
	if !errors.Is(err, domain.ErrAgentBlocked) {
		return false, err
	}
	line, lines := domain.DialogText(text)
	if line == "" {
		log.Info("prompt refused at dialog, nothing to type", slog.String("pane", paneID), slog.Int("text_len", len(text)))
		return false, err
	}
	log.Info("prompt refused at dialog, typing into pane", slog.String("pane", paneID),
		slog.Int("text_len", len(line)), slog.Int("lines", lines))
	if err := h.SendText(ctx, paneID, line); err != nil {
		log.Warn("dialog typing failed", slog.String("pane", paneID), slog.String("step", "send_text"), slog.String("err", err.Error()))
		return false, fmt.Errorf("type into dialog: %w", err)
	}
	log.Debug("dialog text typed", slog.String("pane", paneID))
	if err := h.SendKeys(ctx, paneID, []string{domain.KeyEnter}); err != nil {
		log.Warn("dialog typing failed", slog.String("pane", paneID), slog.String("step", "enter"), slog.String("err", err.Error()))
		return true, fmt.Errorf("press enter after typing into dialog: %w", err)
	}
	log.Debug("dialog text submitted", slog.String("pane", paneID))
	return true, nil
}
