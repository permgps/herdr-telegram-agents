package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// A prompt typed into the pane of an agent Herdr does not drive ends with
// an Enter the agent can drop: Claude Code in a WSL pane kept the text in
// its input box while every Herdr call answered ok. The bridge goroutine
// never waits for it, since it posts for every other agent too:
// typedVerifyDelay later the debouncer reads the visible screen, and a
// line still in the input box (domain.PromptStuck) gets a reply. A failed
// read or a box the check cannot see stays silent: a wrong alarm would make
// the operator press enter twice.

const (
	// typedVerifyDelay is long enough for the agent to clear its input box
	// on a taken Enter, short enough to keep the reply close to the message.
	typedVerifyDelay = 1200 * time.Millisecond
	// typedVerifyLines is how much of the visible screen the check reads.
	typedVerifyLines = 60
	// typedPrefix marks the debouncer key of the check.
	typedPrefix = "typed:"
)

// typedCheck is a prompt typed into a pane, waiting for its screen check.
type typedCheck struct {
	key       domain.Key
	threadID  int
	messageID int
	line      string
}

// typedKey is the debouncer key of the typed-prompt check for a pane.
func typedKey(paneID string) domain.Key { return domain.Key{PaneID: typedPrefix + paneID} }

// checkTyped arms the screen check of text, typed into the pane of key for
// the message messageID. A newer prompt to the same pane replaces it.
func (i *inbound) checkTyped(key domain.Key, threadID, messageID int, text string) {
	line, _ := domain.DialogText(text)
	if line == "" {
		return
	}
	if i.typedChecks == nil {
		i.typedChecks = map[string]typedCheck{}
	}
	i.typedChecks[key.PaneID] = typedCheck{key: key, threadID: threadID, messageID: messageID, line: line}
	i.deb.ScheduleAfter(typedKey(key.PaneID), typedVerifyDelay)
}

// fireTyped reads the pane of a pending typed prompt and replies when the
// line still sits in the agent's input box. Only fatal Telegram errors are
// returned.
func (i *inbound) fireTyped(ctx context.Context, paneID string) error {
	c, ok := i.typedChecks[paneID]
	if !ok {
		return nil
	}
	delete(i.typedChecks, paneID)
	if _, alive := i.agents(c.key); !alive {
		i.log.Debug("typed prompt check dropped", slog.String("key", c.key.String()), slog.String("reason", "agent_gone"))
		return nil
	}
	screen, err := i.herdr.ReadScreen(ctx, paneID, domain.ScreenVisible, typedVerifyLines)
	if err != nil {
		i.log.Warn("typed prompt not verified", slog.String("key", c.key.String()), slog.String("err", err.Error()))
		return nil
	}
	if !domain.PromptStuck(screen.Text, c.line) {
		i.log.Debug("typed prompt taken", slog.String("key", c.key.String()))
		return nil
	}
	i.log.Warn("[FIX] typed prompt not submitted", slog.String("key", c.key.String()),
		slog.Int("message_id", c.messageID), slog.Int("text_len", len(c.line)))
	return i.reply(ctx, c.threadID, c.messageID, "⚠️ "+failureReason(domain.ErrPromptNotSubmitted))
}

// privateNotSubmittedNotice tells a recipient that the text waits in the
// agent's input box.
const privateNotSubmittedNotice = "The text was typed into the pane, but the agent did not take the enter: it waits in the input box. Press enter there, or send /keys enter."

// privateTyped is a recipient's prompt typed into a pane, checked on the
// first private tick after due.
type privateTyped struct {
	origin domain.ShareOrigin
	action domain.ShareAction
	line   string
	due    time.Time
}

// checkTyped arms the screen check of a recipient's typed prompt, the same
// check the owner's topic gets (see fireTyped).
func (p *PrivateControl) checkTyped(o domain.ShareOrigin, action domain.ShareAction, text string) {
	line, _ := domain.DialogText(text)
	if line == "" {
		return
	}
	p.typedChecks = append(p.typedChecks, privateTyped{origin: o, action: action, line: line, due: p.Now().Add(typedVerifyDelay)})
}

// pollTypedChecks runs on every private tick: a due check reads the pane
// under a fresh authorization and tells the recipient when the line still
// sits in the input box. A revoked grant drops the check silently.
func (p *PrivateControl) pollTypedChecks(ctx context.Context) {
	now := p.Now()
	pending := p.typedChecks[:0]
	for _, c := range p.typedChecks {
		if now.Before(c.due) {
			pending = append(pending, c)
			continue
		}
		callCtx, done, d := p.Sharing.Begin(ctx, c.origin, c.action)
		if !d.Allowed {
			continue
		}
		screen, err := p.Herdr.ReadScreen(callCtx, c.origin.Key.PaneID, domain.ScreenVisible, typedVerifyLines)
		done()
		if err != nil {
			p.log().Warn("private typed prompt not verified", slog.String("key", c.origin.Key.String()), slog.String("err", err.Error()))
			continue
		}
		if !domain.PromptStuck(screen.Text, c.line) {
			p.log().Debug("private typed prompt taken", slog.String("key", c.origin.Key.String()))
			continue
		}
		p.log().Warn("[FIX] private typed prompt not submitted", slog.String("key", c.origin.Key.String()),
			slog.Int64("actor", c.origin.ActorID), slog.Int("text_len", len(c.line)))
		_ = p.send(ctx, c.origin, privateNotSubmittedNotice)
	}
	p.typedChecks = pending
}
