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
	"time"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// helpText is the command list shown by /help in a topic and in General.
const helpText = `Commands
/screen [N|all]: post the agent screen: the whole visible screen, its last N lines, or with "all" everything since your last message
/keys k1 k2 ...: send raw keys to the agent (esc, enter, y, 1 ...)
/focus: bring the agent's pane to the front in Herdr
/stop: send esc to the agent (soft cancel of the running turn or dialog)
/interrupt: send ctrl+c to the agent (hard interrupt)
/close: close the agent's pane after a Yes/No confirmation
/clear, /compact [instructions], /usage, /model [name]: typed into the agent as Claude Code commands while it is idle; the screen after the command is posted as a reply, /usage and a bare /model are closed with esc for you
/status: this agent's status; in General, every agent with a link to its topic
/away [2h]: treat you as away until /here or for the given time, so Telegram gets everything (General only)
/here: back to automatic presence (General only)
/new <workspace> [kind]: start an agent in a new tab of that workspace (General only; kind defaults to claude)
/options: settings panel (General only): sync, quiet mode, status icons, secret redaction, topic cleanup
/help: this list

While an agent is blocked, y, n, yes, no, 1-9, enter, ok and esc are sent as keys.
A question with up to 5 numbered options also arrives with buttons; pressing one sends its number and the button turns into ✅.
Any other text is typed into the agent as a prompt.`

// Control command replies (English, like the panel strings).
const (
	// stoppedReply and interruptedReply confirm the key went out.
	stoppedReply     = "⏹ sent esc"
	interruptedReply = "⛔ sent ctrl+c"
	// topicOnly answers an agent command written in General.
	topicOnly = "agent commands live in the agent's topic"
	// closePrefix marks the callback data of the /close keyboard; closeYes
	// and closeNo are its two buttons.
	closePrefix = "c:"
	closeYes    = "c:y"
	closeNo     = "c:n"
	// closeAskFmt is the question, closeYesLabel / closeNoLabel the
	// buttons, closingFmt / closeKept the texts the question turns into.
	closeAskFmt   = "Close %s? The pane and its tab go away."
	closeYesLabel = "Yes, close"
	closeNoLabel  = "No"
	closingFmt    = "closing %s …"
	closeKept     = "not closed"
	// newInGeneral answers /new written in a topic.
	newInGeneral = "/new works in General: /new <workspace> [kind]"
	// Replies of /new: the workspace list, the two progress lines and the
	// failures.
	newWorkspacesFmt   = "workspaces: %s"
	newNoWorkspaces    = "no workspaces"
	newNoMatchFmt      = "no workspace named %q"
	newManyFmt         = "%q matches %s"
	newStartingFmt     = "starting %s in %s …"
	newStartedFmt      = "started %s in %s (pane %s)"
	newFailedFmt       = "⚠️ %s did not start in %s: %s"
	newTabFailedPrefix = "⚠️ could not create a tab: "
	newListFailed      = "⚠️ herdr: "
)

// Presence replies and headers (English, like the panel strings).
const (
	presenceInGeneral   = "presence commands live in General"
	presenceUnavailable = "quiet mode is not available"
	presenceOff         = "quiet mode is off (/options → Quiet)"
	// presenceAwayUntil and presenceAwayOpen answer /away.
	presenceAwayUntil = "🏃 away until %s, Telegram gets everything; /here returns to automatic"
	presenceAwayOpen  = "🏃 away until /here, Telegram gets everything"
	// presenceHereFmt answers /here with the automatic verdict.
	presenceHereFmt     = "🖥 presence is automatic again: %s"
	presenceVerdictDesk = "at the desk, quiet on"
	presenceVerdictAway = "away"
	presenceVerdictNone = "not available on this platform"
	// Headers of the General /status summary.
	presenceHeaderQuiet  = "🔕 quiet: you are at the desk (/away to override)\n"
	presenceHeaderManual = "🏃 away (manual) until %s\n"
	presenceHeaderOpen   = "🏃 away (manual) until /here\n"
	presenceTimeLayout   = "15:04"
)

// inbound turns operator messages into Herdr calls and answers commands.
// It runs on the bridge goroutine.
type inbound struct {
	herdr  domain.HerdrGateway
	tg     domain.TelegramGateway
	topics *topicView
	agents agentLookup
	live   func() []domain.Agent
	out    *outbound
	opts   *Options
	// presence answers /away, /here and the /status header; nil until
	// SetPresence, when the commands say quiet mode is unavailable.
	presence    *Presence
	panel       *panel
	chatID      int64
	botUsername string
	clock       domain.Clock
	log         *slog.Logger

	// deb arms the settle timer of a forwarded command; pending holds what
	// to do when it fires. Both are touched on the bridge goroutine only.
	deb     *debouncer
	pending map[domain.Key]followUp
	// closing is the message id of the active /close question per agent;
	// a newer question retires the older one. Bridge goroutine only.
	closing map[domain.Key]int
	// async runs a slow agent start off the bridge goroutine and delivers
	// its result to StartFinished; the bridge wires it, tests without a
	// bridge get the synchronous default.
	async func(run func(context.Context) startResult)
}

// followUp is a forwarded command waiting for its settle timer: which
// operator message to quote and what the rule says to post.
type followUp struct {
	cmd       domain.Command
	word      string
	threadID  int
	messageID int
}

func newInbound(herdr domain.HerdrGateway, tg domain.TelegramGateway, topics *topicView, agents agentLookup,
	live func() []domain.Agent, out *outbound, opts *Options, chatID int64, botUsername string, clock domain.Clock, log *slog.Logger) *inbound {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if opts == nil {
		opts = NewOptions(domain.DefaultOptions(), nil, nil, log)
	}
	in := &inbound{
		herdr: herdr, tg: tg, topics: topics, agents: agents, live: live, out: out,
		opts: opts, panel: newPanel(opts, tg, log),
		chatID: chatID, botUsername: botUsername, log: log,
		clock:   clock,
		deb:     newDebouncer(clock, commandSettle, log),
		pending: map[domain.Key]followUp{},
		closing: map[domain.Key]int{},
	}
	in.async = func(run func(context.Context) startResult) {
		_ = in.StartFinished(context.Background(), run(context.Background()))
	}
	return in
}

// Due delivers agent keys whose forwarded command has settled; the owner
// calls Fire for each.
func (i *inbound) Due() <-chan domain.Key { return i.deb.Due() }

// Pending returns how many forwarded commands await their follow-up.
func (i *inbound) Pending() int { return len(i.pending) }

// HandleTopic routes one topic message: prompt, short reply or command.
// Success is silent (the topic icon turning ⚡ shows the agent took the
// prompt), failure gets a quoted reply. Only fatal Telegram errors are
// returned.
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
		if err := i.herdr.Prompt(ctx, key.PaneID, cmd.Text); err != nil {
			return i.failed(ctx, msg, key, "prompt", err)
		}
		i.log.Debug("herdr call ok", slog.String("method", "prompt"), slog.String("key", key.String()), slog.Int("message_id", msg.MessageID))
		return i.out.PromptSent(ctx, key, msg.ThreadID, msg.MessageID)
	case domain.CmdKeys:
		return i.herdrCall(ctx, msg, key, "send_keys", func(ctx context.Context) error {
			return i.herdr.SendKeys(ctx, key.PaneID, cmd.Keys)
		})
	case domain.CmdFocus:
		return i.herdrCall(ctx, msg, key, "focus", func(ctx context.Context) error {
			return i.herdr.Focus(ctx, key.PaneID)
		})
	case domain.CmdStop:
		return i.control(ctx, msg, key, "stop", domain.KeyEscape, stoppedReply)
	case domain.CmdInterrupt:
		return i.control(ctx, msg, key, "interrupt", domain.KeyInterrupt, interruptedReply)
	case domain.CmdClose:
		return i.askClose(ctx, msg, key, agent)
	case domain.CmdScreen:
		var err error
		if cmd.All {
			err = i.out.ScreenAll(ctx, key)
		} else {
			err = i.out.Screen(ctx, key, cmd.Lines)
		}
		if err != nil {
			return i.failed(ctx, msg, key, "screen", err)
		}
		return nil
	case domain.CmdStatus:
		line := fmt.Sprintf("%s %s · %s · pane %s", i.opts.StatusIcons().For(agent.Status), agent.Status, agent.Label(), key.PaneID)
		return i.reply(ctx, msg.ThreadID, msg.MessageID, line)
	case domain.CmdHelp:
		return i.reply(ctx, msg.ThreadID, msg.MessageID, helpText)
	case domain.CmdOptions:
		return i.reply(ctx, msg.ThreadID, msg.MessageID, panelInGeneral)
	case domain.CmdAway, domain.CmdHere:
		return i.reply(ctx, msg.ThreadID, msg.MessageID, presenceInGeneral)
	case domain.CmdNew:
		return i.reply(ctx, msg.ThreadID, msg.MessageID, newInGeneral)
	case domain.CmdForward:
		return i.forward(ctx, msg, key, agent, cmd)
	default:
		return i.reply(ctx, msg.ThreadID, msg.MessageID, "unknown command, see /help")
	}
}

// control sends one control key (esc or ctrl+c) to a live agent in any
// status and confirms it with a quoted reply; a failure gets the usual ⚠️
// reply instead.
func (i *inbound) control(ctx context.Context, msg domain.TopicMessage, key domain.Key, method, keyName, confirm string) error {
	if err := i.herdr.SendKeys(ctx, key.PaneID, []string{keyName}); err != nil {
		return i.failed(ctx, msg, key, method, err)
	}
	i.log.Debug("control keys sent", slog.String("method", method), slog.String("key", key.String()),
		slog.String("keys", keyName), slog.Int("message_id", msg.MessageID))
	return i.reply(ctx, msg.ThreadID, msg.MessageID, confirm)
}

// askClose posts the Yes/No question of /close and remembers its message
// id; an older question of the same agent loses its buttons first, so one
// press cannot close a pane twice.
func (i *inbound) askClose(ctx context.Context, msg domain.TopicMessage, key domain.Key, agent domain.Agent) error {
	if prev, ok := i.closing[key]; ok {
		i.log.Debug("close question retired", slog.String("key", key.String()), slog.Int("message_id", prev))
		if err := i.out.absorbEdit(key, i.tg.EditButtons(ctx, prev, nil)); err != nil {
			return err
		}
		delete(i.closing, key)
	}
	id, err := i.tg.Send(ctx, domain.Outgoing{
		ThreadID: msg.ThreadID,
		Text:     fmt.Sprintf(closeAskFmt, agent.Label()),
		ReplyTo:  msg.MessageID,
		Buttons: []domain.Button{
			{Text: closeYesLabel, Data: closeYes, Row: 1},
			{Text: closeNoLabel, Data: closeNo, Row: 1},
		},
	})
	if err != nil {
		return i.absorb(err)
	}
	i.closing[key] = id
	i.log.Info("close requested", slog.String("key", key.String()), slog.Int("thread_id", msg.ThreadID),
		slog.Int("message_id", id), slog.Int64("from_id", msg.FromID))
	return nil
}

// PressClose serves a button of the /close question (callback data with
// the close prefix): Yes closes the pane through Herdr and the topic then
// closes through the ordinary exit path, No keeps everything. Every path
// answers the callback. Only fatal Telegram errors are returned.
func (i *inbound) PressClose(ctx context.Context, ev domain.ButtonPressed) error {
	key, ok := i.topics.KeyForThread(ev.ThreadID)
	if !ok {
		i.log.Debug("close button for unknown thread", slog.Int("thread_id", ev.ThreadID), slog.Int("message_id", ev.MessageID))
		return i.out.stale(ctx, ev, "topic is not mapped")
	}
	if latest, ok := i.closing[key]; !ok || latest != ev.MessageID {
		i.log.Debug("close button stale", slog.String("key", key.String()), slog.Int("message_id", ev.MessageID), slog.Int("latest_id", latest))
		return i.out.stale(ctx, ev, "not the latest question")
	}
	agent, alive := i.agents(key)
	i.log.Info("close button pressed", slog.String("key", key.String()), slog.Int("thread_id", ev.ThreadID),
		slog.Int("message_id", ev.MessageID), slog.Int64("from_id", ev.FromID), slog.String("data", ev.Data), slog.Bool("alive", alive))
	delete(i.closing, key)
	if !alive {
		if err := i.out.absorbEdit(key, i.tg.EditButtons(ctx, ev.MessageID, nil)); err != nil {
			return err
		}
		return i.out.answer(ctx, ev.CallbackID, "agent has exited")
	}
	switch ev.Data {
	case closeNo:
		i.log.Info("close cancelled", slog.String("key", key.String()), slog.Int("message_id", ev.MessageID))
		if err := i.out.absorbEdit(key, i.tg.EditText(ctx, ev.MessageID, closeKept, false, nil)); err != nil {
			return err
		}
		return i.out.answer(ctx, ev.CallbackID, "kept")
	case closeYes:
		if err := i.herdr.ClosePane(ctx, key.PaneID); err != nil {
			i.log.Warn("pane close failed", slog.String("key", key.String()), slog.String("pane", key.PaneID), slog.String("err", err.Error()))
			if err := i.out.absorbEdit(key, i.tg.EditText(ctx, ev.MessageID, "⚠️ could not close: "+failureReason(err), false, nil)); err != nil {
				return err
			}
			return i.out.answer(ctx, ev.CallbackID, "⚠️ failed")
		}
		i.log.Info("pane close sent", slog.String("key", key.String()), slog.String("pane", key.PaneID), slog.Int("message_id", ev.MessageID))
		if err := i.out.absorbEdit(key, i.tg.EditText(ctx, ev.MessageID, fmt.Sprintf(closingFmt, agent.Label()), false, nil)); err != nil {
			return err
		}
		return i.out.answer(ctx, ev.CallbackID, "closing")
	}
	i.log.Warn("close button data unknown", slog.String("key", key.String()), slog.String("data", ev.Data))
	return i.out.stale(ctx, ev, "unknown button")
}

// Forget drops the per-agent state of an agent that is gone: its /close
// question needs no edit, the topic is closing anyway.
func (i *inbound) Forget(key domain.Key) {
	if id, ok := i.closing[key]; ok {
		i.log.Debug("close question dropped", slog.String("key", key.String()), slog.Int("message_id", id))
		delete(i.closing, key)
	}
}

// forward types a Claude Code command into the agent when it is waiting at
// its prompt and arms the follow-up that posts the resulting screen. A
// working or blocked agent gets a refusal instead: the text would land in
// a dialog or in Claude's input queue, and the esc that closes an overlay
// would interrupt it.
func (i *inbound) forward(ctx context.Context, msg domain.TopicMessage, key domain.Key, agent domain.Agent, cmd domain.Command) error {
	word := forwardWord(cmd.Text)
	if hint, refused := forwardRefusal(agent.Status); refused {
		i.log.Info("command refused", slog.String("key", key.String()), slog.String("word", word),
			slog.String("status", string(agent.Status)), slog.Int("message_id", msg.MessageID))
		return i.reply(ctx, msg.ThreadID, msg.MessageID, "⚠️ "+hint)
	}
	if err := i.herdr.Prompt(ctx, key.PaneID, cmd.Text); err != nil {
		return i.failed(ctx, msg, key, "prompt", err)
	}
	i.log.Info("command forwarded", slog.String("key", key.String()), slog.String("word", word),
		slog.Int("thread_id", msg.ThreadID), slog.Int("message_id", msg.MessageID),
		slog.String("post", string(cmd.Forward.Post)), slog.Bool("dismiss", cmd.Forward.Dismiss))
	if cmd.Forward.Post == domain.ForwardPostNone {
		return nil
	}
	if prev, ok := i.pending[key]; ok {
		i.log.Debug("command follow-up replaced", slog.String("key", key.String()), slog.String("word", prev.word), slog.Int("message_id", prev.messageID))
	}
	i.pending[key] = followUp{cmd: cmd, word: word, threadID: msg.ThreadID, messageID: msg.MessageID}
	i.deb.Schedule(key)
	i.log.Debug("command follow-up scheduled", slog.String("key", key.String()), slog.String("word", word), slog.Int64("delay_ms", i.deb.delay.Milliseconds()))
	return nil
}

// forwardRefusal names the statuses in which a forwarded command is not
// typed and the hint the operator gets.
func forwardRefusal(status domain.Status) (string, bool) {
	switch status {
	case domain.StatusWorking:
		return "agent is working, try again when it is idle", true
	case domain.StatusBlocked:
		return "agent is waiting for an answer: reply to it or send /keys esc first", true
	}
	return "", false
}

// forwardWord is the command word of a forwarded line, for logs.
func forwardWord(line string) string {
	word, _, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	return word
}

// Fire runs the follow-up of a forwarded command once its settle timer
// fired: read the screen, post it as a quoted reply and, for overlay
// commands, send esc. A read failure is reported and the esc still goes
// out so no overlay is left open. Only fatal Telegram errors are returned.
func (i *inbound) Fire(ctx context.Context, key domain.Key) error {
	f, ok := i.pending[key]
	if !ok {
		i.log.Debug("command follow-up without pending", slog.String("key", key.String()))
		return nil
	}
	delete(i.pending, key)
	entry, hasTopic := i.topics.Entry(key)
	_, alive := i.agents(key)
	if !alive || !hasTopic || !entry.Status.Live() {
		i.log.Debug("command follow-up skipped", slog.String("key", key.String()), slog.String("word", f.word),
			slog.Bool("alive", alive), slog.Bool("topic", hasTopic && entry.Status.Live()))
		if f.cmd.Forward.Dismiss && alive {
			return i.dismiss(ctx, key, f)
		}
		return nil
	}
	lines := 0
	if f.cmd.Forward.Post == domain.ForwardPostTail {
		lines = commandTailLines
	}
	screen, err := i.herdr.ReadScreen(ctx, key.PaneID, domain.ScreenVisible, lines)
	if err != nil {
		i.log.Warn("command screen read failed", slog.String("key", key.String()), slog.String("word", f.word), slog.String("err", err.Error()))
		if err := i.reply(ctx, f.threadID, f.messageID, "⚠️ screen read failed: "+failureReason(err)); err != nil {
			return err
		}
	} else {
		text := trimScreen(screen.Text)
		if f.cmd.Forward.Post == domain.ForwardPostScreen && f.cmd.Forward.Dismiss {
			text = trimScreen(domain.CutOverlay(text))
		}
		if text == "" {
			text = "(screen is empty)"
		}
		if err := i.absorb(i.send(ctx, domain.Outgoing{ThreadID: entry.ThreadID, Text: text, Code: true, ReplyTo: f.messageID})); err != nil {
			return err
		}
		i.log.Info("command screen posted", slog.String("key", key.String()), slog.String("word", f.word),
			slog.Int("thread_id", entry.ThreadID), slog.Int("lines", strings.Count(text, "\n")+1),
			slog.Int("bytes", len(text)), slog.Bool("dismiss", f.cmd.Forward.Dismiss))
	}
	if !f.cmd.Forward.Dismiss {
		return nil
	}
	return i.dismiss(ctx, key, f)
}

// dismiss closes the overlay a forwarded command opened.
func (i *inbound) dismiss(ctx context.Context, key domain.Key, f followUp) error {
	if err := i.herdr.SendKeys(ctx, key.PaneID, []string{"esc"}); err != nil {
		i.log.Warn("command dismiss failed", slog.String("key", key.String()), slog.String("word", f.word), slog.String("err", err.Error()))
		return i.reply(ctx, f.threadID, f.messageID, "⚠️ could not close the overlay: "+failureReason(err))
	}
	i.log.Debug("command overlay dismissed", slog.String("key", key.String()), slog.String("word", f.word))
	return nil
}

// HandleGeneral answers a command written in the General topic.
func (i *inbound) HandleGeneral(ctx context.Context, cmd domain.GeneralCommand) error {
	parsed := domain.ParseCommand(cmd.Text, i.botUsername)
	i.log.Info("general command", slog.String("kind", string(parsed.Kind)), slog.Int64("from_id", cmd.FromID), slog.Int("message_id", cmd.MessageID))
	switch parsed.Kind {
	case domain.CmdStatus:
		text := i.statusSummary()
		return i.absorb(i.send(ctx, domain.Outgoing{ThreadID: 0, Text: text, HTML: true, ReplyTo: cmd.MessageID}))
	case domain.CmdHelp:
		return i.reply(ctx, 0, cmd.MessageID, helpText)
	case domain.CmdOptions:
		return i.panel.Open(ctx, cmd)
	case domain.CmdAway:
		return i.reply(ctx, 0, cmd.MessageID, i.away(parsed.Away, cmd.FromID))
	case domain.CmdHere:
		return i.reply(ctx, 0, cmd.MessageID, i.here(cmd.FromID))
	case domain.CmdStop, domain.CmdInterrupt, domain.CmdClose:
		return i.reply(ctx, 0, cmd.MessageID, topicOnly)
	case domain.CmdNew:
		return i.startAgent(ctx, cmd, parsed)
	default:
		return i.reply(ctx, 0, cmd.MessageID, "unknown command, see /help")
	}
}

// startAgent serves /new: resolves the workspace, opens a tab in it and
// hands the slow agent.start to the background; the outcome arrives as a
// startResult job and StartFinished words it. Only fatal Telegram errors
// are returned.
func (i *inbound) startAgent(ctx context.Context, cmd domain.GeneralCommand, parsed domain.Command) error {
	i.log.Info("agent start requested", slog.Int64("from_id", cmd.FromID), slog.Int("message_id", cmd.MessageID),
		slog.String("workspace", parsed.Workspace), slog.String("kind", parsed.AgentKind))
	workspaces, err := i.herdr.ListWorkspaces(ctx)
	if err != nil {
		i.log.Warn("workspace list failed", slog.String("err", err.Error()))
		return i.reply(ctx, 0, cmd.MessageID, newListFailed+failureReason(err))
	}
	ws, match := domain.MatchWorkspace(parsed.Workspace, workspaces)
	i.log.Debug("workspace match", slog.String("label", parsed.Workspace), slog.Int("result", int(match)),
		slog.Any("candidates", workspaceLabels(domain.MatchWorkspaces(parsed.Workspace, workspaces))))
	if match != domain.MatchOne {
		return i.reply(ctx, 0, cmd.MessageID, workspaceHint(parsed.Workspace, match, workspaces))
	}
	kind := parsed.AgentKind
	if kind == "" {
		kind = domain.DefaultAgentKind
	}
	tab, err := i.herdr.CreateTab(ctx, ws.ID)
	if err != nil {
		i.log.Warn("tab create failed", slog.String("workspace_id", ws.ID), slog.String("err", err.Error()))
		return i.reply(ctx, 0, cmd.MessageID, newTabFailedPrefix+failureReason(err))
	}
	i.log.Info("tab created", slog.String("workspace_id", ws.ID), slog.String("workspace", ws.Label),
		slog.String("tab_id", tab.ID), slog.String("pane", tab.RootPaneID))
	name := i.uniqueName(kind)
	if err := i.reply(ctx, 0, cmd.MessageID, fmt.Sprintf(newStartingFmt, kind, ws.Label)); err != nil {
		return err
	}
	started := i.clock.Now()
	i.async(func(ctx context.Context) startResult {
		agent, err := i.herdr.StartAgent(ctx, name, kind, tab.RootPaneID, agentStartTimeout)
		return startResult{
			messageID: cmd.MessageID, workspace: ws.Label, kind: kind, name: name,
			paneID: tab.RootPaneID, agent: agent, err: err, started: started,
		}
	})
	return nil
}

// StartFinished words the outcome of a background agent start as a reply
// to the /new message.
func (i *inbound) StartFinished(ctx context.Context, r startResult) error {
	elapsed := i.clock.Now().Sub(r.started).Milliseconds()
	if r.err != nil {
		i.log.Warn("agent start failed", slog.String("pane", r.paneID), slog.String("kind", r.kind),
			slog.String("name", r.name), slog.Int64("elapsed_ms", elapsed), slog.String("err", r.err.Error()))
		return i.reply(ctx, 0, r.messageID, fmt.Sprintf(newFailedFmt, r.kind, r.workspace, failureReason(r.err)))
	}
	pane := r.agent.PaneID
	if pane == "" {
		pane = r.paneID
	}
	i.log.Info("agent started", slog.String("pane", pane), slog.String("kind", r.kind), slog.String("name", r.name),
		slog.String("workspace", r.workspace), slog.Int64("elapsed_ms", elapsed))
	return i.reply(ctx, 0, r.messageID, fmt.Sprintf(newStartedFmt, r.kind, r.workspace, pane))
}

// uniqueName returns kind when no live agent carries that name, else the
// first of kind-2, kind-3 … that is free.
func (i *inbound) uniqueName(kind string) string {
	taken := map[string]bool{}
	for _, a := range i.live() {
		taken[a.Name] = true
	}
	name := kind
	for n := 2; taken[name]; n++ {
		name = fmt.Sprintf("%s-%d", kind, n)
	}
	return name
}

// workspaceHint words a /new that named no single workspace: the list of
// labels, prefixed with why the text did not match.
func workspaceHint(label string, match domain.MatchResult, workspaces []domain.Workspace) string {
	list := newNoWorkspaces
	if len(workspaces) > 0 {
		list = fmt.Sprintf(newWorkspacesFmt, strings.Join(workspaceLabels(workspaces), ", "))
	}
	switch {
	case strings.TrimSpace(label) == "":
		return list
	case match == domain.MatchMany:
		return fmt.Sprintf(newManyFmt, label, joinAnd(workspaceLabels(domain.MatchWorkspaces(label, workspaces)))) + "\n" + list
	}
	return fmt.Sprintf(newNoMatchFmt, label) + "\n" + list
}

// workspaceLabels lists labels in Herdr's order; a workspace without a
// label shows its id.
func workspaceLabels(workspaces []domain.Workspace) []string {
	out := make([]string, 0, len(workspaces))
	for _, ws := range workspaces {
		label := ws.Label
		if label == "" {
			label = ws.ID
		}
		out = append(out, label)
	}
	return out
}

// joinAnd renders "A", "A and B" or "A, B and C".
func joinAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// away applies /away and words the reply.
func (i *inbound) away(d time.Duration, by int64) string {
	switch {
	case i.presence == nil:
		return presenceUnavailable
	case !i.opts.QuietEnabled():
		return presenceOff
	}
	st := i.presence.Away(d, by)
	i.log.Debug("away applied", slog.Int64("by", by), slog.Time("until", st.Until), slog.String("word", st.Word()))
	if st.Until.IsZero() {
		return presenceAwayOpen
	}
	return fmt.Sprintf(presenceAwayUntil, i.clockTime(st.Until))
}

// here applies /here and words the automatic verdict.
func (i *inbound) here(by int64) string {
	switch {
	case i.presence == nil:
		return presenceUnavailable
	case !i.opts.QuietEnabled():
		return presenceOff
	}
	st := i.presence.Here(by)
	i.log.Debug("here applied", slog.Int64("by", by), slog.String("word", st.Word()))
	verdict := presenceVerdictAway
	switch {
	case !st.Supported:
		verdict = presenceVerdictNone
	case st.Quiet:
		verdict = presenceVerdictDesk
	}
	return fmt.Sprintf(presenceHereFmt, verdict)
}

// presenceHeader is the /status line about quiet mode, empty when there is
// nothing to say (quiet off, or away by the automatic verdict).
func (i *inbound) presenceHeader() string {
	if i.presence == nil || !i.opts.QuietEnabled() {
		return ""
	}
	st := i.presence.State()
	switch {
	case st.ManualAway && st.Until.IsZero():
		return presenceHeaderOpen
	case st.ManualAway:
		return fmt.Sprintf(presenceHeaderManual, i.clockTime(st.Until))
	case st.Quiet:
		return presenceHeaderQuiet
	}
	return ""
}

// clockTime renders an instant as wall-clock time in the daemon clock's
// location (local time on a real clock).
func (i *inbound) clockTime(at time.Time) string {
	return at.In(i.clock.Now().Location()).Format(presenceTimeLayout)
}

// SetPresence wires the presence tracker for /away, /here and /status.
func (i *inbound) SetPresence(p *Presence) { i.presence = p }

// PressPanel serves a button of the options panel (callback data with the
// panel prefix); the bridge routes such presses here.
func (i *inbound) PressPanel(ctx context.Context, ev domain.ButtonPressed) error {
	return i.panel.Press(ctx, ev)
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
	header := ""
	if !i.opts.SyncEnabled() {
		header = "🔇 Herdr → Telegram sync is off (/options)\n"
	}
	header += i.presenceHeader()
	if len(live) == 0 {
		return header + "no agents"
	}
	sort.Slice(live, func(a, b int) bool {
		if live[a].Label() != live[b].Label() {
			return live[a].Label() < live[b].Label()
		}
		return live[a].Key.String() < live[b].Key.String()
	})
	icons := i.opts.StatusIcons()
	lines := make([]string, 0, len(live)+1)
	lines = append(lines, header+plural(len(live), "agent"))
	for _, a := range live {
		label := html.EscapeString(a.Label())
		if entry, ok := i.topics.Entry(a.Key); ok && entry.Status.Live() {
			label = fmt.Sprintf(`<a href="%s">%s</a>`, topicLink(i.chatID, entry.ThreadID), label)
		}
		lines = append(lines, icons.For(a.Status)+" "+label)
	}
	return strings.Join(lines, "\n")
}

// herdrCall runs one Herdr call for an operator message; only a failure
// is reported back, as a quoted reply.
func (i *inbound) herdrCall(ctx context.Context, msg domain.TopicMessage, key domain.Key, method string, call func(context.Context) error) error {
	if err := call(ctx); err != nil {
		return i.failed(ctx, msg, key, method, err)
	}
	i.log.Debug("herdr call ok", slog.String("method", method), slog.String("key", key.String()), slog.Int("message_id", msg.MessageID))
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
	return i.absorb(i.send(ctx, domain.Outgoing{ThreadID: threadID, Text: text, ReplyTo: messageID}))
}

// send posts one message and drops the message id, which replies and
// command screens never need.
func (i *inbound) send(ctx context.Context, out domain.Outgoing) error {
	_, err := i.tg.Send(ctx, out)
	return err
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
