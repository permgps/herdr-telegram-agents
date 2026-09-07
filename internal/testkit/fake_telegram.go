package testkit

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// FakeTelegram is an in-memory domain.TelegramGateway that records every
// call in order and can fail the next call of a given method once.
//
// Recorded call shapes:
//
//	create:<name>:<status>   edit:<thread>:name=<n>,status=<s>
//	close:<thread>           reopen:<thread>          delete:<thread>
//	send:<thread>:<text>     send:<thread>:<text>:reply=<id>   (":notify" and ":buttons=<n>" appended when set)
//	buttons:<message>:<text1>|<text2>   (empty text list when the keyboard is removed)
//	answer:<callback>:<text>
//	edittext:<message>:<text>:buttons=<n>
//	react:<thread>:<message>:<emoji>
//	document:<thread>:<name>:<bytes>   (":reply=<id>" appended when set)
//	direct:<user>:<text>     (":notify" and ":buttons=<n>" appended when set)
//	probe:<user>
//	pin:<message>            unpin:<message>          deletemsg:<message>
//	rights
//	noticedelay:<seconds>    noticedelay:keep
//	access:<operators>/<observers>   (counts)
//
// Thread 0 in Send stands for the General topic and is always accepted.
// Send and SendDirect hand out message ids from 1000 upwards; EditButtons
// and EditText keep the last keyboard per message id for Buttons, EditText
// the last text for Text. DeleteMessage forgets the text, so a later
// EditText of that id is domain.ErrMessageGone. SetStatusIcons is
// remembered for Icons; IconPack answers with the slice given to
// SetIconPack (by default the six defaults plus 🔥 🤖 🧠).
type FakeTelegram struct {
	mu        sync.Mutex
	topics    map[int]*domain.Topic
	nextID    int
	nextMsgID int
	calls     []string
	sent      []domain.Outgoing
	direct    []domain.Outgoing
	pinned    map[int]bool
	buttons   map[int][]domain.Button
	texts     map[int]string
	icons     domain.StatusIcons
	operators []int64
	observers []int64
	pack      []string
	docs      []domain.Document
	files     map[string][]byte
	// downloadDelay makes Download sleep that long (real time) so tests
	// can see that a download does not stall the bridge loop.
	downloadDelay time.Duration
	failNext      map[string]error
	rights        domain.Rights
	rightErr      error
	events        chan domain.Event
	log           *slog.Logger
}

var _ domain.TelegramGateway = (*FakeTelegram)(nil)

// NewFakeTelegram returns a fake whose bot is a forum admin with topic rights.
func NewFakeTelegram(log *slog.Logger) *FakeTelegram {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &FakeTelegram{
		topics:    map[int]*domain.Topic{},
		nextID:    100,
		nextMsgID: 1000,
		pinned:    map[int]bool{},
		buttons:   map[int][]domain.Button{},
		texts:     map[int]string{},
		icons:     domain.DefaultStatusIcons(),
		files:     map[string][]byte{},
		pack:      []string{"⚡️", "✅", "❓", "🏆", "👀", "🏁", "🔥", "🤖", "🧠"},
		failNext:  map[string]error{},
		rights:    domain.Rights{IsForum: true, IsAdmin: true, CanManageTopics: true, CanDeleteMessages: true},
		events:    make(chan domain.Event, 64),
		log:       log,
	}
}

// FailNext makes the next call of method (create, edit, close, reopen,
// send, direct, probe, pin, unpin, deletemsg, document, react, rights,
// buttons, answer, edittext, download) return err.
// Only one failure is queued per method.
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

// Direct returns every message accepted by SendDirect, in order.
func (f *FakeTelegram) Direct() []domain.Outgoing {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.Outgoing(nil), f.direct...)
}

// Pinned reports whether the message is pinned after the last Pin or
// Unpin that touched it.
func (f *FakeTelegram) Pinned(messageID int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pinned[messageID]
}

// Buttons returns the keyboard a message carries after the last Send,
// EditButtons or EditText that touched it; nil when it has none.
func (f *FakeTelegram) Buttons(messageID int) []domain.Button {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.Button(nil), f.buttons[messageID]...)
}

// Text returns the text a message carries after the last EditText that
// touched it, or the text it was sent with.
func (f *FakeTelegram) Text(messageID int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.texts[messageID]
}

// Icons returns the table given to the last SetStatusIcons.
func (f *FakeTelegram) Icons() domain.StatusIcons {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.icons
}

// Access returns the lists given to the last SetAccess (copies).
func (f *FakeTelegram) Access() (operators, observers []int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.operators...), append([]int64(nil), f.observers...)
}

// SetIconPack replaces what IconPack answers; nil means "pack unknown".
func (f *FakeTelegram) SetIconPack(pack []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pack = append([]string(nil), pack...)
}

// Documents returns every file accepted by SendDocument, in order.
func (f *FakeTelegram) Documents() []domain.Document {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.Document(nil), f.docs...)
}

// Reset forgets the recorded calls, sent messages, direct messages and
// documents but keeps the topics.
func (f *FakeTelegram) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
	f.sent = nil
	f.direct = nil
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

func (f *FakeTelegram) DeleteTopic(_ context.Context, threadID int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("delete", fmt.Sprintf("delete:%d", threadID)); err != nil {
		return err
	}
	if _, ok := f.topics[threadID]; !ok {
		return domain.ErrTopicGone
	}
	delete(f.topics, threadID)
	return nil
}

func (f *FakeTelegram) Send(_ context.Context, out domain.Outgoing) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := fmt.Sprintf("send:%d:%s", out.ThreadID, out.Text)
	if out.ReplyTo != 0 {
		call += fmt.Sprintf(":reply=%d", out.ReplyTo)
	}
	if out.Notify {
		call += ":notify"
	}
	if len(out.Buttons) > 0 {
		call += fmt.Sprintf(":buttons=%d", len(out.Buttons))
	}
	if out.Markdown {
		call += ":markdown"
	}
	if out.ForceReply {
		call += ":forcereply"
	}
	if err := f.record("send", call); err != nil {
		return 0, err
	}
	if out.ThreadID != 0 {
		if _, ok := f.topics[out.ThreadID]; !ok {
			return 0, domain.ErrTopicGone
		}
	}
	f.sent = append(f.sent, out)
	id := f.nextMsgID
	f.nextMsgID++
	f.texts[id] = out.Text
	if len(out.Buttons) > 0 {
		f.buttons[id] = append([]domain.Button(nil), out.Buttons...)
	}
	return id, nil
}

// SendDirect records the message for the user's private chat and hands
// out a message id like Send. Nothing in the fake models a closed chat;
// use FailNext("direct", domain.ErrForbidden) for that.
func (f *FakeTelegram) SendDirect(_ context.Context, userID int64, out domain.Outgoing) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := fmt.Sprintf("direct:%d:%s", userID, out.Text)
	if out.Notify {
		call += ":notify"
	}
	if len(out.Buttons) > 0 {
		call += fmt.Sprintf(":buttons=%d", len(out.Buttons))
	}
	if err := f.record("direct", call); err != nil {
		return 0, err
	}
	f.direct = append(f.direct, out)
	id := f.nextMsgID
	f.nextMsgID++
	f.texts[id] = out.Text
	if len(out.Buttons) > 0 {
		f.buttons[id] = append([]domain.Button(nil), out.Buttons...)
	}
	return id, nil
}

// ProbeDirect records probe:<user>; FailNext("probe", domain.ErrForbidden)
// stands for a chat the bot may not write to.
func (f *FakeTelegram) ProbeDirect(_ context.Context, userID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.record("probe", fmt.Sprintf("probe:%d", userID))
}

// Pin records pin:<message>; a message the fake never sent is
// domain.ErrMessageGone.
func (f *FakeTelegram) Pin(_ context.Context, messageID int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("pin", fmt.Sprintf("pin:%d", messageID)); err != nil {
		return err
	}
	if _, ok := f.texts[messageID]; !ok {
		return domain.ErrMessageGone
	}
	f.pinned[messageID] = true
	return nil
}

// Unpin records unpin:<message>; unpinning an unknown or unpinned message
// succeeds, as in Telegram.
func (f *FakeTelegram) Unpin(_ context.Context, messageID int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("unpin", fmt.Sprintf("unpin:%d", messageID)); err != nil {
		return err
	}
	delete(f.pinned, messageID)
	return nil
}

// DeleteMessage records deletemsg:<message> and forgets the message; a
// message the fake does not know is domain.ErrMessageGone.
func (f *FakeTelegram) DeleteMessage(_ context.Context, messageID int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("deletemsg", fmt.Sprintf("deletemsg:%d", messageID)); err != nil {
		return err
	}
	if _, ok := f.texts[messageID]; !ok {
		return domain.ErrMessageGone
	}
	delete(f.texts, messageID)
	delete(f.buttons, messageID)
	delete(f.pinned, messageID)
	return nil
}

func (f *FakeTelegram) EditText(_ context.Context, messageID int, text string, _ bool, buttons []domain.Button) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("edittext", fmt.Sprintf("edittext:%d:%s:buttons=%d", messageID, text, len(buttons))); err != nil {
		return err
	}
	if _, ok := f.texts[messageID]; !ok && messageID >= 1000 && messageID < f.nextMsgID {
		return domain.ErrMessageGone
	}
	f.texts[messageID] = text
	if len(buttons) == 0 {
		delete(f.buttons, messageID)
		return nil
	}
	f.buttons[messageID] = append([]domain.Button(nil), buttons...)
	return nil
}

func (f *FakeTelegram) SetStatusIcons(icons domain.StatusIcons) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.icons = icons
	f.log.Debug("fake telegram icons", slog.String("working", icons.Working))
}

func (f *FakeTelegram) SetNoticeDelay(delay time.Duration, del bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !del {
		f.calls = append(f.calls, "noticedelay:keep")
		f.log.Debug("fake telegram notice delay", slog.Bool("keep", true))
		return
	}
	f.calls = append(f.calls, fmt.Sprintf("noticedelay:%d", int(delay/time.Second)))
	f.log.Debug("fake telegram notice delay", slog.Int64("seconds", int64(delay/time.Second)))
}

func (f *FakeTelegram) SetAccess(operators, observers []int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.operators = append([]int64(nil), operators...)
	f.observers = append([]int64(nil), observers...)
	f.calls = append(f.calls, fmt.Sprintf("access:%d/%d", len(operators), len(observers)))
	f.log.Debug("fake telegram access", slog.Int("operators", len(operators)), slog.Int("observers", len(observers)))
}

func (f *FakeTelegram) IconPack() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.pack...)
}

func (f *FakeTelegram) EditButtons(_ context.Context, messageID int, buttons []domain.Button) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	texts := make([]string, 0, len(buttons))
	for _, b := range buttons {
		texts = append(texts, b.Text)
	}
	if err := f.record("buttons", fmt.Sprintf("buttons:%d:%s", messageID, strings.Join(texts, "|"))); err != nil {
		return err
	}
	if len(buttons) == 0 {
		delete(f.buttons, messageID)
		return nil
	}
	f.buttons[messageID] = append([]domain.Button(nil), buttons...)
	return nil
}

func (f *FakeTelegram) AnswerButton(_ context.Context, callbackID, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.record("answer", fmt.Sprintf("answer:%s:%s", callbackID, text))
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

// SetFile scripts the bytes Download answers for fileID.
func (f *FakeTelegram) SetFile(fileID string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[fileID] = append([]byte(nil), data...)
}

// SetDownloadDelay makes every Download sleep d before answering.
func (f *FakeTelegram) SetDownloadDelay(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.downloadDelay = d
}

// Download answers the scripted bytes for fileID, recorded as
// download:<fileID>:<max>; an unscripted id is an error and a scripted
// file longer than max is domain.ErrFileTooBig.
func (f *FakeTelegram) Download(ctx context.Context, fileID string, max int64) ([]byte, error) {
	f.mu.Lock()
	delay := f.downloadDelay
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("download", fmt.Sprintf("download:%s:%d", fileID, max)); err != nil {
		return nil, err
	}
	data, ok := f.files[fileID]
	if !ok {
		return nil, fmt.Errorf("download %s: no such file in the fake", fileID)
	}
	if int64(len(data)) > max {
		return nil, domain.ErrFileTooBig
	}
	return append([]byte(nil), data...), nil
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
