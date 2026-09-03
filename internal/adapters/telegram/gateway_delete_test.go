package telegram_test

import (
	"errors"
	"net/url"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/adapters/telegram"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func TestDeleteTopicSendsThread(t *testing.T) {
	h := newHarness(t)
	h.api.on("deleteForumTopic", func(url.Values) apiReply { return okReply(true) })
	if err := h.gw.DeleteTopic(h.ctx, 42); err != nil {
		t.Fatal(err)
	}
	calls := h.api.callsOf("deleteForumTopic")
	if len(calls) != 1 || calls[0].form.Get("message_thread_id") != "42" || calls[0].form.Get("chat_id") != "-1001234567890" {
		t.Fatalf("deleteForumTopic calls = %+v", calls)
	}
}

func TestDeleteTopicErrors(t *testing.T) {
	tests := []struct {
		name  string
		reply apiReply
		want  error
		code  int
	}{
		{"topic id invalid", errReply(400, "Bad Request: TOPIC_ID_INVALID"), domain.ErrTopicGone, 0},
		{"thread not found", errReply(400, "Bad Request: message thread not found"), domain.ErrTopicGone, 0},
		{"topic not found", errReply(400, "Bad Request: topic not found"), domain.ErrTopicGone, 0},
		{"forbidden", errReply(403, "Forbidden: not enough rights to delete messages"), domain.ErrForbidden, 0},
		{"other 400", errReply(400, "Bad Request: something else"), nil, 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.api.on("deleteForumTopic", func(url.Values) apiReply { return tt.reply })
			err := h.gw.DeleteTopic(h.ctx, 7)
			if err == nil {
				t.Fatal("expected an error")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			var api *telegram.APIError
			if tt.code != 0 && (!errors.As(err, &api) || api.Code != tt.code) {
				t.Fatalf("err = %v, want *APIError %d", err, tt.code)
			}
			if tt.code != 0 && errors.Is(err, domain.ErrTopicGone) {
				t.Fatal("a plain 400 must not become ErrTopicGone")
			}
		})
	}
}
