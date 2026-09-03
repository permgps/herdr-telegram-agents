package telegram_test

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/go-telegram/bot"

	"github.com/permgps/herdr-telegram-agents/internal/adapters/telegram"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func newInspector(t *testing.T) (*telegram.Inspector, *fakeAPI) {
	t.Helper()
	api := newFakeAPI(t)
	log, buf := newTestLog(t)
	insp, err := telegram.NewInspector(testToken, testChatID, log, bot.WithServerURL(api.server.URL))
	if err != nil {
		t.Fatal(err)
	}
	api.on("getMe", func(url.Values) apiReply {
		return okReply(map[string]any{"id": 42, "is_bot": true, "first_name": "Agents", "username": "agents_bot"})
	})
	t.Cleanup(func() {
		assertNoSecret(t, buf)
		for _, m := range api.methods() {
			if m == "deleteWebhook" || m == "getUpdates" {
				t.Errorf("inspector must never call %s", m)
			}
		}
	})
	return insp, api
}

func TestInspectorIdentity(t *testing.T) {
	insp, _ := newInspector(t)
	id, err := insp.Identity(ctxT(t))
	if err != nil || id.ID != 42 || id.Username != "agents_bot" {
		t.Fatalf("Identity = %+v, %v", id, err)
	}
}

func TestInspectorIdentityUnauthorized(t *testing.T) {
	insp, api := newInspector(t)
	api.on("getMe", func(url.Values) apiReply { return errReply(401, "Unauthorized") })
	if _, err := insp.Identity(ctxT(t)); !errors.Is(err, domain.ErrBotUnauthorized) {
		t.Fatalf("Identity err = %v", err)
	}
}

func TestInspectorGroupRights(t *testing.T) {
	tests := []struct {
		name   string
		member apiReply
		want   domain.Rights
	}{
		{"owner", okReply(map[string]any{"status": "creator", "user": map[string]any{"id": 42, "is_bot": true, "first_name": "b"}}),
			domain.Rights{IsForum: true, IsAdmin: true, CanManageTopics: true, CanDeleteMessages: true}},
		{"admin with both", memberReply("administrator", true),
			domain.Rights{IsForum: true, IsAdmin: true, CanManageTopics: true, CanDeleteMessages: true}},
		{"admin without delete", okReply(map[string]any{"status": "administrator", "can_manage_topics": true, "can_delete_messages": false,
			"user": map[string]any{"id": 42, "is_bot": true, "first_name": "b"}}),
			domain.Rights{IsForum: true, IsAdmin: true, CanManageTopics: true}},
		{"member", memberReply("member", false), domain.Rights{IsForum: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			insp, api := newInspector(t)
			api.on("getChat", func(url.Values) apiReply { return chatReply(true) })
			api.on("getChatMember", func(url.Values) apiReply { return tt.member })
			got, err := insp.Group(ctxT(t))
			if err != nil || got.Title != "Agents" || got.Rights != tt.want {
				t.Fatalf("Group = %+v, %v; want rights %+v", got, err, tt.want)
			}
			if f := api.callsOf("getChatMember")[0].form; f.Get("user_id") != "42" || f.Get("chat_id") != "-1001234567890" {
				t.Fatalf("getChatMember form = %v", f)
			}
		})
	}
}

func TestInspectorGroupForbidden(t *testing.T) {
	insp, api := newInspector(t)
	api.on("getChat", func(url.Values) apiReply { return errReply(403, "Forbidden: bot was kicked") })
	if _, err := insp.Group(ctxT(t)); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("Group err = %v", err)
	}
}

func TestInspectorSendTest(t *testing.T) {
	insp, api := newInspector(t)
	api.on("sendMessage", func(url.Values) apiReply { return okReply(map[string]any{"message_id": 77}) })
	id, err := insp.SendTest(ctxT(t), "hello from doctor")
	if err != nil || id != 77 {
		t.Fatalf("SendTest = %d, %v", id, err)
	}
	f := api.callsOf("sendMessage")[0].form
	if f.Get("chat_id") != "-1001234567890" || f.Get("text") != "hello from doctor" || f.Get("message_thread_id") != "" || f.Get("disable_notification") != "" {
		t.Fatalf("sendMessage form = %v", f)
	}
	if got := strings.Join(api.methods(), ","); got != "sendMessage" {
		t.Fatalf("methods = %s, want only sendMessage", got)
	}
}

func TestInspectorSendTestFails(t *testing.T) {
	insp, api := newInspector(t)
	api.on("sendMessage", func(url.Values) apiReply { return errReply(400, "Bad Request: chat not found") })
	var apiErr *telegram.APIError
	if _, err := insp.SendTest(ctxT(t), "x"); !errors.As(err, &apiErr) || apiErr.Code != 400 {
		t.Fatalf("SendTest err = %v", err)
	}
}
