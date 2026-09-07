package domain_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// The frame read from this pane on 2026-09-07 with `herdr agent read
// --source visible`, rules shortened to 40 characters.
const (
	chromeRule   = "────────────────────────────────────────"
	chromeStatus = "  …/Projects/My/herdr_tg │ main ✓ │ 14%: 121k[▓░░░░░░░░░]713k │ $4.37 │ 5h 3%[░░░░░░░░░░]4h19m │ 7d 14%[▓░░░░░░░░░]5d10h           /rc"
	chromeHint   = "  ⏵⏵ auto mode on (shift+tab to cycle) · ← 2 agents"
	transcript   = "⏺ Done. The tests pass.\n\n  Ran 2 shell commands"
)

func frame(status, hint string) string {
	return strings.Join([]string{chromeRule, "❯", chromeRule, status, hint}, "\n")
}

func TestCutChrome(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		want  string
		lines int
	}{
		{"real frame", transcript + "\n" + frame(chromeStatus, chromeHint), transcript, 5},
		{"default permission mode hint", transcript + "\n" + frame(chromeStatus, "? for shortcuts"), transcript, 5},
		{"ascii bars in the status line", transcript + "\n" + frame("  ~/x | main | 3%", chromeHint), transcript, 5},
		{"hint only", transcript + "\n" + chromeHint, transcript, 1},
		{"status and hint under a dialog", measuredDialog + "\n" + chromeStatus + "\n" + chromeHint, measuredDialog, 2},
		{"typed prompt keeps the box", transcript + "\n" + chromeRule + "\n❯ half typed\n" + chromeRule, transcript + "\n" + chromeRule + "\n❯ half typed\n" + chromeRule, 0},
		{"typed prompt still loses the lines under it", transcript + "\n" + chromeRule + "\n❯ half typed\n" + chromeRule + "\n" + chromeStatus + "\n" + chromeHint, transcript + "\n" + chromeRule + "\n❯ half typed\n" + chromeRule, 2},
		{"codex bottom", "codex reply\n\n› ", "codex reply\n\n› ", 0},
		{"plain transcript", transcript, transcript, 0},
		{"status line alone is transcript", transcript + "\n" + chromeStatus, transcript + "\n" + chromeStatus, 0},
		{"box without status or hint", transcript + "\n" + chromeRule + "\n❯\n" + chromeRule, transcript, 3},
		{"short rule is not the box", transcript + "\n───\n❯\n───\n" + chromeHint, transcript + "\n───\n❯\n───", 1},
		{"empty", "", "", 0},
		{"trailing blank lines after the hint", transcript + "\n" + frame(chromeStatus, chromeHint) + "\n  \n\n", transcript, 5},
		{"trailing spaces on the frame lines", transcript + "\n" + chromeRule + "  \n❯   \n" + chromeRule + " \n" + chromeStatus + " \n" + chromeHint + "  ", transcript, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, n := domain.CutChrome(tc.text)
			if got != tc.want || n != tc.lines {
				t.Fatalf("CutChrome() = %q, %d; want %q, %d", got, n, tc.want, tc.lines)
			}
		})
	}
}

// TestCutChromeKeepsTheDialog guards the promise that a question's options
// survive the cut: the dialog under the status line and the hint parses
// exactly as the fixture does.
func TestCutChromeKeepsTheDialog(t *testing.T) {
	got, n := domain.CutChrome(measuredDialog + "\n" + chromeStatus + "\n" + chromeHint)
	if n != 2 {
		t.Fatalf("cut %d lines, want 2", n)
	}
	want := domain.ParseDialog(measuredDialog)
	if len(want.Choices) != 3 || want.TextEntry != 4 {
		t.Fatalf("fixture parses to %+v", want)
	}
	if d := domain.ParseDialog(got); !reflect.DeepEqual(d, want) {
		t.Fatalf("dialog after cut = %+v, want %+v", d, want)
	}
	if _, n := domain.CutChrome(measuredMultiDialog); n != 0 {
		t.Fatalf("the multi-select fixture lost %d lines", n)
	}
}
