package telegram

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// registerHandlers installs the match functions. The allow-list (chat id,
// then operator id) is enforced in the match funcs so nothing else runs for
// foreign traffic; unmatched updates reach the no-op default handler. The
// bot's own service messages are matched first so they never reach the
// operator check; General-topic commands come after topic traffic.
func (g *Gateway) registerHandlers() {
	g.api.RegisterHandlerMatchFunc(g.matchOwnService, g.onOwnService)
	g.api.RegisterHandlerMatchFunc(g.matchCallback, g.onCallback)
	g.api.RegisterHandlerMatchFunc(g.matchOurTopic, g.onTopicUpdate)
	g.api.RegisterHandlerMatchFunc(g.matchGeneral, g.onGeneral)
	g.api.RegisterHandlerMatchFunc(g.matchMyChatMember, g.onMyChatMember)
}

// matchOwnService accepts the notices Telegram posts into a topic when the
// bot itself edits, closes or reopens it ("X changed the topic icon"). With
// a status change per agent event they bury the conversation, so they are
// deleted. Creation notices are excluded: the Bot API refuses to delete
// them.
func (g *Gateway) matchOwnService(u *models.Update) bool {
	m := u.Message
	if m == nil || m.From == nil || g.botID == 0 || m.From.ID != g.botID || m.Chat.ID != g.chatID {
		return false
	}
	return m.ForumTopicEdited != nil || m.ForumTopicClosed != nil || m.ForumTopicReopened != nil
}

// matchCallback accepts button presses under the bot's messages in the
// configured chat. The operator check happens in onCallback, which has the
// context needed to answer a stranger's press so their client stops
// spinning.
func (g *Gateway) matchCallback(u *models.Update) bool {
	q := u.CallbackQuery
	if q == nil {
		return false
	}
	m := callbackMessage(q)
	if m == nil {
		g.drop("callback_without_message", 0, q.From.ID)
		return false
	}
	if m.Chat.ID != g.chatID {
		g.drop("foreign_chat", m.Chat.ID, q.From.ID)
		return false
	}
	return true
}

// callbackMessage returns the accessible message a callback was pressed
// under, or nil when Telegram withheld it.
func callbackMessage(q *models.CallbackQuery) *models.Message {
	if q.Message.Type != models.MaybeInaccessibleMessageTypeMessage {
		return nil
	}
	return q.Message.Message
}

// matchOurTopic accepts new messages posted by an operator inside a topic
// of the configured chat. General-topic traffic (no thread id) is left to
// matchGeneral; edited messages are dropped. Service messages caused by the
// bot's own topic edits carry the bot as sender and fail the operator
// check, which keeps them from echoing back as events.
func (g *Gateway) matchOurTopic(u *models.Update) bool {
	m := u.Message
	if m == nil {
		return false
	}
	switch {
	case m.Chat.ID != g.chatID:
		g.drop("foreign_chat", m.Chat.ID, senderID(m))
		return false
	case isGeneral(m):
		return false
	case !g.operators[senderID(m)]:
		g.drop("not_operator", m.Chat.ID, senderID(m))
		return false
	}
	return true
}

// matchGeneral accepts slash commands an operator writes in the General
// topic of the configured chat. Everything else posted there is dropped:
// General is a control panel, not an agent.
func (g *Gateway) matchGeneral(u *models.Update) bool {
	m := u.Message
	if m == nil || m.Chat.ID != g.chatID || !isGeneral(m) {
		return false
	}
	switch {
	case !g.operators[senderID(m)]:
		g.drop("not_operator", m.Chat.ID, senderID(m))
	case !strings.HasPrefix(m.Text, "/"):
		g.drop("general_topic", m.Chat.ID, senderID(m))
	default:
		return true
	}
	return false
}

// isGeneral reports whether the message was posted outside any topic.
func isGeneral(m *models.Message) bool {
	return !m.IsTopicMessage || m.MessageThreadID == 0
}

func senderID(m *models.Message) int64 {
	if m.From == nil {
		return 0
	}
	return m.From.ID
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
		g.log.Debug("telegram topic message", slog.Int("thread_id", thread), slog.Int("message_id", m.ID), slog.Int("len", len(m.Text)))
		g.emit(ctx, "topic_message", thread, domain.TopicMessage{ThreadID: thread, MessageID: m.ID, FromID: m.From.ID, Text: m.Text})
	default:
		g.drop("unsupported_message", m.Chat.ID, m.From.ID)
	}
}

// onCallback translates an operator's button press into a domain event. A
// press by anyone else is answered with a refusal and dropped.
func (g *Gateway) onCallback(ctx context.Context, _ *bot.Bot, u *models.Update) {
	q := u.CallbackQuery
	m := callbackMessage(q)
	if !g.operators[q.From.ID] {
		g.drop("not_operator", m.Chat.ID, q.From.ID)
		if _, err := g.api.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "not allowed"}); err != nil {
			g.log.Debug("callback refusal not delivered", slog.Int64("from_id", q.From.ID), slog.Any("err", translate(err)))
			return
		}
		g.log.Debug("callback refused", slog.Int64("from_id", q.From.ID), slog.Int("message_id", m.ID))
		return
	}
	g.log.Debug("telegram button pressed", slog.Int("thread_id", m.MessageThreadID), slog.Int("message_id", m.ID),
		slog.Int64("from_id", q.From.ID), slog.String("data", q.Data))
	g.emit(ctx, "button_pressed", m.MessageThreadID, domain.ButtonPressed{
		CallbackID: q.ID, ThreadID: m.MessageThreadID, MessageID: m.ID, FromID: q.From.ID, Data: q.Data,
	})
}

// onGeneral translates a General-topic command into a domain event.
func (g *Gateway) onGeneral(ctx context.Context, _ *bot.Bot, u *models.Update) {
	m := u.Message
	g.log.Debug("telegram general command", slog.Int("message_id", m.ID), slog.Int("len", len(m.Text)))
	g.emit(ctx, "general_command", 0, domain.GeneralCommand{MessageID: m.ID, FromID: m.From.ID, Text: m.Text})
}

// onOwnService deletes one of the bot's own topic notices. The call is
// queued from a goroutine so the poller is not held behind the rate limit;
// the update context ends with polling, which bounds the goroutine.
func (g *Gateway) onOwnService(ctx context.Context, _ *bot.Bot, u *models.Update) {
	m := u.Message
	g.log.Debug("telegram own service message", slog.Int("thread_id", m.MessageThreadID), slog.Int("message_id", m.ID))
	go g.deleteServiceMessage(ctx, m.MessageThreadID, m.ID)
}

// deleteServiceMessage waits out the notice delay, then runs deleteMessage
// through the queue. The wait matters: clients apply the topic's new icon
// or name from the notice itself, and one deleted straight away was never
// seen by a phone that lagged a second behind. Deleting in a supergroup
// needs the "Delete messages" administrator right, which setup requests
// but an operator may withhold, so a failure is reported once at warn
// level with the right named and afterwards at debug only.
func (g *Gateway) deleteServiceMessage(ctx context.Context, threadID, messageID int) {
	attrs := []any{slog.Int("thread_id", threadID), slog.Int("message_id", messageID)}
	if g.noticeDelay > 0 {
		g.log.Debug("service message delete scheduled", append(attrs, slog.Int64("delay_ms", g.noticeDelay.Milliseconds()))...)
		select {
		case <-time.After(g.noticeDelay):
		case <-ctx.Done():
			g.log.Debug("service message not deleted, context done", attrs...)
			return
		}
	}
	err := g.queue.Do(ctx, func(ctx context.Context) error {
		_, err := g.api.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: g.chatID, MessageID: messageID})
		return translate(err)
	})
	switch {
	case err == nil:
		g.log.Debug("service message deleted", attrs...)
	case errors.Is(err, context.Canceled):
		g.log.Debug("service message not deleted, context done", attrs...)
	case g.deleteWarned.CompareAndSwap(false, true):
		g.log.Warn("service message not deleted: grant the bot the \"Delete messages\" right to keep topics free of edit notices",
			append(attrs, slog.Any("err", err))...)
	default:
		g.log.Debug("service message not deleted", append(attrs, slog.Any("err", err))...)
	}
}

// onMyChatMember reports whether the bot can still manage topics.
func (g *Gateway) onMyChatMember(ctx context.Context, _ *bot.Bot, u *models.Update) {
	g.emit(ctx, "rights_changed", 0, domain.RightsChanged{CanManageTopics: canManageTopics(u.MyChatMember.NewChatMember)})
}

func canManageTopics(m models.ChatMember) bool {
	return m.Type == models.ChatMemberTypeAdministrator && m.Administrator != nil && m.Administrator.CanManageTopics
}

func canDeleteMessages(m models.ChatMember) bool {
	return m.Type == models.ChatMemberTypeAdministrator && m.Administrator != nil && m.Administrator.CanDeleteMessages
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
