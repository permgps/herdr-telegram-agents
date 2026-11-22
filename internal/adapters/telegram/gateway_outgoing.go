package telegram

import (
	"context"
	"errors"
	"fmt"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
	"log/slog"
	"strings"
	"unicode/utf8"
)

// Send posts one message, split into parts below Telegram's message limit,
// each as its own queued call. ThreadID 0 addresses the General topic, so
// message_thread_id is omitted. Code wraps every part in <pre>; HTML passes
// the part through as caller-escaped markup; Markdown splits on fence
// boundaries and renders each part to HTML, and a part Telegram rejects
// with "can't parse entities" is sent once more as <pre>. MaxParts drops
// the tail of a long text and says so at the end of the last kept part.
// ReplyTo quotes the operator's message on the first part only; Buttons
// ride on the last part as an inline keyboard. Messages are silent unless
// Notify is set. The first failure stops the remaining parts. The id of
// the last part is returned so the caller can edit its keyboard later.
func (g *Gateway) Send(ctx context.Context, out domain.Outgoing) (int, error) {
	return g.send(ctx, g.chatID, out.ThreadID, out, nil)
}

// SendDirect posts into the private chat with userID: the same splitting,
// rendering and buttons as Send, addressed by user id with no thread. A
// chat the user never opened or blocked is ErrForbidden (Telegram's
// "bot can't initiate conversation with a user" and "bot was blocked by
// the user" are both 403). The message text stays out of the log above
// debug, as for every send.
func (g *Gateway) SendDirect(ctx context.Context, userID int64, out domain.Outgoing) (int, error) {
	out.ThreadID = 0
	out.ReplyTo = 0
	return g.send(ctx, userID, 0, out, nil)
}

// send is the body shared by Send and SendDirect; chatID is the group or
// the user, threadID the topic (0 for General and for a private chat). A
// Footer is escaped and appended to the last part after a newline, the
// split limit reduced by its length so the pair stays under Telegram's
// cap; a part with more lines than Fold goes out inside an expandable
// quote, the footer under it.
func (g *Gateway) send(ctx context.Context, chatID int64, threadID int, out domain.Outgoing, guard domain.DispatchGuard) (int, error) {
	markdown := out.Markdown && !out.Code && !out.HTML
	footer := truncateFooter(out.Footer, footerMax)
	limit := textMax
	if footer != "" {
		limit -= utf16Len(footer) + 1
	}
	var parts []string
	if markdown {
		parts = splitMarkdown(out.Text, limit)
	} else {
		parts = chunk(out.Text, limit)
	}
	parts, droppedParts, droppedChars := capParts(parts, out.MaxParts)
	if markdown || out.MaxParts > 0 || footer != "" {
		g.log.Debug("message split", slog.Int("thread_id", out.ThreadID), slog.Bool("markdown", markdown), slog.Int("limit", limit),
			slog.Int("parts", len(parts)), slog.Int("dropped_parts", droppedParts), slog.Int("dropped_chars", droppedChars))
	}
	lastID := 0
	for i, part := range parts {
		body := renderPlain(part)
		switch {
		case out.Code:
			body = renderCode(part)
		case out.HTML:
			body = part
		case markdown:
			body = renderMarkdown(part)
		}
		folded := out.Fold > 0 && strings.Count(part, "\n")+1 > out.Fold
		partFooter := ""
		if i == len(parts)-1 {
			partFooter = footer
		}
		body = wrap(body, folded, partFooter)
		params := &bot.SendMessageParams{
			ChatID:              chatID,
			MessageThreadID:     threadID,
			Text:                body,
			ParseMode:           models.ParseModeHTML,
			DisableNotification: !out.Notify,
		}
		if i == 0 && out.ReplyTo != 0 {
			params.ReplyParameters = &models.ReplyParameters{MessageID: out.ReplyTo, AllowSendingWithoutReply: true}
		}
		buttons := 0
		switch {
		case i == len(parts)-1 && len(out.Buttons) > 0:
			params.ReplyMarkup = inlineKeyboard(out.Buttons)
			buttons = len(out.Buttons)
			if out.ForceReply {
				g.log.Warn("force reply dropped: buttons take the reply markup", slog.Int("thread_id", out.ThreadID))
			}
		case i == len(parts)-1 && out.ForceReply:
			params.ReplyMarkup = &models.ForceReply{ForceReply: true, Selective: true, InputFieldPlaceholder: forceReplyPlaceholder}
		}
		id, err := g.sendPart(ctx, params, guard)
		if err != nil && markdown && isMarkupError(err) {
			g.log.Warn("markdown rejected, re-sent as pre",
				slog.Int("thread_id", out.ThreadID), slog.Int("part", i+1), slog.Int("parts", len(parts)),
				slog.Any("err", err))
			params.Text = wrap(renderCode(part), folded, partFooter)
			id, err = g.sendPart(ctx, params, guard)
		}
		if err != nil {
			g.log.Warn("sendMessage failed",
				slog.Int("thread_id", out.ThreadID), slog.Int("part", i+1), slog.Int("parts", len(parts)), slog.Any("err", err))
			return 0, fmt.Errorf("sendMessage thread %d part %d/%d: %w", out.ThreadID, i+1, len(parts), err)
		}
		lastID = id
		g.log.Debug("sendMessage",
			slog.Int64("chat_id", chatID), slog.Int("thread_id", threadID), slog.Int("part", i+1), slog.Int("parts", len(parts)),
			slog.Int("reply_to", out.ReplyTo), slog.Bool("notify", out.Notify),
			slog.Int("runes", utf8.RuneCountInString(part)), slog.Int("buttons", buttons),
			slog.Bool("folded", folded), slog.Bool("footer", partFooter != ""), slog.Int("message_id", lastID))
	}
	return lastID, nil
}

// footerMax bounds a footer in UTF-16 units; a longer one is cut with an
// ellipsis so the split limit never collapses.
const footerMax = 200

// wrap finishes the HTML of one part: an expandable quote around a folded
// body and the escaped footer on its own line after it.
func wrap(body string, folded bool, footer string) string {
	if folded {
		body = "<blockquote expandable>" + body + "</blockquote>"
	}
	if footer != "" {
		body += "\n" + renderPlain(footer)
	}
	return body
}

// truncateFooter trims spaces and newlines off a footer, keeps it to one
// line and cuts it to max UTF-16 units with an ellipsis.
func truncateFooter(footer string, max int) string {
	footer = strings.TrimSpace(strings.ReplaceAll(footer, "\n", " "))
	rs := []rune(footer)
	if n := fitUTF16(rs, max); n < len(rs) {
		return string(rs[:fitUTF16(rs, max-1)]) + "…"
	}
	return footer
}

// sendPart sends one message through the queue and returns its id.
func (g *Gateway) sendPart(ctx context.Context, params *bot.SendMessageParams, guard domain.DispatchGuard) (int, error) {
	id := 0
	err := g.destinationQueue(params.ChatID).DoCreateGuard(ctx, guard, func(ctx context.Context) error {
		msg, err := g.api.SendMessage(ctx, params)
		if err == nil && msg != nil {
			id = msg.ID
		}
		return translate(err)
	})
	if err != nil {
		return 0, err
	}
	return id, err
}

// capParts keeps at most max parts (0 = all) and appends a trailer naming
// the dropped characters to the last kept part. It reports how many parts
// and characters were dropped.
func capParts(parts []string, max int) ([]string, int, int) {
	if max <= 0 || len(parts) <= max {
		return parts, 0, 0
	}
	dropped := strings.Join(parts[max:], "\n")
	chars := utf8.RuneCountInString(dropped)
	kept := append([]string(nil), parts[:max]...)
	kept[max-1] += fmt.Sprintf("\n… (+%d chars)", chars)
	return kept, len(parts) - max, chars
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
	return g.EditTextAt(ctx, domain.MessageAddress{ChatID: g.chatID, MessageID: messageID}, text, html, buttons, nil)
}

func (g *Gateway) EditTextAt(ctx context.Context, address domain.MessageAddress, text string, html bool, buttons []domain.Button, guard domain.DispatchGuard) error {
	if utf16Len(text) > textMax {
		runes := []rune(text)
		text = string(runes[:fitUTF16(runes, textMax-1)]) + "…"
	}
	params := &bot.EditMessageTextParams{
		ChatID:      address.ChatID,
		MessageID:   address.MessageID,
		Text:        text,
		ReplyMarkup: inlineKeyboard(buttons),
	}
	if html {
		params.ParseMode = models.ParseModeHTML
	}
	err := g.destinationQueue(address.ChatID).DoGuard(ctx, guard, func(ctx context.Context) error {
		_, err := g.api.EditMessageText(ctx, params)
		return translate(err)
	})
	if errors.Is(err, ErrMessageNotModified) {
		g.log.Debug("editMessageText already in place", slog.Int("message_id", address.MessageID))
		err = nil
	}
	return g.finish("editMessageText", err,
		slog.Int("message_id", address.MessageID),
		slog.Int("chars", utf8.RuneCountInString(text)),
		slog.Int("buttons", len(buttons)))
}

// EditButtons replaces the inline keyboard of one of the bot's messages;
// an empty slice removes it. Telegram answers "message is not modified"
// when the keyboard already matches, which is the state the caller wants.
func (g *Gateway) EditButtons(ctx context.Context, messageID int, buttons []domain.Button) error {
	return g.EditButtonsAt(ctx, domain.MessageAddress{ChatID: g.chatID, MessageID: messageID}, buttons, nil)
}

func (g *Gateway) EditButtonsAt(ctx context.Context, address domain.MessageAddress, buttons []domain.Button, guard domain.DispatchGuard) error {
	markup := inlineKeyboard(buttons)
	err := g.destinationQueue(address.ChatID).DoGuard(ctx, guard, func(ctx context.Context) error {
		_, err := g.api.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{
			ChatID:      address.ChatID,
			MessageID:   address.MessageID,
			ReplyMarkup: markup,
		})
		return translate(err)
	})
	if errors.Is(err, ErrMessageNotModified) {
		g.log.Debug("editMessageReplyMarkup already in place", slog.Int("message_id", address.MessageID))
		err = nil
	}
	return g.finish("editMessageReplyMarkup", err, slog.Int("message_id", address.MessageID), slog.Int("buttons", len(buttons)))
}

// AnswerButton acknowledges a button press with a short toast. The call
// bypasses the serial queue on purpose: Telegram expects the answer within
// seconds or the client keeps its spinner, and answerCallbackQuery does not
// count against the per-group message limit the queue protects.
func (g *Gateway) AnswerButton(ctx context.Context, callbackID, text string) error {
	_, err := g.api.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: callbackID, Text: text})
	return g.finish("answerCallbackQuery", translate(err), slog.Int("text_len", len(text)))
}
