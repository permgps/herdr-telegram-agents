package telegram

import (
	"strings"
	"testing"
)

func TestRenderMarkdown(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain", "just text", "just text"},
		{"escape", `a < b && c > d, "q"`, `a &lt; b &amp;&amp; c &gt; d, "q"`},
		{"heading", "## Title **x**", "<b>Title x</b>"},
		{"bold", "so **bold** here", "so <b>bold</b> here"},
		{"bold underscore", "so __bold__ here", "so <b>bold</b> here"},
		{"italic", "an *odd* one", "an <i>odd</i> one"},
		{"italic underscore", "an _odd_ one", "an <i>odd</i> one"},
		{"snake case untouched", "snake_case_name and 5 * 3", "snake_case_name and 5 * 3"},
		{"cyrillic underscores untouched", "слово_с_подчёркиванием", "слово_с_подчёркиванием"},
		{"strike", "~~gone~~", "<s>gone</s>"},
		{"bold italic", "***both***", "<b><i>both</i></b>"},
		{"italic never crosses a tag", "**x *y** z*", "<b>x *y</b> z*"},
		{"nul bytes dropped", "a \x000\x00 b `c`", "a 0 b <code>c</code>"},
		{"href quotes escaped", `[a](http://x"y)`, `<a href="http://x&quot;y">a</a>`},
		{"rule keeps its length", "---", mdRuleLine},
		{"inline code keeps markup", "run `a <b> && **x**` now", "run <code>a &lt;b&gt; &amp;&amp; **x**</code> now"},
		{"link", "see [docs](https://x.y/a?b=1&c=2)", `see <a href="https://x.y/a?b=1&amp;c=2">docs</a>`},
		{"link empty label", "[](https://x.y)", `<a href="https://x.y">https://x.y</a>`},
		{"bullets", "- one\n* two\n  - nested", "• one\n• two\n  • nested"},
		{"numbered untouched", "1. one\n2. two", "1. one\n2. two"},
		{"quote", "> said `x`", "│ said <code>x</code>"},
		{"rule", "a\n---\nb", "a\n" + mdRuleLine + "\nb"},
		{"fence with lang", "```go\nif a < b {}\n```", `<pre><code class="language-go">if a &lt; b {}</code></pre>`},
		{"fence no lang", "```\nx\n```", `<pre><code class="language-plaintext">x</code></pre>`},
		{"fence bad lang", "```$bad lang\nx\n```", `<pre><code class="language-plaintext">x</code></pre>`},
		{"tilde fence", "~~~sh\nls\n~~~", `<pre><code class="language-sh">ls</code></pre>`},
		{"unclosed fence", "```\nx\ny", "<pre><code class=\"language-plaintext\">x\ny</code></pre>"},
		{"table", "| a | b |\n|---|---|\n| 1 | 2 |", "<pre><code class=\"language-plaintext\">| a | b |\n|---|---|\n| 1 | 2 |</code></pre>"},
		{"blank lines kept", "a\n\nb", "a\n\nb"},
		{"edges trimmed", "\n\na\n\n", "a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := renderMarkdown(tt.in); got != tt.want {
				t.Fatalf("renderMarkdown(%q)\n got  %q\n want %q", tt.in, got, tt.want)
			}
		})
	}
}

// stressSample is the synthetic reply posted to the topic of the
// 2026-09-03 experiment; Telegram accepted the rendered result.
const stressSample = "## Итог проверки\n\nТесты **прошли**, но `make lint` ругается на *один* файл.\n\n- `internal/app/outbound.go` — неиспользуемый импорт\n- второй пункт с [ссылкой](https://core.telegram.org/bots/api#html-style)\n  - вложенный пункт\n\n1. первый шаг\n2. второй шаг\n\n> Цитата из лога: `queue: 429 retry_after=3`\n\n```go\nfunc chunk(text string, max int) []string {\n\tif text == \"\" { return nil }\n}\n```\n\n| Проверка | Итог |\n|---|---|\n| go test -race | ✓ |\n| staticcheck | ✗ |\n\n---\nСимволы, которые ломают HTML: a < b && c > d, \"кавычки\", 5 * 3, snake_case_name, __init__."

const stressWant = "<b>Итог проверки</b>\n\nТесты <b>прошли</b>, но <code>make lint</code> ругается на <i>один</i> файл.\n\n• <code>internal/app/outbound.go</code> — неиспользуемый импорт\n• второй пункт с <a href=\"https://core.telegram.org/bots/api#html-style\">ссылкой</a>\n  • вложенный пункт\n\n1. первый шаг\n2. второй шаг\n\n│ Цитата из лога: <code>queue: 429 retry_after=3</code>\n\n<pre><code class=\"language-go\">func chunk(text string, max int) []string {\n\tif text == \"\" { return nil }\n}</code></pre>\n\n<pre><code class=\"language-plaintext\">| Проверка | Итог |\n|---|---|\n| go test -race | ✓ |\n| staticcheck | ✗ |</code></pre>\n\n" + mdRuleLine + "\nСимволы, которые ломают HTML: a &lt; b &amp;&amp; c &gt; d, \"кавычки\", 5 * 3, snake_case_name, <b>init</b>."

func TestRenderMarkdownStressSample(t *testing.T) {
	if got := renderMarkdown(stressSample); got != stressWant {
		t.Fatalf("stress sample\n got:\n%s\n want:\n%s", got, stressWant)
	}
}

func TestSplitMarkdownKeepsFences(t *testing.T) {
	var b strings.Builder
	b.WriteString("intro\n```go\n")
	for i := 0; i < 40; i++ {
		b.WriteString("line of code number " + strings.Repeat("x", 20) + "\n")
	}
	b.WriteString("```\noutro")
	parts := splitMarkdown(b.String(), 400)
	if len(parts) < 3 {
		t.Fatalf("parts = %d, want several", len(parts))
	}
	for i, p := range parts {
		if utf16Len(p) > 400 {
			t.Errorf("part %d has %d units", i, utf16Len(p))
		}
		if i > 0 && i < len(parts)-1 && (!strings.HasPrefix(p, "```go\n") || !strings.HasSuffix(p, "\n```")) {
			t.Errorf("middle part %d not wrapped in a reopened fence: %q", i, p)
		}
		if strings.Count(p, "```")%2 != 0 {
			t.Errorf("part %d has an odd number of fences: %q", i, p)
		}
	}
	if !strings.HasPrefix(parts[0], "intro\n```go\n") || !strings.HasSuffix(parts[0], "\n```") {
		t.Errorf("first part = %q", parts[0])
	}
	if !strings.HasSuffix(parts[len(parts)-1], "```\noutro") {
		t.Errorf("last part = %q", parts[len(parts)-1])
	}
	if got := strings.Join(parts, "\n"); strings.Count(got, "line of code") != 40 {
		t.Errorf("lost code lines: %d", strings.Count(got, "line of code"))
	}
}

func TestSplitMarkdownNoDanglingFencePart(t *testing.T) {
	// The closing fence lands exactly at a seam: no part may consist of a
	// reopened fence and its closing marker only.
	text := "```\n" + strings.Repeat("a", 90) + "\n```\ntail"
	for _, p := range splitMarkdown(text, 100) {
		if fenceOnly(p) {
			t.Fatalf("dangling fence part %q", p)
		}
	}
}

func TestSplitMarkdownOpenerMovesToNextPart(t *testing.T) {
	// The opener would be the last line of the first part: it moves to the
	// second part, so no part ends with an empty code block and no part is
	// fence markers only.
	text := strings.Repeat("a", 90) + "\n```go\n" + strings.Repeat("b", 90) + "\n```"
	parts := splitMarkdown(text, 100)
	want := []string{strings.Repeat("a", 90), "```go\n" + strings.Repeat("b", 90) + "\n```"}
	if len(parts) != len(want) || parts[0] != want[0] || parts[1] != want[1] {
		t.Fatalf("parts = %q, want %q", parts, want)
	}
	// An opener as the very last line of the text is dropped, not sent as
	// an empty block.
	if got := splitMarkdown("text\n```go", 100); len(got) != 1 || got[0] != "text" {
		t.Fatalf("trailing opener = %q", got)
	}
	if got := renderMarkdown("text\n```go\n```\nmore"); got != "text\nmore" {
		t.Fatalf("empty block rendered: %q", got)
	}
	if got := splitMarkdown(strings.Repeat("a", 95)+"\n```go", 100); len(got) != 1 || got[0] != strings.Repeat("a", 95) {
		t.Fatalf("trailing opener across the seam = %q", got)
	}
}

func TestSplitMarkdownLongLineAfterOpener(t *testing.T) {
	// A long line right after a carried-over opener is cut so the part
	// stays within max even with the opener and the closing fence.
	text := strings.Repeat("a", 95) + "\n```json\n" + strings.Repeat("z", 250) + "\n```\n"
	for i, p := range splitMarkdown(text, 100) {
		if utf16Len(p) > 100 {
			t.Errorf("part %d has %d units: %q", i, utf16Len(p), p)
		}
	}
	if got := splitMarkdown("```json\n"+strings.Repeat("z", 30)+"\n```", 8); len(got) == 0 {
		t.Fatal("tiny max produced nothing")
	}
	// A fenced block whose body is fence-like lines is content, not a
	// dangling marker.
	if fenceOnly("```\n~~~~\n~~~~\n```") {
		t.Error("fenceOnly dropped a block with a fence-like body")
	}
	if !fenceOnly("```go\n```") {
		t.Error("fenceOnly kept an empty block")
	}
}

func TestSplitMarkdownLongLine(t *testing.T) {
	long := strings.Repeat("word ", 300)
	parts := splitMarkdown("start\n"+long+"\nend", 200)
	if len(parts) < 8 {
		t.Fatalf("parts = %d", len(parts))
	}
	for i, p := range parts {
		if utf16Len(p) > 200 {
			t.Errorf("part %d has %d units", i, utf16Len(p))
		}
	}
	joined := strings.Join(parts, "")
	if !strings.Contains(joined, "start") || !strings.HasSuffix(joined, "end") || strings.Count(joined, "word") != 300 {
		t.Errorf("content lost: %q", joined[:40])
	}
}

func TestSplitMarkdownUTF16(t *testing.T) {
	emoji := strings.Repeat("😀", 30) // 60 units
	parts := splitMarkdown(emoji+"\n"+emoji, 70)
	if len(parts) != 2 {
		t.Fatalf("parts = %v", parts)
	}
	for i, p := range parts {
		if utf16Len(p) > 70 || strings.ContainsRune(p, '�') {
			t.Errorf("part %d = %q (%d units)", i, p, utf16Len(p))
		}
	}
	if got := splitMarkdown("  \n", 10); got != nil {
		t.Errorf("blank text = %v", got)
	}
	if got := splitMarkdown("a\nb", 0); len(got) != 1 || got[0] != "a\nb" {
		t.Errorf("max 0 = %v", got)
	}
}
