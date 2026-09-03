// Package telegram adapts the Telegram Bot API (github.com/go-telegram/bot)
// to the domain.TelegramGateway port: forum topics, messages, inbound
// updates and the serial rate-limited call queue.
package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-telegram/bot"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// APIError is a Telegram Bot API failure that has no domain meaning of its
// own: rate limiting (429, handled inside the adapter by the queue), server
// errors and validation failures (400 with a description the reconciler
// should log and skip rather than act on).
type APIError struct {
	Code        int
	Description string
	RetryAfter  time.Duration // set for 429 only
	Err         error         // the library error, kept for %w chains
}

func (e *APIError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("telegram api %d: %s (retry after %s)", e.Code, e.Description, e.RetryAfter)
	}
	return fmt.Sprintf("telegram api %d: %s", e.Code, e.Description)
}

func (e *APIError) Unwrap() error { return e.Err }

// translate maps go-telegram/bot errors to domain sentinels where the
// application can act on them and to *APIError where it cannot. Errors the
// library does not classify (5xx, transport, decode) are returned unchanged.
func translate(err error) error {
	var tmr *bot.TooManyRequestsError
	var mig *bot.MigrateError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &tmr):
		return &APIError{
			Code:        429,
			Description: tmr.Message,
			RetryAfter:  time.Duration(tmr.RetryAfter) * time.Second,
			Err:         err,
		}
	case errors.As(err, &mig):
		return fmt.Errorf("%w: %w", &domain.ChatMigratedError{NewChatID: int64(mig.MigrateToChatID)}, err)
	case errors.Is(err, bot.ErrorForbidden):
		return fmt.Errorf("%w: %w", domain.ErrForbidden, err)
	case errors.Is(err, bot.ErrorUnauthorized):
		return fmt.Errorf("%w: %w", domain.ErrBotUnauthorized, err)
	case errors.Is(err, bot.ErrorConflict):
		return fmt.Errorf("%w: %w", domain.ErrPollerConflict, err)
	case errors.Is(err, bot.ErrorBadRequest):
		return classify400(err)
	}
	return err
}

// badRequestPrefix is how the library renders a 400: "bad request, <desc>".
var badRequestPrefix = bot.ErrorBadRequest.Error() + ", "

// ErrTopicNotModified is Telegram's answer to an edit that changes nothing
// (400 TOPIC_NOT_MODIFIED). The gateway treats it as success; it is exported
// for tests only.
var ErrTopicNotModified = errors.New("topic not modified")

// ErrMessageNotModified is Telegram's answer to a message edit that changes
// nothing (400 "message is not modified"), for example removing a keyboard
// that is already gone. The gateway treats it as success; exported for
// tests only.
var ErrMessageNotModified = errors.New("message not modified")

// description400 extracts Telegram's description from a wrapped 400.
func description400(err error) string {
	return strings.TrimPrefix(err.Error(), badRequestPrefix)
}

// classify400 turns a 400 into a domain sentinel only when its description
// identifies the topic state; any other 400 (empty text, TOPIC_NAME_INVALID,
// unparsable entities, ...) stays an *APIError so the reconciler logs and
// skips instead of deleting the mapping and recreating the topic in a loop.
// Matching is case-insensitive on the description.
//
// Verified on a live forum group 2026-09-02: editForumTopic with the same
// name and icon answers "Bad Request: TOPIC_NOT_MODIFIED".
// TODO: 2026-09-02 — confirm the exact 400 descriptions for a deleted, a
// closed, and an unknown message_thread_id on a real forum group and adjust
// the matchers.
func classify400(err error) error {
	desc := description400(err)
	lower := strings.ToLower(desc)
	switch {
	case strings.Contains(lower, "thread not found"), strings.Contains(lower, "topic_deleted"):
		return fmt.Errorf("%w: %w", domain.ErrTopicGone, err)
	case strings.Contains(lower, "topic_closed"):
		return fmt.Errorf("%w: %w", domain.ErrTopicClosed, err)
	case strings.Contains(lower, "topic_not_modified"):
		return fmt.Errorf("%w: %w", ErrTopicNotModified, err)
	case strings.Contains(lower, "message is not modified"):
		return fmt.Errorf("%w: %w", ErrMessageNotModified, err)
	}
	return &APIError{Code: 400, Description: desc, Err: err}
}

// isRetryable reports whether the queue may run the same call again. It
// accepts both raw library errors and translated ones. Client-side failures
// (400, 401, 403, 404, 409, chat migration) and a cancelled context are
// final; 429, 5xx, transport and decode errors are worth another try.
func isRetryable(err error) bool {
	var api *APIError
	switch {
	case err == nil:
		return false
	case errors.Is(err, context.Canceled):
		return false
	case errors.As(err, &api):
		return api.Code == 429 || api.Code >= 500
	case errors.Is(err, bot.ErrorBadRequest),
		errors.Is(err, bot.ErrorUnauthorized),
		errors.Is(err, bot.ErrorForbidden),
		errors.Is(err, bot.ErrorNotFound),
		errors.Is(err, bot.ErrorConflict),
		errors.Is(err, domain.ErrForbidden),
		errors.Is(err, domain.ErrBotUnauthorized),
		errors.Is(err, domain.ErrPollerConflict),
		errors.Is(err, domain.ErrTopicGone),
		errors.Is(err, domain.ErrTopicClosed),
		errors.Is(err, domain.ErrChatMigrated):
		return false
	}
	var mig *bot.MigrateError
	return !errors.As(err, &mig)
}

// retryAfter returns the wait Telegram asked for on a 429, from either a raw
// *bot.TooManyRequestsError or a translated *APIError.
func retryAfter(err error) (time.Duration, bool) {
	var api *APIError
	if errors.As(err, &api) && api.Code == 429 {
		return api.RetryAfter, true
	}
	var tmr *bot.TooManyRequestsError
	if errors.As(err, &tmr) {
		return time.Duration(tmr.RetryAfter) * time.Second, true
	}
	return 0, false
}
