package domain_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// claudeScreen reads a Claude Code screen captured live on 2026-10-09
// (Claude Code 2.1.295, paths replaced by /tmp/demo).
func claudeScreen(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "claude", name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTextEntryOpenOnLiveScreens(t *testing.T) {
	question := domain.ParseDialog(claudeScreen(t, "question"))
	if !question.TypedAnswerable() || question.TextEntry != 4 || question.Cursor != 1 {
		t.Fatalf("question = %+v", question)
	}
	for name, want := range map[string]bool{
		// The cursor on option 1, no edit hint: a typed character would be
		// a key press in the option list.
		"question": false,
		// After the entry's digit: cursor on the entry, edit hint shown.
		"question-entry-open": true,
		// Text already typed replaces the label: no longer the same dialog,
		// so nothing is typed on top of an answer in progress.
		"question-entry-typed": false,
		// A permission prompt: no free-text entry at all.
		"permission": false,
	} {
		if got := domain.TextEntryOpen(claudeScreen(t, name), question); got != want {
			t.Errorf("TextEntryOpen(%s) = %v, want %v", name, got, want)
		}
	}
}

func TestTextEntryOpenRefusesALookAlike(t *testing.T) {
	question := domain.ParseDialog(claudeScreen(t, "question"))
	open := claudeScreen(t, "question-entry-open")
	// A replacement question with the same shape but other labels.
	if domain.TextEntryOpen(strings.Replace(open, "2. Green", "2. Purple", 1), question) {
		t.Error("a different question passed as the open entry")
	}
	// The edit hint quoted in the transcript, not in the footer.
	moved := strings.Replace(open, "ctrl+g to edit in Nvim · ", "", 1) + "\n  see ctrl+g to edit in the docs\nEnter to select · Esc to cancel"
	if domain.TextEntryOpen(moved, question) {
		t.Error("an edit hint outside the footer passed")
	}
	if domain.TextEntryOpen("", question) {
		t.Error("a blank screen passed")
	}
}

func TestTypedAnswerableOnlyForTypeSomething(t *testing.T) {
	if domain.ParseDialog(claudeScreen(t, "permission")).TypedAnswerable() {
		t.Error("a permission prompt is answerable by typing")
	}
	multi := domain.Dialog{Choices: []domain.Choice{{Number: 1, Label: "a"}}, Multi: true, TextEntry: 2, TextLabel: domain.ServiceTypeSomething}
	if multi.TypedAnswerable() {
		t.Error("a multi-select dialog is answerable by typing")
	}
	chat := domain.Dialog{Choices: []domain.Choice{{Number: 1, Label: "a"}}, TextEntry: 2, TextLabel: domain.ServiceChatAboutThis}
	if chat.TypedAnswerable() {
		t.Error("Chat about this is answerable by typing")
	}
}
