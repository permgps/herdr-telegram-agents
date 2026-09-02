package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"

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

	deb        *debouncer
	lastPosted map[domain.Key]string // SHA-256 of the last screen posted per key
}

func newOutbound(herdr domain.HerdrGateway, tg domain.TelegramGateway, topics *topicView, agents agentLookup, clock domain.Clock, log *slog.Logger) *outbound {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &outbound{
		herdr:      herdr,
		tg:         tg,
		topics:     topics,
		agents:     agents,
		log:        log,
		deb:        newDebouncer(clock, screenSettle, log),
		lastPosted: map[domain.Key]string{},
	}
}

// Due delivers keys whose settle timer fired; call Fire for each.
func (o *outbound) Due() <-chan domain.Key { return o.deb.Due() }

// Observe schedules a screen post when the event moves an agent into a
// status worth posting, and cancels a pending one otherwise. A blocked
// agent that is already known at startup (AgentAppeared) is posted too:
// the question was asked while nobody was watching.
func (o *outbound) Observe(ev AgentEvent) {
	key := ev.Agent.Key
	switch {
	case ev.Kind == AgentGone:
		o.deb.Cancel(key)
		delete(o.lastPosted, key)
	case ev.Kind == AgentAppeared && ev.Agent.Status == domain.StatusBlocked,
		ev.Kind == AgentChanged && (ev.Agent.Status == domain.StatusBlocked || ev.Agent.Status == domain.StatusDone):
		o.log.Debug("screen scheduled", slog.String("key", key.String()), slog.String("status", string(ev.Agent.Status)))
		o.deb.Schedule(key)
	default:
		o.deb.Cancel(key)
	}
}

// Fire posts the screen for key if the agent is still blocked or done and
// the text differs from the last post. Read failures are logged and left
// to the next transition; only fatal Telegram errors are returned.
func (o *outbound) Fire(ctx context.Context, key domain.Key) error {
	agent, ok := o.agents(key)
	if !ok {
		return o.skip(key, "exited")
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
	if o.lastPosted[key] == hash {
		return o.skip(key, "duplicate")
	}
	out := domain.Outgoing{ThreadID: entry.ThreadID, Text: text, Code: true, Notify: agent.Status == domain.StatusBlocked}
	if err := o.tg.Send(ctx, out); err != nil {
		return o.sendFailed(key, err)
	}
	o.lastPosted[key] = hash
	o.log.Info("screen posted", slog.String("key", key.String()), slog.Int("thread_id", entry.ThreadID),
		slog.String("status", string(agent.Status)), slog.Int("lines", strings.Count(text, "\n")+1), slog.Int("bytes", len(text)))
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
	if err := o.tg.Send(ctx, domain.Outgoing{ThreadID: entry.ThreadID, Text: text, Code: true}); err != nil {
		return err
	}
	o.log.Info("screen posted", slog.String("key", key.String()), slog.Int("thread_id", entry.ThreadID),
		slog.String("status", "requested"), slog.Int("lines", strings.Count(text, "\n")+1), slog.Int("bytes", len(text)))
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

// trimScreen normalises terminal text for posting: trailing spaces are cut
// from every line and blank lines at both ends are dropped.
func trimScreen(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t\r")
	}
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
