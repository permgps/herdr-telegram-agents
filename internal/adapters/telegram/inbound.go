package telegram

import (
	"context"
	"log/slog"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// registerHandlers installs the two match functions. The allow-list (chat
// id, then operator id) is enforced in the match funcs so nothing else runs
// for foreign traffic; unmatched updates reach the no-op default handler.
func (g *Gateway) registerHandlers() {
	g.api.RegisterHandlerMatchFunc(g.matchOurTopic, g.onTopicUpdate)
	g.api.RegisterHandlerMatchFunc(g.matchMyChatMember, g.onMyChatMember)
}

// matchOurTopic accepts new messages posted by an operator inside a topic
// of the configured chat. General-topic traffic (no thread id) and edited
// messages are dropped. Service messages caused by the bot's own topic
// edits carry the bot as sender and fail the operator check, which keeps
// them from echoing back as events.
func (g *Gateway) matchOurTopic(u *models.Update) bool {
	m := u.Message
	if m == nil {
		return false
	}
	var fromID int64
	if m.From != nil {
		fromID = m.From.ID
	}
	switch {
	case m.Chat.ID != g.chatID:
		g.drop("foreign_chat", m.Chat.ID, fromID)
		return false
	case !m.IsTopicMessage || m.MessageThreadID == 0:
		g.drop("general_topic", m.Chat.ID, fromID)
		return false
	case !g.operators[fromID]:
		g.drop("not_operator", m.Chat.ID, fromID)
		return false
	}
	return true
}

// matchMyChatMember accepts membership changes of the bot in the chat.
func (g *Gateway) matchMyChatMember(u *models.Update) bool {
	cm := u.MyChatMember
	if cm == nil {
		return false
	}
	if cm.Chat.ID != g.chatID {
		g.drop("foreign_chat", cm.Chat.ID, cm.From.ID)
		return false
	}
	return true
}

func (g *Gateway) drop(reason string, chatID, fromID int64) {
	g.log.Debug("telegram update dropped",
		slog.String("reason", reason), slog.Int64("chat_id", chatID), slog.Int64("from_id", fromID))
}

// onTopicUpdate translates a topic message into a domain event. Service
// payloads are checked before text because a service message has no text.
func (g *Gateway) onTopicUpdate(ctx context.Context, _ *bot.Bot, u *models.Update) {
	m := u.Message
	thread := m.MessageThreadID
	switch {
	case m.ForumTopicEdited != nil:
		if m.ForumTopicEdited.Name == "" {
			g.log.Debug("telegram topic icon edit ignored", slog.Int("thread_id", thread))
			return
		}
		g.emit(ctx, "topic_renamed", thread, domain.TopicRenamed{ThreadID: thread, Name: m.ForumTopicEdited.Name})
	case m.ForumTopicClosed != nil:
		g.emit(ctx, "topic_closed", thread, domain.TopicClosed{ThreadID: thread})
	case m.ForumTopicReopened != nil:
		g.emit(ctx, "topic_reopened", thread, domain.TopicReopened{ThreadID: thread})
	case m.Text != "":
		g.emit(ctx, "topic_message", thread, domain.TopicMessage{ThreadID: thread, FromID: m.From.ID, Text: m.Text})
	default:
		g.drop("unsupported_message", m.Chat.ID, m.From.ID)
	}
}

// onMyChatMember reports whether the bot can still manage topics.
func (g *Gateway) onMyChatMember(ctx context.Context, _ *bot.Bot, u *models.Update) {
	g.emit(ctx, "rights_changed", 0, domain.RightsChanged{CanManageTopics: canManageTopics(u.MyChatMember.NewChatMember)})
}

func canManageTopics(m models.ChatMember) bool {
	return m.Type == models.ChatMemberTypeAdministrator && m.Administrator != nil && m.Administrator.CanManageTopics
}

// emit hands an event to the application without blocking past the update
// context or the gateway's lifetime.
func (g *Gateway) emit(ctx context.Context, kind string, thread int, ev domain.Event) {
	select {
	case g.events <- ev:
		g.log.Debug("telegram event", slog.String("kind", kind), slog.Int("thread_id", thread))
	case <-ctx.Done():
		g.log.Warn("telegram event dropped, context done", slog.String("kind", kind), slog.Int("thread_id", thread))
	case <-g.stopped:
		g.log.Debug("telegram event dropped, gateway stopped", slog.String("kind", kind), slog.Int("thread_id", thread))
	}
}
