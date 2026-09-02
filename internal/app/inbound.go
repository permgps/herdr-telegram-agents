package app

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// helpText is the command list shown by /help in a topic and in General.
const helpText = `Commands
/screen [N]: post the agent screen, the whole visible screen or its last N lines
/keys k1 k2 ...: send raw keys to the agent (esc, enter, y, 1 ...)
/focus: bring the agent's pane to the front in Herdr
/status: this agent's status; in General, every agent with a link to its topic
/help: this list

While an agent is blocked, y, n, yes, no, 1-9, enter, ok and esc are sent as keys.
Any other text is typed into the agent as a prompt.`

// reactionOK is the reaction put on an operator message once Herdr took it.
const reactionOK = "👍"

// inbound turns operator messages into Herdr calls and answers commands.
// It runs on the bridge goroutine.
type inbound struct {
	herdr       domain.HerdrGateway
	tg          domain.TelegramGateway
	topics      *topicView
	agents      agentLookup
	live        func() []domain.Agent
	out         *outbound
	chatID      int64
	botUsername string
	log         *slog.Logger
}

func newInbound(herdr domain.HerdrGateway, tg domain.TelegramGateway, topics *topicView, agents agentLookup,
	live func() []domain.Agent, out *outbound, chatID int64, botUsername string, log *slog.Logger) *inbound {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &inbound{
		herdr: herdr, tg: tg, topics: topics, agents: agents, live: live, out: out,
		chatID: chatID, botUsername: botUsername, log: log,
	}
}

// HandleTopic routes one topic message: prompt, short reply or command.
// Success is acknowledged with a reaction, failure with a quoted reply.
// Only fatal Telegram errors are returned.
func (i *inbound) HandleTopic(ctx context.Context, msg domain.TopicMessage) error {
	key, ok := i.topics.KeyForThread(msg.ThreadID)
	if !ok {
		i.log.Debug("topic message for unknown thread dropped", slog.Int("thread_id", msg.ThreadID), slog.Int("message_id", msg.MessageID))
		return nil
	}
	entry, _ := i.topics.Entry(key)
	agent, alive := i.agents(key)
	if !alive || !entry.Status.Live() {
		i.log.Info("topic message for exited agent", slog.String("key", key.String()), slog.Int("thread_id", msg.ThreadID), slog.Int("message_id", msg.MessageID))
		return i.reply(ctx, msg.ThreadID, msg.MessageID, "agent has exited")
	}
	cmd := domain.Route(msg.Text, i.botUsername, agent.Status)
	i.log.Info("topic command", slog.String("kind", string(cmd.Kind)), slog.String("key", key.String()),
		slog.Int("thread_id", msg.ThreadID), slog.Int64("from_id", msg.FromID), slog.Int("message_id", msg.MessageID),
		slog.String("status", string(agent.Status)), slog.Int("len", len(msg.Text)))
	i.log.Debug("topic command text", slog.String("key", key.String()), slog.String("text", msg.Text), slog.Any("keys", cmd.Keys), slog.Int("lines", cmd.Lines))
	switch cmd.Kind {
	case domain.CmdPrompt:
		return i.herdrCall(ctx, msg, key, "prompt", func(ctx context.Context) error {
			return i.herdr.Prompt(ctx, key.PaneID, cmd.Text)
		})
	case domain.CmdKeys:
		return i.herdrCall(ctx, msg, key, "send_keys", func(ctx context.Context) error {
			return i.herdr.SendKeys(ctx, key.PaneID, cmd.Keys)
		})
	case domain.CmdFocus:
		return i.herdrCall(ctx, msg, key, "focus", func(ctx context.Context) error {
			return i.herdr.Focus(ctx, key.PaneID)
		})
	case domain.CmdScreen:
		if err := i.out.Screen(ctx, key, cmd.Lines); err != nil {
			return i.failed(ctx, msg, key, "screen", err)
		}
		return nil
	case domain.CmdStatus:
		line := fmt.Sprintf("%s %s · %s · pane %s", agent.Status.Emoji(), agent.Status, agent.Label(), key.PaneID)
		return i.reply(ctx, msg.ThreadID, msg.MessageID, line)
	case domain.CmdHelp:
		return i.reply(ctx, msg.ThreadID, msg.MessageID, helpText)
	default:
		return i.reply(ctx, msg.ThreadID, msg.MessageID, "unknown command, see /help")
	}
}

// HandleGeneral answers a command written in the General topic.
func (i *inbound) HandleGeneral(ctx context.Context, cmd domain.GeneralCommand) error {
	parsed := domain.ParseCommand(cmd.Text, i.botUsername)
	i.log.Info("general command", slog.String("kind", string(parsed.Kind)), slog.Int64("from_id", cmd.FromID), slog.Int("message_id", cmd.MessageID))
	switch parsed.Kind {
	case domain.CmdStatus:
		text := i.statusSummary()
		return i.absorb(i.tg.Send(ctx, domain.Outgoing{ThreadID: 0, Text: text, HTML: true, ReplyTo: cmd.MessageID}))
	case domain.CmdHelp:
		return i.reply(ctx, 0, cmd.MessageID, helpText)
	default:
		return i.reply(ctx, 0, cmd.MessageID, "unknown command, see /help")
	}
}

// statusSummary lists the live agents sorted by label, each linked to its
// topic, as HTML.
func (i *inbound) statusSummary() string {
	agents := i.live()
	live := agents[:0]
	for _, a := range agents {
		if a.Status.Live() {
			live = append(live, a)
		}
	}
	if len(live) == 0 {
		return "no agents"
	}
	sort.Slice(live, func(a, b int) bool {
		if live[a].Label() != live[b].Label() {
			return live[a].Label() < live[b].Label()
		}
		return live[a].Key.String() < live[b].Key.String()
	})
	lines := make([]string, 0, len(live)+1)
	lines = append(lines, plural(len(live), "agent"))
	for _, a := range live {
		label := html.EscapeString(a.Label())
		if entry, ok := i.topics.Entry(a.Key); ok && entry.Status.Live() {
			label = fmt.Sprintf(`<a href="%s">%s</a>`, topicLink(i.chatID, entry.ThreadID), label)
		}
		lines = append(lines, a.Status.Emoji()+" "+label)
	}
	return strings.Join(lines, "\n")
}

// herdrCall runs one Herdr call for an operator message and reports the
// outcome: a reaction on success, a quoted reply on failure.
func (i *inbound) herdrCall(ctx context.Context, msg domain.TopicMessage, key domain.Key, method string, call func(context.Context) error) error {
	if err := call(ctx); err != nil {
		return i.failed(ctx, msg, key, method, err)
	}
	i.log.Debug("herdr call ok", slog.String("method", method), slog.String("key", key.String()), slog.Int("message_id", msg.MessageID))
	if err := i.tg.React(ctx, msg.ThreadID, msg.MessageID, reactionOK); err != nil {
		if isFatal(err) {
			return err
		}
		i.log.Debug("reaction failed", slog.Int("thread_id", msg.ThreadID), slog.Int("message_id", msg.MessageID), slog.String("err", err.Error()))
	}
	return nil
}

// failed tells the operator why a call did not go through.
func (i *inbound) failed(ctx context.Context, msg domain.TopicMessage, key domain.Key, method string, err error) error {
	i.log.Warn("herdr call failed", slog.String("method", method), slog.String("key", key.String()),
		slog.Int("thread_id", msg.ThreadID), slog.Int("message_id", msg.MessageID), slog.String("err", err.Error()))
	if isFatal(err) {
		return err
	}
	return i.reply(ctx, msg.ThreadID, msg.MessageID, "⚠️ "+failureReason(err))
}

// reply posts a silent message quoting the operator's message.
func (i *inbound) reply(ctx context.Context, threadID, messageID int, text string) error {
	return i.absorb(i.tg.Send(ctx, domain.Outgoing{ThreadID: threadID, Text: text, ReplyTo: messageID}))
}

// absorb logs a Telegram failure and returns it only when it is fatal for
// the daemon.
func (i *inbound) absorb(err error) error {
	switch {
	case err == nil:
		return nil
	case isFatal(err):
		i.log.Error("reply failed with a fatal telegram error", slog.String("err", err.Error()))
		return err
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		i.log.Debug("reply cancelled", slog.String("err", err.Error()))
	default:
		i.log.Warn("reply failed", slog.String("err", err.Error()))
	}
	return nil
}

// failureReason renders a Herdr error for the operator in one line.
func failureReason(err error) string {
	if errors.Is(err, domain.ErrAgentGone) {
		return "agent is gone"
	}
	if errors.Is(err, domain.ErrDisconnected) {
		return "herdr is unreachable"
	}
	line, _, _ := strings.Cut(err.Error(), "\n")
	return strings.TrimSpace(line)
}

// topicLink builds the t.me deep link for a topic in a supergroup: the
// chat id without its -100 prefix, then the thread id.
func topicLink(chatID int64, threadID int) string {
	id := chatID
	if id < 0 {
		id = -id
	}
	s := strings.TrimPrefix(strconv.FormatInt(id, 10), "100")
	return "https://t.me/c/" + s + "/" + strconv.Itoa(threadID)
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}
