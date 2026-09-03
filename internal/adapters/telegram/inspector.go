package telegram

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// Inspector is the light Telegram client behind the doctor and send-test
// actions: single calls from a short-lived process, no queue, no polling
// and no webhook change, so a daemon polling the same bot is never
// disturbed. Timeouts come from the caller's context.
type Inspector struct {
	api    *bot.Bot
	chatID int64
	token  string
	log    *slog.Logger
}

var _ domain.TelegramInspector = (*Inspector)(nil)

// NewInspector builds the client for a token and chat. Extra opts are for
// tests (bot.WithServerURL).
func NewInspector(token string, chatID int64, log *slog.Logger, opts ...bot.Option) (*Inspector, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	api, err := NewBot(token, log, nil, opts...)
	if err != nil {
		return nil, err
	}
	return &Inspector{api: api, chatID: chatID, token: token, log: log}, nil
}

// Identity is getMe.
func (i *Inspector) Identity(ctx context.Context) (domain.BotIdentity, error) {
	me, err := i.api.GetMe(ctx)
	if err != nil {
		i.log.Warn("inspector getMe failed", slog.String("err", redact(err, i.token)))
		return domain.BotIdentity{}, fmt.Errorf("getMe: %w", translate(err))
	}
	i.log.Info("inspector identity", slog.Int64("bot_id", me.ID), slog.String("username", me.Username))
	return domain.BotIdentity{ID: me.ID, Username: me.Username}, nil
}

// Group reads the chat (title, forum flag) and the bot's membership; the
// bot id comes from getMe, so this is three calls.
func (i *Inspector) Group(ctx context.Context) (domain.GroupInfo, error) {
	me, err := i.Identity(ctx)
	if err != nil {
		return domain.GroupInfo{}, err
	}
	chat, err := i.api.GetChat(ctx, &bot.GetChatParams{ChatID: i.chatID})
	if err != nil {
		i.log.Warn("inspector getChat failed", slog.String("err", redact(err, i.token)))
		return domain.GroupInfo{}, fmt.Errorf("getChat: %w", translate(err))
	}
	info := domain.GroupInfo{Title: chat.Title, Rights: domain.Rights{IsForum: chat.IsForum}}
	member, err := i.api.GetChatMember(ctx, &bot.GetChatMemberParams{ChatID: i.chatID, UserID: me.ID})
	if err != nil {
		i.log.Warn("inspector getChatMember failed", slog.String("err", redact(err, i.token)))
		return info, fmt.Errorf("getChatMember: %w", translate(err))
	}
	owner := member.Type == models.ChatMemberTypeOwner
	info.Rights.IsAdmin = member.Type == models.ChatMemberTypeAdministrator || owner
	info.Rights.CanManageTopics = canManageTopics(*member) || owner
	info.Rights.CanDeleteMessages = canDeleteMessages(*member) || owner
	i.log.Info("inspector group", slog.String("title", info.Title), slog.Bool("forum", info.Rights.IsForum),
		slog.Bool("admin", info.Rights.IsAdmin), slog.Bool("manage_topics", info.Rights.CanManageTopics),
		slog.Bool("delete_messages", info.Rights.CanDeleteMessages))
	return info, nil
}

// SendTest posts text into General with a notification and returns the
// message id.
func (i *Inspector) SendTest(ctx context.Context, text string) (int, error) {
	msg, err := i.api.SendMessage(ctx, &bot.SendMessageParams{ChatID: i.chatID, Text: text})
	if err != nil {
		i.log.Warn("inspector sendMessage failed", slog.String("err", redact(err, i.token)))
		return 0, fmt.Errorf("sendMessage: %w", translate(err))
	}
	i.log.Info("test message sent", slog.Int("message_id", msg.ID))
	return msg.ID, nil
}
