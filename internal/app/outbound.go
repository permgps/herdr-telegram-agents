package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
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
}

// pendingCapture is the first screen of a question kept while the blocked
// delay runs; seq is the agent's StateChangeSeq at that time, so a newer
// question restarts the wait.
type pendingCapture struct {
	text string
	seq  int64
}

// keyboard is the inline keyboard under one blocked post.
type keyboard struct {
	messageID int
	choices   []domain.Choice
}

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

// Reactions put on the operator's prompt.
const (
	reactionTaken = "👀"
	reactionDone  = "✅"
)

func newOutbound(herdr domain.HerdrGateway, tg domain.TelegramGateway, topics *topicView, agents agentLookup,
	live func() []domain.Agent, capture *Capture, opts *Options, replies domain.ReplySource, clock domain.Clock, log *slog.Logger) *outbound {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	paused := func() bool { return false }
	doneMode := func() domain.DoneMode { return domain.DoneScreen }
	reactions := func() bool { return true }
	minTurn := func() time.Duration { return 0 }
	blockedDelay := func() time.Duration { return 0 }
	if opts != nil {
		paused = func() bool { return !opts.SyncEnabled() }
		doneMode = opts.PostsDone
		reactions = opts.PostsReactions
		minTurn = opts.MinTurn
		blockedDelay = opts.BlockedDelay
	}
	if live == nil {
		live = func() []domain.Agent { return nil }
	}
	return &outbound{
		herdr:        herdr,
		tg:           tg,
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
	}
}

// TurnDue delivers keys whose idle timer fired; call EndTurn for each.
func (o *outbound) TurnDue() <-chan domain.Key { return o.turnDeb.Due() }

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
	var choices []domain.Choice
	if agent.Status == domain.StatusBlocked {
		choices = domain.ParseChoices(text)
		out.Buttons = choiceButtons(choices)
		o.logChoices(key, choices)
	}
	id, err := o.tg.Send(ctx, out)
	if err != nil {
		return o.sendFailed(key, err)
	}
	o.lastPosted[key] = hash
	if len(out.Buttons) > 0 {
		o.keyboards[key] = keyboard{messageID: id, choices: choices}
	}
	if notify && agent.Status == domain.StatusBlocked {
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
		slog.Int("buttons", len(out.Buttons)), slog.Int("message_id", id), slog.Bool("notify", notify), slog.Bool("forced", force))
	return nil
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

func (o *outbound) logChoices(key domain.Key, choices []domain.Choice) {
	if len(choices) == 0 {
		o.log.Debug("choices rejected", slog.String("key", key.String()))
		return
	}
	numbers := make([]int, 0, len(choices))
	for _, c := range choices {
		numbers = append(numbers, c.Number)
	}
	o.log.Debug("choices parsed", slog.String("key", key.String()), slog.Int("count", len(choices)), slog.Any("numbers", numbers))
}

// choiceButtons renders options as buttons: the keycap digit, a space and
// the label; the button's data is the digit the agent expects.
func choiceButtons(choices []domain.Choice) []domain.Button {
	if len(choices) == 0 {
		return nil
	}
	buttons := make([]domain.Button, 0, len(choices))
	for _, c := range choices {
		buttons = append(buttons, domain.Button{Text: keycap(c.Number) + " " + cutLabel(c.Label), Data: strconv.Itoa(c.Number)})
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
	o.log.Debug("buttons retired", slog.String("key", key.String()), slog.Int("message_id", kb.messageID), slog.String("reason", reason))
	return o.absorbEdit(key, o.tg.EditButtons(ctx, kb.messageID, nil))
}

// Press handles a button under one of the blocked posts: the digit goes to
// the agent as a key press, the keyboard turns into a single ✅ button and
// the screen timer is armed so a follow-up question is posted. Every path
// answers the callback so the phone stops its spinner. Only fatal Telegram
// errors are returned.
func (o *outbound) Press(ctx context.Context, ev domain.ButtonPressed) error {
	key, ok := o.topics.KeyForThread(ev.ThreadID)
	if !ok {
		o.log.Debug("button for unknown thread", slog.Int("thread_id", ev.ThreadID), slog.Int("message_id", ev.MessageID))
		return o.stale(ctx, ev, "topic is not mapped")
	}
	if ev.Data == "done" {
		o.log.Debug("button already answered", slog.String("key", key.String()), slog.Int("message_id", ev.MessageID))
		return o.answer(ctx, ev.CallbackID, "already answered")
	}
	n, err := strconv.Atoi(ev.Data)
	if err != nil || n < 1 || n > 9 {
		o.log.Warn("button data unknown", slog.String("key", key.String()), slog.Int("message_id", ev.MessageID), slog.String("data", ev.Data))
		return o.stale(ctx, ev, "unknown button")
	}
	kb, ok := o.keyboards[key]
	if !ok || kb.messageID != ev.MessageID {
		o.log.Debug("button stale", slog.String("key", key.String()), slog.Int("message_id", ev.MessageID),
			slog.Int("latest_id", kb.messageID), slog.String("reason", "not_latest"))
		return o.stale(ctx, ev, "not the latest question")
	}
	agent, alive := o.agents(key)
	o.log.Info("button pressed", slog.String("key", key.String()), slog.Int("thread_id", ev.ThreadID), slog.Int("message_id", ev.MessageID),
		slog.Int64("from_id", ev.FromID), slog.String("data", ev.Data), slog.String("status", string(agent.Status)), slog.Bool("alive", alive))
	switch {
	case !alive:
		if err := o.retire(ctx, key, "exited"); err != nil {
			return err
		}
		return o.answer(ctx, ev.CallbackID, "agent has exited")
	case agent.Status != domain.StatusBlocked:
		if err := o.retire(ctx, key, "not_blocked"); err != nil {
			return err
		}
		return o.answer(ctx, ev.CallbackID, "agent is not waiting anymore")
	}
	if err := o.herdr.SendKeys(ctx, key.PaneID, []string{ev.Data}); err != nil {
		o.log.Warn("button send_keys failed", slog.String("key", key.String()), slog.String("data", ev.Data), slog.String("err", err.Error()))
		if err := o.retire(ctx, key, "failed"); err != nil {
			return err
		}
		return o.answer(ctx, ev.CallbackID, "⚠️ "+failureReason(err))
	}
	delete(o.keyboards, key)
	label := ev.Data
	for _, c := range kb.choices {
		if c.Number == n {
			label = cutLabel(c.Label)
		}
	}
	pressed := []domain.Button{{Text: "✅ " + ev.Data + " · " + label, Data: "done"}}
	if err := o.absorbEdit(key, o.tg.EditButtons(ctx, ev.MessageID, pressed)); err != nil {
		return err
	}
	o.log.Info("button sent", slog.String("key", key.String()), slog.String("data", ev.Data), slog.Int("message_id", ev.MessageID))
	if err := o.answer(ctx, ev.CallbackID, "sent: "+ev.Data); err != nil {
		return err
	}
	// The follow-up question starts from a fresh first capture.
	delete(o.captures, key)
	o.deb.Schedule(key)
	return nil
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
