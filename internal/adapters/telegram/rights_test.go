package telegram_test

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/go-telegram/bot"

	"github.com/permgps/herdr-telegram-agents/internal/adapters/telegram"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func chatReply(isForum bool) apiReply {
	return okReply(map[string]any{"id": testChatID, "type": "supergroup", "title": "Agents", "is_forum": isForum})
}

func memberReply(status string, manage bool) apiReply {
	m := map[string]any{"status": status, "user": map[string]any{"id": 42, "is_bot": true, "first_name": "b"}}
	if status == "administrator" {
		m["can_manage_topics"] = manage
	}
	return okReply(m)
}

func TestGatewayRights(t *testing.T) {
	tests := []struct {
		name   string
		chat   apiReply
		member apiReply
		want   domain.Rights
	}{
		{"forum admin with rights", chatReply(true), memberReply("administrator", true), domain.Rights{IsForum: true, IsAdmin: true, CanManageTopics: true}},
		{"admin without rights", chatReply(true), memberReply("administrator", false), domain.Rights{IsForum: true, IsAdmin: true}},
		{"plain member", chatReply(true), memberReply("member", false), domain.Rights{IsForum: true}},
		{"not a forum", chatReply(false), memberReply("administrator", true), domain.Rights{IsAdmin: true, CanManageTopics: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.api.on("getChat", func(url.Values) apiReply { return tt.chat })
			h.api.on("getChatMember", func(url.Values) apiReply { return tt.member })
			got, err := h.gw.Rights(h.ctx)
			if err != nil || got != tt.want {
				t.Fatalf("Rights = %+v, %v; want %+v", got, err, tt.want)
			}
			if f := h.api.callsOf("getChatMember")[0].form; f.Get("chat_id") == "" || f.Get("user_id") == "" {
				t.Fatalf("getChatMember form = %v", f)
			}
		})
	}
	t.Run("forbidden", func(t *testing.T) {
		h := newHarness(t)
		h.api.on("getChat", func(url.Values) apiReply { return errReply(403, "Forbidden: bot was kicked") })
		if _, err := h.gw.Rights(h.ctx); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("Rights = %v", err)
		}
	})
}

func TestConnect(t *testing.T) {
	api := newFakeAPI(t)
	api.on("getMe", func(url.Values) apiReply {
		return okReply(map[string]any{"id": 42, "is_bot": true, "first_name": "Agents", "username": "agents_bot"})
	})
	api.on("getForumTopicIconStickers", func(url.Values) apiReply {
		return okReply([]map[string]any{{"file_id": "f", "file_unique_id": "u", "type": "custom_emoji", "width": 1, "height": 1, "is_animated": false, "is_video": false, "emoji": "⚡", "custom_emoji_id": "bolt"}})
	})
	api.on("getUpdates", func(url.Values) apiReply {
		time.Sleep(20 * time.Millisecond)
		return okReply([]any{})
	})
	api.on("createForumTopic", func(f url.Values) apiReply {
		return okReply(map[string]any{"message_thread_id": 5, "name": f.Get("name"), "icon_color": 7322096, "icon_custom_emoji_id": f.Get("icon_custom_emoji_id")})
	})
	log, buf := newTestLog(t)
	cfg := domain.Config{Version: 1, BotToken: testToken, ChatID: testChatID, OperatorIDs: []int64{testOperator}}
	ctx, cancel := context.WithCancel(ctxT(t))
	gw, run, err := telegram.Connect(ctx, cfg, log, cancel, bot.WithServerURL(api.server.URL))
	if err != nil {
		t.Fatal(err)
	}
	if got := api.methods(); len(got) != 3 || got[0] != "getMe" || got[1] != "deleteWebhook" || got[2] != "getForumTopicIconStickers" {
		t.Fatalf("methods = %v", got)
	}
	if v := api.callsOf("deleteWebhook")[0].form.Get("drop_pending_updates"); v != "true" {
		t.Fatalf("drop_pending_updates = %q", v)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		run(ctx)
	}()
	if _, err := gw.CreateTopic(ctx, "⚙️ a", domain.StatusWorking); err != nil {
		t.Fatal(err)
	}
	if got := api.callsOf("createForumTopic"); len(got) != 1 || got[0].form.Get("icon_custom_emoji_id") != "bolt" {
		t.Fatalf("createForumTopic calls = %v", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("run did not stop")
	}
	assertNoSecret(t, buf)
}

func TestConnectRejectsBadToken(t *testing.T) {
	api := newFakeAPI(t)
	api.on("getMe", func(url.Values) apiReply { return errReply(401, "Unauthorized") })
	_, _, err := telegram.Connect(ctxT(t), domain.Config{BotToken: testToken, ChatID: testChatID}, nil, nil, bot.WithServerURL(api.server.URL))
	if !errors.Is(err, domain.ErrBotUnauthorized) {
		t.Fatalf("Connect = %v", err)
	}
}
