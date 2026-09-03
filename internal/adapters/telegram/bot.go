package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// BotIdentity is what getMe reports about the bot behind the token.
type BotIdentity = domain.BotIdentity

// NewBot builds the client without touching the network: the startup getMe
// is skipped (it cannot be cancelled), handlers run inline so per-topic
// order is kept, and only the update kinds the plugin consumes are polled.
// fatal is called when polling can never succeed (401 invalid token, 409
// another poller) so the daemon can stop. Extra opts are applied last; tests
// use them for bot.WithServerURL.
func NewBot(token string, log *slog.Logger, fatal context.CancelFunc, opts ...bot.Option) (*bot.Bot, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if fatal == nil {
		fatal = func() {}
	}
	base := []bot.Option{
		bot.WithSkipGetMe(),
		bot.WithNotAsyncHandlers(),
		bot.WithAllowedUpdates(bot.AllowedUpdates{
			models.AllowedUpdateMessage,
			models.AllowedUpdateEditedMessage,
			models.AllowedUpdateCallbackQuery,
			models.AllowedUpdateMyChatMember,
		}),
		bot.WithDefaultHandler(func(context.Context, *bot.Bot, *models.Update) {}),
		bot.WithErrorsHandler(errorsHandler(token, log, fatal)),
	}
	b, err := bot.New(token, append(base, opts...)...)
	if err != nil {
		return nil, fmt.Errorf("telegram bot: %w", err)
	}
	return b, nil
}

// errorsHandler receives polling and form-building errors from the library,
// which never stops polling on its own. 401 and 409 are final: log and call
// fatal. A cancelled context is the normal shutdown path and is ignored.
func errorsHandler(token string, log *slog.Logger, fatal context.CancelFunc) bot.ErrorsHandler {
	return func(err error) {
		switch {
		case errors.Is(err, context.Canceled):
		case errors.Is(err, bot.ErrorUnauthorized):
			log.Error("telegram bot token rejected, stopping", slog.String("err", redact(err, token)))
			fatal()
		case errors.Is(err, bot.ErrorConflict):
			log.Error("another poller owns this bot, stopping", slog.String("err", redact(err, token)))
			fatal()
		default:
			log.Warn("telegram polling error", slog.String("err", redact(err, token)))
		}
	}
}

// redact hides the token in an error message. The library already masks it
// in transport errors; this covers every other shape.
func redact(err error, token string) string {
	if err == nil {
		return ""
	}
	if token == "" {
		return err.Error()
	}
	return strings.ReplaceAll(err.Error(), token, "***")
}

// Check verifies the token with getMe and clears any webhook (dropping
// pending updates) so long polling can start. It returns the bot identity.
func Check(ctx context.Context, b *bot.Bot, log *slog.Logger) (BotIdentity, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	me, err := b.GetMe(ctx)
	if err != nil {
		return BotIdentity{}, fmt.Errorf("getMe: %w", translate(err))
	}
	id := BotIdentity{ID: me.ID, Username: me.Username}
	log.Info("telegram bot identified", slog.Int64("bot_id", id.ID), slog.String("username", id.Username))
	if _, err := b.DeleteWebhook(ctx, &bot.DeleteWebhookParams{DropPendingUpdates: true}); err != nil {
		return id, fmt.Errorf("deleteWebhook: %w", translate(err))
	}
	log.Debug("telegram webhook cleared")
	return id, nil
}

// Poll runs long polling until ctx is done. Fatal polling errors reach the
// errors handler installed by NewBot, which cancels the context it was given.
func Poll(ctx context.Context, b *bot.Bot, log *slog.Logger) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	log.Info("telegram polling started")
	b.Start(ctx)
	log.Info("telegram polling stopped", slog.Any("reason", ctx.Err()))
}

// botCommands is the menu Telegram shows for "/" in the configured chat.
// Descriptions are one line each; the commands work in topics and, for
// status and help, in General.
var botCommands = []models.BotCommand{
	{Command: "screen", Description: "Show the agent screen (N: last N lines, all: output since your last message)"},
	{Command: "keys", Description: "Send raw keys to the agent, e.g. /keys esc"},
	{Command: "focus", Description: "Bring the agent's pane to the front in Herdr"},
	{Command: "clear", Description: "Claude Code /clear: start a fresh conversation (idle agents only)"},
	{Command: "compact", Description: "Claude Code /compact [instructions]: compact the context"},
	{Command: "usage", Description: "Claude Code /usage: show the usage panel, closed for you afterwards"},
	{Command: "model", Description: "Claude Code /model [name]: show the picker or set the model"},
	{Command: "status", Description: "Agent status here, all agents in General"},
	{Command: "help", Description: "List the commands"},
}

// RegisterCommands publishes the command menu scoped to the chat with
// setMyCommands. It runs once at connect, outside the queue, like the
// other startup calls. Failure is not fatal: the commands still work when
// typed, only the menu is missing.
func RegisterCommands(ctx context.Context, api *bot.Bot, chatID int64, log *slog.Logger) error {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	_, err := api.SetMyCommands(ctx, &bot.SetMyCommandsParams{
		Commands: botCommands,
		Scope:    &models.BotCommandScopeChat{ChatID: chatID},
	})
	if err = translate(err); err != nil {
		log.Warn("setMyCommands failed, command menu unavailable", slog.Int64("chat_id", chatID), slog.Any("err", err))
		return fmt.Errorf("setMyCommands: %w", err)
	}
	log.Info("commands registered", slog.Int64("chat_id", chatID), slog.Int("count", len(botCommands)))
	return nil
}
