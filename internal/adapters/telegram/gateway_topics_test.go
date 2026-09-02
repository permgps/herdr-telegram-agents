package telegram_test

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/permgps/herdr-telegram-agents/internal/adapters/telegram"
	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

const (
	testChatID   int64 = -1001234567890
	testOperator int64 = 4242
)

// sleepRecorder replaces the queue's sleep so tests run instantly.
type sleepRecorder struct {
	mu     sync.Mutex
	sleeps []time.Duration
}

func (s *sleepRecorder) Sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.sleeps = append(s.sleeps, d)
	s.mu.Unlock()
	return ctx.Err()
}

func (s *sleepRecorder) recorded() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.sleeps...)
}

type harness struct {
	api   *fakeAPI
	bot   *bot.Bot
	gw    *telegram.Gateway
	log   *slog.Logger
	buf   *logBuffer
	sleep *sleepRecorder
	ctx   context.Context
}

// newHarness builds a gateway over a fake API with the icon pack below and
// runs its queue until the test ends.
func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessWith(t, telegram.Config{})
}

// newHarnessWith is newHarness with the gateway options that are not the
// fixed chat, operator, icon pack or bot id taken from opts.
func newHarnessWith(t *testing.T, opts telegram.Config) *harness {
	t.Helper()
	api := newFakeAPI(t)
	log, buf := newTestLog(t)
	b := api.bot(t, log, nil)
	icons := telegram.NewIconSet([]*models.Sticker{
		{Emoji: "⚡", CustomEmojiID: "bolt"},
		{Emoji: "❓", CustomEmojiID: "question"},
		{Emoji: "🏁", CustomEmojiID: "flag"},
	})
	rec := &sleepRecorder{}
	q := telegram.NewQueue(log, telegram.QueueConfig{Sleep: rec.Sleep})
	opts.ChatID, opts.Operators, opts.Icons, opts.BotID = testChatID, []int64{testOperator}, icons, testBotID
	gw := telegram.NewGateway(b, opts, q, log)
	ctx, cancel := context.WithCancel(ctxT(t))
	done := make(chan struct{})
	go func() {
		defer close(done)
		gw.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		assertNoSecret(t, buf)
	})
	return &harness{api: api, bot: b, gw: gw, log: log, buf: buf, sleep: rec, ctx: ctx}
}

func TestCreateTopic(t *testing.T) {
	h := newHarness(t)
	h.api.on("createForumTopic", func(f url.Values) apiReply {
		return okReply(map[string]any{"message_thread_id": 42, "name": f.Get("name"), "icon_custom_emoji_id": f.Get("icon_custom_emoji_id")})
	})

	topic, err := h.gw.CreateTopic(h.ctx, "⚙️ builder", domain.StatusWorking)
	if err != nil {
		t.Fatal(err)
	}
	if topic.ThreadID != 42 || topic.Name != "⚙️ builder" || topic.IconEmojiID != "bolt" {
		t.Errorf("topic = %+v", topic)
	}
	calls := h.api.callsOf("createForumTopic")
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	f := calls[0].form
	if f.Get("chat_id") != "-1001234567890" || f.Get("name") != "⚙️ builder" ||
		f.Get("icon_color") != "7322096" || f.Get("icon_custom_emoji_id") != "bolt" {
		t.Errorf("form = %v", f)
	}
}

func TestCreateTopicWithoutIconUsesColorOnly(t *testing.T) {
	h := newHarness(t)
	h.api.on("createForumTopic", func(url.Values) apiReply {
		return okReply(map[string]any{"message_thread_id": 7, "name": "x"})
	})
	if _, err := h.gw.CreateTopic(h.ctx, strings.Repeat("я", 200), domain.StatusIdle); err != nil {
		t.Fatal(err)
	}
	f := h.api.callsOf("createForumTopic")[0].form
	if _, has := f["icon_custom_emoji_id"]; has {
		t.Errorf("icon_custom_emoji_id sent for a status without icon: %v", f)
	}
	if f.Get("icon_color") != "16766590" {
		t.Errorf("icon_color = %q", f.Get("icon_color"))
	}
	if got := len([]rune(f.Get("name"))); got != 128 {
		t.Errorf("name = %d runes, want 128", got)
	}
}

func TestEditTopicBatchesNameAndIcon(t *testing.T) {
	h := newHarness(t)
	name := "❓ builder"
	status := domain.StatusBlocked
	if err := h.gw.EditTopic(h.ctx, 42, domain.TopicPatch{Name: &name, Status: &status}); err != nil {
		t.Fatal(err)
	}
	calls := h.api.callsOf("editForumTopic")
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	f := calls[0].form
	if f.Get("message_thread_id") != "42" || f.Get("name") != name || f.Get("icon_custom_emoji_id") != "question" {
		t.Errorf("form = %v", f)
	}
}

func TestEditTopicSkipsEmptyPatch(t *testing.T) {
	h := newHarness(t)
	if err := h.gw.EditTopic(h.ctx, 42, domain.TopicPatch{}); err != nil {
		t.Fatal(err)
	}
	// A status without an icon in the pack has nothing to send either.
	status := domain.StatusIdle
	if err := h.gw.EditTopic(h.ctx, 42, domain.TopicPatch{Status: &status}); err != nil {
		t.Fatal(err)
	}
	if n := len(h.api.methods()); n != 0 {
		t.Errorf("%d HTTP calls made for empty patches: %v", n, h.api.methods())
	}
	if !strings.Contains(h.buf.String(), "editForumTopic skipped") {
		t.Errorf("skip not logged: %s", h.buf.String())
	}
}

func TestCloseAndReopenTopic(t *testing.T) {
	h := newHarness(t)
	if err := h.gw.CloseTopic(h.ctx, 42); err != nil {
		t.Fatal(err)
	}
	if err := h.gw.ReopenTopic(h.ctx, 43); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(h.api.methods(), ","); got != "closeForumTopic,reopenForumTopic" {
		t.Errorf("methods = %s", got)
	}
	if f := h.api.callsOf("closeForumTopic")[0].form; f.Get("message_thread_id") != "42" || f.Get("chat_id") != "-1001234567890" {
		t.Errorf("close form = %v", f)
	}
	if f := h.api.callsOf("reopenForumTopic")[0].form; f.Get("message_thread_id") != "43" {
		t.Errorf("reopen form = %v", f)
	}
}

func TestTopicErrorsAreTranslated(t *testing.T) {
	tests := []struct {
		name   string
		reply  apiReply
		want   error
		plain  bool // expect *APIError 400 and no domain sentinel
		wantIs []error
	}{
		{name: "thread not found", reply: errReply(400, "Bad Request: message thread not found"), want: domain.ErrTopicGone},
		{name: "topic closed", reply: errReply(400, "Bad Request: TOPIC_CLOSED"), want: domain.ErrTopicClosed},
		{name: "name invalid", reply: errReply(400, "Bad Request: TOPIC_NAME_INVALID"), plain: true},
		{name: "forbidden", reply: errReply(403, "Forbidden: bot is not a member of the supergroup chat"), want: domain.ErrForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.api.on("editForumTopic", func(url.Values) apiReply { return tc.reply })
			name := "x"
			err := h.gw.EditTopic(h.ctx, 42, domain.TopicPatch{Name: &name})
			if err == nil {
				t.Fatal("expected an error")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			var api *telegram.APIError
			if tc.plain {
				if !errors.As(err, &api) || api.Code != 400 {
					t.Errorf("err = %v, want *APIError 400", err)
				}
				if errors.Is(err, domain.ErrTopicGone) || errors.Is(err, domain.ErrTopicClosed) {
					t.Errorf("validation failure mapped to a topic-state sentinel: %v", err)
				}
			}
			if n := len(h.api.callsOf("editForumTopic")); n != 1 {
				t.Errorf("4xx was retried: %d calls", n)
			}
			if !strings.Contains(h.buf.String(), "editForumTopic failed") {
				t.Errorf("failure not logged: %s", h.buf.String())
			}
		})
	}
}

func TestTopicRetriesAfter429(t *testing.T) {
	h := newHarness(t)
	h.api.once("closeForumTopic", tooManyReply(3), okReply(true))
	if err := h.gw.CloseTopic(h.ctx, 42); err != nil {
		t.Fatal(err)
	}
	if n := len(h.api.callsOf("closeForumTopic")); n != 2 {
		t.Errorf("calls = %d, want 2", n)
	}
	sleeps := h.sleep.recorded()
	if len(sleeps) == 0 || sleeps[0] != 3*time.Second {
		t.Errorf("sleeps = %v, want first 3s", sleeps)
	}
	if !strings.Contains(h.buf.String(), "telegram call retry") {
		t.Errorf("retry not logged: %s", h.buf.String())
	}
}

func TestEditTopicUnchangedIsSuccess(t *testing.T) {
	h := newHarness(t)
	h.api.on("editForumTopic", func(url.Values) apiReply { return errReply(400, "Bad Request: TOPIC_NOT_MODIFIED") })
	name := "V3Jobs · claude"
	st := domain.StatusIdle
	if err := h.gw.EditTopic(h.ctx, 42, domain.TopicPatch{Name: &name, Status: &st}); err != nil {
		t.Fatalf("EditTopic = %v, want nil for TOPIC_NOT_MODIFIED", err)
	}
	if n := len(h.api.callsOf("editForumTopic")); n != 1 {
		t.Fatalf("editForumTopic calls = %d, want 1 (no retry)", n)
	}
}
