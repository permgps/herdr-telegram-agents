package telegram_test

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot"

	"github.com/permgps/herdr-telegram-agents/internal/adapters/telegram"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

func TestNewBotRejectsEmptyToken(t *testing.T) {
	if _, err := telegram.NewBot("   ", nil, nil); err == nil {
		t.Fatal("expected an error for an empty token")
	}
}

func TestCheck(t *testing.T) {
	api := newFakeAPI(t)
	api.on("getMe", func(url.Values) apiReply {
		return okReply(map[string]any{"id": 1, "is_bot": true, "username": "herdr_agents_bot", "first_name": "Herdr"})
	})
	log, buf := newTestLog(t)
	b := api.bot(t, log, nil)

	id, err := telegram.Check(ctxT(t), b, log)
	if err != nil {
		t.Fatal(err)
	}
	if id.ID != 1 || id.Username != "herdr_agents_bot" {
		t.Errorf("identity = %+v", id)
	}
	if got := api.methods(); strings.Join(got, ",") != "getMe,deleteWebhook" {
		t.Errorf("calls = %v", got)
	}
	if got := api.callsOf("deleteWebhook")[0].form.Get("drop_pending_updates"); got != "true" {
		t.Errorf("drop_pending_updates = %q, want true", got)
	}
	out := buf.String()
	if !strings.Contains(out, "bot_id=1") || !strings.Contains(out, "username=herdr_agents_bot") {
		t.Errorf("identity not logged: %s", out)
	}
	assertNoSecret(t, buf)
}

func TestCheckUnauthorized(t *testing.T) {
	api := newFakeAPI(t)
	api.on("getMe", func(url.Values) apiReply { return errReply(401, "Unauthorized") })
	log, buf := newTestLog(t)
	b := api.bot(t, log, nil)

	_, err := telegram.Check(ctxT(t), b, log)
	if !errors.Is(err, domain.ErrBotUnauthorized) {
		t.Fatalf("err = %v, want ErrBotUnauthorized", err)
	}
	if len(api.callsOf("deleteWebhook")) != 0 {
		t.Error("deleteWebhook was called after getMe failed")
	}
	assertNoSecret(t, buf)
}

func TestCheckDeleteWebhookFailure(t *testing.T) {
	api := newFakeAPI(t)
	api.on("getMe", func(url.Values) apiReply {
		return okReply(map[string]any{"id": 7, "is_bot": true, "username": "x"})
	})
	api.on("deleteWebhook", func(url.Values) apiReply { return errReply(500, "Internal Server Error") })
	b := api.bot(t, nil, nil)
	if _, err := telegram.Check(ctxT(t), b, nil); err == nil || !strings.Contains(err.Error(), "deleteWebhook") {
		t.Fatalf("err = %v", err)
	}
}

// runPoll starts Poll on a goroutine and returns a channel closed when it
// returns.
func runPoll(ctx context.Context, b *bot.Bot, log *slog.Logger) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		telegram.Poll(ctx, b, log)
	}()
	return done
}

func waitDone(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: Poll did not return", what)
	}
}

func TestPollStopsOnUnauthorized(t *testing.T) {
	api := newFakeAPI(t)
	api.on("getUpdates", func(url.Values) apiReply { return errReply(401, "Unauthorized") })
	log, buf := newTestLog(t)
	ctx, cancel := context.WithCancel(ctxT(t))
	defer cancel()
	b := api.bot(t, log, cancel)

	done := runPoll(ctx, b, log)
	waitDone(t, done, "401")
	if ctx.Err() == nil {
		t.Error("fatal did not cancel the context")
	}
	if !strings.Contains(buf.String(), "level=ERROR msg=\"telegram bot token rejected, stopping\"") {
		t.Errorf("missing fatal log: %s", buf.String())
	}
	assertNoSecret(t, buf)
}

func TestPollStopsOnConflict(t *testing.T) {
	api := newFakeAPI(t)
	api.on("getUpdates", func(url.Values) apiReply {
		return errReply(409, "Conflict: terminated by other getUpdates request")
	})
	log, buf := newTestLog(t)
	ctx, cancel := context.WithCancel(ctxT(t))
	defer cancel()
	b := api.bot(t, log, cancel)

	waitDone(t, runPoll(ctx, b, log), "409")
	if !strings.Contains(buf.String(), "another poller owns this bot, stopping") {
		t.Errorf("missing fatal log: %s", buf.String())
	}
	assertNoSecret(t, buf)
}

func TestPollWarnsOnTransientErrors(t *testing.T) {
	api := newFakeAPI(t)
	api.on("getUpdates", func(url.Values) apiReply { return errReply(502, "Bad Gateway") })
	log, buf := newTestLog(t)
	ctx, cancel := context.WithCancel(ctxT(t))
	fatal := false
	b := api.bot(t, log, func() { fatal = true })

	done := runPoll(ctx, b, log)
	if !buf.contains("level=WARN msg=\"telegram polling error\"", 5*time.Second) {
		t.Fatalf("no warning logged: %s", buf.String())
	}
	cancel()
	waitDone(t, done, "502")
	if fatal {
		t.Error("fatal was called for a transient error")
	}
	if !strings.Contains(buf.String(), "telegram polling stopped") {
		t.Errorf("stop not logged: %s", buf.String())
	}
	au := api.callsOf("getUpdates")[0].form.Get("allowed_updates")
	if !strings.Contains(au, "message") || !strings.Contains(au, "edited_message") || !strings.Contains(au, "my_chat_member") {
		t.Errorf("allowed_updates = %q", au)
	}
	assertNoSecret(t, buf)
}

func TestRegisterCommands(t *testing.T) {
	h := newHarness(t)
	if err := telegram.RegisterCommands(h.ctx, h.bot, testChatID, h.log); err != nil {
		t.Fatal(err)
	}
	calls := h.api.callsOf("setMyCommands")
	if len(calls) != 1 {
		t.Fatalf("setMyCommands calls = %d", len(calls))
	}
	f := calls[0].form
	cmds := f.Get("commands")
	for _, name := range []string{`"command":"screen"`, `"command":"keys"`, `"command":"focus"`, `"command":"status"`, `"command":"help"`} {
		if !strings.Contains(cmds, name) {
			t.Errorf("commands lack %s: %s", name, cmds)
		}
	}
	if scope := f.Get("scope"); !strings.Contains(scope, `"type":"chat"`) || !strings.Contains(scope, `"chat_id":-1001234567890`) {
		t.Errorf("scope = %q", scope)
	}
	if !strings.Contains(h.buf.String(), "commands registered") {
		t.Errorf("registration not logged: %s", h.buf.String())
	}
}

func TestRegisterCommandsFailureIsReturned(t *testing.T) {
	h := newHarness(t)
	h.api.on("setMyCommands", func(url.Values) apiReply { return errReply(400, "Bad Request: BOT_COMMANDS_INVALID") })
	err := telegram.RegisterCommands(h.ctx, h.bot, testChatID, h.log)
	if err == nil || !strings.Contains(err.Error(), "setMyCommands") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(h.buf.String(), "setMyCommands failed") {
		t.Errorf("failure not logged: %s", h.buf.String())
	}
}
