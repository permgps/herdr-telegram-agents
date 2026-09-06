package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// agentLookup resolves the current view of a live agent; ok is false once
// the agent has exited.
type agentLookup func(domain.Key) (domain.Agent, bool)

// outbound posts agent screens into topics: the tail of the detection
// screen when an agent turns blocked (with a notification) or done
// (silent), and the visible screen on request. It runs on the bridge
// goroutine and never touches the mapping directly.
type outbound struct {
	herdr  domain.HerdrGateway
	tg     domain.TelegramGateway
	topics *topicView
	agents agentLookup
	log    *slog.Logger
	// chatID and operators address the pager: the group for the message
	// link and the private chats that receive the ring.
	chatID    int64
	operators []int64
	// pager reads the posts.pager switch; pagerReachable is set by the
	// daemon after its start probe (and cleared here when every direct
	// send fails), so a closed private chat never loses the ring: the
	// topic post rings as it always did.
	pager          func() bool
	pagerReachable atomic.Bool

	capture *Capture
	clock   domain.Clock
	// paused reads the operator's sync switch: no screen posts while off.
	paused func() bool
	// quiet reads the presence tracker (operator at the desk); posts says
	// what quiet does to screen posts and reannounce whether leaving
	// re-posts still-blocked agents. live lists the agents for the catch-up.
	quiet      func() bool
	posts      func() domain.PostsMode
	reannounce func() bool
	live       func() []domain.Agent
	// replies finds the agent's last reply in its transcript for the done
	// post when doneMode asks for it; nil means the screen is always used.
	replies  domain.ReplySource
	doneMode func() domain.DoneMode

	deb        *debouncer
	lastPosted map[domain.Key]string // SHA-256 of the last screen posted per key
	// keyboards holds the latest button set per agent: which message
	// carries it and which options it offers. Only the latest keyboard
	// acts; older ones are retired when pressed.
	keyboards map[domain.Key]keyboard
	// announced marks agents whose current question was posted with a
	// sound; cleared when the agent leaves blocked. One sound per question.
	announced map[domain.Key]bool
	// turns tracks the open turn per agent: when it started and which
	// operator message, if any, carries the 👀 that owes a ✅. turnDeb
	// ends a turn once the agent has stayed idle for turnSettle; reactions
	// reads the posts.reactions switch.
	turns     map[domain.Key]turn
	turnDeb   *debouncer
	reactions func() bool
	// minTurn reads posts.min_seconds: a done post of a shorter turn is
	// skipped; zero posts every done screen.
	minTurn func() time.Duration
	// blockedDelay reads posts.blocked_delay: with a value the first
	// capture of a question is kept in captures and the post waits that
	// long for a second capture; zero posts the first capture.
	blockedDelay func() time.Duration
	captures     map[domain.Key]pendingCapture
	// refresh holds, per agent, the message whose multi-select keyboard
	// is redrawn from the screen on the next settle instead of a post.
	refresh map[domain.Key]int
	// typing holds the open ✏️ wait per agent.
	typing map[domain.Key]typingWait
}

// pendingCapture is the first screen of a question kept while the blocked
// delay runs; seq is the agent's StateChangeSeq at that time, so a newer
// question restarts the wait.
type pendingCapture struct {
	text string
	seq  int64
}

// keyboard is the inline keyboard under one blocked post. multi marks a
// multi-select dialog whose option buttons toggle and whose Submit row
// sends enter; textEntry is the number of the free-text entry behind the
// ✏️ button (0 without one); waiting marks the keyboard reduced to the
// "waiting for your text" button after a ✏️ press.
type keyboard struct {
	messageID int
	choices   []domain.Choice
	multi     bool
	textEntry int
	textLabel string
	// cursor and submitRow are the dialog rows last seen on screen (see
	// domain.Dialog); the Submit press re-reads the screen and falls back
	// to them when the read fails.
	cursor    int
	submitRow int
	waiting   bool
}

// typingWait is an open ✏️ wait: the next plain message in the topic is
// typed into the agent whatever it looks like, until the wait ends.
type typingWait struct {
	threadID  int
	messageID int
	until     time.Time
}

// Callback data of the dialog buttons beyond the option digits.
const (
	// submitData is the Submit row of a multi-select dialog: enter.
	submitData = "enter"
	// textEntryPrefix precedes the number of the free-text entry.
	textEntryPrefix = "t:"
	// doneData marks a button that already acted.
	doneData = "done"
)

// turn is one exchange with an agent: from the prompt (or the first
// working status seen) until done, or until idle has held for turnSettle.
// Blocked time is part of the turn.
type turn struct {
	threadID  int
	messageID int
	// started is the first working status of the turn; zero while the
	// agent has not started yet (a prompt to an idle agent) or when the
	// daemon never saw the start.
	started time.Time
	// reacted says 👀 is on the message and a ✅ is owed when the turn ends.
	reacted bool
	// ended marks a turn whose done status was seen but not yet fired.
	ended bool
}

// Reactions put on the operator's prompt. Both must be in Telegram's
// fixed set of allowed reactions: ✅ is not (setMessageReaction answers
// REACTION_INVALID, seen 2026-09-06), 👌 is.
const (
	reactionTaken = "👀"
	reactionDone  = "👌"
)

// newOutbound wires the screen poster. chatID and operators feed the
// pager: the group for the message link and the private chats that ring.
func newOutbound(herdr domain.HerdrGateway, tg domain.TelegramGateway, chatID int64, operators []int64, topics *topicView, agents agentLookup,
	live func() []domain.Agent, capture *Capture, opts *Options, replies domain.ReplySource, clock domain.Clock, log *slog.Logger) *outbound {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	paused := func() bool { return false }
	doneMode := func() domain.DoneMode { return domain.DoneScreen }
	reactions := func() bool { return true }
	minTurn := func() time.Duration { return 0 }
	blockedDelay := func() time.Duration { return 0 }
	pager := func() bool { return false }
	if opts != nil {
		paused = func() bool { return !opts.SyncEnabled() }
		doneMode = opts.PostsDone
		reactions = opts.PostsReactions
		minTurn = opts.MinTurn
		blockedDelay = opts.BlockedDelay
		pager = opts.PagerEnabled
	}
	if live == nil {
		live = func() []domain.Agent { return nil }
	}
	return &outbound{
		herdr:        herdr,
		tg:           tg,
		chatID:       chatID,
		operators:    append([]int64(nil), operators...),
		pager:        pager,
		topics:       topics,
		agents:       agents,
		live:         live,
		capture:      capture,
		clock:        clock,
		paused:       paused,
		quiet:        func() bool { return false },
		posts:        func() domain.PostsMode { return domain.PostsNormal },
		reannounce:   func() bool { return false },
		replies:      replies,
		doneMode:     doneMode,
		log:          log,
		deb:          newDebouncer(clock, screenSettle, log),
		lastPosted:   map[domain.Key]string{},
		keyboards:    map[domain.Key]keyboard{},
		announced:    map[domain.Key]bool{},
		turns:        map[domain.Key]turn{},
		turnDeb:      newDebouncer(clock, turnSettle, log),
		reactions:    reactions,
		minTurn:      minTurn,
		blockedDelay: blockedDelay,
		captures:     map[domain.Key]pendingCapture{},
		refresh:      map[domain.Key]int{},
		typing:       map[domain.Key]typingWait{},
	}
}

// TurnDue delivers keys whose idle timer fired; call EndTurn for each.
func (o *outbound) TurnDue() <-chan domain.Key { return o.turnDeb.Due() }

// SetPagerReachable records whether the bot may write to at least one
// operator's private chat; the daemon sets it after its start probe. Safe
// to call from any goroutine.
func (o *outbound) SetPagerReachable(ok bool) {
	o.pagerReachable.Store(ok)
	o.log.Debug("pager reachable set", slog.Bool("reachable", ok))
}

// PagerReachable reports the flag set by SetPagerReachable, cleared when
// every direct send of a question failed.
func (o *outbound) PagerReachable() bool { return o.pagerReachable.Load() }

// paging reports whether a ringing blocked post goes to the private chat
// instead: the option is on, the probe succeeded and there is someone to
// page.
func (o *outbound) paging() bool {
	return o.pager() && o.pagerReachable.Load() && len(o.operators) > 0
}

// PromptSent records that the operator's message was accepted by the agent
// as a prompt: a turn opens (started right away when the agent is already
// working, else on its first working status) and, with reactions on, the
// message gets 👀. An earlier open turn of the agent is replaced; its 👀
// stays as it is. Only fatal Telegram errors are returned.
func (o *outbound) PromptSent(ctx context.Context, key domain.Key, threadID, messageID int) error {
	t := turn{threadID: threadID, messageID: messageID}
	if agent, ok := o.agents(key); ok && agent.Status == domain.StatusWorking {
		t.started = o.clock.Now()
	}
	if prev, ok := o.turns[key]; ok {
		o.log.Debug("turn replaced", slog.String("key", key.String()), slog.Int("message_id", prev.messageID), slog.Bool("reacted", prev.reacted))
	}
	o.turnDeb.Cancel(key)
	if o.reactions() {
		if err := o.react(ctx, key, t, reactionTaken); err != nil {
			return err
		}
		t.reacted = true
	}
	o.turns[key] = t
	o.log.Debug("turn opened", slog.String("key", key.String()), slog.Int("message_id", messageID),
		slog.Bool("started", !t.started.IsZero()), slog.Bool("reacted", t.reacted))
	return nil
}

// observeTurn keeps the turn record in step with the agent status: working
// starts a turn (or the clock of one opened by a prompt) and cancels the
// idle timer, idle arms it, blocked and done cancel it; done marks the
// turn ended for fire. A gone agent loses its turn.
func (o *outbound) observeTurn(ev AgentEvent) {
	key := ev.Agent.Key
	t, open := o.turns[key]
	if ev.Kind == AgentGone {
		if open {
			delete(o.turns, key)
		}
		o.turnDeb.Cancel(key)
		return
	}
	switch ev.Agent.Status {
	case domain.StatusWorking:
		o.turnDeb.Cancel(key)
		if open && t.ended {
			o.log.Debug("turn dropped", slog.String("key", key.String()), slog.String("reason", "working_after_done"))
			t, open = turn{}, false
		}
		if !open {
			t = turn{started: o.clock.Now()}
			o.log.Debug("turn opened", slog.String("key", key.String()), slog.String("reason", "working"))
		} else if t.started.IsZero() {
			t.started = o.clock.Now()
		}
		o.turns[key] = t
	case domain.StatusIdle:
		if open {
			o.turnDeb.Schedule(key)
		}
	case domain.StatusDone:
		o.turnDeb.Cancel(key)
		if open {
			t.ended = true
			o.turns[key] = t
		}
	default:
		o.turnDeb.Cancel(key)
	}
}

// EndTurn runs when an agent has stayed idle for turnSettle: the open turn
// ends with its ✅ and is dropped. An agent that moved on meanwhile keeps
// its turn. Only fatal Telegram errors are returned.
func (o *outbound) EndTurn(ctx context.Context, key domain.Key) error {
	t, ok := o.turns[key]
	if !ok {
		o.log.Debug("turn end without turn", slog.String("key", key.String()))
		return nil
	}
	agent, alive := o.agents(key)
	if !alive || agent.Status != domain.StatusIdle {
		o.log.Debug("turn end skipped", slog.String("key", key.String()), slog.Bool("alive", alive), slog.String("status", string(agent.Status)))
		return nil
	}
	delete(o.turns, key)
	return o.finishTurn(ctx, key, t, "idle")
}

// finishTurn logs the end of a turn and pays the ✅ owed on its prompt.
func (o *outbound) finishTurn(ctx context.Context, key domain.Key, t turn, reason string) error {
	var duration int64 = -1
	if !t.started.IsZero() {
		duration = o.clock.Now().Sub(t.started).Milliseconds()
	}
	o.log.Debug("turn ended", slog.String("key", key.String()), slog.String("reason", reason),
		slog.Int("message_id", t.messageID), slog.Int64("duration_ms", duration))
	if !t.reacted || !o.reactions() {
		return nil
	}
	return o.react(ctx, key, t, reactionDone)
}

// react puts emoji on the turn's prompt; a failure is logged, only fatal
// Telegram errors are returned.
func (o *outbound) react(ctx context.Context, key domain.Key, t turn, emoji string) error {
	err := o.tg.React(ctx, t.threadID, t.messageID, emoji)
	switch {
	case err == nil:
		o.log.Debug("reaction sent", slog.String("key", key.String()), slog.Int("message_id", t.messageID), slog.String("emoji", emoji))
	case isFatal(err):
		o.log.Error("reaction failed with a fatal telegram error", slog.String("key", key.String()), slog.String("err", err.Error()))
		return err
	default:
		o.log.Warn("reaction failed", slog.String("key", key.String()), slog.Int("message_id", t.messageID),
			slog.String("emoji", emoji), slog.String("err", err.Error()))
	}
	return nil
}

// SetPresence wires quiet mode: quiet says whether the operator is at the
// desk, opts supplies the posts mode and the re-announce switch.
func (o *outbound) SetPresence(quiet func() bool, opts *Options) {
	if quiet == nil {
		quiet = func() bool { return false }
	}
	o.quiet = quiet
	if opts != nil {
		o.posts = opts.QuietPosts
		o.reannounce = opts.QuietReannounce
	}
}

// Due delivers keys whose settle timer fired; call Fire for each.
func (o *outbound) Due() <-chan domain.Key { return o.deb.Due() }

// Observe schedules a screen post when the event moves an agent into a
// status worth posting, and cancels a pending one otherwise. A blocked
// agent that is already known at startup (AgentAppeared) is posted too:
// the question was asked while nobody was watching. An exited agent is
// cleaned up by Forget, which the bridge calls with a context.
func (o *outbound) Observe(ev AgentEvent) {
	key := ev.Agent.Key
	o.observeTurn(ev)
	if ev.Agent.Status != domain.StatusBlocked || ev.Kind == AgentGone {
		delete(o.announced, key)
	}
	if ev.Agent.Status != domain.StatusBlocked || ev.Kind == AgentGone {
		if _, ok := o.captures[key]; ok {
			o.log.Debug("capture dropped", slog.String("key", key.String()), slog.String("status", string(ev.Agent.Status)))
			delete(o.captures, key)
		}
	}
	switch {
	case ev.Kind == AgentGone:
		o.deb.Cancel(key)
	case ev.Kind == AgentAppeared && ev.Agent.Status == domain.StatusBlocked,
		ev.Kind == AgentChanged && (ev.Agent.Status == domain.StatusBlocked || ev.Agent.Status == domain.StatusDone):
		// A repeated blocked event for the same question (Herdr's pane
		// updates are chatty) must not shorten a running blocked delay.
		if c, ok := o.captures[key]; ok && c.seq == ev.Agent.StateChangeSeq {
			o.log.Debug("capture pending, timer kept", slog.String("key", key.String()), slog.Int64("seq", c.seq))
			return
		}
		o.log.Debug("screen scheduled", slog.String("key", key.String()), slog.String("status", string(ev.Agent.Status)))
		o.deb.Schedule(key)
	default:
		// A toggle's redraw is pending: the keystroke itself flips the
		// pane to working for a moment, and the timer must survive it.
		if _, pending := o.refresh[key]; pending {
			o.log.Debug("refresh pending, timer kept", slog.String("key", key.String()), slog.String("status", string(ev.Agent.Status)))
			return
		}
		o.deb.Cancel(key)
	}
}

// Forget drops everything kept for an exited agent: the pending timer, the
// duplicate hash and the keyboard under its last question.
func (o *outbound) Forget(ctx context.Context, key domain.Key) error {
	o.deb.Cancel(key)
	o.turnDeb.Cancel(key)
	delete(o.lastPosted, key)
	delete(o.announced, key)
	delete(o.turns, key)
	delete(o.captures, key)
	delete(o.refresh, key)
	o.endTyping(key, "exited")
	return o.retire(ctx, key, "exited")
}

// Fire posts the screen for key if the agent is still blocked or done and
// the text differs from the last post. A blocked screen that ends in a
// numbered dialog gets one button per option. Read failures are logged and
// left to the next transition; only fatal Telegram errors are returned.
// While quiet mode is on the post follows the posts mode: held (not sent),
// silent (no sound) or normal.
func (o *outbound) Fire(ctx context.Context, key domain.Key) error {
	return o.fire(ctx, key, false)
}

// fire is Fire with a force flag for the catch-up: force bypasses the
// duplicate check and the quiet rules and always rings.
func (o *outbound) fire(ctx context.Context, key domain.Key, force bool) error {
	agent, ok := o.agents(key)
	if !ok {
		return o.skip(key, "exited")
	}
	// A done status ends the turn here, before any reason to skip the
	// post: the ✅ is owed even when the post is muted, held or short.
	var t turn
	var hasTurn bool
	if agent.Status == domain.StatusDone {
		if t, hasTurn = o.turns[key]; hasTurn {
			delete(o.turns, key)
			o.turnDeb.Cancel(key)
			if err := o.finishTurn(ctx, key, t, "done"); err != nil {
				return err
			}
		}
	}
	if o.paused() {
		return o.skip(key, "sync_off")
	}
	// A toggled multi-select keyboard is redrawn in place from the screen
	// whatever the status says: the keystroke flips the pane to working
	// for a few seconds while the dialog stays (seen 2026-09-06). When the
	// dialog moved on the ordinary post below takes over.
	if msgID, ok := o.refresh[key]; ok && !force {
		delete(o.refresh, key)
		done, err := o.refreshKeyboard(ctx, key, msgID, blockedLines)
		if done || err != nil {
			return err
		}
	}
	var lines int
	switch agent.Status {
	case domain.StatusBlocked:
		lines = blockedLines
	case domain.StatusDone:
		lines = doneLines
	default:
		delete(o.captures, key)
		return o.skip(key, "not_blocked")
	}
	// A done post of a turn shorter than posts.min_seconds is skipped; a
	// turn whose start the daemon never saw posts. The catch-up never
	// reaches here with done, so force needs no exception.
	if minTurn := o.minTurn(); agent.Status == domain.StatusDone && minTurn > 0 && hasTurn && !t.started.IsZero() {
		if elapsed := o.clock.Now().Sub(t.started); elapsed < minTurn {
			o.log.Debug("screen skipped", slog.String("key", key.String()), slog.String("reason", "short_turn"),
				slog.Int64("duration_ms", elapsed.Milliseconds()), slog.Int64("min_ms", minTurn.Milliseconds()))
			return nil
		}
	}
	entry, ok := o.topics.Entry(key)
	switch {
	case !ok:
		return o.skip(key, "no_topic")
	case !entry.Status.Live():
		return o.skip(key, "exited")
	case entry.Muted:
		return o.skip(key, "muted")
	}
	notify := agent.Status == domain.StatusBlocked
	if force {
		notify = true
	} else if o.quiet() {
		switch o.posts() {
		case domain.PostsHeld:
			return o.skip(key, "quiet_held")
		case domain.PostsSilent:
			notify = false
		}
	}
	// A done post may come from the agent's transcript instead of the
	// screen; any failure there falls back to the screen with one info line.
	mode := domain.DoneScreen
	if agent.Status == domain.StatusDone {
		mode = o.doneMode()
	}
	var text string
	var reply domain.Reply
	// With a blocked delay the first capture waits for a second one; the
	// catch-up never waits and drops whatever was kept.
	captured := false
	if agent.Status == domain.StatusBlocked {
		switch delay := o.blockedDelay(); {
		case force:
			delete(o.captures, key)
		case delay > 0:
			chosen, ready := o.delayedCapture(ctx, key, agent, lines, delay)
			if !ready {
				return nil
			}
			text, captured = chosen, true
		}
	}
	if mode != domain.DoneScreen && o.replies == nil {
		mode = domain.DoneScreen
	}
	if !captured && mode != domain.DoneScreen {
		r, err := o.replies.LastReply(ctx, agent)
		if err != nil {
			o.log.Info("reply source unavailable", slog.String("key", key.String()), slog.String("mode", string(mode)), slog.Any("err", err))
			mode = domain.DoneScreen
		} else {
			reply, text = r, strings.TrimSpace(r.Text)
		}
	}
	if mode == domain.DoneScreen && !captured {
		screen, err := o.herdr.ReadScreen(ctx, key.PaneID, domain.ScreenDetection, lines)
		if err != nil {
			o.log.Warn("screen read failed", slog.String("key", key.String()), slog.String("err", err.Error()))
			return nil
		}
		text = trimScreen(screen.Text)
	}
	if text == "" {
		return o.skip(key, "empty")
	}
	hash := hashText(text)
	if o.lastPosted[key] == hash && !force {
		return o.skip(key, "duplicate")
	}
	if err := o.retire(ctx, key, "superseded"); err != nil {
		return err
	}
	out := domain.Outgoing{ThreadID: entry.ThreadID, Text: text, Code: mode != domain.DoneFormatted, Markdown: mode == domain.DoneFormatted, Notify: notify}
	if mode != domain.DoneScreen {
		out.MaxParts = replyMaxParts
	}
	var dialog domain.Dialog
	if agent.Status == domain.StatusBlocked {
		dialog = domain.ParseDialog(text)
		out.Buttons = choiceButtons(dialog)
		o.logChoices(key, dialog)
	}
	// With the pager the topic post stays silent and the ring comes from
	// the bot's private chat, so a muted group still rings exactly once.
	paged := notify && agent.Status == domain.StatusBlocked && o.paging()
	if paged {
		out.Notify = false
	}
	id, err := o.tg.Send(ctx, out)
	if err != nil {
		return o.sendFailed(key, err)
	}
	o.lastPosted[key] = hash
	if len(out.Buttons) > 0 {
		o.keyboards[key] = keyboard{messageID: id, choices: dialog.Choices, multi: dialog.Multi, textEntry: dialog.TextEntry, textLabel: dialog.TextLabel,
			cursor: dialog.Cursor, submitRow: dialog.SubmitRow}
	}
	switch {
	case paged:
		rang, err := o.page(ctx, key, agent, entry.ThreadID, id, text, dialog)
		if err != nil {
			return err
		}
		if rang {
			o.announced[key] = true
		}
	case notify && agent.Status == domain.StatusBlocked:
		o.announced[key] = true
	}
	if mode != domain.DoneScreen {
		o.log.Info("reply posted", slog.String("key", key.String()), slog.Int("thread_id", entry.ThreadID),
			slog.String("mode", string(mode)), slog.Int("lines", strings.Count(text, "\n")+1), slog.Int("bytes", len(text)),
			slog.String("source", reply.Source), slog.Int64("age_ms", reply.Age.Milliseconds()),
			slog.Int("message_id", id), slog.Bool("notify", notify), slog.Bool("forced", force))
		return nil
	}
	o.log.Info("screen posted", slog.String("key", key.String()), slog.Int("thread_id", entry.ThreadID),
		slog.String("status", string(agent.Status)), slog.Int("lines", strings.Count(text, "\n")+1), slog.Int("bytes", len(text)),
		slog.Int("buttons", len(out.Buttons)), slog.Int("message_id", id), slog.Bool("notify", out.Notify), slog.Bool("paged", paged), slog.Bool("forced", force))
	return nil
}

// page sends the question to every operator's private chat with a sound:
// the agent label, the dialog's options or the last pagerLines lines of
// the screen, and a link to the post in the topic. It reports whether at
// least one operator was reached. When every chat is closed (ErrForbidden:
// the operator blocked the bot or never pressed Start) the pager is
// switched off for the rest of the run, General is told once, and the
// next question rings in the topic; a transient failure (network, 5xx,
// a cancelled context) loses this ring but keeps the pager on for the
// next question. A closed private chat is not the group's fault and is
// never fatal here; only a dead bot token or a poller conflict is.
func (o *outbound) page(ctx context.Context, key domain.Key, agent domain.Agent, threadID, messageID int, text string, dialog domain.Dialog) (bool, error) {
	out := domain.Outgoing{Text: pagerText(agent.Label(), text, dialog, messageLink(o.chatID, threadID, messageID)), HTML: true, Notify: true}
	var reached []int64
	var last error
	closed := 0
	for _, op := range o.operators {
		id, err := o.tg.SendDirect(ctx, op, out)
		switch {
		case err == nil:
			reached = append(reached, op)
			o.log.Debug("pager delivered", slog.String("key", key.String()), slog.Int64("operator", op), slog.Int("direct_id", id))
		case errors.Is(err, domain.ErrBotUnauthorized), errors.Is(err, domain.ErrPollerConflict):
			o.log.Error("pager failed with a fatal telegram error", slog.String("key", key.String()), slog.Int64("operator", op), slog.String("err", err.Error()))
			return len(reached) > 0, err
		case errors.Is(err, domain.ErrForbidden):
			closed++
			last = err
			o.log.Info("operator chat closed", slog.String("key", key.String()), slog.Int64("operator", op), slog.String("err", err.Error()))
		default:
			last = err
			o.log.Debug("pager send failed", slog.String("key", key.String()), slog.Int64("operator", op), slog.String("err", err.Error()))
		}
	}
	if len(reached) == 0 {
		if closed < len(o.operators) {
			o.log.Warn("pager failed, this question rings nowhere", slog.String("key", key.String()),
				slog.Int("operators", len(o.operators)), slog.Int("closed", closed), slog.String("err", errString(last)))
			return false, nil
		}
		o.pagerReachable.Store(false)
		o.log.Warn("pager failed, ringing in the topic next time", slog.String("key", key.String()),
			slog.Int("operators", len(o.operators)), slog.String("err", errString(last)))
		if _, err := o.tg.Send(ctx, domain.Outgoing{ThreadID: 0, Text: pagerNotice}); err != nil {
			o.log.Warn("pager notice not posted", slog.String("err", err.Error()))
		}
		return false, nil
	}
	o.log.Info("pager sent", slog.String("key", key.String()), slog.Any("operators", reached), slog.Int("failed", len(o.operators)-len(reached)),
		slog.Int("message_id", messageID), slog.Int("thread_id", threadID))
	return true, nil
}

// pagerText renders the private-chat message: "❓ <label> is waiting for
// you", then the numbered options of a dialog or the last pagerLines
// lines of the screen as a code block, then the link to the topic post.
// Everything from the screen is HTML-escaped.
func pagerText(label, screen string, dialog domain.Dialog, link string) string {
	var b strings.Builder
	b.WriteString("❓ <b>" + html.EscapeString(label) + "</b> is waiting for you\n")
	if len(dialog.Choices) > 0 {
		for _, c := range dialog.Choices {
			b.WriteString(strconv.Itoa(c.Number) + ". " + html.EscapeString(c.Label) + "\n")
		}
	} else if tail := lastLines(screen, pagerLines); tail != "" {
		b.WriteString("<pre>" + html.EscapeString(tail) + "</pre>\n")
	}
	b.WriteString(`<a href="` + link + `">open the topic</a>`)
	return b.String()
}

// lastLines returns the last n lines of text, trimmed of blank edges.
func lastLines(text string, n int) string {
	lines := screenLines(strings.TrimSpace(text))
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return trimScreen(strings.Join(lines, "\n"))
}

// errString words a possibly nil error for a log field.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// delayedCapture implements the blocked delay: the first fire keeps the
// screen and re-arms the timer for delay; the next fire reads the screen
// again and returns the better of the two (more options recognised by
// ParseChoices, then the longer text). A question that changed meanwhile
// (StateChangeSeq moved) starts over. ready is false while the post has
// to wait, or when the read failed and the next transition retries.
func (o *outbound) delayedCapture(ctx context.Context, key domain.Key, agent domain.Agent, lines int, delay time.Duration) (string, bool) {
	pending, had := o.captures[key]
	restart := had && pending.seq != agent.StateChangeSeq
	screen, err := o.herdr.ReadScreen(ctx, key.PaneID, domain.ScreenDetection, lines)
	if err != nil {
		o.log.Warn("screen read failed", slog.String("key", key.String()), slog.String("err", err.Error()))
		return "", false
	}
	text := trimScreen(screen.Text)
	if !had || restart {
		o.captures[key] = pendingCapture{text: text, seq: agent.StateChangeSeq}
		o.deb.ScheduleAfter(key, delay)
		o.log.Debug("capture kept", slog.String("key", key.String()), slog.Int64("seq", agent.StateChangeSeq),
			slog.Int("choices", len(domain.ParseChoices(text))), slog.Int("bytes", len(text)), slog.Int64("delay_ms", delay.Milliseconds()))
		reason := "delayed"
		if restart {
			reason = "delayed_restart"
		}
		_ = o.skip(key, reason)
		return "", false
	}
	delete(o.captures, key)
	chosen, secondWon := betterCapture(pending.text, text)
	o.log.Debug("capture compared", slog.String("key", key.String()), slog.Int64("seq", agent.StateChangeSeq),
		slog.Int("first_choices", len(domain.ParseChoices(pending.text))), slog.Int("first_bytes", len(pending.text)),
		slog.Int("second_choices", len(domain.ParseChoices(text))), slog.Int("second_bytes", len(text)),
		slog.String("won", map[bool]string{false: "first", true: "second"}[secondWon]))
	return chosen, true
}

// betterCapture picks the capture with more recognised options, then the
// longer one; on a full tie the first is kept.
func betterCapture(first, second string) (string, bool) {
	a, b := len(domain.ParseChoices(first)), len(domain.ParseChoices(second))
	switch {
	case b > a:
		return second, true
	case a > b:
		return first, false
	case len(second) > len(first):
		return second, true
	}
	return first, false
}

// CatchUp runs when quiet mode ends: every agent still blocked whose
// question never rang is posted again with a sound (forced past the
// duplicate check); an agent that already has a post is skipped when
// re-announcing is off. Non-fatal errors are logged and the loop goes on.
func (o *outbound) CatchUp(ctx context.Context) error {
	var blocked, posted, skipped int
	for _, a := range o.live() {
		if a.Status != domain.StatusBlocked {
			continue
		}
		blocked++
		key := a.Key
		switch {
		case o.announced[key]:
			skipped++
			_ = o.skip(key, "announced")
			continue
		case o.lastPosted[key] != "" && !o.reannounce():
			skipped++
			_ = o.skip(key, "reannounce_off")
			continue
		}
		before := len(o.lastPosted)
		if err := o.fire(ctx, key, true); err != nil {
			return err
		}
		if o.announced[key] || len(o.lastPosted) > before {
			posted++
		} else {
			skipped++
		}
	}
	o.log.Info("catch-up done", slog.Int("blocked", blocked), slog.Int("posted", posted), slog.Int("skipped", skipped))
	return nil
}

func (o *outbound) logChoices(key domain.Key, d domain.Dialog) {
	if len(d.Choices) == 0 {
		o.log.Debug("choices rejected", slog.String("key", key.String()))
		return
	}
	numbers := make([]int, 0, len(d.Choices))
	for _, c := range d.Choices {
		numbers = append(numbers, c.Number)
	}
	o.log.Debug("choices parsed", slog.String("key", key.String()), slog.Int("count", len(d.Choices)), slog.Any("numbers", numbers),
		slog.Bool("multi", d.Multi), slog.Int("text_entry", d.TextEntry), slog.Int("cursor", d.Cursor), slog.Int("submit_row", d.SubmitRow))
}

// refreshKeyboard redraws the post msgID from the current screen after a
// toggle press: the text shows the new check marks and the keyboard is
// rebuilt from it (measured 2026-09-06: the operator wants the "[✔]" in
// the screen as well as on the button). It reports true when the message
// was redrawn (or nothing else should happen) and false when the dialog
// is gone, so the caller posts the new screen as usual.
func (o *outbound) refreshKeyboard(ctx context.Context, key domain.Key, msgID, lines int) (bool, error) {
	kb, ok := o.keyboards[key]
	if !ok || kb.messageID != msgID {
		o.log.Debug("keyboard refresh dropped", slog.String("key", key.String()), slog.Int("message_id", msgID), slog.String("reason", "not_latest"))
		return false, nil
	}
	screen, err := o.herdr.ReadScreen(ctx, key.PaneID, domain.ScreenDetection, lines)
	if err != nil {
		o.log.Warn("screen read failed", slog.String("key", key.String()), slog.String("err", err.Error()))
		return true, nil
	}
	text := trimScreen(screen.Text)
	d := domain.ParseDialog(text)
	if !d.Multi {
		o.log.Debug("keyboard refresh dropped", slog.String("key", key.String()), slog.Int("message_id", msgID), slog.String("reason", "not_multi"))
		return false, nil
	}
	kb.choices, kb.textEntry, kb.textLabel = d.Choices, d.TextEntry, d.TextLabel
	kb.cursor, kb.submitRow = d.Cursor, d.SubmitRow
	o.keyboards[key] = kb
	o.lastPosted[key] = hashText(text)
	o.log.Debug("keyboard refreshed", slog.String("key", key.String()), slog.Int("message_id", msgID),
		slog.Int("choices", len(d.Choices)), slog.Int("bytes", len(text)))
	return true, o.absorbEdit(key, o.tg.EditText(ctx, msgID, preHTML(text), true, choiceButtons(d)))
}

// submitKeys returns the keys that submit the multi-select dialog under
// kb: the screen is read once more for the cursor row, since the operator
// may have moved it at the keyboard; a failed read falls back to the rows
// seen at posting time.
func (o *outbound) submitKeys(ctx context.Context, key domain.Key, kb keyboard) []string {
	d := domain.Dialog{Cursor: kb.cursor, SubmitRow: kb.submitRow}
	screen, err := o.herdr.ReadScreen(ctx, key.PaneID, domain.ScreenDetection, blockedLines)
	if err != nil {
		o.log.Warn("screen read failed", slog.String("key", key.String()), slog.String("err", err.Error()))
	} else if live := domain.ParseDialog(trimScreen(screen.Text)); live.Multi {
		d = live
	}
	keys := d.SubmitKeys()
	o.log.Debug("submit keys", slog.String("key", key.String()), slog.Int("cursor", d.Cursor), slog.Int("submit_row", d.SubmitRow), slog.Any("keys", keys))
	return keys
}

// dialogStillOpen reports whether a pane Herdr calls working still shows
// the keyboard's dialog. A keystroke flips the status to working for a
// few seconds while the question stays on screen (seen 2026-09-06 after
// a toggle), and a press in that window must not retire the keyboard.
// Any other status trusts Herdr.
func (o *outbound) dialogStillOpen(ctx context.Context, key domain.Key, kb keyboard, st domain.Status) bool {
	if st != domain.StatusWorking {
		return false
	}
	screen, err := o.herdr.ReadScreen(ctx, key.PaneID, domain.ScreenDetection, blockedLines)
	if err != nil {
		o.log.Warn("screen read failed", slog.String("key", key.String()), slog.String("err", err.Error()))
		return false
	}
	d := domain.ParseDialog(trimScreen(screen.Text))
	open := len(d.Choices) > 0 && len(d.Choices) == len(kb.choices) && d.Multi == kb.multi && d.TextEntry == kb.textEntry
	o.log.Debug("dialog checked while working", slog.String("key", key.String()), slog.Bool("open", open),
		slog.Int("choices", len(d.Choices)), slog.Bool("multi", d.Multi))
	return open
}

// preHTML wraps a screen in the same <pre> block a code post uses, so an
// edited post keeps the look of the original.
func preHTML(text string) string {
	return "<pre>" + html.EscapeString(text) + "</pre>"
}

// choiceButtons renders a dialog as buttons: one per option (the keycap
// digit, a space and the label; data is the digit the agent expects), a
// "✔ Submit" row for a multi-select dialog (data enter) and a "✏️" row for
// the free-text entry (data t:<n>).
func choiceButtons(d domain.Dialog) []domain.Button {
	if len(d.Choices) == 0 {
		return nil
	}
	buttons := make([]domain.Button, 0, len(d.Choices)+2)
	for _, c := range d.Choices {
		buttons = append(buttons, domain.Button{Text: keycap(c.Number) + " " + cutLabel(c.Label), Data: strconv.Itoa(c.Number)})
	}
	if d.Multi {
		buttons = append(buttons, domain.Button{Text: "✔ Submit", Data: submitData})
	}
	if d.TextEntry > 0 {
		buttons = append(buttons, domain.Button{Text: "✏️ " + d.TextLabel, Data: textEntryPrefix + strconv.Itoa(d.TextEntry)})
	}
	return buttons
}

// keycap returns the emoji keycap for a digit 1..9.
func keycap(n int) string {
	return strconv.Itoa(n) + "\uFE0F\u20E3"
}

// cutLabel shortens an option label to choiceLabelRunes runes.
func cutLabel(label string) string {
	if utf8.RuneCountInString(label) <= choiceLabelRunes {
		return label
	}
	runes := []rune(label)
	return strings.TrimSpace(string(runes[:choiceLabelRunes-1])) + "…"
}

// retire removes the keyboard kept for key, if any. A failed edit is
// cosmetic (the buttons answer "not the latest question" when pressed), so
// only fatal Telegram errors are returned.
func (o *outbound) retire(ctx context.Context, key domain.Key, reason string) error {
	kb, ok := o.keyboards[key]
	if !ok {
		return nil
	}
	delete(o.keyboards, key)
	delete(o.refresh, key)
	if kb.waiting {
		o.endTyping(key, reason)
	}
	o.log.Debug("buttons retired", slog.String("key", key.String()), slog.Int("message_id", kb.messageID), slog.String("reason", reason))
	return o.absorbEdit(key, o.tg.EditButtons(ctx, kb.messageID, nil))
}

// endTyping drops the open ✏️ wait of key, if any, with a log line.
func (o *outbound) endTyping(key domain.Key, reason string) {
	if w, ok := o.typing[key]; ok {
		delete(o.typing, key)
		o.log.Debug("typing wait ended", slog.String("key", key.String()), slog.Int("message_id", w.messageID), slog.String("reason", reason))
	}
}

// TakeTyping returns and clears the open ✏️ wait of key when its next
// message should be typed into the agent. An expired wait is dropped and
// its keyboard marked, and false is returned.
func (o *outbound) TakeTyping(ctx context.Context, key domain.Key) (typingWait, bool) {
	w, ok := o.typing[key]
	if !ok {
		return typingWait{}, false
	}
	if o.clock.Now().After(w.until) {
		o.endTyping(key, "timeout")
		if err := o.markTyping(ctx, key, w, "✏️ expired"); err != nil {
			o.log.Warn("typing keyboard edit failed", slog.String("key", key.String()), slog.String("err", err.Error()))
		}
		return typingWait{}, false
	}
	delete(o.typing, key)
	return w, true
}

// TypingDone marks the ✏️ keyboard after the text was typed into the
// agent: "✅ ✏️ · <head of the text>". Only fatal Telegram errors are
// returned.
func (o *outbound) TypingDone(ctx context.Context, key domain.Key, w typingWait, text string) error {
	o.log.Debug("typing wait ended", slog.String("key", key.String()), slog.Int("message_id", w.messageID), slog.String("reason", "typed"))
	return o.markTyping(ctx, key, w, "✅ ✏️ · "+headOf(text, typingHeadRunes))
}

// CancelTyping ends the open ✏️ wait of key because a command arrived
// instead of the text; the keyboard says so. Only fatal Telegram errors
// are returned.
func (o *outbound) CancelTyping(ctx context.Context, key domain.Key) error {
	w, ok := o.typing[key]
	if !ok {
		return nil
	}
	o.endTyping(key, "command")
	return o.markTyping(ctx, key, w, "✏️ cancelled")
}

// markTyping replaces the waiting keyboard of w with one inert button and
// forgets the keyboard.
func (o *outbound) markTyping(ctx context.Context, key domain.Key, w typingWait, text string) error {
	if kb, ok := o.keyboards[key]; ok && kb.messageID == w.messageID {
		delete(o.keyboards, key)
	}
	return o.absorbEdit(key, o.tg.EditButtons(ctx, w.messageID, []domain.Button{{Text: text, Data: doneData}}))
}

// headOf returns the first n runes of text on one line, with an ellipsis
// when cut.
func headOf(text string, n int) string {
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) <= n {
		return text
	}
	return strings.TrimSpace(string([]rune(text)[:n-1])) + "…"
}

// Press handles a button under one of the blocked posts. An option digit
// goes to the agent as a key press and, for a single-select dialog, the
// keyboard turns into a single ✅ button and the screen timer is armed so
// a follow-up question is posted; for a multi-select dialog the digit
// toggles the option and the keyboard is redrawn from the screen on the
// next settle. "enter" (the Submit row) submits a multi-select dialog.
// "t:<n>" (the ✏️ row) selects the free-text entry and opens a wait for
// the operator's next message. Every path answers the callback so the
// phone stops its spinner. Only fatal Telegram errors are returned.
func (o *outbound) Press(ctx context.Context, ev domain.ButtonPressed) error {
	key, ok := o.topics.KeyForThread(ev.ThreadID)
	if !ok {
		o.log.Debug("button for unknown thread", slog.Int("thread_id", ev.ThreadID), slog.Int("message_id", ev.MessageID))
		return o.stale(ctx, ev, "topic is not mapped")
	}
	if ev.Data == doneData {
		o.log.Debug("button already answered", slog.String("key", key.String()), slog.Int("message_id", ev.MessageID))
		return o.answer(ctx, ev.CallbackID, "already answered")
	}
	kind, n := pressKindOf(ev.Data)
	if kind == pressUnknown {
		o.log.Warn("button data unknown", slog.String("key", key.String()), slog.Int("message_id", ev.MessageID), slog.String("data", ev.Data))
		return o.stale(ctx, ev, "unknown button")
	}
	kb, ok := o.keyboards[key]
	if !ok || kb.messageID != ev.MessageID {
		o.log.Debug("button stale", slog.String("key", key.String()), slog.Int("message_id", ev.MessageID),
			slog.Int("latest_id", kb.messageID), slog.String("reason", "not_latest"))
		return o.stale(ctx, ev, "not the latest question")
	}
	if (kind == pressSubmit && !kb.multi) || (kind == pressText && (kb.textEntry == 0 || n != kb.textEntry)) || kb.waiting {
		o.log.Warn("button not offered", slog.String("key", key.String()), slog.Int("message_id", ev.MessageID), slog.String("data", ev.Data))
		return o.stale(ctx, ev, "unknown button")
	}
	agent, alive := o.agents(key)
	o.log.Info("button pressed", slog.String("key", key.String()), slog.Int("thread_id", ev.ThreadID), slog.Int("message_id", ev.MessageID),
		slog.Int64("from_id", ev.FromID), slog.String("data", ev.Data), slog.String("status", string(agent.Status)), slog.Bool("alive", alive),
		slog.Bool("multi", kb.multi), slog.Int("text_entry", kb.textEntry))
	switch {
	case !alive:
		if err := o.retire(ctx, key, "exited"); err != nil {
			return err
		}
		return o.answer(ctx, ev.CallbackID, "agent has exited")
	case agent.Status != domain.StatusBlocked && !o.dialogStillOpen(ctx, key, kb, agent.Status):
		if err := o.retire(ctx, key, "not_blocked"); err != nil {
			return err
		}
		return o.answer(ctx, ev.CallbackID, "agent is not waiting anymore")
	}
	keys := []string{ev.Data}
	switch kind {
	case pressText:
		keys = []string{strconv.Itoa(n)}
	case pressSubmit:
		keys = o.submitKeys(ctx, key, kb)
	}
	if err := o.herdr.SendKeys(ctx, key.PaneID, keys); err != nil {
		o.log.Warn("button send_keys failed", slog.String("key", key.String()), slog.String("data", ev.Data), slog.String("err", err.Error()))
		if err := o.retire(ctx, key, "failed"); err != nil {
			return err
		}
		return o.answer(ctx, ev.CallbackID, "⚠️ "+failureReason(err))
	}
	switch kind {
	case pressText:
		return o.pressedText(ctx, key, kb, ev)
	case pressSubmit:
		delete(o.keyboards, key)
		delete(o.refresh, key)
		if err := o.absorbEdit(key, o.tg.EditButtons(ctx, ev.MessageID, []domain.Button{{Text: "✅ submitted", Data: doneData}})); err != nil {
			return err
		}
		o.log.Info("dialog submitted", slog.String("key", key.String()), slog.Int("message_id", ev.MessageID))
		if err := o.answer(ctx, ev.CallbackID, "submitted"); err != nil {
			return err
		}
	case pressDigit:
		if kb.multi {
			o.refresh[key] = ev.MessageID
			o.log.Info("option toggled", slog.String("key", key.String()), slog.String("data", ev.Data), slog.Int("message_id", ev.MessageID))
			if err := o.answer(ctx, ev.CallbackID, "toggled: "+ev.Data); err != nil {
				return err
			}
			o.deb.Schedule(key)
			return nil
		}
		delete(o.keyboards, key)
		label := ev.Data
		for _, c := range kb.choices {
			if c.Number == n {
				label = cutLabel(c.Label)
			}
		}
		pressed := []domain.Button{{Text: "✅ " + ev.Data + " · " + label, Data: doneData}}
		if err := o.absorbEdit(key, o.tg.EditButtons(ctx, ev.MessageID, pressed)); err != nil {
			return err
		}
		o.log.Info("button sent", slog.String("key", key.String()), slog.String("data", ev.Data), slog.Int("message_id", ev.MessageID))
		if err := o.answer(ctx, ev.CallbackID, "sent: "+ev.Data); err != nil {
			return err
		}
	}
	// The follow-up question starts from a fresh first capture.
	delete(o.captures, key)
	o.deb.Schedule(key)
	return nil
}

// pressedText finishes a ✏️ press once the entry's digit reached the
// agent: the keyboard waits, the toast and a force-reply prompt tell the
// operator to send the text, and the wait is recorded. No screen read is
// armed: the text box the agent shows must not be posted as a question.
func (o *outbound) pressedText(ctx context.Context, key domain.Key, kb keyboard, ev domain.ButtonPressed) error {
	kb.waiting = true
	o.keyboards[key] = kb
	w := typingWait{threadID: ev.ThreadID, messageID: ev.MessageID, until: o.clock.Now().Add(typingTimeout)}
	o.typing[key] = w
	o.log.Info("typing wait opened", slog.String("key", key.String()), slog.Int("message_id", ev.MessageID), slog.Int("entry", kb.textEntry),
		slog.Int64("timeout_ms", typingTimeout.Milliseconds()))
	if err := o.absorbEdit(key, o.tg.EditButtons(ctx, ev.MessageID, []domain.Button{{Text: "✏️ waiting for your text", Data: doneData}})); err != nil {
		return err
	}
	if err := o.answer(ctx, ev.CallbackID, "now send the text"); err != nil {
		return err
	}
	_, err := o.tg.Send(ctx, domain.Outgoing{ThreadID: ev.ThreadID, ReplyTo: ev.MessageID,
		Text: "✏️ " + kb.textLabel + ": send the text as your next message", ForceReply: true})
	return o.absorbEdit(key, err)
}

// pressKind classifies callback data of a dialog keyboard.
type pressKind int

const (
	pressUnknown pressKind = iota
	pressDigit
	pressSubmit
	pressText
)

// pressKindOf returns the kind of data and, for a digit or a text entry,
// its number.
func pressKindOf(data string) (pressKind, int) {
	if data == submitData {
		return pressSubmit, 0
	}
	kind := pressDigit
	if strings.HasPrefix(data, textEntryPrefix) {
		kind = pressText
		data = strings.TrimPrefix(data, textEntryPrefix)
	}
	n, err := strconv.Atoi(data)
	if err != nil || n < 1 || n > 9 {
		return pressUnknown, 0
	}
	return kind, n
}

// stale answers a press that cannot act and strips the buttons from the
// message it came from, whatever its age.
func (o *outbound) stale(ctx context.Context, ev domain.ButtonPressed, text string) error {
	if err := o.absorbEdit(domain.Key{}, o.tg.EditButtons(ctx, ev.MessageID, nil)); err != nil {
		return err
	}
	return o.answer(ctx, ev.CallbackID, text)
}

// answer closes the callback with a toast; only fatal errors are returned.
func (o *outbound) answer(ctx context.Context, callbackID, text string) error {
	err := o.tg.AnswerButton(ctx, callbackID, text)
	switch {
	case err == nil:
		return nil
	case isFatal(err):
		o.log.Error("button answer failed with a fatal telegram error", slog.String("err", err.Error()))
		return err
	default:
		o.log.Warn("button answer failed", slog.String("err", err.Error()))
		return nil
	}
}

// absorbEdit applies the error policy for keyboard edits: fatal errors end
// the daemon, anything else is logged, because a leftover or missing
// keyboard is cosmetic.
func (o *outbound) absorbEdit(key domain.Key, err error) error {
	switch {
	case err == nil:
		return nil
	case isFatal(err):
		o.log.Error("buttons edit failed with a fatal telegram error", slog.String("key", key.String()), slog.String("err", err.Error()))
		return err
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		o.log.Debug("buttons edit cancelled", slog.String("key", key.String()))
	default:
		o.log.Warn("buttons edit failed", slog.String("key", key.String()), slog.String("err", err.Error()))
	}
	return nil
}

// Screen posts the visible screen of an agent on request: the whole screen
// when lines is 0, else its last lines. Unlike Fire it ignores the mute
// flag and the duplicate check because the operator asked for it. Errors
// are returned so the caller can tell the operator.
func (o *outbound) Screen(ctx context.Context, key domain.Key, lines int) error {
	entry, ok := o.topics.Entry(key)
	if !ok {
		return fmt.Errorf("screen for %s: no topic", key)
	}
	screen, err := o.herdr.ReadScreen(ctx, key.PaneID, domain.ScreenVisible, lines)
	if err != nil {
		return err
	}
	text := trimScreen(screen.Text)
	if text == "" {
		text = "(screen is empty)"
	}
	if _, err := o.tg.Send(ctx, domain.Outgoing{ThreadID: entry.ThreadID, Text: text, Code: true}); err != nil {
		return err
	}
	o.log.Info("screen posted", slog.String("key", key.String()), slog.Int("thread_id", entry.ThreadID),
		slog.String("status", "requested"), slog.Int("lines", strings.Count(text, "\n")+1), slog.Int("bytes", len(text)))
	return nil
}

// ScreenAll posts what the agent printed since the last human message: the
// captured history after its mark plus the current screen. Short output is
// sent as code messages like Screen; longer output goes out as one .txt
// document so a long exchange does not flood the topic and the queue.
// Like Screen it ignores the mute flag. Errors are returned so the caller
// can tell the operator.
func (o *outbound) ScreenAll(ctx context.Context, key domain.Key) error {
	entry, ok := o.topics.Entry(key)
	if !ok {
		return fmt.Errorf("screen all for %s: no topic", key)
	}
	lines, marked, err := o.capture.Since(ctx, key)
	if err != nil {
		return err
	}
	text := trimScreen(strings.Join(lines, "\n"))
	if text == "" {
		o.log.Debug("screen all empty", slog.String("key", key.String()), slog.Bool("marked", marked))
		_, err := o.tg.Send(ctx, domain.Outgoing{ThreadID: entry.ThreadID, Text: "(no output since your last message)"})
		return err
	}
	since := "your last message"
	if !marked {
		since = "daemon start"
		text = "(history starts at daemon start)\n" + text
	}
	n := strings.Count(text, "\n") + 1
	asDocument := utf8.RuneCountInString(text) > screenAllInlineRunes
	if asDocument {
		doc := domain.Document{
			ThreadID: entry.ThreadID,
			Name:     fmt.Sprintf("screen-%s-%s.txt", strings.ReplaceAll(key.PaneID, ":", "-"), o.clock.Now().Format("150405")),
			Data:     []byte(text + "\n"),
			Caption:  fmt.Sprintf("%d lines since %s", n, since),
		}
		err = o.tg.SendDocument(ctx, doc)
	} else {
		_, err = o.tg.Send(ctx, domain.Outgoing{ThreadID: entry.ThreadID, Text: text, Code: true})
	}
	if err != nil {
		return err
	}
	o.log.Info("screen posted", slog.String("key", key.String()), slog.Int("thread_id", entry.ThreadID),
		slog.String("status", "history"), slog.Int("lines", n), slog.Int("bytes", len(text)),
		slog.Bool("document", asDocument), slog.Bool("marked", marked))
	return nil
}

func (o *outbound) skip(key domain.Key, reason string) error {
	o.log.Debug("screen skipped", slog.String("key", key.String()), slog.String("reason", reason))
	return nil
}

// sendFailed applies the Telegram error policy for posts: fatal bot errors
// end the daemon, a closed or gone topic is the reconciler's business, and
// anything else is retried on the next transition.
func (o *outbound) sendFailed(key domain.Key, err error) error {
	if isFatal(err) {
		o.log.Error("screen post failed with a fatal telegram error", slog.String("key", key.String()), slog.String("err", err.Error()))
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		o.log.Debug("screen post cancelled", slog.String("key", key.String()))
		return nil
	}
	o.log.Warn("screen post failed", slog.String("key", key.String()), slog.String("err", err.Error()))
	return nil
}

// screenLines splits terminal text into lines with CR and trailing spaces
// removed; blank lines are kept so screens align line by line.
func screenLines(text string) []string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t\r")
	}
	return lines
}

// trimScreen normalises terminal text for posting: trailing spaces are cut
// from every line and blank lines at both ends are dropped.
func trimScreen(text string) string {
	lines := screenLines(text)
	start, end := 0, len(lines)
	for start < end && lines[start] == "" {
		start++
	}
	for end > start && lines[end-1] == "" {
		end--
	}
	return strings.Join(lines[start:end], "\n")
}

func hashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
