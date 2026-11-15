package telegram

import (
	"context"
	"errors"
	"fmt"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"log/slog"
	"unicode/utf8"
)

// CreateTopic creates a forum topic with the status icon; icon_color is the
// fallback Telegram applies when no custom emoji id is given.
func (g *Gateway) CreateTopic(ctx context.Context, name string, status domain.Status) (domain.Topic, error) {
	return g.CreateTopicAt(ctx, g.chatID, name, status, nil)
}

func (g *Gateway) CreateTopicAt(ctx context.Context, chatID int64, name string, status domain.Status, guard domain.DispatchGuard) (domain.Topic, error) {
	icon := g.iconFor(status)
	name = truncateName(name, topicNameMax)
	var topic domain.Topic
	do := g.destinationQueue(chatID).DoCreateGuard
	if chatID > 0 {
		do = g.destinationQueue(chatID).DoOnceGuard
	}
	err := do(ctx, guard, func(ctx context.Context) error {
		t, err := g.api.CreateForumTopic(ctx, &bot.CreateForumTopicParams{
			ChatID:            chatID,
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
	if err != nil {
		return domain.Topic{}, g.finish("createForumTopic", err,
			slog.Int("name_len", utf8.RuneCountInString(name)), slog.String("icon", icon.EmojiID))
	}
	return topic, g.finish("createForumTopic", err,
		slog.Int("thread_id", topic.ThreadID),
		slog.Int("name_len", utf8.RuneCountInString(name)),
		slog.String("icon", icon.EmojiID))
}

// EditTopic batches a rename and an icon change into one editForumTopic
// call and skips the call when the patch changes nothing.
func (g *Gateway) EditTopic(ctx context.Context, threadID int, patch domain.TopicPatch) error {
	return g.EditTopicAt(ctx, domain.TopicAddress{ChatID: g.chatID, ThreadID: threadID}, patch, nil)
}

func (g *Gateway) EditTopicAt(ctx context.Context, address domain.TopicAddress, patch domain.TopicPatch, guard domain.DispatchGuard) error {
	params := &bot.EditForumTopicParams{ChatID: address.ChatID, MessageThreadID: address.ThreadID}
	if patch.Name != nil {
		params.Name = truncateName(*patch.Name, topicNameMax)
	}
	if patch.Status != nil {
		params.IconCustomEmojiID = g.iconFor(*patch.Status).EmojiID
	}
	if params.Name == "" && params.IconCustomEmojiID == "" {
		g.log.Debug("editForumTopic skipped", slog.Int("thread_id", address.ThreadID), slog.Bool("empty_patch", patch.Empty()))
		return nil
	}
	err := g.destinationQueue(address.ChatID).DoGuard(ctx, guard, func(ctx context.Context) error {
		_, err := g.api.EditForumTopic(ctx, params)
		return translate(err)
	})
	if errors.Is(err, ErrTopicNotModified) {
		// Telegram already holds this name and icon: the desired state is
		// reached, which is all the caller wants to know.
		g.log.Debug("editForumTopic already in place", slog.Int("thread_id", address.ThreadID))
		err = nil
	}
	return g.finish("editForumTopic", err,
		slog.Int("thread_id", address.ThreadID),
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

// DeleteTopic deletes the topic with every message in it; Telegram needs
// the can_delete_messages right for it. A topic that is already gone
// translates to domain.ErrTopicGone.
func (g *Gateway) DeleteTopic(ctx context.Context, threadID int) error {
	return g.DeleteTopicAt(ctx, domain.TopicAddress{ChatID: g.chatID, ThreadID: threadID}, nil)
}

func (g *Gateway) DeleteTopicAt(ctx context.Context, address domain.TopicAddress, guard domain.DispatchGuard) error {
	err := g.destinationQueue(address.ChatID).DoGuard(ctx, guard, func(ctx context.Context) error {
		_, err := g.api.DeleteForumTopic(ctx, &bot.DeleteForumTopicParams{ChatID: address.ChatID, MessageThreadID: address.ThreadID})
		return translate(err)
	})
	return g.finish("deleteForumTopic", err, slog.Int("thread_id", address.ThreadID))
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
// administrator allowed to manage topics, delete messages and pin
// messages. Both calls go through the queue.
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
		return domain.Rights{}, g.finish("getChat", err, slog.Int64("chat_id", g.chatID))
	}
	var membership domain.Rights
	err = g.queue.Do(ctx, func(ctx context.Context) error {
		member, err := g.api.GetChatMember(ctx, &bot.GetChatMemberParams{ChatID: g.chatID, UserID: g.botID})
		if err != nil {
			return fmt.Errorf("getChatMember: %w", translate(err))
		}
		membership.IsAdmin = member.Type == models.ChatMemberTypeAdministrator || member.Type == models.ChatMemberTypeOwner
		membership.CanManageTopics = canManageTopics(*member) || member.Type == models.ChatMemberTypeOwner
		membership.CanDeleteMessages = canDeleteMessages(*member) || member.Type == models.ChatMemberTypeOwner
		membership.CanPinMessages = canPinMessages(*member) || member.Type == models.ChatMemberTypeOwner
		return nil
	})
	if err == nil {
		membership.IsForum = rights.IsForum
		rights = membership
	}
	return rights, g.finish("getChatMember", err,
		slog.Bool("forum", rights.IsForum), slog.Bool("admin", rights.IsAdmin),
		slog.Bool("manage_topics", rights.CanManageTopics), slog.Bool("delete_messages", rights.CanDeleteMessages),
		slog.Bool("pin_messages", rights.CanPinMessages))
}
