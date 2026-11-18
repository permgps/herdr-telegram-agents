package telegram_test

import (
	"bytes"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func TestOwnerCreatesAndSendsRetryAfterRateLimit(t *testing.T) {
	t.Run("topic", func(t *testing.T) {
		h := newHarness(t)
		h.api.once("createForumTopic", tooManyReply(2), okReply(map[string]any{"message_thread_id": 42, "name": "builder"}))
		if _, err := h.gw.CreateTopic(h.ctx, "builder", domain.StatusWorking); err != nil {
			t.Fatal(err)
		}
		calls := h.api.callsOf("createForumTopic")
		if len(calls) != 2 || calls[0].form.Get("name") != calls[1].form.Get("name") {
			t.Fatalf("topic calls = %v, want the same create retried once", calls)
		}
	})

	t.Run("message", func(t *testing.T) {
		h := newHarness(t)
		h.api.once("sendMessage", tooManyReply(2), okReply(map[string]any{"message_id": 7}))
		if _, err := h.gw.Send(h.ctx, domain.Outgoing{ThreadID: 42, Text: "same payload"}); err != nil {
			t.Fatal(err)
		}
		calls := h.api.callsOf("sendMessage")
		if len(calls) != 2 || calls[0].form.Get("text") != calls[1].form.Get("text") {
			t.Fatalf("message calls = %v, want the same payload retried once", calls)
		}
	})

	t.Run("document bytes", func(t *testing.T) {
		h := newHarness(t)
		h.api.once("sendDocument", tooManyReply(2), okReply(map[string]any{"message_id": 8}))
		body := []byte{0, 1, 2, 10, 255}
		if err := h.gw.SendDocument(h.ctx, domain.Document{ThreadID: 42, Name: "payload.bin", Data: body}); err != nil {
			t.Fatal(err)
		}
		calls := h.api.callsOf("sendDocument")
		if len(calls) != 2 {
			t.Fatalf("sendDocument calls = %d, want 2", len(calls))
		}
		for _, call := range calls {
			file, ok := call.files["document"]
			if !ok || file.name != "payload.bin" || !bytes.Equal(file.data, body) {
				t.Fatalf("retried document = %+v, want exact filename and bytes", file)
			}
		}
	})
}
