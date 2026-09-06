package telegram_test

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/adapters/telegram"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func TestSendDirectAddressesTheUser(t *testing.T) {
	h := newHarness(t)
	h.api.on("sendMessage", func(url.Values) apiReply { return okReply(map[string]any{"message_id": 9}) })
	out := domain.Outgoing{ThreadID: 42, ReplyTo: 5, Text: "❓ <b>a</b> is waiting for you", HTML: true, Notify: true,
		Buttons: []domain.Button{{Text: "1", Data: "1"}}}
	id, err := h.gw.SendDirect(h.ctx, 7, out)
	if err != nil || id != 9 {
		t.Fatalf("SendDirect = %d, %v", id, err)
	}
	f := h.api.callsOf("sendMessage")[0].form
	if f.Get("chat_id") != "7" || f.Has("message_thread_id") || f.Has("reply_parameters") || f.Has("disable_notification") {
		t.Fatalf("sendMessage form = %v", f)
	}
	if f.Get("parse_mode") != "HTML" || f.Get("text") != "❓ <b>a</b> is waiting for you" || !strings.Contains(f.Get("reply_markup"), `"callback_data":"1"`) {
		t.Fatalf("sendMessage rendering = %v", f)
	}
	if strings.Contains(h.buf.String(), "is waiting for you") && !strings.Contains(h.buf.String(), `"level":"DEBUG"`) {
		t.Fatalf("direct message text reached the log above debug:\n%s", h.buf.String())
	}
	// Silent direct sends carry disable_notification like group posts.
	if _, err := h.gw.SendDirect(h.ctx, 7, domain.Outgoing{Text: "quiet"}); err != nil {
		t.Fatal(err)
	}
	if f := h.api.callsOf("sendMessage")[1].form; f.Get("disable_notification") != "true" {
		t.Fatalf("silent direct form = %v", f)
	}
}

func TestSendDirectClosedChat(t *testing.T) {
	h := newHarness(t)
	h.api.on("sendMessage", func(url.Values) apiReply {
		return errReply(403, "Forbidden: bot can't initiate conversation with a user")
	})
	if _, err := h.gw.SendDirect(h.ctx, 7, domain.Outgoing{Text: "x"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("SendDirect = %v, want ErrForbidden", err)
	}
	h.api.on("sendMessage", func(url.Values) apiReply { return errReply(403, "Forbidden: bot was blocked by the user") })
	if _, err := h.gw.SendDirect(h.ctx, 7, domain.Outgoing{Text: "x"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("SendDirect blocked = %v, want ErrForbidden", err)
	}
}

func TestProbeDirect(t *testing.T) {
	h := newHarness(t)
	if err := h.gw.ProbeDirect(h.ctx, 7); err != nil {
		t.Fatal(err)
	}
	f := h.api.callsOf("sendChatAction")[0].form
	if f.Get("chat_id") != "7" || f.Get("action") != "typing" || f.Has("message_thread_id") {
		t.Fatalf("sendChatAction form = %v", f)
	}
	h.api.on("sendChatAction", func(url.Values) apiReply {
		return errReply(403, "Forbidden: bot can't initiate conversation with a user")
	})
	if err := h.gw.ProbeDirect(h.ctx, 8); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("ProbeDirect closed chat = %v, want ErrForbidden", err)
	}
	if got := strings.Join(h.api.methods(), ","); got != "sendChatAction,sendChatAction" {
		t.Fatalf("methods = %s", got)
	}
}

func TestPinUnpinAndDeleteMessage(t *testing.T) {
	h := newHarness(t)
	if err := h.gw.Pin(h.ctx, 55); err != nil {
		t.Fatal(err)
	}
	if f := h.api.callsOf("pinChatMessage")[0].form; f.Get("chat_id") != "-1001234567890" || f.Get("message_id") != "55" || f.Get("disable_notification") != "true" {
		t.Fatalf("pinChatMessage form = %v", f)
	}
	if err := h.gw.Unpin(h.ctx, 55); err != nil {
		t.Fatal(err)
	}
	if f := h.api.callsOf("unpinChatMessage")[0].form; f.Get("chat_id") != "-1001234567890" || f.Get("message_id") != "55" {
		t.Fatalf("unpinChatMessage form = %v", f)
	}
	if err := h.gw.DeleteMessage(h.ctx, 55); err != nil {
		t.Fatal(err)
	}
	if f := h.api.callsOf("deleteMessage")[0].form; f.Get("chat_id") != "-1001234567890" || f.Get("message_id") != "55" {
		t.Fatalf("deleteMessage form = %v", f)
	}
	if got := strings.Join(h.api.methods(), ","); got != "pinChatMessage,unpinChatMessage,deleteMessage" {
		t.Fatalf("methods = %s", got)
	}
}

func TestMessageGoneTranslations(t *testing.T) {
	h := newHarness(t)
	h.api.on("editMessageText", func(url.Values) apiReply { return errReply(400, "Bad Request: message to edit not found") })
	if err := h.gw.EditText(h.ctx, 55, "x", true, nil); !errors.Is(err, domain.ErrMessageGone) {
		t.Fatalf("EditText = %v, want ErrMessageGone", err)
	}
	h.api.on("deleteMessage", func(url.Values) apiReply { return errReply(400, "Bad Request: message to delete not found") })
	if err := h.gw.DeleteMessage(h.ctx, 55); !errors.Is(err, domain.ErrMessageGone) {
		t.Fatalf("DeleteMessage = %v, want ErrMessageGone", err)
	}
	h.api.on("pinChatMessage", func(url.Values) apiReply {
		return errReply(400, "Bad Request: not enough rights to manage pinned messages in the chat")
	})
	err := h.gw.Pin(h.ctx, 55)
	var api *telegram.APIError
	if err == nil || errors.Is(err, domain.ErrMessageGone) || errors.Is(err, domain.ErrForbidden) || !errors.As(err, &api) || api.Code != 400 {
		t.Fatalf("Pin without the right = %v", err)
	}
	// One attempt each: 400s are final for the queue.
	if got := strings.Join(h.api.methods(), ","); got != "editMessageText,deleteMessage,pinChatMessage" {
		t.Fatalf("methods = %s", got)
	}
}
