package domain

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Choice is one selectable option in a numbered dialog an agent drew on its
// screen (a Claude Code question, a tool-approval prompt, a picker). Number
// is the digit the agent expects as the answer; Label is the option text
// without its number.
type Choice struct {
	Number int
	Label  string
}

// MaxChoiceButtons is the most options a dialog may have and still get one
// inline button per option; longer dialogs are answered by typing a digit.
const MaxChoiceButtons = 5

const (
	// minChoices is the fewest real options that count as a dialog; a lone
	// "1." is more likely a list fragment in the transcript.
	minChoices = 2
	// choiceRuleMinRunes is the shortest run of rule characters that counts
	// as a separator line between the transcript and the dialog.
	choiceRuleMinRunes = 8
)

// choiceServiceLabels are Claude Code's own entries in a question dialog;
// they are numbered like options but never become buttons: a text reply
// covers "Type something." and a digit reply still reaches "Chat about
// this".
var choiceServiceLabels = map[string]bool{
	"Type something.": true,
	"Chat about this": true,
}

// choiceItem matches "1. Label", with the optional ❯ cursor Claude Code
// puts before the highlighted option.
var choiceItem = regexp.MustCompile(`^\s*(?:❯\s*)?([1-9])\.\s+(\S.*)$`)

// ParseChoices finds the numbered dialog at the bottom of a screen and
// returns its real options, or nil when the screen ends in no such dialog.
//
// The dialog is the bottom-most block whose first item is "1."; its items
// must be numbered 1, 2, 3, … without gaps, and between and after them only
// blank lines, rules, indented description lines and dialog footers may
// appear. Anything else after the block (a "(y/n)" prompt, transcript
// text) means the numbers were a list in the transcript, not a dialog.
// Service entries are dropped from the result but keep their numbers on
// the remaining options, so the digit sent for an option is always the
// one the agent shows.
func ParseChoices(screen string) []Choice {
	lines := strings.Split(strings.ReplaceAll(screen, "\r\n", "\n"), "\n")
	start := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if n, _, ok := parseChoiceLine(lines[i]); ok && n == 1 {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	var items []Choice
	for _, line := range lines[start:] {
		if n, label, ok := parseChoiceLine(line); ok {
			if n != len(items)+1 {
				return nil
			}
			items = append(items, Choice{Number: n, Label: label})
			continue
		}
		if !isChoiceFiller(line) {
			return nil
		}
	}
	choices := items[:0:0]
	for _, c := range items {
		if !choiceServiceLabels[c.Label] {
			choices = append(choices, c)
		}
	}
	if len(choices) < minChoices || len(choices) > MaxChoiceButtons {
		return nil
	}
	return choices
}

// parseChoiceLine reads one "N. Label" line.
func parseChoiceLine(line string) (int, string, bool) {
	m := choiceItem.FindStringSubmatch(strings.TrimRight(line, " \t\r"))
	if m == nil {
		return 0, "", false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, "", false
	}
	return n, strings.TrimSpace(m[2]), true
}

// isChoiceFiller reports whether a non-item line may sit inside or after
// the dialog block: blank, a rule, an indented description or a footer
// such as "Enter to select · ↑/↓ to navigate · Esc to cancel".
func isChoiceFiller(line string) bool {
	trimmed := strings.TrimSpace(line)
	switch {
	case trimmed == "":
		return true
	case isChoiceRule(trimmed):
		return true
	case strings.HasPrefix(line, "    "):
		return true
	case strings.Contains(trimmed, " · "):
		return true
	case strings.HasPrefix(trimmed, "Enter "), strings.HasPrefix(trimmed, "Esc "):
		return true
	}
	return false
}

// isChoiceRule reports whether a trimmed line is a horizontal rule drawn
// with box-drawing characters.
func isChoiceRule(trimmed string) bool {
	if utf8.RuneCountInString(trimmed) < choiceRuleMinRunes {
		return false
	}
	for _, r := range trimmed {
		switch r {
		case '─', '▔', '━', '═':
		default:
			return false
		}
	}
	return true
}
