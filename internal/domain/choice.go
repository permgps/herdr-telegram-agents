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

// Dialog is the numbered dialog at the bottom of a blocked screen: its real
// options and two facts about the whole dialog. Multi is set when every
// option starts with a checkbox glyph, meaning the agent toggles an option
// per digit and submits with enter. TextEntry is the number of the entry
// that opens a free-text answer ("Type something.", else "Chat about
// this"), or 0 when the dialog has neither. The zero Dialog means the
// screen ends in no dialog at all.
type Dialog struct {
	Choices   []Choice
	Multi     bool
	TextEntry int
	// TextLabel is the text of the TextEntry entry without its trailing
	// period ("Type something", "Chat about this"); empty without one.
	TextLabel string
	// Cursor is the navigable row the ❯ cursor sits on, counting every
	// numbered entry and the bare "Submit" row from 1 in screen order; 0
	// when no cursor is drawn. SubmitRow is the row of the bare "Submit"
	// line a Claude Code multi-select dialog ends its entries with, or 0
	// when the renderer submits on enter.
	Cursor    int
	SubmitRow int
}

// Key names Herdr accepts for the arrows and enter.
const (
	KeyEnter = "enter"
	KeyDown  = "down"
	KeyUp    = "up"
)

// submitLabel is the bare row that submits a Claude Code multi-select
// dialog, drawn indented under the last entry.
const submitLabel = "Submit"

// SubmitKeys returns the keys that submit a multi-select dialog. Without a
// Submit row the renderer submits on enter. With one (Claude Code,
// measured 2026-09-06: a digit toggles its option without moving the
// cursor, enter toggles the row under the cursor) the arrows walk from
// the cursor row, or row 1 when none is drawn, to the Submit row, then
// enter.
func (d Dialog) SubmitKeys() []string {
	if d.SubmitRow == 0 {
		return []string{KeyEnter}
	}
	cursor := d.Cursor
	if cursor == 0 {
		cursor = 1
	}
	var keys []string
	for ; cursor < d.SubmitRow; cursor++ {
		keys = append(keys, KeyDown)
	}
	for ; cursor > d.SubmitRow; cursor-- {
		keys = append(keys, KeyUp)
	}
	return append(keys, KeyEnter)
}

// Service entries Claude Code adds to a question dialog: numbered like
// options but never buttons of their own. ServiceTypeSomething opens a
// free-text answer ("Type something." in a single-select dialog, "[ ] Type
// something" in a multi-select one); ServiceChatAboutThis hands the
// question back to the conversation. Both count for Dialog.TextEntry, the
// first wins. Compared without the trailing period and the checkbox.
const (
	ServiceTypeSomething = "Type something"
	ServiceChatAboutThis = "Chat about this"
)

// choiceServiceLabels lists the service entries in the order of preference
// for Dialog.TextEntry. Labels are compared after stripCheckbox and without
// a trailing period: a multi-select dialog draws "4. [ ] Type something".
var choiceServiceLabels = []string{ServiceTypeSomething, ServiceChatAboutThis}

// checkboxGlyph matches the marker a multi-select option starts with:
// square brackets around one or two runes ("[ ]" empty, "[x]", and the
// "[✔]" Claude Code draws once an option is toggled, with or without a
// variation selector — screens read live on 2026-09-06), or the bare
// ☐ / ☑ / ☒ pair of other renderers.
var checkboxGlyph = regexp.MustCompile(`^(?:\[[^\[\]\s]{1,2}\]|\[ \]|[☐☑☒])\s+`)

// choiceItem matches "1. Label", with the optional ❯ cursor Claude Code
// puts before the highlighted option.
var choiceItem = regexp.MustCompile(`^\s*(?:❯\s*)?([1-9])\.\s+(\S.*)$`)

// ParseChoices returns the real options of ParseDialog; kept for callers
// that need nothing but the buttons.
func ParseChoices(screen string) []Choice {
	return ParseDialog(screen).Choices
}

// ParseDialog finds the numbered dialog at the bottom of a screen and
// returns its real options with the multi-select and free-text facts, or
// the zero Dialog when the screen ends in no such dialog.
//
// The dialog is the bottom-most block whose first item is "1."; its items
// must be numbered 1, 2, 3, … without gaps, and between and after them only
// blank lines, rules, indented description lines and dialog footers may
// appear. Anything else after the block (a "(y/n)" prompt, transcript
// text) means the numbers were a list in the transcript, not a dialog.
// Service entries are dropped from the options but keep their numbers on
// the remaining ones, so the digit sent for an option is always the one
// the agent shows; the free-text entry's number is kept as TextEntry.
func ParseDialog(screen string) Dialog {
	lines := strings.Split(strings.ReplaceAll(screen, "\r\n", "\n"), "\n")
	start := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if n, _, ok := parseChoiceLine(lines[i]); ok && n == 1 {
			start = i
			break
		}
	}
	if start < 0 {
		return Dialog{}
	}
	var items []Choice
	rows, cursor, submitRow := 0, 0, 0
	for _, line := range lines[start:] {
		if n, label, ok := parseChoiceLine(line); ok {
			if n != len(items)+1 {
				return Dialog{}
			}
			items = append(items, Choice{Number: n, Label: label})
			rows++
			if hasCursor(line) {
				cursor = rows
			}
			continue
		}
		if isSubmitRow(line) {
			rows++
			submitRow = rows
			if hasCursor(line) {
				cursor = rows
			}
			continue
		}
		if !isChoiceFiller(line) {
			return Dialog{}
		}
	}
	d := Dialog{Choices: items[:0:0], Cursor: cursor, SubmitRow: submitRow}
	bestRank := len(choiceServiceLabels)
	for _, c := range items {
		plain := strings.TrimSuffix(stripCheckbox(c.Label), ".")
		if rank := serviceRank(plain); rank >= 0 {
			if rank < bestRank {
				bestRank = rank
				d.TextEntry = c.Number
				d.TextLabel = plain
			}
			continue
		}
		d.Choices = append(d.Choices, c)
	}
	if len(d.Choices) < minChoices || len(d.Choices) > MaxChoiceButtons {
		return Dialog{}
	}
	d.Multi = allCheckboxes(d.Choices)
	return d
}

// serviceRank is the index of label in choiceServiceLabels, -1 for a real
// option.
func serviceRank(label string) int {
	for i, s := range choiceServiceLabels {
		if s == label {
			return i
		}
	}
	return -1
}

// allCheckboxes reports whether every option starts with a checkbox glyph.
func allCheckboxes(choices []Choice) bool {
	for _, c := range choices {
		if !hasCheckbox(c.Label) {
			return false
		}
	}
	return len(choices) > 0
}

func hasCheckbox(label string) bool {
	return stripCheckbox(label) != label
}

// stripCheckbox removes a leading checkbox glyph from an option label.
func stripCheckbox(label string) string {
	if loc := checkboxGlyph.FindStringIndex(label); loc != nil {
		return strings.TrimSpace(label[loc[1]:])
	}
	return label
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

// hasCursor reports whether the line carries the ❯ cursor.
func hasCursor(line string) bool {
	return strings.HasPrefix(strings.TrimLeft(line, " \t"), "❯")
}

// isSubmitRow reports whether the line is the bare "Submit" row of a
// multi-select dialog, with or without the cursor in front of it.
func isSubmitRow(line string) bool {
	trimmed := strings.TrimSpace(line)
	return trimmed == submitLabel || strings.TrimSpace(strings.TrimPrefix(trimmed, "❯")) == submitLabel && strings.HasPrefix(trimmed, "❯")
}

// isChoiceFiller reports whether a non-item line may sit inside or after
// the dialog block: blank, a rule, an indented description (Claude Code
// indents option descriptions by two spaces in a multi-select dialog and
// by five in a single-select one) or a footer such as "Enter to select ·
// ↑/↓ to navigate · Esc to cancel".
func isChoiceFiller(line string) bool {
	trimmed := strings.TrimSpace(line)
	switch {
	case trimmed == "":
		return true
	case isChoiceRule(trimmed):
		return true
	case strings.HasPrefix(line, "  "):
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
