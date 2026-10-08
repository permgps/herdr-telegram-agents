package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

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
		bot.WithErrorsHandler(errorsHandler(token, log, fatal, time.Now)),
	}
	b, err := bot.New(token, append(base, opts...)...)
	if err != nil {
		return nil, fmt.Errorf("telegram bot: %w", err)
	}
	return b, nil
}

// pollWarnEvery spaces the warnings about transient polling errors. A
// network outage makes the library retry every few seconds; one warning per
// interval with a count keeps the outage visible without flooding the log.
const pollWarnEvery = 10 * time.Minute

// errorsHandler receives polling and form-building errors from the library,
// which never stops polling on its own. 401 and 409 are final: log and call
// fatal. A cancelled context is the normal shutdown path and is ignored. Any
// other error is warned on first sight and then at most once per
// pollWarnEvery, with the count suppressed since the last warning; the ones
// in between go to debug. now is injected so tests need no real time.
func errorsHandler(token string, log *slog.Logger, fatal context.CancelFunc, now func() time.Time) bot.ErrorsHandler {
	var (
		mu         sync.Mutex
		lastWarn   time.Time
		suppressed int
	)
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
			mu.Lock()
			defer mu.Unlock()
			t := now()
			if lastWarn.IsZero() {
				log.Warn("telegram polling error", slog.String("err", redact(err, token)))
				lastWarn = t
				return
			}
			if t.Sub(lastWarn) < pollWarnEvery {
				suppressed++
				log.Debug("telegram polling error", slog.String("err", redact(err, token)), slog.Int("suppressed", suppressed))
				return
			}
			log.Warn("telegram polling error", slog.String("err", redact(err, token)),
				slog.Int("suppressed", suppressed), slog.Int64("since_s", int64(t.Sub(lastWarn)/time.Second)))
			lastWarn = t
			suppressed = 0
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

// Check verifies the token with getMe and clears any webhook, retaining
// pending updates so private contacts can be registered. It returns the bot identity.
func Check(ctx context.Context, b *bot.Bot, log *slog.Logger) (BotIdentity, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	me, err := b.GetMe(ctx)
	if err != nil {
		return BotIdentity{}, fmt.Errorf("getMe: %w", translate(err))
	}
	id := BotIdentity{ID: me.ID, Username: me.Username, HasTopicsEnabled: me.HasTopicsEnabled}
	log.Info("telegram bot identified", slog.Int64("bot_id", id.ID), slog.String("username", id.Username))
	log.Info("private topics capability", slog.Bool("enabled", id.HasTopicsEnabled))
	// nil keeps the default drop_pending_updates=false and avoids an empty
	// multipart form, which the live endpoint may answer with an empty body.
	if _, err := b.DeleteWebhook(ctx, nil); err != nil {
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
// status, options and help, in General (options, new, observers, away and
// here only there).
var botCommands = []models.BotCommand{
	{Command: "share", Description: "Share this agent with a private contact"},
	{Command: "shares", Description: "Manage shared access"},
	{Command: "screen", Description: "Show screen; idle OpenCode/Codex/agy/Pi/Muse: last reply (N: screen tail, all: history)"},
	{Command: "keys", Description: "Send raw keys to the agent, e.g. /keys esc"},
	{Command: "focus", Description: "Bring the agent's pane to the front in Herdr"},
	{Command: "git", Description: "git status | diff [staged] | log [N] in the agent's directory"},
	{Command: "stop", Description: "Send esc to the agent: cancel the running turn or dialog"},
	{Command: "interrupt", Description: "Send ctrl+c to the agent"},
	{Command: "clear", Description: "Claude Code /clear: start a fresh conversation (idle agents only)"},
	{Command: "compact", Description: "Claude Code /compact [instructions]: compact the context"},
	{Command: "usage", Description: "/usage: show the usage panel; Claude Code's is closed for you afterwards"},
	{Command: "model", Description: "/model [name]: show the picker or set the model; non-Claude pickers stay open"},
	{Command: "models", Description: "OpenCode /models: the model picker, stays open for /keys"},
	{Command: "close", Description: "Close the agent's pane (asks Yes/No)"},
	{Command: "new", Description: "Start an agent: /new <workspace> [kind] (General)"},
	{Command: "observers", Description: "List or change observers: /observers [add|remove <id>] (General)"},
	{Command: "status", Description: "Agent status here, all agents in General"},
	{Command: "away", Description: "Treat me as away (General): /away or /away 2h"},
	{Command: "here", Description: "Back to automatic presence (General)"},
	{Command: "options", Description: "Settings and update check (General)"},
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

func (g *Gateway) RegisterPrivateCommands(ctx context.Context, chat int64, commands []string) error {
	if chat <= 0 {
		return errors.New("invalid private command destination")
	}
	seen := map[string]bool{}
	menu := make([]models.BotCommand, 0, len(commands))
	for _, name := range commands {
		if seen[name] {
			continue
		}
		seen[name] = true
		menu = append(menu, models.BotCommand{Command: name, Description: "Shared agent: " + name})
	}
	return g.queue.Do(ctx, func(ctx context.Context) error {
		_, err := g.api.SetMyCommands(ctx, &bot.SetMyCommandsParams{Commands: menu, Scope: &models.BotCommandScopeChat{ChatID: chat}})
		return translate(err)
	})
}
