package telegram_test

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/permgps/herdr-telegram-agents/internal/adapters/telegram"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func promotion(chatID int64, title string, isForum bool, from models.User, rights bool) *models.Update {
	return &models.Update{ID: 7, MyChatMember: &models.ChatMemberUpdated{
		Chat: models.Chat{ID: chatID, Type: models.ChatTypeSupergroup, Title: title, IsForum: isForum},
		From: from,
		NewChatMember: models.ChatMember{
			Type:          models.ChatMemberTypeAdministrator,
			Administrator: &models.ChatMemberAdministrator{CanManageTopics: rights},
		},
	}}
}

func newProbe(t *testing.T) (*telegram.Probe, *fakeAPI, *logBuffer) {
	t.Helper()
	api := newFakeAPI(t)
	// A long-poll that answers instantly would spin; slow it down a little.
	api.on("getUpdates", func(url.Values) apiReply {
		time.Sleep(20 * time.Millisecond)
		return okReply([]any{})
	})
	api.on("getMe", func(url.Values) apiReply {
		return okReply(map[string]any{"id": 42, "is_bot": true, "first_name": "Agents", "username": "agents_bot"})
	})
	log, buf := newTestLog(t)
	p, err := telegram.NewProbe(testToken, log, bot.WithServerURL(api.server.URL))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = p.Close()
		assertNoSecret(t, buf)
	})
	return p, api, buf
}

func recvCandidate(t *testing.T, ch <-chan domain.GroupCandidate) domain.GroupCandidate {
	t.Helper()
	select {
	case c := <-ch:
		return c
	case <-time.After(2 * time.Second):
		t.Fatal("no candidate")
		return domain.GroupCandidate{}
	}
}

func noCandidate(t *testing.T, ch <-chan domain.GroupCandidate) {
	t.Helper()
	select {
	case c := <-ch:
		t.Fatalf("unexpected candidate %+v", c)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestProbeIdentityKeepsPendingUpdates(t *testing.T) {
	p, api, _ := newProbe(t)
	id, err := p.Identity(ctxT(t))
	if err != nil || id.ID != 42 || id.Username != "agents_bot" {
		t.Fatalf("Identity = %+v, %v", id, err)
	}
	if got := api.methods(); len(got) != 2 || got[0] != "getMe" || got[1] != "deleteWebhook" {
		t.Fatalf("methods = %v", got)
	}
	if v := api.callsOf("deleteWebhook")[0].form.Get("drop_pending_updates"); v == "true" {
		t.Fatalf("drop_pending_updates = %q, want unset or false", v)
	}
}

func TestProbeIdentityRejectsBadToken(t *testing.T) {
	p, api, _ := newProbe(t)
	api.on("getMe", func(url.Values) apiReply { return errReply(401, "Unauthorized") })
	if _, err := p.Identity(ctxT(t)); !errors.Is(err, domain.ErrBotUnauthorized) {
		t.Fatalf("Identity = %v", err)
	}
}

func TestProbeCandidates(t *testing.T) {
	p, api, _ := newProbe(t)
	api.on("getChat", func(url.Values) apiReply {
		return okReply(map[string]any{"id": -300, "type": "supergroup", "title": "Late forum", "is_forum": true})
	})
	ctx, cancel := context.WithCancel(ctxT(t))
	defer cancel()
	ch, err := p.Candidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Candidates(ctx); err == nil {
		t.Fatal("second Candidates should fail")
	}
	alex := models.User{ID: 7, Username: "alex"}

	p.Process(ctx, promotion(-100, "Agents", true, alex, true))
	c := recvCandidate(t, ch)
	if c.ChatID != -100 || c.Title != "Agents" || c.FromID != 7 || c.FromUsername != "alex" {
		t.Fatalf("candidate = %+v", c)
	}

	p.Process(ctx, promotion(-100, "Agents", true, alex, true))
	noCandidate(t, ch)

	p.Process(ctx, promotion(-200, "No rights", true, alex, false))
	noCandidate(t, ch)

	// Flag missing on the wire: getChat confirms it is a forum.
	p.Process(ctx, promotion(-300, "", false, alex, true))
	c = recvCandidate(t, ch)
	if c.ChatID != -300 || c.Title != "Late forum" {
		t.Fatalf("candidate via getChat = %+v", c)
	}

	api.on("getChat", func(url.Values) apiReply {
		return okReply(map[string]any{"id": -400, "type": "supergroup", "title": "Plain group", "is_forum": false})
	})
	p.Process(ctx, promotion(-400, "Plain group", false, alex, true))
	noCandidate(t, ch)

	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, open := <-ch; open {
		t.Fatal("channel still open after Close")
	}
}
