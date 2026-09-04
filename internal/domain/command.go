package domain

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// CommandKind names what an operator asked for in a topic or in General.
type CommandKind string

const (
	// CmdPrompt types the text into the agent and submits it.
	CmdPrompt CommandKind = "prompt"
	// CmdKeys sends raw key names to the agent.
	CmdKeys CommandKind = "keys"
	// CmdScreen posts the agent's screen; Lines 0 means the whole visible
	// screen, otherwise the last Lines lines; All means the output since
	// the last human message.
	CmdScreen CommandKind = "screen"
	// CmdFocus brings the agent's pane to the front in Herdr.
	CmdFocus CommandKind = "focus"
	// CmdStatus answers with the agent status (topic) or a summary of every
	// agent (General).
	CmdStatus CommandKind = "status"
	// CmdHelp prints the command list.
	CmdHelp CommandKind = "help"
	// CmdOptions opens the settings panel; only General serves it, a
	// topic answers with a hint.
	CmdOptions CommandKind = "options"
	// CmdAway treats the operator as away (General only): quiet mode is
	// off until /here, or for Command.Away when it is non-zero.
	CmdAway CommandKind = "away"
	// CmdHere returns presence to the automatic verdict (General only).
	CmdHere CommandKind = "here"
	// CmdForward types the slash line into the agent as one of its own
	// commands (Claude Code /clear, /compact, /usage, /model); Text holds
	// the exact line and Forward says what to do once it has run.
	CmdForward CommandKind = "forward"
	// CmdStop sends Escape to the agent: a soft cancel of the running turn
	// or of an open dialog (topic only).
	CmdStop CommandKind = "stop"
	// CmdInterrupt sends Ctrl-C to the agent: a hard interrupt (topic only).
	CmdInterrupt CommandKind = "interrupt"
	// CmdClose closes the agent's pane after a confirmation (topic only).
	CmdClose CommandKind = "close"
	// CmdNew starts an agent in a new tab of a workspace (General only);
	// Workspace is the label typed after the word and AgentKind the kind.
	CmdNew CommandKind = "new"
	// CmdUnknown is a slash word the plugin does not know; Text holds it.
	CmdUnknown CommandKind = "unknown"
)

// Key names the control commands send through agent.send_keys.
const (
	// KeyEscape is Herdr's canonical name for Escape (`herdr agent
	// send-keys --help`).
	KeyEscape = "esc"
	// KeyInterrupt is Ctrl-C. The spelling comes from herdr-remote's live
	// check against Herdr 0.8.0, which accepts "+"-joined chords; the live
	// check of plan feature-agent-control-and-hygiene verifies it against
	// 0.7.5 (the tmux spelling "C-c" is the fallback).
	KeyInterrupt = "ctrl+c"
)

// DefaultAgentKind is what /new starts when no kind is given.
const DefaultAgentKind = "claude"

// AgentKinds lists the kinds `herdr agent start` accepts (Herdr 0.7.5, from
// `herdr agent start --help`). /new treats its last word as a kind only when
// it is in this list, so a workspace label may end in any other word.
var AgentKinds = []string{
	"pi", "claude", "codex", "gemini", "cursor", "devin", "agy", "cline",
	"omp", "mastracode", "opencode", "copilot", "kimi", "kiro", "droid",
	"amp", "grok", "hermes", "kilo", "qodercli", "maki",
}

// IsAgentKind reports whether s names a kind in AgentKinds
// (case-insensitive).
func IsAgentKind(s string) bool {
	for _, k := range AgentKinds {
		if strings.EqualFold(k, s) {
			return true
		}
	}
	return false
}

// MatchResult says how a typed workspace label matched Herdr's list.
type MatchResult int

const (
	// MatchNone means no workspace label equals or starts with the text.
	MatchNone MatchResult = iota
	// MatchOne means exactly one workspace matched.
	MatchOne
	// MatchMany means several labels start with the text and none equals it.
	MatchMany
)

// MatchWorkspaces returns the workspaces a typed label selects: the ones
// whose trimmed label equals the trimmed text (case-insensitive) when any
// does, else every workspace whose label starts with it. A workspace
// without a label is matched by its id. An empty text matches nothing.
func MatchWorkspaces(label string, workspaces []Workspace) []Workspace {
	want := strings.ToLower(strings.TrimSpace(label))
	if want == "" {
		return nil
	}
	var exact, prefixed []Workspace
	for _, ws := range workspaces {
		have := strings.ToLower(strings.TrimSpace(ws.Label))
		if have == "" {
			have = strings.ToLower(ws.ID)
		}
		switch {
		case have == want:
			exact = append(exact, ws)
		case strings.HasPrefix(have, want):
			prefixed = append(prefixed, ws)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return prefixed
}

// MatchWorkspace picks the workspace a typed label means: an exact match
// (case-insensitive, whitespace trimmed) wins, else a unique prefix; several
// prefix matches are MatchMany and no match is MatchNone.
func MatchWorkspace(label string, workspaces []Workspace) (Workspace, MatchResult) {
	found := MatchWorkspaces(label, workspaces)
	switch len(found) {
	case 0:
		return Workspace{}, MatchNone
	case 1:
		return found[0], MatchOne
	}
	return Workspace{}, MatchMany
}

// MaxScreenLines caps the line count an operator may request with /screen.
const MaxScreenLines = 200

// ForwardPost selects what the bridge posts after a forwarded command has
// been typed and the settle delay has passed.
type ForwardPost string

const (
	// ForwardPostNone posts nothing; the result shows up through the
	// agent status (used for /compact).
	ForwardPostNone ForwardPost = "none"
	// ForwardPostTail posts the last few lines of the visible screen.
	ForwardPostTail ForwardPost = "tail"
	// ForwardPostScreen posts the visible screen, cut to the overlay the
	// command opened when one is recognisable.
	ForwardPostScreen ForwardPost = "screen"
)

// ForwardRule describes the follow-up of one forwarded command.
type ForwardRule struct {
	Post ForwardPost
	// Dismiss sends esc after the screen was read because the command
	// left an overlay open (/usage, the /model picker).
	Dismiss bool
}

// forwardRules maps the Claude Code commands an operator may send from a
// topic to their follow-up. A word missing here stays an unknown command,
// so a typo in a plugin command never reaches the agent as a prompt.
var forwardRules = map[string]ForwardRule{
	"clear":   {Post: ForwardPostTail},
	"compact": {Post: ForwardPostNone},
	"usage":   {Post: ForwardPostScreen, Dismiss: true},
	"model":   {Post: ForwardPostScreen, Dismiss: true},
}

// forwardRuleFor resolves the rule for a slash word; /model with a name
// sets the model outright instead of opening the picker, so it only needs
// the tail.
func forwardRuleFor(word string, hasArgs bool) (ForwardRule, bool) {
	rule, ok := forwardRules[word]
	if !ok {
		return ForwardRule{}, false
	}
	if word == "model" && hasArgs {
		return ForwardRule{Post: ForwardPostTail}, true
	}
	return rule, true
}

// ForwardWords lists the forwarded command words in a stable order.
func ForwardWords() []string {
	words := make([]string, 0, len(forwardRules))
	for w := range forwardRules {
		words = append(words, w)
	}
	sort.Strings(words)
	return words
}

// overlayRuleMinRunes is the shortest run of ▔ that counts as the line
// separating the transcript from an overlay Claude Code drew below it.
const overlayRuleMinRunes = 8

// CutOverlay returns the lines after the last full-width ▔ rule in a
// screen, which is where Claude Code draws its /usage panel and /model
// picker; text without such a rule is returned unchanged.
func CutOverlay(text string) string {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if isOverlayRule(lines[i]) {
			return strings.Join(lines[i+1:], "\n")
		}
	}
	return text
}

func isOverlayRule(line string) bool {
	line = strings.TrimSpace(line)
	n := 0
	for _, r := range line {
		if r != '▔' {
			return false
		}
		n++
	}
	return n >= overlayRuleMinRunes
}

// Command is one parsed operator instruction.
type Command struct {
	Kind CommandKind
	// Text is the prompt text for CmdPrompt and the unknown word (with its
	// slash) for CmdUnknown.
	Text string
	// Keys is the key list for CmdKeys.
	Keys []string
	// Lines is the requested line count for CmdScreen; 0 means the full
	// visible screen.
	Lines int
	// All asks CmdScreen for the output since the last human message
	// instead of the visible screen ("/screen all").
	All bool
	// Forward is the follow-up rule for CmdForward.
	Forward ForwardRule
	// Away is how long CmdAway lasts; zero means until /here.
	Away time.Duration
	// Workspace is the label typed after /new, inner spaces kept; empty
	// when the operator gave none (the reply lists the workspaces).
	Workspace string
	// AgentKind is the kind /new starts: the last word when it is one of
	// AgentKinds, else DefaultAgentKind. Empty for a bare /new.
	AgentKind string
}

// Bounds of the /away duration argument.
const (
	MinAway = time.Minute
	MaxAway = 7 * 24 * time.Hour
)

// shortReplies maps what an operator types while an agent is blocked to the
// Herdr key name that answers the dialog. Digits 1..9 are handled in code.
var shortReplies = map[string]string{
	"y":      "y",
	"yes":    "y",
	"n":      "n",
	"no":     "n",
	"enter":  "enter",
	"ok":     "enter",
	"esc":    KeyEscape,
	"escape": KeyEscape,
}

// ParseCommand turns an operator message into a Command. Text that does not
// start with a slash is a prompt, passed through as typed. A slash word may
// carry the "@botUsername" suffix Telegram adds in groups; it is stripped when
// it matches botUsername (case-insensitive) or when botUsername is empty.
// A suffix naming another bot yields CmdUnknown.
func ParseCommand(text, botUsername string) Command {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "/") {
		return Command{Kind: CmdPrompt, Text: text}
	}
	fields := strings.Fields(trimmed)
	word := strings.TrimPrefix(fields[0], "/")
	if at := strings.IndexByte(word, '@'); at >= 0 {
		suffix := word[at+1:]
		word = word[:at]
		if botUsername != "" && !strings.EqualFold(suffix, botUsername) {
			return Command{Kind: CmdUnknown, Text: "/" + word + "@" + suffix}
		}
	}
	word = strings.ToLower(word)
	args := fields[1:]
	// rest is the message after the slash word with its inner spacing kept,
	// so a forwarded /compact instruction reaches the agent as typed.
	rest := strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
	switch word {
	case "screen":
		return parseScreen(args)
	case "keys":
		if len(args) == 0 {
			return Command{Kind: CmdUnknown, Text: "/keys"}
		}
		return Command{Kind: CmdKeys, Keys: append([]string(nil), args...)}
	case "focus":
		return Command{Kind: CmdFocus}
	case "status":
		return Command{Kind: CmdStatus}
	case "help":
		return Command{Kind: CmdHelp}
	case "options":
		return Command{Kind: CmdOptions}
	case "away":
		return parseAway(args)
	case "here":
		return bareCommand(CmdHere, word, args)
	case "stop":
		return bareCommand(CmdStop, word, args)
	case "interrupt":
		return bareCommand(CmdInterrupt, word, args)
	case "close":
		return bareCommand(CmdClose, word, args)
	case "new":
		return parseNew(args)
	}
	if rule, ok := forwardRuleFor(word, rest != ""); ok {
		line := "/" + word
		if rest != "" {
			line += " " + rest
		}
		return Command{Kind: CmdForward, Text: line, Forward: rule}
	}
	return Command{Kind: CmdUnknown, Text: "/" + word}
}

// bareCommand is the shape of a command that takes no argument: with
// arguments the full line is CmdUnknown, so a typo never reaches the agent.
func bareCommand(kind CommandKind, word string, args []string) Command {
	if len(args) > 0 {
		return Command{Kind: CmdUnknown, Text: "/" + word + " " + strings.Join(args, " ")}
	}
	return Command{Kind: kind}
}

// parseNew reads "/new <workspace> [kind]": the last word is the kind when
// it is in AgentKinds, the rest (joined by single spaces) is the workspace
// label; a bare /new carries neither, so the caller lists the workspaces.
func parseNew(args []string) Command {
	if len(args) == 0 {
		return Command{Kind: CmdNew}
	}
	cmd := Command{Kind: CmdNew, AgentKind: DefaultAgentKind}
	if last := args[len(args)-1]; IsAgentKind(last) {
		cmd.AgentKind = strings.ToLower(last)
		args = args[:len(args)-1]
	}
	cmd.Workspace = strings.Join(args, " ")
	return cmd
}

// parseAway accepts no argument (away until /here) or one Go duration such
// as 2h, 30m or 1h30m between MinAway and MaxAway.
func parseAway(args []string) Command {
	if len(args) == 0 {
		return Command{Kind: CmdAway}
	}
	unknown := Command{Kind: CmdUnknown, Text: "/away " + strings.Join(args, " ")}
	if len(args) > 1 {
		return unknown
	}
	d, err := time.ParseDuration(strings.ToLower(args[0]))
	if err != nil || d < MinAway || d > MaxAway {
		return unknown
	}
	return Command{Kind: CmdAway, Away: d}
}

func parseScreen(args []string) Command {
	if len(args) == 0 {
		return Command{Kind: CmdScreen}
	}
	if strings.EqualFold(args[0], "all") {
		if len(args) > 1 {
			return Command{Kind: CmdUnknown, Text: "/screen " + strings.Join(args, " ")}
		}
		return Command{Kind: CmdScreen, All: true}
	}
	n, err := strconv.Atoi(args[0])
	if err != nil {
		return Command{Kind: CmdUnknown, Text: "/screen " + args[0]}
	}
	if n < 1 {
		n = 1
	}
	if n > MaxScreenLines {
		n = MaxScreenLines
	}
	return Command{Kind: CmdScreen, Lines: n}
}

// ShortReply recognises the answers an operator gives to a blocked agent's
// dialog: y, n, yes, no, 1..9, enter, ok, esc, escape (case-insensitive,
// surrounding whitespace ignored). It returns the key list for SendKeys.
func ShortReply(text string) ([]string, bool) {
	t := strings.ToLower(strings.TrimSpace(text))
	if len(t) == 1 && t[0] >= '1' && t[0] <= '9' {
		return []string{t}, true
	}
	if key, ok := shortReplies[t]; ok {
		return []string{key}, true
	}
	return nil, false
}

// Route decides what a topic message means for an agent in the given status:
// a slash word is parsed as a command; a short reply to a blocked agent is a
// key press; everything else is a prompt.
func Route(text, botUsername string, status Status) Command {
	if strings.HasPrefix(strings.TrimSpace(text), "/") {
		return ParseCommand(text, botUsername)
	}
	if status == StatusBlocked {
		if keys, ok := ShortReply(text); ok {
			return Command{Kind: CmdKeys, Keys: keys}
		}
	}
	return Command{Kind: CmdPrompt, Text: text}
}
