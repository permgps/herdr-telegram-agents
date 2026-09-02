package domain

import (
	"sort"
	"strconv"
	"strings"
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
	// CmdForward types the slash line into the agent as one of its own
	// commands (Claude Code /clear, /compact, /usage, /model); Text holds
	// the exact line and Forward says what to do once it has run.
	CmdForward CommandKind = "forward"
	// CmdUnknown is a slash word the plugin does not know; Text holds it.
	CmdUnknown CommandKind = "unknown"
)

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
}

// shortReplies maps what an operator types while an agent is blocked to the
// Herdr key name that answers the dialog. Digits 1..9 are handled in code.
var shortReplies = map[string]string{
	"y":      "y",
	"yes":    "y",
	"n":      "n",
	"no":     "n",
	"enter":  "enter",
	"ok":     "enter",
	"esc":    "esc",
	"escape": "esc",
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
