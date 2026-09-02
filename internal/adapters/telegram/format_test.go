package telegram

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunkBoundaries(t *testing.T) {
	exact := strings.Repeat("a", textMax)
	if got := chunk(exact, textMax); len(got) != 1 || got[0] != exact {
		t.Errorf("%d runes were split into %d parts", textMax, len(got))
	}

	// One rune over: a newline past the midpoint becomes the cut point and
	// is dropped.
	over := strings.Repeat("a", 3000) + "\n" + strings.Repeat("b", textMax-3000)
	got := chunk(over, textMax)
	if len(got) != 2 {
		t.Fatalf("got %d parts, want 2", len(got))
	}
	if got[0] != strings.Repeat("a", 3000) {
		t.Errorf("first part = %d runes ending %q", len(got[0]), got[0][len(got[0])-1:])
	}
	if got[1] != strings.Repeat("b", textMax-3000) {
		t.Errorf("second part = %d runes", utf8.RuneCountInString(got[1]))
	}

	// A newline only before the midpoint is ignored: hard cut at max.
	early := strings.Repeat("a", 10) + "\n" + strings.Repeat("b", textMax)
	got = chunk(early, textMax)
	if len(got) != 2 || utf8.RuneCountInString(got[0]) != textMax {
		t.Errorf("early newline: parts=%d first=%d runes", len(got), utf8.RuneCountInString(got[0]))
	}

	// No newline at all: hard cuts, nothing lost.
	long := strings.Repeat("x", textMax*2+5)
	got = chunk(long, textMax)
	if len(got) != 3 || strings.Join(got, "") != long {
		t.Errorf("no newline: %d parts, joined %d runes", len(got), utf8.RuneCountInString(strings.Join(got, "")))
	}

	if got := chunk("", textMax); got != nil {
		t.Errorf("empty text produced %v", got)
	}
}

func TestChunkKeepsRunesWhole(t *testing.T) {
	text := strings.Repeat("ж", 9) // 2 bytes each, 18 bytes
	got := chunk(text, 4)
	if len(got) != 3 {
		t.Fatalf("got %d parts, want 3", len(got))
	}
	for i, p := range got {
		if !utf8.ValidString(p) {
			t.Errorf("part %d is not valid UTF-8", i)
		}
	}
	if got[0] != "жжжж" || got[2] != "ж" {
		t.Errorf("parts = %q", got)
	}
	emoji := strings.Repeat("🏁", 5)
	if got := chunk(emoji, 2); len(got) != 3 || got[0] != "🏁🏁" || got[2] != "🏁" {
		t.Errorf("emoji parts = %q", got)
	}
}

func TestChunkDropsCutNewline(t *testing.T) {
	got := chunk("abc\ndef\nghi", 7)
	want := []string{"abc\ndef", "ghi"}
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("part %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRender(t *testing.T) {
	in := `<b>&"it's"`
	if got, want := renderPlain(in), "&lt;b&gt;&amp;&#34;it&#39;s&#34;"; got != want {
		t.Errorf("renderPlain = %q, want %q", got, want)
	}
	if got, want := renderCode("a < b"), "<pre>a &lt; b</pre>"; got != want {
		t.Errorf("renderCode = %q, want %q", got, want)
	}
}

func TestTruncateName(t *testing.T) {
	tests := []struct {
		in   string
		max  int
		want string
	}{
		{"", 10, "agent"},
		{"   ", 10, "agent"},
		{"  builder  ", 10, "builder"},
		{"⚙️ " + strings.Repeat("я", 20), 5, "⚙️ яя"},
		{"short", 128, "short"},
		{strings.Repeat("🏁", 130), 128, strings.Repeat("🏁", 128)},
	}
	for _, tc := range tests {
		if got := truncateName(tc.in, tc.max); got != tc.want {
			t.Errorf("truncateName(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
	}
}
