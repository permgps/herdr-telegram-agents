package telegram

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Markdown rendering for Outgoing.Markdown: the agent's reply arrives as the
// Markdown it wrote, and Telegram accepts only a small HTML subset (b, i,
// s, a, code, pre). The renderer is deliberately regex-based and lossy:
// headings become bold, bullets become "•", quotes get a bar, tables stay
// monospaced. Anything it does not understand is escaped text, so the
// worst case is plain text, never a broken message. splitMarkdown cuts the
// text before rendering so no tag ever straddles two messages and an open
// code fence is closed on the seam and reopened in the next part.

var (
	mdFence     = regexp.MustCompile("^\\s{0,3}(`{3,}|~{3,})(.*)$")
	mdTableRule = regexp.MustCompile(`^\s*\|?[\s:|-]*-[\s:|-]*\|[\s:|-]*$`)
	mdHeading   = regexp.MustCompile(`^\s{0,3}#{1,6}\s+(.*?)\s*#*\s*$`)
	mdBullet    = regexp.MustCompile(`^(\s*)[-*+][ \t]+`)
	mdQuote     = regexp.MustCompile(`^\s{0,3}&gt;[ \t]?`)
	mdRule      = regexp.MustCompile(`^\s{0,3}[-*_](?:[ \t]*[-*_]){2,}[ \t]*$`)
	mdCode      = regexp.MustCompile("`([^`\n]+)`")
	mdLink      = regexp.MustCompile(`\[([^\]\n]*)\]\(([^)\s]+)\)`)
	mdBoldItal  = regexp.MustCompile(`\*\*\*([^*\n]+?)\*\*\*`)
	mdBold      = regexp.MustCompile(`(?s)\*\*(.+?)\*\*`)
	mdBoldAlt   = regexp.MustCompile(`(?s)(^|[^\pL\pN_])__(.+?)__([^\pL\pN_]|$)`)
	mdItalic    = regexp.MustCompile(`(^|[^\pL\pN_*])\*([^*\n<>]+?)\*([^\pL\pN_*]|$)`)
	mdItalicAlt = regexp.MustCompile(`(^|[^\pL\pN_])_([^_\n<>]+?)_([^\pL\pN_]|$)`)
	mdStrike    = regexp.MustCompile(`(?s)~~(.+?)~~`)
	mdStash     = regexp.MustCompile("\x00(\\d+)\x00")
	mdLang      = regexp.MustCompile(`^[A-Za-z0-9_+#.-]+$`)
)

// mdRuleLine replaces a horizontal rule; Telegram has no tag for one. It
// is three units long, like the shortest source rule, so rendering never
// makes a part longer than the Markdown splitMarkdown budgeted.
const mdRuleLine = "———"

// htmlEscaper escapes only what Telegram's HTML parser requires in text.
// Quotes stay as they are: html.EscapeString would turn them into numeric
// entities that are correct but noisy in the fallback log lines.
var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// attrEscaper additionally escapes quotes, for the href attribute.
var attrEscaper = strings.NewReplacer("\"", "&quot;", "'", "&#39;")

// mdBlock is one run of the text: fenced code, a pipe table, or prose.
type mdBlock struct {
	kind string // "code", "table" or "text"
	body string
	info string // fence info string for code
}

// renderMarkdown renders one already-split part into Telegram HTML.
func renderMarkdown(part string) string {
	var out []string
	for _, b := range mdBlocks(strings.ReplaceAll(part, "\r\n", "\n")) {
		switch b.kind {
		case "code", "table":
			if strings.TrimSpace(b.body) == "" {
				continue // an empty block renders as an empty <pre>; skip it
			}
			lang := "plaintext"
			if f := strings.Fields(b.info); b.kind == "code" && len(f) > 0 && mdLang.MatchString(f[0]) {
				lang = f[0]
			}
			out = append(out, `<pre><code class="language-`+lang+`">`+htmlEscaper.Replace(b.body)+"</code></pre>")
		default:
			out = append(out, renderInline(b.body))
		}
	}
	return strings.Trim(strings.Join(out, "\n"), "\n")
}

// mdBlocks splits Markdown into code, table and text runs. A fence without
// a closing marker runs to the end of the part (splitMarkdown never
// produces one, but a reply may).
func mdBlocks(text string) []mdBlock {
	lines := strings.Split(text, "\n")
	var blocks []mdBlock
	var plain []string
	flushPlain := func() {
		if len(plain) > 0 {
			blocks = append(blocks, mdBlock{kind: "text", body: strings.Join(plain, "\n")})
			plain = nil
		}
	}
	for i := 0; i < len(lines); {
		if m := mdFence.FindStringSubmatch(lines[i]); m != nil {
			flushPlain()
			marker, info := m[1], strings.TrimSpace(m[2])
			i++
			var body []string
			for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), marker) {
				body = append(body, lines[i])
				i++
			}
			i++ // closing fence, or past the end
			blocks = append(blocks, mdBlock{kind: "code", body: strings.Join(body, "\n"), info: info})
			continue
		}
		if strings.Contains(lines[i], "|") && i+1 < len(lines) && strings.Contains(lines[i+1], "|") && mdTableRule.MatchString(lines[i+1]) {
			flushPlain()
			var body []string
			for i < len(lines) && strings.Contains(lines[i], "|") {
				body = append(body, strings.TrimSpace(lines[i]))
				i++
			}
			blocks = append(blocks, mdBlock{kind: "table", body: strings.Join(body, "\n")})
			continue
		}
		plain = append(plain, lines[i])
		i++
	}
	flushPlain()
	return blocks
}

// renderInline renders prose: inline code is stashed first so escaping and
// emphasis never touch it, then the text is escaped, then line-level
// structure (rules, headings, quotes, bullets) and inline emphasis are
// applied, and the code spans are put back as <code>.
func renderInline(text string) string {
	// NUL is the placeholder delimiter; a reply carrying one would collide.
	text = strings.ReplaceAll(text, "\x00", "")
	var stash []string
	text = mdCode.ReplaceAllStringFunc(text, func(m string) string {
		stash = append(stash, m[1:len(m)-1])
		return "\x00" + strconv.Itoa(len(stash)-1) + "\x00"
	})
	text = htmlEscaper.Replace(text)

	lines := strings.Split(text, "\n")
	for i, ln := range lines {
		switch {
		case mdRule.MatchString(ln):
			lines[i] = mdRuleLine
		case mdHeading.MatchString(ln):
			head := mdHeading.FindStringSubmatch(ln)[1]
			lines[i] = "<b>" + strings.ReplaceAll(head, "**", "") + "</b>"
		default:
			ln = mdQuote.ReplaceAllString(ln, "│ ")
			lines[i] = mdBullet.ReplaceAllString(ln, "${1}• ")
		}
	}
	text = strings.Join(lines, "\n")

	text = mdLink.ReplaceAllStringFunc(text, func(m string) string {
		sm := mdLink.FindStringSubmatch(m)
		label := sm[1]
		if label == "" {
			label = sm[2]
		}
		return `<a href="` + attrEscaper.Replace(sm[2]) + `">` + label + "</a>"
	})
	text = mdBoldItal.ReplaceAllString(text, "<b><i>$1</i></b>")
	text = mdBold.ReplaceAllString(text, "<b>$1</b>")
	text = mdBoldAlt.ReplaceAllString(text, "${1}<b>$2</b>$3")
	text = mdItalic.ReplaceAllString(text, "${1}<i>$2</i>$3")
	text = mdItalicAlt.ReplaceAllString(text, "${1}<i>$2</i>$3")
	text = mdStrike.ReplaceAllString(text, "<s>$1</s>")
	return mdStash.ReplaceAllStringFunc(text, func(m string) string {
		n, err := strconv.Atoi(m[1 : len(m)-1])
		if err != nil || n < 0 || n >= len(stash) {
			return m
		}
		return "<code>" + htmlEscaper.Replace(stash[n]) + "</code>"
	})
}

// splitMarkdown cuts Markdown into parts of at most max UTF-16 code units
// so that each part renders on its own: a part never ends inside an open
// fence (the fence is closed at the seam and reopened, with its info
// string, at the top of the next part), a fence opener that would be the
// last line of a part moves to the next part instead, lines stay whole
// unless a single line is longer than a part, and parts that would hold
// nothing but fence markers are dropped.
func splitMarkdown(text string, max int) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if max <= 0 {
		return []string{text}
	}
	var parts, cur []string
	size := 0
	fence, info := "", ""
	openAt := -1 // index in cur of the line that opened the current fence, -1 if it came from an earlier part
	emit := func() {
		if p := strings.Join(cur, "\n"); strings.TrimSpace(p) != "" && !fenceOnly(p) {
			parts = append(parts, p)
		}
	}
	flush := func(reopen bool) {
		if reopen && fence != "" && openAt >= 0 && openAt == len(cur)-1 {
			// The fence was opened on the last line: carry the opener over
			// instead of leaving an empty block behind.
			opener := cur[openAt]
			cur = cur[:openAt]
			emit()
			cur, size, openAt = []string{opener}, utf16Len(opener), 0
			return
		}
		switch {
		case fence != "" && openAt >= 0 && openAt == len(cur)-1:
			// Final flush with an opener and nothing after it: drop it.
			cur = cur[:openAt]
		case fence != "":
			cur = append(cur, fence)
		}
		emit()
		cur, size, openAt = nil, 0, -1
		if reopen && fence != "" {
			cur = []string{fence + info}
			size = utf16Len(fence + info)
		}
	}
	// fresh reports whether cur holds nothing but a reopened fence.
	fresh := func() bool {
		return len(cur) == 0 || (len(cur) == 1 && fence != "" && openAt <= 0)
	}
	for _, line := range strings.Split(text, "\n") {
		room := max
		if fence != "" {
			room -= utf16Len(fence) + 1
		}
		if room < 1 {
			room = 1
		}
		var add int
		for {
			add = utf16Len(line)
			if len(cur) > 0 {
				add++ // the newline before it
			}
			if size+add <= room || line == "" {
				break
			}
			if !fresh() {
				// Start a new part (with the fence reopened or the opener
				// carried over) and see whether the line fits there.
				flush(true)
				continue
			}
			// A fresh part and the line still does not fit: cut it.
			avail := room - size
			if len(cur) > 0 {
				avail--
			}
			if avail < 1 {
				// Even a fresh part has no room (max smaller than a fence
				// marker): accept the overflow instead of looping.
				break
			}
			rs := []rune(line)
			n := fitUTF16(rs, avail)
			cur = append(cur, string(rs[:n]))
			flush(true)
			line = string(rs[n:])
		}
		if m := mdFence.FindStringSubmatch(line); m != nil {
			switch {
			case fence != "" && strings.HasPrefix(strings.TrimSpace(line), fence):
				fence, info, openAt = "", "", -1
			case fence == "":
				fence, info, openAt = m[1], strings.TrimSpace(m[2]), len(cur)
			}
		}
		cur = append(cur, line)
		size += add
	}
	flush(false)
	return parts
}

// fenceOnly reports whether a part holds nothing but fence markers (an
// opener carried over with nothing after it, or a reopened fence and its
// closing marker): such a part would render as an empty code block. Three
// or more marker lines are a fenced block whose body looks like fences,
// which is content.
func fenceOnly(part string) bool {
	markers := 0
	for _, ln := range strings.Split(part, "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		if !mdFence.MatchString(ln) {
			return false
		}
		markers++
	}
	return markers <= 2
}

// utf16Len is the length of s in UTF-16 code units, Telegram's measure.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if u := utf16.RuneLen(r); u > 0 {
			n += u
		} else {
			n++
		}
	}
	return n
}
