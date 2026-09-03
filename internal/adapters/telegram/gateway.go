package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
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
	// NoticeDelay is how long a topic edit notice made by the bot stays
	// before it is deleted. Telegram clients learn a topic's new icon or
	// name from that service message; deleting it at once left phones
	// showing the old icon, so the notice is kept until connected clients
	// have applied it.
	NoticeDelay = 10 * time.Second
)

// Config selects the forum group and who may talk to the bot in it.
type Config struct {
	ChatID    int64
	Operators []int64
	Icons     IconSet
	// BotID is the bot's own user id, needed for the rights check.
	BotID int64
	// NoticeDelay defers the deletion of the bot's own topic edit notices;
	// zero deletes them as soon as they arrive. Production uses the
	// NoticeDelay constant.
	NoticeDelay time.Duration
}

// Gateway implements domain.TelegramGateway on top of one bot client, a
// serial call queue and the update handlers registered at construction.
type Gateway struct {
	api       *bot.Bot
	chatID    int64
	botID     int64
	operators map[int64]bool
	icons     IconSet
	// statusIcons is the emoji per status in force (SetStatusIcons);
	// iconWarned lists emoji already reported as missing from the pack.
	iconMu      sync.RWMutex
	statusIcons domain.StatusIcons
	iconWarned  map[string]bool
	queue       *Queue
	events      chan domain.Event
	stopped     chan struct{}
	log         *slog.Logger
	// noticeDelay is Config.NoticeDelay.
	noticeDelay time.Duration
	// deleteWarned is set after the first failed service-message deletion
	// so a missing right is reported once, not per edit.
	deleteWarned atomic.Bool
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
		api:         api,
		chatID:      cfg.ChatID,
		botID:       cfg.BotID,
		operators:   ops,
		icons:       cfg.Icons,
		statusIcons: domain.DefaultStatusIcons(),
		iconWarned:  map[string]bool{},
		queue:       queue,
		events:      make(chan domain.Event, eventBuffer),
		stopped:     make(chan struct{}),
		log:         log,

		noticeDelay: cfg.NoticeDelay,
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

// SetStatusIcons replaces the emoji used for topic icons from now on and
// logs which of them the pack resolves; unresolved ones leave an edited
// topic's icon unchanged and give a created topic only its colour.
func (g *Gateway) SetStatusIcons(icons domain.StatusIcons) {
	g.iconMu.Lock()
	g.statusIcons = icons
	g.iconWarned = map[string]bool{}
	g.iconMu.Unlock()
	var unresolved []string
	for _, st := range []domain.Status{
		domain.StatusWorking, domain.StatusIdle, domain.StatusBlocked,
		domain.StatusDone, domain.StatusUnknown, domain.StatusExited,
	} {
		if _, ok := g.icons.ID(icons.For(st)); !ok {
			unresolved = append(unresolved, string(st))
		}
	}
	g.log.Info("status icons set",
		slog.String("working", icons.Working), slog.String("idle", icons.Idle),
		slog.String("blocked", icons.Blocked), slog.String("done", icons.Done),
		slog.String("unknown", icons.Unknown), slog.String("exited", icons.Exited),
		slog.Any("unresolved", unresolved))
}

// IconPack lists the free topic-icon pack in Telegram's order.
func (g *Gateway) IconPack() []string { return g.icons.Emojis() }

// iconFor resolves the configured emoji of a status to an Icon and warns
// once per emoji when the pack lacks it.
func (g *Gateway) iconFor(status domain.Status) Icon {
	g.iconMu.RLock()
	emoji := g.statusIcons.For(status)
	g.iconMu.RUnlock()
	icon := g.icons.ForEmoji(emoji, status)
	if icon.EmojiID == "" && emoji != "" {
		g.iconMu.Lock()
		warned := g.iconWarned[emoji]
		g.iconWarned[emoji] = true
		g.iconMu.Unlock()
		if !warned {
			g.log.Warn("icon not in pack, topic icon unchanged", slog.String("emoji", emoji), slog.String("status", string(status)))
		}
	}
	return icon
}

// CreateTopic creates a forum topic with the status icon; icon_color is the
// fallback Telegram applies when no custom emoji id is given.
func (g *Gateway) CreateTopic(ctx context.Context, name string, status domain.Status) (domain.Topic, error) {
	icon := g.iconFor(status)
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
		params.IconCustomEmojiID = g.iconFor(*patch.Status).EmojiID
	}
	if params.Name == "" && params.IconCustomEmojiID == "" {
		g.log.Debug("editForumTopic skipped", slog.Int("thread_id", threadID), slog.Bool("empty_patch", patch.Empty()))
		return nil
	}
	err := g.queue.Do(ctx, func(ctx context.Context) error {
		_, err := g.api.EditForumTopic(ctx, params)
		return translate(err)
	})
	if errors.Is(err, ErrTopicNotModified) {
		// Telegram already holds this name and icon: the desired state is
		// reached, which is all the caller wants to know.
		g.log.Debug("editForumTopic already in place", slog.Int("thread_id", threadID))
		err = nil
	}
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
		rights.CanDeleteMessages = canDeleteMessages(*member) || member.Type == models.ChatMemberTypeOwner
		return nil
	})
	return rights, g.finish("getChatMember", err,
		slog.Bool("forum", rights.IsForum), slog.Bool("admin", rights.IsAdmin),
		slog.Bool("manage_topics", rights.CanManageTopics), slog.Bool("delete_messages", rights.CanDeleteMessages))
}

// Send posts one message, split into parts below Telegram's message limit,
// each as its own queued call. ThreadID 0 addresses the General topic, so
// message_thread_id is omitted. Code wraps every part in <pre>; HTML passes
// the part through as caller-escaped markup. ReplyTo quotes the operator's
// message on the first part only; Buttons ride on the last part as an
// inline keyboard. Messages are silent unless Notify is set. The first
// failure stops the remaining parts. The id of the last part is returned
// so the caller can edit its keyboard later.
func (g *Gateway) Send(ctx context.Context, out domain.Outgoing) (int, error) {
	parts := chunk(out.Text, textMax)
	lastID := 0
	for i, part := range parts {
		body := renderPlain(part)
		switch {
		case out.Code:
			body = renderCode(part)
		case out.HTML:
			body = part
		}
		params := &bot.SendMessageParams{
			ChatID:              g.chatID,
			MessageThreadID:     out.ThreadID,
			Text:                body,
			ParseMode:           models.ParseModeHTML,
			DisableNotification: !out.Notify,
		}
		if i == 0 && out.ReplyTo != 0 {
			params.ReplyParameters = &models.ReplyParameters{MessageID: out.ReplyTo, AllowSendingWithoutReply: true}
		}
		buttons := 0
		if i == len(parts)-1 && len(out.Buttons) > 0 {
			params.ReplyMarkup = inlineKeyboard(out.Buttons)
			buttons = len(out.Buttons)
		}
		err := g.queue.Do(ctx, func(ctx context.Context) error {
			msg, err := g.api.SendMessage(ctx, params)
			if err == nil && msg != nil {
				lastID = msg.ID
			}
			return translate(err)
		})
		if err != nil {
			g.log.Warn("sendMessage failed",
				slog.Int("thread_id", out.ThreadID), slog.Int("part", i+1), slog.Int("parts", len(parts)), slog.Any("err", err))
			return 0, fmt.Errorf("sendMessage thread %d part %d/%d: %w", out.ThreadID, i+1, len(parts), err)
		}
		g.log.Debug("sendMessage",
			slog.Int("thread_id", out.ThreadID), slog.Int("part", i+1), slog.Int("parts", len(parts)),
			slog.Int("reply_to", out.ReplyTo), slog.Bool("notify", out.Notify),
			slog.Int("runes", utf8.RuneCountInString(part)), slog.Int("buttons", buttons), slog.Int("message_id", lastID))
	}
	return lastID, nil
}

// inlineKeyboard renders buttons as an inline keyboard with one button per
// row, which keeps long option labels readable on a phone.
func inlineKeyboard(buttons []domain.Button) *models.InlineKeyboardMarkup {
	rows := make([][]models.InlineKeyboardButton, 0, len(buttons))
	lastRow := 0
	for _, b := range buttons {
		btn := models.InlineKeyboardButton{Text: b.Text, CallbackData: b.Data}
		if b.Row != 0 && b.Row == lastRow && len(rows) > 0 {
			rows[len(rows)-1] = append(rows[len(rows)-1], btn)
			continue
		}
		rows = append(rows, []models.InlineKeyboardButton{btn})
		lastRow = b.Row
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// EditText replaces the text and the keyboard of one of the bot's messages
// in one editMessageText call; an empty buttons slice removes the keyboard.
// "message is not modified" is the state the caller wants.
func (g *Gateway) EditText(ctx context.Context, messageID int, text string, html bool, buttons []domain.Button) error {
	if utf8.RuneCountInString(text) > textMax {
		text = string([]rune(text)[:textMax-1]) + "…"
	}
	params := &bot.EditMessageTextParams{
		ChatID:      g.chatID,
		MessageID:   messageID,
		Text:        text,
		ReplyMarkup: inlineKeyboard(buttons),
	}
	if html {
		params.ParseMode = models.ParseModeHTML
	}
	err := g.queue.Do(ctx, func(ctx context.Context) error {
		_, err := g.api.EditMessageText(ctx, params)
		return translate(err)
	})
	if errors.Is(err, ErrMessageNotModified) {
		g.log.Debug("editMessageText already in place", slog.Int("message_id", messageID))
		err = nil
	}
	return g.finish("editMessageText", err,
		slog.Int("message_id", messageID),
		slog.Int("chars", utf8.RuneCountInString(text)),
		slog.Int("buttons", len(buttons)))
}

// EditButtons replaces the inline keyboard of one of the bot's messages;
// an empty slice removes it. Telegram answers "message is not modified"
// when the keyboard already matches, which is the state the caller wants.
func (g *Gateway) EditButtons(ctx context.Context, messageID int, buttons []domain.Button) error {
	markup := inlineKeyboard(buttons)
	err := g.queue.Do(ctx, func(ctx context.Context) error {
		_, err := g.api.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{
			ChatID:      g.chatID,
			MessageID:   messageID,
			ReplyMarkup: markup,
		})
		return translate(err)
	})
	if errors.Is(err, ErrMessageNotModified) {
		g.log.Debug("editMessageReplyMarkup already in place", slog.Int("message_id", messageID))
		err = nil
	}
	return g.finish("editMessageReplyMarkup", err, slog.Int("message_id", messageID), slog.Int("buttons", len(buttons)))
}

// AnswerButton acknowledges a button press with a short toast. The call
// bypasses the serial queue on purpose: Telegram expects the answer within
// seconds or the client keeps its spinner, and answerCallbackQuery does not
// count against the per-group message limit the queue protects.
func (g *Gateway) AnswerButton(ctx context.Context, callbackID, text string) error {
	_, err := g.api.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: callbackID, Text: text})
	return g.finish("answerCallbackQuery", translate(err), slog.Int("text_len", len(text)))
}

// React puts one emoji reaction on a message. Telegram addresses reactions
// by message id alone; threadID only labels the log line.
// SendDocument uploads one file as a single silent message. ThreadID 0
// addresses the General topic. Content type detection is disabled so a
// .txt stays a plain file instead of being previewed as something else.
func (g *Gateway) SendDocument(ctx context.Context, doc domain.Document) error {
	params := &bot.SendDocumentParams{
		ChatID:                      g.chatID,
		MessageThreadID:             doc.ThreadID,
		Document:                    &models.InputFileUpload{Filename: doc.Name, Data: bytes.NewReader(doc.Data)},
		Caption:                     doc.Caption,
		DisableNotification:         true,
		DisableContentTypeDetection: true,
	}
	if doc.ReplyTo != 0 {
		params.ReplyParameters = &models.ReplyParameters{MessageID: doc.ReplyTo, AllowSendingWithoutReply: true}
	}
	err := g.queue.Do(ctx, func(ctx context.Context) error {
		_, err := g.api.SendDocument(ctx, params)
		return translate(err)
	})
	if err != nil {
		g.log.Warn("sendDocument failed",
			slog.Int("thread_id", doc.ThreadID), slog.String("name", doc.Name), slog.Int("bytes", len(doc.Data)), slog.Any("err", err))
		return fmt.Errorf("sendDocument thread %d %q: %w", doc.ThreadID, doc.Name, err)
	}
	g.log.Debug("sendDocument",
		slog.Int("thread_id", doc.ThreadID), slog.String("name", doc.Name), slog.Int("bytes", len(doc.Data)),
		slog.Int("reply_to", doc.ReplyTo), slog.Int("caption_len", len(doc.Caption)))
	return nil
}

func (g *Gateway) React(ctx context.Context, threadID, messageID int, emoji string) error {
	err := g.queue.Do(ctx, func(ctx context.Context) error {
		_, err := g.api.SetMessageReaction(ctx, &bot.SetMessageReactionParams{
			ChatID:    g.chatID,
			MessageID: messageID,
			Reaction: []models.ReactionType{{
				Type:              models.ReactionTypeTypeEmoji,
				ReactionTypeEmoji: &models.ReactionTypeEmoji{Emoji: emoji},
			}},
		})
		return translate(err)
	})
	return g.finish("setMessageReaction", err,
		slog.Int("thread_id", threadID), slog.Int("message_id", messageID), slog.String("emoji", emoji))
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
