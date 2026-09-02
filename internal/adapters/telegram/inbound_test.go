package telegram_test

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// testBotID is the gateway's own user id in the harness.
const testBotID int64 = 42

// ownService builds a service message Telegram posts when the bot itself
// changes a topic; the sender is the bot.
func ownService(id, thread int, mutate func(*models.Message)) *models.Update {
	u := service(thread, mutate)
	u.Message.ID = id
	u.Message.From = &models.User{ID: testBotID, IsBot: true}
	return u
}

func topicMessage(chatID, fromID int64, thread int, text string) *models.Update {
	m := &models.Message{
		ID:              1,
		Chat:            models.Chat{ID: chatID, Type: models.ChatTypeSupergroup},
		MessageThreadID: thread,
		IsTopicMessage:  thread != 0,
		Text:            text,
	}
	if fromID != 0 {
		m.From = &models.User{ID: fromID}
	}
	return &models.Update{ID: 1, Message: m}
}

func service(thread int, mutate func(*models.Message)) *models.Update {
	u := topicMessage(testChatID, testOperator, thread, "")
	mutate(u.Message)
	return u
}

func memberUpdate(chatID int64, member models.ChatMember) *models.Update {
	return &models.Update{ID: 2, MyChatMember: &models.ChatMemberUpdated{
		Chat:          models.Chat{ID: chatID},
		From:          models.User{ID: testOperator},
		NewChatMember: member,
	}}
}

func expectEvent(t *testing.T, events <-chan domain.Event) domain.Event {
	t.Helper()
	select {
	case ev := <-events:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("no event emitted")
		return nil
	}
}

func expectNoEvent(t *testing.T, events <-chan domain.Event) {
	t.Helper()
	select {
	case ev := <-events:
		t.Fatalf("unexpected event %#v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestInboundDropsForeignAndUnauthorised(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	tests := []struct {
		name   string
		update *models.Update
		reason string
	}{
		{"foreign chat", topicMessage(-100999, testOperator, 5, "hi"), "reason=foreign_chat"},
		{"not operator", topicMessage(testChatID, 777, 5, "hi"), "reason=not_operator"},
		{"no sender", topicMessage(testChatID, 0, 5, "hi"), "reason=not_operator"},
		{"general topic", topicMessage(testChatID, testOperator, 0, "hi"), "reason=general_topic"},
		{"edited message", &models.Update{EditedMessage: topicMessage(testChatID, testOperator, 5, "edit").Message}, ""},
		{"foreign my_chat_member", memberUpdate(-100999, models.ChatMember{Type: models.ChatMemberTypeMember}), "reason=foreign_chat"},
		{"sticker only", service(5, func(m *models.Message) { m.Sticker = &models.Sticker{} }), "reason=unsupported_message"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h.bot.ProcessUpdate(ctx, tc.update)
			expectNoEvent(t, h.gw.Events())
			if tc.reason != "" && !strings.Contains(h.buf.String(), tc.reason) {
				t.Errorf("drop reason %q not logged: %s", tc.reason, h.buf.String())
			}
		})
	}
}

func TestInboundTopicEvents(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	h.bot.ProcessUpdate(ctx, topicMessage(testChatID, testOperator, 42, "run the tests"))
	if got, ok := expectEvent(t, h.gw.Events()).(domain.TopicMessage); !ok || got.ThreadID != 42 || got.FromID != testOperator || got.Text != "run the tests" {
		t.Errorf("event = %#v", got)
	}

	h.bot.ProcessUpdate(ctx, service(42, func(m *models.Message) { m.ForumTopicEdited = &models.ForumTopicEdited{Name: "reviewer"} }))
	if got, ok := expectEvent(t, h.gw.Events()).(domain.TopicRenamed); !ok || got.ThreadID != 42 || got.Name != "reviewer" {
		t.Errorf("event = %#v", got)
	}

	// An icon-only edit has no name and is ignored.
	h.bot.ProcessUpdate(ctx, service(42, func(m *models.Message) { m.ForumTopicEdited = &models.ForumTopicEdited{IconCustomEmojiID: "x"} }))
	expectNoEvent(t, h.gw.Events())

	h.bot.ProcessUpdate(ctx, service(42, func(m *models.Message) { m.ForumTopicClosed = &models.ForumTopicClosed{} }))
	if got, ok := expectEvent(t, h.gw.Events()).(domain.TopicClosed); !ok || got.ThreadID != 42 {
		t.Errorf("event = %#v", got)
	}

	h.bot.ProcessUpdate(ctx, service(43, func(m *models.Message) { m.ForumTopicReopened = &models.ForumTopicReopened{} }))
	if got, ok := expectEvent(t, h.gw.Events()).(domain.TopicReopened); !ok || got.ThreadID != 43 {
		t.Errorf("event = %#v", got)
	}
	if !strings.Contains(h.buf.String(), "kind=topic_message thread_id=42") {
		t.Errorf("emit not logged: %s", h.buf.String())
	}
}

func TestInboundRightsChanged(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	h.bot.ProcessUpdate(ctx, memberUpdate(testChatID, models.ChatMember{
		Type:          models.ChatMemberTypeAdministrator,
		Administrator: &models.ChatMemberAdministrator{CanManageTopics: true},
	}))
	if got, ok := expectEvent(t, h.gw.Events()).(domain.RightsChanged); !ok || !got.CanManageTopics {
		t.Errorf("event = %#v", got)
	}

	h.bot.ProcessUpdate(ctx, memberUpdate(testChatID, models.ChatMember{
		Type:          models.ChatMemberTypeAdministrator,
		Administrator: &models.ChatMemberAdministrator{CanManageTopics: false},
	}))
	if got, ok := expectEvent(t, h.gw.Events()).(domain.RightsChanged); !ok || got.CanManageTopics {
		t.Errorf("event = %#v", got)
	}

	h.bot.ProcessUpdate(ctx, memberUpdate(testChatID, models.ChatMember{Type: models.ChatMemberTypeMember, Member: &models.ChatMemberMember{}}))
	if got, ok := expectEvent(t, h.gw.Events()).(domain.RightsChanged); !ok || got.CanManageTopics {
		t.Errorf("event = %#v", got)
	}
}

func TestEmitDoesNotBlockPastContext(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Fill the buffer, then one more with a dead context must return.
	for i := 0; i < 64; i++ {
		h.bot.ProcessUpdate(context.Background(), topicMessage(testChatID, testOperator, 1, "x"))
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.bot.ProcessUpdate(ctx, topicMessage(testChatID, testOperator, 1, "overflow"))
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("emit blocked with a cancelled context")
	}
	if !strings.Contains(h.buf.String(), "telegram event dropped, context done") {
		t.Errorf("drop not logged: %s", h.buf.String())
	}
}

// TestInboundDeletesOwnServiceMessages: "X changed the topic name/icon"
// notices caused by the bot's own edits are deleted instead of piling up
// in the topic, and never become events. Topic creation notices cannot be
// deleted through the Bot API and are left alone.
func TestInboundDeletesOwnServiceMessages(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	deleted := make(chan int, 8)
	h.api.on("deleteMessage", func(f url.Values) apiReply {
		id, _ := strconv.Atoi(f.Get("message_id"))
		deleted <- id
		return okReply(true)
	})
	expectDeleted := func(t *testing.T, want int) {
		t.Helper()
		select {
		case id := <-deleted:
			if id != want {
				t.Fatalf("deleted message %d, want %d", id, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("service message %d not deleted", want)
		}
	}
	tests := []struct {
		name   string
		update *models.Update
		want   int
	}{
		{"icon changed", ownService(11, 42, func(m *models.Message) { m.ForumTopicEdited = &models.ForumTopicEdited{IconCustomEmojiID: "x"} }), 11},
		{"renamed", ownService(12, 42, func(m *models.Message) { m.ForumTopicEdited = &models.ForumTopicEdited{Name: "V3Jobs · claude"} }), 12},
		{"closed", ownService(13, 42, func(m *models.Message) { m.ForumTopicClosed = &models.ForumTopicClosed{} }), 13},
		{"reopened", ownService(14, 42, func(m *models.Message) { m.ForumTopicReopened = &models.ForumTopicReopened{} }), 14},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h.bot.ProcessUpdate(ctx, tc.update)
			expectNoEvent(t, h.gw.Events())
			expectDeleted(t, tc.want)
		})
	}

	t.Run("created is kept", func(t *testing.T) {
		h.bot.ProcessUpdate(ctx, ownService(15, 42, func(m *models.Message) { m.ForumTopicCreated = &models.ForumTopicCreated{Name: "n"} }))
		expectNoEvent(t, h.gw.Events())
		select {
		case id := <-deleted:
			t.Fatalf("creation notice %d deleted", id)
		case <-time.After(50 * time.Millisecond):
		}
	})

	t.Run("operator rename still an event", func(t *testing.T) {
		h.bot.ProcessUpdate(ctx, service(42, func(m *models.Message) { m.ID = 16; m.ForumTopicEdited = &models.ForumTopicEdited{Name: "by hand"} }))
		if ev, ok := expectEvent(t, h.gw.Events()).(domain.TopicRenamed); !ok || ev.Name != "by hand" {
			t.Fatalf("event = %#v", ev)
		}
		select {
		case id := <-deleted:
			t.Fatalf("operator's notice %d deleted", id)
		case <-time.After(50 * time.Millisecond):
		}
	})
}

// TestInboundServiceDeleteWithoutRight: without "Delete messages" the first
// failure is a warning naming the right, later ones stay at debug.
func TestInboundServiceDeleteWithoutRight(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.api.on("deleteMessage", func(url.Values) apiReply { return errReply(400, "Bad Request: message can't be deleted") })
	h.bot.ProcessUpdate(ctx, ownService(21, 42, func(m *models.Message) { m.ForumTopicEdited = &models.ForumTopicEdited{IconCustomEmojiID: "x"} }))
	h.bot.ProcessUpdate(ctx, ownService(22, 42, func(m *models.Message) { m.ForumTopicEdited = &models.ForumTopicEdited{IconCustomEmojiID: "y"} }))
	expectNoEvent(t, h.gw.Events())
	if !h.buf.contains("Delete messages", 2*time.Second) {
		t.Fatalf("no hint about the missing right: %s", h.buf.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(h.api.callsOf("deleteMessage")) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := strings.Count(h.buf.String(), "level=WARN msg=\"service message not deleted"); n != 1 {
		t.Fatalf("warned %d times, want 1: %s", n, h.buf.String())
	}
}
