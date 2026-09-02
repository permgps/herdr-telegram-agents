package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// lib mimics how go-telegram/bot wraps a coded error: "%w, <description>".
func lib(sentinel error, description string) error {
	return fmt.Errorf("%w, %s", sentinel, description)
}

func TestTranslate(t *testing.T) {
	tooMany := &bot.TooManyRequestsError{Message: "too many requests, Too Many Requests: retry after 3", RetryAfter: 3}
	migrate := &bot.MigrateError{Message: "bad request: group chat was upgraded", MigrateToChatID: -1001234}
	network := errors.New("error do request for method sendMessage, dial tcp: connection refused")
	server := errors.New("error response from telegram for method sendMessage, 502 Bad Gateway")

	tests := []struct {
		name     string
		in       error
		is       []error // errors.Is must hold for each
		notIs    []error // errors.Is must fail for each
		apiCode  int     // expected *APIError code, 0 = no APIError expected
		retry    time.Duration
		descHas  string
		migrated int64
	}{
		{name: "nil", in: nil},
		{name: "429", in: tooMany, apiCode: 429, retry: 3 * time.Second, descHas: "retry after 3"},
		{name: "migrate", in: migrate, is: []error{domain.ErrChatMigrated}, migrated: -1001234},
		{name: "403", in: lib(bot.ErrorForbidden, "bot was kicked"), is: []error{domain.ErrForbidden, bot.ErrorForbidden}},
		{name: "401", in: lib(bot.ErrorUnauthorized, "Unauthorized"), is: []error{domain.ErrBotUnauthorized}},
		{name: "409", in: lib(bot.ErrorConflict, "terminated by other getUpdates request"), is: []error{domain.ErrPollerConflict}},
		{
			name:  "400 thread not found",
			in:    lib(bot.ErrorBadRequest, "Bad Request: message thread not found"),
			is:    []error{domain.ErrTopicGone, bot.ErrorBadRequest},
			notIs: []error{domain.ErrTopicClosed},
		},
		{
			name: "400 TOPIC_DELETED case-insensitive",
			in:   lib(bot.ErrorBadRequest, "Bad Request: topic_deleted"),
			is:   []error{domain.ErrTopicGone},
		},
		{
			name:  "400 TOPIC_CLOSED",
			in:    lib(bot.ErrorBadRequest, "Bad Request: TOPIC_CLOSED"),
			is:    []error{domain.ErrTopicClosed},
			notIs: []error{domain.ErrTopicGone},
		},
		{
			name:    "400 TOPIC_NAME_INVALID stays APIError",
			in:      lib(bot.ErrorBadRequest, "Bad Request: TOPIC_NAME_INVALID"),
			notIs:   []error{domain.ErrTopicGone, domain.ErrTopicClosed},
			is:      []error{bot.ErrorBadRequest},
			apiCode: 400,
			descHas: "TOPIC_NAME_INVALID",
		},
		{name: "404 unchanged", in: lib(bot.ErrorNotFound, "Not Found"), is: []error{bot.ErrorNotFound}},
		{name: "network unchanged", in: network, is: []error{network}},
		{name: "5xx unchanged", in: server, is: []error{server}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := translate(tc.in)
			if tc.in == nil {
				if got != nil {
					t.Fatalf("translate(nil) = %v", got)
				}
				return
			}
			for _, target := range tc.is {
				if !errors.Is(got, target) {
					t.Errorf("errors.Is(%v, %v) = false", got, target)
				}
			}
			for _, target := range tc.notIs {
				if errors.Is(got, target) {
					t.Errorf("errors.Is(%v, %v) = true, want false", got, target)
				}
			}
			var api *APIError
			hasAPI := errors.As(got, &api)
			if tc.apiCode == 0 && hasAPI {
				t.Errorf("unexpected *APIError %v", api)
			}
			if tc.apiCode != 0 {
				if !hasAPI {
					t.Fatalf("want *APIError %d, got %T %v", tc.apiCode, got, got)
				}
				if api.Code != tc.apiCode {
					t.Errorf("code = %d, want %d", api.Code, tc.apiCode)
				}
				if api.RetryAfter != tc.retry {
					t.Errorf("retry after = %s, want %s", api.RetryAfter, tc.retry)
				}
				if tc.descHas != "" && !strings.Contains(api.Description, tc.descHas) {
					t.Errorf("description %q lacks %q", api.Description, tc.descHas)
				}
				if !errors.Is(got, tc.in) {
					t.Errorf("APIError does not unwrap to the library error")
				}
			}
			if tc.migrated != 0 {
				var m *domain.ChatMigratedError
				if !errors.As(got, &m) {
					t.Fatalf("want ChatMigratedError, got %v", got)
				}
				if m.NewChatID != tc.migrated {
					t.Errorf("new chat id = %d, want %d", m.NewChatID, tc.migrated)
				}
			}
		})
	}
}

func TestAPIErrorString(t *testing.T) {
	e := &APIError{Code: 429, Description: "slow down", RetryAfter: 2 * time.Second}
	if got, want := e.Error(), "telegram api 429: slow down (retry after 2s)"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	e = &APIError{Code: 400, Description: "TOPIC_NAME_INVALID"}
	if got, want := e.Error(), "telegram api 400: TOPIC_NAME_INVALID"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestIsRetryable(t *testing.T) {
	tooMany := &bot.TooManyRequestsError{Message: "too many requests", RetryAfter: 1}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"canceled", context.Canceled, false},
		{"wrapped canceled", fmt.Errorf("error do request, %w", context.Canceled), false},
		{"raw 429", tooMany, true},
		{"translated 429", translate(tooMany), true},
		{"raw 400", lib(bot.ErrorBadRequest, "x"), false},
		{"translated 400 gone", translate(lib(bot.ErrorBadRequest, "message thread not found")), false},
		{"translated 400 closed", translate(lib(bot.ErrorBadRequest, "TOPIC_CLOSED")), false},
		{"translated 400 plain", translate(lib(bot.ErrorBadRequest, "TOPIC_NAME_INVALID")), false},
		{"raw 401", lib(bot.ErrorUnauthorized, "x"), false},
		{"translated 401", translate(lib(bot.ErrorUnauthorized, "x")), false},
		{"raw 403", lib(bot.ErrorForbidden, "x"), false},
		{"translated 403", translate(lib(bot.ErrorForbidden, "x")), false},
		{"raw 404", lib(bot.ErrorNotFound, "x"), false},
		{"raw 409", lib(bot.ErrorConflict, "x"), false},
		{"translated 409", translate(lib(bot.ErrorConflict, "x")), false},
		{"raw migrate", &bot.MigrateError{MigrateToChatID: 5}, false},
		{"translated migrate", translate(&bot.MigrateError{MigrateToChatID: 5}), false},
		{"5xx string", errors.New("error response from telegram for method sendMessage, 502 Bad Gateway"), true},
		{"5xx APIError", &APIError{Code: 502}, true},
		{"network", errors.New("error do request for method sendMessage, dial tcp: connection refused"), true},
		{"deadline", context.DeadlineExceeded, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRetryable(tc.err); got != tc.want {
				t.Errorf("isRetryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestRetryAfter(t *testing.T) {
	raw := &bot.TooManyRequestsError{Message: "too many requests", RetryAfter: 7}
	if d, ok := retryAfter(raw); !ok || d != 7*time.Second {
		t.Errorf("raw: (%s, %v)", d, ok)
	}
	if d, ok := retryAfter(fmt.Errorf("sendMessage: %w", translate(raw))); !ok || d != 7*time.Second {
		t.Errorf("translated and wrapped: (%s, %v)", d, ok)
	}
	if _, ok := retryAfter(lib(bot.ErrorBadRequest, "x")); ok {
		t.Error("400 reported a retry-after")
	}
	if _, ok := retryAfter(nil); ok {
		t.Error("nil reported a retry-after")
	}
}
