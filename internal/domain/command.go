package domain

import (
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
	// screen, otherwise the last Lines lines.
	CmdScreen CommandKind = "screen"
	// CmdFocus brings the agent's pane to the front in Herdr.
	CmdFocus CommandKind = "focus"
	// CmdStatus answers with the agent status (topic) or a summary of every
	// agent (General).
	CmdStatus CommandKind = "status"
	// CmdHelp prints the command list.
	CmdHelp CommandKind = "help"
	// CmdUnknown is a slash word the plugin does not know; Text holds it.
	CmdUnknown CommandKind = "unknown"
)

// MaxScreenLines caps the line count an operator may request with /screen.
const MaxScreenLines = 200

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
	return Command{Kind: CmdUnknown, Text: "/" + word}
}

func parseScreen(args []string) Command {
	if len(args) == 0 {
		return Command{Kind: CmdScreen}
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
