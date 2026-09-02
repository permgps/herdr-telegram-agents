package testkit

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// FakeTelegram is an in-memory domain.TelegramGateway that records every
// call in order and can fail the next call of a given method once.
//
// Recorded call shapes:
//
//	create:<name>:<status>   edit:<thread>:name=<n>,status=<s>
//	close:<thread>           reopen:<thread>
//	send:<thread>:<text>     send:<thread>:<text>:reply=<id>   (":notify" appended when set)
//	react:<thread>:<message>:<emoji>
//	document:<thread>:<name>:<bytes>   (":reply=<id>" appended when set)
//	rights
//
// Thread 0 in Send stands for the General topic and is always accepted.
type FakeTelegram struct {
	mu       sync.Mutex
	topics   map[int]*domain.Topic
	nextID   int
	calls    []string
	sent     []domain.Outgoing
	docs     []domain.Document
	failNext map[string]error
	rights   domain.Rights
	rightErr error
	events   chan domain.Event
	log      *slog.Logger
}

var _ domain.TelegramGateway = (*FakeTelegram)(nil)

// NewFakeTelegram returns a fake whose bot is a forum admin with topic rights.
func NewFakeTelegram(log *slog.Logger) *FakeTelegram {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &FakeTelegram{
		topics:   map[int]*domain.Topic{},
		nextID:   100,
		failNext: map[string]error{},
		rights:   domain.Rights{IsForum: true, IsAdmin: true, CanManageTopics: true, CanDeleteMessages: true},
		events:   make(chan domain.Event, 64),
		log:      log,
	}
}

// FailNext makes the next call of method (create, edit, close, reopen,
// send, document, react, rights) return err. Only one failure is queued per method.
func (f *FakeTelegram) FailNext(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext[method] = err
}

// SetRights changes what Rights reports.
func (f *FakeTelegram) SetRights(r domain.Rights, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rights, f.rightErr = r, err
}

// Push delivers an inbound Telegram event to the application.
func (f *FakeTelegram) Push(ev domain.Event) {
	f.events <- ev
}

// Calls returns every recorded call, in order.
func (f *FakeTelegram) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// Sent returns every message accepted by Send, in order.
func (f *FakeTelegram) Sent() []domain.Outgoing {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.Outgoing(nil), f.sent...)
}

// Documents returns every file accepted by SendDocument, in order.
func (f *FakeTelegram) Documents() []domain.Document {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.Document(nil), f.docs...)
}

// Reset forgets the recorded calls, sent messages and documents but keeps
// the topics.
func (f *FakeTelegram) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
	f.sent = nil
	f.docs = nil
}

// Topics returns a copy of the topics sorted by thread id.
func (f *FakeTelegram) Topics() []domain.Topic {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.Topic, 0, len(f.topics))
	for _, t := range f.topics {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ThreadID < out[j].ThreadID })
	return out
}

// Topic returns the topic with the thread id, if any.
func (f *FakeTelegram) Topic(threadID int) (domain.Topic, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.topics[threadID]
	if !ok {
		return domain.Topic{}, false
	}
	return *t, true
}

func (f *FakeTelegram) record(method, call string) error {
	f.calls = append(f.calls, call)
	f.log.Debug("fake telegram call", slog.String("call", call))
	if err, ok := f.failNext[method]; ok {
		delete(f.failNext, method)
		return err
	}
	return nil
}

func (f *FakeTelegram) CreateTopic(_ context.Context, name string, status domain.Status) (domain.Topic, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("create", fmt.Sprintf("create:%s:%s", name, status)); err != nil {
		return domain.Topic{}, err
	}
	f.nextID++
	t := &domain.Topic{ThreadID: f.nextID, Name: name, IconEmojiID: string(status)}
	f.topics[t.ThreadID] = t
	return *t, nil
}

func (f *FakeTelegram) EditTopic(_ context.Context, threadID int, patch domain.TopicPatch) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if patch.Empty() {
		return nil
	}
	var parts []string
	if patch.Name != nil {
		parts = append(parts, "name="+*patch.Name)
	}
	if patch.Status != nil {
		parts = append(parts, "status="+string(*patch.Status))
	}
	if err := f.record("edit", fmt.Sprintf("edit:%d:%s", threadID, strings.Join(parts, ","))); err != nil {
		return err
	}
	t, ok := f.topics[threadID]
	if !ok {
		return domain.ErrTopicGone
	}
	if patch.Name != nil {
		t.Name = *patch.Name
	}
	if patch.Status != nil {
		t.IconEmojiID = string(*patch.Status)
	}
	return nil
}

func (f *FakeTelegram) CloseTopic(_ context.Context, threadID int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("close", fmt.Sprintf("close:%d", threadID)); err != nil {
		return err
	}
	t, ok := f.topics[threadID]
	if !ok {
		return domain.ErrTopicGone
	}
	t.Closed = true
	return nil
}

func (f *FakeTelegram) ReopenTopic(_ context.Context, threadID int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("reopen", fmt.Sprintf("reopen:%d", threadID)); err != nil {
		return err
	}
	t, ok := f.topics[threadID]
	if !ok {
		return domain.ErrTopicGone
	}
	t.Closed = false
	return nil
}

func (f *FakeTelegram) Send(_ context.Context, out domain.Outgoing) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := fmt.Sprintf("send:%d:%s", out.ThreadID, out.Text)
	if out.ReplyTo != 0 {
		call += fmt.Sprintf(":reply=%d", out.ReplyTo)
	}
	if out.Notify {
		call += ":notify"
	}
	if err := f.record("send", call); err != nil {
		return err
	}
	if out.ThreadID != 0 {
		if _, ok := f.topics[out.ThreadID]; !ok {
			return domain.ErrTopicGone
		}
	}
	f.sent = append(f.sent, out)
	return nil
}

func (f *FakeTelegram) SendDocument(_ context.Context, doc domain.Document) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := fmt.Sprintf("document:%d:%s:%d", doc.ThreadID, doc.Name, len(doc.Data))
	if doc.ReplyTo != 0 {
		call += fmt.Sprintf(":reply=%d", doc.ReplyTo)
	}
	if err := f.record("document", call); err != nil {
		return err
	}
	if doc.ThreadID != 0 {
		if _, ok := f.topics[doc.ThreadID]; !ok {
			return domain.ErrTopicGone
		}
	}
	f.docs = append(f.docs, doc)
	return nil
}

func (f *FakeTelegram) React(_ context.Context, threadID, messageID int, emoji string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.record("react", fmt.Sprintf("react:%d:%d:%s", threadID, messageID, emoji))
}

func (f *FakeTelegram) Rights(context.Context) (domain.Rights, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("rights", "rights"); err != nil {
		return domain.Rights{}, err
	}
	return f.rights, f.rightErr
}

func (f *FakeTelegram) Events() <-chan domain.Event { return f.events }
