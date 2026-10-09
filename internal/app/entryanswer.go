package app

import (
	"context"
	"log/slog"
	"strconv"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// A plain message to an agent waiting at a dialog (Herdr refused the
// prompt with agent_blocked) is never typed into the dialog blind: at a
// permission prompt the text is ignored and the enter confirms the
// highlighted option, usually the approval (seen live 2026-10-09). It goes
// in only through a free-text entry the daemon can verify on the screen,
// the same way the ✏️ button does it by hand:
//
//  1. the dialog behind the latest buttons has a "Type something" entry
//     (domain.Dialog.TypedAnswerable) and the agent is Claude Code, the one
//     renderer whose typing state was read on a real screen;
//  2. a fresh screen still shows that very dialog (same options, labels and
//     entry); the entry's digit is pressed, unless the entry is already
//     focused, where a digit would be typed as text;
//  3. the screen is polled on the debouncer, never by sleeping on the
//     bridge goroutine, until domain.TextEntryOpen confirms the focused
//     entry; only then the text is typed and submitted.
//
// Any failed check types nothing and the operator is told the message was
// not sent.

// entryAnswer is a plain message waiting for its dialog's free-text entry
// to open after the entry's digit was pressed.
type entryAnswer struct {
	key       domain.Key
	threadID  int
	messageID int
	// keyboard is the message carrying the dialog's buttons; it is marked
	// like a ✏️ answer once the text went in.
	keyboard int
	text     string
	want     domain.Dialog
	polls    int
}

// answerKey is the debouncer key of the entry poll for a pane.
func answerKey(paneID string) domain.Key { return domain.Key{PaneID: answerPrefix + paneID} }

// blockedHint is the reply when a message to a blocked agent was not sent.
func blockedHint() string { return "⚠️ " + failureReason(domain.ErrAgentBlocked) }

// answerBlocked delivers text to a blocked agent through its dialog's
// free-text entry, or refuses with the hint. Only fatal Telegram errors are
// returned.
func (i *inbound) answerBlocked(ctx context.Context, msg domain.TopicMessage, key domain.Key, agent domain.Agent, text string) error {
	refuse := func(reason string) error {
		i.log.Info("blocked text not sent", slog.String("key", key.String()), slog.String("reason", reason),
			slog.Int("thread_id", msg.ThreadID), slog.Int("message_id", msg.MessageID), slog.Int("len", len(text)))
		return i.reply(ctx, msg.ThreadID, msg.MessageID, blockedHint())
	}
	if agent.Kind != domain.ClaudeKind {
		return refuse("agent_kind")
	}
	if _, busy := i.answers[key.PaneID]; busy {
		return refuse("answer_in_progress")
	}
	want, keyboard, ok := i.out.TextEntryDialog(key)
	if !ok {
		return refuse("no_text_entry")
	}
	screen, err := i.herdr.ReadScreen(ctx, key.PaneID, domain.ScreenDetection, blockedLines)
	if err != nil {
		i.log.Warn("blocked text screen read failed", slog.String("key", key.String()), slog.String("err", err.Error()))
		return refuse("screen_read")
	}
	a := entryAnswer{key: key, threadID: msg.ThreadID, messageID: msg.MessageID, keyboard: keyboard, text: text, want: want}
	clean := i.out.clean(key, screen.Text)
	if domain.TextEntryOpen(clean, want) {
		return i.typeAnswer(ctx, a)
	}
	if !domain.ParseDialog(clean).Same(want) {
		return refuse("dialog_changed")
	}
	if err := i.herdr.SendKeys(ctx, key.PaneID, []string{strconv.Itoa(want.TextEntry)}); err != nil {
		return i.failed(ctx, msg, key, "send_keys", err)
	}
	i.answers[key.PaneID] = a
	i.deb.ScheduleAfter(answerKey(key.PaneID), entryPollDelay)
	i.log.Info("blocked text waits for the dialog entry", slog.String("key", key.String()), slog.Int("entry", want.TextEntry),
		slog.Int("message_id", msg.MessageID))
	return nil
}

// fireAnswer polls the screen of a pending entryAnswer: the text is typed
// once the entry is focused; after entryPolls reads without it the
// operator is told the message was not sent. Only fatal Telegram errors
// are returned.
func (i *inbound) fireAnswer(ctx context.Context, paneID string) error {
	a, ok := i.answers[paneID]
	if !ok {
		return nil
	}
	if _, alive := i.agents(a.key); !alive {
		delete(i.answers, paneID)
		i.log.Info("blocked text dropped", slog.String("key", a.key.String()), slog.String("reason", "agent_gone"))
		return nil
	}
	screen, err := i.herdr.ReadScreen(ctx, paneID, domain.ScreenDetection, blockedLines)
	if err == nil && domain.TextEntryOpen(i.out.clean(a.key, screen.Text), a.want) {
		delete(i.answers, paneID)
		return i.typeAnswer(ctx, a)
	}
	a.polls++
	if a.polls < entryPolls {
		i.answers[paneID] = a
		i.deb.ScheduleAfter(answerKey(paneID), entryPollDelay)
		return nil
	}
	delete(i.answers, paneID)
	reason := "entry_not_open"
	if err != nil {
		reason = "screen_read"
	}
	i.log.Warn("blocked text not sent", slog.String("key", a.key.String()), slog.String("reason", reason), slog.Int("polls", a.polls))
	return i.reply(ctx, a.threadID, a.messageID, blockedHint())
}

// typeAnswer types the text into the focused entry, marks the dialog's
// buttons like a ✏️ answer and confirms the message like a prompt. Only
// fatal Telegram errors are returned.
func (i *inbound) typeAnswer(ctx context.Context, a entryAnswer) error {
	msg := domain.TopicMessage{ThreadID: a.threadID, MessageID: a.messageID}
	if _, err := typeIntoEntry(ctx, i.herdr, i.log, a.key.PaneID, a.text); err != nil {
		return i.failed(ctx, msg, a.key, "send_text", err)
	}
	i.log.Info("blocked text typed into the dialog entry", slog.String("key", a.key.String()), slog.Int("entry", a.want.TextEntry),
		slog.Int("message_id", a.messageID), slog.Int("len", len(a.text)))
	if err := i.out.EntryAnswered(ctx, a.key, a.keyboard, a.text); err != nil {
		return err
	}
	return i.out.PromptSent(ctx, a.key, a.threadID, a.messageID)
}

// TextEntryDialog returns the dialog behind key's latest buttons and the
// message carrying them when a plain message may answer it by typing (see
// domain.Dialog.TypedAnswerable).
func (o *outbound) TextEntryDialog(key domain.Key) (domain.Dialog, int, bool) {
	kb, ok := o.keyboards[key]
	if !ok {
		return domain.Dialog{}, 0, false
	}
	d := domain.Dialog{Choices: kb.choices, Multi: kb.multi, TextEntry: kb.textEntry, TextLabel: kb.textLabel}
	return d, kb.messageID, d.TypedAnswerable()
}

// EntryAnswered marks the dialog's buttons after a plain message was typed
// into its free-text entry: "✅ ✏️ · <head of the text>", as after a ✏️
// answer. Only fatal Telegram errors are returned.
func (o *outbound) EntryAnswered(ctx context.Context, key domain.Key, messageID int, text string) error {
	if w, ok := o.typing[key]; ok && w.messageID == messageID {
		o.endTyping(key, "typed")
	}
	return o.markTyping(ctx, key, typingWait{messageID: messageID}, "✅ ✏️ · "+headOf(text, typingHeadRunes))
}
