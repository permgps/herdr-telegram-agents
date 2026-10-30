package domain_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// piQuestionnaireDialog is a pi-questionnaire AskUserQuestion screen read from
// a live pi pane under Herdr on 2026-10-26: a boxed dialog with a 👉 cursor,
// key hints and status lines inside the box, and pi's footer under it.
const piQuestionnaireDialog = ` AskUserQuestion 1 question (Relay test)


┌─ AskUserQuestion ───────────────────────────────────────────────────────────┐
│Relay test                                                                   │
│Telegram relay test: pick a colour                                           │
│                                                                             │
│👉 1. Red                                                                    │
│     Pick the colour red                                                     │
│   2. Green                                                                  │
│     Pick the colour green                                                   │
│   3. Blue                                                                   │
│     Pick the colour blue                                                    │
│   4. Other                                                                  │
│     Provide a custom answer                                                 │
│                                                                             │
│↑/↓ navigate  Enter select  1-9 quick  Tab notes  e preview  o browser  ? help│
│                                                                             │
│⏱  2s elapsed                                                                │
│🌐 browser sync unavailable                                                  │
└─────────────────────────────────────────────────────────────────────────────┘
~/projects
↑26k ↓120 $0.004 2.6%/1.0M (auto)                     (zai) glm-5.3-flash • max
herdr 0.9.5`

func TestParseDialogFramedPiQuestionnaire(t *testing.T) {
	d := domain.ParseDialog(piQuestionnaireDialog)
	want := []domain.Choice{{Number: 1, Label: "Red"}, {Number: 2, Label: "Green"}, {Number: 3, Label: "Blue"}}
	if !reflect.DeepEqual(d.Choices, want) {
		t.Fatalf("choices = %#v, want %#v", d.Choices, want)
	}
	if d.Cursor != 1 || d.Multi || d.TextEntry != 4 || d.TextLabel != domain.ServiceOther || !d.TypedAnswerable() {
		t.Fatalf("dialog = %+v, want cursor 1, text entry 4 Other, typed-answerable", d)
	}
	if domain.TextEntryOpen(piQuestionnaireDialog, d) {
		t.Fatal("TextEntryOpen = true before the Other entry was opened")
	}
}

// After the Other digit: the cursor sits on Other and its editor line shows.
func TestTextEntryOpenPiQuestionnaireOther(t *testing.T) {
	want := domain.ParseDialog(piQuestionnaireDialog)
	open := strings.Replace(piQuestionnaireDialog, "│👉 1. Red", "│   1. Red", 1)
	open = strings.Replace(open, "│   4. Other                                                                  │\n│     Provide a custom answer                                                 │",
		"│👉 4. Other                                                                  │\n│     Provide a custom answer                                                 │\n│     Other: (type a custom answer)▏                                         │", 1)
	if !domain.TextEntryOpen(open, want) {
		t.Fatalf("TextEntryOpen = false for the open Other editor:\n%s", open)
	}
}

// A Claude Code dialog outside a frame keeps "Other" as a real option.
func TestParseDialogUnframedOtherStaysOption(t *testing.T) {
	screen := "Pick one\n❯ 1. Red\n  2. Other\n  3. Type something.\nEnter to select · ↑/↓ to navigate · Esc to cancel"
	d := domain.ParseDialog(screen)
	if len(d.Choices) != 2 || d.Choices[1].Label != "Other" || d.TextLabel != domain.ServiceTypeSomething {
		t.Fatalf("dialog = %+v, want Red + Other options and the Type something entry", d)
	}
}

// A split-pane frame (pi-ask-user) draws a column border after the label.
func TestParseDialogFramedSplitPane(t *testing.T) {
	screen := "╭─ ask_user ─────────────────────────────╮\n" +
		"│  Pick a colour                          │\n" +
		"│ → 1. Red            │ The colour red    │\n" +
		"│   2. Green          │                   │\n" +
		"│   3. Blue           │ Press Enter       │\n" +
		"╰─────────────────────────────────────────╯\n" +
		"~/projects"
	want := []domain.Choice{{Number: 1, Label: "Red"}, {Number: 2, Label: "Green"}, {Number: 3, Label: "Blue"}}
	if got := domain.ParseDialog(screen).Choices; !reflect.DeepEqual(got, want) {
		t.Fatalf("choices = %#v, want %#v", got, want)
	}
}

// A numbered list in a box that is followed by more numbered transcript
// inside the same box is not a dialog: items after the tail are rejected.
func TestParseDialogFramedRejectsItemsAfterTail(t *testing.T) {
	screen := "┌──────────────┐\n│1. A          │\n│2. B          │\n│some text     │\n│3. C          │\n└──────────────┘"
	if got := domain.ParseDialog(screen).Choices; len(got) != 0 {
		t.Fatalf("choices = %#v, want none", got)
	}
}

// A markdown table Claude Code draws with a numbered first column is not a
// dialog, even when it is the last box on the screen.
func TestParseDialogFramedRejectsTable(t *testing.T) {
	screen := "┌──────────┬────────┐\n" +
		"│ Step     │ Status │\n" +
		"├──────────┼────────┤\n" +
		"│ 1. Build │ done   │\n" +
		"│ 2. Test  │ done   │\n" +
		"│ 3. Other │ todo   │\n" +
		"└──────────┴────────┘\n" +
		"❯ "
	if got := domain.ParseDialog(screen).Choices; len(got) != 0 {
		t.Fatalf("choices = %#v, want none", got)
	}
}

// Only the free-text entry read live for an agent kind may take a typed
// answer: "Other" in a box is pi-questionnaire's editor, but a plain option
// for Claude Code.
func TestTypedAnswerableBy(t *testing.T) {
	pi := domain.ParseDialog(piQuestionnaireDialog)
	claude := domain.Dialog{Choices: []domain.Choice{{Number: 1, Label: "Red"}}, TextEntry: 2, TextLabel: domain.ServiceTypeSomething}
	for _, tc := range []struct {
		name string
		d    domain.Dialog
		kind string
		want bool
	}{
		{"pi Other", pi, domain.PiKind, true},
		{"Claude with a framed Other", pi, domain.ClaudeKind, false},
		{"Claude Type something", claude, domain.ClaudeKind, true},
		{"pi Type something", claude, domain.PiKind, false},
		{"other kind", claude, "codex", false},
	} {
		if got := tc.d.TypedAnswerableBy(tc.kind); got != tc.want {
			t.Errorf("%s: TypedAnswerableBy = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// For any kind but pi a framed "Other" goes back among the options in its
// place; pi keeps it as the free-text entry.
func TestDialogForKind(t *testing.T) {
	d := domain.Dialog{Choices: []domain.Choice{{Number: 1, Label: "Red"}, {Number: 3, Label: "Blue"}},
		TextEntry: 2, TextLabel: domain.ServiceOther, Cursor: 1}
	if got := d.ForKind(domain.PiKind); !reflect.DeepEqual(got, d) {
		t.Fatalf("pi: %+v, want unchanged", got)
	}
	got := d.ForKind(domain.ClaudeKind)
	want := []domain.Choice{{Number: 1, Label: "Red"}, {Number: 2, Label: "Other"}, {Number: 3, Label: "Blue"}}
	if !reflect.DeepEqual(got.Choices, want) || got.TextEntry != 0 || got.TextLabel != "" || got.Cursor != 1 {
		t.Fatalf("claude: %+v, want %v without an entry", got, want)
	}
	if len(d.Choices) != 2 {
		t.Fatalf("ForKind changed the original: %+v", d.Choices)
	}
	plain := domain.Dialog{Choices: want[:1], TextEntry: 2, TextLabel: domain.ServiceTypeSomething}
	if got := plain.ForKind(domain.ClaudeKind); !reflect.DeepEqual(got, plain) {
		t.Fatalf("Type something: %+v, want unchanged", got)
	}
}

// A box without a numbered list yields no dialog.
func TestParseDialogFramedWithoutList(t *testing.T) {
	screen := "┌──────────────┐\n│just a note   │\n└──────────────┘\n~/projects"
	if got := domain.ParseDialog(screen).Choices; len(got) != 0 {
		t.Fatalf("choices = %#v, want none", got)
	}
}
