package domain

import "testing"

// stuckScreen holds a prompt typed twice, both times left in the box: the
// second send landed after the first, wrapped over three lines each.
const stuckScreen = `● Compacted while idle, before the prompt cache expired · turn off with "idleCompaction": false in settings.json
──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
❯ 1. Why do the admins need roles at all? Isn't that a design flaw for the admin side? For example, locally I have the
  address admin@example.com set as admin, so when this user opens the admin area, do they get errors on the
  admin screens?
  1. Why do the admins need roles at all? Isn't that a design flaw for the admin side? For example, locally I have the
  address admin@example.com set as admin, so when this user opens the admin area, do they get errors on the
  admin screens?
──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
     work  project  backend   fix/admin-permission-guard  Opus 5.5 …
  ⏵⏵ auto mode on (shift+tab to cycle)`

const submittedScreen = `❯ 1. Why do the admins need roles at all? Isn't that a design flaw for the admin side? For example, locally I have the
  address admin@example.com set as admin, so when this user opens the admin area, do they get errors on the
  admin screens?
✽ Symbioting… (2s · thinking with high effort)
─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
❯
─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle) · ← 1 agent`

const suggestionScreen = `● OK
✻ Cooked for 1s · done 10:16 PM
──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
❯ Burst test three: answer only OK.
──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle) · ← 1 agent`

// twoLineScreen is a two-line message typed as one line and left in the
// box, with a blank line inside the box under it.
const twoLineScreen = `✻ Baked for 1m 12s · done 2:01 PM
  ⎿  Read load/run_matrix.py (62 lines)

● Compacted while idle, before the prompt cache expired · turn off with "idleCompaction": false in settings.json

──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
❯ Yeah, let's delete it Let's add the message to the team in the docs

──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
     work  project  Opus 5.5 · high  ctx 0%
  ⏵⏵ auto mode on (shift+tab to cycle)`

func TestPromptStuck(t *testing.T) {
	const typed = "1. Why do the admins need roles at all? Isn't that a design flaw for the admin side? For example, locally I have the address admin@example.com set as admin, so when this user opens the admin area, do they get errors on the admin screens?"
	tests := []struct {
		name   string
		screen string
		line   string
		want   bool
	}{
		{"typed text still in the box", stuckScreen, typed, true},
		{"line breaks folded before typing", stuckScreen, "1. Why do the admins need roles at all? Isn't that a design flaw for the admin side?\nFor example, locally", true},
		{"two typed lines still in the box", twoLineScreen, "Yeah, let's delete it\nLet's add the message to the team in the docs", true},
		{"submitted: echoed above, box empty", submittedScreen, typed, false},
		{"suggestion in the empty box", suggestionScreen, "Burst test: answer only OK.", false},
		{"suggestion equal to a short line", suggestionScreen, "Burst test three: answer only OK.", true},
		{"other text in the box", stuckScreen, "something else entirely was typed here", false},
		{"no prompt marker", "  > waiting\n  ok\n", typed, false},
		{"empty line", stuckScreen, "   ", false},
		{"marker without text", "❯\n────\n", typed, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PromptStuck(tt.screen, tt.line); got != tt.want {
				t.Fatalf("PromptStuck = %v, want %v", got, tt.want)
			}
		})
	}
}
