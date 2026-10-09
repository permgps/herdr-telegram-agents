package app

import (
	"context"
	"errors"
	"log/slog"
	"strconv"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// errAnswerPending tells effect that a plain message is waiting for its
// dialog's free-text entry: the entry poll reports the outcome, so there is
// neither a notice nor a reaction yet.
var errAnswerPending = errors.New("dialog entry answer pending")

// privateEntryPolls bounds how many private ticks (one a second) wait for a
// dialog's free-text entry to take the focus.
const privateEntryPolls = 3

// privateAnswer is a recipient's plain message waiting for the free-text
// entry of the dialog the recipient was shown (see answerBlocked for the
// rule both paths follow).
type privateAnswer struct {
	origin domain.ShareOrigin
	action domain.ShareAction
	text   string
	want   domain.Dialog
	polls  int
}

// prompt sends a recipient's text as a prompt. When Herdr refuses it
// because the agent waits at a dialog, the text goes in only through the
// dialog's free-text entry, verified on the screen; otherwise nothing is
// typed and the recipient is told why.
func (p *PrivateControl) prompt(ctx context.Context, o domain.ShareOrigin, action domain.ShareAction, text string) error {
	// The dialog the recipient was shown must be looked up before effect:
	// it drops the agent's buttons before acting.
	want, shown := p.shownTextEntry(o)
	return p.effect(ctx, o, action, func(ctx context.Context) error {
		err := p.Herdr.Prompt(ctx, o.Key.PaneID, text)
		if !errors.Is(err, domain.ErrAgentBlocked) {
			return err
		}
		if !shown {
			p.log().Info("private blocked text not sent", slog.String("key", o.Key.String()), slog.Int64("actor", o.ActorID), slog.String("reason", "no_text_entry"))
			return err
		}
		return p.startPrivateAnswer(ctx, o, action, text, want, err)
	})
}

// shownTextEntry returns the dialog of the recipient's live "text" button
// for the agent's current dialog, when it may be answered by typing.
func (p *PrivateControl) shownTextEntry(o domain.ShareOrigin) (domain.Dialog, bool) {
	a, live := p.Agent(o.Key)
	if !live || a.Kind != domain.ClaudeKind {
		return domain.Dialog{}, false
	}
	now := p.Now()
	for _, b := range p.callbacks {
		if b.kind == "text" && b.origin.Key == o.Key && b.origin.ActorID == o.ActorID && b.origin.GrantID == o.GrantID &&
			b.seq == a.StateChangeSeq && now.Before(b.expires) && b.dialog.TypedAnswerable() {
			return b.dialog, true
		}
	}
	return domain.Dialog{}, false
}

// startPrivateAnswer checks the screen against the shown dialog and either
// types at once (the entry already has the focus), presses the entry's
// digit and leaves the rest to pollPrivateAnswers, or refuses with blocked.
func (p *PrivateControl) startPrivateAnswer(ctx context.Context, o domain.ShareOrigin, action domain.ShareAction, text string, want domain.Dialog, blocked error) error {
	if _, busy := p.answers[o.Key]; busy {
		return blocked
	}
	screen, err := p.Herdr.ReadScreen(ctx, o.Key.PaneID, domain.ScreenDetection, domain.MaxScreenLines)
	if err != nil {
		p.log().Warn("private blocked text screen read failed", slog.String("key", o.Key.String()), slog.String("err", err.Error()))
		return blocked
	}
	if domain.TextEntryOpen(screen.Text, want) {
		_, err := typeIntoEntry(ctx, p.Herdr, p.log(), o.Key.PaneID, text)
		return err
	}
	if !domain.ParseDialog(screen.Text).Same(want) {
		p.log().Info("private blocked text not sent", slog.String("key", o.Key.String()), slog.Int64("actor", o.ActorID), slog.String("reason", "dialog_changed"))
		return blocked
	}
	if err := p.Herdr.SendKeys(ctx, o.Key.PaneID, []string{strconv.Itoa(want.TextEntry)}); err != nil {
		return err
	}
	if p.answers == nil {
		p.answers = map[domain.Key]privateAnswer{}
	}
	p.answers[o.Key] = privateAnswer{origin: o, action: action, text: text, want: want}
	p.log().Info("private blocked text waits for the dialog entry", slog.String("key", o.Key.String()), slog.Int64("actor", o.ActorID), slog.Int("entry", want.TextEntry))
	return errAnswerPending
}

// pollPrivateAnswers runs on every private tick: a pending answer is typed
// once the screen shows the entry focused, under a fresh authorization; it
// is dropped with a notice after privateEntryPolls ticks, or silently when
// the grant no longer allows it.
func (p *PrivateControl) pollPrivateAnswers(ctx context.Context) {
	for key, a := range p.answers {
		callCtx, done, d := p.Sharing.Begin(ctx, a.origin, a.action)
		if !d.Allowed {
			delete(p.answers, key)
			continue
		}
		screen, err := p.Herdr.ReadScreen(callCtx, key.PaneID, domain.ScreenDetection, domain.MaxScreenLines)
		if err == nil && domain.TextEntryOpen(screen.Text, a.want) {
			delete(p.answers, key)
			_, terr := typeIntoEntry(callCtx, p.Herdr, p.log(), key.PaneID, a.text)
			done()
			p.Sharing.log.Info("private agent action", "actor_id", a.origin.ActorID, "grant_id", a.origin.GrantID, "action", string(a.action), "success", terr == nil, "via", "dialog_entry")
			if terr != nil {
				_ = p.send(ctx, a.origin, "The agent action failed. It was not retried.")
				continue
			}
			_ = p.Telegram.ReactAt(ctx, domain.MessageAddress{ChatID: a.origin.Address.ChatID, MessageID: a.origin.MessageID}, "👍", p.Sharing.Guard(a.origin, domain.ShareOutput))
			continue
		}
		done()
		a.polls++
		if a.polls < privateEntryPolls {
			p.answers[key] = a
			continue
		}
		delete(p.answers, key)
		p.log().Warn("private blocked text not sent", slog.String("key", key.String()), slog.Int64("actor", a.origin.ActorID), slog.String("reason", "entry_not_open"))
		_ = p.send(ctx, a.origin, privateBlockedNotice)
	}
}
