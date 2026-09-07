package domain

import (
	"testing"
	"time"
)

func TestTurnMetaLine(t *testing.T) {
	start := time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)
	at := func(d time.Duration) time.Time { return start.Add(d) }
	for _, tc := range []struct {
		name string
		meta TurnMeta
		want string
	}{
		{"all four parts", TurnMeta{Model: "claude-fable-5-1", Started: start, Ended: at(4 * time.Minute), Files: []string{"a", "b", "c"}, OutputTokens: 12000}, "⏱ 4 min · fable-5-1 · ✏️ 3 files · ↑ 12k tokens"},
		{"duration only", TurnMeta{Started: start, Ended: at(4 * time.Minute)}, "⏱ 4 min"},
		{"seconds", TurnMeta{Started: start, Ended: at(42 * time.Second)}, "⏱ 42 s"},
		{"rounded down to a minute", TurnMeta{Started: start, Ended: at(80 * time.Second)}, "⏱ 1 min"},
		{"rounded up to two minutes", TurnMeta{Started: start, Ended: at(100 * time.Second)}, "⏱ 2 min"},
		{"hours", TurnMeta{Started: start, Ended: at(72 * time.Minute)}, "⏱ 1 h 12 min"},
		{"whole hours", TurnMeta{Started: start, Ended: at(2 * time.Hour)}, "⏱ 2 h"},
		{"ended before started", TurnMeta{Started: at(time.Minute), Ended: start, Model: "gpt-5"}, "gpt-5"},
		{"only started", TurnMeta{Started: start, OutputTokens: 954}, "↑ 954 tokens"},
		{"one file", TurnMeta{Files: []string{"a"}}, "✏️ 1 file"},
		{"thousands", TurnMeta{OutputTokens: 4600}, "↑ 4.6k tokens"},
		{"tens of thousands", TurnMeta{OutputTokens: 46000}, "↑ 46k tokens"},
		{"just under ten thousand", TurnMeta{OutputTokens: 9960}, "↑ 10k tokens"},
		{"zero tokens", TurnMeta{OutputTokens: 0, Model: "claude-haiku-4-5-20251001"}, "haiku-4-5"},
		{"nothing known", TurnMeta{}, ""},
	} {
		if got := tc.meta.Line(); got != tc.want {
			t.Errorf("%s: Line() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestShortModel(t *testing.T) {
	for in, want := range map[string]string{
		"claude-fable-5-1":          "fable-5-1",
		"claude-haiku-4-5-20251001": "haiku-4-5",
		"claude-sonnet-4-20250514":  "sonnet-4",
		"gpt-5":                     "gpt-5",
		"o3-20250101":               "o3",
		"model-1234567":             "model-1234567",
		" claude-opus-5 ":           "opus-5",
		"":                          "",
	} {
		if got := ShortModel(in); got != want {
			t.Errorf("ShortModel(%q) = %q, want %q", in, got, want)
		}
	}
}
