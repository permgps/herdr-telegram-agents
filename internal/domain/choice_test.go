package domain_test

import (
	"reflect"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/domain"
)

// measuredDialog is the AskUserQuestion screen read from a live Claude Code
// pane on 2026-09-03 (trailing spaces removed), preceded by transcript text
// that itself contains a numbered list.
const measuredDialog = `⏺ Причина найдена. Варианты:
  1. Добавить импорт
  2. Переписать экшен

❯ Тест интерфейса: задай мне один вопрос инструментом AskUserQuestion
────────────────────────────────────────────────────────────────────────
 ☐ Цвет

Какой цвет выбрать?

❯ 1. Красный
     Тёплый, яркий, привлекает внимание
  2. Зелёный
     Спокойный, ассоциируется с природой
  3. Синий
     Холодный, строгий, деловой
  4. Type something.
────────────────────────────────────────────────────────────────────────
  5. Chat about this

Enter to select · ↑/↓ to navigate · Esc to cancel`

func TestParseChoices(t *testing.T) {
	colours := []domain.Choice{{1, "Красный"}, {2, "Зелёный"}, {3, "Синий"}}
	cases := []struct {
		name   string
		screen string
		want   []domain.Choice
	}{
		{"measured dialog", measuredDialog, colours},
		{"cursor on the second item", "Q?\n\n  1. Red\n❯ 2. Green\n  3. Blue\n", []domain.Choice{{1, "Red"}, {2, "Green"}, {3, "Blue"}}},
		{"approval dialog",
			"Bash(rm -rf build)\n\nDo you want to proceed?\n❯ 1. Yes\n  2. Yes, and don't ask again for this session\n  3. No, and tell Claude what to do differently (esc)\n▔▔▔▔▔▔▔▔▔▔▔▔\n",
			[]domain.Choice{{1, "Yes"}, {2, "Yes, and don't ask again for this session"}, {3, "No, and tell Claude what to do differently (esc)"}}},
		{"list followed by a y/n prompt", "Plan:\n  1. Add the import\n  2. Run the tests\n\nDo you want to continue? (y/n)", nil},
		{"five real options plus service items",
			"❯ 1. A\n  2. B\n  3. C\n  4. D\n  5. E\n  6. Type something.\n  7. Chat about this\n\nEnter to select · Esc to cancel",
			[]domain.Choice{{1, "A"}, {2, "B"}, {3, "C"}, {4, "D"}, {5, "E"}}},
		{"six real options", "❯ 1. A\n  2. B\n  3. C\n  4. D\n  5. E\n  6. F\n", nil},
		{"gap in numbering", "❯ 1. A\n  2. B\n  4. D\n", nil},
		{"only service items", "  1. Type something.\n  2. Chat about this\n", nil},
		{"one real option", "❯ 1. Only\n  2. Type something.\n  3. Chat about this\n", nil},
		{"multi-select marker kept", "❯ 1. ☐ Red\n  2. ☑ Green\n  3. ☐ Blue\n\nSpace to toggle · Enter to submit", []domain.Choice{{1, "☐ Red"}, {2, "☑ Green"}, {3, "☐ Blue"}}},
		{"descriptions between items", "  1. A\n     first\n     more\n  2. B\n     second\n", []domain.Choice{{1, "A"}, {2, "B"}}},
		{"box rule between items", "  1. A\n  2. B\n━━━━━━━━━━━━\n  3. C\n", []domain.Choice{{1, "A"}, {2, "B"}, {3, "C"}}},
		{"short rule is foreign text", "  1. A\n  2. B\n───\nsomething", nil},
		{"trailing prose rejects", "  1. A\n  2. B\nType your answer:", nil},
		{"crlf screen", "  1. A\r\n  2. B\r\n", []domain.Choice{{1, "A"}, {2, "B"}}},
		{"no numbers", "just text\n❯ ", nil},
		{"empty", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.ParseChoices(tc.screen)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseChoices() = %v, want %v", got, tc.want)
			}
		})
	}
}
