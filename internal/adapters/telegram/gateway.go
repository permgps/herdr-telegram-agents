package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"unicode/utf8"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

const (
	// topicNameMax is Telegram's limit for a forum topic name.
	topicNameMax = 128
	// eventBuffer bounds inbound events waiting for the application.
	eventBuffer = 64
)

// Config selects the forum group and who may talk to the bot in it.
type Config struct {
	ChatID    int64
	Operators []int64
	Icons     IconSet
	// BotID is the bot's own user id, needed for the rights check.
	BotID int64
}

// Gateway implements domain.TelegramGateway on top of one bot client, a
// serial call queue and the update handlers registered at construction.
type Gateway struct {
	api       *bot.Bot
	chatID    int64
	botID     int64
	operators map[int64]bool
	icons     IconSet
	queue     *Queue
	events    chan domain.Event
	stopped   chan struct{}
	log       *slog.Logger
}

var _ domain.TelegramGateway = (*Gateway)(nil)

// NewGateway wires the gateway and registers its update handlers on api, so
// polling may start right after; call Run to serve outbound calls.
func NewGateway(api *bot.Bot, cfg Config, queue *Queue, log *slog.Logger) *Gateway {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	ops := make(map[int64]bool, len(cfg.Operators))
	for _, id := range cfg.Operators {
		ops[id] = true
	}
	g := &Gateway{
		api:       api,
		chatID:    cfg.ChatID,
		botID:     cfg.BotID,
		operators: ops,
		icons:     cfg.Icons,
		queue:     queue,
		events:    make(chan domain.Event, eventBuffer),
		stopped:   make(chan struct{}),
		log:       log,
	}
	g.registerHandlers()
	return g
}

// Run serves the outbound call queue until ctx is done. Inbound handlers
// stop emitting once it returns; the events channel is never closed because
// the poller may still be delivering updates.
func (g *Gateway) Run(ctx context.Context) {
	g.log.Info("telegram gateway started", slog.Int64("chat_id", g.chatID), slog.Int("operators", len(g.operators)))
	g.queue.Run(ctx)
	close(g.stopped)
	g.log.Info("telegram gateway stopped")
}

// Events returns inbound topic and membership events.
func (g *Gateway) Events() <-chan domain.Event { return g.events }

// CreateTopic creates a forum topic with the status icon; icon_color is the
// fallback Telegram applies when no custom emoji id is given.
func (g *Gateway) CreateTopic(ctx context.Context, name string, status domain.Status) (domain.Topic, error) {
	icon := g.icons.For(status)
	name = truncateName(name, topicNameMax)
	var topic domain.Topic
	err := g.queue.Do(ctx, func(ctx context.Context) error {
		t, err := g.api.CreateForumTopic(ctx, &bot.CreateForumTopicParams{
			ChatID:            g.chatID,
			Name:              name,
			IconColor:         icon.Color,
			IconCustomEmojiID: icon.EmojiID,
		})
		if err != nil {
			return translate(err)
		}
		topic = domain.Topic{ThreadID: t.MessageThreadID, Name: t.Name, IconEmojiID: t.IconCustomEmojiID}
		return nil
	})
	return topic, g.finish("createForumTopic", err,
		slog.Int("thread_id", topic.ThreadID),
		slog.Int("name_len", utf8.RuneCountInString(name)),
		slog.String("icon", icon.EmojiID))
}

// EditTopic batches a rename and an icon change into one editForumTopic
// call and skips the call when the patch changes nothing.
func (g *Gateway) EditTopic(ctx context.Context, threadID int, patch domain.TopicPatch) error {
	params := &bot.EditForumTopicParams{ChatID: g.chatID, MessageThreadID: threadID}
	if patch.Name != nil {
		params.Name = truncateName(*patch.Name, topicNameMax)
	}
	if patch.Status != nil {
		params.IconCustomEmojiID = g.icons.For(*patch.Status).EmojiID
	}
	if params.Name == "" && params.IconCustomEmojiID == "" {
		g.log.Debug("editForumTopic skipped", slog.Int("thread_id", threadID), slog.Bool("empty_patch", patch.Empty()))
		return nil
	}
	err := g.queue.Do(ctx, func(ctx context.Context) error {
		_, err := g.api.EditForumTopic(ctx, params)
		return translate(err)
	})
	return g.finish("editForumTopic", err,
		slog.Int("thread_id", threadID),
		slog.Int("name_len", utf8.RuneCountInString(params.Name)),
		slog.String("icon", params.IconCustomEmojiID))
}

// CloseTopic closes the topic; it stays readable and can be reopened.
func (g *Gateway) CloseTopic(ctx context.Context, threadID int) error {
	err := g.queue.Do(ctx, func(ctx context.Context) error {
		_, err := g.api.CloseForumTopic(ctx, &bot.CloseForumTopicParams{ChatID: g.chatID, MessageThreadID: threadID})
		return translate(err)
	})
	return g.finish("closeForumTopic", err, slog.Int("thread_id", threadID))
}

// ReopenTopic reopens a closed topic.
func (g *Gateway) ReopenTopic(ctx context.Context, threadID int) error {
	err := g.queue.Do(ctx, func(ctx context.Context) error {
		_, err := g.api.ReopenForumTopic(ctx, &bot.ReopenForumTopicParams{ChatID: g.chatID, MessageThreadID: threadID})
		return translate(err)
	})
	return g.finish("reopenForumTopic", err, slog.Int("thread_id", threadID))
}

// Rights reports whether the chat is a forum and whether the bot is an
// administrator allowed to manage topics. Both calls go through the queue.
func (g *Gateway) Rights(ctx context.Context) (domain.Rights, error) {
	var rights domain.Rights
	err := g.queue.Do(ctx, func(ctx context.Context) error {
		chat, err := g.api.GetChat(ctx, &bot.GetChatParams{ChatID: g.chatID})
		if err != nil {
			return fmt.Errorf("getChat: %w", translate(err))
		}
		rights.IsForum = chat.IsForum
		return nil
	})
	if err != nil {
		return rights, g.finish("getChat", err, slog.Int64("chat_id", g.chatID))
	}
	err = g.queue.Do(ctx, func(ctx context.Context) error {
		member, err := g.api.GetChatMember(ctx, &bot.GetChatMemberParams{ChatID: g.chatID, UserID: g.botID})
		if err != nil {
			return fmt.Errorf("getChatMember: %w", translate(err))
		}
		rights.IsAdmin = member.Type == models.ChatMemberTypeAdministrator || member.Type == models.ChatMemberTypeOwner
		rights.CanManageTopics = canManageTopics(*member) || member.Type == models.ChatMemberTypeOwner
		return nil
	})
	return rights, g.finish("getChatMember", err,
		slog.Bool("forum", rights.IsForum), slog.Bool("admin", rights.IsAdmin), slog.Bool("manage_topics", rights.CanManageTopics))
}

// SendText posts text into the topic, split into parts below Telegram's
// message limit, each as its own queued call. code wraps every part in
// <pre>. The first failure stops the remaining parts.
func (g *Gateway) SendText(ctx context.Context, threadID int, text string, code bool) error {
	parts := chunk(text, textMax)
	for i, part := range parts {
		body := renderPlain(part)
		if code {
			body = renderCode(part)
		}
		err := g.queue.Do(ctx, func(ctx context.Context) error {
			_, err := g.api.SendMessage(ctx, &bot.SendMessageParams{
				ChatID:              g.chatID,
				MessageThreadID:     threadID,
				Text:                body,
				ParseMode:           models.ParseModeHTML,
				DisableNotification: true,
			})
			return translate(err)
		})
		if err != nil {
			g.log.Warn("sendMessage failed",
				slog.Int("thread_id", threadID), slog.Int("part", i+1), slog.Int("parts", len(parts)), slog.Any("err", err))
			return fmt.Errorf("sendMessage thread %d part %d/%d: %w", threadID, i+1, len(parts), err)
		}
		g.log.Debug("sendMessage",
			slog.Int("thread_id", threadID), slog.Int("part", i+1), slog.Int("parts", len(parts)),
			slog.Int("runes", utf8.RuneCountInString(part)))
	}
	return nil
}

// finish logs the outcome of one call and wraps its error with the method
// name. Domain sentinels stay reachable through errors.Is.
func (g *Gateway) finish(method string, err error, attrs ...any) error {
	if err != nil {
		g.log.Warn(method+" failed", append(attrs, slog.Any("err", err))...)
		return fmt.Errorf("%s: %w", method, err)
	}
	g.log.Debug(method, attrs...)
	return nil
}
