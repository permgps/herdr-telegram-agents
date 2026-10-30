package domain

import "strings"

// textEntryEditHint is the key hint Claude Code adds to a single-select
// question's footer while its "Type something." entry has the focus, the
// state in which typed characters land in that entry. Read live on
// 2026-10-09 (Claude Code 2.1.295): the footer reads "Enter to select ·
// ↑/↓ to navigate · Esc to cancel" with the cursor on an option and gains
// "· ctrl+g to edit in Nvim ·" once the entry's digit is pressed; typed
// text then replaces the entry's label in place, digits included, and
// enter submits it as the answer. The editor name varies, so only the
// prefix is matched.
const textEntryEditHint = "ctrl+g to edit"

// Same reports whether d and o are one dialog: the same options in the same
// order with the same labels, the same kind and the same free-text entry.
// A replacement question with the same shape is not the same dialog.
func (d Dialog) Same(o Dialog) bool {
	if len(d.Choices) != len(o.Choices) || d.Multi != o.Multi || d.TextEntry != o.TextEntry || d.TextLabel != o.TextLabel {
		return false
	}
	for i, c := range d.Choices {
		if c != o.Choices[i] {
			return false
		}
	}
	return true
}

// TypedAnswerable reports whether a plain message may answer d through its
// free-text entry: a single-select dialog with a "Type something" entry,
// the one shape whose typing state was verified on a real screen (see
// textEntryEditHint). A permission dialog has no such entry; a
// multi-select one toggles on a digit.
func (d Dialog) TypedAnswerable() bool {
	return d.TextEntry > 0 && !d.Multi && (d.TextLabel == ServiceTypeSomething || d.TextLabel == ServiceOther)
}

// TextEntryOpen reports whether screen shows the dialog want with its
// free-text entry focused, so typed text goes into that entry and not into
// an option list: the same dialog, the ❯ cursor on the entry row and
// Claude Code's edit hint on the last line. Any other screen (another
// dialog, a permission prompt, the entry still unfocused, a blank or
// unread screen) is not open. A "❯" alone proves nothing: Claude Code draws
// it before its input line and before the highlighted option of every
// dialog.
func TextEntryOpen(screen string, want Dialog) bool {
	if !want.TypedAnswerable() {
		return false
	}
	d := ParseDialog(screen)
	if !d.Same(want) || d.Cursor != want.TextEntry {
		return false
	}
	if want.TextLabel == ServiceOther {
		return otherEditorOpen(screen)
	}
	return strings.Contains(lastLine(screen), textEntryEditHint)
}

// otherEditorPrefix starts the inline editor line pi-questionnaire draws
// under its "Other" entry once that entry's digit is pressed ("Other:
// (type a custom answer)" while empty); typed text then goes into it and
// enter submits it. Read live on 2026-10-26 (pi-questionnaire 2.1.8).
const otherEditorPrefix = "Other:"

// otherEditorOpen reports whether the bottom-most dialog frame of screen
// shows pi-questionnaire's open "Other" editor.
func otherEditorOpen(screen string) bool {
	inner, ok := lastFrame(strings.Split(strings.ReplaceAll(screen, "\r\n", "\n"), "\n"))
	if !ok {
		return false
	}
	for _, line := range inner {
		if strings.HasPrefix(strings.TrimSpace(line), otherEditorPrefix) {
			return true
		}
	}
	return false
}

// lastLine returns the last non-blank line of text.
func lastLine(text string) string {
	lines := strings.Split(strings.TrimRight(text, " \t\r\n"), "\n")
	return lines[len(lines)-1]
}
