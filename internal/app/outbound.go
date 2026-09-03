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

	deb        *debouncer
	lastPosted map[domain.Key]string // SHA-256 of the last screen posted per key
	// keyboards holds the latest button set per agent: which message
	// carries it and which options it offers. Only the latest keyboard
	// acts; older ones are retired when pressed.
	keyboards map[domain.Key]keyboard
	// announced marks agents whose current question was posted with a
	// sound; cleared when the agent leaves blocked. One sound per question.
	announced map[domain.Key]bool
}

// keyboard is the inline keyboard under one blocked post.
type keyboard struct {
	messageID int
	choices   []domain.Choice
}

func newOutbound(herdr domain.HerdrGateway, tg domain.TelegramGateway, topics *topicView, agents agentLookup,
	live func() []domain.Agent, capture *Capture, opts *Options, clock domain.Clock, log *slog.Logger) *outbound {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	paused := func() bool { return false }
	if opts != nil {
		paused = func() bool { return !opts.SyncEnabled() }
	}
	if live == nil {
		live = func() []domain.Agent { return nil }
	}
	return &outbound{
		herdr:      herdr,
		tg:         tg,
		topics:     topics,
		agents:     agents,
		live:       live,
		capture:    capture,
		clock:      clock,
		paused:     paused,
		quiet:      func() bool { return false },
		posts:      func() domain.PostsMode { return domain.PostsNormal },
		reannounce: func() bool { return false },
		log:        log,
		deb:        newDebouncer(clock, screenSettle, log),
		lastPosted: map[domain.Key]string{},
		keyboards:  map[domain.Key]keyboard{},
		announced:  map[domain.Key]bool{},
	}
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
	if ev.Agent.Status != domain.StatusBlocked || ev.Kind == AgentGone {
		delete(o.announced, key)
	}
	switch {
	case ev.Kind == AgentGone:
		o.deb.Cancel(key)
	case ev.Kind == AgentAppeared && ev.Agent.Status == domain.StatusBlocked,
		ev.Kind == AgentChanged && (ev.Agent.Status == domain.StatusBlocked || ev.Agent.Status == domain.StatusDone):
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
	delete(o.lastPosted, key)
	delete(o.announced, key)
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
		return o.skip(key, "not_blocked")
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
	screen, err := o.herdr.ReadScreen(ctx, key.PaneID, domain.ScreenDetection, lines)
	if err != nil {
		o.log.Warn("screen read failed", slog.String("key", key.String()), slog.String("err", err.Error()))
		return nil
	}
	text := trimScreen(screen.Text)
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
	out := domain.Outgoing{ThreadID: entry.ThreadID, Text: text, Code: true, Notify: notify}
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
	o.log.Info("screen posted", slog.String("key", key.String()), slog.Int("thread_id", entry.ThreadID),
		slog.String("status", string(agent.Status)), slog.Int("lines", strings.Count(text, "\n")+1), slog.Int("bytes", len(text)),
		slog.Int("buttons", len(out.Buttons)), slog.Int("message_id", id), slog.Bool("notify", notify), slog.Bool("forced", force))
	return nil
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
