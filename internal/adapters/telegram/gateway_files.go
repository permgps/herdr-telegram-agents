package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Download fetches the bytes of a file an operator sent: getFile for the
// path, then a GET on the file endpoint. The size Telegram reports and the
// bytes actually read are both checked against max, so a file above the
// cap is refused before the download or cut short during it. The URL
// carries the bot token and never reaches the log or an error.
func (g *Gateway) Download(ctx context.Context, fileID string, max int64) ([]byte, error) {
	start := time.Now()
	f, err := g.api.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "too big") {
			return nil, domain.ErrFileTooBig
		}
		g.log.Warn("getFile failed", slog.String("file_id", fileID), slog.Any("err", translate(err)))
		return nil, fmt.Errorf("getFile: %w", translate(err))
	}
	if f.FileSize > max {
		g.log.Debug("download refused by size", slog.String("file_id", fileID), slog.Int64("size", f.FileSize), slog.Int64("max", max))
		return nil, domain.ErrFileTooBig
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.api.FileDownloadLink(f), nil)
	if err != nil {
		return nil, fmt.Errorf("download request: %w", err)
	}
	resp, err := g.http.Do(req)
	if err != nil {
		g.log.Warn("download failed", slog.String("file_id", fileID), slog.String("err", downloadErr(err)))
		return nil, fmt.Errorf("download: %s", downloadErr(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		g.log.Warn("download failed", slog.String("file_id", fileID), slog.Int("status", resp.StatusCode))
		return nil, fmt.Errorf("download: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, fmt.Errorf("download read: %w", err)
	}
	if int64(len(data)) > max {
		g.log.Debug("download cut by size", slog.String("file_id", fileID), slog.Int64("max", max))
		return nil, domain.ErrFileTooBig
	}
	g.log.Debug("file download", slog.String("file_id", fileID), slog.Int("bytes", len(data)),
		slog.Int64("dur_ms", time.Since(start).Milliseconds()))
	return data, nil
}

// downloadErr words a transport error without the URL, which carries the
// bot token.
func downloadErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return err.Error()
}

// SendDocument uploads one file as a single silent message. ThreadID 0
// addresses the General topic. Content type detection is disabled so a
// .txt stays a plain file instead of being previewed as something else.
func (g *Gateway) SendDocument(ctx context.Context, doc domain.Document) error {
	return g.DocumentAt(ctx, domain.TopicAddress{ChatID: g.chatID, ThreadID: doc.ThreadID}, doc, nil)
}

func (g *Gateway) DocumentAt(ctx context.Context, address domain.TopicAddress, doc domain.Document, guard domain.DispatchGuard) error {
	params := &bot.SendDocumentParams{
		ChatID:                      address.ChatID,
		MessageThreadID:             address.ThreadID,
		Document:                    &models.InputFileUpload{Filename: doc.Name, Data: bytes.NewReader(doc.Data)},
		Caption:                     doc.Caption,
		DisableNotification:         true,
		DisableContentTypeDetection: true,
	}
	if doc.ReplyTo != 0 {
		params.ReplyParameters = &models.ReplyParameters{MessageID: doc.ReplyTo, AllowSendingWithoutReply: true}
	}
	err := g.destinationQueue(address.ChatID).DoCreateGuard(ctx, guard, func(ctx context.Context) error {
		params.Document = &models.InputFileUpload{Filename: doc.Name, Data: bytes.NewReader(doc.Data)}
		_, err := g.api.SendDocument(ctx, params)
		return translate(err)
	})
	if err != nil {
		g.log.Warn("sendDocument failed",
			slog.Int("thread_id", address.ThreadID), slog.Int("bytes", len(doc.Data)), slog.Any("err", err))
		return fmt.Errorf("sendDocument thread %d: %w", address.ThreadID, err)
	}
	g.log.Debug("sendDocument",
		slog.Int("thread_id", address.ThreadID), slog.Int("bytes", len(doc.Data)),
		slog.Int("reply_to", doc.ReplyTo), slog.Int("caption_len", len(doc.Caption)))
	return nil
}

func (g *Gateway) React(ctx context.Context, threadID, messageID int, emoji string) error {
	return g.ReactAt(ctx, domain.MessageAddress{ChatID: g.chatID, MessageID: messageID}, emoji, nil)
}

func (g *Gateway) ReactAt(ctx context.Context, address domain.MessageAddress, emoji string, guard domain.DispatchGuard) error {
	err := g.destinationQueue(address.ChatID).DoGuard(ctx, guard, func(ctx context.Context) error {
		_, err := g.api.SetMessageReaction(ctx, &bot.SetMessageReactionParams{
			ChatID:    address.ChatID,
			MessageID: address.MessageID,
			Reaction: []models.ReactionType{{
				Type:              models.ReactionTypeTypeEmoji,
				ReactionTypeEmoji: &models.ReactionTypeEmoji{Emoji: emoji},
			}},
		})
		return translate(err)
	})
	return g.finish("setMessageReaction", err,
		slog.Int("message_id", address.MessageID), slog.String("emoji", emoji))
}

// ProbeDirect sends a "typing" chat action to the user's private chat:
// nothing visible when it succeeds, ErrForbidden when the user never
// pressed Start or blocked the bot.
func (g *Gateway) ProbeDirect(ctx context.Context, userID int64) error {
	err := g.queue.Do(ctx, func(ctx context.Context) error {
		_, err := g.api.SendChatAction(ctx, &bot.SendChatActionParams{ChatID: userID, Action: models.ChatActionTyping})
		return translate(err)
	})
	return g.finish("sendChatAction", err, slog.Int64("user_id", userID), slog.String("action", string(models.ChatActionTyping)))
}

// Pin pins one of the bot's messages in the group without a notification.
// Telegram still posts a "pinned a message" service message, which the
// inbound handler deletes like a topic edit notice.
func (g *Gateway) Pin(ctx context.Context, messageID int) error {
	return g.PinAt(ctx, domain.MessageAddress{ChatID: g.chatID, MessageID: messageID}, nil)
}

func (g *Gateway) PinAt(ctx context.Context, address domain.MessageAddress, guard domain.DispatchGuard) error {
	err := g.destinationQueue(address.ChatID).DoGuard(ctx, guard, func(ctx context.Context) error {
		_, err := g.api.PinChatMessage(ctx, &bot.PinChatMessageParams{ChatID: address.ChatID, MessageID: address.MessageID, DisableNotification: true})
		return translate(err)
	})
	return g.finish("pinChatMessage", err, slog.Int("message_id", address.MessageID))
}

// Unpin removes the pin from one message.
func (g *Gateway) Unpin(ctx context.Context, messageID int) error {
	return g.UnpinAt(ctx, domain.MessageAddress{ChatID: g.chatID, MessageID: messageID}, nil)
}

func (g *Gateway) UnpinAt(ctx context.Context, address domain.MessageAddress, guard domain.DispatchGuard) error {
	err := g.destinationQueue(address.ChatID).DoGuard(ctx, guard, func(ctx context.Context) error {
		_, err := g.api.UnpinChatMessage(ctx, &bot.UnpinChatMessageParams{ChatID: address.ChatID, MessageID: address.MessageID})
		return translate(err)
	})
	return g.finish("unpinChatMessage", err, slog.Int("message_id", address.MessageID))
}

// DeleteMessage deletes one message of the bot in the group.
func (g *Gateway) DeleteMessage(ctx context.Context, messageID int) error {
	return g.DeleteMessageAt(ctx, domain.MessageAddress{ChatID: g.chatID, MessageID: messageID}, nil)
}

func (g *Gateway) DeleteMessageAt(ctx context.Context, address domain.MessageAddress, guard domain.DispatchGuard) error {
	err := g.destinationQueue(address.ChatID).DoGuard(ctx, guard, func(ctx context.Context) error {
		_, err := g.api.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: address.ChatID, MessageID: address.MessageID})
		return translate(err)
	})
	return g.finish("deleteMessage", err, slog.Int("message_id", address.MessageID))
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

func (g *Gateway) SendAt(ctx context.Context, address domain.TopicAddress, out domain.Outgoing, guard domain.DispatchGuard) (int, error) {
	if address.ChatID == 0 || address.ThreadID < 0 {
		return 0, errors.New("invalid Telegram destination")
	}
	out.ThreadID = address.ThreadID
	return g.send(ctx, address.ChatID, address.ThreadID, out, guard)
}

var _ domain.DestinationTelegram = (*Gateway)(nil)

// ConnectedBotID is the getMe identity verified before polling began.
func (g *Gateway) ConnectedBotID() int64 { return g.botID }

// Private lanes have bounded calls and no automatic retries, so one blocked
// recipient cannot occupy the owner queue during Telegram flood control.
func (g *Gateway) destinationQueue(chat any) *Queue {
	id, ok := chat.(int64)
	if !ok || id == g.chatID || id <= 0 || len(g.privateQueues) == 0 {
		return g.queue
	}
	return g.privateQueues[uint64(id)%uint64(len(g.privateQueues))]
}
